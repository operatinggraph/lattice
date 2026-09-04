package projection_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// recordingGrantSink satisfies pipeline.GrantChangeSink and remembers what it
// was told, so an installation test can prove the edge is wired rather than
// merely that install returned true.
type recordingGrantSink struct {
	actors  []string
	entries []string
}

func (s *recordingGrantSink) GrantChanged(actorKey, entryID string) {
	s.actors = append(s.actors, actorKey)
	s.entries = append(s.entries, entryID)
}

// TestInstallActorAggregate_GrantChangeSinkClassification is T2 of
// personal-lens-grant-change-trigger-design.md §10: the sink reaches exactly
// the lenses that produce the D1 read-grant projection a Personal Lens gates
// on, and no others.
//
// The negative half carries the weight. Every case below shares the auth-plane
// bucket, so a classification that collapsed to "auth-plane" alone would still
// pass the positive case and wire the edge onto the write-plane producers too —
// which would drive a personal reprojection on every role/permission change,
// permanently, for a projection no personal lens ever reads.
func TestInstallActorAggregate_GrantChangeSinkClassification(t *testing.T) {
	cases := []struct {
		name           string
		bucket         string
		keyPattern     string
		entryKeyColumn string
		want           bool
		// refusedAtInstall marks the shapes the producer-closure gate
		// (projection.CapReadWriterRefusal) now refuses outright: a lens
		// claiming the cap-read.* namespace that cannot carry the change edge
		// is not installed sink-less, it is not installed at all. The
		// cap.*-prefixed write-plane rows below stay installable — they never
		// claim the namespace, so the D1 gate's wildcard never reads them.
		refusedAtInstall bool
	}{
		{
			// The bootstrap capabilityRead base lens's real shape.
			name:           "the cap-read base producer is admitted",
			bucket:         projection.AuthPlaneBucket,
			keyPattern:     "cap-read.{actorSuffix}",
			entryKeyColumn: "anchorId",
			want:           true,
		},
		{
			// One generated producer per declared read-grant domain
			// (pkgmgr.ExpandReadGrantWalks) — same prefix, extra segment.
			name:           "a generated per-domain cap-read producer is admitted",
			bucket:         projection.AuthPlaneBucket,
			keyPattern:     "cap-read.staff.{actorSuffix}",
			entryKeyColumn: "anchorId",
			want:           true,
		},
		{
			// The write plane. Its consumer is the Processor, which reads
			// Capability KV synchronously at commit — a revocation takes
			// effect on the next operation with no notification needed.
			name:           "a cap.roles write-plane producer is refused",
			bucket:         projection.AuthPlaneBucket,
			keyPattern:     "cap.roles.{actorSuffix}",
			entryKeyColumn: "anchorId",
			want:           false,
		},
		{
			name:           "a cap.{actorSuffix} write-plane producer is refused",
			bucket:         projection.AuthPlaneBucket,
			keyPattern:     "cap.{actorSuffix}",
			entryKeyColumn: "anchorId",
			want:           false,
		},
		{
			// A doc-mode lens writes one document per actor, so a write to it
			// says "this actor's grants changed somehow" — not which grant, and
			// not whether any liveness flipped. The per-key transition the edge
			// is built on does not exist for it.
			name:             "a cap-read-prefixed lens that is NOT per-entry is refused",
			bucket:           projection.AuthPlaneBucket,
			keyPattern:       "cap-read.{actorSuffix}",
			entryKeyColumn:   "",
			want:             false,
			refusedAtInstall: true,
		},
		{
			// The pattern grammar permits a repeated placeholder, and BuildKey
			// substitutes EVERY occurrence while AnchorFromKey brackets only
			// the first — so the inverse can never recover the actor from a key
			// this lens wrote. Wiring the edge onto it would install something
			// that emits nothing for its entire life, with no trace anywhere
			// that names the edge.
			name:             "a lens whose key pattern does not round-trip is refused",
			bucket:           projection.AuthPlaneBucket,
			keyPattern:       "cap-read.{actorSuffix}.{actorSuffix}",
			entryKeyColumn:   "anchorId",
			want:             false,
			refusedAtInstall: true,
		},
		{
			name:             "a business-plane lens is refused",
			bucket:           "weaver-targets",
			keyPattern:       "cap-read.{actorSuffix}",
			entryKeyColumn:   "anchorId",
			want:             false,
			refusedAtInstall: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := installRule(t, tc.bucket, string(projection.EmptyDelete))
			r.Output.OutputKeyPattern = tc.keyPattern
			if tc.entryKeyColumn != "" {
				r.Output.EntryKeyColumn = tc.entryKeyColumn
				r.Output.RealnessFilter = tc.entryKeyColumn
			}
			adpt := newUnguardedAdapter(t)
			p := newTestPipeline(t, adpt)

			ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 },
				nil, nil, discardLogger(), projection.WithGrantChangeSink(&recordingGrantSink{}))
			if tc.refusedAtInstall {
				if ok {
					t.Fatalf("expected the producer-closure gate to refuse registration (bucket %q, pattern %q, entryKeyColumn %q)",
						tc.bucket, tc.keyPattern, tc.entryKeyColumn)
				}
				return
			}
			if !ok {
				t.Fatalf("expected the lens to install")
			}
			if got := p.HasGrantChangeSink(); got != tc.want {
				t.Fatalf("grant-change sink installed = %v, want %v (bucket %q, pattern %q, entryKeyColumn %q)",
					got, tc.want, tc.bucket, tc.keyPattern, tc.entryKeyColumn)
			}
		})
	}
}

