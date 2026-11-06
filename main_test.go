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
