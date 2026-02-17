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
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultAnthropicModel = "claude-sonnet-4-5-20250929"
	defaultOllamaModel    = "llama3.2"
	defaultOllamaURL      = "http://localhost:11434"
	anthropicURL          = "https://api.anthropic.com/v1/messages"
	diffLineSizeLimit     = 1000 // Skip AI generation for diffs larger than this
)

// Config represents the application configuration
type Config struct {
	Provider    string `json:"provider"`               // "anthropic" or "ollama"
	Model       string `json:"model"`                  // Default model name (fallback)
	CommitModel string `json:"commit_model,omitempty"` // Model for commit message generation
	PRModel     string `json:"pr_model,omitempty"`     // Model for PR description generation
	OllamaURL   string `json:"ollama_url"`             // Ollama server URL
}

// GetCommitModel returns the model to use for commit message generation.
// Falls back to the default Model if CommitModel is not set.
func (c *Config) GetCommitModel() string {
	if c.CommitModel != "" {
		return c.CommitModel
	}
	return c.Model
}

// GetPRModel returns the model to use for PR description generation.
// Falls back to the default Model if PRModel is not set.
func (c *Config) GetPRModel() string {
	if c.PRModel != "" {
		return c.PRModel
	}
	return c.Model
}

var (
	modelFlag       = flag.String("model", "", "Model to use for both commit and PR (overrides config)")
	mFlag           = flag.String("m", "", "Model to use for both commit and PR (shorthand, overrides config)")
	commitModelFlag = flag.String("commit-model", "", "Model for commit message generation (overrides config)")
	prModelFlag     = flag.String("pr-model", "", "Model for PR description generation (overrides config)")
	providerFlag    = flag.String("provider", "", "LLM provider: anthropic or ollama (overrides config)")
	pFlag           = flag.String("p", "", "LLM provider (shorthand, overrides config)")
	ollamaURLFlag   = flag.String("ollama-url", "", "Ollama server URL (overrides config)")
	prFlag          = flag.Bool("pr", false, "Generate a PR from existing commits without committing")
	appConfig       *Config
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

// Ollama API types
type OllamaRequest struct {
	Model    string          `json:"model"`
	Messages []OllamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type OllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OllamaResponse struct {
	Model   string        `json:"model"`
	Message OllamaMessage `json:"message"`
}

// getConfigPath returns the path to the config file
func getConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".config", "gitcat", "config.json"), nil
}

// loadConfig loads the configuration from the config file
func loadConfig() (*Config, error) {
	configPath, err := getConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default config if file doesn't exist
			return &Config{
				Provider:  "anthropic",
				Model:     defaultAnthropicModel,
				OllamaURL: defaultOllamaURL,
			}, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set defaults for missing values
	if config.Provider == "" {
		config.Provider = "anthropic"
	}
	if config.Model == "" {
		if config.Provider == "ollama" {
			config.Model = defaultOllamaModel
		} else {
			config.Model = defaultAnthropicModel
		}
	}
	if config.OllamaURL == "" {
		config.OllamaURL = defaultOllamaURL
	}

	return &config, nil
}

