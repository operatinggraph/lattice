package pkgmgr

import (
	"os"
	"strings"
	"testing"
)

// personalLens is the shared shape a Personal-lens fixture starts from.
func personalLens(name string, walks []AnchorWalk, spec string) LensSpec {
	return LensSpec{
		CanonicalName: name,
		Class:         "meta.lens",
		Adapter:       "nats-subject",
		SubjectPrefix: "lattice.sync.user",
		Stream:        "SYNC",
		Personal:      true,
		Engine:        "full",
		IntoKey:       []string{"__actor", "ns", "entityId"},
		Walks:         walks,
		Spec:          spec,
	}
}

// oneWalk wraps a single AnchorWalk into the Walks slice shape.
func oneWalk(w AnchorWalk) []AnchorWalk { return []AnchorWalk{w} }

const testSharedPrefix = "(identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)"

// twoWalkDefinition declares two lenses, each a single walk, sharing one domain.
func twoWalkDefinition() Definition {
	return Definition{
		Name:             "fixture",
		Version:          "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "fx"}},
		Lenses: []LensSpec{
			personalLens("svcLens", oneWalk(AnchorWalk{
				GrantDomain: "fx", AnchorType: "service", AnchorVar: "tpl",
				Chain: []string{testSharedPrefix, "(container)<-[:availableAt]-(tpl:service)"},
			}), "\nWITH tpl\nWHERE tpl.key <> null\nRETURN\n  tpl.key AS anchor,\n  \"m.svc\" AS ns\n"),
			personalLens("provLens", oneWalk(AnchorWalk{
				GrantDomain: "fx", AnchorType: "provider", AnchorVar: "prov",
				Chain: []string{testSharedPrefix, "(container)<-[:practicesAt]-(prov:provider)"},
			}), "\nWITH prov\nWHERE prov.key <> null\nRETURN\n  prov.key AS anchor,\n  \"m.ent\" AS ns\n"),
		},
	}
}

func TestExpandReadGrantWalks_ComposesTheDataLensPrefix(t *testing.T) {
	exp, err := twoWalkDefinition().ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	want := `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
WITH tpl
WHERE tpl.key <> null
RETURN
  tpl.key AS anchor,
  "m.svc" AS ns
`
	if got := exp.Lenses[0].Spec; got != want {
		t.Errorf("composed data spec mismatch\n got:%s\nwant:%s", got, want)
	}
}

func TestExpandReadGrantWalks_GeneratesOneProducerPerDomain(t *testing.T) {
	exp, err := twoWalkDefinition().ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(exp.Lenses) != 3 {
		t.Fatalf("expected 2 data lenses + 1 generated producer, got %d", len(exp.Lenses))
	}
	p := exp.Lenses[2]
	if p.CanonicalName != "fxReadGrants" {
		t.Errorf("producer canonicalName = %q, want fxReadGrants", p.CanonicalName)
	}
	if p.Adapter != "nats-kv" || p.Bucket != "capability-kv" || p.ProjectionKind != "actorAggregate" {
		t.Errorf("producer shape = %q/%q/%q", p.Adapter, p.Bucket, p.ProjectionKind)
	}
	if p.Output == nil {
		t.Fatal("producer has no Output descriptor")
	}
	if p.Output.OutputKeyPattern != "cap-read.fx.{actorSuffix}" {
		t.Errorf("OutputKeyPattern = %q", p.Output.OutputKeyPattern)
	}
	if p.Output.RealnessFilter != "anchorId" || p.Output.EmptyBehavior != "delete" {
		t.Errorf("without realnessFilter=anchorId + emptyBehavior=delete the driver never deletes an emptied slice: got %q/%q",
			p.Output.RealnessFilter, p.Output.EmptyBehavior)
	}
	if p.Output.EntryKeyColumn != "anchorId" {
		t.Errorf("EntryKeyColumn = %q, want anchorId — every generated producer must opt into per-anchor "+
			"key emission (Fire 2)", p.Output.EntryKeyColumn)
	}

	// Each walk gets its own STAGE: the walk's own chain clauses (the
	// residence prefix is re-walked per stage rather than factored — staging
	// shares nothing textually between walks), then a `WITH` that folds them
	// into that walk's grant slice before the next walk's clauses run. The
	// final RETURN concatenates every slice.
	//
	// The expected text lives in twoWalkProducerGoldenPath rather than inline
	// because a second test reads the SAME file: Refractor's
	// TestAnchorHopIndex_GeneratedProducerIndexesToTheUnstagedGraph indexes
	// this emission and proves the derivation can act on it. Sharing the file
	// is what makes that a link rather than a copy — an emission change reds
	// here, and the moment the file is updated the index test re-reads the new
	// text and reds too if the new shape stops indexing.
	want := readTwoWalkProducerGolden(t)
	if p.Spec != want {
		t.Errorf("generated producer mismatch\n got:%s\nwant:%s", p.Spec, want)
	}
}

// twoWalkProducerGoldenPath holds the generated two-walk producer emission, so
// tests in two packages assert against one text. It is relative to this
// package's directory; the Refractor side spells the path out from its own
// (hopindex_test.go's generatedTwoWalkProducerGolden) — a test file's
// identifiers do not leave their package, so the FILE is the shared thing here,
// not this constant.
const twoWalkProducerGoldenPath = "testdata/generated_two_walk_producer.cypher"

func readTwoWalkProducerGolden(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(twoWalkProducerGoldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", twoWalkProducerGoldenPath, err)
	}
	return string(b)
}

