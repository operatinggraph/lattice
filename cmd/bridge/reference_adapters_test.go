package main

import (
	"regexp"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// envGatedAdapters is the documented exception to the composition-root
// coverage gate below: adapter names a shipped package declares that run()
// deliberately does NOT register in referenceAdapters, because registering
// one unconditionally would be wrong, not merely incomplete.
//
// capabilityAuthor is the sole entry today: the real adapter holds a vendor
// credential path (reached through the model-runner fleet) and spends money
// per call, so registering nothing is the deliberate default (run() only adds
// it when BRIDGE_CAPABILITY_AUTHOR=real). A future gated adapter must be added
// here explicitly — an unreviewed miss stays a red CI job, not a silent
// carve-out.
var envGatedAdapters = map[string]bool{
	"capabilityAuthor": true,
}

// censusedAdapter names one adapter name declared by a package's structured
// data, and which package declared it — the census, one entry per (name,
// package) occurrence found.
type censusedAdapter struct {
	name    string
	pkgName string
}

// censusDeclaredAdapters walks every registered package's Definition over the
// three structured sources that name a bridge adapter — never doc prose — and
// returns one entry per declaration found:
//
//   - LoomPatterns[].Steps[] where Kind == "externalTask" -> Adapter
//   - WeaverTargets[].Augur (when present) -> Adapter, defaulting to "augur"
//     when the block is present but Adapter is empty (Contract #10 §10.8's
//     documented default)
//   - DDLs[].Script literal `"adapter": "<name>"` assignments (see
//     censusFromDDLScripts) — a DDL op that emits an external.<adapter> event
//     directly, with no LoomPattern step or WeaverTarget in between
//     (clinic-reminders / wellness-reminders' notification dispatch).
//
// GapActionSpec.Adapter — on a WeaverTarget's Gaps[] and on their Actions[] —
// is deliberately NOT a source. Despite the name it never selects a bridge
// adapter: its only consumer is admission pacing, where it keys
// AdmissionPolicy.AdapterRates (internal/weaver/evaluator.go's admitGap ->
// internal/weaver/admission.go). A target may legitimately name a rate bucket
// no bridge adapter answers to, so censusing it would red this gate over a
// correct package.
//
// Out of reach by construction: an adapter named by a loomPattern that is
// materialized at RUNTIME from an AI-authored capability proposal
// (internal/pkgmgr's capability materializer) never enters pkgregistry, so no
// static census can see it.
func censusDeclaredAdapters() []censusedAdapter {
	var out []censusedAdapter
	for _, pkgName := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(pkgName)
		if !ok {
			continue
		}
		out = append(out, censusFromLoomPatterns(pkgName, def)...)
		out = append(out, censusFromWeaverTargets(pkgName, def)...)
		out = append(out, censusFromDDLScripts(pkgName, def)...)
	}
	return out
}

func censusFromLoomPatterns(pkgName string, def pkgmgr.Definition) []censusedAdapter {
	var out []censusedAdapter
	for _, pattern := range def.LoomPatterns {
		for _, step := range pattern.Steps {
			if step.Kind == "externalTask" && step.Adapter != "" {
				out = append(out, censusedAdapter{name: step.Adapter, pkgName: pkgName})
			}
		}
	}
	return out
}

func censusFromWeaverTargets(pkgName string, def pkgmgr.Definition) []censusedAdapter {
	var out []censusedAdapter
	for _, target := range def.WeaverTargets {
		if target.Augur == nil {
			continue
		}
		name := target.Augur.Adapter
		if name == "" {
			name = "augur"
		}
		out = append(out, censusedAdapter{name: name, pkgName: pkgName})
	}
	return out
}

// ddlScriptAdapterLiteral matches a literal `"adapter": "<name>"` key/value
// pair in a Starlark script's source text — a DDL op building an
// external.<adapter> event body directly (map syntax, not Go). It requires a
// quoted string value, so it does NOT match the variable form a script writes
// as `"adapter": adapter` (lease-signing's poll/dispatch scripts read the name
// out of the op's own params at runtime); that form assembles the name at
// runtime and is out of a static census's reach by construction.
var ddlScriptAdapterLiteral = regexp.MustCompile(`"adapter"\s*:\s*"([A-Za-z][A-Za-z0-9_-]*)"`)

// censusFromDDLScripts scans each DDL's COMPILED Script body (the Starlark
// source string baked into the Definition, not the .go source file that
// declares it) for a literal adapter name via ddlScriptAdapterLiteral. This is
// the one census source that reads script text rather than a typed field, so it
// catches an op that mints its external.<adapter> event inline with no
// LoomPattern step in between — and it catches nothing beyond a literal: a name
// assembled at runtime from a variable or string concatenation inside the
// script is invisible to it.
func censusFromDDLScripts(pkgName string, def pkgmgr.Definition) []censusedAdapter {
	var out []censusedAdapter
	for _, ddl := range def.DDLs {
		for _, match := range ddlScriptAdapterLiteral.FindAllStringSubmatch(ddl.Script, -1) {
			out = append(out, censusedAdapter{name: match[1], pkgName: pkgName})
		}
	}
	return out
}

