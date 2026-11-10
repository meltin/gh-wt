package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	bspinner "github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/dustin/go-humanize"
	"github.com/mattn/go-isatty"
)

const helpText = `Switch between git worktrees, creating them as needed.

USAGE
  gh wt [<branch|worktree|path> [<start-point>]]
  gh wt -b <branch> <worktree> [<start-point>]
  gh wt --orphan [-b <branch>] <worktree>
  gh wt -p [<number>|<url>|<branch>]
  gh wt -d|-D <branch|worktree|path>...
  gh wt -m|-M [<branch|worktree|path>] <new-name>
  gh wt [flags]

FLAGS
  -l, --list           List worktrees with changes, PR and last commit
  -p, --pr             Select an open PR, or give one, and open its branch
  -b, --branch NAME    Create with a branch name different from the directory
      --orphan         Create on a new branch with no history
  -d, --delete         Delete worktrees and their branches (safe)
  -D, --force-delete   Delete even with changes or an unmerged branch
  -m, --move           Rename a worktree's directory and branch
  -M, --force-move     Rename over an existing branch, or a locked worktree
      --init zsh       Print the shell function that cds into the worktree
  -h, --help           Show help for command

EXAMPLES
  $ gh wt                  # Select a worktree and print its path
  $ gh wt feat/x           # Print the path of the worktree for feat/x,
                           # creating the worktree (and branch) if needed
  $ gh wt fix/y main       # Create branch fix/y from main in a new worktree
  $ gh wt -b me/feat feat  # Directory feat, branch me/feat
  $ gh wt -p               # Select an open PR and open its branch
  $ gh wt -p 1720          # Open PR #1720's branch (a PR URL works too)
  $ gh wt -l               # List worktrees
  $ gh wt -d feat/x        # Delete the worktree and branch feat/x
  $ gh wt -m feat/y        # Rename the current worktree and branch to feat/y

A name is always a target, so "gh wt list" creates a worktree named list.

gh wt only prints the path. To cd into it, add this to your .zshrc:
  eval "$(gh wt --init zsh)"
`

// zshInit is printed by `gh wt --init zsh`. gh wt only prints a path, so this
// function wraps gh to cd into the last line of its output when it's a directory.
// `function gh` (not `gh()`) still defines it when gh is an alias, and ${1-}
// keeps it working under setopt nounset.
const zshInit = `# gh-wt (https://github.com/meltin/gh-wt): cd into the worktree gh wt prints.
function gh {
  if [[ ${1-} == wt && ${2-} != (-l|--list|-h|--help|--init) ]]; then
    local out rc last
    out=$(command gh "$@"); rc=$?
    last=${out##*$'\n'}
    if [[ -d $last ]]; then
      [[ $out == *$'\n'* ]] && print -r -- "${out%$'\n'*}"
      cd -- "$last"
    elif [[ -n $out ]]; then
      print -r -- "$out"
    fi
    return $rc
  else
    command gh "$@"
  fi
}
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
var grayStyle, greenStyle, yellowStyle, cyanStyle, redStyle, purpleStyle, underlineStyle lipgloss.Style

func initStyles() {
	grayStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	cyanStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	redStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	purpleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A371F7"))
	underlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("7"))
}

type worktree struct {
	path     string
	branch   string // short name, empty when detached
	prunable bool   // directory is gone; only delete deals with these
}

type pullRequest struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	IsDraft           bool   `json:"isDraft"`
	HeadRefName       string `json:"headRefName"`
	HeadRefOid        string `json:"headRefOid"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	HeadRepository    struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	// Only for gh wt -p.
	HeadRepositoryOwner struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updatedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

type status struct {
	ahead, behind, staged, modified, untracked, conflicts int
	remoteNew                                             bool // the PR has commits not fetched yet
	unknown                                               bool // git status failed
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
	if !(len(args) == 1 && (args[0] == "-l" || args[0] == "--list")) {
		// Only the list is styled on stdout. Everything else styled (pickers,
		// warnings) goes to stderr, as stdout is captured by the shell function,
		// so let lipgloss detect colors from stderr.
		lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stderr))
	}
	initStyles()

	switch {
	case len(args) == 0:
		selectWorktree()
	case slices.Contains(args, "-h") || slices.Contains(args, "--help"):
		fmt.Print(helpText)
	case args[0] == "--init":
		switch {
		case len(args) != 2:
			fail("usage: gh wt --init zsh")
		case args[1] != "zsh":
			fail(fmt.Sprintf("unsupported shell %q (zsh only)", args[1]))
		}
		fmt.Print(zshInit)
	case len(args) == 1 && (args[0] == "-l" || args[0] == "--list"):
		listWorktrees()
	case args[0] == "-d" || args[0] == "--delete" || args[0] == "-D" || args[0] == "--force-delete":
		if len(args) < 2 {
			fail("usage: gh wt -d <branch|worktree|path>...")
		}
		rejectFlags(args[1:], args[0])
		dir, err := deleteTargets(args[1:], args[0] == "-D" || args[0] == "--force-delete")
		if dir != "" {
			fmt.Println(dir) // the current worktree is gone; let the shell function cd out
		}
		if err != nil {
			exitWithStatus(err)
		}
	case args[0] == "-p" || args[0] == "--pr":
		rejectFlags(args[1:], args[0])
		switch len(args) {
		case 1:
			selectPR()
		case 2:
			openPR(args[1])
		default:
			fail("usage: gh wt -p [<number>|<url>|<branch>]")
		}
	case args[0] == "-m" || args[0] == "--move" || args[0] == "-M" || args[0] == "--force-move":
		if len(args) < 2 || len(args) > 3 {
			fail("usage: gh wt -m [<branch|worktree|path>] <new-name>")
		}
		rejectFlags(args[1:], args[0])
		dir, err := renameWorktree(args[1:len(args)-1], args[len(args)-1], args[0] == "-M" || args[0] == "--force-move")
		if err != nil {
			exitWithStatus(err)
		}
		if dir != "" {
			fmt.Println(dir) // the current worktree moved; let the shell function follow it
		}
	default:
		createOrSwitch(args)
	}
}