// TestExpandReadGrantWalks_CollidingWalkVariablesAreStagedApart is the guard
// that keeps two walks' unrelated variables from silently joining in the
// producer. Two lenses in one domain both bind `x` — one to a task, the other
// to a booking. A single flat query concatenating both walks' OPTIONAL MATCHes
// into one row stream would treat the second `(x:booking)` as a join
// constraint on whatever `x` the first clause already bound, silently turning
// "task OR booking" into "the one vertex both walks happen to agree on" (here,
// none — a task is never a booking). Staging closes that off structurally: each
// walk's `x` dies at its own `WITH`, so the emitted cypher must read as walk 0's
// clauses + its own `WITH … AS grantSlice0`, THEN walk 1's clauses (a fresh `x`,
// unconstrained by walk 0's) + its own `WITH … AS grantSlice1` — never one
// flat run where both `(x:task)` and `(x:booking)` MATCH under a single shared
// scope.
func TestExpandReadGrantWalks_CollidingWalkVariablesAreStagedApart(t *testing.T) {
	def := Definition{
		Name: "fixture", Version: "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "fx"}},
		Lenses: []LensSpec{
			personalLens("a", oneWalk(AnchorWalk{
				GrantDomain: "fx", AnchorType: "task", AnchorVar: "x",
				Chain: []string{"(identity)<-[:assignedTo]-(x:task)"},
			}), "\nRETURN x.key AS anchor\n"),
			personalLens("b", oneWalk(AnchorWalk{
				GrantDomain: "fx", AnchorType: "booking", AnchorVar: "x",
				Chain: []string{"(identity)<-[:bookedBy]-(x:booking)"},
			}), "\nRETURN x.key AS anchor\n"),
		},
	}
	exp, err := def.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	spec := exp.Lenses[2].Spec

	stage0 := "OPTIONAL MATCH (identity)<-[:assignedTo]-(x:task)\n" +
		"WITH identity,\n" +
		"  collect(DISTINCT {anchorType: 'task', anchorId: nanoIdFromKey(x.key), via: ['assignedTo']}) AS grantSlice0\n"
	stage1 := "OPTIONAL MATCH (identity)<-[:bookedBy]-(x:booking)\n" +
		"WITH identity, grantSlice0,\n" +
		"  collect(DISTINCT {anchorType: 'booking', anchorId: nanoIdFromKey(x.key), via: ['bookedBy']}) AS grantSlice1\n"

	i0 := strings.Index(spec, stage0)
	if i0 < 0 {
		t.Fatalf("walk 0's clause + its own WITH…AS grantSlice0 (reading `x:task` inside its own stage) not found verbatim:\n%s", spec)
	}
	i1 := strings.Index(spec, stage1)
	if i1 < 0 {
		t.Fatalf("walk 1's clause + its own WITH…AS grantSlice1 (reading `x:booking` inside its own stage) not found verbatim:\n%s", spec)
	}
	if i1 < i0+len(stage0) {
		t.Errorf("walk 1's stage must come entirely AFTER walk 0's own WITH closes it — got them overlapping/out of order:\n%s", spec)
	}
	if !strings.Contains(spec, "grantSlice0 + grantSlice1 AS readableAnchors") {
		t.Errorf("final RETURN must concatenate both walks' staged slices:%s", spec)
	}

	// The DATA lenses keep their own variable names — staging is
	// producer-local, so each hand-authored tail still resolves.
	if !strings.Contains(exp.Lenses[1].Spec, "(x:booking)") {
		t.Errorf("data lens variables must be untouched:%s", exp.Lenses[1].Spec)
	}
}

// TestExpandReadGrantWalks_RejectsAWalkVariableNamedLikeAnAccumulator pins
// validateGrantSliceVarNames: a walk that binds a variable literally named
// `grantSlice0` would have its own pattern silently JOINED against the
// generated accumulator by the staging `WITH` (the accumulator is already in
// scope from an earlier stage) instead of binding fresh — the same class of
// silent-join hazard the staged-apart guard above closes for cross-walk
// collisions, but against the producer's OWN reserved names.
func TestExpandReadGrantWalks_RejectsAWalkVariableNamedLikeAnAccumulator(t *testing.T) {
	def := Definition{
		Name: "fixture", Version: "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "fx"}},
		Lenses: []LensSpec{
			personalLens("a", oneWalk(AnchorWalk{
				GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)<-[:assignedTo]-(t:task)"},
			}), "\nRETURN t.key AS anchor\n"),
			personalLens("b", oneWalk(AnchorWalk{
				GrantDomain: "fx", AnchorType: "booking", AnchorVar: "grantSlice0",
				Chain: []string{"(identity)<-[:bookedBy]-(grantSlice0:booking)"},
			}), "\nRETURN grantSlice0.key AS anchor\n"),
		},
	}
	_, err := def.ExpandReadGrantWalks()
	if err == nil {
		t.Fatal("expected rejection — a walk variable named like a generated grant-slice accumulator")
	}
	if !strings.Contains(err.Error(), "accumulator") {
		t.Errorf("error %q does not name the accumulator collision", err.Error())
	}
}

// --- Fire U1: multiple Walks declared on ONE lens -------------------------

