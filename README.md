# gh-wt

A GitHub CLI extension that makes `git worktree` simple, with the state of every worktree and pull request at a glance.

It doesn't wait for GitHub: the worktrees show up as soon as git has read them, and their pull requests fill in when `gh` answers.

[日本語](README.ja.md)

## Usage

``` console
$ gh wt                              # Select a worktree and move to it
$ gh wt -l                           # List all worktrees
$ gh wt <branch|worktree|path>       # Switch to worktree (create worktree/branch if needed)
$ gh wt <branch> <start-point>       # Create worktree with a new branch from <start-point>
$ gh wt -b <branch> <worktree>       # Create worktree with a different branch name
$ gh wt --orphan <branch|worktree>   # Create worktree with a new orphan branch
$ gh wt -p                           # Select an open pull request and open it in a worktree
$ gh wt -p <number|url|branch>       # Open a pull request in a worktree
$ gh wt -d <branch|worktree|path>... # Delete worktree and branch (safe)
$ gh wt -D <branch|worktree|path>... # Force delete worktree and branch
$ gh wt -m [<old>] <new>             # Rename worktree directory and branch (safe)
$ gh wt -M [<old>] <new>             # Force rename (overwrite existing branch, allow moving locked worktrees)
```

The target can be specified as:
- **branch**: a git branch name — _eg._ `gh wt feature-branch`
- **worktree**: a directory name relative to [`wt.basedir`](#wtbasedir) (default `.wt`) — _eg._ `gh wt some-worktree-folder-name`
- **path**: a filesystem path (absolute or relative to the current working directory) to an existing worktree — _eg._ `gh wt ../sibling`, `gh wt /absolute/path`

Targets are matched in that order: a branch first, then a directory under `wt.basedir`, then a path.

> [!NOTE]
> gh-wt has no subcommands. The first non-flag argument is always a target name, never a command, so `gh wt list` creates a worktree named `list` instead of listing anything. Run `gh wt -l` to list worktrees.

gh-wt follows [k1LoW/git-wt](https://github.com/k1LoW/git-wt): the same target rules, the same `wt.*` git config keys (except [these](#not-supported-yet)) and the same behaviour for the operations it has, built on `git` and `gh` alone. Where git or gh already does something, gh-wt runs it and shows its output as is.

### Listing worktrees

`gh wt` shows the other worktrees and moves you to the one you pick; `gh wt -l` prints the same table for all of them.

![Picking a worktree with gh wt](docs/demo/select.gif)

| Column      | Content                                                                                 |
| ----------- | --------------------------------------------------------------------------------------- |
| BRANCH      | Branch name (up to 26 cells), or `(detached)`                                           |
| (changes)   | `↑` ahead / `↓` behind upstream, `!` conflicts, `+` staged, `~` modified, `?` untracked |
| PR          | Number and state (open / draft / merged / closed) of the branch's PR, an open one first |
| LAST COMMIT | Subject and relative time                                                               |
| PATH        | Worktree path, `~` for home                                                             |

- The table appears as soon as git has read the worktrees. The PR column shows a spinner until `gh` answers, then fills in without moving the other columns, and you can move or pick in the meantime. Piped, `gh wt -l` waits and prints once.
- For a branch with an open PR, `↓` also counts commits pushed to the PR that the worktree doesn't have yet, without fetching. `↓N` means there are such commits, but they haven't been fetched, so they can't be counted.
- The changes follow the branch name without a title of their own, and show nothing for a clean worktree. They read `unknown` for a worktree whose status can't be read (a locked worktree on a volume that isn't mounted, for one).
- In the picker, the current worktree is shown grayed out under the header and can't be selected.
- When the table is wider than the terminal, the commit subject and BRANCH are shortened. PATH shows its parent directories cut to their first letter (`~/d/w/r/feat-x`), then is cut at its end down to 20 cells, and is left out when even that doesn't fit. A narrower terminal leaves out the commit's time and then the commit as well.

Icons use [Nerd Fonts](https://www.nerdfonts.com/), so set a Nerd Font in your terminal.

### Creating worktrees

When no worktree matches, `gh wt <branch>` runs `git worktree add` under `wt.basedir`. It checks out the local branch, or a remote branch of that name with tracking, or creates a new branch from `HEAD`.

Give a start point to create the new branch from somewhere other than `HEAD`:

``` console
$ gh wt fix/y main
```

A start point that exists only on a remote is used as `<remote>/<start-point>` (`checkout.defaultRemote` picks between remotes), so the new branch keeps its own name. A start point only applies to a new branch: it is refused when the worktree or the local branch already exists.

Use `-b`/`--branch` to give the branch a different name from the worktree directory:

``` console
$ gh wt -b user/my-feature my-feature
```

You can later switch to the worktree by either branch name or directory name:

``` console
$ gh wt user/my-feature  # switch by branch name
$ gh wt my-feature       # switch by directory name
```

Use `--orphan` to create a worktree with a new orphan branch, which has no history and starts from an empty working tree:

``` console
$ gh wt --orphan gh-pages
```

- It can be combined with `-b`, but not with a start point, and git refuses it when the local branch already exists.
- No files are copied into the new worktree, so that it starts clean. Hooks still run.

Flags may come before or after the names. `-b` also takes `--branch=NAME` or `-bNAME`, and `--` ends the flags.

### Opening pull requests

`gh wt -p` lists the repository's open pull requests, including ones from forks, and opens the selected one in a worktree. Like `gh wt <branch>`, it moves to the worktree if there is one and creates it otherwise. The current worktree is shown grayed out under the header, with its PR if it has one, and can't be selected. `gh wt -p <number>` with the current worktree's PR brings that worktree up to date.

![Opening a pull request with gh wt -p](docs/demo/pr.gif)

``` console
$ gh wt -p          # select from the list
$ gh wt -p 1720     # or #1720, the PR's URL, or its branch
```

| Column   | Content                                                                      |
| -------- | ---------------------------------------------------------------------------- |
| PR       | Number, green for open and yellow `draft` for drafts                         |
| TITLE    | PR title, shortened first when the table is wider than the terminal          |
| BRANCH   | Head branch, followed by its worktree's changes as in `gh wt`                |
| AUTHOR   | PR author                                                                    |
| UPDATED  | When the PR was last updated                                                 |
| PATH     | Worktree path if it exists (selecting moves there), `~` for home             |

PATH is shortened and left out as in the worktree table; a narrower terminal then leaves out UPDATED and AUTHOR, in that order, before TITLE gets shorter than 20 cells.

The checkout is `gh pr checkout --worktree`, so the PR opens just as gh opens it:

- The PR is fetched first. Plain `gh wt <branch>` doesn't fetch.
- The new branch tracks the PR's branch, and an existing branch is fast-forwarded (or refused when it can't be).
- A fork's branch named like the default branch gets gh's `<owner>/<branch>` name. So does one named like a local branch that isn't that PR's, so gh doesn't fast-forward your branch to it.
- A URL of another repository's PR fetches that PR, as `gh pr checkout <url>` does.

gh-wt adds only the path under `wt.basedir`, and for a new worktree, the file copies and `wt.hook`.

### Deleting worktrees

When deleting, the same target types apply: `gh wt -d feature-branch`, `gh wt -d .`, `gh wt -d ../sibling`. Several targets can be given at once; processing stops at the first one that fails.

`-d` is the safe form and stops short when something would be lost:

- If the worktree has modified or untracked files, nothing is deleted. Ignored files such as `node_modules` don't count.
- If the branch is not fully merged, the worktree is removed but the branch is kept. git explains why and gh-wt still exits `0`, so read the message rather than only the exit code.

`-D` skips both checks and removes the worktree together with its branch.

- A target with no worktree but a local branch deletes just the branch (except a protected one, see below).
- A worktree whose directory was already removed is cleaned up as well.
- Parent directories left empty under `wt.basedir` (e.g. `feat/` after deleting `feat/x`) are removed.
- When the current worktree is deleted, the shell integration moves you to the main repository.

### Renaming worktrees

Use `-m` (`-M` to force) to rename a worktree's directory and branch in a single operation. With one argument, the current worktree is renamed; with two, an explicit worktree is renamed:

``` console
$ gh wt -m new-name                 # rename the current worktree to "new-name"
$ gh wt -m old-name new-name        # rename worktree "old-name" to "new-name"
```

- The directory moves to `<wt.basedir>/<new-name>`.
- The branch is renamed first, so git refuses invalid or conflicting names before anything on disk changes. If the directory then fails to move, the branch is renamed back.
- An existing destination, or a destination inside another worktree, stops the rename.
- `-M` overwrites an existing branch of the new name and moves a locked worktree.
- Parent directories left empty under `wt.basedir` (e.g. `feat/` after renaming `feat/x`) are removed.

When invoked through the shell integration on the current worktree, the shell function `cd`s to the new path (the same way `-d` returns to the main repository after deleting the current worktree).

> [!NOTE]
> The default branch and the branch the main repository's `HEAD` points to are protected.
> - `-d`/`-D` remove their worktrees but keep the branches, and refuse a branch that has no worktree.
> - `-m`/`-M` refuses to rename them.
> - The main working tree and detached worktrees can't be renamed either.
>
> The default branch is `origin/HEAD`, else `init.defaultBranch`, else `master`.

## Install

You need the [GitHub CLI](https://cli.github.com/), installed and authenticated, and a [Nerd Font](https://www.nerdfonts.com/) in your terminal.

gh-wt is for macOS and Linux. Windows binaries are published but untested: copying files (`wt.copy*`) calls `cp`, hooks run with `sh`, and the shell function is for zsh, so it needs an environment that has them, such as WSL or Git Bash.

``` console
$ gh extension install meltin/gh-wt
```

## Shell Integration

An extension runs as a child process, so it can't change your shell's directory. Add the following to your `~/.zshrc` to move into the worktree:

``` zsh
eval "$(gh wt --init zsh)"
```

> [!IMPORTANT]
> The shell integration creates a `gh()` wrapper function to enable automatic directory switching with `gh wt <branch>`. This wrapper intercepts only `gh wt` commands and passes all other gh commands through unchanged. If you have other tools or customizations that also wrap the `gh` command, there may be conflicts.

The `cd` is performed by that wrapper function, so it only exists in a shell that ran the line above. The extension itself always prints the resulting worktree path as the **last line of stdout**, which is what the wrapper reads. git's and gh's own output and [hook](#wthook) output all go to stderr, so scripts, editors and other tools can rely on the last line:

``` console
$ WT=$(gh wt feature-branch | tail -1)
$ git -C "$WT" status
```

- The exit status is passed through. If a hook fails after creating a worktree, you are still moved into it and the status is non-zero.
- `gh wt -l`, `gh wt -h`/`--help` and `gh wt --init` run directly, keeping colors and the terminal width.
- Put the line after `gh` is on your `PATH`. It replaces any `gh` function defined before it, and works with a `gh` alias and `setopt nounset`.
- Upgrading gh-wt updates the function the next time you open a shell. Run `gh wt --init zsh` to read it.

## Configuration

Configuration is done via `git config`, with the same keys as git-wt, so an existing git-wt setup keeps working.

#### `wt.basedir`

Worktree base directory.

``` console
$ git config wt.basedir "../{gitroot}-worktrees"
```

Supported template variables:
- `{gitroot}`: repository root directory name

A relative path is under the repository, and `~` is your home directory.

Default: `.wt`

#### `wt.copyignored`

Copy files ignored by `.gitignore` (e.g., `.env`) to new worktrees.

``` console
$ git config wt.copyignored true
```

Default: `false`

#### `wt.copyuntracked`

Copy untracked files (not yet added to git) to new worktrees.

``` console
$ git config wt.copyuntracked true
```

Default: `false`

#### `wt.copymodified`

Copy modified files (tracked but with uncommitted changes) to new worktrees.

``` console
$ git config wt.copymodified true
```

Default: `false`

> [!NOTE]
> - Files are copied from the worktree you run gh-wt in. The worktree base directory (`wt.basedir`) is always excluded.
> - On macOS, files are cloned with `cp -c` (APFS copy-on-write), so large directories such as `node_modules` copy quickly.

#### `wt.hook`

Commands to run after creating a new worktree. Hooks run in the new worktree directory.

``` console
$ git config --add wt.hook "npm install"
$ git config --add wt.hook "go generate ./..."
```

> [!NOTE]
> - Hooks only run when **creating** a new worktree, not when switching to an existing one.
> - If a hook fails, the remaining hooks are skipped and `gh wt` exits with an error. The worktree has already been created at that point, so the shell integration still `cd`s into it, and the non-zero exit code stays visible to scripts.
> - Hooks run through `sh -c`, so a hook whose failure should be tolerated can be written as `git config --add wt.hook "direnv allow || true"`.

#### Not supported yet

`wt.copy`, `wt.nocopy`, `wt.symlink`, `wt.deletehook`, `wt.remover`, `wt.nocd`, `wt.relative` and git-wt's per-invocation flags (`--basedir`, `--copyignored`, ...) are not supported yet ([#3](https://github.com/meltin/gh-wt/issues/3)).
