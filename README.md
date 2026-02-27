# gitcat

A Go CLI tool that generates conventional commit messages and pull requests using AI, with an interactive bubbletea terminal interface. Supports Anthropic Claude, Ollama (local models), and OpenAI-compatible APIs.

## Features

- 🤖 AI-powered commit message generation using Claude, Ollama, or OpenAI-compatible APIs
- 📝 Conventional Commits format (feat, fix, docs, etc.)
- 🎨 Interactive terminal UI with dropdown selections
- 🔐 Secure API key management via environment variables (1Password compatible)
- ⚙️ Configurable model selection — use different models for commits and PRs
- 🚀 Smart git workflow (auto-detect unstaged changes, upstream branch setup)
- 🛡️ Protected branch detection — warns when committing to main/master and offers to create a feature branch
- 🔄 Optional push with automatic upstream branch detection
- 🔀 AI-powered PR creation with automatic title and body generation (GitHub only)
- 📋 PR-only mode — generate a PR from existing commits without a new commit

## Installation

```bash
go install github.com/burritocatai/gitcat@latest
```

### PR Creation Requirements

To use the PR creation feature, you need:
- GitHub CLI (`gh`) installed and authenticated
- Repository origin must be on github.com
- Branch must be pushed to remote

Install GitHub CLI:
```bash
# macOS
brew install gh

# Linux
curl -sS https://webi.sh/gh | sh

# Authenticate
gh auth login
```

## Configuration

### Interactive Config

Run the built-in configuration TUI to set your provider, models, and credentials:

```bash
gitcat config
```

This saves settings to `~/.config/gitcat/config.json`.

### Config File

Settings are stored in `~/.config/gitcat/config.json`:

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-5-20250929",
  "commit_model": "claude-sonnet-4-5-20250929",
  "pr_model": "claude-sonnet-4-5-20250929",
  "ollama_url": "http://localhost:11434",
  "openai_url": "",
  "openai_api_key": ""
}
```

### Environment Variables

| Variable | Description |
|---|---|
| `ANTHROPIC_API_KEY` | API key for Anthropic provider |
| `OPENAI_API_KEY` | API key for OpenAI-compatible provider (can also be set via config or CLI flag) |

For 1Password integration:
```bash
export ANTHROPIC_API_KEY="op://vault/item/field"
```

### Providers

**Anthropic** (default)
```bash
export ANTHROPIC_API_KEY="your-api-key"
gitcat
```

**Ollama** (local models)
```bash
gitcat -p ollama
gitcat -p ollama --ollama-url http://localhost:11434
```

**OpenAI-compatible** (OpenAI, LiteLLM, etc.)
```bash
gitcat -p openai --openai-url http://localhost:4000 --openai-api-key sk-your-key
```

## Usage

```bash
# Generate commit with default config
gitcat

# Specify a model for both commit and PR
gitcat -m claude-opus-4-5-20251101

# Use different models for commits and PRs
gitcat --commit-model claude-haiku-3-5-20241022 --pr-model claude-sonnet-4-5-20250929

# Use Ollama provider
gitcat -p ollama

# Use OpenAI-compatible provider
gitcat -p openai --openai-url http://localhost:4000 --openai-api-key sk-your-key

# Generate a PR from existing commits (no new commit)
gitcat --pr

# Open interactive config
gitcat config
```

### CLI Flags

| Flag | Short | Description |
|---|---|---|
| `--model` | `-m` | Model to use for both commit and PR generation |
| `--commit-model` | | Model for commit message generation |
| `--pr-model` | | Model for PR description generation |
| `--provider` | `-p` | LLM provider: `anthropic`, `ollama`, or `openai` |
| `--ollama-url` | | Ollama server URL |
| `--openai-url` | | OpenAI-compatible endpoint URL |
| `--openai-api-key` | | OpenAI-compatible API key |
| `--pr` | | Generate a PR from existing commits without committing |

CLI flags override config file settings.

### Available Models

**Anthropic**
- `claude-sonnet-4-5-20250929` (default)
- `claude-opus-4-5-20251101`
- `claude-haiku-3-5-20241022`

**Ollama**
- Any locally installed model (default: `llama3.2`)

**OpenAI-compatible**
- `gpt-4o` (default)
- Any model supported by the endpoint

## Workflow

1. **Check branch**: Warns if on main/master and offers to create a feature branch
2. **Check for changes**: Checks for staged changes
3. **Add files** (if needed): If no staged changes, offers to run `git add .`
4. **Select commit type**: Choose from conventional commit types
5. **Enter scope**: Provide a scope for your commit
6. **AI generation**: Generates a commit message based on your diff
7. **Review & edit**: Review the generated message and optionally edit it
8. **Commit**: Confirm to create the commit
9. **Push** (optional): Choose whether to push to remote
10. **Set upstream** (if needed): Offers to set upstream branch automatically
11. **Create PR** (optional): Generate and create a GitHub pull request

> If the diff exceeds 1000 lines, the tool skips AI generation and falls back to manual input.

## Conventional Commit Types

- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `perf`: Performance improvements
- `test`: Test changes
- `build`: Build system changes
- `ci`: CI configuration changes
- `chore`: Other changes

## Pull Request Generation

After a successful push, gitcat checks if a PR already exists for your branch. If no PR exists and your origin is GitHub, it offers to create one.

You can also generate a PR independently with `gitcat --pr`.

When creating a PR, gitcat will:

1. **Verify GitHub origin**: Checks that your remote is on github.com
2. **Check for existing PR**: Skips if a PR already exists for the branch
3. **Analyze git log**: Examines commits on your branch compared to the default branch
4. **Generate PR content**: Uses AI to create a title and detailed body
5. **Create PR**: Submits via `gh pr create`

## Keyboard Controls

- `↑/↓` or `k/j`: Navigate options
- `Enter`: Confirm selection
- `Type`: Enter text for scope/editing
- `Backspace`: Delete characters
- `Esc`: Quit config screen
- `q` or `Ctrl+C`: Quit

## License

MIT