// TestExpandReadGrantWalks_MultiWalkLensComposesIndependentBranches proves
// the §13.2 composition primitive: two Walks entries on ONE lens, reaching
// the SAME anchor kind via independent paths, compose into N independent
// queries — one per walk, each the walk's own OPTIONAL MATCH chain plus the
// shared tail, unrenamed — rather than one concatenated query. The anchor is
// reachable via EITHER path because Refractor evaluates and merges the
// branches, not because a single query's coalesce folds them.
func TestExpandReadGrantWalks_MultiWalkLensComposesIndependentBranches(t *testing.T) {
	def := Definition{
		Name:             "fixture",
		Version:          "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "base"}, {Name: "staff"}},
		Lenses: []LensSpec{
			{
				CanonicalName: "multi",
				Class:         "meta.lens",
				Adapter:       "nats-subject",
				SubjectPrefix: "lattice.sync.user",
				Stream:        "SYNC",
				Personal:      true,
				Engine:        "full",
				IntoKey:       []string{"__actor", "ns", "entityId"},
				Walks: []AnchorWalk{
					{GrantDomain: "base", AnchorType: "task", AnchorVar: "t",
						Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}},
					{GrantDomain: "staff", AnchorType: "task", AnchorVar: "t",
						Chain: []string{"(identity)-[:worksAt]->(w)", "(w)<-[:queuedFor]-(t:task)"}},
				},
				Spec: "\nRETURN t.key AS anchor\n",
			},
		},
	}
	expanded, err := def.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("ExpandReadGrantWalks: %v", err)
	}
	if got := expanded.Lenses[0].Spec; got != "" {
		t.Errorf("Spec must be empty for a multi-walk lens, got %q", got)
	}
	got := expanded.Lenses[0].SpecBranches
	want := []string{
		"\n" + anchorWalkHead + "\n" +
			"OPTIONAL MATCH (identity)<-[:assignedTo]-(t:task)\n" +
			"RETURN t.key AS anchor\n",
		"\n" + anchorWalkHead + "\n" +
			"OPTIONAL MATCH (identity)-[:worksAt]->(w)\n" +
			"OPTIONAL MATCH (w)<-[:queuedFor]-(t:task)\n" +
			"RETURN t.key AS anchor\n",
	}
	if len(got) != len(want) {
		t.Fatalf("branch count mismatch: got %d, want %d\ngot: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("branch %d mismatch:\ngot:\n%s\nwant:\n%s", i, got[i], want[i])
		}
	}
}

// A tail column that combines a variable owned by one walk with a variable
// owned by another (e.g. "g.name + w.name AS mixed") is NOT rejected here —
// pkgmgr only composes the N branch strings; Refractor's translateSpec
// classifies each RETURN column via full.ClassifyBranchReturnColumns and
// refuses the lens at compile time (internal/refractor/lens's
// branchspec_translate_test.go covers that refusal; pkgmgr cannot import the
// full engine's package in production code without an import cycle through
// its own test corpus — see composeDataLensSpec's doc comment).

// TestExpandReadGrantWalks_RejectsMismatchedAnchorAcrossWalks pins §3.1's
// "every walk in one lens must share the lens's anchor type/var" rule —
// independent of the composition question above, this rule stands alone: a
// lens has exactly one hand-authored tail, so it can RETURN exactly one
// anchor variable.
func TestExpandReadGrantWalks_RejectsMismatchedAnchorAcrossWalks(t *testing.T) {
	def := Definition{
		Name:             "fixture",
		Version:          "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "base"}, {Name: "staff"}},
		Lenses: []LensSpec{
			{
				CanonicalName: "multi",
				Class:         "meta.lens",
				Adapter:       "nats-subject",
				SubjectPrefix: "lattice.sync.user",
				Stream:        "SYNC",
				Personal:      true,
				Engine:        "full",
				IntoKey:       []string{"__actor", "ns", "entityId"},
				Walks: []AnchorWalk{
					{GrantDomain: "base", AnchorType: "task", AnchorVar: "t",
						Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}},
					{GrantDomain: "staff", AnchorType: "booking", AnchorVar: "bk",
						Chain: []string{"(identity)<-[:bookedBy]-(bk:booking)"}},
				},
				Spec: "\nRETURN t.key AS anchor\n",
			},
		},
	}
	_, err := def.ExpandReadGrantWalks()
	if err == nil {
		t.Fatal("expected rejection — two walks in one lens resolving to different anchors")
	}
	if !strings.Contains(err.Error(), "every walk in one lens must resolve to the same anchor") {
		t.Errorf("error %q does not name the anchor-consistency rule", err.Error())
	}
}

// TestExpandReadGrantWalks_SelfAnchoredLensIsUntouched pins that the one lens
// kind exempt from the walk rule compiles byte-for-byte as declared.
func TestExpandReadGrantWalks_SelfAnchoredLensIsUntouched(t *testing.T) {
	selfSpec := "\nMATCH (identity:identity {key: $actorKey})\nRETURN identity.key AS anchor, \"m.me\" AS ns\n"
	def := Definition{
		Name: "fixture", Version: "1.0.0",
		Lenses: []LensSpec{personalLens("me", nil, selfSpec)},
	}
	exp, err := def.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("a self-anchored Personal lens needs no Walk: %v", err)
	}
	if len(exp.Lenses) != 1 || exp.Lenses[0].Spec != selfSpec {
		t.Errorf("self-anchored lens was rewritten:%s", exp.Lenses[0].Spec)
	}
}

func TestExpandReadGrantWalks_IsIdempotent(t *testing.T) {
	once, err := twoWalkDefinition().ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	twice, err := once.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("re-expand: %v", err)
	}
	if len(twice.Lenses) != len(once.Lenses) {
		t.Errorf("re-expansion changed the lens count: %d then %d", len(once.Lenses), len(twice.Lenses))
	}
	for i := range once.Lenses {
		if once.Lenses[i].Spec != twice.Lenses[i].Spec {
			t.Errorf("re-expansion rewrote %s", once.Lenses[i].CanonicalName)
		}
	}
}

