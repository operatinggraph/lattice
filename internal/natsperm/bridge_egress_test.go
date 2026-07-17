package natsperm

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// vaultDecryptRefSubject is the ref-verified decrypt RPC subject
// (internal/vault.DecryptRefSubject). Hardcoded here — like vaultDecryptSubject
// in the sibling file — so the assertion is against the committed
// deploy/nats-server.conf, not a shared Go constant.
const vaultDecryptRefSubject = "lattice.vault.decryptref"

// TestBridgeVaultDecryptRefReachability proves the Fire 2 grant swap (design
// sensitive-ref-mac-provenance-design.md §3.3/§8): the bridge is now a named
// trusted caller of the ref-verified lattice.vault.decryptref RPC — the
// mirror of TestVaultDecryptReachability with "bridge"/"decryptref" in place
// of "loupe"/"decrypt".
func TestBridgeVaultDecryptRefReachability(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	resp := connectAs(t, url, "processor")
	sub, err := resp.NATS().Subscribe(vaultDecryptRefSubject, func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"plaintext":"b2s="}`))
	})
	if err != nil {
		t.Fatalf("processor subscribe %q: %v", vaultDecryptRefSubject, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := resp.NATS().Flush(); err != nil {
		t.Fatalf("flush responder: %v", err)
	}

	bridge := connectAs(t, url, "bridge")
	reply, err := bridge.NATS().Request(vaultDecryptRefSubject, []byte(`{"ref":"vtx.identity.x.ssn"}`), 3*time.Second)
	if err != nil {
		t.Fatalf("bridge request %q: want reply, got %v", vaultDecryptRefSubject, err)
	}
	if len(reply.Data) == 0 {
		t.Fatalf("bridge request %q: empty reply", vaultDecryptRefSubject)
	}
}

// TestBridgeVaultDecryptWholesaleDenied proves the OLD grant does not linger
// (design §3.3: "a new DENY vector — the old grant must not linger"): the
// bridge's decrypt authority shrank to exactly lattice.vault.decryptref, so a
// publish to the wholesale lattice.vault.decrypt (still granted to Loupe,
// TestVaultDecryptReachability) must now be rejected at the transport.
func TestBridgeVaultDecryptWholesaleDenied(t *testing.T) {
	t.Parallel()
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
	ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
	defer cancel()
	if _, err := bridge.NATS().RequestWithContext(ctx, vaultDecryptSubject, []byte(`{"identityKey":"vtx.identity.x"}`)); err == nil {
		t.Errorf("bridge request %q: want transport denial (timeout), got a reply", vaultDecryptSubject)
	}
}

// TestVaultDecryptRefAppsDenied proves the third vector design §3.3 names
// ("apps denied on both"): an ordinary vertical app holds neither the
// wholesale lattice.vault.decrypt grant (Loupe-only) nor the new ref-verified
// lattice.vault.decryptref grant (bridge-only) — completing the vector table
// alongside TestBridgeVaultDecryptRefReachability (bridge allowed) and
// TestBridgeVaultDecryptWholesaleDenied (bridge denied on the old subject).
func TestVaultDecryptRefAppsDenied(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)
	assertDeniedPublish(t, url, vaultDecryptRefSubject, []string{"clinic-app", "loftspace-app"})
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
	t.Parallel()
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
	t.Parallel()
	url := startServerFromConf(t)
	assertDeniedPublish(t, url, "$JS.API.DIRECT.GET.KV_core-kv", []string{"bridge"})
}
