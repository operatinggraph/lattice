//go:build ignore

// lint-board.go — board-discipline gate for the backlog lane files.
//
//	go run ./scripts/lint-board.go [files...]
//
// The backlog board is an INDEX, not a journal (see backlog/lattice.md
// "How this board works" + agentic-ops-swimlanes-design.md §5). After a day of
// autonomous fires the lane files re-bloated 22KB→41KB — State cells grew into
// design-summaries, the survey log into a ~70-line fire-journal, Done-log
// entries into paragraphs. This gate fails a board commit that re-bloats, so
// leanness is structural rather than diligence-dependent.
//
// With --strict (or STRICT=1) it exits non-zero on any FAIL; otherwise advisory
// (prints, exits 0). WARN findings (dependency-consistency — fuzzy, free-text)
// never fail the build; they surface drift for a human.
//
// Checks:
//
//	FAIL  row      — a table data row over rowMax chars (State-cell bloat).
//	FAIL  journal  — a cell narrating change ("Fire N SHIPPED", "Was:", an
//	                 "SHA — prose" build-log) — the CLAUDE.md no-changelog rule.
//	FAIL  section  — a Survey-log / PO-notes section over sectionMax lines (the
//	                 rotation memory became a per-fire run-log).
//	FAIL  doneline — a Done-log entry that is not one line (over doneEntryMax).
//	FAIL  filesize — a lane file over fileMaxBytes.
//	FAIL  conflict — a git conflict marker committed into a tracked text file.
//	                 Swept repo-wide, not just the lane files: a marker inside a
//	                 .go file fails the build, but in markdown nothing notices.
//	                 1caaaf8 committed a `<<<<<<< Updated upstream` block into
//	                 backlog/lattice.md — duplicating one row and contradicting
//	                 its own state — and CI passed on it, because every gate that
//	                 reads the board parses rows and no gate read the raw text.
//	FAIL  shipped  — a row still in the open-items table whose State reads as
//	                 shipped (e.g. "✅ shipped `sha`") — "Open items only —
//	                 shipped demand is in the Done log" (verticals.md's own
//	                 header); this happened three separate times (92e8e1f0,
//	                 8f9b0633, c97b784f all landed with the row left dangling
//	                 after the State flip) before this check existed.
//	FAIL  designer — a "needs designer pass" row that does not NAME the absent
//	                 ratified pattern (`no-pattern: <primitive>`). The §2.5
//	                 honest-gate: a precedent in the touched file means the item
//	                 is a steward 📋, not a designer 📐. Default-deny the bare
//	                 label, make the filer declare — the shipped `# read-posture:`
//	                 shape (lint-conventions.go). Backstops the filing-inflation
//	                 the 2026-08-20 backlog audit found (📐 rows 0→12 in 9 days).
//	WARN  dep      — a 🚧/🏗️/📋/📐 row "behind X / blocked-on X" where X reads done.
//	WARN  openrows — a lane has more than openRowWarnMax open (non-Done-log)
//	                 rows. Never fails, even under --strict: closure pressure
//	                 is a signal for the steward to prefer a closure/
//	                 consolidation unit next, not a cap that blocks filing.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	rowMax         = 600    // a table data row (aim ≤300; hard cap here)
	sectionMax     = 22     // Survey-log / PO-notes section line budget
	doneEntryMax   = 250    // a Done-log bullet must be one line
	doneCountMax   = 35     // Done-log entries before the oldest should roll to archive/ (WARN)
	openRowWarnMax = 80     // open (non-Done-log) rows before a closure/consolidation unit is preferred (WARN)
	fileMaxBytes   = 40_000 // a lane file ceiling (clean lattice ≈22KB)
)

var defaultFiles = []string{
	"_bmad-output/planning-artifacts/backlog.md",
	"_bmad-output/planning-artifacts/backlog/lattice.md",
	"_bmad-output/planning-artifacts/backlog/verticals.md",
	"_bmad-output/planning-artifacts/backlog/loupe.md",
}

var (
	journalRe  = regexp.MustCompile(`(?i)(fire\s+\d+\s+shipped|\bwas:|✅\s*fire\s+\d)`)
	shaProseRe = regexp.MustCompile("`[0-9a-f]{7,40}`[^|]{120,}") // a SHA followed by a long prose tail inside a cell
	depRe      = regexp.MustCompile(`(?i)(blocked-on|behind)[: ]+([A-Za-z0-9 ._/-]{3,40})`)
	headingRe  = regexp.MustCompile(`^#{2,3}\s+(.*)`)
	tokenStrip = regexp.MustCompile(`[*` + "`" + `]`)
	// shippedRe matches only when the row's OWN leading state token is ✅ and
	// the cell also reads as shipped/closed — not a ✅-ratified-but-unbuilt
	// design (no "shipped" text) and not an open row (📐/🏗️/📋/🚧) whose prose
	// merely mentions that one increment among several has shipped.
	shippedRe = regexp.MustCompile(`(?i)^✅.*\b(shipped|closed)\b`)
	// designerPassRe matches the design-WANTED filing marker ("needs designer
	// pass") — distinct from "📐 awaiting-Andrew", which is a FINISHED design.
	// noPatternRe is its required honest-gate declaration: the filer must NAME
	// the specific absent ratified pattern, so a row that has a precedent in the
	// touched file cannot hide behind the designer out (§2.5).
	designerPassRe = regexp.MustCompile(`(?i)needs designer pass`)
	noPatternRe    = regexp.MustCompile(`(?i)no-pattern:\s*\S`)
	// conflictRe matches the three marker lines `git merge` / `git stash pop`
	// leave behind. The `=======` arm is anchored to a whole line of exactly
	// seven, which no setext underline in this tree uses (every heading is ATX).
	conflictRe = regexp.MustCompile(`^(?:<{7}|>{7})[ \t]|^={7}$`)
)

