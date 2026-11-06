A GitHub CLI extension to switch between git worktrees and see their state at a glance.

## Overview

`gh-wt` = `git worktree list` + `cd`

- `gh wt` shows the other worktrees with their status, PR and last commit, and moves you to the one you pick
- `gh wt <branch|worktree|path>` moves you to that worktree, creating the worktree (and the branch) if needed
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
    local dir rc
    dir=$(command gh "$@"); rc=$?
    [[ -d $dir ]] && cd -- "$dir"
    return $rc
  else
    command gh "$@"
  fi
}
```

Only `gh wt` and `gh wt <branch|worktree|path>` are intercepted. Every other `gh` command, including `gh wt -l`, runs as usual. If a hook fails after creating a worktree, you are still moved into it and the exit status is non-zero.

## Usage

```
gh wt --help

# Output:
Switch between git worktrees, creating them as needed.

USAGE
  gh wt [<branch|worktree|path>]
  gh wt [flags]

FLAGS
  -l, --list   List worktrees with status, PR and last commit
  -h, --help   Show help for command

EXAMPLES
  $ gh wt          # Select a worktree and print its path
  $ gh wt feat/x   # Print the path of the worktree for feat/x,
                   # creating the worktree (and branch) if needed
  $ gh wt -l       # List worktrees

A name is always a target, so "gh wt list" creates a worktree named list.

gh wt only prints the path. To cd into it, add the shell function
from the README to your .zshrc.
```

### Columns

| Column      | Content                                                                                   |
| ----------- | ----------------------------------------------------------------------------------------- |
| BRANCH      | Branch name (up to 28 cells), or `(detached)`                                             |
| STATUS      | `↑` ahead / `↓` behind upstream, `!` conflicts, `+` staged, `~` modified, `?` untracked     |
| PR          | Number and state (open / draft / merged / closed) of the newest PR for the branch          |
| LAST COMMIT | Subject and relative time                                                                 |
| PATH        | Worktree path, `~` for home                                                               |

In the picker, the current worktree is shown grayed out under the header and cannot be selected. When a table is wider than the terminal, PATH, the commit subject and BRANCH are shortened, in that order.

## Creating worktrees

When no worktree matches, `gh wt <name>` runs `git worktree add` under the base directory: it checks out the local branch `<name>`, or a remote branch of that name with tracking, or creates a new branch from `HEAD`. A name is always a target, so `gh wt list` creates a worktree named `list`.

A target can be a branch name, a worktree directory name relative to the base directory, or a path to an existing worktree.

### Configuration

gh-wt reads the same `git config` keys as [k1LoW/git-wt](https://github.com/k1LoW/git-wt), so an existing git-wt setup keeps working:

| Key                | Default | Effect                                                                              |
| ------------------ | ------- | ----------------------------------------------------------------------------------- |
| `wt.basedir`       | `.wt`   | Base directory for new worktrees. `{gitroot}` is the repository's directory name; a relative path is under the repository |
| `wt.copyignored`   | `false` | Copy files ignored by `.gitignore` (e.g. `.env`) from the current worktree         |
| `wt.copyuntracked` | `false` | Copy untracked files                                                                |
| `wt.copymodified`  | `false` | Copy tracked files with uncommitted changes                                         |
| `wt.hook`          | (none)  | Commands run with `sh -c` in the new worktree, in order; repeat the key for more    |

```bash
git config --global wt.basedir "~/dev/worktrees/{gitroot}"
git config --global wt.copyignored true
git config --global --add wt.hook "npm install"
```

Files are cloned with `cp -c` on macOS (APFS copy-on-write), so large directories such as `node_modules` copy quickly. `wt.copy`, `wt.nocopy`, `wt.symlink` and the other git-wt keys are not supported yet.
