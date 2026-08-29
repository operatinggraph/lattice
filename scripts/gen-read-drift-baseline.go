//go:build ignore

// gen-read-drift-baseline.go — regenerates internal/testutil/read_drift_baseline.txt,
// the measured read-posture debt the drift guard ratchets against.
//
// Input is a read census: one JSON line per script execution, written by
// internal/testutil's ReadCensus when LATTICE_READ_CENSUS names a file. Produce
// one over every package that stands up a testutil.CapabilityPipeline, then
// regenerate:
//
//	export POSTGRES_TEST_DSN=postgres://lattice:lattice_dev@127.0.0.1:5433/lattice?sslmode=disable
//	rm -f /tmp/read-census.jsonl
//	LATTICE_READ_CENSUS=/tmp/read-census.jsonl go test ./packages/... \
//	    ./internal/processor/... ./internal/pkgmgr/... ./internal/aiagent/... \
//	    ./internal/refractor/... ./cmd/lattice/... -count=1 -p 4
//	LATTICE_READ_CENSUS=/tmp/read-census.jsonl go test -tags cryptoshred ./internal/cryptoshred/... -count=1
//	go run ./scripts/gen-read-drift-baseline.go /tmp/read-census.jsonl
//
// An entry is emitted only for what the guard would otherwise REJECT: a live
// read whose vertex root no walk in the same execution surfaced, and a walk the
// envelope's contextHint did not declare. Reads already sanctioned as class-(e)
// follow-ups never reach the file, so regenerating after a declaration lands
// shrinks it.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/testutil"
)

