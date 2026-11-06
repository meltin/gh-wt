A GitHub CLI extension to switch between git worktrees and see their state at a glance.

## Overview

`gh-wt` = `git worktree list` + `cd`

- `gh wt` shows the other worktrees with their status, PR and last commit, and moves you to the one you pick
- `gh wt <branch|worktree|path>` moves you to that worktree, creating the worktree (and the branch) if needed
- `gh wt -l` lists all worktrees
- `gh wt -d <target>...` deletes worktrees and their branches

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
  if [[ $1 == wt && $2 != (-l|--list|-h|--help) ]]; then
    local out rc last
    out=$(command gh "$@"); rc=$?
    last=${out##*$'\n'}
    if [[ -d $last ]]; then
      [[ $out == *$'\n'* ]] && print -r -- "${out%$'\n'*}"
      cd -- "$last"
    elif [[ -n $out ]]; then
      print -r -- "$out"
    fi
    return $rc
  else
    command gh "$@"
  fi
}
```

Like git-wt's shell integration, every `gh wt` call is intercepted: when the last line of its output is a directory, the function moves there and prints the lines before it. The exit status is passed through, so if a hook fails after creating a worktree you are still moved into it and the status is non-zero. `gh wt -l` and `gh wt --help` run directly, keeping colors and the terminal width. Every other `gh` command runs as usual.

## Usage

```
gh wt --help

# Output:
Switch between git worktrees, creating them as needed.

USAGE
  gh wt [<branch|worktree|path>]
  gh wt -d|-D <branch|worktree|path>...
  gh wt [flags]

FLAGS
  -l, --list           List worktrees with status, PR and last commit
  -d, --delete         Delete worktrees and their branches (safe)
  -D, --force-delete   Delete even with changes or an unmerged branch
  -h, --help           Show help for command

EXAMPLES
  $ gh wt            # Select a worktree and print its path
  $ gh wt feat/x     # Print the path of the worktree for feat/x,
                     # creating the worktree (and branch) if needed
  $ gh wt -l         # List worktrees
  $ gh wt -d feat/x  # Delete the worktree and branch feat/x

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

## Deleting worktrees

`gh wt -d <target>...` removes each worktree and then its local branch, like git-wt:

- A worktree with modified or untracked files is not removed (ignored files such as `node_modules` don't count)
- A branch that is not fully merged is kept, with a message; the exit status is still 0
- The default branch (`origin/HEAD`, else `init.defaultBranch`, else `master`) and the branch the main repository's `HEAD` points to are never deleted; their worktrees can be
- A target with no worktree but a local branch deletes just the branch
- A worktree whose directory was already removed is cleaned up as well
- Targets are matched as a branch first, then a directory under the base directory, then a path
- Processing stops at the first target that fails

`gh wt -D` removes worktrees with changes and deletes unmerged branches. When the current worktree is deleted, the shell function moves you to the main repository.

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
