#!/bin/sh
# Records select.gif and pr.gif with vhs (brew install vhs).
# The demo is a clone of cli/cli with a few worktrees under /tmp/demo, so the
# paths in the recording stay short. Pick other PRs below once these close.
set -eu
cd "$(dirname "$0")"
demo=/private/tmp/demo # /tmp resolved, so the prompt shows ~ for it

# gh keeps its token in the keychain and its extensions under the real home.
export GH_TOKEN="$(gh auth token)" GH_CONFIG_DIR="${GH_CONFIG_DIR:-$HOME/.config/gh}" XDG_DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
export HOME="$demo"

if [ ! -d "$demo/cli" ]; then
  mkdir -p "$demo"
  git clone -q --filter=blob:none https://github.com/cli/cli.git "$demo/cli"
  cd "$demo/cli"
  echo .wt >>.git/info/exclude
  git config user.name demo && git config user.email demo@example.com
  for pr in 14845 14920 14846; do gh wt -p "$pr" >/dev/null; done # open, draft, open
  gh wt bagtoad/triage-ghmock-skill >/dev/null                     # merged PR's branch
  gh wt fix/pr-view-json-fields >/dev/null                         # no PR, local changes
  (cd .wt/fix/pr-view-json-fields &&
    echo '// TODO' >>pkg/cmd/pr/view/view.go && git commit -qam 'fix(pr view): keep requested JSON fields in order' &&
    echo '// more' >>pkg/cmd/pr/view/view.go && echo x >scratch.txt)
  (cd .wt/babakks/skip-refresh-ro-lock && echo '// note' >>README.md && git add README.md)
  cd - >/dev/null
fi

# pr.tape checks out PR #14921 into a new worktree; remove it from a previous run.
(cd "$demo/cli" && gh wt -D williammartin-multiaccount-draft-02-repository >/dev/null 2>&1) || true
vhs select.tape
vhs pr.tape