// rejectFlags fails on any flag after -d/-D/-m/-M; -b, --orphan and others
// only apply to creating (branch names can't start with "-").
func rejectFlags(args []string, mode string) {
	if i := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") }); i >= 0 {
		fail(fmt.Sprintf("cannot use %s with %s", args[i], mode))
	}
}

// createOrSwitch parses [-b <branch>] [--orphan] <worktree> [<start-point>],
// with the flags in any position like git-wt.
func createOrSwitch(args []string) {
	var pos []string
	branch, orphan, flagsDone := "", false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		value, inline := "", false // -bNAME, -b=NAME, --branch=NAME
		for _, prefix := range []string{"--branch=", "-b="} {
			if v, ok := strings.CutPrefix(a, prefix); ok && !flagsDone {
				value, inline = v, true
			}
		}
		if v, ok := strings.CutPrefix(a, "-b"); ok && v != "" && !inline && !flagsDone {
			value, inline = v, true
		}
		switch {
		case flagsDone || !strings.HasPrefix(a, "-"):
			pos = append(pos, a)
		case a == "--":
			flagsDone = true
		case inline || a == "-b" || a == "--branch":
			if !inline && i+1 < len(args) {
				i++
				value = args[i]
			}
			if value == "" || strings.HasPrefix(value, "-") {
				fail("-b needs a branch name")
			}
			branch = value
		case a == "--orphan":
			orphan = true
		case a == "-l" || a == "--list":
			fail(fmt.Sprintf("%s takes no other arguments", a))
		case slices.Contains([]string{"-d", "--delete", "-D", "--force-delete", "-m", "--move", "-M", "--force-move", "-p", "--pr"}, a):
			fail(fmt.Sprintf("%s must come first and can't be combined with -b or --orphan", a))
		default:
			fail(fmt.Sprintf("unknown flag: %s", a))
		}
	}
	switch {
	case len(pos) == 0:
		fail("usage: gh wt [-b <branch>] [--orphan] <branch|worktree|path> [<start-point>]")
	case len(pos) > 2:
		fail("too many arguments: expected <branch|worktree|path> [<start-point>]")
	case pos[0] == "":
		fail("empty target")
	case len(pos) == 2 && pos[1] == "":
		fail(`invalid start point ""`)
	case len(pos) == 2 && orphan:
		fail("cannot use a start point with --orphan")
	}
	if branch == "" {
		branch = pos[0]
	}
	if strings.HasPrefix(branch, "-") { // only reachable after --; git can't name a branch so
		fail(fmt.Sprintf("invalid branch name %q", branch))
	}
	startPoint := ""
	if len(pos) == 2 {
		startPoint = pos[1]
	}
	switchTo(pos[0], branch, startPoint, orphan)
}

func selectWorktree() {
	wts := liveWorktrees()
	current := currentRoot()
	if !slices.ContainsFunc(wts, func(wt worktree) bool { return !samePath(wt.path, current) }) {
		fail("no other worktrees")
	}
	prs := lookupPRs(wts)
	var rows []row
	var prWidth int
	spin("Loading worktrees...", func() { rows, prWidth = localRows(wts, current), prColumnWidth() })

	var selected string
	p := newPicker(rows, pickerWidth(), prWidth, prs, &selected)
	_, err := tea.NewProgram(p, tea.WithOutput(os.Stderr), tea.WithReportFocus()).Run()
	if err != nil || p.form.State != huh.StateCompleted {
		pickerStopped(err)
	}
	p.reportErr()
	fmt.Println(selected)
}

// pickerMargin is what a picker's table can't use of the terminal's width:
// huh's "┃ " border plus a spare cell.
const pickerMargin = 3

// pickerWidth is the width a picker's table has, 0 when it can't be known.
func pickerWidth() int {
	w, _, err := term.GetSize(os.Stderr.Fd())
	if err != nil {
		return 0
	}
	return w - pickerMargin
}

// pickerStopped ends a picker that didn't complete: cancelled by the user, or
// failed with err (no terminal to draw on, for one).
func pickerStopped(err error) {
	if err != nil && !errors.Is(err, tea.ErrInterrupted) && !errors.Is(err, huh.ErrUserAborted) {
		fail(err.Error())
	}
	fmt.Fprintln(os.Stderr, grayStyle.Render("Operation cancelled."))
	os.Exit(1)
}

// switchTo prints the path of the worktree matching name as a branch, a
// directory under wt.basedir, or a path. Without a match it creates the
// worktree (and the branch if needed) under wt.basedir, like git-wt.
func switchTo(dir, branch, startPoint string, orphan bool) {
	_, base := repoBase()
	wts := liveWorktrees()
	// -b names a branch only: a directory or path called like it is not a match.
	i := slices.IndexFunc(wts, func(wt worktree) bool { return wt.branch == branch })
	wt, ok := worktree{}, i >= 0
	if ok {
		wt = wts[i]
	} else {
		wt, ok = findWorktree(wts, base, dir)
	}
	if ok {
		if startPoint != "" || orphan {
			fail(fmt.Sprintf("worktree %s (%s) already exists (a start point or --orphan only applies to a new branch)", tilde(wt.path), label(wt)))
		}
		fmt.Println(wt.path)
		return
	}
	path := filepath.Join(base, dir)
	if err := checkNotNested(wts, path, base); err != nil {
		fail(err.Error())
	}
	createWorktree(branch, path, base, startPoint, orphan)
}