// saveConfig saves the configuration to the config file
func saveConfig(config *Config) error {
	configPath, err := getConfigPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// getEffectiveConfig returns the config with CLI flag overrides applied
func getEffectiveConfig() *Config {
	config := *appConfig // Copy the config

	// Apply provider override
	provider := *providerFlag
	if *pFlag != "" {
		provider = *pFlag
	}
	if provider != "" {
		config.Provider = provider
	}

	// Apply model override: -m/--model overrides the default model for both
	model := *modelFlag
	if *mFlag != "" {
		model = *mFlag
	}
	if model != "" {
		config.Model = model
	}

	// Apply specific model overrides (take precedence over -m/--model)
	if *commitModelFlag != "" {
		config.CommitModel = *commitModelFlag
	}
	if *prModelFlag != "" {
		config.PRModel = *prModelFlag
	}

	// Apply Ollama URL override
	if *ollamaURLFlag != "" {
		config.OllamaURL = *ollamaURLFlag
	}

	return &config
}

type model struct {
	choices           []string
	cursor            int
	selected          map[int]struct{}
	commitTypes       []string
	typeSelected      int
	scopeInput        string
	phase             string
	diff              string
	needsAdd          bool
	generatedMsg      string
	errorMsg          string
	currentBranch     string
	prTitle           string
	prBody            string
	isProtectedBranch bool   // Track if on main/master
	branchInput       string // User input for branch name

	// Tracking completed actions for exit summary
	filesCommitted  int
	didCommit       bool
	didPush         bool
	didCreatePR     bool
	createdBranch   string // Non-empty if a new branch was created

	// API error context for retry capability
	apiErrorMsg string // Stores the API error message to display

	// PR-only mode (--pr flag)
	prOnly bool
}

func initialModel(diff string, needsAdd bool, currentBranch string, isProtectedBranch bool, prOnly bool) model {
	commitTypes := []string{"feat", "fix", "docs", "style", "refactor", "perf", "test", "build", "ci", "chore"}

	// Determine initial phase based on conditions
	phase := "type"
	choices := []string{"Yes, add all changes", "No, exit"}

	if prOnly {
		phase = "pr_generating"
	} else if isProtectedBranch {
		phase = "branch_warning"
		choices = []string{"Yes, create a new branch", fmt.Sprintf("No, continue on %s", currentBranch)}
	} else if needsAdd {
		phase = "add"
	}

	return model{
		choices:           choices,
		commitTypes:       commitTypes,
		typeSelected:      0,
		phase:             phase,
		diff:              diff,
		needsAdd:          needsAdd,
		currentBranch:     currentBranch,
		isProtectedBranch: isProtectedBranch,
		branchInput:       generateDefaultBranchName(),
		prOnly:            prOnly,
	}
}

func (m model) Init() tea.Cmd {
	if m.prOnly {
		return generatePRContent(m.currentBranch)
	}
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "q":
			// Only quit if not in an input phase where 'q' should be typed (e.g. model names like "qwen")
			if m.phase != "branch_input" && m.phase != "scope" && m.phase != "edit" && m.phase != "manual_input" && m.phase != "pr_manual_title" && m.phase != "pr_manual_body" {
				return m, tea.Quit
			}
			// Fall through to default handler for text input
			if m.phase == "branch_input" {
				m.branchInput += msg.String()
			} else if m.phase == "scope" {
				m.scopeInput += msg.String()
			} else if m.phase == "edit" || m.phase == "manual_input" {
				m.generatedMsg += msg.String()
			} else if m.phase == "pr_manual_title" {
				m.prTitle += msg.String()
			} else if m.phase == "pr_manual_body" {
				m.prBody += msg.String()
			}

		case "up", "k":
			// Only handle as navigation if not in input phase
			if m.phase != "branch_input" && m.phase != "scope" && m.phase != "edit" && m.phase != "manual_input" && m.phase != "pr_manual_title" && m.phase != "pr_manual_body" {
				if m.phase == "branch_warning" && m.cursor > 0 {
					m.cursor--
				} else if m.phase == "add" && m.cursor > 0 {
					m.cursor--
				} else if m.phase == "type" && m.typeSelected > 0 {
					m.typeSelected--
				} else if (m.phase == "push_prompt" || m.phase == "upstream_prompt" || m.phase == "pr_prompt" || m.phase == "confirm" || m.phase == "commit_error" || m.phase == "pr_error") && m.cursor > 0 {
					m.cursor--
				}
			} else if msg.String() == "k" && len(msg.String()) == 1 {
				// Allow typing 'k' in input phases
				if m.phase == "branch_input" {
					m.branchInput += msg.String()
				} else if m.phase == "scope" {
					m.scopeInput += msg.String()
				} else if m.phase == "edit" || m.phase == "manual_input" {
					m.generatedMsg += msg.String()
				} else if m.phase == "pr_manual_title" {
					m.prTitle += msg.String()
				} else if m.phase == "pr_manual_body" {
					m.prBody += msg.String()
				}
			}

		case "down", "j":
			// Only handle as navigation if not in input phase
			if m.phase != "branch_input" && m.phase != "scope" && m.phase != "edit" && m.phase != "manual_input" && m.phase != "pr_manual_title" && m.phase != "pr_manual_body" {
				if m.phase == "branch_warning" && m.cursor < len(m.choices)-1 {
					m.cursor++
				} else if m.phase == "add" && m.cursor < len(m.choices)-1 {
					m.cursor++
				} else if m.phase == "type" && m.typeSelected < len(m.commitTypes)-1 {
					m.typeSelected++
				} else if (m.phase == "push_prompt" || m.phase == "upstream_prompt" || m.phase == "pr_prompt" || m.phase == "confirm" || m.phase == "commit_error" || m.phase == "pr_error") && m.cursor < len(m.choices)-1 {
					m.cursor++
				}
			} else if msg.String() == "j" && len(msg.String()) == 1 {
				// Allow typing 'j' in input phases
				if m.phase == "branch_input" {
					m.branchInput += msg.String()
				} else if m.phase == "scope" {
					m.scopeInput += msg.String()
				} else if m.phase == "edit" || m.phase == "manual_input" {
					m.generatedMsg += msg.String()
				} else if m.phase == "pr_manual_title" {
					m.prTitle += msg.String()
				} else if m.phase == "pr_manual_body" {
					m.prBody += msg.String()
				}
			}

		case "enter":
			if m.phase == "branch_warning" {
				if m.cursor == 0 {
					// User wants to create new branch
					m.phase = "branch_input"
				} else {
					// User wants to continue on main/master
					// Move to next phase in normal flow
					if m.needsAdd {
						m.phase = "add"
						m.cursor = 0
						m.choices = []string{"Yes, add all changes", "No, exit"}
					} else {
						m.phase = "type"
					}
				}
			} else if m.phase == "branch_input" {
				// Validate branch name
				if err := validateBranchName(m.branchInput); err != nil {
					m.errorMsg = err.Error()
					return m, tea.Quit
				}
				// User submitted branch name
				m.phase = "branch_creating"
				return m, createAndCheckoutBranch(m.branchInput)
			} else if m.phase == "add" {
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
				// Check if diff is too large
				if isDiffTooLarge(m.diff) {
					m.phase = "manual_input"
					m.generatedMsg = "" // Start with empty message for manual input
				} else {
					m.phase = "generating"
					return m, generateCommitMsg(m.diff, m.commitTypes[m.typeSelected], m.scopeInput)
				}
			} else if m.phase == "confirm" {
				if m.cursor == 0 {
					m.filesCommitted = countStagedFiles()
					if err := gitCommit(m.generatedMsg); err != nil {
						m.errorMsg = fmt.Sprintf("Error committing: %v", err)
						return m, tea.Quit
					}
					m.didCommit = true
					m.phase = "push_prompt"
					m.cursor = 1
					m.choices = []string{"Yes, push", "No, skip"}
				} else {
					m.phase = "edit"
				}
			} else if m.phase == "edit" || m.phase == "manual_input" {
				m.filesCommitted = countStagedFiles()
				if err := gitCommit(m.generatedMsg); err != nil {
					m.errorMsg = fmt.Sprintf("Error committing: %v", err)
					return m, tea.Quit
				}
				m.didCommit = true
				m.phase = "push_prompt"
				m.cursor = 1
				m.choices = []string{"Yes, push", "No, skip"}
			} else if m.phase == "push_prompt" {
				if m.cursor == 0 {
					err := gitPush()
					if err != nil {
						errStr := err.Error()
						if strings.Contains(errStr, "no upstream branch") || strings.Contains(errStr, "has no upstream branch") {
							m.phase = "upstream_prompt"
							m.cursor = 0
							m.choices = []string{"Yes, set upstream and push", "No, skip"}
							return m, nil
						}
						m.errorMsg = fmt.Sprintf("Error pushing: %v", err)
						return m, tea.Quit
					}
					m.didPush = true
					// Check if PR already exists or if origin is not GitHub
					if err := isGitHubOrigin(); err != nil {
						m.phase = "exiting"
						return m, tea.Quit
					}
					if hasExistingPR(m.currentBranch) {
						m.phase = "exiting"
						return m, tea.Quit
					}
					m.phase = "pr_prompt"
					m.cursor = 1
					m.choices = []string{"Yes, create PR", "No, skip"}
					return m, nil
				}
				m.phase = "exiting"
				return m, tea.Quit
			} else if m.phase == "upstream_prompt" {
				if m.cursor == 0 {
					if err := gitPushSetUpstream(m.currentBranch); err != nil {
						m.errorMsg = fmt.Sprintf("Error setting upstream: %v", err)
						return m, tea.Quit
					}
					m.didPush = true
					// Check if PR already exists (GitHub origin already verified earlier)
					if hasExistingPR(m.currentBranch) {
						m.phase = "exiting"
						return m, tea.Quit
					}
					m.phase = "pr_prompt"
					m.cursor = 1
					m.choices = []string{"Yes, create PR", "No, skip"}
					return m, nil
				}
				m.phase = "exiting"
				return m, tea.Quit
			} else if m.phase == "pr_prompt" {
				if m.cursor == 0 {
					m.phase = "pr_generating"
					return m, generatePRContent(m.currentBranch)
				}
				m.phase = "exiting"
				return m, tea.Quit
			} else if m.phase == "commit_error" {
				if m.cursor == 0 {
					// Retry
					m.phase = "generating"
					m.apiErrorMsg = ""
					return m, generateCommitMsg(m.diff, m.commitTypes[m.typeSelected], m.scopeInput)
				} else {
					// Enter commit message manually
					m.phase = "manual_input"
					m.generatedMsg = ""
					m.apiErrorMsg = ""
				}
			} else if m.phase == "pr_error" {
				if m.cursor == 0 {
					// Retry
					m.phase = "pr_generating"
					m.apiErrorMsg = ""
					return m, generatePRContent(m.currentBranch)
				} else if m.cursor == 1 {
					// Enter PR details manually
					m.phase = "pr_manual_title"
					m.prTitle = ""
					m.prBody = ""
					m.apiErrorMsg = ""
				} else {
					// Skip PR creation
					m.phase = "exiting"
					m.apiErrorMsg = ""
					return m, tea.Quit
				}
			} else if m.phase == "pr_manual_title" {
				// Move to body input
				m.phase = "pr_manual_body"
			} else if m.phase == "pr_manual_body" {
				// Create the PR
				if err := createPR(m.prTitle, m.prBody); err != nil {
					m.errorMsg = fmt.Sprintf("Error creating PR: %v", err)
					return m, tea.Quit
				}
				m.didCreatePR = true
				m.phase = "pr_creating"
				return m, tea.Quit
			}

		case "backspace":
			if m.phase == "branch_input" && len(m.branchInput) > 0 {
				m.branchInput = m.branchInput[:len(m.branchInput)-1]
			} else if m.phase == "scope" && len(m.scopeInput) > 0 {
				m.scopeInput = m.scopeInput[:len(m.scopeInput)-1]
			} else if (m.phase == "edit" || m.phase == "manual_input") && len(m.generatedMsg) > 0 {
				m.generatedMsg = m.generatedMsg[:len(m.generatedMsg)-1]
			} else if m.phase == "pr_manual_title" && len(m.prTitle) > 0 {
				m.prTitle = m.prTitle[:len(m.prTitle)-1]
			} else if m.phase == "pr_manual_body" && len(m.prBody) > 0 {
				m.prBody = m.prBody[:len(m.prBody)-1]
			}

		default:
			if m.phase == "branch_input" && len(msg.String()) == 1 {
				m.branchInput += msg.String()
			} else if m.phase == "scope" && len(msg.String()) == 1 {
				m.scopeInput += msg.String()
			} else if m.phase == "edit" || m.phase == "manual_input" {
				if msg.String() == "enter" {
					m.generatedMsg += "\n"
				} else if len(msg.String()) == 1 {
					m.generatedMsg += msg.String()
				}
			} else if m.phase == "pr_manual_title" {
				if len(msg.String()) == 1 {
					m.prTitle += msg.String()
				}
			} else if m.phase == "pr_manual_body" {
				if msg.String() == "enter" {
					m.prBody += "\n"
				} else if len(msg.String()) == 1 {
					m.prBody += msg.String()
				}
			}
		}

	case commitMsgMsg:
		m.generatedMsg = string(msg)
		m.phase = "confirm"
		m.cursor = 0
		m.choices = []string{"Yes, commit", "No, let me edit"}

	case prContentMsg:
		parts := strings.SplitN(string(msg), "\n---BODY---\n", 2)
		if len(parts) == 2 {
			m.prTitle = parts[0]
			m.prBody = parts[1]
		} else {
			m.prTitle = string(msg)
			m.prBody = ""
		}
		if err := createPR(m.prTitle, m.prBody); err != nil {
			m.errorMsg = fmt.Sprintf("Error creating PR: %v", err)
			return m, tea.Quit
		}
		m.didCreatePR = true
		m.phase = "pr_creating"
		return m, tea.Quit

	case branchCreatedMsg:
		// Branch created successfully, update current branch name
		m.createdBranch = string(msg)
		m.currentBranch = string(msg)
		// Continue to normal flow
		if m.needsAdd {
			m.phase = "add"
			m.cursor = 0
			m.choices = []string{"Yes, add all changes", "No, exit"}
		} else {
			m.phase = "type"
		}

	case errMsg:
		m.errorMsg = string(msg)
		return m, tea.Quit

	case commitMsgErrMsg:
		m.apiErrorMsg = string(msg)
		m.phase = "commit_error"
		m.cursor = 0
		m.choices = []string{"Retry", "Enter commit message manually"}

	case prContentErrMsg:
		m.apiErrorMsg = string(msg)
		m.phase = "pr_error"
		m.cursor = 0
		m.choices = []string{"Retry", "Enter PR details manually", "Skip PR creation"}
	}

	return m, nil
}

