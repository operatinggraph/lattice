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
			if _, ok := walked[vertexRoot(k)]; ok {
				continue
			}
			add(reads, l.OperationType, testutil.NormalizeReadKey(k))
		}
		declared := map[string]struct{}{}
		for _, h := range l.HintEnumerations {
			declared[testutil.NormalizeEnumeration(h.Hub, h.Relation, h.Direction)] = struct{}{}
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
}

func count(m map[string]map[string]struct{}) int {
	n := 0
	for _, s := range m {
		n += len(s)
	}
	return n
}

// vertexRoot mirrors the guard's own rule: the 3-segment vertex key an aspect
// belongs to, and "" for anything that is not a vertex key.
func vertexRoot(key string) string {
	if !strings.HasPrefix(key, "vtx.") {
		return ""
	}
	p := strings.Split(key, ".")
	if len(p) < 3 {
		return ""
	}
	return strings.Join(p[:3], ".")
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

const header = `# MEASURED DEBT, NOT A PERMISSION LIST.
#
# Every row below is a packages/ script reading or walking Core KV that the
# operation's dispatcher never declared — the class-(b) read-posture debt
# Contract #2 §2.5 and CLAUDE.md name, counted rather than described. The file
# exists so the drift guard (read_drift_guard.go) can be armed on every
# CapabilityPipeline TODAY: new drift cannot land, while the sweep that removes
# these rows runs at its own pace.
#
# A row is a shape the guard TOLERATES, never one it endorses. Reads already
# sanctioned as Contract #2 §2.5 class-(e) follow-ups on an enumeration are
# admitted by the guard itself and never appear here — what remains is the
# residue, and the file only shrinks as declarations land.
#
# Rows are REMOVED when a dispatcher declares the key or walk, and are added
# ONLY by regenerating from a fresh census — never by hand to silence a failing
# test. A guard failure means a script started reading something new; the fix is
# the declaration the failure names.
#
# Format: <kind> TAB <operationType> TAB <normalized shape>, sorted.
# GENERATED — regenerate with the census + command in
# scripts/gen-read-drift-baseline.go.

`
