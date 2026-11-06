package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/mattn/go-runewidth"
)

const helpText = `Interactively switch between git worktrees.

USAGE
  gh wt [worktree]
  gh wt [flags]

FLAGS
  -l, --list   List worktrees with changes, last commit and PR state
  -h, --help   Show help for command

EXAMPLES
  $ gh wt          # Select a worktree and print its path
  $ gh wt feat/x   # Print the path of the worktree for branch feat/x
  $ gh wt -l       # List worktrees

gh wt only prints the path. To cd into it, add the shell function
from the README to your .zshrc.
`

var (
	grayStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	cyanStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	redStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	magentaStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	underlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("7"))
)

type worktree struct {
	path   string
	branch string // short name, empty when detached
}

type pullRequest struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
}

func main() {
	args := os.Args[1:]
	switch {
	case len(args) == 0:
		selectWorktree()
	case args[0] == "-h" || args[0] == "--help":
		fmt.Print(helpText)
	case len(args) == 1 && (args[0] == "-l" || args[0] == "--list"):
		listWorktrees()
	case strings.HasPrefix(args[0], "-"):
		fail(fmt.Sprintf("unknown flag: %s", args[0]))
	case len(args) == 1:
		switchTo(args[0])
	default:
		fail("too many arguments")
	}
}

func selectWorktree() {
	wts := mustWorktrees()
	current := currentRoot()

	var options []huh.Option[string]
	for _, wt := range wts {
		if samePath(wt.path, current) {
			continue
		}
		options = append(options, huh.NewOption(label(wt), wt.path))
	}
	if len(options) == 0 {
		fail("no other worktrees")
	}

	// stdout is captured by the shell function, so draw the picker on stderr
	// and let lipgloss detect colors from stderr too.
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stderr))

	var selected string
	err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Select a worktree:").
			Options(options...).
			Value(&selected),
	)).WithOutput(os.Stderr).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, grayStyle.Render("Operation cancelled."))
		os.Exit(1)
	}
	fmt.Println(selected)
}

func switchTo(name string) {
	abs, _ := filepath.Abs(name)
	for _, wt := range mustWorktrees() {
		if wt.branch == name || samePath(wt.path, abs) {
			fmt.Println(wt.path)
			return
		}
	}
	fail(fmt.Sprintf("no worktree for %q", name))
}

func listWorktrees() {
	wts := mustWorktrees()
	current := currentRoot()

	var prs map[string]pullRequest
	if isatty.IsTerminal(os.Stderr.Fd()) {
		_ = spinner.New().
			Title("Fetching pull requests...").
			Output(os.Stderr).
			Action(func() { prs = fetchPRs() }).
			Run()
	} else {
		prs = fetchPRs()
	}

	type row struct {
		mark, branch, dirty, pr, commit, path string
	}
	rows := make([]row, 0, len(wts))
	for _, wt := range wts {
		r := row{branch: wt.branch, path: tilde(wt.path)}
		if samePath(wt.path, current) {
			r.mark = "*"
		}
		if r.branch == "" {
			r.branch = "(detached)"
		}
		if out, err := git("-C", wt.path, "status", "--porcelain"); err == nil && out != "" {
			r.dirty = "●"
		}
		if pr, ok := prs[wt.branch]; ok && wt.branch != "" {
			r.pr = fmt.Sprintf("#%d %s", pr.Number, strings.ToLower(pr.State))
		}
		if out, err := git("-C", wt.path, "log", "-1", "--format=%cr\x1f%s"); err == nil {
			when, subject, _ := strings.Cut(out, "\x1f")
			r.commit = grayStyle.Render(when) + "  " + runewidth.Truncate(subject, 50, "…")
		}
		rows = append(rows, r)
	}

	branchW, prW := len("BRANCH"), len("PR")
	for _, r := range rows {
		branchW = max(branchW, runewidth.StringWidth(r.branch))
		prW = max(prW, runewidth.StringWidth(r.pr))
	}
	commitW := len("LAST COMMIT")
	for _, r := range rows {
		commitW = max(commitW, lipgloss.Width(r.commit))
	}

	pad := func(s string, w int) string { return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s))) }
	header := func(s string, w int) string { return underlineStyle.Render(s) + strings.Repeat(" ", w-len(s)) }

	fmt.Printf("  %s    %s  %s  %s\n",
		header("BRANCH", branchW), header("PR", prW), header("LAST COMMIT", commitW), underlineStyle.Render("PATH"))
	for _, r := range rows {
		branch := cyanStyle.Render(r.branch)
		if r.branch == "(detached)" {
			branch = grayStyle.Render(r.branch)
		}
		fmt.Printf("%s %s %s  %s  %s  %s\n",
			pad(greenStyle.Render(r.mark), 1),
			pad(branch, branchW),
			pad(yellowStyle.Render(r.dirty), 1),
			pad(prStyle(r.pr).Render(r.pr), prW),
			pad(r.commit, commitW),
			grayStyle.Render(r.path))
	}
}

func prStyle(pr string) lipgloss.Style {
	switch {
	case strings.HasSuffix(pr, " open"):
		return greenStyle
	case strings.HasSuffix(pr, " merged"):
		return magentaStyle
	default:
		return redStyle
	}
}

// fetchPRs returns the newest PR per head branch. Failures are reported but not fatal.
func fetchPRs() map[string]pullRequest {
	cmd := exec.Command("gh", "pr", "list", "--state", "all", "--limit", "200", "--json", "headRefName,number,state")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, grayStyle.Render("PR state unavailable: "+strings.TrimSpace(stderr.String())))
		return nil
	}
	var prs []pullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil
	}
	m := make(map[string]pullRequest, len(prs))
	for _, pr := range prs { // gh returns newest first
		if _, ok := m[pr.HeadRefName]; !ok {
			m[pr.HeadRefName] = pr
		}
	}
	return m
}

func mustWorktrees() []worktree {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		exitWithStatus(err)
	}
	return parseWorktrees(string(out))
}

// parseWorktrees parses `git worktree list --porcelain`, skipping bare and prunable entries.
func parseWorktrees(out string) []worktree {
	var wts []worktree
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var wt worktree
		skip := false
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				wt.path = value
			case "branch":
				wt.branch = strings.TrimPrefix(value, "refs/heads/")
			case "bare", "prunable":
				skip = true
			}
		}
		if wt.path != "" && !skip {
			wts = append(wts, wt)
		}
	}
	return wts
}

func currentRoot() string {
	root, _ := git("rev-parse", "--show-toplevel")
	return root
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func label(wt worktree) string {
	if wt.branch != "" {
		return wt.branch
	}
	return tilde(wt.path)
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == home || strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}

func exitWithStatus(err error) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
