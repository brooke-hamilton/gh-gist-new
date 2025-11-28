# gh gist-new

A GitHub CLI extension that creates a new gist from a directory and sets up the local folder as a git working copy.

## Features

- Create a gist from all files in a directory (or the current directory)
- Automatically initializes the directory as a git repository linked to the gist
- Supports public and secret gists
- Handles empty directories by creating a starter file

## Installation

```bash
gh extension install brooke-hamilton/gh-gist-new
```

## Usage

```text
gh gist-new [name] [flags]
```

### Arguments

- `[name]` - **Required**. The name of the directory to create the gist from. Use `.` to create a gist from the current directory.

### Flags

| Flag | Description |
|------|-------------|
| `--public` | Create the gist as public (defaults to secret) |
| `-d, --description` | Description to attach to the gist (must not be empty if provided) |
| `--verbose` | Show detailed per-file logs and timing information |
| `-h, --help` | Show help message |

### Examples

Create a gist from the current directory:

```bash
gh gist-new .
```

Create a new directory and gist with a description:

```bash
gh gist-new my-snippet -d "My code snippet"
```

Create a public gist with verbose output:

```bash
gh gist-new my-project --public --verbose
```

## How It Works

1. **Resolves the target directory** - If `[name]` is `.`, uses the current directory. Otherwise, creates a new directory with that name if it doesn't exist.

2. **Collects files** - Gathers all regular files in the directory, excluding:
   - Dotfiles (files starting with `.`)
   - Subdirectories (gists don't support directories)
   - Symlinks to directories

3. **Handles empty directories** - If the directory is empty, creates a `[name].md` file with a heading as a starter.

4. **Creates the gist** - Uploads all collected files to GitHub as a new gist.

5. **Sets up git** - Clones the gist's git metadata into the target directory, turning it into a working copy. You can then use standard git commands to push updates to your gist.

## Requirements

- [GitHub CLI](https://cli.github.com/) (`gh`) installed and authenticated
- Git installed

## Limitations

- Gists cannot contain subdirectories. If the target directory contains subdirectories or directory symlinks, the command will abort with an error.
- The target directory must not already be a git repository (when using `.`).

## License

See [LICENSE](license) file.
