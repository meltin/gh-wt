package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestParseWorktrees(t *testing.T) {
	out := `worktree /repo
HEAD 1111
branch refs/heads/main

worktree /wt/feat/x
HEAD 2222
branch refs/heads/feat/x

worktree /wt/detached
HEAD 3333
detached

worktree /wt/gone
HEAD 4444
branch refs/heads/gone
prunable gitdir file points to non-existent location
`
	got := parseWorktrees(out)
	want := []worktree{
		{path: "/repo", branch: "main"},
		{path: "/wt/feat/x", branch: "feat/x"},
		{path: "/wt/detached"},
		{path: "/wt/gone", branch: "gone", prunable: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseStatus(t *testing.T) {
	out := `# branch.oid 1111
# branch.head feat/x
# branch.upstream origin/feat/x
# branch.ab +2 -1
1 M. N... 100644 100644 100644 aaa bbb staged.go
1 .M N... 100644 100644 100644 aaa bbb modified.go
1 MM N... 100644 100644 100644 aaa bbb both.go
2 R. N... 100644 100644 100644 aaa bbb R100 new.go	old.go
u UU N... 100644 100644 100644 100644 aaa bbb ccc conflict.go
? untracked.go`
	got := parseStatus(out)
	want := status{ahead: 2, behind: 1, staged: 3, modified: 2, untracked: 1, conflicts: 1}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A worktree git can't read is not shown as clean.
func TestUnknownStatus(t *testing.T) {
	initStyles()
	gitEnv(t)
	s := worktreeStatus(filepath.Join(t.TempDir(), "missing"))
	if !s.unknown {
		t.Fatalf("status = %+v, want unknown", s)
	}
	if got := ansi.Strip(statusCell(s)); got != "unknown" {
		t.Errorf("statusCell = %q", got)
	}
	if got := ansi.Strip(statusCell(status{})); got != iconClean {
		t.Errorf("a clean worktree's cell = %q", got)
	}
}

// Headers git prints without an upstream, on a first commit or detached leave
// ahead and behind at 0; only entry lines count.
func TestParseStatusWithoutUpstream(t *testing.T) {
	for name, out := range map[string]string{
		"no upstream":  "# branch.oid 1111\n# branch.head feat/x\n? new.go",
		"first commit": "# branch.oid (initial)\n# branch.head main\n? new.go",
		"detached":     "# branch.oid 1111\n# branch.head (detached)\n? new.go",
	} {
		if got, want := parseStatus(out), (status{untracked: 1}); got != want {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
	if got := parseStatus(""); got != (status{}) {
		t.Errorf("clean: got %+v", got)
	}
}

// A local branch gets its PR the way gh pr view finds it: by the repository
// the branch pushes to and its name there, an open PR before a newer closed one.
func TestPRByBranch(t *testing.T) {
	pr := func(number int, state, branch, forkOwner string) pullRequest {
		p := pullRequest{Number: number, State: state, HeadRefName: branch, IsCrossRepository: forkOwner != ""}
		p.HeadRepositoryOwner.Login = forkOwner
		return p
	}
	names := prNames{def: "main", bases: []string{"up/proj"}, heads: map[string]prHead{
		"main":          {repo: "up/proj", branch: "main"},
		"feat":          {repo: "up/proj", branch: "feat"},        // pushes to the repository itself
		"done":          {repo: "Old/Name", branch: "done"},       // ... under the name it had when cloned
		"local":         {branch: "local"},                        // no remote: the repository itself, as gh assumes
		"develop":       {repo: "me/proj", branch: "develop"},     // pushes to our fork
		"review":        {repo: "someone/proj", branch: "review"}, // a fork's PR opened with gh pr checkout
		"someone/taken": {repo: "someone/proj", branch: "taken"},  // ... under another local name
		"taken":         {repo: "up/proj", branch: "taken"},
		"readonly":      {number: 2}, // refs/pull/2/head
	}}
	got := prByBranch([]pullRequest{ // newest first, as gh lists them
		pr(20, "MERGED", "main", ""),  // the default branch only counts an open PR
		pr(15, "OPEN", "develop", ""), // the repository's own develop, not our fork's
		pr(14, "OPEN", "develop", "me"),
		pr(13, "OPEN", "local", ""),
		pr(12, "OPEN", "taken", "someone"),
		pr(11, "OPEN", "review", "someone"),
		pr(10, "OPEN", "review", "other"), // another fork's branch of that name
		pr(9, "OPEN", "feat", "someone"),  // a stranger's branch named like ours
		pr(8, "CLOSED", "feat", ""),
		pr(7, "OPEN", "feat", ""),
		pr(6, "OPEN", "feat", ""),
		pr(5, "MERGED", "done", ""),
		pr(4, "CLOSED", "done", ""),
		pr(3, "OPEN", "unknown", "someone"), // no local branch
		pr(2, "OPEN", "whatever", "someone"),
	}, prNames{def: names.def, bases: append(names.bases, "old/name"), heads: names.heads})
	want := map[string]int{"feat": 7, "done": 5, "local": 13, "develop": 14, "review": 11, "someone/taken": 12, "readonly": 2}
	for branch, number := range want {
		if got[branch].Number != number {
			t.Errorf("%s: PR #%d, want #%d", branch, got[branch].Number, number)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d branches, want %d: %v", len(got), len(want), got)
	}
}

// An open PR older than the newest PRs of any state is still found.
func TestFetchPRsFindsOldOpenPR(t *testing.T) {
	gitEnv(t)
	stub := t.TempDir()
	script := `#!/bin/sh
case "$*" in
*"repo view"*) echo main ;;
*"--state all"*) echo '[{"number":10,"state":"MERGED","headRefName":"main","url":"https://github.com/up/proj/pull/10"},{"number":9,"state":"MERGED","headRefName":"done","url":"https://github.com/up/proj/pull/9"},{"number":8,"state":"CLOSED","headRefName":"retried","url":"https://github.com/up/proj/pull/8"}]' ;;
*) echo '[{"number":7,"state":"OPEN","headRefName":"retried","url":"https://github.com/up/proj/pull/7"},{"number":3,"state":"OPEN","headRefName":"long-lived","url":"https://github.com/up/proj/pull/3"}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(stub, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := t.TempDir()
	run := gitIn(t, repo)
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	for _, b := range []string{"long-lived", "retried", "done"} {
		run("branch", b)
	}
	t.Chdir(repo)

	prs, msg := fetchPRs()
	if msg != "" {
		t.Fatal(msg)
	}
	for branch, want := range map[string]int{"long-lived": 3, "retried": 7, "done": 9} {
		if prs[branch].Number != want {
			t.Errorf("%s: PR #%d, want #%d", branch, prs[branch].Number, want)
		}
	}
	// main is the default branch on GitHub, whatever the clone says: a merged
	// PR whose head was main is not its PR.
	if pr, ok := prs["main"]; ok {
		t.Errorf("main: PR #%d, want none", pr.Number)
	}
}

func TestRenderTableFits(t *testing.T) {
	initStyles()
	rows := []row{{
		wt:      worktree{path: "/very/long/" + strings.Repeat("path/", 18), branch: "issue-1783-karte-position-update-missing-constructions"},
		status:  status{ahead: 2, modified: 3},
		pr:      &pullRequest{Number: 1720, State: "OPEN", IsDraft: true},
		when:    "31 hours ago",
		subject: "fix(最近見た工事): 一覧の見出しと最初の日付見出しの間隔を詰める",
	}}
	header, lines := renderTable(rows, 100, "", 0)
	for _, l := range append(lines, header) {
		if w := ansi.StringWidth(l) + 2; w > 100 { // +2 for the mark or cursor
			t.Errorf("width %d > 100: %q", w, ansi.Strip(l))
		}
	}
	// A path that doesn't fit whole is left out: shortened, it tells nothing.
	if h, l := ansi.Strip(header), ansi.Strip(lines[0]); strings.Contains(h, "PATH") || strings.Contains(l, "/very/long") {
		t.Errorf("the path should be left out in 100 cells:\n%s\n%s", h, l)
	}
	// Narrower still, the commit goes too, but the branch, its changes and its PR stay.
	_, lines = renderTable(rows, 40, "", 0)
	if l := ansi.Strip(lines[0]); ansi.StringWidth(l)+2 > 40 || !strings.Contains(l, "#1720") || strings.Contains(l, "31 hours") {
		t.Errorf("40 cells should show the branch, changes and PR only: %q", l)
	}
	// Without a width to fit, as in a pipe, nothing is left out or shortened.
	if _, lines = renderTable(rows, 0, "", 0); !strings.Contains(ansi.Strip(lines[0]), rows[0].wt.path) {
		t.Errorf("the path should be whole without a width: %q", ansi.Strip(lines[0]))
	}
}

func TestExpandBaseDir(t *testing.T) {
	const root, home = "/src/repo", "/home/me"
	for cfg, want := range map[string]string{
		"":                          "/src/repo/.wt",
		"../{gitroot}-wt":           "/src/repo-wt",
		"~/dev/worktrees/{gitroot}": "/home/me/dev/worktrees/repo",
		"/abs/{gitroot}/":           "/abs/repo",
	} {
		if got := expandBaseDir(cfg, root, home); got != want {
			t.Errorf("expandBaseDir(%q) = %q, want %q", cfg, got, want)
		}
	}
	if !within("/a/b/c", "/a/b") || within("/a/bc", "/a/b") || !within("/a/b", "/a/b") {
		t.Error("within")
	}
}

func TestDeleteTargets(t *testing.T) {
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := gitIn(t, repo)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("config", "init.defaultBranch", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("worktree", "add", "-q", "-b", "merged", filepath.Join(repo, ".wt/merged"))
	run("worktree", "add", "-q", "-b", "nest/x", filepath.Join(repo, ".wt/nest/x"))
	run("worktree", "add", "-q", "-b", "unmerged", filepath.Join(repo, ".wt/unmerged"))
	run("-C", filepath.Join(repo, ".wt/unmerged"), "commit", "-q", "--allow-empty", "-m", "wip")
	run("branch", "only-branch")
	t.Chdir(filepath.Join(repo, ".wt/merged"))

	cd, err := deleteTargets([]string{"merged", "unmerged", "only-branch", "nest/x"}, false)
	if err != nil || cd != repo {
		t.Fatalf("safe delete: cd=%q err=%v, want cd=%q", cd, err, repo)
	}
	t.Chdir(repo)
	branches, _ := git("branch", "--format=%(refname:short)")
	if branches != "main\nunmerged" {
		t.Fatalf("after -d, branches = %q, want main and the unmerged branch kept", branches)
	}
	if _, err := os.Stat(filepath.Join(repo, ".wt/nest")); !os.IsNotExist(err) {
		t.Fatal("deleting nest/x should remove the empty .wt/nest")
	}

	// From an empty parent that gets removed, the shell is sent to the main repo.
	run("worktree", "add", "-q", "-b", "nest2/y", filepath.Join(repo, ".wt/nest2/y"))
	t.Chdir(filepath.Join(repo, ".wt/nest2"))
	// A later target failing must not skip it.
	if cd, err := deleteTargets([]string{"nest2/y", "nope"}, false); err == nil || cd != repo {
		t.Fatalf("delete from an emptied parent: cd=%q err=%v, want %q and an error for nope", cd, err, repo)
	}
	t.Chdir(repo)
	if len(mustWorktrees()) != 1 {
		t.Fatalf("worktrees left: %v", mustWorktrees())
	}
	if _, err := deleteTargets([]string{"unmerged"}, true); err != nil {
		t.Fatalf("-D unmerged branch: %v", err)
	}
	if _, err := deleteTargets([]string{"main"}, true); err == nil {
		t.Fatal("deleting the default branch should fail")
	}
	if _, err := deleteTargets([]string{"nope"}, false); err == nil {
		t.Fatal("unknown target should fail")
	}
}

// TestDeleteTargetsEdgeCases covers cases found in review: lookup precedence,
// the main HEAD branch, vanished worktrees, empty and duplicate targets.
func TestDeleteTargetsEdgeCases(t *testing.T) {
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := gitIn(t, repo)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "trunk") // no origin/HEAD or init.defaultBranch: default falls back to master
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("branch", "a")
	run("branch", "b")
	run("worktree", "add", "-q", filepath.Join(repo, ".wt/a"), "b") // dir "a" holds branch b
	run("worktree", "add", "-q", filepath.Join(repo, ".wt/b"), "a") // dir "b" holds branch a
	run("worktree", "add", "-q", "-b", "gone", filepath.Join(repo, ".wt/gone"))
	if err := os.RemoveAll(filepath.Join(repo, ".wt/gone")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(repo, ".wt/a"))

	if _, err := deleteTargets([]string{""}, true); err == nil {
		t.Fatal("empty target should fail")
	}
	// "a" is a branch name first: it must remove .wt/b (branch a), not .wt/a.
	if _, err := deleteTargets([]string{"a", "a"}, false); err != nil {
		t.Fatalf("duplicate target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".wt/a")); err != nil {
		t.Fatalf(".wt/a should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".wt/b")); !os.IsNotExist(err) {
		t.Fatal(".wt/b should be removed")
	}
	if _, err := deleteTargets([]string{"gone"}, false); err != nil {
		t.Fatalf("vanished worktree: %v", err)
	}
	branches, _ := git("-C", repo, "branch", "--format=%(refname:short)")
	if branches != "b\ntrunk" {
		t.Fatalf("branches = %q, want b and trunk", branches)
	}

	// Bare repository whose HEAD is trunk while the default falls back to master:
	// trunk's worktree can go, but the branch must stay or HEAD would dangle.
	bare := filepath.Join(dir, "bare.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", repo, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}
	repo = bare
	run = gitIn(t, repo)
	run("worktree", "add", "-q", "-b", "feat", filepath.Join(bare, "wt/feat"))
	run("worktree", "add", "-q", filepath.Join(bare, "wt/trunk"), "trunk")
	t.Chdir(filepath.Join(bare, "wt/feat"))
	if _, err := deleteTargets([]string{"trunk"}, true); err != nil {
		t.Fatalf("removing the HEAD branch's worktree: %v", err)
	}
	_, err := deleteTargets([]string{"trunk"}, true)
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("deleting the bare HEAD branch: err = %v, want protected", err)
	}
	if _, err := git("-C", bare, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		t.Fatal("bare HEAD dangles")
	}
}

func TestRenameWorktree(t *testing.T) {
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := gitIn(t, repo)
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repo, rel))
		return err == nil
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("worktree", "add", "-q", "-b", "feat/x", filepath.Join(repo, ".wt/feat/x"))
	run("worktree", "add", "-q", "-b", "other", filepath.Join(repo, ".wt/other"))
	run("worktree", "add", "-q", "--detach", filepath.Join(repo, ".wt/det"))
	t.Chdir(filepath.Join(repo, ".wt/feat/x"))

	// Current worktree, one argument: directory and branch move, empty feat/ is removed.
	got, err := renameWorktree(nil, "y", false)
	if want := filepath.Join(repo, ".wt/y"); err != nil || got != want {
		t.Fatalf("rename current: got %q, %v; want %q", got, err, want)
	}
	t.Chdir(repo)
	if !exists(".wt/y") || exists(".wt/feat") {
		t.Fatal("want .wt/y and no leftover .wt/feat")
	}
	if b, _ := git("-C", filepath.Join(repo, ".wt/y"), "branch", "--show-current"); b != "y" {
		t.Fatalf("branch = %q, want y", b)
	}

	// Existing branch: -m refuses before moving anything, -M overwrites it.
	if _, err := renameWorktree([]string{"y"}, "other", false); err == nil || !exists(".wt/y") {
		t.Fatalf("-m onto an existing branch should fail and keep .wt/y: %v", err)
	}
	run("worktree", "remove", filepath.Join(repo, ".wt/other"))
	if got, err := renameWorktree([]string{"y"}, "other", true); err != nil || got != "" {
		t.Fatalf("-M onto an existing branch: %q, %v", got, err)
	}

	for _, c := range []struct {
		old, new string
	}{
		{"other", "a..b"},  // invalid branch name: nothing moves
		{"main", "trunk"},  // main working tree
		{".wt/det", "z"},   // detached
		{"other", "other"}, // unchanged
		{"nope", "z"},      // unknown
	} {
		if _, err := renameWorktree([]string{c.old}, c.new, true); err == nil {
			t.Errorf("rename %q to %q should fail", c.old, c.new)
		}
	}
	if !exists(".wt/other") {
		t.Fatal("a failed rename moved .wt/other")
	}
}

// TestRenameWorktreeConflicts covers review findings: ref conflicts must fail
// before anything moves, no worktree may nest in another, and a symlinked base
// still gets its empty parents cleaned up.
func TestRenameWorktreeConflicts(t *testing.T) {
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := gitIn(t, repo)
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("config", "wt.basedir", filepath.Join(dir, "link")) // symlinked base
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("branch", "feat")
	run("worktree", "add", "-q", "-b", "x", filepath.Join(real, "x"))
	run("worktree", "add", "-q", "-b", "bar", filepath.Join(real, "foo"))
	run("worktree", "add", "-q", "-b", "deep/a", filepath.Join(real, "deep/a"))
	t.Chdir(repo)
	branchOf := func(p string) string {
		b, _ := git("-C", p, "branch", "--show-current")
		return b
	}

	for _, c := range []struct{ old, new string }{
		{"x", "feat/y"}, // ref "feat" exists: git refuses the branch rename
		{"x", "main"},   // -M onto a branch checked out in the main worktree
		{"x", "foo/z"},  // would nest inside worktree foo
		{"x", "x/y/z"},  // would nest inside itself
	} {
		if _, err := renameWorktree([]string{c.old}, c.new, true); err == nil {
			t.Errorf("rename %q to %q should fail", c.old, c.new)
		}
		if !exists(filepath.Join(real, "x")) || branchOf(filepath.Join(real, "x")) != "x" {
			t.Fatalf("after failed rename to %q, x must be unchanged", c.new)
		}
	}
	if exists(filepath.Join(real, "feat")) || exists(filepath.Join(real, "foo/z")) || exists(filepath.Join(real, "x/y")) {
		t.Fatal("a failed rename left directories behind")
	}

	if _, err := renameWorktree([]string{"deep/a"}, "b", false); err != nil {
		t.Fatalf("rename deep/a: %v", err)
	}
	if exists(filepath.Join(real, "deep")) || branchOf(filepath.Join(real, "b")) != "b" {
		t.Fatal("want real/b on branch b and no empty real/deep under the symlinked base")
	}
}

// TestRenameWorktreeFailedMove checks that a move failing after the branch was
// renamed leaves everything as it was, including pre-existing paths under base
// and a branch that -M overwrote.
func TestRenameWorktreeFailedMove(t *testing.T) {
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	wt := filepath.Join(repo, ".wt")
	run := gitIn(t, repo)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("worktree", "add", "-q", "-b", "lk", filepath.Join(wt, "lk"))
	run("worktree", "lock", filepath.Join(wt, "lk"))
	run("branch", "ro/z")
	run("-C", filepath.Join(wt, "lk"), "commit", "-q", "--allow-empty", "-m", "lk only") // lk and ro/z differ
	if err := os.WriteFile(filepath.Join(wt, "notes"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(wt, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(wt, "ro"), 0o755) })
	t.Chdir(repo)
	roTip, _ := git("rev-parse", "ro/z")

	// The branch is renamed, then creating .wt/notes/ fails on the regular file.
	if _, err := renameWorktree([]string{"lk"}, "notes/x", false); err == nil {
		t.Fatal("rename into a path under a regular file should fail")
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "notes")); string(b) != "keep" {
		t.Fatal("a failed rename removed the pre-existing file .wt/notes")
	}
	// -M over ro/z into an unwritable directory: the move fails, ro/z comes back.
	if _, err := renameWorktree([]string{"lk"}, "ro/z", true); err == nil {
		t.Fatal("moving into an unwritable directory should fail")
	}
	if tip, _ := git("rev-parse", "ro/z"); tip != roTip {
		t.Fatalf("ro/z = %s, want the overwritten tip %s restored", tip, roTip)
	}
	if b, _ := git("-C", filepath.Join(wt, "lk"), "branch", "--show-current"); b != "lk" {
		t.Fatalf("branch = %q, want lk renamed back", b)
	}
	if _, err := os.Stat(filepath.Join(wt, "ro")); err != nil {
		t.Fatal("a failed rename removed the pre-existing directory .wt/ro")
	}
}

func TestQualifyStartPoint(t *testing.T) {
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := gitIn(t, repo)
	for _, r := range []string{"origin.git", "up.git"} {
		if out, err := exec.Command("git", "init", "-q", "--bare", filepath.Join(dir, r)).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("remote", "add", "origin", filepath.Join(dir, "origin.git"))
	run("remote", "add", "up", filepath.Join(dir, "up.git"))
	run("switch", "-q", "-c", "develop")
	run("commit", "-q", "--allow-empty", "-m", "dev")
	run("push", "-q", "origin", "develop")
	run("switch", "-q", "-c", "solo")
	run("push", "-q", "origin", "solo")
	run("switch", "-q", "main")
	run("branch", "-q", "-D", "develop", "solo")
	run("fetch", "-q", "origin")
	t.Chdir(repo)

	for sp, want := range map[string]string{
		"main":        "main",        // local branch
		"origin/solo": "origin/solo", // already qualified
		"solo":        "origin/solo", // remote-only: qualified so -b isn't dropped
		"nope":        "nope",        // unknown: git reports it
	} {
		if got, err := qualifyStartPoint(sp); err != nil || got != want {
			t.Errorf("qualifyStartPoint(%q) = %q, %v; want %q", sp, got, err, want)
		}
	}

	run("push", "-q", "up", "origin/develop:refs/heads/develop")
	run("fetch", "-q", "up")
	if _, err := qualifyStartPoint("develop"); err == nil {
		t.Error("develop on two remotes should be ambiguous")
	}
	run("config", "checkout.defaultRemote", "up")
	if got, err := qualifyStartPoint("develop"); err != nil || got != "up/develop" {
		t.Errorf("with checkout.defaultRemote=up: %q, %v", got, err)
	}
}

func TestCheckDestination(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "full/x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, wantErr := range map[string]bool{"missing": false, "empty": false, "full": true, "file": true} {
		if err := checkDestination(filepath.Join(dir, name)); (err != nil) != wantErr {
			t.Errorf("checkDestination(%s) = %v, want error %v", name, err, wantErr)
		}
	}
}

// TestCLIBranchAndOrphan runs the built binary for -b and --orphan, whose
// create paths exit the process on errors.
func TestCLIBranchAndOrphan(t *testing.T) {
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(dir, "gh-wt")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	repo := filepath.Join(dir, "repo")
	gitIn := gitIn(t, repo)
	wt := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n.wt/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn("add", ".gitignore")
	gitIn("commit", "-q", "-m", "init")
	gitIn("config", "wt.copyignored", "true")
	gitIn("config", "wt.hook", "touch hooked")

	// -b: directory and branch differ; both names find it afterwards.
	short := filepath.Join(repo, ".wt/short")
	if p, err := wt("-b", "user/long-name", "short"); err != nil || p != short {
		t.Fatalf("-b create: %q, %v", p, err)
	}
	if b := gitIn("-C", short, "branch", "--show-current"); b != "user/long-name" {
		t.Fatalf("branch = %q", b)
	}
	for _, name := range []string{"user/long-name", "short"} {
		if p, err := wt(name); err != nil || p != short {
			t.Errorf("switch by %q: %q, %v", name, p, err)
		}
	}
	// -b names a branch, not a directory: "short" holds another branch, so a
	// new branch "short" is created in .wt/elsewhere.
	elsewhere := filepath.Join(repo, ".wt/elsewhere")
	if p, err := wt("-b", "short", "elsewhere"); err != nil || p != elsewhere {
		t.Fatalf("-b with a directory's name: %q, %v", p, err)
	}
	if b := gitIn("-C", elsewhere, "branch", "--show-current"); b != "short" {
		t.Fatalf("branch = %q", b)
	}

	// --orphan (flag after the name): no history, nothing copied, hook still runs.
	orphan := filepath.Join(repo, ".wt/pages")
	if p, err := wt("pages", "--orphan"); err != nil || p != orphan {
		t.Fatalf("--orphan create: %q, %v", p, err)
	}
	if _, err := exec.Command("git", "-C", orphan, "rev-parse", "-q", "--verify", "HEAD").Output(); err == nil {
		t.Error("orphan worktree should have no commits")
	}
	if _, err := os.Stat(filepath.Join(orphan, ".env")); err == nil {
		t.Error("orphan worktree should not get copied files")
	}
	if _, err := os.Stat(filepath.Join(orphan, "hooked")); err != nil {
		t.Error("hook should run for an orphan worktree")
	}

	// -bNAME / --branch=NAME / -b=NAME, and -- before a name.
	for i, args := range [][]string{{"-bme/a", "a"}, {"--branch=me/b", "b"}, {"-b=me/c", "--", "c"}} {
		name := []string{"a", "b", "c"}[i]
		if p, err := wt(args...); err != nil || p != filepath.Join(repo, ".wt", name) {
			t.Errorf("gh wt %v: %q, %v", args, p, err)
		}
	}
	// --init zsh prints a function zsh accepts; other shells are refused.
	// The function must load over an alias and run `gh wt` under nounset.
	if out, err := wt("--init", "zsh"); err != nil || !strings.Contains(out, "command gh") {
		t.Errorf("--init zsh: %v", err)
	} else if zsh, err := exec.LookPath("zsh"); err == nil {
		fake := filepath.Join(dir, "fakebin")
		if err := os.MkdirAll(fake, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fake, "gh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		check := exec.Command(zsh, "-f", "-c", `alias gh="noglob gh"; eval "$INIT"; setopt nounset; gh wt && gh && print defined=$+functions[gh]`)
		check.Env = append(os.Environ(), "INIT="+out, "PATH="+fake+":"+os.Getenv("PATH"))
		if b, err := check.CombinedOutput(); err != nil || !strings.Contains(string(b), "defined=1") {
			t.Errorf("loading --init output: %v\n%s", err, b)
		}
	}
	if _, err := wt("--init", "fish"); err == nil {
		t.Error("--init fish should fail")
	}
	if out, err := wt("short", "-h"); err != nil || !strings.Contains(out, "USAGE") {
		t.Errorf("-h after a name should print help: %v", err)
	}

	for _, args := range [][]string{
		{"-b", "-x", "dd"},         // -b value that looks like a flag
		{"--orphan", "x", "main"},  // start point with --orphan
		{"--orphan", "main"},       // existing branch
		{"--orphan", "short"},      // existing worktree
		{"short", "main"},          // start point for an existing worktree
		{"-b"},                     // missing value
		{"-d", "short", "-b", "x"}, // -b with delete
		{"-m", "short", "--orphan"},
		{"-x", "a"},
	} {
		if _, err := wt(args...); err == nil {
			t.Errorf("gh wt %v should fail", args)
		}
	}
}

func TestPRBehind(t *testing.T) {
	gitEnv(t)
	repo := t.TempDir()
	run := gitIn(t, repo)
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "base")
	base := run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "pushed 1")
	run("commit", "-q", "--allow-empty", "-m", "pushed 2")
	head := run("rev-parse", "HEAD")
	run("reset", "-q", "--hard", base) // the worktree is 2 commits behind the PR head

	for _, c := range []struct {
		oid   string
		n     int
		known bool
	}{
		{head, 2, true},                     // fetched: counted
		{base, 0, true},                     // same as HEAD
		{"", 0, true},                       // no PR head
		{strings.Repeat("1", 40), 0, false}, // not fetched yet
	} {
		if n, known := prBehind(repo, c.oid); n != c.n || known != c.known {
			t.Errorf("prBehind(%.7s) = %d, %v; want %d, %v", c.oid, n, known, c.n, c.known)
		}
	}
	initStyles()
	if got := ansi.Strip(statusCell(status{remoteNew: true, modified: 1})); got != "↓N ~1" {
		t.Errorf("statusCell = %q", got)
	}
}

func TestRepoOf(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:me/fork.git":        "me/fork",
		"https://github.com/me/fork.git":    "me/fork",
		"https://github.com/me/fork":        "me/fork",
		"ssh://git@github.com/me/fork":      "me/fork",
		"https://github.com/up/proj/pull/7": "pull/7", // callers cut the URL of a PR first
		"":                                  "",
	} {
		if got := repoOf(url); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestPRBehindNoLazyFetch: in a partial clone, an unfetched PR head must not
// be fetched just to draw the table.
func TestPRBehindNoLazyFetch(t *testing.T) {
	gitEnv(t)
	dir := t.TempDir()
	server, clone := filepath.Join(dir, "server"), filepath.Join(dir, "clone")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main", server)
	git("-C", server, "config", "uploadpack.allowFilter", "true")
	git("-C", server, "config", "uploadpack.allowAnySHA1InWant", "true")
	git("-C", server, "commit", "-q", "--allow-empty", "-m", "base")
	git("clone", "-q", "--filter=blob:none", "file://"+server, clone)
	git("-C", server, "commit", "-q", "--allow-empty", "-m", "pushed later")
	head := git("-C", server, "rev-parse", "HEAD")

	if n, known := prBehind(clone, head); known || n != 0 {
		t.Fatalf("prBehind = %d, %v; want unknown", n, known)
	}
	check := exec.Command("git", "-C", clone, "cat-file", "-e", head+"^{commit}")
	check.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1") // checking must not fetch either
	if err := check.Run(); err == nil {
		t.Fatal("prBehind fetched the PR head into the partial clone")
	}
}

func TestPRBranch(t *testing.T) {
	pr := func(head string, cross bool) pullRequest {
		p := pullRequest{Number: 7, HeadRefName: head, IsCrossRepository: cross}
		p.HeadRepositoryOwner.Login = "someone"
		return p
	}
	names := prNames{def: "main", bases: []string{"up/proj"}, heads: map[string]prHead{
		"mine":     {repo: "up/proj", branch: "mine"},        // our own branch, pushed
		"unpushed": {branch: "unpushed"},                     // our own branch, no remote
		"theirs":   {repo: "Someone/proj", branch: "theirs"}, // gh pr checkout of a fork that can be pushed to
		"readonly": {number: 7},                              // ... and of one that can't
		"other-pr": {number: 8},
	}}
	for _, c := range []struct {
		pr   pullRequest
		want string
	}{
		{pr("feat", false), "feat"},
		{pr("feat", true), "feat"}, // no local branch of that name
		{pr("main", false), "main"},
		{pr("main", true), "someone/main"},
		{pr("mine", false), "mine"},
		{pr("mine", true), "someone/mine"}, // a stranger's branch named like ours
		{pr("unpushed", true), "someone/unpushed"},
		{pr("theirs", true), "theirs"},
		{pr("readonly", true), "readonly"},
		{pr("other-pr", true), "someone/other-pr"},
	} {
		if got := names.branch(c.pr); got != c.want {
			t.Errorf("branch(%s, fork %v) = %q, want %q", c.pr.HeadRefName, c.pr.IsCrossRepository, got, c.want)
		}
	}
}

// localBranches works out each branch's PR head by gh's rules.
func TestLocalBranches(t *testing.T) {
	gitEnv(t)
	repo := t.TempDir()
	run := gitIn(t, repo)
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("remote", "add", "origin", "git@github.com:me/proj.git")
	run("remote", "add", "upstream", "https://github.com/up/proj.git")
	for _, b := range []string{"mine", "tracks-main", "to-upstream", "on-mine", "theirs", "someone/taken", "readonly", "feat/plain"} {
		run("branch", b)
	}
	config := func(branch string, pairs ...string) {
		for i := 0; i < len(pairs); i += 2 {
			run("config", "branch."+branch+"."+pairs[i], pairs[i+1])
		}
	}
	config("mine", "remote", "origin", "merge", "refs/heads/mine")
	config("tracks-main", "remote", "upstream", "merge", "refs/heads/main") // git switch -c tracks-main upstream/main
	config("to-upstream", "remote", "origin", "pushRemote", "upstream", "merge", "refs/heads/to-upstream")
	config("on-mine", "remote", ".", "merge", "refs/heads/mine") // git switch -c on-mine -t mine
	// As gh pr checkout writes them for a fork's PR:
	config("theirs", "remote", "git@github.com:someone/proj.git", "pushRemote", "git@github.com:someone/proj.git", "merge", "refs/heads/theirs")
	config("someone/taken", "remote", "git@github.com:someone/proj.git", "pushRemote", "git@github.com:someone/proj.git", "merge", "refs/heads/taken")
	config("readonly", "remote", "upstream", "merge", "refs/pull/7/head")
	t.Chdir(repo)
	check := func(want map[string]prHead, wantBases ...string) {
		t.Helper()
		got, bases := localBranches()
		slices.Sort(bases)
		if !slices.Equal(bases, wantBases) {
			t.Errorf("bases = %v, want %v", bases, wantBases)
		}
		if len(got) != len(want) {
			t.Errorf("got %d branches: %v", len(got), got)
		}
		for name, w := range want {
			if got[name] != w {
				t.Errorf("%s: %+v, want %+v", name, got[name], w)
			}
		}
	}
	want := map[string]prHead{
		"main":          {branch: "main"},
		"mine":          {repo: "me/proj", branch: "mine"},
		"tracks-main":   {repo: "up/proj", branch: "tracks-main"}, // the name it pushes as, not what it merges
		"to-upstream":   {repo: "up/proj", branch: "to-upstream"}, // pushRemote before remote
		"on-mine":       {branch: "on-mine"},                      // its own name, not the local branch it tracks
		"theirs":        {repo: "someone/proj", branch: "theirs"},
		"someone/taken": {repo: "someone/proj", branch: "taken"},
		"readonly":      {number: 7, branch: "readonly"},
		"feat/plain":    {branch: "feat/plain"},
	}
	check(want) // two remotes and none chosen with gh repo set-default: no base from them

	run("config", "remote.upstream.gh-resolved", "base")
	check(want, "up/proj")

	// remote.pushDefault comes before branch.<name>.remote, and push.default
	// upstream pushes to the branch that is merged from.
	run("config", "remote.pushDefault", "origin")
	run("config", "push.default", "upstream")
	want["main"] = prHead{repo: "me/proj", branch: "main"}
	want["feat/plain"] = prHead{repo: "me/proj", branch: "feat/plain"}
	want["tracks-main"] = prHead{repo: "me/proj", branch: "main"}
	want["on-mine"] = prHead{repo: "me/proj", branch: "mine"}
	check(want, "up/proj")

	// With one remote, that is the base repository.
	run("remote", "remove", "upstream")
	if _, bases := localBranches(); !slices.Equal(bases, []string{"me/proj"}) {
		t.Errorf("bases with one remote = %v", bases)
	}
}

func TestRenderPRTableFits(t *testing.T) {
	initStyles()
	prs := []pullRequest{{
		Number:      1720,
		IsDraft:     true,
		HeadRefName: "fix/" + strings.Repeat("long-branch-", 6),
		Title:       "docs(e2e): GitHubでの停止理由とローカル実行手順を記載し、" + strings.Repeat("長い説明", 10),
		UpdatedAt:   time.Now().Add(-3 * 24 * time.Hour),
	}}
	prs[0].Author.Login = "someone"
	header, lines, _ := renderPRTable(prs, nil, worktree{}, prNames{def: "main"}, 100)
	if w := ansi.StringWidth(header); w > 100 { // its indent is in it already
		t.Errorf("header width %d > 100: %q", w, ansi.Strip(header))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l) + 2; w > 100 {
			t.Errorf("width %d > 100: %q", w, ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(lines[0]), "#1720 draft") {
		t.Errorf("draft PR should say so: %q", ansi.Strip(lines[0]))
	}

	// The worktree's path shows whole next to a title cut to 30 cells, and
	// otherwise not at all, leaving the title the room it had without it.
	path := "/worktrees/repo/fix/long-branch"
	wts := []worktree{{path: path, branch: prs[0].HeadRefName}}
	_, lines, _ = renderPRTable(prs, wts, worktree{}, prNames{def: "main"}, 140)
	if l := ansi.Strip(lines[0]); !strings.Contains(l, path) || ansi.StringWidth(l)+2 > 140 {
		t.Errorf("the path should show whole in 140 cells: %q", l)
	}
	// UPDATED makes room for it first,
	header, lines, _ = renderPRTable(prs, wts, worktree{}, prNames{def: "main"}, 125)
	if h, l := ansi.Strip(header), ansi.Strip(lines[0]); !strings.Contains(l, path) || strings.Contains(h, "UPDATED") || !strings.Contains(h, "AUTHOR") || ansi.StringWidth(h) > 125 || ansi.StringWidth(l)+2 > 125 {
		t.Errorf("125 cells should leave UPDATED out for the path:\n%s\n%s", h, l)
	}
	// but not for the path of the current worktree, which can't be selected.
	if header, _, _ = renderPRTable(prs, wts, wts[0], prNames{def: "main"}, 125); !strings.Contains(ansi.Strip(header), "UPDATED") {
		t.Errorf("125 cells should keep UPDATED when only the current line has a path: %q", ansi.Strip(header))
	}
	header, lines, _ = renderPRTable(prs, wts, worktree{}, prNames{def: "main"}, 100)
	if l := ansi.Strip(lines[0]); strings.Contains(l, "/worktrees") || !strings.Contains(l, "docs(e2e): GitHub") || ansi.StringWidth(l)+2 > 100 {
		t.Errorf("the path should give way to the title in 100 cells: %q", l)
	}
	if h := ansi.Strip(header); strings.Contains(h, "PATH") || ansi.StringWidth(h) > 100 {
		t.Errorf("the header should leave PATH out: %q", h)
	}
	// Narrower still, UPDATED goes before the title gets shorter than 20 cells,
	header, lines, _ = renderPRTable(prs, wts, worktree{}, prNames{def: "main"}, 80)
	if h, l := ansi.Strip(header), ansi.Strip(lines[0]); ansi.StringWidth(h) > 80 || ansi.StringWidth(l)+2 > 80 || strings.Contains(h, "UPDATED") || !strings.Contains(h, "AUTHOR") || !strings.Contains(l, "docs(e2e): GitHub") {
		t.Errorf("80 cells should leave UPDATED out for the title:\n%s\n%s", h, l)
	}
	// and then AUTHOR, but the PR, its title and branch stay.
	header, lines, _ = renderPRTable(prs, wts, worktree{}, prNames{def: "main"}, 60)
	if h, l := ansi.Strip(header), ansi.Strip(lines[0]); ansi.StringWidth(h) > 60 || ansi.StringWidth(l)+2 > 60 || strings.Contains(h, "AUTHOR") || !strings.Contains(l, "#1720 draft") {
		t.Errorf("60 cells should leave UPDATED and AUTHOR out and fit:\n%s\n%s", h, l)
	}
}

func TestPROptionsMoveCurrentUnderHeader(t *testing.T) {
	description, options := prOptions("HEADER", []string{"line0", "line1", "line2"}, []bool{false, true, false})
	if want := "HEADER\n" + iconCurrent + " line1"; ansi.Strip(description) != want {
		t.Errorf("description = %q, want %q", ansi.Strip(description), want)
	}
	// The values stay indexes into prs, so the selected PR is still the right one.
	if len(options) != 2 || options[0].Value != 0 || options[1].Value != 2 {
		t.Errorf("options = %v, want PRs 0 and 2", options)
	}
}

func TestCurrentWorktreeIsUnderHeader(t *testing.T) {
	initStyles()
	gitEnv(t)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "here").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "new"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	prs := []pullRequest{{Number: 1, HeadRefName: "other"}}
	here := worktree{path: repo, branch: "here"}

	// Without a PR on the branch, it gets a line with its changes after the PR's.
	header, lines, current := renderPRTable(prs, []worktree{here}, here, prNames{def: "main"}, 0)
	if len(lines) != 2 || !slices.Equal(current, []bool{false, true}) {
		t.Fatalf("lines = %q, current = %v", lines, current)
	}
	if l := ansi.Strip(lines[1]); !strings.Contains(l, "?1") || !strings.Contains(l, "here") || !strings.Contains(l, tilde(repo)) {
		t.Errorf("the branch's line should show it, its untracked file and its path: %q", l)
	}
	description, options := prOptions(header, lines, current)
	if !strings.Contains(ansi.Strip(description), "here") {
		t.Errorf("the branch should be under the header: %q", ansi.Strip(description))
	}
	if len(options) != 1 || options[0].Value != 0 {
		t.Errorf("options = %v, want only PR 0", options)
	}

	// With a PR on the branch, that PR's line is the current one: no extra line.
	prs = append(prs, pullRequest{Number: 2, HeadRefName: "here"})
	_, lines, current = renderPRTable(prs, []worktree{here}, here, prNames{def: "main"}, 0)
	if len(lines) != 2 || !slices.Equal(current, []bool{false, true}) {
		t.Fatalf("lines = %q, current = %v", lines, current)
	}
	if l := ansi.Strip(lines[1]); !strings.Contains(l, "?1") || !strings.Contains(l, "#2") {
		t.Errorf("the PR's line should show the worktree's untracked file: %q", l)
	}
}

// cliRepo builds gh-wt and makes a repository with one commit. git runs git in
// the repository; wt runs gh-wt there and returns its stdout and stderr.
func cliRepo(t *testing.T) (repo string, git func(args ...string) string, wt func(args ...string) (string, string, error)) {
	t.Helper()
	gitEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(dir, "gh-wt")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	repo = filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git = gitIn(t, repo)
	wt = func(args ...string) (string, string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), stderr.String(), err
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "init")
	return repo, git, wt
}

// A worktree inside another one would be deleted along with it.
func TestCLINoWorktreeInsideAnother(t *testing.T) {
	repo, git, wt := cliRepo(t)
	if _, stderr, err := wt("-b", "me/feat", "feat"); err != nil {
		t.Fatalf("create: %v\n%s", err, stderr)
	}
	out, stderr, err := wt("feat/x")
	if err == nil || out != "" || !strings.Contains(stderr, "would be inside the worktree") {
		t.Fatalf("feat/x inside the worktree feat: %q, %v\n%s", out, err, stderr)
	}
	if exists(filepath.Join(repo, ".wt/feat/x")) || strings.Contains(git("branch"), "feat/x") {
		t.Error("nothing should be created")
	}
	// A directory that only shares the parent is fine.
	if _, stderr, err := wt("other/x"); err != nil {
		t.Fatalf("other/x: %v\n%s", err, stderr)
	}
}

// -d takes a name as a branch before a directory, also for a branch without a worktree.
func TestCLIDeleteBranchBeforeDirectory(t *testing.T) {
	repo, git, wt := cliRepo(t)
	if _, stderr, err := wt("-b", "me/feat", "feat"); err != nil {
		t.Fatalf("create: %v\n%s", err, stderr)
	}
	git("branch", "feat")
	if _, stderr, err := wt("-d", "feat"); err != nil {
		t.Fatalf("-d feat: %v\n%s", err, stderr)
	}
	if branches := git("branch", "--format=%(refname:short)"); branches != "main\nme/feat" {
		t.Errorf("branches = %q, want only feat deleted", branches)
	}
	if !exists(filepath.Join(repo, ".wt/feat")) {
		t.Error("the worktree in the directory feat should be kept")
	}
	// With the branch gone, the name is the directory.
	if _, stderr, err := wt("-d", "feat"); err != nil || exists(filepath.Join(repo, ".wt/feat")) {
		t.Errorf("-d feat as a directory: %v\n%s", err, stderr)
	}
}

// Names git would quote (non-ASCII, quotes, spaces at the ends) are copied too.
func TestCLICopyQuotedNames(t *testing.T) {
	repo, git, wt := cliRepo(t)
	git("config", "wt.copyuntracked", "true")
	names := []string{"日本語.txt", `a "quoted" name.txt`, " spaced ", "dir/日本語/x.txt"}
	for _, name := range names {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path, stderr, err := wt("copy")
	if err != nil || strings.Contains(stderr, "warning") {
		t.Fatalf("create: %v\n%s", err, stderr)
	}
	for _, name := range names {
		if !exists(filepath.Join(path, name)) {
			t.Errorf("%q was not copied", name)
		}
	}
}

// wt.copymodified copies a changed file and passes over a deleted one.
func TestCLICopyModifiedSkipsDeleted(t *testing.T) {
	repo, git, wt := cliRepo(t)
	for _, name := range []string{"changed.txt", "gone.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "files")
	git("config", "wt.copymodified", "true")
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	path, stderr, err := wt("copy")
	if err != nil || strings.Contains(stderr, "warning") {
		t.Fatalf("create: %v\n%s", err, stderr)
	}
	if got, _ := os.ReadFile(filepath.Join(path, "changed.txt")); string(got) != "new" {
		t.Errorf("changed.txt = %q, want the uncommitted change", got)
	}
}

// With wt.basedir two levels deep, the directory above it is copied without
// the worktrees in it.
func TestCLICopyAroundNestedBase(t *testing.T) {
	repo, git, wt := cliRepo(t)
	git("config", "wt.basedir", "work/trees")
	git("config", "wt.copyuntracked", "true")
	for _, name := range []string{"work/notes.txt", "work/sub/deep.txt"} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"n1", "n2"} {
		path, stderr, err := wt(name)
		if err != nil || strings.Contains(stderr, "warning") {
			t.Fatalf("create %s: %v\n%s", name, err, stderr)
		}
		for _, copied := range []string{"work/notes.txt", "work/sub/deep.txt"} {
			if !exists(filepath.Join(path, copied)) {
				t.Errorf("%s: %s was not copied", name, copied)
			}
		}
		if exists(filepath.Join(path, "work/trees")) {
			t.Errorf("%s: the worktrees under work/trees were copied in", name)
		}
	}
}

// A wt.hook value that spans lines is one command; each value is a hook.
func TestCLIHookSpanningLines(t *testing.T) {
	_, git, wt := cliRepo(t)
	git("config", "wt.hook", "if true; then\n  touch first\nfi")
	git("config", "--add", "wt.hook", "touch second")
	path, stderr, err := wt("hooked")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, stderr)
	}
	for _, name := range []string{"first", "second"} {
		if !exists(filepath.Join(path, name)) {
			t.Errorf("hook did not create %s\n%s", name, stderr)
		}
	}
}

// A gh that fails without a word is still a failure, not "no pull requests".
func TestCLISilentGhFailure(t *testing.T) {
	_, _, wt := cliRepo(t)
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, stderr, err := wt("-p")
	if err == nil || strings.Contains(stderr, "no open pull requests") || !strings.Contains(stderr, "exit status 1") {
		t.Errorf("-p: %v\n%s", err, stderr)
	}
	out, stderr, err := wt("-l")
	if err != nil || !strings.Contains(out, "main") || !strings.Contains(stderr, "PR state unavailable") {
		t.Errorf("-l: %v\n%s\n%s", err, out, stderr)
	}
}

// wt.copyuntracked leaves out the ignored files of an untracked directory
// unless wt.copyignored asks for them.
func TestCLICopyUntrackedWithoutIgnored(t *testing.T) {
	repo, git, wt := cliRepo(t)
	for name, content := range map[string]string{".gitignore": ".env\n", "pkg/u.txt": "u", "pkg/.env": "secret", "pkg/deep/.env": "secret"} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".gitignore")
	git("commit", "-q", "-m", "ignore")
	git("config", "wt.copyuntracked", "true")
	path, stderr, err := wt("untracked-only")
	if err != nil || strings.Contains(stderr, "warning") {
		t.Fatalf("create: %v\n%s", err, stderr)
	}
	if !exists(filepath.Join(path, "pkg/u.txt")) {
		t.Error("the untracked file was not copied")
	}
	for _, name := range []string{"pkg/.env", "pkg/deep/.env"} {
		if exists(filepath.Join(path, name)) {
			t.Errorf("the ignored %s was copied without wt.copyignored", name)
		}
	}
	git("config", "wt.copyignored", "true")
	if path, _, err = wt("both"); err != nil || !exists(filepath.Join(path, "pkg/u.txt")) || !exists(filepath.Join(path, "pkg/deep/.env")) {
		t.Errorf("with wt.copyignored both kinds should be copied: %v", err)
	}
}

// As in git-wt, -b with the directory of an existing worktree switches to that
// worktree, and the directory is joined to wt.basedir as it is given.
func TestCLIBranchFlagWithExistingDirectory(t *testing.T) {
	repo, git, wt := cliRepo(t)
	if _, stderr, err := wt("feat"); err != nil {
		t.Fatalf("create: %v\n%s", err, stderr)
	}
	if path, stderr, err := wt("-b", "fix/2", "feat"); err != nil || path != filepath.Join(repo, ".wt/feat") {
		t.Errorf("-b fix/2 feat = %q, want the worktree feat: %v\n%s", path, err, stderr)
	}
	if branches := git("branch", "--format=%(refname:short)"); branches != "feat\nmain" {
		t.Errorf("branches = %q, want no fix/2", branches)
	}
	if path, stderr, err := wt("-b", "esc", "../outside"); err != nil || path != filepath.Join(repo, "outside") {
		t.Errorf("-b esc ../outside = %q, want %q: %v\n%s", path, filepath.Join(repo, "outside"), err, stderr)
	}
}

// gitIn returns a function that runs git in repo, fails the test when git
// does, and returns its output.
func gitIn(t *testing.T, repo string) func(args ...string) string {
	return func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
}

// gitEnv keeps the user's and the system's git config out of a test, and the
// repository of a git hook the tests may run from, and lets it commit.
func gitEnv(t *testing.T) {
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if value, ok := os.LookupEnv(key); ok {
			os.Unsetenv(key)
			t.Cleanup(func() { os.Setenv(key, value) })
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
}

// pickerRows are three worktrees (the first current) and a PR for each branch.
func pickerRows() ([]row, prLookup) {
	rows := []row{
		{wt: worktree{path: "/r", branch: "main"}, current: true},
		{wt: worktree{path: "/r/.wt/alpha", branch: "alpha"}},
		{wt: worktree{path: "/r/.wt/beta", branch: "beta"}},
		{wt: worktree{path: "/r/.wt/gamma", branch: "gamma"}},
	}
	l := prLookup{prs: make([]prRow, len(rows))}
	for i := 1; i < len(rows); i++ {
		l.prs[i] = prRow{pr: &pullRequest{Number: 100 + i, State: "MERGED"}}
	}
	return rows, l
}

func startPicker(t *testing.T, rows []row) (*picker, *string) {
	t.Helper()
	var selected string
	p := newPicker(rows, 120, 0, nil, &selected)
	p.Init()
	p.Update(tea.WindowSizeMsg{Width: 130, Height: 40})
	return p, &selected
}

func key(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// The PRs fill into the running picker without moving the cursor or hiding rows.
func TestPickerFillsInPRs(t *testing.T) {
	initStyles()
	rows, l := pickerRows()
	p, selected := startPicker(t, rows)
	if v := ansi.Strip(p.View()); strings.Contains(v, "#10") {
		t.Fatalf("PRs shown before they arrive:\n%s", v)
	}
	p.Update(key("down"))
	p.Update(l)
	v := ansi.Strip(p.View())
	for _, want := range []string{"alpha", "#101 merged", "#102 merged", "#103 merged"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	cursor := slices.IndexFunc(strings.Split(v, "\n"), func(line string) bool { return strings.Contains(line, "> ") })
	if line := strings.Split(v, "\n")[cursor]; !strings.Contains(line, "beta") {
		t.Errorf("cursor moved to %q", line)
	}
	if *selected != "/r/.wt/beta" {
		t.Errorf("selected = %q", *selected)
	}
}

// A filter keeps the lines it shows until it's cleared, then the PRs fill in.
func TestPickerHoldsPRsWhileFiltering(t *testing.T) {
	initStyles()
	rows, l := pickerRows()
	p, _ := startPicker(t, rows)
	for _, k := range []string{"/", "a"} {
		p.Update(key(k))
	}
	p.Update(l)
	if v := ansi.Strip(p.View()); !p.held || strings.Contains(v, "#10") {
		t.Fatalf("PRs shown while typing a filter (held = %v):\n%s", p.held, v)
	}
	p.Update(key("esc")) // keep the filter, leave typing
	p.Update(refreshMsg{})
	if v := ansi.Strip(p.View()); !p.held || strings.Contains(v, "#10") {
		t.Fatalf("PRs shown under a filter (held = %v):\n%s", p.held, v)
	}
	_, cmd := p.Update(key("esc")) // clear the filter
	if cmd == nil {
		t.Fatal("clearing the filter should ask for a refresh")
	}
	p.Update(refreshMsg{})
	if v := ansi.Strip(p.View()); !strings.Contains(v, "#101 merged") || !strings.Contains(v, "#103 merged") {
		t.Errorf("PRs missing after the filter is cleared:\n%s", v)
	}
}

// gh wt -l redraws once with the PRs, then quits.
func TestListModelQuitsWithPRs(t *testing.T) {
	initStyles()
	rows, l := pickerRows()
	m := &listModel{newLiveTable(120, nil)}
	m.rows = rows
	if _, cmd := m.Update(l); cmd == nil || cmd() != tea.Quit() {
		t.Error("should quit once the PRs are in")
	}
	if v := m.View(); v != "" {
		t.Errorf("the live region should be cleared for the final print, got %q", v)
	}
	if v := ansi.Strip(m.listText()); !strings.Contains(v, "#101 merged") || !strings.HasSuffix(v, "\n") {
		t.Errorf("final table:\n%q", v)
	}
}

// The lookup's result comes through the channel the table was given.
func TestLiveTableReceivesLookup(t *testing.T) {
	initStyles()
	rows, l := pickerRows()
	ch := make(chan prLookup, 1)
	ch <- l
	m := &listModel{newLiveTable(120, ch)}
	m.rows = rows
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("Init should batch the spinner and the lookup")
	}
	for _, cmd := range batch {
		if got, ok := cmd().(prLookup); ok {
			if _, quit := m.Update(got); quit == nil || !m.loaded {
				t.Error("the lookup should fill the table and quit")
			}
			return
		}
	}
	t.Fatal("no cmd returned the lookup")
}

// An open PR's head adds to the commits behind the upstream, as in gh wt -p;
// a head that isn't fetched shows as unknown.
func TestLiveTableOpenPRBehind(t *testing.T) {
	open := &pullRequest{Number: 1, State: "OPEN"}
	tbl := newLiveTable(0, nil)
	tbl.rows = []row{{status: status{behind: 5}}, {status: status{behind: 1}}, {}, {status: status{behind: 3}}}
	tbl.update(prLookup{prs: []prRow{
		{pr: open, behind: 0, known: true}, // up to date with the PR, 5 behind the upstream
		{pr: open, behind: 4, known: true},
		{pr: open}, // head not fetched
		{pr: &pullRequest{State: "MERGED"}, behind: 9, known: true}, // only open PRs count
	}})
	for i, want := range []status{{behind: 5}, {behind: 4}, {remoteNew: true}, {behind: 3}} {
		if got := tbl.rows[i].status; got != want {
			t.Errorf("row %d: status = %+v, want %+v", i, got, want)
		}
	}
}

// The PR picker shortens its columns too when the terminal gets narrower, and
// keeps the PRs' indexes as the options' values.
func TestPRPickerFitsAfterResize(t *testing.T) {
	initStyles()
	prs := []pullRequest{
		{Number: 1, HeadRefName: "here", Title: strings.Repeat("the current one ", 5)},
		{Number: 2, HeadRefName: "feat/a", Title: strings.Repeat("a long title ", 6)},
		{Number: 3, HeadRefName: "feat/b", Title: strings.Repeat("another long title ", 4)},
	}
	cells, current := prCells(prs, nil, worktree{branch: "here"}, prNames{})
	var selected int
	p := newPRPicker(cells, current, 200, &selected)
	wide := ansi.StringWidth(p.options[0].Key)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if len(p.options) != 2 || p.options[0].Value != 1 || p.options[1].Value != 2 {
		t.Fatalf("options = %v, want PRs 1 and 2", p.options)
	}
	for _, o := range p.options {
		if w := ansi.StringWidth(o.Key); w > 100-pickerMargin || w >= wide {
			t.Errorf("line is %d cells wide in a 100-cell terminal (was %d): %q", w, wide, ansi.Strip(o.Key))
		}
	}
	if v := ansi.Strip(p.View()); !strings.Contains(v, iconCurrent) {
		t.Errorf("the current PR should stay under the header:\n%s", v)
	}
}

// Narrowing the terminal shortens the columns again instead of wrapping lines.
func TestPickerFitsAfterResize(t *testing.T) {
	initStyles()
	rows, _ := pickerRows()
	for i := range rows {
		rows[i].subject = strings.Repeat("a long subject ", 5)
	}
	var selected string
	p := newPicker(rows, 200, 0, make(chan prLookup), &selected)
	p.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	for _, o := range p.options {
		if w := ansi.StringWidth(o.Key); w > 60-pickerMargin {
			t.Errorf("line is %d cells wide in a 60-cell terminal: %q", w, ansi.Strip(o.Key))
		}
	}
}

// In a narrow terminal the PRs make the lines wrap; huh must re-measure so no
// row disappears and the cursor stays on its row.
func TestPickerNarrowFill(t *testing.T) {
	initStyles()
	rows, l := pickerRows()
	for i := range rows {
		rows[i].wt.branch = "williammartin-multiaccount-" + rows[i].wt.branch
		rows[i].wt.path = "/private/tmp/demo/cli/.wt/" + rows[i].wt.branch
		rows[i].subject = "Merge pull request #14976 from cli/bagtoad/triage-ghmock-skill"
		rows[i].when = "30 hours ago"
	}
	var selected string
	p := newPicker(rows, 72-3, 0, nil, &selected)
	p.Init()
	p.Update(tea.WindowSizeMsg{Width: 72, Height: 40})
	p.Update(key("down"))
	p.Update(l)
	v := ansi.Strip(p.View())
	for _, b := range []string{"#101", "#102", "#103"} {
		if !strings.Contains(v, b) {
			t.Errorf("%q vanished after the PRs filled in:\n%s", b, v)
		}
	}
	if selected != rows[2].wt.path {
		t.Errorf("selected = %q, want %q", selected, rows[2].wt.path)
	}
}

// PRs that arrive before the window size don't measure the form at size 0.
func TestPickerLookupBeforeSize(t *testing.T) {
	initStyles()
	rows, l := pickerRows()
	var selected string
	p := newPicker(rows, 120, 0, nil, &selected)
	p.Init()
	p.Update(l)
	if v := ansi.Strip(p.View()); !strings.Contains(v, "#103 merged") {
		t.Errorf("rows gone before the size came:\n%s", v)
	}
	p.Update(tea.WindowSizeMsg{Width: 130, Height: 40})
	if v := ansi.Strip(p.View()); !strings.Contains(v, "#101 merged") || !strings.Contains(v, "#103 merged") {
		t.Errorf("view:\n%s", v)
	}
}

// The PR column's room comes from the PR numbers in the default branch's subjects.
func TestPRColumnWidth(t *testing.T) {
	initStyles()
	gitEnv(t)
	repo := t.TempDir()
	run := gitIn(t, repo)
	run("init", "-q", "-b", "main")
	run("config", "init.defaultBranch", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	t.Chdir(repo)
	if w := prColumnWidth(); w != 0 {
		t.Errorf("no PR in the history: width %d, want 0", w)
	}
	run("commit", "-q", "--allow-empty", "-m", "Merge pull request #4567 from me/x")
	run("commit", "-q", "--allow-empty", "-m", "fix: y (#89)")
	want := ansi.StringWidth(prCell(&pullRequest{Number: 4567, State: "MERGED"}))
	if w := prColumnWidth(); w != want {
		t.Errorf("width %d, want %d", w, want)
	}
}

// With the PR column's room kept, the columns don't move when the PRs arrive.
func TestNoShiftWhenPRsArrive(t *testing.T) {
	initStyles()
	rows, l := pickerRows()
	tbl := newLiveTable(160, nil)
	tbl.rows = rows
	tbl.prWidth = ansi.StringWidth(prCell(&pullRequest{Number: 103, State: "MERGED"}))
	before, linesBefore := tbl.render()
	tbl.update(l)
	after, linesAfter := tbl.render()
	if ansi.Strip(before) != ansi.Strip(after) {
		t.Errorf("header moved:\n%q\n%q", ansi.Strip(before), ansi.Strip(after))
	}
	// The cells after the PR column stay where they were.
	pathAt := func(line, path string) int {
		line = ansi.Strip(line)
		return ansi.StringWidth(line[:strings.LastIndex(line, path)])
	}
	for i, r := range rows {
		if b, a := pathAt(linesBefore[i], r.wt.path), pathAt(linesAfter[i], r.wt.path); b != a {
			t.Errorf("row %d: PATH moved from cell %d to %d", i, b, a)
		}
	}
}