func (m model) getSummary() string {
	// PR-only mode summary
	if m.prOnly && m.didCreatePR {
		return fmt.Sprintf("Created PR on branch %s", m.currentBranch)
	}

	if !m.didCommit {
		return ""
	}

	var parts []string

	// Files committed
	fileWord := "file"
	if m.filesCommitted != 1 {
		fileWord = "files"
	}
	parts = append(parts, fmt.Sprintf("Committed %d %s", m.filesCommitted, fileWord))

	// Branch info
	if m.createdBranch != "" {
		parts = append(parts, fmt.Sprintf("to new branch %s", m.createdBranch))
	} else {
		parts = append(parts, fmt.Sprintf("to branch %s", m.currentBranch))
	}

	// Push info
	if m.didPush {
		parts = append(parts, "and pushed")
	}

	// PR info
	if m.didCreatePR {
		parts = append(parts, "and created PR")
	}

	return strings.Join(parts, " ")
}

func (m model) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

	if m.errorMsg != "" {
		return fmt.Sprintf("Error: %s\n", m.errorMsg)
	}

	if m.phase == "branch_warning" {
		warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
		s := titleStyle.Render("⚠️  Warning: You are on a protected branch!") + "\n\n"
		s += warningStyle.Render(fmt.Sprintf("Current branch: %s", m.currentBranch)) + "\n\n"
		s += "Committing directly to main/master branches is not recommended.\n"
		s += "Would you like to create a new branch instead?\n\n"

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

	if m.phase == "branch_input" {
		s := titleStyle.Render("Enter new branch name:") + "\n\n"
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(fmt.Sprintf("Suggested: %s", generateDefaultBranchName())) + "\n\n"
		s += fmt.Sprintf("> %s_\n\n", m.branchInput)
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Tip: Use format like 'feature/description' or 'fix/issue-123'") + "\n"
		s += "\n(type branch name, enter to create, q to quit)\n"
		return s
	}

	if m.phase == "branch_creating" {
		return titleStyle.Render(fmt.Sprintf("Creating and switching to branch '%s'...", m.branchInput)) + "\n"
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

	if m.phase == "manual_input" {
		warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
		s := titleStyle.Render("⚠️  Large diff detected") + "\n\n"
		s += warningStyle.Render(fmt.Sprintf("The diff is too large (>%d lines) to send to the API.", diffLineSizeLimit)) + "\n"
		s += "Please enter your commit message manually:\n\n"
		s += fmt.Sprintf("%s(%s): %s_\n\n", m.commitTypes[m.typeSelected], m.scopeInput, m.generatedMsg)
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Tip: Follow conventional commits format") + "\n"
		s += "\n(type your message, press enter when done)\n"
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

	if m.phase == "upstream_prompt" {
		s := titleStyle.Render("No upstream branch configured.") + "\n\n"
		s += titleStyle.Render(fmt.Sprintf("Set upstream to 'origin/%s' and push?", m.currentBranch)) + "\n\n"
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

	if m.phase == "pr_prompt" {
		s := titleStyle.Render("Create a pull request?") + "\n\n"
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

	if m.phase == "pr_generating" {
		return titleStyle.Render("Generating PR title and body...") + "\n"
	}

	if m.phase == "commit_error" {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
		s := titleStyle.Render("⚠️  API Error") + "\n\n"
		s += errorStyle.Render("Failed to generate commit message:") + "\n"
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(m.apiErrorMsg) + "\n\n"
		s += titleStyle.Render("What would you like to do?") + "\n\n"
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

	if m.phase == "pr_error" {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
		s := titleStyle.Render("⚠️  API Error") + "\n\n"
		s += errorStyle.Render("Failed to generate PR content:") + "\n"
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(m.apiErrorMsg) + "\n\n"
		s += titleStyle.Render("What would you like to do?") + "\n\n"
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

	if m.phase == "pr_manual_title" {
		s := titleStyle.Render("Enter PR title:") + "\n\n"
		s += fmt.Sprintf("> %s_\n\n", m.prTitle)
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Tip: Keep it concise and descriptive (max 72 chars)") + "\n"
		s += "\n(type your title, press enter to continue to body)\n"
		return s
	}

	if m.phase == "pr_manual_body" {
		s := titleStyle.Render("Enter PR body:") + "\n\n"
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render(fmt.Sprintf("Title: %s", m.prTitle)) + "\n\n"
		s += fmt.Sprintf("%s_\n\n", m.prBody)
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Tip: Describe your changes, press enter for newlines") + "\n"
		s += "\n(type your body, press enter twice to create PR)\n"
		return s
	}

	if m.phase == "pr_creating" {
		summaryStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
		return summaryStyle.Render(m.getSummary()) + "\n"
	}

	if m.phase == "done" || m.phase == "exiting" {
		if summary := m.getSummary(); summary != "" {
			summaryStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
			return summaryStyle.Render(summary) + "\n"
		}
		return ""
	}

	return ""
}

type commitMsgMsg string
type prContentMsg string
type errMsg string
type branchCreatedMsg string
type commitMsgErrMsg string // API error during commit message generation
type prContentErrMsg string // API error during PR content generation

func generateCommitMsg(diff, commitType, scope string) tea.Cmd {
	return func() tea.Msg {
		config := getEffectiveConfig()
		// Use the commit-specific model
		config.Model = config.GetCommitModel()

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

		if config.Provider == "ollama" {
			return generateWithOllama(config, prompt, 1024, false)
		}
		return generateWithAnthropic(config, prompt, 1024, false)
	}
}

// generateWithAnthropic sends a request to the Anthropic API
func generateWithAnthropic(config *Config, prompt string, maxTokens int, isPR bool) tea.Msg {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		if isPR {
			return prContentErrMsg("ANTHROPIC_API_KEY environment variable not set")
		}
		return commitMsgErrMsg("ANTHROPIC_API_KEY environment variable not set")
	}

	reqBody := AnthropicRequest{
		Model:     config.Model,
		MaxTokens: maxTokens,
		Messages: []Message{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error marshaling request: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error marshaling request: %v", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicURL, bytes.NewBuffer(jsonData))
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error creating request: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error creating request: %v", err))
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error making request: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error making request: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error reading response: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error reading response: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("API error (%d): %s", resp.StatusCode, string(body)))
		}
		return commitMsgErrMsg(fmt.Sprintf("API error (%d): %s", resp.StatusCode, string(body)))
	}

	var apiResp AnthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error parsing response: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error parsing response: %v", err))
	}

	if len(apiResp.Content) == 0 {
		if isPR {
			return prContentErrMsg("No content in API response")
		}
		return commitMsgErrMsg("No content in API response")
	}

	result := strings.TrimSpace(apiResp.Content[0].Text)
	if isPR {
		return prContentMsg(result)
	}
	return commitMsgMsg(result)
}

// generateWithOllama sends a request to the Ollama API
func generateWithOllama(config *Config, prompt string, _ int, isPR bool) tea.Msg {
	reqBody := OllamaRequest{
		Model: config.Model,
		Messages: []OllamaMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Stream: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error marshaling request: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error marshaling request: %v", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // Longer timeout for local models
	defer cancel()

	ollamaEndpoint := config.OllamaURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, "POST", ollamaEndpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error creating request: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error creating request: %v", err))
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error making request to Ollama (%s): %v", ollamaEndpoint, err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error making request to Ollama (%s): %v", ollamaEndpoint, err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error reading response: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error reading response: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Ollama API error (%d): %s", resp.StatusCode, string(body)))
		}
		return commitMsgErrMsg(fmt.Sprintf("Ollama API error (%d): %s", resp.StatusCode, string(body)))
	}

	var apiResp OllamaResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		if isPR {
			return prContentErrMsg(fmt.Sprintf("Error parsing response: %v", err))
		}
		return commitMsgErrMsg(fmt.Sprintf("Error parsing response: %v", err))
	}

	result := strings.TrimSpace(apiResp.Message.Content)
	if result == "" {
		if isPR {
			return prContentErrMsg("No content in Ollama API response")
		}
		return commitMsgErrMsg("No content in Ollama API response")
	}

	if isPR {
		return prContentMsg(result)
	}
	return commitMsgMsg(result)
}

func getGitDiff() (string, error) {
	cmd := exec.Command("git", "diff", "--staged")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}
	return string(output), nil
}

