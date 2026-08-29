//go:build ignore

// lint-gap-column-declaration — every `missing_*` column that lands in a weaver
// target's rows is declared in that target's `gaps` map (Contract #10 §10.8;
// weaver-decline-retry-substrate-native-design.md §3.2 row 8).
//
// THE HAZARD. `internal/pkgmgr/definition.go`'s WeaverTargetSpec states the
// contract in prose — "Gaps maps each `missing_<gap>` violation column to the
// remediation action the engine runs when that column is set" — and nothing
// enforces it. Weaver's dispatchGap (internal/weaver/evaluator.go) looks the
// open column up in `target.Gaps`; on a miss with no Augur escalation it takes
// the config-error arm and returns substrate.NakWithLongDelay. That is not a
// drop: the row holds a MaxAckPending slot and re-runs the whole
// clearClosedMarks preamble on every 5-minute redelivery floor, forever, for as
// long as the column stays true. The long floor is deliberate — a package
// re-author IS the fix, and it projects no new row, so an Ack would strand the
// entity violating with nothing to re-deliver it — but it only pays for itself
// if the miss is genuinely an authoring omission.
//
// The engine cannot tell an authoring omission from a column a package projects
// DELIBERATELY with no remediation (an operator-closed condition, say). Both
// arrive as "column true, no gaps entry". So the deliberate case is made
// declarable instead — the sanctioned form is a `surface` gap, which raises a
// per-(target, entity, column) Health issue and Acks — and this gate asserts the
// resulting invariant: projected `missing_*` ⊆ declared gaps. With it, a
// long-Nak means what it says.
//
// WHAT THE ROW BODY ACTUALLY CONTAINS. Refractor's projection driver
// (internal/refractor/projection/driver.go) writes every Output.BodyColumns
// entry into the envelope AND then writes every Output.StaticEmptyColumns entry
// as an empty array, and Weaver deserializes that envelope verbatim. So the row
// body is the UNION of the two lists, not BodyColumns alone — a `missing_*` name
// declared only in StaticEmptyColumns is a present key at runtime. The installer's
// sibling check states the same thing and computes the same union
// (internal/pkgmgr/orchestrationguard.go's validateGapCompanionPair and its
// declaredRowBodyColumns helper); this gate mirrors that helper rather than
// re-deciding it. Reading BodyColumns alone lets the StaticEmptyColumns shape
// through untouched.
//
// The cypher is deliberately NOT the input. A RETURN column can be composed,
// aliased or carried through a WITH, and a cypher-regex census attributes it to
// the wrong lens (`scripts/lint-lens-anchors.go` takes the regex route; it is the
// shape not copied here). Over-declaration in the descriptor fails SAFE — the
// author writes a gaps entry nothing fires; a column absent from the descriptor
// cannot reach Weaver at all, because it is never written into the row.
//
// WHICH LENSES FEED A TARGET: THE KEY PREFIX, NOT LensRef. LensRef is a proxy.
// The real binding is positional: a targetId IS the row prefix its rows carry in
// the SHARED weaver-targets bucket (`<targetId>.<entityId>` — definition.go's
// TargetID doc, installer.go's ErrWeaverTargetIDCollision), and lane-1 dispatch
// "watches weaver-targets directly, not via LensRef" (definition.go's LensRef
// doc). So a target's row set is every key under `<targetId>.`, whoever wrote it:
// a SECOND lens in the package whose OutputKeyPattern starts with the same
// segment feeds the same target's rows while naming no target at all. This gate
// therefore collects gap columns from every weaver-targets lens in the Definition
// whose OutputKeyPattern prefix equals the TargetID, and separately asserts that
// the LensRef'd lens's own prefix equals the TargetID — the two identifiers are
// NOT interchangeable (augurDispatchPending → augurDispatch,
// capabilityAuthorPending → capabilityAuthorDispatch, identityErasureResidue →
// identityErasureComplete all bind a lens whose canonicalName differs from the
// targetId, and in each the PATTERN prefix is what agrees).
//
// A weaver-targets lens whose prefix matches no declared target is NOT a finding:
// Weaver evaluates rows under a target's prefix, so such rows reach no
// dispatchGap at all (objects-base's objectAttachments display read model is that
// shape today — it shares the bucket and declares no gap columns).
//
// WHAT THIS GATE CANNOT READ, AND SAYS SO. Output is consulted only on the
// actor-aggregate projection path (internal/refractor/projection/plan.go's
// IsActorAggregate/Compile). A PLAIN weaver-targets lens is a first-class
// production shape (cmd/refractor/main.go has a guard written for exactly it) and
// on that path the cypher's own RETURN aliases are written verbatim, so its
// Output — if it even carries one — is an inert descriptor this gate must not
// pretend to have read. Such a lens, and one with a nil Output, and one whose
// OutputKeyPattern yields no `<prefix>.` segment, are each reported as
// UNREADABLE-BY-THIS-GATE: a finding in its own right, distinct in wording from a
// real undeclared column, naming why the columns could not be read. It is not a
// violation the author caused and there is no annotation to add — the remedy is
// structural, and the gate says which. Zero such lenses exist in the corpus
// today. The same posture covers an empty LensRef, a LensRef this Definition does
// not resolve (internal/pkgmgr/build.go admits a bare NanoID naming a lens in an
// ALREADY-INSTALLED package, which is not statically resolvable), and a target
// left with no readable source lens: reported, never silently passed, because a
// check that quietly passes what it could not resolve is the lint-gates dossier's
// "resolved, not counted" failure.
//
// SCOPE: THE COMPILED CORPUS ONLY. The walk is pkgregistry.Names()/Lookup over
// compiled Definitions, as scripts/lint-package-standard.go does, so the Go
// compiler resolves every constant, helper closure and fmt.Sprintf composition a
// package uses to build a column name or a gaps key — a hand list or a text scan
// rots against exactly those idioms.
//
// It does NOT cover every way a meta.weaverTarget reaches a running Weaver. The
// AI-authored capability-artifact path installs one too — EnabledArtifactKinds
// admits the "weaverTarget" kind (internal/pkgmgr/capabilitymaterializer.go),
// weaverTargetArtifactDefinition materializes a single-target Definition,
// DefinitionForCapabilityArtifact (internal/pkgmgr/capabilityapply.go) applies it,
// and cmd/loupe/review.go / cmd/lattice-pkg/main.go drive it — and that path
// DEFERS LensRef resolution to build time by design, so nothing static holds this
// invariant there. Closing it is out of this gate's scope. Its counterpart is
// runtime and observational: cmd/loupe/weaver.go derives an `Unhandled` list per
// target from the columns actually OBSERVED in that target's rows against the
// declared gaps, which is the only check that sees an authored target's real row
// body. Cross-package feeding is out of scope for the same static reason: the
// prefix scan runs within one Definition.
//
// A SELF-TEST RUNS ON EVERY INVOCATION. A sweep that asserts a property of an
// empty set prints a clean line and reds nothing — so main runs runSelfTest
// first (synthetic Definitions through this file's own checkPackage; verbose with
// --selftest, silent-unless-failing otherwise, exit 2 on a mismatch because a
// misbehaving gate is a different failure from a corpus violation), and the
// corpus run refuses its all-clear if it examined zero gap columns. Both counts
// are printed so a future regression in the extraction is visible rather than
// green.
//
// STRICT=1 exits non-zero on any finding; unset, it reports and exits 0.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

