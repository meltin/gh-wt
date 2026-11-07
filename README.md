A GitHub CLI extension to switch between git worktrees and see their state at a glance.

## Overview

`gh-wt` = `git worktree list` + `cd`

- `gh wt` shows the other worktrees with their status, PR and last commit, and moves you to the one you pick
- `gh wt <branch|worktree|path>` moves you to that worktree, creating the worktree (and the branch) if needed
- `gh wt -l` lists all worktrees
- `gh wt -p` opens an open pull request's branch as a worktree
- `gh wt -d <target>...` deletes worktrees and their branches
- `gh wt -m [<target>] <new-name>` renames a worktree's directory and branch together

It only needs `git` and `gh`. Icons use [Nerd Fonts](https://www.nerdfonts.com/), so set a Nerd Font in your terminal.

## Installation

### Prerequisites

- [GitHub CLI](https://cli.github.com/) must be installed and authenticated
- A [Nerd Font](https://www.nerdfonts.com/) in your terminal

### Install as a GitHub CLI extension

```bash
gh extension install meltin/gh-wt
```

### Shell integration (zsh)

An extension runs as a child process, so it cannot change your shell's directory. `gh wt` prints the path of the worktree, and a small `gh()` shell function does the `cd`. Add this to your `.zshrc`:

```zsh
eval "$(gh wt --init zsh)"
```

Like git-wt's shell integration, the function intercepts every `gh wt` call: when the last line of its output is a directory, it moves there and prints the lines before it. The exit status is passed through, so if a hook fails after creating a worktree you are still moved into it and the status is non-zero. `gh wt -l`, `gh wt --help` and `gh wt --init` run directly, keeping colors and the terminal width. Every other `gh` command runs as usual.

Run `gh wt --init zsh` to read the function. Because it is loaded at shell startup, upgrading gh-wt updates it the next time you open a shell. Put the line after `gh` is on your `PATH`; it replaces any `gh` function defined before it, and works with a `gh` alias and `setopt nounset`.

## Usage

```
gh wt --help

# Output:
Switch between git worktrees, creating them as needed.

USAGE
  gh wt [<branch|worktree|path> [<start-point>]]
  gh wt -b <branch> <worktree> [<start-point>]
  gh wt --orphan [-b <branch>] <worktree>
  gh wt -p [<number>|<url>|<branch>]
  gh wt -d|-D <branch|worktree|path>...
  gh wt -m|-M [<branch|worktree|path>] <new-name>
  gh wt [flags]

FLAGS
  -l, --list           List worktrees with changes, PR and last commit
  -p, --pr             Select an open PR, or give one, and open its branch
  -b, --branch NAME    Create with a branch name different from the directory
      --orphan         Create on a new branch with no history
  -d, --delete         Delete worktrees and their branches (safe)
  -D, --force-delete   Delete even with changes or an unmerged branch
  -m, --move           Rename a worktree's directory and branch
  -M, --force-move     Rename over an existing branch, or a locked worktree
      --init zsh       Print the shell function that cds into the worktree
  -h, --help           Show help for command

EXAMPLES
  $ gh wt                  # Select a worktree and print its path
  $ gh wt feat/x           # Print the path of the worktree for feat/x,
                           # creating the worktree (and branch) if needed
  $ gh wt fix/y main       # Create branch fix/y from main in a new worktree
  $ gh wt -b me/feat feat  # Directory feat, branch me/feat
  $ gh wt -p               # Select an open PR and open its branch
  $ gh wt -p 1720          # Open PR #1720's branch (a PR URL works too)
  $ gh wt -l               # List worktrees
  $ gh wt -d feat/x        # Delete the worktree and branch feat/x
  $ gh wt -m feat/y        # Rename the current worktree and branch to feat/y

A name is always a target, so "gh wt list" creates a worktree named list.

gh wt only prints the path. To cd into it, add this to your .zshrc:
  eval "$(gh wt --init zsh)"
```

### Columns

| Column      | Content                                                                                   |
| ----------- | ----------------------------------------------------------------------------------------- |
| BRANCH      | Branch name (up to 28 cells), or `(detached)`                                             |
| CHANGES     | `↑` ahead / `↓` behind upstream, `!` conflicts, `+` staged, `~` modified, `?` untracked     |
| PR          | Number and state (open / draft / merged / closed) of the newest PR for the branch          |
| LAST COMMIT | Subject and relative time                                                                 |
| PATH        | Worktree path, `~` for home                                                               |

For a branch with an open PR, `↓` also counts commits pushed to the PR on GitHub that the worktree doesn't have yet, without fetching; `↓N` means there are such commits but they haven't been fetched, so they can't be counted. In the picker, the current worktree is shown grayed out under the header and cannot be selected. When a table is wider than the terminal, PATH, the commit subject and BRANCH are shortened, in that order.

## Creating worktrees

When no worktree matches, `gh wt <name>` runs `git worktree add` under the base directory: it checks out the local branch `<name>`, or a remote branch of that name with tracking, or creates a new branch from `HEAD`. A name is always a target, so `gh wt list` creates a worktree named `list`.

A target can be a branch name, a worktree directory name relative to the base directory, or a path to an existing worktree.

`gh wt <name> <start-point>` creates the new branch from `<start-point>` (a branch, tag or commit) instead of `HEAD`, e.g. `gh wt fix/y main` from inside another worktree. A start point that exists only on a remote is used as `<remote>/<start-point>` (with `checkout.defaultRemote` breaking ties between remotes), so the new branch keeps its own name. A start point is refused when the worktree or a local branch named `<name>` already exists.

`gh wt -b <branch> <worktree>` names the directory `<worktree>` and the branch `<branch>`, e.g. `gh wt -b me/feat feat`; either name finds it afterwards. `gh wt --orphan <name>` creates a branch with no history (Git 2.42+) in an empty worktree: no files are copied, hooks still run, and it can't take a start point or an existing branch. Flags may come before or after the names, as in git-wt; `-b` also takes `--branch=NAME` or `-bNAME`, and `--` ends the flags.

## Opening pull requests

`gh wt -p` lists the repository's open pull requests and opens the selected one's branch: like `gh wt <branch>`, it moves to the worktree if there is one and creates it otherwise. `gh wt -p <number>` (or `#<number>`, the PR's URL or its branch) opens a PR directly.

| Column   | Content                                                                         |
| -------- | ------------------------------------------------------------------------------- |
| WORKTREE | The worktree's CHANGES if it exists (selecting moves there); empty otherwise     |
| PR       | Number, green for open and yellow `draft` for drafts                            |
| TITLE    | PR title, shortened first when the table is wider than the terminal            |
| BRANCH   | Head branch                                                                     |
| AUTHOR   | PR author                                                                       |
| UPDATED  | When the PR was last updated                                                    |

`gh pr checkout --worktree` does the checkout, so the PR opens just as gh opens it: fetched first (plain `gh wt <branch>` doesn't fetch), tracking the PR's branch, an existing branch fast-forwarded (or refused when it can't be), and PRs from forks included; a URL of another repository's PR fetches that PR, as `gh pr checkout <url>` does. gh-wt picks the path under `wt.basedir` and, for a new worktree, copies files and runs `wt.hook`.

## Deleting worktrees

`gh wt -d <target>...` removes each worktree and then its local branch, like git-wt:

- A worktree with modified or untracked files is not removed (ignored files such as `node_modules` don't count)
- A branch that is not fully merged is kept, with a message; the exit status is still 0
- The default branch (`origin/HEAD`, else `init.defaultBranch`, else `master`) and the branch the main repository's `HEAD` points to are never deleted; their worktrees can be
- A target with no worktree but a local branch deletes just the branch
- A worktree whose directory was already removed is cleaned up as well
- Parent directories left empty under the base directory (e.g. `feat/` after deleting `feat/x`) are removed
- Targets are matched as a branch first, then a directory under the base directory, then a path
- Processing stops at the first target that fails

`gh wt -D` removes worktrees with changes and deletes unmerged branches. When the current worktree is deleted, the shell function moves you to the main repository.

## Renaming worktrees

`gh wt -m <new-name>` renames the current worktree; `gh wt -m <target> <new-name>` renames another one. The directory moves to `<base directory>/<new-name>` and the branch is renamed to `<new-name>`, like git-wt:

- The branch is renamed first, so git refuses invalid or conflicting names (an existing branch, `feat` when renaming to `feat/y`, a branch checked out elsewhere) before anything on disk changes. If the directory then fails to move, the branch is renamed back
- An existing destination, or a destination inside another worktree, stops the rename
- The main working tree, a detached worktree and the protected branches (see deleting) can't be renamed
- Parent directories left empty under the base directory (e.g. `feat/` after renaming `feat/x`) are removed

`gh wt -M` overwrites an existing branch of the new name and moves a locked worktree. When the current worktree is renamed, the shell function moves you to its new path.

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