func isDiffTooLarge(diff string) bool {
	lines := strings.Split(diff, "\n")
	return len(lines) > diffLineSizeLimit
}

func getGitStatus() (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(output) > 0, nil
}

func countStagedFiles() int {
	cmd := exec.Command("git", "diff", "--staged", "--name-only")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	return len(lines)
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

func getCurrentBranch() (string, error) {
	cmd := exec.Command("git", "branch", "--show-current")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git branch failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func gitPushSetUpstream(branch string) error {
	cmd := exec.Command("git", "push", "--set-upstream", "origin", branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push --set-upstream failed: %w\n%s", err, string(output))
	}
	return nil
}

func validateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("branch name cannot start with a hyphen")
	}
	// Check for invalid characters (git doesn't allow certain chars)
	invalidChars := []string{"..", "~", "^", ":", "?", "*", "[", "\\", " "}
	for _, invalid := range invalidChars {
		if strings.Contains(name, invalid) {
			return fmt.Errorf("branch name contains invalid character: %s", invalid)
		}
	}
	return nil
}

func generateDefaultBranchName() string {
	// Get current timestamp for uniqueness
	now := time.Now()
	dateStr := now.Format("2006-01-02")

	// Try to get git username
	cmd := exec.Command("git", "config", "user.name")
	output, err := cmd.CombinedOutput()
	userName := "dev"
	if err == nil && len(output) > 0 {
		userName = strings.ToLower(strings.TrimSpace(string(output)))
		userName = strings.ReplaceAll(userName, " ", "-")
	}

	return fmt.Sprintf("%s/feature-%s", userName, dateStr)
}