// --- validation failures ----------------------------------------------------

func TestExpandReadGrantWalks_Rejects(t *testing.T) {
	base := func(l LensSpec, domains ...ReadGrantDomainSpec) Definition {
		if domains == nil {
			domains = []ReadGrantDomainSpec{{Name: "fx"}}
		}
		return Definition{Name: "fixture", Version: "1.0.0", ReadGrantDomains: domains, Lenses: []LensSpec{l}}
	}
	okWalk := func() []AnchorWalk {
		return oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
			Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}})
	}

	cases := []struct {
		name string
		def  Definition
		want string
	}{
		{
			"non-self Personal lens with no Walk",
			base(personalLens("x", nil, "\nMATCH (identity:identity {key: $actorKey})\nOPTIONAL MATCH (identity)<-[:assignedTo]-(t:task)\nRETURN t.key AS anchor\n")),
			"declares no Walks",
		},
		{
			"Personal lens with no anchor alias",
			base(personalLens("x", nil, "\nMATCH (identity:identity {key: $actorKey})\nRETURN identity.key AS k\n")),
			"has no `<var>.key AS anchor`",
		},
		{
			"undeclared grant domain",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "nope", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}}), "\nRETURN t.key AS anchor\n")),
			"not a declared ReadGrantDomain",
		},
		{
			"declared domain no walk names",
			base(personalLens("me", nil, "\nMATCH (identity:identity {key: $actorKey})\nRETURN identity.key AS anchor\n")),
			"no lens Walk names it",
		},
		{
			"Walk on a non-Personal lens",
			base(LensSpec{CanonicalName: "kv", Adapter: "nats-kv", Bucket: "b", Engine: "full",
				Walks: okWalk(), Spec: "\nRETURN t.key AS anchor\n"}),
			"is not a Personal (nats-subject) lens",
		},
		{
			"multi-pattern (comma) chain clause",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)-[:worksAt]->(w), (t:task)"}}), "\nRETURN t.key AS anchor\n")),
			"SINGLE linear relationship pattern",
		},
		{
			"chain clause disconnected from the actor",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(q:queue)<-[:queuedFor]-(t:task)"}}), "\nRETURN t.key AS anchor\n")),
			"binds no variable reachable from the actor",
		},
		{
			"chain clause with no relationship",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)"}}), "\nRETURN t.key AS anchor\n")),
			"at least one relationship",
		},
		{
			"anchor var not bound by the chain",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "zzz",
				Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}}), "\nRETURN zzz.key AS anchor\n")),
			"is not bound by Chain",
		},
		{
			"anchor label disagrees with AnchorType",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "workorder", AnchorVar: "t",
				Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}}), "\nRETURN t.key AS anchor\n")),
			"must agree",
		},
		{
			"tail does not return the declared anchor",
			base(personalLens("x", okWalk(), "\nRETURN identity.key AS anchor\n")),
			"must `RETURN t.key AS anchor`",
		},
		{
			"tail aliases something to the anchor var",
			base(personalLens("x", okWalk(), "\nOPTIONAL MATCH (t)-[:scopedTo]->(g)\nRETURN t.key AS anchor, g.key AS t\n")),
			"rebinding the Walk's anchor variable",
		},
		{
			"tail rebinds the anchor var with a fresh labelled pattern",
			base(personalLens("x", okWalk(), "\nOPTIONAL MATCH (t:task)-[:scopedTo]->(g)\nRETURN t.key AS anchor\n")),
			"rebinds",
		},
		{
			"empty chain",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t"}),
				"\nRETURN t.key AS anchor\n")),
			"chain is empty",
		},
		{
			// The anchor-alias check must be identifier-bounded on the LEFT:
			// `art.key AS anchor` contains `t.key AS anchor` as a substring, and
			// reading it as the declared anchor lets a tail re-aim the anchor at a
			// vertex the compiled producer never grants — 100% of rows dropped.
			"tail re-aims the anchor via a variable whose name ends in the anchor var",
			base(personalLens("x", okWalk(),
				"\nOPTIONAL MATCH (art:workorder)\nWITH art\nWHERE art.key <> null\nRETURN art.key AS anchor, \"m.x\" AS ns\n")),
			"must `RETURN t.key AS anchor`",
		},
		{
			// And on the RIGHT: `t.key AS anchorId` must not satisfy it.
			"tail aliases the anchor var to a decoy column, never to anchor",
			base(personalLens("x", okWalk(), "\nRETURN t.key AS anchorId\n")),
			"must `RETURN t.key AS anchor`",
		},
		{
			// A chain hop with no relation type reaches every neighbour over any
			// relation — the walk would grant far more than it names.
			"untyped chain hop",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)-[r*0..3]->(t:task)"}}), "\nRETURN t.key AS anchor\n")),
			"must name its relation type",
		},
		{
			// An alternation reaches two relations but `via` reports one.
			"alternation chain hop",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)<-[:assignedTo|:queuedFor]-(t:task)"}}), "\nRETURN t.key AS anchor\n")),
			"exactly ONE relation type",
		},
		{
			"bidirectional chain hop",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)<-[:assignedTo]->(t:task)"}}), "\nRETURN t.key AS anchor\n")),
			"one direction, not both",
		},
		{
			// A dotted domain becomes TWO key tokens, and readers enumerate
			// cap-read slices with a single-token wildcard — the slice would never
			// be found and every row of its lenses silently dropped.
			"grant domain name that is not a single key token",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "fx.sub", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}}), "\nRETURN t.key AS anchor\n"),
				ReadGrantDomainSpec{Name: "fx.sub"}),
			"must be a single key token",
		},
		{
			"duplicate grant domain",
			base(personalLens("x", okWalk(), "\nRETURN t.key AS anchor\n"),
				ReadGrantDomainSpec{Name: "fx"}, ReadGrantDomainSpec{Name: "fx"}),
			"duplicate domain",
		},
		{
			// The Fire 2 residual hardening (cap-read-per-anchor-grant-keys-
			// design.md §3.1): "identity" is the ONLY vertex-type token any
			// cap-read key ever carries (every generated producer's actorSuffix),
			// so a domain named identically to it collides in form with that
			// fixed segment at the same key position. An anchor's OWN type
			// (Walk.AnchorType, e.g. "task" here) never appears in a key at all —
			// only its bare NanoID does — so it is NOT part of this check.
			"grant domain name collides with the platform's fixed actorSuffix type",
			base(personalLens("x", oneWalk(AnchorWalk{GrantDomain: "identity", AnchorType: "task", AnchorVar: "t",
				Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}}), "\nRETURN t.key AS anchor\n"),
				ReadGrantDomainSpec{Name: "identity"}),
			"collides with the fixed actorSuffix vertex-type token",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.def.ExpandReadGrantWalks()
			if err == nil {
				t.Fatalf("expected rejection, got none")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err.Error(), c.want)
			}
		})
	}
}

