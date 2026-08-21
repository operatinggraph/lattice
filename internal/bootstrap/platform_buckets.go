package bootstrap

// PlatformBucket describes one platform-provisioned KV bucket: who may write
// its rows, whether packages may target it with lenses, and how it is
// provisioned. The registry is the single source for ProvisionBuckets,
// pkgmgr's reserved-bucket lens guard (and Refractor's activation-time
// mirror of that guard), and the gen-dev-nkeys/internal/natsperm permission
// matrix's owner-allows + denies — a bucket absent here does not exist: it
// is never provisioned, never guarded, never granted.
type PlatformBucket struct {
	Name        string
	Description string
	// PerKeyTTL enables per-key TTL support (Contract #4 §4.3) on the bucket.
	PerKeyTTL bool
	// Owner is the matrix component name (internal/natsperm) whose connection
	// writes the bucket's rows. Empty when SharedWrite is true.
	Owner string
	// SharedWrite marks the buckets every component writes (health-kv).
	SharedWrite bool
	// LensTarget marks the shared projection buckets package lenses may
	// legitimately declare (weaver-targets, capability-kv,
	// orchestration-history). Non-LensTarget buckets are platform-private:
	// pkgmgr and Refractor's activation-time mirror reject a lens naming them.
	LensTarget bool
}

// PlatformBuckets returns the platform-provisioned KV bucket registry — the
// one place a platform bucket is born. ProvisionBuckets, pkgmgr's reserved-
// bucket lens guard, Refractor's activation-time mirror, and the NATS
// permission matrix (internal/natsperm) all derive from this list.
func PlatformBuckets() []PlatformBucket {
	return []PlatformBucket{
		{
			Name:        CoreKVBucket,
			Description: "Lattice Core KV — primary graph store",
			PerKeyTTL:   true,
			Owner:       "processor",
		},
		{
			Name:        HealthKVBucket,
			Description: "Lattice Health KV — component heartbeats",
			PerKeyTTL:   true,
			SharedWrite: true,
		},
		{
			Name:        CapabilityKVBucket,
			Description: "Lattice Capability KV — Refractor projection targets",
			PerKeyTTL:   true,
			Owner:       "refractor",
			LensTarget:  true,
		},
		{
			Name:        WeaverStateBucket,
			Description: "Lattice Weaver State KV",
			PerKeyTTL:   true,
			Owner:       "weaver",
		},
		{
			Name:        LoomStateBucket,
			Description: "Lattice Loom State KV — per-instance pattern cursors",
			PerKeyTTL:   true,
			Owner:       "loom",
		},
		{
			// weaver-targets rows are durable Lens projections — no per-key TTL
			// keys live here (TTL-leased marks live in weaver-state). History
			// stays the KV default 1, which is what DeliverLastPerSubject CDC
			// consumers expect.
			Name:        WeaverTargetsBucket,
			Description: "Lattice Weaver Targets KV — shared target-Lens projection bucket",
			Owner:       "refractor",
			LensTarget:  true,
		},
		{
			// orchestration-history is a guarded eventStream Lens target
			// (monotonic last_event_seq CAS, not per-key TTL) — history stays
			// the KV default 1.
			Name:        OrchestrationHistoryBucket,
			Description: "Lattice Orchestration History KV — Chronicler durable Loom-flow read model",
			Owner:       "chronicler",
			LensTarget:  true,
		},
		{
			// model-results is the model-runner's per-ref result store: an
			// in-flight marker CAS-created before the vendor call, replaced by
			// the terminal result, plus the fleet's daily spend counter. Every
			// key is per-key TTL'd — nothing here is durable truth, and the
			// TTL is what reopens a ref after a runner dies mid-call.
			// Platform-private operational state like weaver-state: not Core
			// KV, not a lens target. Callers (the bridge's model-backed
			// adapters) read it; only the runner writes it.
			Name:        ModelResultsBucket,
			Description: "Lattice Model Results KV — model-runner call results (per-ref, TTL'd)",
			PerKeyTTL:   true,
			Owner:       "model-runner",
		},
		{
			Name:        RefractorAdjacencyKV,
			Description: "Refractor internal adjacency store (private)",
			Owner:       "refractor",
		},
		{
			Name:        PersonalLensInterestKV,
			Description: "Refractor Personal Lens Interest Set registry (private)",
			Owner:       "refractor",
		},
		{
			// token-revocation is a compacting latest-per-actor set (put on
			// revoke, del on unrevoke) materialized by the Gateway from
			// events.gateway.>; no per-key TTL, durable (rebuildable from the
			// event stream on cold start, but must not silently disappear
			// between rebuilds).
			Name:        GatewayRevocationBucket,
			Description: "Lattice Gateway Token-Revocation KV — actor kill-switch set",
			Owner:       "gateway",
		},
		{
			// credential-bindings is a compacting latest-per-actor set (put on
			// claim; no unbind path in this refinement's scope) materialized
			// by the Gateway from events.identity.>; no per-key TTL, durable
			// (rebuildable from the event stream on cold start).
			Name:        GatewayCredentialBindingsBucket,
			Description: "Lattice Gateway Credential-Bindings KV — credential→identity resolution set",
			Owner:       "gateway",
		},
	}
}

// ReservedBuckets returns the set of platform-private bucket names — every
// registry row with LensTarget=false. A package lens must never declare one
// of these as its own Bucket: pkgmgr's install-time guard and Refractor's
// activation-time mirror both derive from this so the reserved set can never
// drift out of sync with the registry (the failure mode that let
// credential-bindings ship unguarded).
func ReservedBuckets() map[string]struct{} {
	out := make(map[string]struct{})
	for _, b := range PlatformBuckets() {
		if !b.LensTarget {
			out[b.Name] = struct{}{}
		}
	}
	return out
}