func createAndCheckoutBranch(branchName string) tea.Cmd {
	return func() tea.Msg {
		// Create and checkout the branch
		cmd := exec.Command("git", "checkout", "-b", branchName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return errMsg(fmt.Sprintf("Failed to create branch: %v\n%s", err, string(output)))
		}
		return branchCreatedMsg(branchName)
	}
}

func isGitHubOrigin() error {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get origin URL: %w", err)
	}

	originURL := strings.TrimSpace(string(output))
	if !strings.Contains(originURL, "github.com") {
		return fmt.Errorf("origin is not GitHub (found: %s). Only GitHub repositories are supported for PR creation", originURL)
	}

	return nil
}

func hasExistingPR(branch string) bool {
	cmd := exec.Command("gh", "pr", "list", "--head", branch, "--json", "number")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	// If output is "[]" there are no PRs, otherwise there's at least one
	result := strings.TrimSpace(string(output))
	return result != "[]" && result != ""
}

func getGitLog(branch string) (string, error) {
	// Get the default branch (usually main or master)
	cmd := exec.Command("git", "remote", "show", "origin")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get remote info: %w", err)
	}

	// Parse the default branch
	defaultBranch := "main"
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "HEAD branch:") {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				defaultBranch = strings.TrimSpace(parts[1])
			}
			break
		}
	}

	// Get commits that are on current branch but not on default branch
	cmd = exec.Command("git", "log", fmt.Sprintf("origin/%s..%s", defaultBranch, branch), "--pretty=format:%s%n%b%n---")
	output, err = cmd.CombinedOutput()
	if err != nil {
		// If the branch comparison fails, just get recent commits
		cmd = exec.Command("git", "log", "-10", "--pretty=format:%s%n%b%n---")
		output, err = cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("failed to get git log: %w", err)
		}
	}

	return string(output), nil
}

