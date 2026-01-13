# gitcat

A Go CLI tool that generates conventional commit messages using Anthropic's Claude AI models with an interactive bubbletea interface.

## Features

- 🤖 AI-powered commit message generation using Claude models
- 📝 Conventional Commits format (feat, fix, docs, etc.)
- 🎨 Interactive terminal UI with dropdown selections
- 🔐 Secure API key management via environment variables (1Password compatible)
- ⚙️ Configurable model selection via CLI flags
- 🚀 Smart git workflow (auto-detect unstaged changes, upstream branch setup)
- 🔄 Optional push with automatic upstream branch detection
- 🔀 AI-powered PR creation with automatic title and body generation (GitHub only)

## Installation

```bash
go install github.com/burritocatai/gitcat@v0.0.3
```

Move the binary to your PATH:
```bash
mv gitcat /usr/local/bin/
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

Set your Anthropic API key as an environment variable:

```bash
export ANTHROPIC_API_KEY="your-api-key-here"
```

For 1Password integration:
```bash
export ANTHROPIC_API_KEY="op://vault/item/field"
```

## Usage

Basic usage:
```bash
gitcat
```

Specify a model:
```bash
gitcat --model claude-sonnet-4-5-20250929
gitcat -m claude-opus-4-5-20251101
```

### Available Models
- `claude-sonnet-4-5-20250929` (default)
- `claude-opus-4-5-20251101`
- `claude-haiku-3-5-20241022`

## Workflow

1. **Check for changes**: The tool checks for staged changes
2. **Add files** (if needed): If no staged changes, offers to run `git add .`
3. **Select commit type**: Choose from conventional commit types
4. **Enter scope**: Provide a scope for your commit
5. **AI generation**: Claude generates a commit message based on your diff
6. **Review & edit**: Review the generated message and optionally edit it
7. **Commit**: Confirm to create the commit
8. **Push** (optional): Choose whether to push to remote (defaults to "No")
9. **Set upstream** (if needed): If no upstream branch exists, offers to set it automatically
10. **Create PR** (optional): Generate and create a GitHub pull request (defaults to "No")

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

After a successful push, gitcat will automatically check if a PR already exists for your branch. If no PR exists and your origin is GitHub, it will offer to create one.

When you choose to create a PR, gitcat will:

1. **Verify GitHub origin**: Checks that your remote is on github.com
2. **Check for existing PR**: Uses `gh pr list` to see if a PR already exists for this branch
3. **Analyze git log**: Examines commits on your branch (compared to the default branch)
4. **Generate PR content**: Uses Claude AI to create:
   - A clear, concise title summarizing the changes
   - A detailed body with bullet points, context, and any important notes
5. **Create PR**: Uses `gh pr create` to submit the pull request

The AI analyzes your commit history to understand the full scope of changes and generates appropriate PR documentation automatically. If a PR already exists, the prompt is automatically skipped.

## Examples

```bash
# Generate commit for staged changes
$ gitcat
> Select commit type: feat
> Enter scope: auth
> Generated: feat(auth): add OAuth2 authentication flow

# Use a different model
$ gitcat -m claude-opus-4-5-20251101
```

## Keyboard Controls

- `↑/↓` or `k/j`: Navigate options
- `Enter`: Confirm selection
- `Type`: Enter text for scope/editing
- `Backspace`: Delete characters
- `q` or `Ctrl+C`: Quit

## License

MIT
