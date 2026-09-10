package natsperm

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// vaultDecryptSubject is the trusted-tool PII decrypt RPC subject
// (internal/vault.DecryptSubject). Hardcoded here — like the bucket names in
// the other conformance tests — so the assertion is against the committed
// deploy/nats-server.conf, not a shared Go constant.
const vaultDecryptSubject = "lattice.vault.decrypt"

// TestVaultDecryptReachability proves the transport gate for the trusted-tool
// PII decrypt RPC (vault-crypto-shredding-design.md §2.3; Loupe F12 Reveal).
// The responder does NO caller-level authorization (its refusals — a shredded
// holder, a holder that is not an identity — are about the record, never the
// caller), so this publish allow-list IS the boundary: Loupe — a named trusted
// plaintext consumer — may reach lattice.vault.decrypt, while an ordinary
// vertical app may not.
//
// The Processor hosts the responder in production (it holds the authoritative
// Vault). Here a processor-seed connection stands in as the responder:
// subscribe is unrestricted under the write-only-restriction model, and it
// replies over _INBOX.> (which the processor's publish allow-list carries).
func TestVaultDecryptReachability(t *testing.T) {
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

	// Loupe is authorized to publish the request — it gets a reply promptly.
	loupe := connectAs(t, url, "loupe")
	reply, err := loupe.NATS().Request(vaultDecryptSubject, []byte(`{"identityKey":"vtx.identity.x"}`), 3*time.Second)
	if err != nil {
		t.Fatalf("loupe request %q: want reply, got %v", vaultDecryptSubject, err)
	}
	if len(reply.Data) == 0 {
		t.Fatalf("loupe request %q: empty reply", vaultDecryptSubject)
	}

	// An ordinary vertical app is NOT authorized: its publish is rejected at the
	// transport, so the request never reaches the responder and the call times
	// out (the denied-publish signal for a plain request — no reply ever comes).
	rogue := connectAs(t, url, "clinic-app")
	rctx, rcancel := context.WithTimeout(context.Background(), deniedTimeout)
	defer rcancel()
	if _, err := rogue.NATS().RequestWithContext(rctx, vaultDecryptSubject, []byte(`{"identityKey":"vtx.identity.x"}`)); err == nil {
		t.Errorf("clinic-app request %q: want transport denial (timeout), got a reply", vaultDecryptSubject)
	}
}