// qualifyStartPoint keeps a start point that resolves to a commit, and turns a
// branch that exists only on a remote into "<remote>/<name>", which git would
// otherwise reject or (older versions) check out under its own name, dropping
// -b (as in git-wt). Unresolvable names pass through for git to report.
func qualifyStartPoint(sp string) (string, error) {
	if _, err := git("rev-parse", "--verify", "--quiet", sp+"^{commit}"); err == nil {
		return sp, nil
	}
	out, _ := git("for-each-ref", "--format=%(refname:short)", "refs/remotes/*/"+sp)
	remotes := strings.Fields(out)
	switch len(remotes) {
	case 0:
		return sp, nil
	case 1:
		return remotes[0], nil
	}
	if def, _ := git("config", "--get", "checkout.defaultRemote"); slices.Contains(remotes, def+"/"+sp) {
		return def + "/" + sp, nil
	}
	return "", fmt.Errorf("start point %q matches several remotes (%s): qualify it (e.g. %s) or set checkout.defaultRemote", sp, strings.Join(remotes, ", "), remotes[0])
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
	base = expandBaseDir(baseCfg, mainRoot, home)
	// git records worktree paths with symlinks resolved; compare against the same.
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	return mainRoot, base
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
func deleteTargets(targets []string, force bool) (cd string, err error) {
	wts := mustWorktrees() // including prunable ones, so a vanished worktree can be cleaned up
	mainRoot, base := repoBase()
	current := currentRoot()
	protected := protectedBranches(mainRoot)
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
		if ok && wt.branch != name { // matched as a directory or path: a branch of that name wins
			if _, err := git("-C", mainRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+name); err == nil {
				ok = false
			}
		}
		resolved[i] = target{name, wt, ok}
	}

	wd, _ := os.Getwd()
	// On every return, including errors after earlier targets were removed.
	defer func() { cd = leaveIfGone(cd, wd, mainRoot) }()
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
				return cdMain, fmt.Errorf("cannot delete protected branch %q (default or main HEAD)", name)
			}
			if err := gitLoud("-C", mainRoot, "branch", branchFlag, name); err != nil {
				return cdMain, err
			}
			continue
		}

		isCurrent := samePath(wt.path, current) // before removal, while the path still resolves
		if err := gitLoud(append(removeArgs, wt.path)...); err != nil {
			return cdMain, err
		}
		if isCurrent {
			cdMain = mainRoot
		}
		removeEmptyParents(filepath.Dir(wt.path), base) // e.g. "feat/" after deleting feat/x
		_, branchErr := git("-C", mainRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+wt.branch)
		switch {
		case wt.branch == "" || branchErr != nil:
			note("Deleted worktree %s", tilde(wt.path))
		case slices.Contains(protected, wt.branch):
			note("Deleted worktree %q (branch is protected, not deleted)", wt.branch)
		case gitLoud("-C", mainRoot, "branch", branchFlag, wt.branch) != nil: // git says why it kept the branch
			note("Deleted worktree %s", tilde(wt.path))
		default:
			note("Deleted worktree %q", wt.branch)
		}
	}
	return cdMain, nil
}

// leaveIfGone returns cd, or mainRoot when the shell's directory wd was removed
// along the way (e.g. an emptied parent like .wt/feat), so it doesn't stay in a
// deleted directory.
func leaveIfGone(cd, wd, mainRoot string) string {
	if _, err := os.Stat(wd); cd == "" && wd != "" && err != nil { // Stat: a symlinked cwd dangles
		return mainRoot
	}
	return cd
}

// selectPR lists the open PRs and opens the selected one in a worktree.
func selectPR() {
	var prs []pullRequest
	var prErr, def string
	var defErr error
	load := func() {
		if prs, prErr = openPRs(); prErr == "" && slices.ContainsFunc(prs, func(pr pullRequest) bool { return pr.IsCrossRepository }) {
			def, defErr = repoDefaultBranch()
		}
	}
	spin("Loading pull requests...", load)
	if prErr != "" {
		fail(prErr)
	}
	if defErr != nil {
		exitWithStatus(defErr) // gh already said why
	}
	if len(prs) == 0 {
		fail("no open pull requests")
	}

	wts := liveWorktrees()
	current := currentRoot()
	var here worktree
	if j := slices.IndexFunc(wts, func(wt worktree) bool { return samePath(wt.path, current) }); j >= 0 {
		here = wts[j]
	}
	description, options := prOptions(renderPRTable(prs, wts, here, def, pickerWidth()))
	if len(options) == 0 {
		fail("no other open pull requests")
	}
	var selected int
	err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[int]().
			Title("Select a pull request:").
			Description(description).
			Options(options...).
			Value(&selected),
	)).WithOutput(os.Stderr).Run()
	if err != nil {
		pickerStopped(err)
	}
	checkoutPR(strconv.Itoa(prs[selected].Number), prs[selected], def) // def is loaded when a PR needs it
}

// renderPRTable lays out PRs with the worktree's CHANGES when one exists, so
// it's clear whether selecting moves to it or creates it. When no PR is on the
// current worktree here's branch, it gets a line of its own after the PRs'.
// current marks the lines of here's branch.
func renderPRTable(prs []pullRequest, wts []worktree, here worktree, def string, maxWidth int) (header string, lines []string, current []bool) {
	const colTitle, colBranch = 2, 3
	cells := make([][]string, len(prs))
	current = make([]bool, len(prs))
	for i, pr := range prs {
		changes := ""
		name := prBranch(pr, def)
		current[i] = name == here.branch
		if j := slices.IndexFunc(wts, func(wt worktree) bool { return wt.branch == name }); j >= 0 {
			s := worktreeStatus(wts[j].path)
			n, known := prBehind(wts[j].path, pr.HeadRefOid)
			s.behind, s.remoteNew = max(s.behind, n), !known
			changes = statusCell(s)
		}
		number := greenStyle.Render(fmt.Sprintf("#%d", pr.Number))
		if pr.IsDraft {
			number = yellowStyle.Render(fmt.Sprintf("#%d draft", pr.Number))
		}
		cells[i] = []string{
			changes,
			number,
			pr.Title,
			ansi.Truncate(cyanStyle.Render(iconBranch+" "+pr.HeadRefName), 28, "…"),
			grayStyle.Render(pr.Author.Login),
			grayStyle.Render(humanize.Time(pr.UpdatedAt)),
		}
	}
	if here.path != "" && !slices.Contains(current, true) {
		cells = append(cells, []string{statusCell(worktreeStatus(here.path)), "", "", ansi.Truncate(branchCell(here), 28, "…"), "", ""})
		current = append(current, true)
	}
	header, lines = layoutTable([]string{"WORKTREE", "PR", "TITLE", "BRANCH", "AUTHOR", "UPDATED"}, cells, maxWidth, []shrinkStep{
		{colTitle, 30}, {colBranch, 20}, {colTitle, 10}, {colBranch, 10},
	}, nil)
	return header, lines, current
}