// TestInstallActorAggregate_NoSinkOfferedInstallsNone pins the fail-slow
// default: an installation that offers no sink wires no edge, and the lens
// still installs. Every existing caller — production paths that predate the
// edge and the harness corpus alike — takes this branch.
func TestInstallActorAggregate_NoSinkOfferedInstallsNone(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptyDelete))
	r.Output.OutputKeyPattern = "cap-read.{actorSuffix}"
	r.Output.EntryKeyColumn = "anchorId"
	r.Output.RealnessFilter = "anchorId"
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if !projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()) {
		t.Fatalf("expected the lens to install without a sink")
	}
	if p.HasGrantChangeSink() {
		t.Fatalf("no sink was offered, so none may be installed")
	}
}

// TestReadGrantSinkCensus_CountsTheSinkLessProducers pins the §4.3(d)
// amendment's consumer half at its source.
//
// A qualifying producer with no sink INSTALLS — refusing it would turn a host
// with no reprojector into an auth-plane outage on the primordial capabilityRead
// lens — and warns. The warning alone is not something a consumer can refuse on,
// so the same boolean the warning is emitted from is recorded in a process-level
// census, and the personal derivation licence's conjunct 1 reads it.
//
// Both directions are pinned. A census that only ever answered "none" would pass
// just as happily if nothing ever wrote to it, which is precisely how the
// conjunct it feeds would become vacuous.
func TestReadGrantSinkCensus_CountsTheSinkLessProducers(t *testing.T) {
	install := func(t *testing.T, ruleID string, offerSink bool) {
		t.Helper()
		r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptyDelete))
		r.ID = ruleID
		r.Output.OutputKeyPattern = "cap-read.{actorSuffix}"
		r.Output.EntryKeyColumn = "anchorId"
		r.Output.RealnessFilter = "anchorId"
		adpt := newUnguardedAdapter(t)
		p := newTestPipeline(t, adpt)

		opts := []projection.InstallOption{}
		if offerSink {
			opts = append(opts, projection.WithGrantChangeSink(&recordingGrantSink{}))
		}
		if !projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger(), opts...) {
			t.Fatalf("expected %s to install", ruleID)
		}
	}
	listed := func(ruleID string) bool {
		for _, id := range projection.ReadGrantProducersWithoutSink() {
			if id == ruleID {
				return true
			}
		}
		return false
	}

	const sinkless, sinked = "census-sinkless-lens", "census-sinked-lens"
	t.Cleanup(func() {
		projection.ForgetReadGrantProducer(sinkless)
		projection.ForgetReadGrantProducer(sinked)
	})

	install(t, sinkless, false)
	if !listed(sinkless) {
		t.Fatalf("a read-grant producer installed with no sink must be counted; the licence's conjunct 1 has nothing to refuse on otherwise")
	}

	install(t, sinked, true)
	if listed(sinked) {
		t.Fatalf("a producer that DID get a sink must not be counted")
	}

	// A hot reload that adds the sink clears the entry the previous body wrote —
	// otherwise one boot-order accident would refuse the narrowing for the life
	// of the process.
	install(t, sinkless, true)
	if listed(sinkless) {
		t.Fatalf("re-installing the same lens WITH a sink must clear its census entry")
	}

	// And a deleted lens stops being counted, for the reason the reprojector's
	// own deregistration exists: a lens that no longer runs must stop being held
	// against a consumer deciding what to narrow.
	install(t, sinkless, false)
	projection.ForgetReadGrantProducer(sinkless)
	if listed(sinkless) {
		t.Fatalf("a lens removed from the process must leave the census")
	}
}
