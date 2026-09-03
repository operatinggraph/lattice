package projection_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// TestCapReadWriterRefusal_ClosesTheProducerSet is the producer-closure gate's
// own table (personal-lens-derivation-licence-design.md §4.3b).
//
// The D1 read gate finds its rows by a WILDCARD listing over cap-read.*, so any
// lens writing that namespace is read as a live grant whether or not the
// platform has ever heard of it. That makes "every cap-read producer announces"
// a standing claim rather than a census result — and a consumer that narrows on
// the strength of it needs the claim to be an install-time property. This is
// the predicate that makes it one.
//
// Each negative row asserts the SPECIFIC conjunct named in the refusal, not
// merely that something was refused: a gate that answered "does not qualify"
// for every shape would pass a want-a-refusal test while leaving the author to
// re-derive a four-conjunct predicate from a log line.
func TestCapReadWriterRefusal_ClosesTheProducerSet(t *testing.T) {
	cases := []struct {
		name           string
		bucket         string
		projectionKind string
		keyPattern     string
		entryKeyColumn string
		noOutput       bool
		wantRefusal    string
	}{
		{
			name:           "the cap-read base producer is sanctioned",
			bucket:         projection.AuthPlaneBucket,
			projectionKind: projection.ActorAggregateKind,
			keyPattern:     "cap-read.{actorSuffix}",
			entryKeyColumn: "anchorId",
		},
		{
			name:           "a generated per-domain producer is sanctioned",
			bucket:         projection.AuthPlaneBucket,
			projectionKind: projection.ActorAggregateKind,
			keyPattern:     "cap-read.edge-manifest.{actorSuffix}",
			entryKeyColumn: "anchorId",
		},
		{
			// The concrete exploit §4.3b names: a vertical shipping
			// cap-read.billing.<actor> through a lens with no projection plan.
			// The reader's wildcard finds its keys; nothing on the platform can
			// ever hear them withdrawn.
			name:           "a plain lens claiming the namespace is refused",
			bucket:         projection.AuthPlaneBucket,
			projectionKind: "",
			keyPattern:     "cap-read.billing.{actorSuffix}",
			entryKeyColumn: "anchorId",
			wantRefusal:    "without projectionKind actorAggregate",
		},
		{
			name:           "a doc-mode producer is refused, naming the missing entryKeyColumn",
			bucket:         projection.AuthPlaneBucket,
			projectionKind: projection.ActorAggregateKind,
			keyPattern:     "cap-read.billing.{actorSuffix}",
			entryKeyColumn: "",
			wantRefusal:    "no entryKeyColumn",
		},
		{
			name:           "a producer outside the auth plane is refused, naming the plane",
			bucket:         "weaver-targets",
			projectionKind: projection.ActorAggregateKind,
			keyPattern:     "cap-read.billing.{actorSuffix}",
			entryKeyColumn: "anchorId",
			wantRefusal:    "does not project an authorization surface",
		},
		{
			name:           "a producer whose key does not round-trip is refused, naming the inverse",
			bucket:         projection.AuthPlaneBucket,
			projectionKind: projection.ActorAggregateKind,
			keyPattern:     "cap-read.{actorSuffix}.{actorSuffix}",
			entryKeyColumn: "anchorId",
			wantRefusal:    "does not round-trip through AnchorFromKey",
		},
		{
			// The gate governs the cap-read namespace and nothing else. A
			// write-plane producer sharing the bucket is not its business —
			// the Processor consumes that projection synchronously at commit.
			name:           "a cap.roles write-plane producer is untouched",
			bucket:         projection.AuthPlaneBucket,
			projectionKind: projection.ActorAggregateKind,
			keyPattern:     "cap.roles.{actorSuffix}",
			entryKeyColumn: "anchorId",
		},
		{
			// A lens with no §6.13 descriptor declares no key space at all: its
			// keys come from row columns, so there is no static prefix to
			// refuse on and the authoring gate sees exactly the same nothing.
			name:     "a lens with no output descriptor declares no key space",
			bucket:   projection.AuthPlaneBucket,
			noOutput: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := installRule(t, tc.bucket, string(projection.EmptyDelete))
			r.ProjectionKind = tc.projectionKind
			if tc.noOutput {
				r.Output = nil
			} else {
				r.Output.OutputKeyPattern = tc.keyPattern
				if tc.entryKeyColumn != "" {
					r.Output.EntryKeyColumn = tc.entryKeyColumn
					r.Output.RealnessFilter = tc.entryKeyColumn
				}
			}

			got := projection.CapReadWriterRefusal(r)
			switch {
			case tc.wantRefusal == "" && got != "":
				t.Fatalf("expected no refusal, got %q", got)
			case tc.wantRefusal != "" && got == "":
				t.Fatalf("expected a refusal naming %q, got none", tc.wantRefusal)
			case tc.wantRefusal != "" && !strings.Contains(got, tc.wantRefusal):
				t.Fatalf("refusal %q does not name %q", got, tc.wantRefusal)
			}
		})
	}
}