// TestExpandReadGrantWalks_RejectsDecoyedSelfAnchorExemptions pins the two ways
// a non-self-anchored Personal lens could read as self-anchored and so escape the
// Walk requirement entirely: a decoy `AS anchorId` column whose variable IS the
// actor, and a `key: $actorKey` that is only the text of some other property's
// value. Either misread installs a lens whose every row D1 silently drops.
func TestExpandReadGrantWalks_RejectsDecoyedSelfAnchorExemptions(t *testing.T) {
	for _, c := range []struct{ name, spec string }{
		{
			"decoy anchorId column on the actor",
			"\nMATCH (identity:identity {key: $actorKey})\nOPTIONAL MATCH (wo:workorder)\n" +
				"RETURN identity.key AS anchorId, wo.key AS anchor, \"m.x\" AS ns\n",
		},
		{
			"actor-key text inside another property's value",
			"\nMATCH (wo:workorder {note: \"key: $actorKey\"})\nRETURN wo.key AS anchor\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			def := Definition{Name: "fixture", Version: "1.0.0",
				Lenses: []LensSpec{personalLens("x", nil, c.spec)}}
			_, err := def.ExpandReadGrantWalks()
			if err == nil {
				t.Fatal("expected the lens to be required to declare a Walk")
			}
			if !strings.Contains(err.Error(), "declares no Walk") {
				t.Errorf("error %q does not name the missing Walk", err.Error())
			}
		})
	}
}

// TestExpandReadGrantWalks_TailMayEnrichOffBoundVariables is the other half of
// the tail rules: reusing an already-bound variable UNLABELLED joins the
// existing binding in this engine, which is legitimate enrichment and must
// stay legal.
func TestExpandReadGrantWalks_TailMayEnrichOffBoundVariables(t *testing.T) {
	def := Definition{
		Name: "fixture", Version: "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "fx"}},
		Lenses: []LensSpec{personalLens("x", oneWalk(AnchorWalk{
			GrantDomain: "fx", AnchorType: "task", AnchorVar: "t",
			Chain: []string{"(identity)<-[:assignedTo]-(t:task)"},
		}), "\nOPTIONAL MATCH (t)-[:scopedTo]->(tgt)\nWITH t, tgt\nWHERE t.key <> null\nRETURN t.key AS anchor, tgt.key AS scopedTo\n")},
	}
	if _, err := def.ExpandReadGrantWalks(); err != nil {
		t.Fatalf("unlabelled reuse of a bound variable must stay legal: %v", err)
	}
}

// TestParseLinearPattern_AcceptsAVariableLengthTypedHop keeps the tightened hop
// grammar from over-rejecting: a typed variable-length hop is the shape the
// containment walks need and must stay legal.
func TestParseLinearPattern_AcceptsAVariableLengthTypedHop(t *testing.T) {
	for _, src := range []string{
		"(identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)",
		"(work)<-[:containedIn*0..]-(place)<-[:locatedAt]-(wo:workorder)",
		"(identity)-[r:worksAt]->(w)",
	} {
		if _, err := parseLinearPattern(src); err != nil {
			t.Errorf("%q must parse: %v", src, err)
		}
	}
}

func TestParseLinearPattern_RecordsRelationsAndLabels(t *testing.T) {
	lp, err := parseLinearPattern("(identity)<-[:identifiedBy]-(pr:provider)<-[:withProvider]-(appt:appointment)")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := strings.Join(lp.relTypes, ","); got != "identifiedBy,withProvider" {
		t.Errorf("relTypes = %q", got)
	}
	if lp.labels["appt"] != "appointment" || lp.labels["pr"] != "provider" {
		t.Errorf("labels = %v", lp.labels)
	}
	if len(lp.nodeVars) != 3 {
		t.Errorf("nodeVars = %v", lp.nodeVars)
	}
}