const (
	// gapColumnPrefix is Contract #10 §10.2's gap-column naming convention: a
	// gap column, a §10.8 gaps key and a mark key segment are all
	// `missing_<gap>`. Restated here rather than imported — internal/weaver's
	// own constant (state.go) is unexported, and this gate needs no other part
	// of the engine.
	gapColumnPrefix = "missing_"

	// escalateUnplannable is the Contract #10 §10.8 Augur trigger for "a
	// missing_* column with no gaps[col] entry". A target escalating it routes
	// the undeclared column to the reasoning tier instead of the long-Nak arm.
	// Restated for the same reason as gapColumnPrefix
	// (internal/weaver/registry.go's constant is unexported).
	escalateUnplannable = "unplannable"

	// natsKVAdapter is the LensSpec.Adapter value that selects the NATS-KV
	// projection output — the adapter a weaver-targets lens uses.
	natsKVAdapter = "nats-kv"

	// actorAggregateKind is the LensSpec.ProjectionKind value that opts a lens
	// into the declarative actor-aggregate projection plan, which is the only
	// path that reads the Output descriptor
	// (internal/refractor/projection/plan.go's IsActorAggregate).
	actorAggregateKind = "actorAggregate"
)

// stats accumulates what a run actually examined, so a clean verdict can be
// audited instead of taken on faith.
type stats struct {
	packagesWithTargets int
	targets             int
	exemptTargets       int
	lensesRead          int
	columnsChecked      int
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	verboseSelfTest := false
	for _, a := range os.Args[1:] {
		if a == "--selftest" {
			verboseSelfTest = true
		}
	}
	runSelfTest(verboseSelfTest)

	var findings []string
	var st stats

	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: pkgregistry.Names() lists this package but Lookup does not resolve it — the corpus enumeration and the registry disagree, so this run cannot claim to have checked it", name))
			continue
		}
		if len(def.WeaverTargets) == 0 {
			continue
		}
		st.packagesWithTargets++
		f := checkPackage(name, def, &st)
		findings = append(findings, f...)
	}

	// A sweep that examined nothing must not report an all-clear: a broken
	// prefix derivation or a broken descriptor read would otherwise print the
	// same clean line a healthy corpus does.
	if st.columnsChecked == 0 {
		findings = append(findings, fmt.Sprintf("lint-gap-column-declaration: examined %d target(s) but ZERO gap columns — the extraction is broken (no weaver-targets lens resolved, no Output descriptor read, or no missing_* column found), and a gate that checked nothing has no all-clear to give", st.targets))
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-gap-column-declaration: clean — %d target(s) across %d package(s); %d weaver-targets lens(es) read, %d gap column(s) checked against their target's gaps map (%d target(s) exempt via augur.escalate %q)\n",
			st.targets, st.packagesWithTargets, st.lensesRead, st.columnsChecked, st.exemptTargets, escalateUnplannable)
		return
	}
	fmt.Printf("lint-gap-column-declaration: %d issue(s) — %d target(s) across %d package(s), %d weaver-targets lens(es) read, %d gap column(s) checked\n",
		len(findings), st.targets, st.packagesWithTargets, st.lensesRead, st.columnsChecked)
	if strict {
		os.Exit(1)
	}
}

