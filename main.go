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
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-isatty"
)

const helpText = `Interactively switch between git worktrees.

USAGE
  gh wt [worktree]
  gh wt [flags]

FLAGS
  -l, --list   List worktrees with status, PR and last commit
  -h, --help   Show help for command

EXAMPLES
  $ gh wt          # Select a worktree and print its path
  $ gh wt feat/x   # Print the path of the worktree for branch feat/x
  $ gh wt -l       # List worktrees

gh wt only prints the path. To cd into it, add the shell function
from the README to your .zshrc.
`

// Nerd Font (Octicons) glyphs.
const (
	iconBranch   = ""
	iconCommit   = ""
	iconCurrent  = ""
	iconClean    = ""
	iconPROpen   = ""
	iconPRDraft  = ""
	iconPRMerged = ""
	iconPRClosed = ""
)

// Styles are built in initStyles, after the default renderer is chosen,
// because a lipgloss style keeps the renderer it was created with.
var grayStyle, greenStyle, yellowStyle, cyanStyle, redStyle, magentaStyle, underlineStyle lipgloss.Style

func initStyles() {
	grayStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	cyanStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	redStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	magentaStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	underlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("7"))
}

type worktree struct {
	path   string
	branch string // short name, empty when detached
}

type pullRequest struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	HeadRefName string `json:"headRefName"`
}

type status struct {
	ahead, behind, staged, modified, untracked, conflicts int
}

type row struct {
	wt            worktree
	current       bool
	status        status
	pr            *pullRequest
	when, subject string
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		// stdout is captured by the shell function, so draw on stderr
		// and let lipgloss detect colors from stderr too.
		lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stderr))
	}
	initStyles()

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
	current := currentRoot()
	var others []worktree
	for _, wt := range mustWorktrees() {
		if !samePath(wt.path, current) {
			others = append(others, wt)
		}
	}
	if len(others) == 0 {
		fail("no other worktrees")
	}

	rows := collectRows(others, current)
	header, lines := renderTable(rows, false)
	options := make([]huh.Option[string], len(rows))
	for i, r := range rows {
		options[i] = huh.NewOption(lines[i], r.wt.path)
	}

	var selected string
	err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Select a worktree:").
			Description(header).
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
	rows := collectRows(mustWorktrees(), currentRoot())
	header, lines := renderTable(rows, true)
	fmt.Println(header)
	for _, line := range lines {
		fmt.Println(line)
	}
}

// collectRows gathers git status, last commit and PR for each worktree,
// fetching PRs concurrently and showing a spinner on a terminal.
func collectRows(wts []worktree, current string) []row {
	var rows []row
	var prErr string
	collect := func() {
		type prResult struct {
			prs map[string]pullRequest
			err string
		}
		ch := make(chan prResult, 1)
		go func() {
			prs, err := fetchPRs()
			ch <- prResult{prs, err}
		}()

		rows = make([]row, len(wts))
		for i, wt := range wts {
			r := row{wt: wt, current: samePath(wt.path, current)}
			if out, err := git("--no-optional-locks", "-C", wt.path, "status", "--porcelain=v2", "--branch"); err == nil {
				r.status = parseStatus(out)
			}
			if out, err := git("-C", wt.path, "log", "-1", "--format=%cr\x1f%s"); err == nil {
				r.when, r.subject, _ = strings.Cut(out, "\x1f")
			}
			rows[i] = r
		}

		res := <-ch
		prErr = res.err
		for i := range rows {
			if pr, ok := res.prs[rows[i].wt.branch]; ok && rows[i].wt.branch != "" {
				rows[i].pr = &pr
			}
		}
	}

	if isatty.IsTerminal(os.Stderr.Fd()) {
		_ = spinner.New().Title("Loading worktrees...").Output(os.Stderr).Action(collect).Run()
	} else {
		collect()
	}
	// Print after the spinner, which clears its line on exit.
	if prErr != "" {
		fmt.Fprintln(os.Stderr, grayStyle.Render("PR state unavailable: "+prErr))
	}
	return rows
}