// prOptions is the select's description and options: as in the worktree picker,
// the current lines go under the header, where they can't be selected. An
// option's value is its line's index, which is its PR's.
func prOptions(header string, lines []string, current []bool) (string, []huh.Option[int]) {
	description := header
	var options []huh.Option[int]
	for i, line := range lines {
		if current[i] {
			description += "\n" + currentLine(line)
			continue
		}
		options = append(options, huh.NewOption(line, i))
	}
	return description, options
}

// currentLine is a picker's table line for the current worktree.
func currentLine(line string) string {
	return grayStyle.Render(iconCurrent + " " + ansi.Strip(line))
}

// openPRs returns the repository's open PRs, including ones from forks.
func openPRs() ([]pullRequest, string) {
	return ghPRs("--limit", "100", "--json", "number,title,headRefName,headRefOid,headRepositoryOwner,isDraft,isCrossRepository,author,updatedAt")
}

// ghPRs runs gh pr list with args. The error is what gh said, or how it failed
// when it said nothing, so a failure never looks like an empty list.
func ghPRs(args ...string) ([]pullRequest, string) {
	cmd := exec.Command("gh", append([]string{"pr", "list"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, msg
		}
		return nil, "gh pr list: " + err.Error()
	}
	var prs []pullRequest
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, err.Error()
	}
	return prs, ""
}

// openPR opens the PR given as a number, URL or branch (anything gh pr
// checkout takes) in a worktree under wt.basedir. gh pr checkout --worktree
// fetches it, including from forks, and creates the worktree or brings an
// existing one up to date; gh-wt only picks the path and, for a new worktree,
// copies files and runs wt.hook.
func openPR(arg string) {
	arg = strings.TrimPrefix(arg, "#")
	cmd := exec.Command("gh", "pr", "view", arg, "--json", "number,headRefName,headRepositoryOwner,isCrossRepository")
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		exitWithStatus(err)
	}
	var pr pullRequest
	if err := json.Unmarshal(out, &pr); err != nil {
		fail(err.Error())
	}
	def := ""
	if pr.IsCrossRepository {
		if def, err = repoDefaultBranch(); err != nil {
			exitWithStatus(err)
		}
	}
	checkoutPR(arg, pr, def)
}

// checkoutPR opens pr, which gh pr checkout knows as arg, in a worktree. def
// is the repository's default branch, needed for a PR from a fork.
func checkoutPR(arg string, pr pullRequest, def string) {
	name := prBranch(pr, def)
	_, base := repoBase()
	path, reuse := filepath.Join(base, name), false
	wts := liveWorktrees()
	if wt, ok := findWorktree(wts, base, name); ok {
		if wt.branch != name { // gh would switch that worktree to the PR's branch
			fail(fmt.Sprintf("worktree %s (%s) already exists", tilde(wt.path), label(wt)))
		}
		path, reuse = wt.path, true
	} else if err := checkNotNested(wts, path, base); err != nil {
		fail(err.Error())
	}
	src := currentRoot()
	args := []string{"pr", "checkout", arg, "--branch", name}
	if !reuse || !samePath(path, src) { // gh checks out the current worktree only without --worktree
		args = append(args, "--worktree", path)
	}
	cmd := exec.Command("gh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		exitWithStatus(err)
	}
	if reuse {
		fmt.Println(path)
		return
	}
	setUpWorktree(src, path, base)
}

// prBranch is the local branch gh pr checkout uses for the PR: its head branch,
// or <owner>/<branch> for a fork's branch named like the repository's default
// branch def (as gh gets it from GitHub), so it doesn't collide with ours.
func prBranch(pr pullRequest, def string) string {
	if pr.IsCrossRepository && pr.HeadRefName == def {
		return pr.HeadRepositoryOwner.Login + "/" + pr.HeadRefName
	}
	return pr.HeadRefName
}