func generatePRContent(branch string) tea.Cmd {
	return func() tea.Msg {
		config := getEffectiveConfig()
		// Use the PR-specific model
		config.Model = config.GetPRModel()

		gitLog, err := getGitLog(branch)
		if err != nil {
			return prContentErrMsg(fmt.Sprintf("Error getting git log: %v", err))
		}

		prompt := fmt.Sprintf(`You are a pull request generator. Based on the following git log from a branch, generate a clear and concise pull request title and body.

Git log:
%s

Generate:
1. A clear, concise PR title (max 72 characters) that summarizes the changes
2. A detailed PR body that:
   - Summarizes the changes in bullet points
   - Explains the motivation and context
   - Notes any breaking changes or important details

Format your response as:
[PR Title]
---BODY---
[PR Body]

Respond with ONLY the title and body in this format, no explanations or markdown code blocks.`, gitLog)

		if config.Provider == "ollama" {
			return generateWithOllama(config, prompt, 2048, true)
		}
		return generateWithAnthropic(config, prompt, 2048, true)
	}
}

func createPR(title, body string) error {
	cmd := exec.Command("gh", "pr", "create", "--title", title, "--body", body)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr create failed: %w\n%s", err, string(output))
	}
	return nil
}

// Config TUI model for endpoint configuration
type configModel struct {
	phase       string // "provider", "commit_model", "pr_model", "ollama_url", "confirm", "saved", "error"
	provider    string
	commitModel string
	prModel     string
	ollamaURL   string
	input       string // Current input value
	errorMsg    string
	configPath  string
}

