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

// TestRenameWorktreeConflicts covers review findings: ref conflicts must fail
// before anything moves, no worktree may nest in another, and a symlinked base
// still gets its empty parents cleaned up.
func TestRenameWorktreeConflicts(t *testing.T) {
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	wt := filepath.Join(repo, ".wt")
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(dir, "gh-wt")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	repo := filepath.Join(dir, "repo")
	gitIn := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
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

func TestOriginRepoAndInOrigin(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Chdir(repo)
	for _, url := range []string{"git@github.com:me/fork.git", "https://github.com/me/fork.git", "ssh://git@github.com/me/fork"} {
		_ = exec.Command("git", "remote", "remove", "origin").Run()
		if out, err := exec.Command("git", "remote", "add", "origin", url).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := originRepo(); got != "me/fork" {
			t.Errorf("originRepo(%s) = %q", url, got)
		}
	}
	pr := func(cross bool, head string) pullRequest {
		p := pullRequest{IsCrossRepository: cross}
		p.HeadRepository.NameWithOwner = head
		return p
	}
	for _, c := range []struct {
		pr   pullRequest
		want bool
	}{
		{pr(false, "up/proj"), true}, // same repository
		{pr(true, "Me/Fork"), true},  // from origin, which is a fork
		{pr(true, "someone/fork"), false},
	} {
		if got := inOrigin(c.pr, "me/fork"); got != c.want {
			t.Errorf("inOrigin(%+v) = %v", c.pr, got)
		}
	}
}

// TestPRBehindNoLazyFetch: in a partial clone, an unfetched PR head must not
// be fetched just to draw the table.
func TestPRBehindNoLazyFetch(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
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
		p := pullRequest{HeadRefName: head, IsCrossRepository: cross}
		p.HeadRepositoryOwner.Login = "someone"
		return p
	}
	for _, c := range []struct {
		pr   pullRequest
		want string
	}{
		{pr("feat", false), "feat"},
		{pr("feat", true), "feat"},
		{pr("main", false), "main"},
		{pr("main", true), "someone/main"},
	} {
		if got := prBranch(c.pr, "main"); got != c.want {
			t.Errorf("prBranch(%+v) = %q, want %q", c.pr, got, c.want)
		}
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
	header, lines := renderPRTable(prs, nil, "main", 100)
	for _, l := range append(lines, header) {
		if w := ansi.StringWidth(l) + 2; w > 100 {
			t.Errorf("width %d > 100: %q", w, ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(lines[0]), "#1720 draft") {
		t.Errorf("draft PR should say so: %q", ansi.Strip(lines[0]))
	}
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
	if v := ansi.Strip(p.View()); strings.Contains(v, "#10") {
		t.Fatalf("PRs shown while typing a filter:\n%s", v)
	}
	p.Update(key("esc")) // keep the filter, leave typing
	p.Update(refreshMsg{})
	if v := ansi.Strip(p.View()); strings.Contains(v, "#10") {
		t.Fatalf("PRs shown under a filter:\n%s", v)
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
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
	before, _ := tbl.render()
	tbl.update(l)
	after, _ := tbl.render()
	if ansi.Strip(before) != ansi.Strip(after) {
		t.Errorf("header moved:\n%q\n%q", ansi.Strip(before), ansi.Strip(after))
	}
}