// repoDefaultBranch is the repository's default branch on GitHub.
func repoDefaultBranch() (string, error) {
	// gh's stderr is kept in the error, so it's shown after the picker's spinner.
	out, err := exec.Command("gh", "repo", "view", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name").Output()
	return strings.TrimSpace(string(out)), err
}

// renameWorktree moves a linked worktree to <base>/<newName> and renames its
// branch to newName, like git-wt's -m (safe) and -M (overwrite an existing
// branch, move a locked worktree). old is empty for the current worktree.
// It returns the new path when the current worktree was renamed.
func renameWorktree(old []string, newName string, force bool) (string, error) {
	mainRoot, base := repoBase()
	current := currentRoot()
	wts := liveWorktrees()
	var src worktree
	var ok bool
	if len(old) == 0 {
		src, ok = findWorktree(wts, base, current)
	} else {
		src, ok = findWorktree(wts, base, old[0])
	}
	switch {
	case !ok && len(old) == 0:
		return "", errors.New("not in a worktree; name the worktree to rename")
	case !ok:
		return "", fmt.Errorf("no worktree found for %q", old[0])
	case newName == "":
		return "", errors.New("empty new name")
	case src.branch == "":
		return "", fmt.Errorf("cannot rename %s: it has no branch (detached HEAD)", tilde(src.path))
	case samePath(src.path, mainRoot):
		return "", errors.New("cannot rename the main working tree")
	case slices.Contains(protectedBranches(mainRoot), src.branch):
		return "", fmt.Errorf("cannot rename protected branch %q (default or main HEAD)", src.branch)
	}

	newPath := filepath.Join(base, newName)
	movesDir := !samePath(src.path, newPath)
	renamesBranch := src.branch != newName
	if !movesDir && !renamesBranch {
		return "", fmt.Errorf("worktree %q is already named %q", src.branch, newName)
	}
	// On a case-insensitive filesystem a name that differs only in case is the
	// same directory and, to git, the same branch; git refuses both renames.
	caseOnlyDir := movesDir && sameFile(src.path, newPath)
	caseOnlyBranch := renamesBranch && strings.EqualFold(src.branch, newName) && sameRef(mainRoot, src.branch, newName)
	if movesDir && !caseOnlyDir {
		if _, err := os.Lstat(newPath); err == nil {
			return "", fmt.Errorf("%s already exists", tilde(newPath))
		}
		if err := checkNotNested(wts, newPath, base); err != nil { // or into itself
			return "", err
		}
	}

	// Rename the branch first: git checks every ref conflict (existing,
	// ancestor or checked-out branches) before anything on disk changes.
	isCurrent := samePath(src.path, current)
	wd, _ := os.Getwd()
	overwritten, _ := git("show-ref", "--verify", "--hash", "refs/heads/"+newName) // restored if the move fails
	if caseOnlyBranch {
		overwritten = "" // that is the branch itself
	}
	if renamesBranch {
		flag := "-m"
		if force || caseOnlyBranch { // git takes a change of case only with -M
			flag = "-M"
		}
		if err := gitLoud("-C", mainRoot, "branch", flag, "--", src.branch, newName); err != nil {
			return "", err
		}
	}
	if movesDir {
		args := []string{"-C", mainRoot, "worktree", "move"}
		if force {
			args = append(args, "--force", "--force") // twice to move a locked worktree
		}
		// Remember the topmost parent MkdirAll creates, so a failure removes only that.
		created := ""
		for dir := filepath.Dir(newPath); !exists(dir); dir = filepath.Dir(dir) {
			created = dir
		}
		err := os.MkdirAll(filepath.Dir(newPath), 0o755)
		switch {
		case err != nil:
		case caseOnlyDir: // git can't move a directory onto itself: go through another name
			tmp := newPath + ".gh-wt-rename"
			if err = gitLoud(append(args, src.path, tmp)...); err == nil {
				if err = gitLoud(append(args, tmp, newPath)...); err != nil {
					_ = gitLoud(append(args, tmp, src.path)...)
				}
			}
		default:
			err = gitLoud(append(args, src.path, newPath)...)
		}
		if err != nil {
			if created != "" {
				removeEmptyParents(filepath.Dir(newPath), filepath.Dir(created))
			}
			if !renamesBranch {
				return "", err // git (or MkdirAll) says why
			}
			return "", undoBranchRename(mainRoot, src.branch, newName, overwritten)
		}
		removeEmptyParents(filepath.Dir(src.path), base) // e.g. "feat/" after renaming feat/x
	}
	switch {
	case !renamesBranch:
		note("Moved worktree %q to %s", src.branch, tilde(newPath))
	case !movesDir:
		note("Renamed branch %q to %q (worktree directory unchanged)", src.branch, newName)
	default:
		note("Renamed worktree %q to %q (%s)", src.branch, newName, tilde(newPath))
	}
	if isCurrent {
		return newPath, nil
	}
	return leaveIfGone("", wd, mainRoot), nil
}

// sameFile reports whether two paths that exist are one file, as two spellings
// of a name are on a case-insensitive filesystem.
func sameFile(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// sameRef reports whether the branch other is only another spelling of branch:
// it resolves (the filesystem ignores case) but isn't among the branches.
func sameRef(mainRoot, branch, other string) bool {
	if _, err := git("-C", mainRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+other); err != nil {
		return false
	}
	listed, _ := git("-C", mainRoot, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	return !slices.Contains(strings.Split(listed, "\n"), other)
}

// undoBranchRename restores the branch name after the worktree failed to move
// and builds the error describing the state left behind.
// overwritten is the commit newName pointed to before -M replaced it, if any.
func undoBranchRename(mainRoot, oldName, newName, overwritten string) error {
	if _, err := git("-C", mainRoot, "branch", "-m", "--", newName, oldName); err != nil {
		if overwritten != "" {
			return fmt.Errorf("failed to move worktree; the branch is now %q and could not be renamed back to %q (the overwritten %q was at %s)", newName, oldName, newName, overwritten)
		}
		return fmt.Errorf("failed to move worktree; the branch is now %q and could not be renamed back to %q", newName, oldName)
	}
	if overwritten != "" {
		if _, err := git("-C", mainRoot, "branch", "--", newName, overwritten); err != nil {
			return fmt.Errorf("failed to move worktree; renamed the branch back to %q, but could not restore %q at %s", oldName, newName, overwritten)
		}
	}
	return fmt.Errorf("failed to move worktree; renamed the branch back to %q", oldName)
}

// checkNotNested refuses a path inside one of the worktrees under base:
// deleting the outer worktree later would delete the inner one's files too.
func checkNotNested(wts []worktree, path, base string) error {
	for dir := filepath.Dir(path); within(dir, base) && !samePath(dir, base); dir = filepath.Dir(dir) {
		if slices.ContainsFunc(wts, func(wt worktree) bool { return samePath(wt.path, dir) }) {
			return fmt.Errorf("%s would be inside the worktree %s", tilde(path), tilde(dir))
		}
	}
	return nil
}

// removeEmptyParents removes dir and its parents while they are empty
// directories strictly inside stop.
func removeEmptyParents(dir, stop string) {
	for ; within(dir, stop) && !samePath(dir, stop); dir = filepath.Dir(dir) {
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() || os.Remove(dir) != nil {
			return
		}
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// protectedBranches are never deleted or renamed: the default branch and the
// branch the main repository's HEAD points to (a bare repository's HEAD has
// no worktree to keep git from deleting it).
func protectedBranches(mainRoot string) []string {
	protected := []string{defaultBranch()}
	if head, err := git("-C", mainRoot, "symbolic-ref", "--short", "HEAD"); err == nil {
		protected = append(protected, head)
	}
	return protected
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
func createWorktree(name, path, base, startPoint string, orphan bool) {
	if err := checkDestination(path); err != nil {
		fail(err.Error())
	}
	src := currentRoot()
	args := []string{"worktree", "add", "-b", name, path}
	switch {
	case orphan:
		args = []string{"worktree", "add", "--orphan", "-b", name, path}
	case startPoint != "":
		sp, err := qualifyStartPoint(startPoint)
		if err != nil {
			fail(err.Error())
		}
		if sp != startPoint {
			note("Using %s as the start point", sp)
		}
		args = append(args, sp)
	case branchExists(name):
		args = []string{"worktree", "add", path, name} // git sets up tracking for a remote-only branch
	}
	if err := gitLoud(args...); err != nil {
		exitWithStatus(err)
	}
	if orphan { // an orphan worktree starts empty, as in git-wt
		src = ""
	}
	setUpWorktree(src, path, base)
}

// setUpWorktree copies files from the src worktree (if any) into the new
// worktree at path, runs wt.hook there and prints the path.
func setUpWorktree(src, path, base string) {
	if src != "" {
		copyFiles(src, path, base)
	}

	// -z: a hook may span lines.
	hooks, _ := exec.Command("git", "config", "-z", "--get-all", "wt.hook").Output()
	for _, hook := range strings.FieldsFunc(string(hooks), func(r rune) bool { return r == 0 }) {
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

func label(wt worktree) string {
	if wt.branch == "" {
		return "detached"
	}
	return "branch " + wt.branch
}

// checkDestination refuses a path git worktree add would reject only after
// creating the branch: anything there except an empty directory.
func checkDestination(path string) error {
	if !exists(path) {
		return nil
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) > 0 {
		return fmt.Errorf("%s already exists", tilde(path))
	}
	return nil
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
		// -z: without it git quotes names with non-ASCII or special characters.
		out, err := exec.Command("git", append([]string{"-C", src, "ls-files", "-z"}, l.args...)...).Output()
		if err == nil {
			entries = append(entries, strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 })...)
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
		if !exists(from) { // ls-files --modified lists deleted files too
			continue
		}
		if (skipBase && within(from, base)) || samePath(from, base) {
			continue
		}
		if strings.HasSuffix(e, "/") {
			copiedDir = e
		}
		if err := copyEntry(from, to, base, cpFlags); err != nil {
			fmt.Fprintln(os.Stderr, grayStyle.Render(fmt.Sprintf("warning: failed to copy %s: %v", e, err)))
		}
	}
}

// copyEntry copies the file or directory from to to. A directory that holds
// the worktree base (wt.basedir two or more levels deep) is copied entry by
// entry around it, or every sibling worktree would be copied in.
func copyEntry(from, to, base, cpFlags string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if info.IsDir() && within(base, from) {
		if samePath(from, base) {
			return nil
		}
		children, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		var errs []error
		for _, c := range children {
			errs = append(errs, copyEntry(filepath.Join(from, c.Name()), filepath.Join(to, c.Name()), base, cpFlags))
		}
		return errors.Join(errs...)
	}
	if info.IsDir() {
		from += "/." // copy the contents, so an existing directory is merged into
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return exec.Command("cp", cpFlags, from, to).Run()
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func listWorktrees() {
	wts := liveWorktrees()
	t := newLiveTable(0, lookupPRs(wts))
	width, height, err := term.GetSize(os.Stdout.Fd())
	if err == nil {
		t.width = width
	}
	// The table is drawn first and redrawn when the PRs arrive, which needs all
	// of it on screen and the terminal to ourselves; a taller one, a pipe or a
	// background job waits for the PRs as before.
	live := err == nil && len(wts)+2 <= height && foreground()
	spin("Loading worktrees...", func() {
		t.rows = localRows(wts, currentRoot())
		if live {
			t.prWidth = prColumnWidth()
		} else { // printed once with the PRs in: no room to keep
			t.update(<-t.prs)
		}
	})
	if !live {
		fmt.Print(t.listText())
		t.reportErr()
		return
	}
	m := &listModel{t}
	restore := noEcho()
	_, err = tea.NewProgram(m, tea.WithOutput(os.Stdout), tea.WithInput(nil)).Run()
	restore()
	if err != nil {
		if errors.Is(err, tea.ErrInterrupted) {
			os.Exit(130)
		}
		fail(err.Error())
	}
	// Printed after the live region is cleared, so it wraps like any output
	// instead of being cut at the terminal width.
	fmt.Print(m.listText())
	m.reportErr()
}

// noEcho stops the terminal from echoing keys typed while the table is drawn
// in place, which would push it down; the keys still reach the shell
// afterwards. It returns the function that restores the terminal.
func noEcho() func() {
	stty := func(args ...string) (string, error) {
		cmd := exec.Command("stty", args...)
		cmd.Stdin = os.Stdin
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	saved, err := stty("-g")
	if err != nil {
		return func() {}
	}
	if _, err := stty("-echo"); err != nil {
		return func() {}
	}
	return func() {
		// After Ctrl+Z and bg the shell has put its own settings back, and a
		// restore from the background would be stopped (SIGTTOU).
		if foreground() {
			_, _ = stty(saved)
		}
	}
}

// spin runs fn behind a spinner on stderr when that's a terminal. The spinner
// returns on Ctrl+C while fn still runs, so what fn loads can't be used then.
func spin(title string, fn func()) {
	if !isatty.IsTerminal(os.Stderr.Fd()) {
		fn()
		return
	}
	err := spinner.New().Title(title).Output(os.Stderr).Action(fn).Run()
	if errors.Is(err, tea.ErrInterrupted) {
		os.Exit(130)
	}
	if err != nil {
		fail(err.Error())
	}
}

// localRows reads each worktree's status and last commit, which takes no network.
func localRows(wts []worktree, current string) []row {
	rows := make([]row, len(wts))
	for i, wt := range wts {
		r := row{wt: wt, current: samePath(wt.path, current)}
		r.status = worktreeStatus(wt.path)
		if out, err := git("-C", wt.path, "log", "-1", "--format=%cr\x1f%s"); err == nil {
			r.when, r.subject, _ = strings.Cut(out, "\x1f")
		}
		rows[i] = r
	}
	return rows
}

// worktreeStatus reads the worktree's git status, unknown when git fails.
func worktreeStatus(path string) status {
	out, err := git("--no-optional-locks", "-C", path, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return status{unknown: true}
	}
	return parseStatus(out)
}

// prRef is a PR number in a subject, as in "(#123)" or "Merge pull request #123".
var prRef = regexp.MustCompile(`#(\d+)`)

// prColumnWidth is how wide the PR column will get, guessed from the largest
// PR number in the default branch's recent subjects, so the table keeps that
// room while the PRs load. It is 0 when the history names no PR.
func prColumnWidth() int {
	out, _ := git("log", "-n", "50", "--format=%s", defaultBranch())
	n := 0
	for _, m := range prRef.FindAllStringSubmatch(out, -1) {
		v, _ := strconv.Atoi(m[1])
		n = max(n, v)
	}
	if n == 0 {
		return 0
	}
	return ansi.StringWidth(prCell(&pullRequest{Number: n, State: "MERGED"}))
}

// prLookup is each worktree's PR, in the order of the worktrees, or gh's error.
type prLookup struct {
	prs []prRow
	err string
}

type prRow struct {
	pr     *pullRequest
	behind int  // commits of an open PR's head missing from the worktree
	known  bool // behind could be counted: the head commit is fetched
}

// lookupPRs finds each worktree's PR in the background. The channel is
// buffered, so the lookup can finish after nobody waits for it any more.
func lookupPRs(wts []worktree) <-chan prLookup {
	ch := make(chan prLookup, 1)
	go func() {
		prs, err := fetchPRs()
		l := prLookup{prs: make([]prRow, len(wts)), err: err}
		for i, wt := range wts {
			if pr, ok := prs[wt.branch]; ok && wt.branch != "" {
				l.prs[i].pr = &pr
				if pr.State == "OPEN" {
					l.prs[i].behind, l.prs[i].known = prBehind(wt.path, pr.HeadRefOid)
				}
			}
		}
		ch <- l
	}()
	return ch
}

// liveTable is the worktree table while the PRs load: the PR column shows a
// spinner until they arrive, then the PRs and the commits pushed to them.
type liveTable struct {
	rows    []row
	width   int
	prWidth int // room kept for the PR column, from prColumnWidth
	prs     <-chan prLookup
	spin    bspinner.Model
	loaded  bool
	err     string
}

func newLiveTable(width int, prs <-chan prLookup) liveTable {
	return liveTable{width: width, prs: prs, spin: bspinner.New(bspinner.WithSpinner(bspinner.MiniDot), bspinner.WithStyle(grayStyle))}
}

func (t *liveTable) init() tea.Cmd {
	prs := t.prs
	return tea.Batch(t.spin.Tick, func() tea.Msg { return <-prs })
}

// update takes the PRs or a spinner tick.
func (t *liveTable) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case prLookup:
		for i, p := range msg.prs {
			if p.pr == nil {
				continue
			}
			t.rows[i].pr = p.pr
			if p.pr.State == "OPEN" {
				// As in renderPRTable: the PR head may be fresher than the upstream.
				t.rows[i].status.behind = max(t.rows[i].status.behind, p.behind)
				t.rows[i].status.remoteNew = !p.known
			}
		}
		t.loaded, t.err = true, msg.err
	case bspinner.TickMsg:
		if !t.loaded {
			var cmd tea.Cmd
			t.spin, cmd = t.spin.Update(msg)
			return cmd
		}
	}
	return nil
}

func (t *liveTable) render() (string, []string) {
	frame := ""
	if !t.loaded {
		frame = t.spin.View()
	}
	return renderTable(t.rows, t.width, frame, t.prWidth)
}

// listText is the table of gh wt -l, marking the current worktree.
func (t *liveTable) listText() string {
	header, lines := t.render()
	var b strings.Builder
	b.WriteString(header + "\n")
	for i, line := range lines {
		mark := " "
		if t.rows[i].current {
			mark = greenStyle.Render(iconCurrent)
		}
		b.WriteString(mark + " " + line + "\n")
	}
	return b.String()
}

func (t *liveTable) reportErr() {
	if t.err != "" {
		fmt.Fprintln(os.Stderr, grayStyle.Render("PR state unavailable: "+t.err))
	}
}

// listModel draws gh wt -l and quits once the PRs are in.
type listModel struct{ liveTable }

func (m *listModel) Init() tea.Cmd { return m.init() }

func (m *listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	if m.loaded {
		return m, tea.Quit
	}
	return m, cmd
}

// View is empty once the PRs are in, which clears the live region for the
// final print. It ends with a newline, as bubbletea erases the last line when
// it stops.
func (m *listModel) View() string {
	if m.loaded {
		return ""
	}
	return m.listText()
}

// picker runs the worktree select while the PRs load.
type picker struct {
	liveTable
	form    *huh.Form
	sel     *huh.Select[string]
	options []huh.Option[string] // the select's own slice: a new Key shows on its next update
	held    bool                 // a refresh was skipped while a filter was in use
	filled  bool                 // the PRs are in the select
	size    tea.WindowSizeMsg
}

type refreshMsg struct{}

func newPicker(rows []row, width, prWidth int, prs <-chan prLookup, selected *string) *picker {
	p := &picker{liveTable: newLiveTable(width, prs)}
	p.rows, p.prWidth = rows, prWidth
	for _, r := range rows {
		if !r.current {
			p.options = append(p.options, huh.NewOption("", r.wt.path))
		}
	}
	p.sel = huh.NewSelect[string]().Title("Select a worktree:").Options(p.options...).Value(selected)
	p.refresh()
	p.form = huh.NewForm(huh.NewGroup(p.sel))
	p.form.SubmitCmd, p.form.CancelCmd = tea.Quit, tea.Interrupt
	return p
}

func (p *picker) Init() tea.Cmd { return tea.Batch(p.form.Init(), p.init()) }

func (p *picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		p.size = size
		p.width = size.Width - pickerMargin // lay the table out again for the new width
	}
	cmd := p.update(msg)
	// A filter shows copies of the options, which keep the old lines while the
	// header would change, so the table waits until the filter is cleared.
	p.held = filtering(p.sel)
	if !p.held {
		p.refresh()
	}
	form, formCmd := p.form.Update(msg)
	p.form = form.(*huh.Form)
	if p.held && !filtering(p.sel) { // the filter was just cleared
		formCmd = tea.Batch(formCmd, func() tea.Msg { return refreshMsg{} })
	}
	if !p.held && p.loaded && !p.filled && p.size.Width > 0 { // no size yet: huh measures when it comes
		// The PRs widen the lines, which may now wrap, and huh measures its
		// height only on a resize: give it the size again.
		p.filled = true
		form, sizeCmd := p.form.Update(p.size)
		p.form = form.(*huh.Form)
		formCmd = tea.Batch(formCmd, sizeCmd)
	}
	return p, tea.Batch(cmd, formCmd)
}

func (p *picker) View() string { return p.form.View() }

// refresh writes the table into the select: the other worktrees as options and,
// as huh has no disabled options, the current one into the description under
// the header, where it shows but can't be selected.
func (p *picker) refresh() {
	header, lines := p.render()
	description := header
	i := 0
	for j, r := range p.rows {
		if r.current {
			description += "\n" + currentLine(lines[j])
			continue
		}
		p.options[i].Key = lines[j]
		i++
	}
	p.sel.Description(description)
}

// filtering reports whether a filter is being typed or narrows the options.
func filtering(s *huh.Select[string]) bool {
	if s.GetFiltering() {
		return true
	}
	for _, b := range s.KeyBinds() { // enabled while a typed filter narrows the list
		if b.Help().Desc == "clear filter" {
			return b.Enabled()
		}
	}
	return false
}

// renderTable aligns rows under a header. The header starts with 2 cells of
// indent, matching the caller's mark in the list or huh's cursor in the picker;
// lines are returned without it. With maxWidth > 0 the path, subject and branch
// columns shrink, in that order, so every line fits. A spinner frame stands in
// for the PR of each branch while the PRs load, and the PR column keeps at
// least prWidth cells, so it doesn't widen when they arrive.
func renderTable(rows []row, maxWidth int, loading string, prWidth int) (string, []string) {
	const colBranch, colPR, colSubject, colPath = 0, 2, 3, 5
	titles := []string{"BRANCH", "CHANGES", "PR", "LAST COMMIT", "", "PATH"}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		pr := prCell(r.pr)
		if loading != "" && r.wt.branch != "" {
			pr = loading
		}
		cells[i] = []string{
			ansi.Truncate(branchCell(r.wt), 28, "…"),
			statusCell(r.status),
			pr,
			ansi.Truncate(r.subject, 64, "…"),
			grayStyle.Render(r.when),
			grayStyle.Render(tilde(r.wt.path)),
		}
	}
	return layoutTable(titles, cells, maxWidth, []shrinkStep{
		{colPath, 24}, {colSubject, 30}, {colBranch, 20},
		{colPath, 8}, {colSubject, 10}, {colBranch, 10},
	}, map[int]int{colPR: prWidth})
}

// shrinkStep narrows column col down to min cells while the table is too wide.
type shrinkStep struct{ col, min int }

// layoutTable aligns cells under underlined titles. The header starts with 2
// cells of indent, matching the caller's mark or huh's cursor; lines are
// returned without it. A column is at least as wide as its floor, and with
// maxWidth > 0 the shrink steps apply in order.
func layoutTable(titles []string, cells [][]string, maxWidth int, shrink []shrinkStep, floors map[int]int) (string, []string) {
	widths := make([]int, len(titles))
	for c, t := range titles {
		widths[c] = max(len(t), floors[c])
		for _, cs := range cells {
			widths[c] = max(widths[c], ansi.StringWidth(cs[c]))
		}
	}
	if maxWidth > 0 {
		over := 2 + 2*(len(widths)-1) - maxWidth
		for _, w := range widths {
			over += w
		}
		for _, s := range shrink {
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
	lines := make([]string, len(cells))
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

// prBehind counts the commits of the PR head (on GitHub) missing from the
// worktree's HEAD, so new pushes show without a fetch. known is false when
// the head commit isn't fetched yet, so it can't be counted.
func prBehind(path, headOid string) (n int, known bool) {
	if headOid == "" {
		return 0, true
	}
	// GIT_NO_LAZY_FETCH: in a partial clone, don't fetch the missing commit.
	noFetch := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := noFetch("cat-file", "-e", headOid+"^{commit}"); err != nil {
		return 0, false
	}
	out, err := noFetch("rev-list", "--count", "HEAD.."+headOid)
	if err != nil {
		return 0, true
	}
	n, _ = strconv.Atoi(out)
	return n, true
}

func statusCell(s status) string {
	if s.unknown { // not the clean mark: nothing is known about it
		return grayStyle.Render("unknown")
	}
	var parts []string
	add := func(n int, prefix string, style lipgloss.Style) {
		if n > 0 {
			parts = append(parts, style.Render(fmt.Sprintf("%s%d", prefix, n)))
		}
	}
	add(s.ahead, "↑", cyanStyle)
	if s.remoteNew {
		parts = append(parts, cyanStyle.Render("↓N")) // commits on GitHub, count unknown until fetched
	} else {
		add(s.behind, "↓", cyanStyle)
	}
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
		icon, word, style = iconPRMerged, "merged", purpleStyle
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
	prs, msg := ghPRs("--state", "all", "--limit", "200", "--json", "headRefName,headRefOid,headRepository,isCrossRepository,number,state,isDraft")
	if msg != "" {
		return nil, msg
	}
	return prByBranch(prs, originRepo()), ""
}

// prByBranch picks one PR per head branch from prs, which gh lists newest
// first: the newest, unless an older one is open. PRs whose branch isn't on
// origin are left out.
func prByBranch(prs []pullRequest, origin string) map[string]pullRequest {
	m := make(map[string]pullRequest, len(prs))
	for _, pr := range prs {
		if prev, ok := m[pr.HeadRefName]; inOrigin(pr, origin) && (!ok || (prev.State != "OPEN" && pr.State == "OPEN")) {
			m[pr.HeadRefName] = pr
		}
	}
	return m
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

// originRepo is "owner/name" parsed from origin's URL, or "" if it can't be.
func originRepo() string {
	url, err := git("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	url = strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	parts := strings.FieldsFunc(url, func(r rune) bool { return r == '/' || r == ':' })
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// inOrigin reports whether the PR's branch can be found on origin: a PR from
// the same repository, or from origin itself when origin is a fork. A
// stranger's fork branch that merely shares a local branch's name is not.
func inOrigin(pr pullRequest, origin string) bool {
	return !pr.IsCrossRepository || (origin != "" && strings.EqualFold(pr.HeadRepository.NameWithOwner, origin))
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

// exitWithStatus exits with a failed command's status, showing what it said
// if that was captured rather than printed, or reports any other error.
func exitWithStatus(err error) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Stderr.Write(exitErr.Stderr)
		os.Exit(exitErr.ExitCode())
	}
	fail(err.Error())
}
