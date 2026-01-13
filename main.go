package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultModel = "claude-sonnet-4-5-20250929"
	anthropicURL = "https://api.anthropic.com/v1/messages"
)

var (
	modelFlag = flag.String("model", defaultModel, "Anthropic model to use")
	mFlag     = flag.String("m", "", "Anthropic model to use (shorthand)")
)

type AnthropicRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AnthropicResponse struct {
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type model struct {
	choices       []string
	cursor        int
	selected      map[int]struct{}
	commitTypes   []string
	typeSelected  int
	scopeInput    string
	phase         string
	diff          string
	needsAdd      bool
	generatedMsg  string
	errorMsg      string
}

func initialModel(diff string, needsAdd bool) model {
	commitTypes := []string{"feat", "fix", "docs", "style", "refactor", "perf", "test", "build", "ci", "chore"}

	phase := "type"
	if needsAdd {
		phase = "add"
	}

	return model{
		choices:      []string{"Yes, add all changes", "No, exit"},
		commitTypes:  commitTypes,
		typeSelected: 0,
		phase:        phase,
		diff:         diff,
		needsAdd:     needsAdd,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit

		case "up", "k":
			if m.phase == "add" && m.cursor > 0 {
				m.cursor--
			} else if m.phase == "type" && m.typeSelected > 0 {
				m.typeSelected--
			} else if m.phase == "push_prompt" && m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.phase == "add" && m.cursor < len(m.choices)-1 {
				m.cursor++
			} else if m.phase == "type" && m.typeSelected < len(m.commitTypes)-1 {
				m.typeSelected++
			} else if m.phase == "push_prompt" && m.cursor < len(m.choices)-1 {
				m.cursor++
			}

		case "enter":
			if m.phase == "add" {
				if m.cursor == 0 {
					if err := gitAdd(); err != nil {
						m.errorMsg = fmt.Sprintf("Error adding files: %v", err)
						return m, tea.Quit
					}
					diff, err := getGitDiff()
					if err != nil {
						m.errorMsg = fmt.Sprintf("Error getting diff: %v", err)
						return m, tea.Quit
					}
					m.diff = diff
					m.phase = "type"
				} else {
					return m, tea.Quit
				}
			} else if m.phase == "type" {
				m.phase = "scope"
			} else if m.phase == "scope" {
				m.phase = "generating"
				return m, generateCommitMsg(m.diff, m.commitTypes[m.typeSelected], m.scopeInput)
			} else if m.phase == "confirm" {
				if m.cursor == 0 {
					if err := gitCommit(m.generatedMsg); err != nil {
						m.errorMsg = fmt.Sprintf("Error committing: %v", err)
						return m, tea.Quit
					}
					m.phase = "push_prompt"
					m.cursor = 1
					m.choices = []string{"Yes, push", "No, skip"}
				} else {
					m.phase = "edit"
				}
			} else if m.phase == "edit" {
				if err := gitCommit(m.generatedMsg); err != nil {
					m.errorMsg = fmt.Sprintf("Error committing: %v", err)
					return m, tea.Quit
				}
				m.phase = "push_prompt"
				m.cursor = 1
				m.choices = []string{"Yes, push", "No, skip"}
			} else if m.phase == "push_prompt" {
				if m.cursor == 0 {
					if err := gitPush(); err != nil {
						m.errorMsg = fmt.Sprintf("Error pushing: %v", err)
						return m, tea.Quit
					}
				}
				return m, tea.Quit
			}

		case "backspace":
			if m.phase == "scope" && len(m.scopeInput) > 0 {
				m.scopeInput = m.scopeInput[:len(m.scopeInput)-1]
			} else if m.phase == "edit" && len(m.generatedMsg) > 0 {
				m.generatedMsg = m.generatedMsg[:len(m.generatedMsg)-1]
			}

		default:
			if m.phase == "scope" && len(msg.String()) == 1 {
				m.scopeInput += msg.String()
			} else if m.phase == "edit" {
				if msg.String() == "enter" {
					m.generatedMsg += "\n"
				} else if len(msg.String()) == 1 {
					m.generatedMsg += msg.String()
				}
			}
		}

	case commitMsgMsg:
		m.generatedMsg = string(msg)
		m.phase = "confirm"
		m.cursor = 0
		m.choices = []string{"Yes, commit", "No, let me edit"}

	case errMsg:
		m.errorMsg = string(msg)
		return m, tea.Quit
	}

	return m, nil
}

