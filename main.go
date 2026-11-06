package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
)

const helpText = `Switch between git worktrees, creating them as needed.

USAGE
  gh wt [<branch|worktree|path>]
  gh wt -d|-D <branch|worktree|path>...
  gh wt [flags]

FLAGS
  -l, --list           List worktrees with status, PR and last commit
  -d, --delete         Delete worktrees and their branches (safe)
  -D, --force-delete   Delete even with changes or an unmerged branch
  -h, --help           Show help for command

EXAMPLES
  $ gh wt            # Select a worktree and print its path
  $ gh wt feat/x     # Print the path of the worktree for feat/x,
                     # creating the worktree (and branch) if needed
  $ gh wt -l         # List worktrees
  $ gh wt -d feat/x  # Delete the worktree and branch feat/x

A name is always a target, so "gh wt list" creates a worktree named list.

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
	path     string
	branch   string // short name, empty when detached
	prunable bool   // directory is gone; only delete deals with these
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
	case args[0] == "-d" || args[0] == "--delete" || args[0] == "-D" || args[0] == "--force-delete":
		if len(args) < 2 {
			fail("usage: gh wt -d <branch|worktree|path>...")
		}
		dir, err := deleteTargets(args[1:], args[0] == "-D" || args[0] == "--force-delete")
		if dir != "" {
			fmt.Println(dir) // the current worktree is gone; let the shell function cd out
		}
		if err != nil {
			fail(err.Error())
		}
	case strings.HasPrefix(args[0], "-"):
		fail(fmt.Sprintf("unknown flag: %s", args[0]))
	case len(args) == 1:
		switchTo(args[0])
	default:
		fail("too many arguments")
	}
}

func selectWorktree() {
	wts := liveWorktrees()
	current := currentRoot()
	if !slices.ContainsFunc(wts, func(wt worktree) bool { return !samePath(wt.path, current) }) {
		fail("no other worktrees")
	}

	rows := collectRows(wts, current)
	width := 0
	if w, _, err := term.GetSize(os.Stderr.Fd()); err == nil {
		width = w - 3 // huh's "┃ " border plus a spare cell
	}
	header, lines := renderTable(rows, width)

	// huh has no disabled options, so the current worktree goes into the
	// description under the header, where it shows but cannot be selected.
	description := header
	var options []huh.Option[string]
	for i, r := range rows {
		if r.current {
			description += "\n" + grayStyle.Render(iconCurrent+" "+ansi.Strip(lines[i]))
			continue
		}
		options = append(options, huh.NewOption(lines[i], r.wt.path))
	}

	var selected string
	err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Select a worktree:").
			Description(description).
			Options(options...).
			Value(&selected),
	)).WithOutput(os.Stderr).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, grayStyle.Render("Operation cancelled."))
		os.Exit(1)
	}
	fmt.Println(selected)
}

// switchTo prints the path of the worktree matching name as a branch, a
// directory under wt.basedir, or a path. Without a match it creates the
// worktree (and the branch if needed) under wt.basedir, like git-wt.
func switchTo(name string) {
	if name == "" {
		fail("empty target")
	}
	_, base := repoBase()
	if wt, ok := findWorktree(liveWorktrees(), base, name); ok {
		fmt.Println(wt.path)
		return
	}
	createWorktree(name, filepath.Join(base, name), base)
}

// repoBase returns the main repository root and the expanded wt.basedir.
func repoBase() (mainRoot, base string) {
	commonDir, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		exitWithStatus(err)
	}
	// The main repository is the parent of the shared .git, even from a linked
	// worktree; a bare repository is the common dir itself (as in git-wt).
	mainRoot = commonDir
	if filepath.Base(commonDir) == ".git" {
		mainRoot = filepath.Dir(commonDir)
	}
	baseCfg, _ := git("config", "--get", "wt.basedir")
	home, _ := os.UserHomeDir()
	return mainRoot, expandBaseDir(baseCfg, mainRoot, home)
}

// findWorktree matches name as a branch, then a directory under base, then a
// path, in separate passes like git-wt, so a branch name always wins.
func findWorktree(wts []worktree, base, name string) (worktree, bool) {
	if name == "" {
		return worktree{}, false
	}
	abs, _ := filepath.Abs(name)
	for _, match := range []func(worktree) bool{
		func(wt worktree) bool { return wt.branch == name },
		func(wt worktree) bool { return samePath(wt.path, filepath.Join(base, name)) },
		func(wt worktree) bool { return samePath(wt.path, abs) },
	} {
		if i := slices.IndexFunc(wts, match); i >= 0 {
			return wts[i], true
		}
	}
	return worktree{}, false
}

// deleteTargets removes each target's worktree and local branch like git-wt's
// -d (safe) and -D (force), stopping at the first error. A dirty worktree is
// refused by git worktree remove; an unmerged or protected branch is kept with a
// message. It returns the main repository root when the current worktree was
// removed, so the shell function can move out of it.
func deleteTargets(targets []string, force bool) (string, error) {
	wts := mustWorktrees() // including prunable ones, so a vanished worktree can be cleaned up
	mainRoot, base := repoBase()
	current := currentRoot()
	// Keep the default branch, and the branch the main repository's HEAD points
	// to: in a bare repository nothing else stops git from deleting it.
	protected := []string{defaultBranch()}
	if head, err := git("-C", mainRoot, "symbolic-ref", "--short", "HEAD"); err == nil {
		protected = append(protected, head)
	}
	removeArgs, branchFlag := []string{"-C", mainRoot, "worktree", "remove"}, "-d"
	if force {
		removeArgs, branchFlag = append(removeArgs, "--force"), "-D"
	}

	// Resolve every target before removing anything: paths are relative to a
	// cwd that may be gone afterwards, and duplicates must be handled once.
	type target struct {
		name string
		wt   worktree
		ok   bool
	}
	resolved := make([]target, len(targets))
	for i, name := range targets {
		if name == "" {
			return "", errors.New("empty target")
		}
		wt, ok := findWorktree(wts, base, name)
		resolved[i] = target{name, wt, ok}
	}

	cdMain := ""
	done := map[string]bool{}
	for _, t := range resolved {
		name, wt := t.name, t.wt
		key := "branch " + name
		if t.ok {
			key = wt.path
		}
		if done[key] {
			continue
		}
		done[key] = true

		if !t.ok {
			// Run from mainRoot: an earlier target may have removed the current directory.
			if _, err := git("-C", mainRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+name); err != nil {
				return cdMain, fmt.Errorf("no worktree or branch found for %q", name)
			}
			if slices.Contains(protected, name) {
				return cdMain, fmt.Errorf("cannot delete default branch %q", name)
			}
			if err := gitLoud("-C", mainRoot, "branch", branchFlag, name); err != nil {
				return cdMain, fmt.Errorf("failed to delete branch %q", name)
			}
			continue
		}

		isCurrent := samePath(wt.path, current) // before removal, while the path still resolves
		if err := gitLoud(append(removeArgs, wt.path)...); err != nil {
			return cdMain, fmt.Errorf("failed to remove worktree %q", name)
		}
		if isCurrent {
			cdMain = mainRoot
		}
		_, branchErr := git("-C", mainRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+wt.branch)
		switch {
		case wt.branch == "" || branchErr != nil:
			note("Deleted worktree %s", tilde(wt.path))
		case slices.Contains(protected, wt.branch):
			note("Deleted worktree %q (branch is default, not deleted)", wt.branch)
		case gitLoud("-C", mainRoot, "branch", branchFlag, wt.branch) != nil:
			note("Deleted worktree, but kept unmerged branch %q (use -D to force)", wt.branch)
		default:
			note("Deleted worktree %q", wt.branch)
		}
	}
	return cdMain, nil
}

// defaultBranch is origin/HEAD's branch, else init.defaultBranch, else master (as in git-wt).
func defaultBranch() string {
	if b, err := git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(b, "origin/")
	}
	if b, _ := git("config", "--get", "init.defaultBranch"); b != "" {
		return b
	}
	return "master"
}

// gitLoud runs git with all output on stderr, keeping stdout for the path.
func gitLoud(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func note(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// expandBaseDir resolves git-wt's wt.basedir (default ".wt"): {gitroot} is the
// main repository's directory name, "~" is home, and a relative path is under
// the main repository.
func expandBaseDir(cfg, mainRoot, home string) string {
	if cfg == "" {
		cfg = ".wt"
	}
	cfg = strings.ReplaceAll(cfg, "{gitroot}", filepath.Base(mainRoot))
	switch {
	case cfg == "~":
		return home
	case strings.HasPrefix(cfg, "~/"):
		return filepath.Join(home, cfg[2:])
	case filepath.IsAbs(cfg):
		return filepath.Clean(cfg)
	}
	return filepath.Join(mainRoot, cfg)
}

// createWorktree adds a worktree for branch name at path, checking out an
// existing local or remote branch or creating a new one from HEAD, then copies
// files and runs wt.hook. All output except the final path goes to stderr.
func createWorktree(name, path, base string) {
	src := currentRoot()
	args := []string{"worktree", "add", "-b", name, path}
	if branchExists(name) {
		args = []string{"worktree", "add", path, name} // git sets up tracking for a remote-only branch
	}
	if err := gitLoud(args...); err != nil {
		exitWithStatus(err)
	}

	if src != "" {
		copyFiles(src, path, base)
	}

	hooks, _ := git("config", "--get-all", "wt.hook")
	for _, hook := range strings.Split(hooks, "\n") {
		if hook == "" {
			continue
		}
		cmd := exec.Command("sh", "-c", hook)
		cmd.Dir = path
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			// The worktree is usable, so still hand its path to the shell function.
			fmt.Println(path)
			fail(fmt.Sprintf("hook %q failed: %v", hook, err))
		}
	}
	fmt.Println(path)
}

func branchExists(name string) bool {
	if _, err := git("show-ref", "--verify", "--quiet", "refs/heads/"+name); err == nil {
		return true
	}
	remote, _ := git("for-each-ref", "--format=%(refname)", "refs/remotes/*/"+name)
	return remote != ""
}

// copyFiles copies what git-wt's wt.copyignored, wt.copyuntracked and
// wt.copymodified select from the src worktree into dst. Directories are listed
// as one entry and copied whole, cloned on APFS with cp -c.
func copyFiles(src, dst, base string) {
	lists := []struct {
		key  string
		args []string
	}{
		{"wt.copyignored", []string{"--others", "--ignored", "--exclude-standard", "--directory"}},
		{"wt.copyuntracked", []string{"--others", "--exclude-standard", "--directory"}},
		{"wt.copymodified", []string{"--modified"}},
	}
	var entries []string
	for _, l := range lists {
		if on, _ := git("config", "--get", l.key); on != "true" { // literal "true", as git-wt reads it
			continue
		}
		out, err := git(append([]string{"-C", src, "ls-files"}, l.args...)...)
		if err == nil && out != "" {
			entries = append(entries, strings.Split(out, "\n")...)
		}
	}
	slices.Sort(entries)
	entries = slices.Compact(entries)

	cpFlags := "-RP"
	if runtime.GOOS == "darwin" {
		cpFlags = "-cRP"
	}
	// Skip the worktree base directory when it lives inside the repository,
	// or every sibling worktree would be copied in.
	skipBase := within(base, src)
	copiedDir := ""
	for _, e := range entries {
		if copiedDir != "" && strings.HasPrefix(e, copiedDir) {
			continue
		}
		from, to := filepath.Join(src, e), filepath.Join(dst, e)
		if skipBase && within(from, base) {
			continue
		}
		if strings.HasSuffix(e, "/") {
			copiedDir = e
			from += "/." // copy the contents, so an existing dst directory is merged into
		}
		err := os.MkdirAll(filepath.Dir(to), 0o755)
		if err == nil {
			err = exec.Command("cp", cpFlags, from, to).Run()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, grayStyle.Render(fmt.Sprintf("warning: failed to copy %s: %v", e, err)))
		}
	}
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func listWorktrees() {
	rows := collectRows(liveWorktrees(), currentRoot())
	width := 0
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil {
		width = w
	}
	header, lines := renderTable(rows, width)
	fmt.Println(header)
	for i, line := range lines {
		mark := " "
		if rows[i].current {
			mark = greenStyle.Render(iconCurrent)
		}
		fmt.Println(mark + " " + line)
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

// renderTable aligns rows under a header. The header starts with 2 cells of
// indent, matching the caller's mark in the list or huh's cursor in the picker;
// lines are returned without it. With maxWidth > 0 the path, subject and branch
// columns shrink, in that order, so every line fits.
func renderTable(rows []row, maxWidth int) (string, []string) {
	const colBranch, colSubject, colPath = 0, 3, 5
	titles := []string{"BRANCH", "STATUS", "PR", "LAST COMMIT", "", "PATH"}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{
			ansi.Truncate(branchCell(r.wt), 28, "…"),
			statusCell(r.status),
			prCell(r.pr),
			ansi.Truncate(r.subject, 64, "…"),
			grayStyle.Render(r.when),
			grayStyle.Render(tilde(r.wt.path)),
		}
	}

	widths := make([]int, len(titles))
	for c, t := range titles {
		widths[c] = len(t)
		for _, cs := range cells {
			widths[c] = max(widths[c], ansi.StringWidth(cs[c]))
		}
	}
	if maxWidth > 0 {
		over := 2 + 2*(len(widths)-1) - maxWidth
		for _, w := range widths {
			over += w
		}
		// Shrink to comfortable minimums first, then further if still too wide.
		for _, s := range []struct{ col, min int }{
			{colPath, 24}, {colSubject, 30}, {colBranch, 20},
			{colPath, 8}, {colSubject, 10}, {colBranch, 10},
		} {
			if over > 0 {
				cut := min(over, max(0, widths[s.col]-s.min))
				widths[s.col] -= cut
				over -= cut
			}
		}
	}

	join := func(cs []string) string {
		var b strings.Builder
		for c, s := range cs {
			if c > 0 {
				b.WriteString("  ")
			}
			s = ansi.Truncate(s, widths[c], "…")
			b.WriteString(s)
			if c < len(cs)-1 {
				b.WriteString(strings.Repeat(" ", widths[c]-ansi.StringWidth(s)))
			}
		}
		return strings.TrimRight(b.String(), " ")
	}

	header := make([]string, len(titles))
	for c, t := range titles {
		header[c] = underlineStyle.Render(t)
	}
	lines := make([]string, len(rows))
	for i, cs := range cells {
		lines[i] = join(cs)
	}
	return "  " + join(header), lines
}

func branchCell(wt worktree) string {
	if wt.branch == "" {
		return grayStyle.Render(iconCommit + " (detached)")
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

// liveWorktrees is mustWorktrees without prunable entries, whose directory is gone.
func liveWorktrees() []worktree {
	return slices.DeleteFunc(mustWorktrees(), func(wt worktree) bool { return wt.prunable })
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

// parseWorktrees parses `git worktree list --porcelain`, skipping the bare entry.
func parseWorktrees(out string) []worktree {
	var wts []worktree
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var wt worktree
		bare := false
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				wt.path = value
			case "branch":
				wt.branch = strings.TrimPrefix(value, "refs/heads/")
			case "prunable":
				wt.prunable = true
			case "bare":
				bare = true
			}
		}
		if wt.path != "" && !bare {
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