// rowSource is one weaver-targets lens as this gate can see it: the row prefix
// its keys carry, and the gap columns its Output descriptor puts into the row
// body. `readable` is false when the descriptor cannot be trusted to describe
// the row at all, in which case `why` states which structural reason applies.
type rowSource struct {
	name     string
	prefix   string
	gapCols  map[string]string
	readable bool
	why      string
}

// checkPackage returns every finding for one package Definition and folds what
// it examined into st. It is the single entry point the corpus walk and the
// self-test both use, so a vector proven here is proven for the real run.
func checkPackage(pkg string, def pkgmgr.Definition, st *stats) []string {
	var findings []string

	var sources []rowSource
	for _, l := range def.Lenses {
		if !projectsIntoWeaverTargets(l) {
			continue
		}
		src := classify(l)
		sources = append(sources, src)
		if src.readable {
			st.lensesRead++
			continue
		}
		findings = append(findings, fmt.Sprintf("%s: lens %q projects into the %s bucket but this gate cannot read the columns its rows carry: %s. Its rows land under a `<targetId>.` prefix like any other, so whichever target owns that prefix has its gap columns UNVERIFIED here — no annotation makes this readable, the remedy is structural (an actorAggregate lens with an Output descriptor whose OutputKeyPattern is `<targetId>.<...>`), and until then Weaver's own dispatch is the first thing that sees an undeclared column.",
			pkg, src.name, bootstrap.WeaverTargetsBucket, src.why))
	}

	for _, t := range def.WeaverTargets {
		st.targets++
		findings = append(findings, checkTarget(pkg, def, t, sources, st)...)
	}
	return findings
}

