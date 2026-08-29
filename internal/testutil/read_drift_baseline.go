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
// operationType then shape. Parsed once per process.
func baselineTables() (reads, walks map[string]map[string]struct{}) {
	baselineOnce.Do(func() { baselineSets = parseBaseline(baselineData) })
	return baselineSets[baselineKindRead], baselineSets[baselineKindWalk]
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
)

// parseBaseline reads the generated table: blank lines and `#` comments are
// prose, every other line is `<kind> TAB <operationType> TAB <shape>`. A
// malformed line panics rather than being skipped — the file is generated and
// checked in, so a line this cannot read means the generator and the guard have
// diverged, and silently ignoring it would widen the guard's blind spot exactly
// where it is least visible.
func parseBaseline(data string) map[baselineKind]map[string]map[string]struct{} {
	out := map[baselineKind]map[string]map[string]struct{}{
		baselineKindRead: {},
		baselineKindWalk: {},
	}
	for n, line := range strings.Split(data, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			panic(fmt.Sprintf("testutil: read_drift_baseline.txt line %d: want 3 tab-separated fields, got %d: %q", n+1, len(parts), line))
		}
		kind := baselineKind(parts[0])
		byOp, ok := out[kind]
		if !ok {
			panic(fmt.Sprintf("testutil: read_drift_baseline.txt line %d: unknown kind %q", n+1, parts[0]))
		}
		if byOp[parts[1]] == nil {
			byOp[parts[1]] = map[string]struct{}{}
		}
		byOp[parts[1]][parts[2]] = struct{}{}
	}
	return out
}