// TestCapReadWriterRefusal_ShippedProducersStillInstall runs the gate over the
// producers the tree actually ships, built from their own declarations rather
// than restated here: the base capabilityRead lens, and every generated
// producer the shipped packages' read-grant domains compile to.
//
// It is the half a preventive gate most needs and least often has. The table
// above proves the gate refuses; only this proves it refuses nothing the
// platform depends on — and a refusal here would take the whole read-auth plane
// down at boot rather than one lens.
func TestCapReadWriterRefusal_ShippedProducersStillInstall(t *testing.T) {
	sanctioned := 0
	// Each declaration is run through the gate with ITS OWN projectionKind and
	// ITS OWN target bucket. Overwriting either would make the test assert
	// about a lens nobody ships: forcing actorAggregate onto every candidate
	// and skipping the rest means a shipped plain lens that claimed the
	// namespace could never be caught here, which is precisely the violation
	// the gate exists for.
	check := func(t *testing.T, who, kind, bucket string, out *lens.OutputDescriptorSpec) {
		t.Helper()
		if out == nil || !strings.HasPrefix(out.OutputKeyPattern, "cap-read.") {
			return
		}
		sanctioned++
		r := installRule(t, bucket, string(projection.EmptyDelete))
		r.ProjectionKind = kind
		r.Output = out
		if refusal := projection.CapReadWriterRefusal(r); refusal != "" {
			t.Fatalf("%s is a SHIPPED cap-read producer and must stay sanctioned as declared (projectionKind=%q, bucket=%q), got refusal: %s",
				who, kind, bucket, refusal)
		}
	}

	base := bootstrap.CapabilityReadLensDefinition()
	check(t, "the base capabilityRead producer", base.ProjectionKind,
		provisionedKernelBucket(base.TargetBucket), bootstrapOutput(base.Output))

	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			t.Fatalf("pkgregistry.Names() returned %q but Lookup does not know it", name)
		}
		expanded, err := def.ExpandReadGrantWalks()
		if err != nil {
			t.Fatalf("%s: ExpandReadGrantWalks: %v", name, err)
		}
		for _, l := range expanded.Lenses {
			check(t, name+"/"+l.CanonicalName, l.ProjectionKind, l.Bucket, pkgmgrOutput(l.Output))
		}
	}

	if sanctioned < 4 {
		t.Fatalf("only %d shipped cap-read producer(s) reached the gate — the census behind this test is four (the base plus one per shipped read-grant domain), and a shrunken set is the wrong-reason green it exists to avoid", sanctioned)
	}
}

// provisionedKernelBucket mirrors internal/bootstrap's makeLensSpecBody: a
// kernel definition carries the short bucket name, or omits it, and both map
// onto the provisioned capability bucket before Refractor ever classifies the
// lens.
func provisionedKernelBucket(target string) string {
	if target == "capability" || target == "" {
		return projection.AuthPlaneBucket
	}
	return target
}

// bootstrapOutput and pkgmgrOutput carry a shipped declaration across into the
// descriptor shape the resolver reads. Field-for-field rather than a cast: the
// three types are independently declared, and a silently-dropped field here
// would make the gate answer about a descriptor no lens ever had.
func bootstrapOutput(o *bootstrap.OutputDescriptorSpec) *lens.OutputDescriptorSpec {
	if o == nil {
		return nil
	}
	return &lens.OutputDescriptorSpec{
		AnchorType:         o.AnchorType,
		OutputKeyPattern:   o.OutputKeyPattern,
		BodyColumns:        o.BodyColumns,
		EmptyBehavior:      o.EmptyBehavior,
		RealnessFilter:     o.RealnessFilter,
		Freshness:          o.Freshness,
		ActorField:         o.ActorField,
		Lanes:              o.Lanes,
		StaticEmptyColumns: o.StaticEmptyColumns,
		EntryKeyColumn:     o.EntryKeyColumn,
	}
}

func pkgmgrOutput(o *pkgmgr.OutputDescriptorSpec) *lens.OutputDescriptorSpec {
	if o == nil {
		return nil
	}
	return &lens.OutputDescriptorSpec{
		AnchorType:       o.AnchorType,
		OutputKeyPattern: o.OutputKeyPattern,
		BodyColumns:      o.BodyColumns,
		EmptyBehavior:    o.EmptyBehavior,
		RealnessFilter:   o.RealnessFilter,
		Freshness:        o.Freshness,
		KeyColumn:        o.KeyColumn,
		ActorField:       o.ActorField,
		Lanes:            o.Lanes,
		EntryKeyColumn:   o.EntryKeyColumn,
	}
}

// TestInstallActorAggregate_BindsTheReadGrantLicence pins the SECOND binding
// site of the namespace licence, and why there are two.
//
// cmd/refractor's buildAdapter binds it for every adapter it builds — which is
// the only site an INTO-only hot reload passes through, since a reload never
// runs the installer. But a caller that constructs an adapter directly never
// passes through buildAdapter: every harness does this, and so does any
// embedder. Binding at one site alone leaves the other's producers unlicensed,
// and an unlicensed producer has every write it makes refused.
//
// Both sites call the same rule-derived function, so they cannot reach two
// answers about one lens.
func TestInstallActorAggregate_BindsTheReadGrantLicence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		entryKeyColumn string
		want           bool
	}{
		{"a qualifying producer is licensed", "anchorId", true},
		{"a doc-mode lens claiming the namespace is not", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptyDelete))
			r.Output.OutputKeyPattern = "cap-read.installtest.{actorSuffix}"
			r.Output.BodyColumns = []string{"readableAnchors"}
			if tc.entryKeyColumn != "" {
				r.Output.EntryKeyColumn = tc.entryKeyColumn
				r.Output.RealnessFilter = tc.entryKeyColumn
			}
			adpt := newUnguardedAdapter(t)
			p := newTestPipeline(t, adpt)

			ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 },
				nil, nil, discardLogger())
			if !tc.want {
				// A doc-mode lens claiming the namespace is refused outright by
				// the declaration-level check, so it never installs — and an
				// adapter that never installed is unlicensed by default.
				require.False(t, ok, "the producer-closure gate refuses this shape at registration")
				require.False(t, adpt.ReadGrantWriter(), "and it acquires no licence on the way out")
				return
			}
			require.True(t, ok)
			require.True(t, adpt.ReadGrantWriter(),
				"a qualifying producer installed outside cmd/refractor must still be licensed, or every grant it writes is refused")
		})
	}
}
