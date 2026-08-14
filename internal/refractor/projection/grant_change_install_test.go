package projection_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// recordingGrantSink satisfies pipeline.GrantChangeSink and remembers what it
// was told, so an installation test can prove the edge is wired rather than
// merely that install returned true.
type recordingGrantSink struct{ actors []string }

func (s *recordingGrantSink) GrantChanged(actorKey string) { s.actors = append(s.actors, actorKey) }

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
			name:           "a cap-read-prefixed lens that is NOT per-entry is refused",
			bucket:         projection.AuthPlaneBucket,
			keyPattern:     "cap-read.{actorSuffix}",
			entryKeyColumn: "",
			want:           false,
		},
		{
			// The pattern grammar permits a repeated placeholder, and BuildKey
			// substitutes EVERY occurrence while AnchorFromKey brackets only
			// the first — so the inverse can never recover the actor from a key
			// this lens wrote. Wiring the edge onto it would install something
			// that emits nothing for its entire life, with no trace anywhere
			// that names the edge.
			name:           "a lens whose key pattern does not round-trip is refused",
			bucket:         projection.AuthPlaneBucket,
			keyPattern:     "cap-read.{actorSuffix}.{actorSuffix}",
			entryKeyColumn: "anchorId",
			want:           false,
		},
		{
			name:           "a business-plane lens is refused",
			bucket:         "weaver-targets",
			keyPattern:     "cap-read.{actorSuffix}",
			entryKeyColumn: "anchorId",
			want:           false,
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