// TestParseLinearPattern_RenameIsPositional pins that a rename splices the
// recorded byte span rather than substituting text — `work` as a variable must
// not touch the `worksAt` relation or a `workorder` label.
func TestParseLinearPattern_RenameIsPositional(t *testing.T) {
	lp, err := parseLinearPattern("(work)<-[:worksAt]-(wo:workorder)")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := lp.render(map[string]string{"work": "work_w3"})
	want := "(work_w3)<-[:worksAt]-(wo:workorder)"
	if got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
}

// The read-grant Walk grammar refuses the taxonomy-expansion sigil in every
// node position, and refuses it BY NAME rather than as a stray-character parse
// failure at some offset. The refusal is the point (node()'s doc has why: the
// walk's AnchorType is written into every grant row as a literal audit field,
// which an abstract label would make false for every concrete anchor it binds);
// naming it is what tells a package author that, instead of leaving them to read
// it as a typo.
//
// Both spacings, because the openCypher grammar the lens engine compiles against
// admits whitespace between the label and the sigil — a walk clause written that
// way carries the sigil just as much, and must not slip past into a bare `)`
// complaint.
func TestParseLinearPattern_RefusesTheTaxonomySigilByName(t *testing.T) {
	for _, src := range []string{
		"(identity)-[:residesIn]->(l:location*)",
		"(identity)-[:residesIn]->(l:location *)",
		"(l:location*)-[:containedIn]->(c:building)",
	} {
		_, err := parseLinearPattern(src)
		if err == nil {
			t.Errorf("%q must not parse — a Walk chain names concrete types only", src)
			continue
		}
		if !strings.Contains(err.Error(), "taxonomy-expansion sigil") {
			t.Errorf("%q must be refused by name, not as an offset complaint; got %v", src, err)
		}
	}
}

// The sigil position is reachable with NO label at all — `(l *)`, where the
// sigil follows the variable and the label branch never ran — so the refusal has
// to name a coordinate the author can find in their own source. Reporting an
// empty label sends them looking for a label that is not there; the variable, or
// the offset alone for an anonymous pattern, is what they can act on.
func TestParseLinearPattern_UnlabeledSigilNamesAFindableCoordinate(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"(identity)-[:residesIn]->(l *)", `node pattern "l"`},
		{"(identity)-[:residesIn]->( *)", "an unlabeled node pattern"},
	} {
		_, err := parseLinearPattern(tc.src)
		if err == nil {
			t.Errorf("%q must not parse — a Walk chain names concrete types only", tc.src)
			continue
		}
		if !strings.Contains(err.Error(), "taxonomy-expansion sigil") {
			t.Errorf("%q must be refused by name; got %v", tc.src, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q must name %q; got %v", tc.src, tc.want, err)
		}
		if strings.Contains(err.Error(), `node label ""`) {
			t.Errorf("%q must not report an empty label — there is no label in the source; got %v", tc.src, err)
		}
	}
}

// The positive vector for the refusal above: the identical clauses without the
// sigil parse, so each rejection is caused by the `*` and not by the label, the
// relation, or the fixture's shape.
func TestParseLinearPattern_SigilRefusalHasAPositiveVector(t *testing.T) {
	for _, src := range []string{
		"(identity)-[:residesIn]->(l:location)",
		"(l:location)-[:containedIn]->(c:building)",
	} {
		if _, err := parseLinearPattern(src); err != nil {
			t.Errorf("%q must parse — only the sigil is refused: %v", src, err)
		}
	}
}

// --- property-map value variables -----------------------------------------

// propMapWalkDefinition is one lens, one walk, whose single chain clause is the
// caller's — the smallest fixture that puts a node property map in front of the
// full expansion, reserving exactly `grantSlice0`.
func propMapWalkDefinition(clause string) Definition {
	return Definition{
		Name: "fixture", Version: "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "fx"}},
		Lenses: []LensSpec{personalLens("a", oneWalk(AnchorWalk{
			GrantDomain: "fx", AnchorType: "booking", AnchorVar: "bk",
			Chain: []string{clause},
		}), "\nRETURN bk.key AS anchor\n")},
	}
}

// A node's property map binds nothing, but its VALUES resolve in the same scope
// the generated producer stages its accumulators in: `{slice: grantSlice0}`
// would be read against the accumulated grant list rather than as the author's
// own name, exactly as a node variable of that name would be. Every shape a
// value can carry the reference in — bare, nested in a map, inside a list, as a
// function argument — reaches the same scope, so each is refused.
func TestExpandReadGrantWalks_RejectsAnAccumulatorNamedInANodePropertyMap(t *testing.T) {
	for _, clause := range []string{
		"(identity)<-[:bookedBy]-(bk:booking {slice: grantSlice0})",
		"(identity)<-[:bookedBy]-(bk:booking {meta: {slice: grantSlice0}})",
		"(identity)<-[:bookedBy]-(bk:booking {slice: [grantSlice0]})",
		"(identity)<-[:bookedBy]-(bk:booking {n: size(grantSlice0)})",
		"(identity)<-[:bookedBy]-(bk:booking {slice: grantSlice0.entries})",
	} {
		_, err := propMapWalkDefinition(clause).ExpandReadGrantWalks()
		if err == nil {
			t.Errorf("%q must be refused — its property map names a generated grant-slice accumulator", clause)
			continue
		}
		if !strings.Contains(err.Error(), "accumulator") {
			t.Errorf("%q: error %q does not name the accumulator collision", clause, err.Error())
		}
	}
}