// censusLine mirrors testutil.ReadCensusLine's wire shape. Only the fields the
// baseline is derived from are read back.
type censusLine struct {
	OperationType      string   `json:"operationType"`
	LiveReads          []string `json:"liveReads"`
	EnumeratedVertices []string `json:"enumeratedVertices"`
	Enumerations       []struct {
		Hub       string
		Relation  string
		Direction string
	} `json:"enumerations"`
	HintEnumerations []struct {
		Hub       string `json:"hub"`
		Relation  string `json:"relation"`
		Direction string `json:"direction"`
	} `json:"hintEnumerations"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/gen-read-drift-baseline.go <census.jsonl>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-read-drift-baseline: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	reads := map[string]map[string]struct{}{}
	walks := map[string]map[string]struct{}{}
	// everDeclared records, per operationType, every enumeration shape some
	// execution's envelope DID declare — the evidence that a declaring channel
	// exists for that op at all.
	everDeclared := map[string]map[string]struct{}{}
	add := func(m map[string]map[string]struct{}, op, shape string) {
		if m[op] == nil {
			m[op] = map[string]struct{}{}
		}
		m[op][shape] = struct{}{}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	lines := 0
	for sc.Scan() {
		var l censusLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			fmt.Fprintf(os.Stderr, "gen-read-drift-baseline: skipping unparseable line: %v\n", err)
			continue
		}
		lines++
		walked := map[string]struct{}{}
		for _, v := range l.EnumeratedVertices {
			walked[v] = struct{}{}
		}
		for _, k := range l.LiveReads {
			if _, ok := walked[testutil.VertexRoot(k)]; ok {
				continue
			}
			add(reads, l.OperationType, testutil.NormalizeReadKey(k))
		}
		declared := map[string]struct{}{}
		for _, h := range l.HintEnumerations {
			shape := testutil.NormalizeEnumeration(h.Hub, h.Relation, h.Direction)
			declared[shape] = struct{}{}
			add(everDeclared, l.OperationType, shape)
		}
		for _, e := range l.Enumerations {
			shape := testutil.NormalizeEnumeration(e.Hub, e.Relation, e.Direction)
			if _, ok := declared[shape]; ok {
				continue
			}
			add(walks, l.OperationType, shape)
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-read-drift-baseline: %v\n", err)
		os.Exit(1)
	}

	var b strings.Builder
	b.WriteString(header)
	writeTable(&b, "read", "the live Core KV reads no declaration covers", reads)
	writeTable(&b, "walk", "the kv.Links enumerations no contextHint declares", walks)

	const out = "internal/testutil/read_drift_baseline.txt"
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gen-read-drift-baseline: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("gen-read-drift-baseline: %d executions -> %s\n", lines, out)
	fmt.Printf("  reads: %d operations, %d shapes\n", len(reads), count(reads))
	fmt.Printf("  walks: %d operations, %d shapes\n", len(walks), count(walks))

	// The walk split the header points at: a baselined walk shape whose own op
	// is seen declaring that exact shape somewhere is fixture under-declaration
	// (a declaring channel demonstrably exists); the rest have none observed.
	fixable, unfixable := 0, 0
	fixableOps := map[string]struct{}{}
	for op, shapes := range walks {
		for shape := range shapes {
			if _, ok := everDeclared[op][shape]; ok {
				fixable++
				fixableOps[op] = struct{}{}
				continue
			}
			unfixable++
		}
	}
	fmt.Printf("  walk split: %d shape(s) across %d op(s) are fixture under-declaration (that op declares the same shape elsewhere); %d have no observed declaring channel\n",
		fixable, len(fixableOps), unfixable)
}

func count(m map[string]map[string]struct{}) int {
	n := 0
	for _, s := range m {
		n += len(s)
	}
	return n
}

func writeTable(b *strings.Builder, kind, what string, m map[string]map[string]struct{}) {
	fmt.Fprintf(b, "# %s rows: per operationType, %s.\n#\n", kind, what)
	ops := make([]string, 0, len(m))
	for op := range m {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		shapes := make([]string, 0, len(m[op]))
		for s := range m[op] {
			shapes = append(shapes, s)
		}
		sort.Strings(shapes)
		for _, s := range shapes {
			fmt.Fprintf(b, "%s\t%s\t%s\n", kind, op, s)
		}
	}
	b.WriteString("\n")
}

const header = `# MEASURED RESIDUE, NOT A PERMISSION LIST.
#
# Every row is a shape some script read or walked live in a test run while the
# submitting envelope declared nothing that covers it. The file exists so the
# drift guard (read_drift_guard.go) can be armed on every CapabilityPipeline
# TODAY: no NEW undeclared read or walk can land, whatever these rows say.
#
# A row is a shape the guard TOLERATES, never one it endorses. Reads already
# sanctioned as Contract #2 §2.5 class-(e) follow-ups on an enumeration — the
# key's vertex root was DISCOVERED by this execution's own kv.Links walk, hubs
# excluded — are admitted by the guard itself and never appear here.
#
# WHAT THE READ ROWS ACTUALLY ARE — three different things, and only the first
# two are removable. Do not read the file as one undifferentiated debt pile; the
# classification was measured against the declaring sources, not assumed.
#
#  1. The envelope the TEST builds under-declares what the real dispatcher
#     already sends. The script is fine and production declares correctly; the
#     fixture omits it, which means the test drives the lazy live-read path
#     production never takes. Fix the fixture, drop the row.
#  2. A genuine declaration gap, payload-direct and therefore declarable —
#     the worksAt confinement reads reachable from the submitted payload. Fix
#     the descriptor, drop the row.
#  3. Sanctioned and undeclarable BY DESIGN — class-(c) config reads
#     (deliberately outside OCC) and reads chained off a value only a prior read
#     resolves. Nothing should ever declare these; the rows record where the
#     guard must stay quiet.
#
# WHAT THE WALK ROWS ACTUALLY ARE. A walk is declarable through
# ContextHint.Enumerations, and four channels populate it: Loom StepSpec, Weaver
# GapActionSpec, a hand-built envelope (cmd/lattice/candidates/candidates.go
# does this for MergeIdentity), and a client passing enumerations through the
# gateway or Loupe (internal/gateway/gateway.go, cmd/loupe/op.go). The channel
# that does NOT exist is the descriptor one: pkgmgr.OpDispatchSpec carries Reads
# and OptionalReads but no Enumerations field (internal/pkgmgr/definition.go),
# so an ordinary descriptor-dispatched op cannot declare a walk at all. Those
# rows are a missing vocabulary, not script debt.
#
# The two are not the same fix and the split is measurable: a walk row whose op
# is ALSO seen declaring that exact shape in some other execution is a fixture
# under-declaration (class 1 applied to walks), because a declaring channel
# demonstrably exists for it. The rest have no observed declaring channel. The
# generator prints both counts each time it runs — read them off that output
# rather than trusting a number frozen into this comment.
#
# Rows are REMOVED when a dispatcher declares the key or walk. A row may be ADDED
# BY HAND, under a comment line stating why, when a read or walk is sanctioned and
# undeclarable — that is the path the guard failure itself points at.
# REGENERATING is not that path: it re-records everything the run observed,
# including drift that has just landed, so its DIFF must be reviewed row by row
# and it must never be run to make a failing test pass.
#
# Format: <kind> TAB <operationType> TAB <normalized shape>, sorted. Fields are
# whitespace-trimmed and an indented # comment is prose, so hand annotation is
# safe.
# Regenerate with the census + command in scripts/gen-read-drift-baseline.go.

`