// checkTarget returns the findings for one target: the LensRef bindings this
// gate cannot resolve, a LensRef'd lens whose key prefix disagrees with the
// TargetID, and every gap column landing in the target's rows that its Gaps map
// does not name.
func checkTarget(pkg string, def pkgmgr.Definition, t pkgmgr.WeaverTargetSpec, sources []rowSource, st *stats) []string {
	where := fmt.Sprintf("%s: target %s", pkg, t.TargetID)
	var findings []string

	exempt := escalatesUnplannable(t)
	if exempt {
		st.exemptTargets++
	}

	// The LensRef checks run for EVERY target, exempt or not: the unplannable
	// escalation covers an undeclared COLUMN, and says nothing about a binding
	// that names no lens. Suppressing these here would let the exemption
	// silently widen into "this target is not checked at all".
	switch {
	case strings.TrimSpace(t.LensRef) == "":
		findings = append(findings, fmt.Sprintf("%s: declares no LensRef — the target names no violation lens, so nothing states which lens is meant to feed its rows. Name the lens's CanonicalName.", where))
	default:
		lens, found := lookupLens(def, t.LensRef)
		switch {
		case !found:
			findings = append(findings, fmt.Sprintf("%s: LensRef %q resolves to no lens in package %s. A bare NanoID naming an already-installed lens is admitted by the installer (build.go's resolveLensRef) but is not statically resolvable, so this gate cannot confirm which lens feeds this target — declare the lens in this package, or accept that the projected-⊆-declared invariant goes unverified for it.", where, t.LensRef, pkg))
		case !projectsIntoWeaverTargets(lens):
			findings = append(findings, fmt.Sprintf("%s: LensRef %q names a lens that does not project into the %s bucket (adapter %q, bucket %q). A weaver target dispatches over rows in that bucket, so this binding names a lens whose rows the target never sees.", where, t.LensRef, bootstrap.WeaverTargetsBucket, lens.Adapter, lens.Bucket))
		default:
			if src := classify(lens); src.readable && src.prefix != t.TargetID {
				findings = append(findings, fmt.Sprintf("%s: LensRef %q writes its rows under the key prefix %q (OutputKeyPattern %q), not under %q. A targetId IS the row prefix the target dispatches over, so this lens feeds a different prefix than the one the target reads — either the pattern or the targetId is wrong.",
					where, t.LensRef, src.prefix, lens.Output.OutputKeyPattern, t.TargetID))
			}
		}
	}

	// The row set is every key under `<targetId>.`, so the columns to check come
	// from EVERY readable lens writing that prefix — not only the LensRef'd one.
	var feeders []rowSource
	for _, s := range sources {
		if s.readable && s.prefix == t.TargetID {
			feeders = append(feeders, s)
		}
	}
	if len(feeders) == 0 {
		findings = append(findings, fmt.Sprintf("%s: no readable lens in package %s writes rows under the prefix %q, so this target's gap columns were not checked against anything. A target dispatches over `%s.<entityId>` keys in the %s bucket; declare the lens that produces them (or see this package's unreadable-lens finding above for why its columns could not be read).",
			where, pkg, t.TargetID+".", t.TargetID, bootstrap.WeaverTargetsBucket))
		return findings
	}

	for _, col := range sortedGapColumns(feeders) {
		st.columnsChecked++
		if _, declared := t.Gaps[col.name]; declared {
			continue
		}
		// The exemption suppresses THIS finding only — an undeclared column on
		// a target that escalates it is dispatched, not stranded.
		if exempt {
			continue
		}
		findings = append(findings, fmt.Sprintf("%s: lens %q projects gap column %q (in %s), which the target's gaps map does not declare (declared: %s). Weaver's dispatchGap holds such a row on the long redelivery floor indefinitely. Add a gaps entry naming the remediation action — or, if the column is deliberately not remediated, declare it `surface` so it raises a standing Health issue and Acks.",
			where, col.lens, col.name, col.field, declaredKeys(t.Gaps)))
	}
	return findings
}

// gapColumn is one `missing_*` column attributed to the lens and descriptor
// field that put it in the row body, so a finding can point the author at the
// exact list to edit.
type gapColumn struct {
	name  string
	lens  string
	field string
}

