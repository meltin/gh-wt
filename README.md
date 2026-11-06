A GitHub CLI extension to switch between git worktrees and see their state at a glance.

## Overview

`gh-wt` = `git worktree list` + `cd`

- `gh wt` shows the other worktrees with their status, PR and last commit, and moves you to the one you pick
- `gh wt <branch|path>` moves you to an existing worktree directly
- `gh wt -l` lists all worktrees

It only needs `git` and `gh`. Icons use [Nerd Fonts](https://www.nerdfonts.com/), so set a Nerd Font in your terminal.

## Installation

### Prerequisites

- [GitHub CLI](https://cli.github.com/) must be installed and authenticated
- A [Nerd Font](https://www.nerdfonts.com/) in your terminal

### Install as a GitHub CLI extension

```bash
gh extension install unmett/gh-wt
```

### Shell integration (zsh)

An extension runs as a child process, so it cannot change your shell's directory. `gh wt` prints the path of the worktree, and this function in your `.zshrc` does the `cd`:

```zsh
gh() {
  if [[ $1 == wt && ( $# == 1 || ( $# == 2 && $2 != -* ) ) ]]; then
    local dir
    dir=$(command gh "$@") && cd -- "$dir"
  else
    command gh "$@"
  fi
}
```

Only `gh wt` and `gh wt <branch|path>` are intercepted. Every other `gh` command, including `gh wt -l`, runs as usual.

## Usage

```
gh wt --help

# Output:
Interactively switch between git worktrees.

USAGE
  gh wt [worktree]
  gh wt [flags]

FLAGS
  -l, --list   List worktrees with status, PR and last commit
  -h, --help   Show help for command

EXAMPLES
  $ gh wt          # Select a worktree and print its path
  $ gh wt feat/x   # Print the path of the worktree for branch feat/x
  $ gh wt -l       # List worktrees

gh wt only prints the path. To cd into it, add the shell function
from the README to your .zshrc.
```

### Columns

| Column      | Content                                                                                   |
| ----------- | ----------------------------------------------------------------------------------------- |
| BRANCH      | Branch name (up to 28 cells), or `(detached)`                                             |
| STATUS      | `↑` ahead / `↓` behind upstream, `!` conflicts, `+` staged, `~` modified, `?` untracked     |
| PR          | Number and state (open / draft / merged / closed) of the newest PR for the branch          |
| LAST COMMIT | Relative time and subject                                                                 |
| PATH        | Worktree path, `~` for home                                                               |

In the picker, the current worktree is shown grayed out under the header and cannot be selected. When a table is wider than the terminal, PATH, the commit subject and BRANCH are shortened, in that order.
