package natsperm

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// TestBridgeVaultDecryptReachability proves the egress-unwrap transport gate
// (sensitive-param-egress-design.md §3.5/§For-Andrew #1): the bridge is now a
// named trusted plaintext consumer of lattice.vault.decrypt, alongside Loupe —
// the mirror of TestVaultDecryptReachability with "bridge" in place of "loupe".
func TestBridgeVaultDecryptReachability(t *testing.T) {
	url := startServerFromConf(t)

	resp := connectAs(t, url, "processor")
	sub, err := resp.NATS().Subscribe(vaultDecryptSubject, func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"plaintext":"b2s="}`))
	})
	if err != nil {
		t.Fatalf("processor subscribe %q: %v", vaultDecryptSubject, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := resp.NATS().Flush(); err != nil {
		t.Fatalf("flush responder: %v", err)
	}

	bridge := connectAs(t, url, "bridge")
	reply, err := bridge.NATS().Request(vaultDecryptSubject, []byte(`{"identityKey":"vtx.identity.x"}`), 3*time.Second)
	if err != nil {
		t.Fatalf("bridge request %q: want reply, got %v", vaultDecryptSubject, err)
	}
	if len(reply.Data) == 0 {
		t.Fatalf("bridge request %q: empty reply", vaultDecryptSubject)
	}
}

// TestBridgeCoreKVReadIsolation proves the read-side half of the egress grant
// (design §For-Andrew #1 / §8, adversarial finding B2): the bridge's new
// lattice.vault.decrypt grant is paired with a deny on the core-kv backing
// stream's DIRECT.GET / STREAM.MSG.GET subjects, so a compromised bridge
// cannot use the broad $JS.API.> grant every component holds to read the
// core-kv corpus directly — narrowing its reachable read set to the ONE lens
// bucket its egress unwrap actually needs. The processor (the legitimate
// core-kv owner) is the positive control proving the bucket/key exist and
// reads work in general, so the bridge's failure is permission-based, not
// bucket-absence.
func TestBridgeCoreKVReadIsolation(t *testing.T) {
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, "core-kv")
	provision(t, boot, "privacy-pii-key-envelopes")

	proc := connectAs(t, url, "processor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := proc.KVPut(ctx, "core-kv", "vtx.identity.x.ssn", []byte(`{"ct":"x"}`)); err != nil {
		t.Fatalf("processor KVPut core-kv: want success, got %v", err)
	}
	// The envelope lens bucket is refractor-written (the sole projector), not
	// processor-written — mirroring how a real piiKeyEnvelope row lands.
	ref := connectAs(t, url, "refractor")
	if _, err := ref.KVPut(ctx, "privacy-pii-key-envelopes", "vtx.identity.x", []byte(`{"wrappedDEK":"x"}`)); err != nil {
		t.Fatalf("refractor KVPut privacy-pii-key-envelopes: want success, got %v", err)
	}

	// Owner read succeeds — proves the key exists and reads work at all.
	if _, err := proc.KVGet(ctx, "core-kv", "vtx.identity.x.ssn"); err != nil {
		t.Fatalf("processor KVGet core-kv: want success, got %v", err)
	}

	bridge := connectAs(t, url, "bridge")
	rctx, rcancel := context.WithTimeout(context.Background(), deniedTimeout)
	defer rcancel()
	if _, err := bridge.KVGet(rctx, "core-kv", "vtx.identity.x.ssn"); err == nil {
		t.Error("bridge KVGet core-kv: want transport denial, got success")
	}

	// The one lens bucket the egress unwrap actually needs stays reachable —
	// the deny is scoped to core-kv's backing stream only, not a blanket
	// read lockout.
	octx, ocancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ocancel()
	if _, err := bridge.KVGet(octx, "privacy-pii-key-envelopes", "vtx.identity.x"); err != nil {
		t.Fatalf("bridge KVGet privacy-pii-key-envelopes: want success, got %v", err)
	}
}

// TestBridgeCoreKVReadIsolation_DirectGetBareSubject proves the bare-subject
// half of the read-deny (adversarial review finding, this fire): NATS' `>`
// wildcard requires at least one token AFTER the prefix it matches, so a deny
// on "$JS.API.DIRECT.GET.KV_core-kv.>" alone does NOT cover the BARE subject
// "$JS.API.DIRECT.GET.KV_core-kv" — the exact subject nats.go's
// direct-get-by-sequence (KeyValue.GetRevision) publishes to with no
// subject-suffix. Without this literal deny, a bridge could sequence-walk the
// whole core-kv corpus even though the ordinary key-based KVGet path (the
// sibling test above) is correctly denied — the sibling test would then pass
// for the wrong reason (it never exercises this subject shape).
func TestBridgeCoreKVReadIsolation_DirectGetBareSubject(t *testing.T) {
	url := startServerFromConf(t)
	assertDeniedPublish(t, url, "$JS.API.DIRECT.GET.KV_core-kv", []string{"bridge"})
}