// renderTable returns a header and one aligned line per row.
// The list adds a current-worktree mark and a PATH column; the picker omits both
// (huh draws its own 2-cell cursor where the mark would be).
func renderTable(rows []row, list bool) (string, []string) {
	titles := []string{"BRANCH", "STATUS", "PR", "LAST COMMIT"}
	if list {
		titles = append(titles, "PATH")
	}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{branchCell(r.wt, list), statusCell(r.status), prCell(r.pr), commitCell(r.when, r.subject)}
		if list {
			cells[i] = append(cells[i], grayStyle.Render(tilde(r.wt.path)))
		}
	}

	widths := make([]int, len(titles))
	for c, t := range titles {
		widths[c] = len(t)
		for _, cs := range cells {
			widths[c] = max(widths[c], ansi.StringWidth(cs[c]))
		}
	}
	join := func(cs []string) string {
		var b strings.Builder
		for c, s := range cs {
			if c > 0 {
				b.WriteString("  ")
			}
			b.WriteString(s)
			if c < len(cs)-1 {
				b.WriteString(strings.Repeat(" ", widths[c]-ansi.StringWidth(s)))
			}
		}
		return b.String()
	}

	header := make([]string, len(titles))
	for c, t := range titles {
		header[c] = underlineStyle.Render(t) + strings.Repeat(" ", widths[c]-len(t))
	}
	lines := make([]string, len(rows))
	for i, cs := range cells {
		lines[i] = join(cs)
		if list {
			mark := " "
			if rows[i].current {
				mark = greenStyle.Render(iconCurrent)
			}
			lines[i] = mark + " " + lines[i]
		}
	}
	return "  " + strings.TrimRight(join(header), " "), lines
}

// branchCell shows the branch, or for a detached worktree its path in the
// picker and "(detached)" in the list, where the path has its own column.
func branchCell(wt worktree, list bool) string {
	if wt.branch == "" && list {
		return grayStyle.Render(iconCommit + " (detached)")
	}
	if wt.branch == "" {
		return grayStyle.Render(iconCommit + " " + tilde(wt.path))
	}
	return cyanStyle.Render(iconBranch + " " + wt.branch)
}

func statusCell(s status) string {
	var parts []string
	add := func(n int, prefix string, style lipgloss.Style) {
		if n > 0 {
			parts = append(parts, style.Render(fmt.Sprintf("%s%d", prefix, n)))
		}
	}
	add(s.ahead, "↑", cyanStyle)
	add(s.behind, "↓", cyanStyle)
	add(s.conflicts, "!", redStyle)
	add(s.staged, "+", greenStyle)
	add(s.modified, "~", yellowStyle)
	add(s.untracked, "?", grayStyle)
	if len(parts) == 0 {
		return grayStyle.Render(iconClean)
	}
	return strings.Join(parts, " ")
}

func prCell(pr *pullRequest) string {
	if pr == nil {
		return ""
	}
	icon, word, style := iconPRClosed, "closed", redStyle
	switch {
	case pr.State == "MERGED":
		icon, word, style = iconPRMerged, "merged", magentaStyle
	case pr.State == "OPEN" && pr.IsDraft:
		icon, word, style = iconPRDraft, "draft", grayStyle
	case pr.State == "OPEN":
		icon, word, style = iconPROpen, "open", greenStyle
	}
	return style.Render(fmt.Sprintf("%s #%d %s", icon, pr.Number, word))
}

func commitCell(when, subject string) string {
	if when == "" {
		return ""
	}
	return grayStyle.Render(when) + "  " + ansi.Truncate(subject, 50, "…")
}

// parseStatus counts entries of `git status --porcelain=v2 --branch`.
func parseStatus(out string) status {
	var s status
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.ab "):
			_, _ = fmt.Sscanf(line, "# branch.ab +%d -%d", &s.ahead, &s.behind)
		case strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 "):
			if line[2] != '.' {
				s.staged++
			}
			if line[3] != '.' {
				s.modified++
			}
		case strings.HasPrefix(line, "u "):
			s.conflicts++
		case strings.HasPrefix(line, "? "):
			s.untracked++
		}
	}
	return s
}

// fetchPRs returns the newest PR per head branch, or gh's error message.
func fetchPRs() (map[string]pullRequest, string) {
	cmd := exec.Command("gh", "pr", "list", "--state", "all", "--limit", "200", "--json", "headRefName,number,state,isDraft")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, strings.TrimSpace(stderr.String())
	}
	var prs []pullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, err.Error()
	}
	m := make(map[string]pullRequest, len(prs))
	for _, pr := range prs { // gh returns newest first
		if _, ok := m[pr.HeadRefName]; !ok {
			m[pr.HeadRefName] = pr
		}
	}
	return m, ""
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
