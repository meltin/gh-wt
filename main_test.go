package main

import (
	"slices"
	"testing"
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