// TestCensusDeclaredAdaptersIsNotVacuous guards the gate test below against
// its own census going silently empty (or a single source family silently
// dying): a bug that made censusDeclaredAdapters return nothing, or that
// dropped one of its source families, would make
// TestReferenceAdapters_CoverEveryPackageDeclaredAdapter pass on a shrunken
// (or empty) set regardless of what run() actually registers. It names one
// adapter EXCLUSIVE to each independently-walked source family, so killing any
// one traversal fails here even though the surviving families keep the overall
// census non-empty: "docGen" comes only from a LoomPattern externalTask step,
// "augur" only from a WeaverTarget's Augur block, "notification" only from a
// DDL script literal. A name that stops being exclusive to its family weakens
// this guard, so re-derive the trio if the corpus's declarations move.
func TestCensusDeclaredAdaptersIsNotVacuous(t *testing.T) {
	census := censusDeclaredAdapters()
	if len(census) == 0 {
		t.Fatal("censusDeclaredAdapters returned nothing; the census walk is broken")
	}
	for _, want := range []string{"docGen", "augur", "notification"} {
		found := false
		for _, c := range census {
			if c.name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("census = %+v, want at least one %q entry", census, want)
		}
	}
}

// TestReferenceAdapters_CoverEveryPackageDeclaredAdapter is the composition-root
// gate: every adapter name any shipped package declares (by structured data,
// not doc prose) must have a registration in referenceAdapters, except the
// documented env-gated exception. Without this test, a package can ship an
// adapter name with no bridge-side registration, and the failure surfaces only
// live, as a BridgeAdapterMissing config error on the first real dispatch.
func TestReferenceAdapters_CoverEveryPackageDeclaredAdapter(t *testing.T) {
	registered := referenceAdapters(nil, nil, 0)

	census := censusDeclaredAdapters()
	seen := map[string]bool{}
	for _, c := range census {
		if envGatedAdapters[c.name] {
			continue
		}
		if seen[c.name] {
			continue
		}
		seen[c.name] = true
		if _, ok := registered[c.name]; !ok {
			t.Errorf("package %q declares adapter %q, but referenceAdapters does not register it "+
				"(every live dispatch to it will fail with BridgeAdapterMissing)", c.pkgName, c.name)
		}
	}
}

// TestEnvGatedAdapters_EachEntryIsRealAndUnregistered keeps the carve-out above
// honest. Every skip it grants is a name the gate stops checking, so each entry
// must earn itself twice: the census must actually declare it (or the entry is
// stale, silently excusing nothing), and referenceAdapters must actually leave
// it out (or the entry is a contradiction — a name registered unconditionally
// while claiming to be env-gated). Without this, widening the map is the one
// edit that can blind the gate for a name and still pass every test in the file.
func TestEnvGatedAdapters_EachEntryIsRealAndUnregistered(t *testing.T) {
	census := censusDeclaredAdapters()
	registered := referenceAdapters(nil, nil, 0)

	for name := range envGatedAdapters {
		declared := false
		for _, c := range census {
			if c.name == name {
				declared = true
				break
			}
		}
		if !declared {
			t.Errorf("envGatedAdapters carves out %q, but no shipped package declares it — "+
				"a stale carve-out excuses nothing and hides the next real miss", name)
		}
		if _, ok := registered[name]; ok {
			t.Errorf("envGatedAdapters carves out %q, but referenceAdapters registers it "+
				"unconditionally — the carve-out contradicts the registration", name)
		}
	}
}

// TestReferenceAdapters_RegistersAugur asserts referenceAdapters registers
// "augur": an unregistered "augur" makes every Weaver escalation dispatch
// (Contract #10 §10.8) fail closed with a BridgeAdapterMissing config error.
//
// It is deliberately independent of the census — the platform owes the Augur
// tier an adapter whether or not any installed vertical currently declares an
// escalation. The census reaches "augur" only through a vertical's Augur block
// (packages/augur's own dispatch script builds the name from a variable, which
// no literal scan can see), so without this test the gate's coverage of the
// platform's own reasoning adapter would ride on one vertical's configuration.
func TestReferenceAdapters_RegistersAugur(t *testing.T) {
	registered := referenceAdapters(nil, nil, 0)

	if _, ok := registered["augur"]; !ok {
		t.Fatal(`referenceAdapters does not register "augur"`)
	}
}
