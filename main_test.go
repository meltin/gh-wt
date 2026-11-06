package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

func TestRenderTableFits(t *testing.T) {
	initStyles()
	rows := []row{{
		wt:      worktree{path: "/very/long/" + strings.Repeat("path/", 18), branch: "issue-1783-karte-position-update-missing-constructions"},
		status:  status{ahead: 2, modified: 3},
		pr:      &pullRequest{Number: 1720, State: "OPEN", IsDraft: true},
		when:    "31 hours ago",
		subject: "fix(最近見た工事): 一覧の見出しと最初の日付見出しの間隔を詰める",
	}}
	header, lines := renderTable(rows, 100)
	for _, l := range append(lines, header) {
		if w := ansi.StringWidth(l) + 2; w > 100 { // +2 for the mark or cursor
			t.Errorf("width %d > 100: %q", w, ansi.Strip(l))
		}
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("config", "init.defaultBranch", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("worktree", "add", "-q", "-b", "merged", filepath.Join(repo, ".wt/merged"))
	run("worktree", "add", "-q", "-b", "unmerged", filepath.Join(repo, ".wt/unmerged"))
	run("-C", filepath.Join(repo, ".wt/unmerged"), "commit", "-q", "--allow-empty", "-m", "wip")
	run("branch", "only-branch")
	t.Chdir(filepath.Join(repo, ".wt/merged"))

	cd, err := deleteTargets([]string{"merged", "unmerged", "only-branch"}, false)
	if err != nil || cd != repo {
		t.Fatalf("safe delete: cd=%q err=%v, want cd=%q", cd, err, repo)
	}
	t.Chdir(repo)
	branches, _ := git("branch", "--format=%(refname:short)")
	if branches != "main\nunmerged" {
		t.Fatalf("after -d, branches = %q, want main and the unmerged branch kept", branches)
	}
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
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