type finding struct {
	file string
	line int
	kind string // row | journal | section | doneline | filesize | conflict | dep | openrows
	warn bool
	msg  string
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	files := os.Args[1:]
	files = filterFlags(files, &strict)
	if len(files) == 0 {
		files = defaultFiles
	}

	var all []finding
	doneItems := map[string]bool{} // lowercased fragments that appear shipped
	rowStates := []rowRef{}        // every row's item + state, for the dep check

	for _, f := range files {
		fs, items, rows := checkFile(f)
		all = append(all, fs...)
		for k := range items {
			doneItems[k] = true
		}
		rowStates = append(rowStates, rows...)
	}
	all = append(all, depCheck(rowStates, doneItems)...)
	all = append(all, conflictSweep()...)

	fails, warns := 0, 0
	for _, x := range all {
		tag := "FAIL"
		if x.warn {
			tag = "WARN"
			warns++
		} else {
			fails++
		}
		fmt.Printf("%s  %-9s %s:%d  %s\n", tag, x.kind, x.file, x.line, x.msg)
	}
	if len(all) == 0 {
		fmt.Println("lint-board: clean — the board is an index, not a journal.")
	} else {
		fmt.Printf("lint-board: %d FAIL, %d WARN\n", fails, warns)
	}
	if strict && fails > 0 {
		os.Exit(1)
	}
}

type rowRef struct {
	file  string
	line  int
	item  string
	state string
}

// checkFile runs the per-file checks and returns (findings, shipped-item-fragments, rows).
func checkFile(path string) ([]finding, map[string]bool, []rowRef) {
	data, err := os.ReadFile(path)
	if err != nil {
		return []finding{{file: path, line: 0, kind: "filesize", msg: "cannot read: " + err.Error()}}, nil, nil
	}
	var out []finding
	if len(data) > fileMaxBytes {
		out = append(out, finding{path, 0, "filesize", false,
			fmt.Sprintf("%d bytes > %d ceiling — the board is bloating; compact rows / roll the Done log to archive/", len(data), fileMaxBytes)})
	}

	doneItems := map[string]bool{}
	var rows []rowRef

	curSection := ""   // current ## / ### heading text
	secStart := 0      // line where the current capped section began
	secCount := 0      // lines in the current capped section
	secCapped := false // is the current section a Survey-log / PO-notes section?
	inDone := false
	doneCount := 0
	openRowCount := 0

	closeSection := func(atLine int) {
		if secCapped && secCount > sectionMax {
			out = append(out, finding{path, secStart, "section", false,
				fmt.Sprintf("'%s' section is %d lines > %d — rotation memory only (one dated line per run); a fire-journal belongs in the commit message", curSection, secCount, sectionMax)})
		}
	}

	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()

		if m := headingRe.FindStringSubmatch(line); m != nil {
			closeSection(n)
			curSection = strings.TrimSpace(m[1])
			lc := strings.ToLower(curSection)
			secCapped = strings.Contains(lc, "survey log") || strings.Contains(lc, "po notes")
			secStart, secCount = n, 0
			inDone = strings.Contains(lc, "done log")
			continue
		}
		if secCapped {
			secCount++
		}

		// Done-log entries must be one line.
		if inDone && strings.HasPrefix(strings.TrimSpace(line), "- ") {
			doneCount++
			if len(line) > doneEntryMax {
				out = append(out, finding{path, n, "doneline", false,
					fmt.Sprintf("Done-log entry %d chars > %d — one line only (`date · SHA · [tag] title`); narrative → commit", len(line), doneEntryMax)})
			}
		}

		// Table data rows.
		if strings.HasPrefix(line, "|") && !isTableMeta(line) {
			if len(line) > rowMax {
				out = append(out, finding{path, n, "row", false,
					fmt.Sprintf("row %d chars > %d — State = token + design-doc/commit link + (if 🏗️) one ≤10-word next; detail → the design doc", len(line), rowMax)})
			}
			if journalRe.MatchString(line) || shaProseRe.MatchString(line) {
				out = append(out, finding{path, n, "journal", false,
					"cell narrates build history (Fire-N-SHIPPED / Was: / SHA+prose) — the no-changelog rule applies to the board; move it to the commit + design doc"})
			}
			if item, state := splitRow(line); item != "" {
				rows = append(rows, rowRef{path, n, item, state})
				if !inDone {
					openRowCount++
					if shippedRe.MatchString(state) {
						out = append(out, finding{path, n, "shipped", false,
							fmt.Sprintf("row state %q reads as shipped but the row is still in the open-items table — delete the row (its Done-log line already carries the record)", state)})
					}
					if designerPassRe.MatchString(state) && !noPatternRe.MatchString(state) {
						out = append(out, finding{path, n, "designer", false,
							"'needs designer pass' row does not name the absent ratified pattern — add `no-pattern: <named primitive>`; a precedent in the touched file means it is a steward 📋, not a designer 📐 (§2.5 honest-gate)"})
					}
				}
			}
		}

		// Track shipped items (Done-log titles + ✅-state rows) for the dep check.
		if inDone && strings.Contains(line, "·") {
			doneItems[itemKey(line)] = true
		}
	}
	closeSection(n)
	if doneCount > doneCountMax {
		out = append(out, finding{path, secStart, "doneline", true,
			fmt.Sprintf("Done log has %d entries > %d — roll the oldest to backlog/archive/", doneCount, doneCountMax)})
	}
	if openRowCount > openRowWarnMax {
		out = append(out, finding{path, 0, "openrows", true,
			fmt.Sprintf("lane has %d open rows > %d — prefer a closure/consolidation unit next (steward SKILL §4)", openRowCount, openRowWarnMax)})
	}
	return out, doneItems, rows
}