// sortedGapColumns unions the gap columns of every feeding lens, deduplicated by
// column name (two lenses writing one prefix may legitimately project the same
// column) and sorted so the output is stable.
func sortedGapColumns(feeders []rowSource) []gapColumn {
	seen := map[string]gapColumn{}
	for _, s := range feeders {
		for col, field := range s.gapCols {
			if _, dup := seen[col]; dup {
				continue
			}
			seen[col] = gapColumn{name: col, lens: s.name, field: field}
		}
	}
	out := make([]gapColumn, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// projectsIntoWeaverTargets reports whether a lens writes into the shared
// weaver-targets bucket — the positional test the engine itself uses
// (cmd/refractor/main.go keys its convergence guard off the same adapter/bucket
// pair), never off a canonical-name list.
func projectsIntoWeaverTargets(l pkgmgr.LensSpec) bool {
	return l.Adapter == natsKVAdapter && l.Bucket == bootstrap.WeaverTargetsBucket
}

// classify resolves what this gate can read from one weaver-targets lens: its
// row-key prefix and its gap columns, or the structural reason neither can be
// trusted.
func classify(l pkgmgr.LensSpec) rowSource {
	src := rowSource{name: l.CanonicalName}
	switch {
	case l.ProjectionKind != actorAggregateKind:
		src.why = fmt.Sprintf("its projectionKind is %q, not %q, so Refractor never compiles an Output descriptor for it (projection/plan.go's Compile refuses a non-actorAggregate lens) — on the plain path the cypher's own RETURN aliases become the row body, and any Output it carries is inert", l.ProjectionKind, actorAggregateKind)
		return src
	case l.Output == nil:
		src.why = "it declares no Output descriptor, so there is no BodyColumns/StaticEmptyColumns list stating what its rows carry"
		return src
	}
	prefix, ok := keyPrefix(l.Output.OutputKeyPattern)
	if !ok {
		src.why = fmt.Sprintf("its OutputKeyPattern %q yields no `<prefix>.` segment, so the target whose rows it writes cannot be determined (a weaver-targets key is `<targetId>.<entityId>`)", l.Output.OutputKeyPattern)
		return src
	}
	src.prefix = prefix
	src.gapCols = gapColumnsOf(l.Output)
	src.readable = true
	return src
}

// gapColumnsOf returns the `missing_*` columns an actor-aggregate lens's Output
// descriptor puts into the projected row body, mapped to the descriptor field
// that declares each. It mirrors pkgmgr's declaredRowBodyColumns
// (orchestrationguard.go), including its attribution of a name appearing in both
// lists to BodyColumns — the one carrying a real value. The union is what
// matters: Refractor's driver writes every BodyColumn into the envelope and then
// writes each StaticEmptyColumn as an empty array, so both are keys Weaver sees.
func gapColumnsOf(out *pkgmgr.OutputDescriptorSpec) map[string]string {
	cols := make(map[string]string, len(out.BodyColumns)+len(out.StaticEmptyColumns))
	for _, c := range out.StaticEmptyColumns {
		if strings.HasPrefix(c, gapColumnPrefix) {
			cols[c] = "Output.StaticEmptyColumns"
		}
	}
	for _, c := range out.BodyColumns {
		if strings.HasPrefix(c, gapColumnPrefix) {
			cols[c] = "Output.BodyColumns"
		}
	}
	return cols
}

// keyPrefix returns the segment of an OutputKeyPattern before its first dot —
// the `<targetId>` half of a §10.2 `<targetId>.<entityId>` weaver-targets key.
// A pattern with no dot names no entity segment and yields no prefix.
func keyPrefix(pattern string) (string, bool) {
	i := strings.Index(pattern, ".")
	if i <= 0 {
		return "", false
	}
	return pattern[:i], true
}

// escalatesUnplannable reports whether the target's Augur policy redirects an
// undeclared gap column to the AI reasoning tier — dispatchGap routes it there
// and never reaches the long-Nak arm, so such a column needs no Gaps entry.
// Nil-safe: the field is default-absent, and most targets declare no policy.
func escalatesUnplannable(t pkgmgr.WeaverTargetSpec) bool {
	if t.Augur == nil {
		return false
	}
	for _, trigger := range t.Augur.Escalate {
		if trigger == escalateUnplannable {
			return true
		}
	}
	return false
}

// lookupLens resolves a target's LensRef against the lenses declared in the SAME
// Definition, by CanonicalName — the form a package author writes. It takes the
// LAST match, mirroring pkgmgr's lensByCanonicalName and the canonicalName→id
// map build.go's batch build overwrites per duplicate, so this gate reads the
// lens the installer would actually bind.
func lookupLens(def pkgmgr.Definition, ref string) (pkgmgr.LensSpec, bool) {
	var found pkgmgr.LensSpec
	ok := false
	for _, l := range def.Lenses {
		if l.CanonicalName == ref {
			found, ok = l, true
		}
	}
	return found, ok
}

// declaredKeys renders a target's declared gap keys in sorted order, so the
// finding names what the author DID declare beside what they did not.
func declaredKeys(gaps map[string]pkgmgr.GapActionSpec) string {
	if len(gaps) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(gaps))
	for k := range gaps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// runSelfTest drives synthetic Definitions through checkPackage — the same entry
// point the corpus walk uses — so every rule this file documents has a proven
// positive vector and a paired negative one. This file carries `//go:build
// ignore`, which keeps it out of `go test`'s package builds, so this doubles as
// the colocated test: `go run ./scripts/lint-gap-column-declaration.go
// --selftest`. It also runs unconditionally from main, where verbose is false:
// only failures print, and any mismatch exits 2 — a gate that does not behave as
// documented is a different failure from a corpus violation, and the count it
// prints in the clean line is worthless if the extraction beneath it is broken.
func runSelfTest(verbose bool) {
	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-gap-column-declaration selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}

	wtLens := func(name, pattern string, body, staticEmpty []string) pkgmgr.LensSpec {
		return pkgmgr.LensSpec{
			CanonicalName:  name,
			Adapter:        natsKVAdapter,
			Bucket:         bootstrap.WeaverTargetsBucket,
			ProjectionKind: actorAggregateKind,
			Output: &pkgmgr.OutputDescriptorSpec{
				OutputKeyPattern:   pattern,
				BodyColumns:        body,
				StaticEmptyColumns: staticEmpty,
			},
		}
	}
	target := func(id, lensRef string, gaps []string) pkgmgr.WeaverTargetSpec {
		t := pkgmgr.WeaverTargetSpec{TargetID: id, LensRef: lensRef, Gaps: map[string]pkgmgr.GapActionSpec{}}
		for _, g := range gaps {
			t.Gaps[g] = pkgmgr.GapActionSpec{Action: "surface"}
		}
		return t
	}
	run := func(def pkgmgr.Definition) ([]string, stats) {
		var st stats
		return checkPackage("selftestpkg", def, &st), st
	}
	joined := func(f []string) string { return strings.Join(f, "\n") }

	// Vector 1 — the happy path: a declared column, plus two lenses that do NOT
	// write the weaver-targets bucket and are therefore out of scope even though
	// they project `missing_*` columns under the target's own key pattern. The
	// protected Postgres read model is the shape lease-signing actually ships;
	// the other-bucket nats-kv lens is the same test on the adapter's other axis.
	f, st := run(pkgmgr.Definition{
		Lenses: []pkgmgr.LensSpec{
			wtLens("okLens", "okTarget.{actorSuffix}", []string{"violating", "missing_thing"}, nil),
			{CanonicalName: "readModel", Adapter: "postgres", Table: "rm", Protected: true, ProjectionKind: actorAggregateKind,
				Output: &pkgmgr.OutputDescriptorSpec{OutputKeyPattern: "okTarget.{actorSuffix}", BodyColumns: []string{"missing_notAGap"}}},
			{CanonicalName: "otherBucket", Adapter: natsKVAdapter, Bucket: "some-other-bucket", ProjectionKind: actorAggregateKind,
				Output: &pkgmgr.OutputDescriptorSpec{OutputKeyPattern: "okTarget.{actorSuffix}", BodyColumns: []string{"missing_alsoNotAGap"}}},
		},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("okTarget", "okLens", []string{"missing_thing"})},
	})
	check(len(f) == 0, fmt.Sprintf("a fully-declared target is clean and a non-weaver-targets lens's missing_* is out of scope (got %d finding(s): %s)", len(f), joined(f)))
	check(st.columnsChecked == 1 && st.lensesRead == 1, fmt.Sprintf("the happy path reports 1 lens read and 1 column checked (got %d/%d)", st.lensesRead, st.columnsChecked))

	// Vector 2 — a gap column declared ONLY in StaticEmptyColumns still lands in
	// the row body, so it must be declared.
	f, _ = run(pkgmgr.Definition{
		Lenses:        []pkgmgr.LensSpec{wtLens("secLens", "secTarget.{actorSuffix}", []string{"violating"}, []string{"missing_static"})},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("secTarget", "secLens", nil)},
	})
	check(len(f) == 1 && strings.Contains(joined(f), "missing_static") && strings.Contains(joined(f), "Output.StaticEmptyColumns"),
		fmt.Sprintf("a StaticEmptyColumns-only gap column is flagged and attributed to that list (got: %s)", joined(f)))

	// Vector 3 — a SECOND lens writing the target's key prefix, bound by no
	// target, feeds the same rows and is in scope.
	f, _ = run(pkgmgr.Definition{
		Lenses: []pkgmgr.LensSpec{
			wtLens("boundLens", "sharedTarget.{actorSuffix}", []string{"missing_declared"}, nil),
			wtLens("unboundLens", "sharedTarget.{actorSuffix}", []string{"missing_orphan"}, nil),
		},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("sharedTarget", "boundLens", []string{"missing_declared"})},
	})
	check(len(f) == 1 && strings.Contains(joined(f), "missing_orphan") && strings.Contains(joined(f), "unboundLens"),
		fmt.Sprintf("a second lens writing the target's key prefix is in scope even though no target names it (got: %s)", joined(f)))

	// Vector 4 — a weaver-targets lens whose prefix matches no target writes
	// rows no dispatchGap reads, so its gap columns are not a finding.
	f, _ = run(pkgmgr.Definition{
		Lenses: []pkgmgr.LensSpec{
			wtLens("realLens", "realTarget.{actorSuffix}", []string{"missing_declared"}, nil),
			wtLens("displayLens", "displayOnly.{actorSuffix}", []string{"missing_ignored"}, nil),
		},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("realTarget", "realLens", []string{"missing_declared"})},
	})
	check(len(f) == 0, fmt.Sprintf("a weaver-targets lens under a prefix no target claims is not flagged (got: %s)", joined(f)))

	// Vector 5 — the LensRef'd lens's key prefix must equal the TargetID; the
	// canonical NAME need not (augurDispatchPending → augurDispatch).
	f, _ = run(pkgmgr.Definition{
		Lenses:        []pkgmgr.LensSpec{wtLens("pendingLens", "dispatchTarget.{actorSuffix}", []string{"missing_declared"}, nil)},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("dispatchTarget", "pendingLens", []string{"missing_declared"})},
	})
	check(len(f) == 0, fmt.Sprintf("a LensRef whose canonicalName differs from the targetId is clean when the key prefix agrees (got: %s)", joined(f)))
	f, _ = run(pkgmgr.Definition{
		Lenses: []pkgmgr.LensSpec{
			wtLens("driftedLens", "otherPrefix.{actorSuffix}", []string{"missing_declared"}, nil),
			wtLens("feedLens", "driftTarget.{actorSuffix}", []string{"missing_declared"}, nil),
		},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("driftTarget", "driftedLens", []string{"missing_declared"})},
	})
	check(len(f) == 1 && strings.Contains(joined(f), "otherPrefix") && strings.Contains(joined(f), "not under \"driftTarget\""),
		fmt.Sprintf("a LensRef'd lens whose key prefix disagrees with the targetId is flagged (got: %s)", joined(f)))

	// Vector 6 — an unreadable weaver-targets lens is reported as unreadable,
	// in its own words, not as an undeclared column.
	for _, tc := range []struct {
		desc string
		lens pkgmgr.LensSpec
		want string
	}{
		{"a plain (non-actorAggregate) weaver-targets lens", pkgmgr.LensSpec{CanonicalName: "plainLens", Adapter: natsKVAdapter, Bucket: bootstrap.WeaverTargetsBucket,
			Output: &pkgmgr.OutputDescriptorSpec{OutputKeyPattern: "plainTarget.{actorSuffix}", BodyColumns: []string{"missing_thing"}}}, "projectionKind"},
		{"a weaver-targets lens with a nil Output", pkgmgr.LensSpec{CanonicalName: "nilOutLens", Adapter: natsKVAdapter, Bucket: bootstrap.WeaverTargetsBucket,
			ProjectionKind: actorAggregateKind}, "no Output descriptor"},
		{"a weaver-targets lens whose OutputKeyPattern has no prefix segment", wtLens("noDotLens", "plainTarget", []string{"missing_thing"}, nil), "no `<prefix>.` segment"},
	} {
		f, st = run(pkgmgr.Definition{
			Lenses:        []pkgmgr.LensSpec{tc.lens},
			WeaverTargets: []pkgmgr.WeaverTargetSpec{target("plainTarget", tc.lens.CanonicalName, nil)},
		})
		check(strings.Contains(joined(f), "cannot read the columns its rows carry") && strings.Contains(joined(f), tc.want),
			fmt.Sprintf("%s is reported as unreadable-by-this-gate, naming why (got: %s)", tc.desc, joined(f)))
		check(!strings.Contains(joined(f), "does not declare"),
			fmt.Sprintf("%s is NOT reported as an undeclared column (got: %s)", tc.desc, joined(f)))
		check(st.lensesRead == 0 && st.columnsChecked == 0,
			fmt.Sprintf("%s contributes nothing to the examined counts (got %d/%d)", tc.desc, st.lensesRead, st.columnsChecked))
	}

	// Vector 7 — a duplicate canonicalName resolves LAST-wins, as the
	// installer's canonicalName→id map does.
	f, _ = run(pkgmgr.Definition{
		Lenses: []pkgmgr.LensSpec{
			wtLens("dupLens", "dupTarget.{actorSuffix}", []string{"missing_declared"}, nil),
			wtLens("dupLens", "wrongPrefix.{actorSuffix}", []string{"missing_declared"}, nil),
		},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("dupTarget", "dupLens", []string{"missing_declared"})},
	})
	check(len(f) == 1 && strings.Contains(joined(f), "wrongPrefix"),
		fmt.Sprintf("a duplicated canonicalName resolves to the LAST declaration, as the installer binds it (got: %s)", joined(f)))

	// Vector 8 — the unplannable exemption suppresses the undeclared-column
	// finding and nothing else: an unresolvable LensRef on an exempt target is
	// still reported.
	exempt := target("exemptTarget", "exemptLens", nil)
	exempt.Augur = &pkgmgr.AugurSpec{Escalate: []string{escalateUnplannable}}
	f, _ = run(pkgmgr.Definition{
		Lenses:        []pkgmgr.LensSpec{wtLens("exemptLens", "exemptTarget.{actorSuffix}", []string{"missing_undeclared"}, nil)},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{exempt},
	})
	check(len(f) == 0, fmt.Sprintf("a target escalating unplannable is exempt from the undeclared-column finding (got: %s)", joined(f)))

	badRef := target("exemptTarget", "noSuchLens", nil)
	badRef.Augur = &pkgmgr.AugurSpec{Escalate: []string{escalateUnplannable}}
	f, _ = run(pkgmgr.Definition{
		Lenses:        []pkgmgr.LensSpec{wtLens("exemptLens", "exemptTarget.{actorSuffix}", []string{"missing_undeclared"}, nil)},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{badRef},
	})
	check(strings.Contains(joined(f), "resolves to no lens"),
		fmt.Sprintf("an unresolvable LensRef is still reported on an unplannable-exempt target (got: %s)", joined(f)))

	// Vector 9 — an empty LensRef, and a target no readable lens feeds, are each
	// reported rather than passed.
	f, _ = run(pkgmgr.Definition{
		Lenses:        []pkgmgr.LensSpec{wtLens("someLens", "someTarget.{actorSuffix}", []string{"missing_declared"}, nil)},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("someTarget", "", []string{"missing_declared"})},
	})
	check(len(f) == 1 && strings.Contains(joined(f), "declares no LensRef"),
		fmt.Sprintf("an empty LensRef is reported (got: %s)", joined(f)))
	f, _ = run(pkgmgr.Definition{
		Lenses:        []pkgmgr.LensSpec{wtLens("elsewhere", "elsewhere.{actorSuffix}", nil, nil)},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{target("starvedTarget", "elsewhere", nil)},
	})
	check(strings.Contains(joined(f), "no readable lens") && strings.Contains(joined(f), "starvedTarget"),
		fmt.Sprintf("a target no lens writes rows for is reported, not silently clean (got: %s)", joined(f)))

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-gap-column-declaration: self-test failure(s) — the gate does not behave as documented")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
