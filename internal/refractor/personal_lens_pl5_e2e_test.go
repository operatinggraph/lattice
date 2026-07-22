// Package refractor_test — end-to-end proof for personal-secure-lens-design.md
// Fire 5 (PL.5): the Edge ciphertext-delta + transient-key path (§3.6). The
// cloud never decrypts for the Edge — a sensitive aspect's ciphertext
// envelope flows through the Personal Lens exactly as Core KV stores it, with
// the delta envelope's `encrypted` flag telling the Edge it needs a Vault
// transient session key before it can read the field. This covers Gate-3
// vector 5 (personal-secure-lens-design.md §5): a shredded identity's
// IssueSessionKey call is denied, so its ciphertext deltas can never be
// freshly decrypted again. Reuses pl2Harness/activatePersonalLens/
// writePL2Vertex/writePL2Link/pl2NanoID (same package — PL.5 builds on the
// identical fan-out fixtures PL.2/PL.3/PL.4 proved).
package refractor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// TestPersonalLens_PL5_E2E_CiphertextFieldForwardedEncrypted proves the
// blind-projection half of Fire 5: a lease field carrying a Vault
// ciphertext-envelope shape (as Core KV stores a sensitive aspect's data)
// reaches the recipient's SYNC delta with encrypted:true, forwarded
// byte-for-byte — the Personal Lens never decodes or decrypts it.
func TestPersonalLens_PL5_E2E_CiphertextFieldForwardedEncrypted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("pl5-ciphertext-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("pl5-ciphertext-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)

	cypher := `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.ssn AS ssn`
	_, _ = activatePersonalLens(t, h, pl2NanoID("pl5-ciphertext-lens"), cypher, []string{"entityId"}, nil)

	drainCons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	// ssn's shape mirrors exactly what step 6.5's encrypt-on-write produces —
	// a vault.Ciphertext JSON envelope — but this fixture writes it directly
	// (no live Vault/Processor in this ephemeral-NATS test), since the object
	// under test is the Personal Lens's FORWARDING of that shape, not its
	// production.
	ciphertextEnvelope := map[string]any{"ct": "c2VhbGVkLWJ5dGVz", "nonce": "b25jZS1ieXRlcw==", "keyId": identityKey}
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl5-1", "ssn": ciphertextEnvelope})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	msg, err := drainCons.Next(jetstream.FetchMaxWait(10 * time.Second))
	require.NoError(t, err, "the lease write must fan out to the recipient's subject")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, "upsert", env["op"])
	require.Equal(t, true, env["encrypted"], "a ciphertext-shaped field must set encrypted:true")
	data, ok := env["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, ciphertextEnvelope, data["ssn"], "the ciphertext envelope must forward unchanged")
}

// TestPersonalLens_PL5_E2E_ShreddedIdentitySessionKeyDenied is Gate-3 vector 5
// (personal-secure-lens-design.md §5): once an identity is shredded, the
// Vault's transient session-key RPC — the ONLY way an Edge can decrypt that
// identity's already-delivered ciphertext deltas — must refuse, so those
// deltas stay permanently unreadable ("remote shredding renders all local
// copies permanent gibberish", Edge Lattice.md §5). Runs on the same
// in-process NATS connection the personal-lens fan-out fixtures use, proving
// the Vault RPC composes on the identical substrate.
func TestPersonalLens_PL5_E2E_ShreddedIdentitySessionKeyDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	svcCtx, svcCancel := context.WithCancel(h.ctx)
	t.Cleanup(svcCancel)
	require.NoError(t, svc.StartNATSListener(svcCtx, h.conn.NATS()))

	identityKey := "identity-pl5-shred"
	env, err := backend.CreateIdentityKey(h.ctx, identityKey)
	require.NoError(t, err)

	// Before shredding, the Edge can obtain a session key.
	reqData, err := json.Marshal(vault.IssueSessionKeyRequest{IdentityKey: identityKey, Envelope: env, TTLSeconds: 60})
	require.NoError(t, err)
	reply, err := h.conn.NATS().Request(vault.IssueSessionKeySubject, reqData, 5*time.Second)
	require.NoError(t, err)
	var resp vault.IssueSessionKeyResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.Empty(t, resp.Error)
	require.NotEmpty(t, resp.Key)

	// After shredding, IssueSessionKey must deny every subsequent request —
	// the Edge can never freshly decrypt this identity's deltas again.
	require.NoError(t, backend.ShredKey(h.ctx, identityKey))
	reply, err = h.conn.NATS().Request(vault.IssueSessionKeySubject, reqData, 5*time.Second)
	require.NoError(t, err)
	var deniedResp vault.IssueSessionKeyResponse
	require.NoError(t, json.Unmarshal(reply.Data, &deniedResp))
	require.NotEmpty(t, deniedResp.Error)
	require.Empty(t, deniedResp.Key)
}