// conflictSweep reports any git conflict marker committed into a tracked text
// file. It is a repo invariant rather than a board one — the board is only
// where it bit first — so it runs over `git ls-files` regardless of which files
// the caller named. Outside a git checkout it reports nothing rather than
// failing: the gate's job is to catch a marker, not to police the environment.
func conflictSweep() []finding {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		return nil
	}
	var found []finding
	for _, path := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if path == "" || skipConflictScan(path) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 { // unreadable or binary
			continue
		}
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		n := 0
		for sc.Scan() {
			n++
			if conflictRe.MatchString(sc.Text()) {
				found = append(found, finding{path, n, "conflict", false,
					fmt.Sprintf("git conflict marker %q committed — resolve the merge, never commit the markers", sc.Text())})
			}
		}
	}
	return found
}

// skipConflictScan excludes trees whose contents are not ours to police and
// file shapes a marker line cannot meaningfully appear in.
func skipConflictScan(path string) bool {
	if strings.HasPrefix(path, "vendor/") || strings.HasPrefix(path, "deploy/nkeys/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf", ".zip", ".gz", ".woff", ".woff2":
		return true
	}
	return false
}

// isTableMeta reports whether a |-line is a header or the |---| separator (not a data row).
func isTableMeta(line string) bool {
	body := strings.Trim(line, "| ")
	if body == "" {
		return true
	}
	// separator row: only dashes, colons, pipes, spaces
	if strings.Trim(body, "-:| ") == "" {
		return true
	}
	// header row
	return strings.HasPrefix(body, "Item ") || strings.HasPrefix(body, "Item|")
}

// splitRow returns the first cell (item) and last non-empty cell (state) of a data row.
func splitRow(line string) (item, state string) {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	if len(parts) < 2 {
		return "", ""
	}
	item = clean(parts[0])
	for i := len(parts) - 1; i >= 0; i-- {
		if c := clean(parts[i]); c != "" {
			state = c
			break
		}
	}
	return item, state
}

func clean(s string) string   { return strings.TrimSpace(tokenStrip.ReplaceAllString(s, "")) }
func itemKey(s string) string { return strings.ToLower(clean(firstField(s, "·"))) }
func firstField(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}

// depCheck (WARN-only): a still-open row that is "behind X / blocked-on X" where X reads as shipped.
func depCheck(rows []rowRef, done map[string]bool) []finding {
	var out []finding
	openTok := []string{"🚧", "🏗️", "📋", "📐"}
	for _, r := range rows {
		open := false
		for _, t := range openTok {
			if strings.Contains(r.state, t) {
				open = true
				break
			}
		}
		if !open {
			continue
		}
		for _, m := range depRe.FindAllStringSubmatch(r.state, -1) {
			target := strings.ToLower(strings.TrimSpace(m[2]))
			target = strings.TrimRight(target, " .)(")
			if len(target) < 4 {
				continue
			}
			for k := range done {
				if k != "" && len(k) >= 4 && strings.Contains(k, target) {
					out = append(out, finding{r.file, r.line, "dep", true,
						fmt.Sprintf("'%s' is %s '%s' but a Done-log entry matches it — verify the dependency hasn't cleared", trunc(r.item, 40), m[1], strings.TrimSpace(m[2]))})
					break
				}
			}
		}
	}
	return out
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func filterFlags(args []string, strict *bool) []string {
	var files []string
	for _, a := range args {
		switch a {
		case "--strict":
			*strict = true
		default:
			files = append(files, a)
		}
	}
	return files
}