const (
	phaseProvider    = "provider"
	phaseCommitModel = "commit_model"
	phasePRModel     = "pr_model"
	phaseOllamaURL   = "ollama_url"
	phaseConfirm     = "confirm"
	phaseSaved       = "saved"
	phaseError       = "error"
)

func initialConfigModel(config *Config, configPath string) configModel {
	commitModel := config.CommitModel
	if commitModel == "" {
		commitModel = config.Model
	}
	prModel := config.PRModel
	if prModel == "" {
		prModel = config.Model
	}
	return configModel{
		phase:       phaseProvider,
		provider:    config.Provider,
		commitModel: commitModel,
		prModel:     prModel,
		ollamaURL:   config.OllamaURL,
		configPath:  configPath,
	}
}

func (m configModel) Init() tea.Cmd {
	return nil
}

func (m configModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit

		case "enter":
			switch m.phase {
			case phaseProvider:
				m.phase = phaseCommitModel
				m.input = m.commitModel
			case phaseCommitModel:
				if m.input != "" {
					m.commitModel = m.input
				}
				m.phase = phasePRModel
				m.input = m.prModel
			case phasePRModel:
				if m.input != "" {
					m.prModel = m.input
				}
				if m.provider == "ollama" {
					m.phase = phaseOllamaURL
					m.input = m.ollamaURL
				} else {
					m.phase = phaseConfirm
				}
			case phaseOllamaURL:
				if m.input != "" {
					m.ollamaURL = m.input
				}
				m.phase = phaseConfirm
			case phaseConfirm:
				// Save the config
				newConfig := &Config{
					Provider:    m.provider,
					CommitModel: m.commitModel,
					PRModel:     m.prModel,
					OllamaURL:   m.ollamaURL,
				}
				// Set Model as fallback for backward compatibility
				newConfig.Model = m.commitModel
				if err := saveConfig(newConfig); err != nil {
					m.errorMsg = err.Error()
					m.phase = phaseError
				} else {
					m.phase = phaseSaved
					return m, tea.Quit
				}
			case phaseError:
				return m, tea.Quit
			case phaseSaved:
				return m, tea.Quit
			}

		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}

		default:
			key := msg.String()
			if len(key) != 1 {
				break
			}
			switch m.phase {
			case phaseProvider:
				if key == "1" {
					m.provider = "anthropic"
				} else if key == "2" {
					m.provider = "ollama"
				}
			case phaseConfirm:
				if key == "y" {
					// Save the config
					newConfig := &Config{
						Provider:    m.provider,
						CommitModel: m.commitModel,
						PRModel:     m.prModel,
						OllamaURL:   m.ollamaURL,
					}
					// Set Model as fallback for backward compatibility
					newConfig.Model = m.commitModel
					if err := saveConfig(newConfig); err != nil {
						m.errorMsg = err.Error()
						m.phase = phaseError
					} else {
						m.phase = phaseSaved
						return m, tea.Quit
					}
				} else if key == "n" {
					return m, tea.Quit
				}
			case phaseCommitModel, phasePRModel, phaseOllamaURL:
				m.input += key
			}
		}

	case tea.WindowSizeMsg:
		// Handle window resize if needed
	}

	return m, nil
}

