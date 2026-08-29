package testutil

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"
)

// baselineData is the checked-in read-posture debt the drift guard ratchets
// against. See the file itself for what an entry means and how it is
// regenerated.
//
// It is DATA, not Go, for two reasons that point the same way. A baseline row
// necessarily names a concrete vertical's types and operations, and engine code
// under internal/ must stay type-agnostic — an invariant packages/ asserts by
// grepping internal/**/*.go for its own concrete tokens
// (lease-signing/lens_unit_test.go, service-domain/type_agnostic_test.go). And
// a generated measurement is more reviewable as a sorted, greppable table than
// as a Go map literal: a reviewer reads the diff, not the syntax.
//
//go:embed read_drift_baseline.txt
var baselineData string

// baselineTables returns the baselined read and walk shapes, keyed by
// operationType then shape. Parsed once per process; a parse failure is
// returned to every caller, not just the first.
//
// The error is a return value rather than a panic because the parse happens
// inside a sync.Once: a panic there unwinds the first caller and leaves the
// Once "done" with nil tables, so every later guard in the binary would run
// against an EMPTY baseline — the guard reddening the whole corpus for a reason
// stated only in the first goroutine's stack trace. A returned error reaches
// each guard's own t.Fatalf, naming the bad line where the test can see it.
func baselineTables() (reads, walks map[string]map[string]struct{}, err error) {
	baselineOnce.Do(func() { baselineSets, baselineErr = parseBaseline(baselineData) })
	if baselineErr != nil {
		return nil, nil, baselineErr
	}
	return baselineSets[baselineKindRead], baselineSets[baselineKindWalk], nil
}

// baselineKind distinguishes the two tables the one file carries.
type baselineKind string

const (
	baselineKindRead baselineKind = "read"
	baselineKindWalk baselineKind = "walk"
)

var (
	baselineOnce sync.Once
	baselineSets map[baselineKind]map[string]map[string]struct{}
	baselineErr  error
)

// parseBaseline reads the table: blank lines and `#` comments are prose, every
// other line is `<kind> TAB <operationType> TAB <shape>`. A malformed line is an
// error rather than a skip — a row this cannot read is a row the guard silently
// stops honouring, which is the one failure mode nobody would notice.
//
// Every field is whitespace-trimmed, and a comment may be indented. Both make
// the file survive being hand-edited, which is the sanctioned way to add a row
// with its reason: a Windows checkout's trailing \r would otherwise leave every
// shape parsed-but-unmatchable (three fields, none of them equal to anything the
// guard computes), and an indented `#` annotation under a row would be read as
// data.
func parseBaseline(data string) (map[baselineKind]map[string]map[string]struct{}, error) {
	out := map[baselineKind]map[string]map[string]struct{}{
		baselineKindRead: {},
		baselineKindWalk: {},
	}
	for n, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("read_drift_baseline.txt line %d: want 3 tab-separated fields, got %d: %q", n+1, len(parts), line)
		}
		kind, op, shape := baselineKind(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		byOp, ok := out[kind]
		if !ok {
			return nil, fmt.Errorf("read_drift_baseline.txt line %d: unknown kind %q, want %q or %q", n+1, kind, baselineKindRead, baselineKindWalk)
		}
		if op == "" || shape == "" {
			return nil, fmt.Errorf("read_drift_baseline.txt line %d: operationType and shape must both be non-empty: %q", n+1, line)
		}
		if byOp[op] == nil {
			byOp[op] = map[string]struct{}{}
		}
		byOp[op][shape] = struct{}{}
	}
	return out, nil
}
