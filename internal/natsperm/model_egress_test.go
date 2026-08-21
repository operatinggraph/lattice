package natsperm

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// modelGenerateSubject is the model-runner's request subject
// (internal/modelrunner/wire.GenerateSubject). Hardcoded here — like the other
// conformance tests — so the assertion is against the committed
// deploy/nats-server.conf, not a shared Go constant.
const modelGenerateSubject = "svc.model.generate"

// TestModelEgressReachability proves the transport gate around the platform's
// only external-model egress (natural-language-weaver-targets-design.md §3.1).
//
// The runner does NO caller-level authorization — it takes any well-formed
// request and spends money on it — so this publish allow-list IS the boundary
// on who can make the platform call a model vendor. The bridge is the one
// sanctioned caller; everything else, including the trusted inspector and the
// operator CLI, is denied.
//
// The runner stands in as the responder on its own seed, exactly as it does in
// production: subscribe is unrestricted under the write-only-restriction
// model, and it replies through allow_responses rather than any publish grant
// on its own subject — the Weaver control-plane posture.
func TestModelEgressReachability(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	runner := connectAs(t, url, "model-runner")
	sub, err := runner.NATS().Subscribe(modelGenerateSubject, func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"status":"accepted","ref":"x"}`))
	})
	if err != nil {
		t.Fatalf("model-runner subscribe %q: %v", modelGenerateSubject, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := runner.NATS().Flush(); err != nil {
		t.Fatalf("flush responder: %v", err)
	}

	// The bridge is the caller: its adapters dispatch authoring work here.
	bridge := connectAs(t, url, "bridge")
	reply, err := bridge.NATS().Request(modelGenerateSubject, []byte(`{"ref":"x"}`), 3*time.Second)
	if err != nil {
		t.Fatalf("bridge request %q: want reply, got %v", modelGenerateSubject, err)
	}
	if len(reply.Data) == 0 {
		t.Fatalf("bridge request %q: empty reply", modelGenerateSubject)
	}

	// Everyone else is denied. Spend is the asset being protected here, so the
	// denial set deliberately includes the components that hold the BROADEST
	// grants elsewhere — Loupe reads all KV and the CLI drives every control
	// plane, and neither may make the platform call a vendor.
	for _, component := range []string{"loupe", "lattice", "loom", "weaver", "gateway", "clinic-app", "facet"} {
		component := component
		t.Run("denied/"+component, func(t *testing.T) {
			t.Parallel()
			rogue := connectAs(t, url, component)
			ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
			defer cancel()
			if _, err := rogue.NATS().RequestWithContext(ctx, modelGenerateSubject, []byte(`{"ref":"x"}`)); err == nil {
				t.Errorf("%s request %q: want transport denial (timeout), got a reply", component, modelGenerateSubject)
			}
		})
	}
}

// capabilityAuthorContextBucket is the capability-author package's catalog lens
// target — the installed-DDL self-description read model the bridge's
// capabilityAuthor adapter assembles its prompt from
// (packages/capability-author's CapabilityAuthorContextBucket). Hardcoded here,
// like the other conformance constants, so the assertion is against the
// committed deploy/nats-server.conf rather than a shared Go constant.
const capabilityAuthorContextBucket = "capability-author-context"

// TestCapabilityAuthorCatalogAccess pins both halves of the bridge's catalog
// grant (natural-language-weaver-targets-design.md §3.2).
//
// READ is the load-bearing positive: the catalog IS the adapter's prompt, so a
// silently-denied read would not fail loudly — it would send a model a request
// to bind a target to a lens it was never shown, and bill for the guess. The
// bridge reaches it through the same broad $JS.API.> publish every component
// holds; its two extra read-side denies are scoped to the core-kv backing stream
// specifically, so a package lens target stays reachable.
//
// WRITE is the negative that keeps the read-model one-directional: the Refractor
// is the sole projector of every lens target, and a bridge that could write this
// bucket could author its own catalog and then reason over it — a prompt-
// injection vector with no human in the loop, since the catalog is the one
// prompt input no operator reviews.
func TestCapabilityAuthorCatalogAccess(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, capabilityAuthorContextBucket)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The projector writes a row — the positive control that makes the bridge's
	// read below a permission result rather than an empty bucket.
	ref := connectAs(t, url, "refractor")
	row := []byte(`{"key":"vtx.meta.MetaCatRowAbc1234567","class":"meta.lens","canonicalName":"someLens"}`)
	if _, err := ref.KVPut(ctx, capabilityAuthorContextBucket, "vtx.meta.MetaCatRowAbc1234567", row); err != nil {
		t.Fatalf("refractor KVPut %s: want success, got %v", capabilityAuthorContextBucket, err)
	}

	bridge := connectAs(t, url, "bridge")
	entry, err := bridge.KVGet(ctx, capabilityAuthorContextBucket, "vtx.meta.MetaCatRowAbc1234567")
	if err != nil {
		t.Fatalf("bridge KVGet %s: want success, got %v", capabilityAuthorContextBucket, err)
	}
	if len(entry.Value) == 0 {
		t.Fatalf("bridge KVGet %s: empty row", capabilityAuthorContextBucket)
	}
	// The adapter lists the bucket before reading it, and a listing runs an
	// ordered consumer rather than a direct get — a distinct permission path,
	// pinned separately so a future narrowing that leaves KVGet working cannot
	// silently break the snapshot.
	keys, err := bridge.KVListKeys(ctx, capabilityAuthorContextBucket)
	if err != nil {
		t.Fatalf("bridge KVListKeys %s: want success, got %v", capabilityAuthorContextBucket, err)
	}
	if len(keys) == 0 {
		t.Fatalf("bridge KVListKeys %s: want the projected row, got none", capabilityAuthorContextBucket)
	}

	assertDeniedPuts(t, url, capabilityAuthorContextBucket, []string{"bridge", "loupe", "processor", "weaver", "loom"})
}

// TestModelResultsWriteIsolation pins the one-writer rule on the result
// bucket. The runner owns every key there — the in-flight marker that is the
// double-spend guard, the terminal result, and the fleet's daily spend counter
// — so a second writer could forge an answer the bridge then files as a
// proposal, or reset the spend counter to zero.
//
// The bridge is called out by name rather than left to the registry-driven
// loop: it is the component that reads this bucket every poll, which makes it
// the one most likely to acquire a write grant by accident later.
func TestModelResultsWriteIsolation(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)
	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "model-results")

	owner := connectAs(t, url, "model-runner")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := owner.KVPut(ctx, "model-results", "ref-owner-write", []byte(`{"state":"inflight"}`)); err != nil {
		t.Fatalf("model-runner KVPut model-results: want success, got %v", err)
	}

	assertDeniedPuts(t, url, "model-results", []string{"bridge", "loupe", "processor", "refractor", "weaver", "loom"})
}
