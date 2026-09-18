//go:build ignore

// lint-gap-params-optional-hop — a weaver target's `row.<column>` Params value
// names a column that is non-null on every row the gap opens.
//
// THE HAZARD. Weaver's dispatch templates a gap's Params off the violating row
// (internal/weaver/strategist.go resolveRowTemplate), and a null or absent
// column is a DATA error: the gap is alerted and skipped, and no redelivery can
// fix it, because the same row projects the same null. A column walked off an
// OPTIONAL MATCH is null on exactly the rows where that hop bound nothing — and
// those are often the rows the gap exists for. Minted four times before this
// gate: café `cafeArrearsReminders` templated a lease off `heldFor` (2026-09-06,
// live — the one account with no lease was never evaluated); wellness
// `CreateBooking`'s descriptor hub (2026-09-15); wellness
// `wellnessBookingChangeNotices` templating `row.startsAt` off the OPTIONAL
// forSession walk, null for the whole call-off window (2026-09-16,
// builder-caught, fixed by the gap's own `<> null` conjunct); and the same lens
// templating `row.instructorName` off an OPTIONAL ledBy walk for an
// instructor-CLEARED class — the exact case the new gap existed to tell
// (2026-09-18, caught cold, the fire's blocking finding).
//
// THE RULE. For every WeaverTargetSpec gap whose Params carry a `row.<column>`
// value, resolve the target's LensRef to its lens in the same Definition, parse
// the cypher on the full engine, and classify `<column>` through
// CompiledRule.OptionalHopColumns + ReturnColumnGuardsNonNull:
//
//   - CLEAN when the column reads no OPTIONAL-only variable (the anchor, a
//     required MATCH, a parameter, a literal);
//   - CLEAN when the column is null-safe at its top (a literal, or a coalesce
//     whose last argument is a non-null literal) — the fix the 2026-09-18
//     sighting took;
//   - CLEAN when the gap's OWN column carries `<expr> <> null` (or
//     `NOT (<expr> = null)`) over the templated column's exact expression as a
//     top-level AND conjunct — the gap cannot open while the hop is unbound
//     (the 2026-09-16 fix);
//   - a FINDING otherwise: the target dispatches a param Weaver will refuse on
//     the rows where the hop misses.
//
// A lens whose cypher the engine cannot classify exhaustively (a WITH boundary
// the scope walk refuses, an unnamed alias) is reported as UNCLASSIFIED rather
// than passed: a gate that cannot tell must not answer clean. A target whose
// LensRef resolves to no lens in its Definition, or to a lens with no single
// Spec (an eventStream lens, a multi-branch personal lens), is out of scope
// here — lint-gap-column-declaration owns the LensRef binding itself.
//
// SCOPE: the compiled corpus (pkgregistry.Names()/Lookup), plus the bootstrap
// kernel packages the way lint-gap-column-declaration walks them.
//
// STRICT=1 exits non-zero on any finding; unset, it reports and exits 0. The
// self-test replays the minting shape (an OPTIONAL-hop name templated bare) and
// the three clean shapes before the corpus is walked; a self-test failure exits
// 2 whatever STRICT says.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

const rowTemplatePrefix = "row."

type stats struct {
	packages, targets, params, unclassified int
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	runSelfTest()

	var findings []string
	var st stats
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: pkgregistry.Names() lists this package but Lookup does not resolve it", name))
			continue
		}
		if len(def.WeaverTargets) == 0 {
			continue
		}
		st.packages++
		findings = append(findings, checkDefinition(name, def, &st)...)
	}
	if st.params == 0 {
		findings = append(findings, "lint-gap-params-optional-hop: examined ZERO row.<column> params — the extraction is broken, and a gate that checked nothing has no all-clear to give")
	}
	sort.Strings(findings)
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-gap-params-optional-hop: clean — %d target(s) across %d package(s); %d row.<column> param(s) classified, %d unclassified\n",
			st.targets, st.packages, st.params, st.unclassified)
		return
	}
	fmt.Printf("lint-gap-params-optional-hop: %d issue(s) — %d target(s) across %d package(s), %d row.<column> param(s) examined\n",
		len(findings), st.targets, st.packages, st.params)
	if strict {
		os.Exit(1)
	}
}

