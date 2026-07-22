package refractor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// capabilityRolesSpecForTest returns rbac-domain's capabilityRoles LensSpec,
// selected by canonical name. The capability e2e tests drive it directly to
// exercise the role-derived grant projection + its fan-out/reprojection on the
// disjoint cap.roles.<actor> key.
func capabilityRolesSpecForTest(t *testing.T) pkgmgr.LensSpec {
	t.Helper()
	for _, l := range rbacdomain.Lenses() {
		if l.CanonicalName == "capabilityRoles" {
			return l
		}
	}
	require.FailNow(t, "rbac-domain must declare a capabilityRoles lens")
	return pkgmgr.LensSpec{}
}

// descFromPkgSpec converts a package LensSpec's §6.13 Output descriptor into a
// compiled projection.OutputDescriptor for wiring a package actor-aggregate
// pipeline directly in a test (the field shapes are identical).
func descFromPkgSpec(t *testing.T, l pkgmgr.LensSpec) projection.OutputDescriptor {
	t.Helper()
	require.NotNil(t, l.Output, "package lens %q must declare an Output descriptor", l.CanonicalName)
	desc, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:         l.Output.AnchorType,
		OutputKeyPattern:   l.Output.OutputKeyPattern,
		BodyColumns:        l.Output.BodyColumns,
		EmptyBehavior:      l.Output.EmptyBehavior,
		RealnessFilter:     l.Output.RealnessFilter,
		Freshness:          l.Output.Freshness,
		ActorField:         l.Output.ActorField,
		Lanes:              l.Output.Lanes,
		StaticEmptyColumns: l.Output.StaticEmptyColumns,
	})
	require.NoError(t, err, "package lens %q Output descriptor must be valid", l.CanonicalName)
	return desc
}

// wireActorAggregate installs the envelope + cross-vertex fan-out + actor-
// delete-key on an actor-aggregate pipeline from the activated lens Rule's §6.13
// Output descriptor — the subset of projection.InstallActorAggregate's wiring
// that does not require an adapter or logger (no guard, no plan-compile
// diagnostics). The e2e tests use it so they exercise the same data-driven
// envelope/fan-out/delete-key path production runs.
func wireActorAggregate(t *testing.T, p *pipeline.Pipeline, r *lens.Rule, adjKV, coreKV *substrate.KV, projectionRevision func(string) uint64) {
	t.Helper()
	desc, err := projection.ParseOutputDescriptor(r.Output)
	require.NoError(t, err, "lens %q must carry a valid Output descriptor", r.CanonicalName)
	p.SetEnvelopeFn(desc.EnvelopeFn("vtx.meta."+r.ID, projectionRevision))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, desc.AnchorType))
	p.SetActorDeleteKey(desc.BuildKey)
}

// e2eSpec builds the supervised-consumer spec the e2e tests pass to
// pipeline.RunOn, mirroring production wiring (durable refractor-<ruleID>, queue
// group = same name, DeliverLastPerSubject, Core KV stream + filter).
func e2eSpec(ruleID, bucket string) substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          "refractor-" + ruleID,
		Stream:        subjects.CoreKVStream(bucket),
		FilterSubject: subjects.CoreKVFilter(bucket),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-" + ruleID,
	}
}