// The false-refusal guard, which matters more than the refusal above: this gate
// fails a package build CLOSED, so every name that merely LOOKS like the
// accumulator without resolving to it must still install. A property KEY is a
// name in the vertex's own namespace, a quoted value is data, a `$parameter` is
// supplied by the caller, the name after a `.` is a property of the value on its
// left, and a nested map's keys are keys like any other map's — none of them can
// join against the producer's accumulator, so none of them may refuse a build.
func TestExpandReadGrantWalks_AcceptsPropertyMapsThatOnlyLookLikeAnAccumulator(t *testing.T) {
	for _, clause := range []string{
		"(identity)<-[:bookedBy]-(bk:booking {grantSlice0: 1})",
		"(identity)<-[:bookedBy]-(bk:booking {slice: 'grantSlice0'})",
		"(identity)<-[:bookedBy]-(bk:booking {slice: \"grantSlice0\"})",
		"(identity)<-[:bookedBy]-(bk:booking {slice: $grantSlice0})",
		"(identity)<-[:bookedBy]-(bk:booking {slice: identity.grantSlice0})",
		"(identity)<-[:bookedBy]-(bk:booking {meta: {grantSlice0: 1}})",
		"(identity)<-[:bookedBy]-(bk:booking {note: 'a,grantSlice0'})",
		"(identity)<-[:bookedBy]-(bk:booking {status: 'active', tenant: identity.tenant})",
		"(identity)<-[:bookedBy]-(bk:booking {})",
		"(identity)<-[:bookedBy]-(bk:booking)",
	} {
		if _, err := propMapWalkDefinition(clause).ExpandReadGrantWalks(); err != nil {
			t.Errorf("%q must install — nothing in it resolves to the producer's accumulator: %v", clause, err)
		}
	}
}

// The extraction itself, pinned at the parser rather than through the whole
// expansion: only value positions yield a name, and a dotted path yields its
// ROOT — the variable the engine resolves — not the property hanging off it.
func TestParseLinearPattern_RecordsPropertyMapValueVariables(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"(identity)<-[:bookedBy]-(bk:booking {slice: other})", "other"},
		{"(identity)<-[:bookedBy]-(bk:booking {slice: other.entries})", "other"},
		{"(identity)<-[:bookedBy]-(bk:booking {a: [x, y]})", "x,y"},
		{"(identity)<-[:bookedBy]-(bk:booking {a: {b: x}})", "x"},
		{"(identity)<-[:bookedBy]-(bk:booking {a: coalesce(x, 'y')})", "x"},
		{"(identity)<-[:bookedBy]-(bk:booking {x: 1, y: 'x'})", ""},
		{"(identity)<-[:bookedBy]-(bk:booking {key: $actorKey})", ""},
		{"(identity)<-[:bookedBy]-(bk:booking {})", ""},
	} {
		lp, err := parseLinearPattern(tc.src)
		if err != nil {
			t.Errorf("%q must parse: %v", tc.src, err)
			continue
		}
		var got []string
		for _, v := range lp.propVars {
			got = append(got, v.name)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("%q: propVars = %q, want %q", tc.src, strings.Join(got, ","), tc.want)
		}
	}
}

// A property-map value variable is renamed by span like any other, so a rename
// that reaches the clause cannot leave a reference pointing at the old name
// while its binding moves.
func TestParseLinearPattern_RenamesPropertyMapValueVariables(t *testing.T) {
	lp, err := parseLinearPattern("(work)<-[:bookedBy]-(bk:booking {owner: work, note: 'work'})")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := lp.render(map[string]string{"work": "work_w3"})
	want := "(work_w3)<-[:bookedBy]-(bk:booking {owner: work_w3, note: 'work'})"
	if got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
}

// The relationship half of the same hazard is closed by the hop grammar itself
// rather than by recording anything: a hop admits no property map at all, so it
// carries no value expression that could name an accumulator. This pins that
// closure, because a hop that quietly started accepting `{...}` would re-open
// exactly what the node side records its way out of.
func TestParseLinearPattern_RefusesARelationshipPropertyMap(t *testing.T) {
	for _, src := range []string{
		"(identity)<-[r:bookedBy {slice: grantSlice0}]-(bk:booking)",
		"(identity)<-[{slice: grantSlice0}]-(bk:booking)",
	} {
		if _, err := parseLinearPattern(src); err == nil {
			t.Errorf("%q must not parse — a chain hop admits no property map", src)
		}
	}
}

// A brace inside a quoted property value is part of the string, and the map
// ends at the brace that closes it — the parser and the extractor read that
// boundary with the same scanner, so the entries after such a string are inside
// the map for BOTH of them. The last two rows are the shape that matters: a
// quoted brace does not end the map early, so a `slice: grantSlice0` entry
// behind one is still seen.
func TestParseLinearPattern_QuotedBracesStayInsideTheirPropertyValue(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"(identity)<-[:bookedBy]-(bk:booking {note: 'x}y'})", ""},
		{"(identity)<-[:bookedBy]-(bk:booking {note: 'x{y'})", ""},
		{"(identity)<-[:bookedBy]-(bk:booking {note: 'x}y', slice: grantSlice0})", "grantSlice0"},
		{"(identity)<-[:bookedBy]-(bk:booking {note: 'x{y', slice: grantSlice0})", "grantSlice0"},
	} {
		lp, err := parseLinearPattern(tc.src)
		if err != nil {
			t.Errorf("%q must parse — a quoted brace is string content, not the map's edge: %v", tc.src, err)
			continue
		}
		var got []string
		for _, v := range lp.propVars {
			got = append(got, v.name)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("%q: propVars = %q, want %q", tc.src, strings.Join(got, ","), tc.want)
		}
	}
}

