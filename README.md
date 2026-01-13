# gitcat

A Go CLI tool that generates conventional commit messages using Anthropic's Claude AI models with an interactive bubbletea interface.

## Features

- 🤖 AI-powered commit message generation using Claude models
- 📝 Conventional Commits format (feat, fix, docs, etc.)
- 🎨 Interactive terminal UI with dropdown selections
- 🔐 Secure API key management via environment variables (1Password compatible)
- ⚙️ Configurable model selection via CLI flags
- 🚀 Smart git workflow (auto-detect unstaged changes)

## Installation

```bash
go install github.com/burritocatai/gitcat@v0.0.3
```

Move the binary to your PATH:
```bash
mv gitcat /usr/local/bin/
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