func (m model) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

	if m.errorMsg != "" {
		return fmt.Sprintf("Error: %s\n", m.errorMsg)
	}

	if m.phase == "add" {
		s := titleStyle.Render("No staged changes found. Would you like to add all changes?") + "\n\n"
		for i, choice := range m.choices {
			cursor := " "
			if m.cursor == i {
				cursor = ">"
				choice = selectedStyle.Render(choice)
			}
			s += fmt.Sprintf("%s %s\n", cursor, choice)
		}
		s += "\n(use arrow keys to select, enter to confirm, q to quit)\n"
		return s
	}

	if m.phase == "type" {
		s := titleStyle.Render("Select commit type:") + "\n\n"
		for i, commitType := range m.commitTypes {
			cursor := " "
			if m.typeSelected == i {
				cursor = ">"
				commitType = selectedStyle.Render(commitType)
			}
			s += fmt.Sprintf("%s %s\n", cursor, commitType)
		}
		s += "\n(use arrow keys to select, enter to confirm, q to quit)\n"
		return s
	}

	if m.phase == "scope" {
		s := titleStyle.Render(fmt.Sprintf("Enter scope for %s (press enter when done):", m.commitTypes[m.typeSelected])) + "\n\n"
		s += fmt.Sprintf("> %s_\n", m.scopeInput)
		return s
	}

	if m.phase == "generating" {
		return titleStyle.Render("Generating commit message...") + "\n"
	}

	if m.phase == "confirm" {
		s := titleStyle.Render("Generated commit message:") + "\n\n"
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render(m.generatedMsg) + "\n\n"
		s += titleStyle.Render("Use this message?") + "\n\n"
		for i, choice := range m.choices {
			cursor := " "
			if m.cursor == i {
				cursor = ">"
				choice = selectedStyle.Render(choice)
			}
			s += fmt.Sprintf("%s %s\n", cursor, choice)
		}
		s += "\n(use arrow keys to select, enter to confirm, q to quit)\n"
		return s
	}

	if m.phase == "edit" {
		s := titleStyle.Render("Edit commit message (press enter when done):") + "\n\n"
		s += fmt.Sprintf("%s_\n", m.generatedMsg)
		return s
	}

	if m.phase == "push_prompt" {
		s := titleStyle.Render("✓ Commit created successfully!") + "\n\n"
		s += titleStyle.Render("Push to remote?") + "\n\n"
		for i, choice := range m.choices {
			cursor := " "
			if m.cursor == i {
				cursor = ">"
				choice = selectedStyle.Render(choice)
			}
			s += fmt.Sprintf("%s %s\n", cursor, choice)
		}
		s += "\n(use arrow keys to select, enter to confirm, q to quit)\n"
		return s
	}

	if m.phase == "done" {
		return titleStyle.Render("✓ Done!") + "\n"
	}

	return ""
}

type commitMsgMsg string
type errMsg string

func generateCommitMsg(diff, commitType, scope string) tea.Cmd {
	return func() tea.Msg {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return errMsg("ANTHROPIC_API_KEY environment variable not set")
		}

		model := *modelFlag
		if *mFlag != "" {
			model = *mFlag
		}

		prompt := fmt.Sprintf(`You are a commit message generator. Based on the following git diff, generate a concise commit message using conventional commits format.

The commit type is: %s
The scope is: %s

Format: %s(%s): <description>

The description should be:
- Clear and concise (max 72 characters for the first line)
- In imperative mood (e.g., "add" not "added")
- Explain WHAT and WHY, not HOW

If the changes warrant it, you can add a body after a blank line with more details.

Git diff:
%s

Respond with ONLY the commit message, no explanations or markdown formatting.`, commitType, scope, commitType, scope, diff)

		reqBody := AnthropicRequest{
			Model:     model,
			MaxTokens: 1024,
			Messages: []Message{
				{
					Role:    "user",
					Content: prompt,
				},
			},
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return errMsg(fmt.Sprintf("Error marshaling request: %v", err))
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "POST", anthropicURL, bytes.NewBuffer(jsonData))
		if err != nil {
			return errMsg(fmt.Sprintf("Error creating request: %v", err))
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return errMsg(fmt.Sprintf("Error making request: %v", err))
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errMsg(fmt.Sprintf("Error reading response: %v", err))
		}

		if resp.StatusCode != http.StatusOK {
			return errMsg(fmt.Sprintf("API error (%d): %s", resp.StatusCode, string(body)))
		}

		var apiResp AnthropicResponse
		if err := json.Unmarshal(body, &apiResp); err != nil {
			return errMsg(fmt.Sprintf("Error parsing response: %v", err))
		}

		if len(apiResp.Content) == 0 {
			return errMsg("No content in API response")
		}

		commitMsg := strings.TrimSpace(apiResp.Content[0].Text)
		return commitMsgMsg(commitMsg)
	}
}

func getGitDiff() (string, error) {
	cmd := exec.Command("git", "diff", "--staged")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}
	return string(output), nil
}

func getGitStatus() (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(output) > 0, nil
}

func gitAdd() error {
	cmd := exec.Command("git", "add", ".")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}
	return nil
}

func gitCommit(message string) error {
	cmd := exec.Command("git", "commit", "-m", message)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, string(output))
	}
	return nil
}

func gitPush() error {
	cmd := exec.Command("git", "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %w\n%s", err, string(output))
	}
	return nil
}

func main() {
	flag.Parse()

	diff, err := getGitDiff()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting git diff: %v\n", err)
		os.Exit(1)
	}

	needsAdd := false
	if diff == "" {
		hasChanges, err := getGitStatus()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error checking git status: %v\n", err)
			os.Exit(1)
		}
		if !hasChanges {
			fmt.Println("No changes to commit.")
			os.Exit(0)
		}
		needsAdd = true
	}

	p := tea.NewProgram(initialModel(diff, needsAdd))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