// A map with no closing brace at all is still unterminated, and named as such
// rather than run off the end of the clause.
func TestParseLinearPattern_RefusesAnUnterminatedPropertyMap(t *testing.T) {
	for _, src := range []string{
		"(identity)<-[:bookedBy]-(bk:booking {a: 1",
		"(identity)<-[:bookedBy]-(bk:booking {a: 'x)",
	} {
		_, err := parseLinearPattern(src)
		if err == nil {
			t.Errorf("%q must not parse", src)
			continue
		}
		if !strings.Contains(err.Error(), "unterminated property map") {
			t.Errorf("%q: error %q must report the unterminated map", src, err.Error())
		}
	}
}

// The smuggling clause: a property value whose string content is written to
// close the map, re-open a relationship and a second node, so that a scan
// reading the map's edge differently from the parser hands the extractor the
// truncated body `a: 'x` and never sees the `slice: grantSlice0` entry — while
// the emitted producer carries that entry verbatim, and the engine reads it
// back as a reference to the accumulator. One scanner for the boundary is what
// closes this: the whole thing is one property map, the entry is inside it, and
// the build is refused.
func TestExpandReadGrantWalks_RefusesAnAccumulatorSmuggledBehindAQuotedBrace(t *testing.T) {
	clause := "(identity)<-[:bookedBy]-(bk:booking {a: 'x})<-[:r ', slice: grantSlice0, b: ']-(q {z: 1'})"
	_, err := propMapWalkDefinition(clause).ExpandReadGrantWalks()
	if err == nil {
		t.Fatal("expected refusal — the clause's property map names a generated grant-slice accumulator")
	}
	if !strings.Contains(err.Error(), "accumulator") {
		t.Errorf("error %q does not name the accumulator collision", err.Error())
	}
}

// --- property-map references are not bindings ------------------------------

// The cross-walk disjointness rule asks what a walk BINDS. Sibling walks in one
// lens routinely carry the same property predicate — `{cancelled: false}` on
// both of two paths to the same booking is ordinary authoring — and a keyword
// in a value position is not a name anyone can rename. Feeding property-map
// references into that rule would refuse such a lens at install time, naming a
// token that is not a variable at all.
func TestExpandReadGrantWalks_SiblingWalksMaySharePropertyPredicates(t *testing.T) {
	for _, prop := range []string{"{cancelled: false}", "{archived: null}", "{active: true}"} {
		def := Definition{
			Name: "fixture", Version: "1.0.0",
			ReadGrantDomains: []ReadGrantDomainSpec{{Name: "fx"}},
			Lenses: []LensSpec{personalLens("a", []AnchorWalk{
				{
					GrantDomain: "fx", AnchorType: "booking", AnchorVar: "bk",
					Chain: []string{"(identity)-[:worksAt]->(p1)<-[:heldAt]-(bk:booking " + prop + ")"},
				},
				{
					GrantDomain: "fx", AnchorType: "booking", AnchorVar: "bk",
					Chain: []string{"(identity)-[:livesAt]->(p2)<-[:heldAt]-(bk:booking " + prop + ")"},
				},
			}, "\nRETURN bk.key AS anchor\n")},
		}
		if _, err := def.ExpandReadGrantWalks(); err != nil {
			t.Errorf("two walks may both carry %s — a value keyword is not a variable either walk binds: %v", prop, err)
		}
	}
}

// The same rule, at its other edge: one walk REFERENCES in a property map a
// name a sibling walk BINDS. That is not the collision the rule guards, and
// both compiled artifacts show why — the data lens emits one query per walk, so
// the reference resolves (to null) inside its own branch and never reaches the
// sibling's binding, and the producer stages each walk behind a `WITH` that
// projects only the actor and the accumulated slices, so the sibling's binding
// is already gone by the time the referencing walk's clauses run.
func TestExpandReadGrantWalks_APropertyReferenceIsNotACrossWalkBinding(t *testing.T) {
	def := Definition{
		Name: "fixture", Version: "1.0.0",
		ReadGrantDomains: []ReadGrantDomainSpec{{Name: "fx"}},
		Lenses: []LensSpec{personalLens("a", []AnchorWalk{
			{
				GrantDomain: "fx", AnchorType: "booking", AnchorVar: "bk",
				Chain: []string{"(identity)-[:worksAt]->(p1)<-[:heldAt]-(bk:booking {room: p2})"},
			},
			{
				GrantDomain: "fx", AnchorType: "booking", AnchorVar: "bk",
				Chain: []string{"(identity)-[:livesAt]->(p2)<-[:heldAt]-(bk:booking)"},
			},
		}, "\nRETURN bk.key AS anchor\n")},
	}
	exp, err := def.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("a reference is not a binding, so the two walks do not collide: %v", err)
	}
	if len(exp.Lenses[0].SpecBranches) != 2 {
		t.Fatalf("a two-walk lens compiles to one query per walk, got %d branches", len(exp.Lenses[0].SpecBranches))
	}
	if strings.Contains(exp.Lenses[0].SpecBranches[0], "(p2)") {
		t.Errorf("walk 0's branch must not carry walk 1's binding of p2:%s", exp.Lenses[0].SpecBranches[0])
	}
	producer := exp.Lenses[1].Spec
	stage0End := strings.Index(producer, "AS grantSlice0")
	if stage0End < 0 {
		t.Fatalf("producer has no staged slice for walk 0:%s", producer)
	}
	if strings.Contains(producer[:stage0End], "(p2)") {
		t.Errorf("walk 1's binding of p2 must not appear before walk 0's stage closes:%s", producer)
	}
}