// checkDefinition returns every finding for one package Definition.
func checkDefinition(pkg string, def pkgmgr.Definition, st *stats) []string {
	lenses := map[string]pkgmgr.LensSpec{}
	for _, l := range def.Lenses {
		lenses[l.CanonicalName] = l
	}
	var findings []string
	for _, t := range def.WeaverTargets {
		st.targets++
		lens, ok := lenses[t.LensRef]
		if !ok || lens.Spec == "" {
			continue
		}
		cr, err := full.New().Parse(lens.Spec)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: target %s: lens %s does not parse on the full engine: %v", pkg, t.TargetID, t.LensRef, err))
			continue
		}
		fcr, isFull := cr.(*full.CompiledRule)
		if !isFull {
			continue
		}
		cols, exhaustive := fcr.OptionalHopColumns()
		byAlias := map[string]full.OptionalHopColumn{}
		for _, c := range cols {
			byAlias[c.Alias] = c
		}
		gapNames := make([]string, 0, len(t.Gaps))
		for g := range t.Gaps {
			gapNames = append(gapNames, g)
		}
		sort.Strings(gapNames)
		for _, gap := range gapNames {
			params := t.Gaps[gap].Params
			names := make([]string, 0, len(params))
			for p := range params {
				names = append(names, p)
			}
			sort.Strings(names)
			for _, p := range names {
				col, templated := strings.CutPrefix(params[p], rowTemplatePrefix)
				if !templated {
					continue
				}
				st.params++
				if !exhaustive {
					st.unclassified++
					findings = append(findings, fmt.Sprintf("%s: target %s gap %s param %q = row.%s: lens %s could not be classified exhaustively (a WITH boundary or an unnamed alias) — the gate cannot answer clean for it",
						pkg, t.TargetID, gap, p, col, t.LensRef))
					continue
				}
				c, present := byAlias[col]
				if !present {
					findings = append(findings, fmt.Sprintf("%s: target %s gap %s param %q templates row.%s, which lens %s does not RETURN", pkg, t.TargetID, gap, p, col, t.LensRef))
					continue
				}
				if len(c.OptionalVars) == 0 || c.NullSafe || fcr.ReturnColumnGuardsNonNull(gap, col) {
					continue
				}
				findings = append(findings, fmt.Sprintf("%s: target %s gap %s param %q templates row.%s, read off the OPTIONAL hop(s) %s of lens %s with no coalesce and no `<> null` conjunct on the gap — Weaver refuses the dispatch on every row where the hop binds nothing (strategist.go resolveRowTemplate); coalesce the column to a literal, or guard the gap on it",
					pkg, t.TargetID, gap, p, col, strings.Join(c.OptionalVars, ","), t.LensRef))
			}
		}
	}
	return findings
}

// runSelfTest replays the minting shape and the three clean shapes through
// checkDefinition, so a vector proven here is proven for the real run.
func runSelfTest() {
	const spec = `MATCH (b:booking {key: $actorKey})
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (se)-[:ledBy]->(i:instructor)
RETURN
  b.key AS entityKey,
  se.schedule.data.startsAt AS startsAt,
  i.profile.data.displayName AS instructorName,
  coalesce(i.profile.data.displayName, '') AS instructorNameSafe,
  ((se.schedule.data.startsAt <> null) AND (b.status.data.value = 'booked')) AS missing_guarded,
  (b.status.data.value = 'booked') AS missing_bare`
	def := pkgmgr.Definition{
		Lenses: []pkgmgr.LensSpec{{CanonicalName: "probe", Spec: spec}},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{{
			TargetID: "probe", LensRef: "probe",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_guarded": {Params: map[string]string{"anchor": "row.entityKey", "when": "row.startsAt", "who": "row.instructorName"}},
				"missing_bare":    {Params: map[string]string{"when": "row.startsAt", "whoSafe": "row.instructorNameSafe", "literal": "json:0"}},
			},
		}},
	}
	var st stats
	got := checkDefinition("selftest", def, &st)
	want := []string{
		`selftest: target probe gap missing_bare param "when" templates row.startsAt, read off the OPTIONAL hop(s) se`,
		`selftest: target probe gap missing_guarded param "who" templates row.instructorName, read off the OPTIONAL hop(s) i`,
	}
	fail := func(msg string) {
		fmt.Println("lint-gap-params-optional-hop: SELF-TEST FAILED — " + msg)
		for _, g := range got {
			fmt.Println("  got: " + g)
		}
		os.Exit(2)
	}
	if len(got) != len(want) {
		fail(fmt.Sprintf("want %d finding(s), got %d", len(want), len(got)))
	}
	sort.Strings(got)
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			fail("finding " + got[i] + " does not match " + want[i])
		}
	}
	if st.params != 5 {
		fail(fmt.Sprintf("want 5 row.<column> params examined, got %d", st.params))
	}
}
