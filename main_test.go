package main

import (
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
	for _, withPath := range []bool{true, false} {
		header, lines := renderTable(rows, withPath, 100)
		for _, l := range append(lines, header) {
			if w := ansi.StringWidth(l) + 2; w > 100 { // +2 for the mark or cursor
				t.Errorf("withPath=%v: width %d > 100: %q", withPath, w, ansi.Strip(l))
			}
		}
	}
}