func (m configModel) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

	if m.phase == phaseError {
		s := titleStyle.Render("Error saving configuration") + "\n\n"
		s += errorStyle.Render(m.errorMsg) + "\n\n"
		s += "Press enter to exit or esc to quit\n"
		return s
	}

	if m.phase == phaseSaved {
		successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
		s := successStyle.Render("✓ Configuration saved successfully!") + "\n\n"
		s += labelStyle.Render("Provider:") + " " + m.provider + "\n"
		s += labelStyle.Render("Commit model:") + " " + m.commitModel + "\n"
		s += labelStyle.Render("PR model:") + " " + m.prModel + "\n"
		if m.provider == "ollama" {
			s += labelStyle.Render("Ollama URL:") + " " + m.ollamaURL + "\n"
		}
		s += "\n" + labelStyle.Render("Config file:") + " " + m.configPath + "\n\n"
		s += "Press enter to exit\n"
		return s
	}

	if m.phase == phaseProvider {
		s := titleStyle.Render("Select LLM Provider") + "\n\n"
		providers := []string{"anthropic", "ollama"}
		for _, p := range providers {
			prefix := " "
			if m.provider == p {
				prefix = ">"
				p = selectedStyle.Render(p)
			}
			s += fmt.Sprintf("%s %s\n", prefix, p)
		}
		s += "\n(press 1 for anthropic, 2 for ollama, enter to continue)\n"
		return s
	}

	if m.phase == phaseCommitModel {
		defaultModel := defaultAnthropicModel
		if m.provider == "ollama" {
			defaultModel = defaultOllamaModel
		}
		s := titleStyle.Render("Configure Commit Model") + "\n\n"
		s += labelStyle.Render("Provider:") + " " + m.provider + "\n\n"
		s += "Enter model for commit message generation (fast model recommended):\n"
		s += fmt.Sprintf("> %s_\n", m.input)
		s += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Default: "+defaultModel) + "\n"
		s += "(press enter when done)\n"
		return s
	}

	if m.phase == phasePRModel {
		defaultModel := defaultAnthropicModel
		if m.provider == "ollama" {
			defaultModel = defaultOllamaModel
		}
		s := titleStyle.Render("Configure PR Model") + "\n\n"
		s += labelStyle.Render("Provider:") + " " + m.provider + "\n"
		s += labelStyle.Render("Commit model:") + " " + m.commitModel + "\n\n"
		s += "Enter model for PR description generation (smarter model recommended):\n"
		s += fmt.Sprintf("> %s_\n", m.input)
		s += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Default: "+defaultModel) + "\n"
		s += "(press enter when done)\n"
		return s
	}

	if m.phase == phaseOllamaURL {
		s := titleStyle.Render("Configure Ollama Server URL") + "\n\n"
		s += labelStyle.Render("Provider:") + " ollama\n"
		s += labelStyle.Render("Commit model:") + " " + m.commitModel + "\n"
		s += labelStyle.Render("PR model:") + " " + m.prModel + "\n\n"
		s += "Enter Ollama server URL:\n"
		s += fmt.Sprintf("> %s_\n", m.input)
		s += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Default: "+defaultOllamaURL) + "\n"
		s += "(press enter when done)\n"
		return s
	}

	if m.phase == phaseConfirm {
		s := titleStyle.Render("Confirm Configuration") + "\n\n"
		s += labelStyle.Render("Provider:") + " " + m.provider + "\n"
		s += labelStyle.Render("Commit model:") + " " + m.commitModel + "\n"
		s += labelStyle.Render("PR model:") + " " + m.prModel + "\n"
		if m.provider == "ollama" {
			s += labelStyle.Render("Ollama URL:") + " " + m.ollamaURL + "\n"
		}
		s += "\n" + labelStyle.Render("Config file:") + " " + m.configPath + "\n\n"
		s += titleStyle.Render("Save this configuration?") + "\n\n"
		s += "  [y] Yes, save\n"
		s += "  [n] No, cancel\n\n"
		s += "(press y to save, n to cancel, esc to quit)\n"
		return s
	}

	return ""
}

func runConfigUI() {
	// Load current config
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	configPath, err := getConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting config path: %v\n", err)
		os.Exit(1)
	}

	p := tea.NewProgram(initialConfigModel(config, configPath))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running config UI: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`gitcat - AI-powered git commit message generator

USAGE:
    gitcat [OPTIONS]

OPTIONS:
    -m, --model <model>           Model to use for both commit and PR (overrides config)
    --commit-model <model>        Model for commit message generation (overrides config and -m)
    --pr-model <model>            Model for PR description generation (overrides config and -m)
    -p, --provider <provider>     LLM provider: anthropic or ollama (overrides config)
    --ollama-url <url>            Ollama server URL (overrides config)
    --pr                          Generate a PR from existing commits (no commit required)

SUBCOMMANDS:
    config                        Open configuration TUI to set provider, models, and endpoints
    help                          Show this help message

EXAMPLES:
    gitcat                        Generate a commit message with default config
    gitcat -m claude-3-opus       Use a specific model for both commit and PR
    gitcat --commit-model claude-haiku-3-5-20241022 --pr-model claude-sonnet-4-5-20250929
                                  Use a fast model for commits, smarter model for PRs
    gitcat -p ollama              Use Ollama provider
    gitcat --pr                   Generate a PR from current branch commits
    gitcat config                 Configure endpoints and settings

CONFIGURATION:
    Config is stored in: ~/.config/gitcat/config.json
    Separate models can be configured for commit messages and PR descriptions.
    Use 'gitcat config' to set them interactively.

    Available providers:
      - anthropic: Requires ANTHROPIC_API_KEY environment variable
      - ollama: Local Ollama instance for running open-source models`)
}

func main() {
	flag.Parse()

	// Handle subcommands
	if len(flag.Args()) > 0 {
		switch flag.Arg(0) {
		case "config":
			// Run the configuration TUI and exit
			runConfigUI()
			return
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}

	// Load configuration
	var err error
	appConfig, err = loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Save config if it doesn't exist (creates default config file)
	configPath, _ := getConfigPath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := saveConfig(appConfig); err != nil {
			// Non-fatal: just warn if we can't save the default config
			fmt.Fprintf(os.Stderr, "Warning: could not save default config: %v\n", err)
		}
	}

	// Handle --pr flag: skip commit flow and generate PR directly
	if *prFlag {
		currentBranch, err := getCurrentBranch()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current branch: %v\n", err)
			os.Exit(1)
		}

		if err := isGitHubOrigin(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if hasExistingPR(currentBranch) {
			fmt.Fprintf(os.Stderr, "A pull request already exists for branch '%s'.\n", currentBranch)
			os.Exit(1)
		}

		p := tea.NewProgram(initialModel("", false, currentBranch, false, true))
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
			os.Exit(1)
		}
		return
	}

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

	currentBranch, err := getCurrentBranch()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current branch: %v\n", err)
		os.Exit(1)
	}

	isProtectedBranch := currentBranch == "main" || currentBranch == "master"

	p := tea.NewProgram(initialModel(diff, needsAdd, currentBranch, isProtectedBranch, false))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
