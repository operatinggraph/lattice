package objectcrypto

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

// newVaultRPCConn stands up an in-process NATS server and returns a
// substrate.Conn wrapping a client connection plus the raw *nats.Conn used to
// register fake Vault responders. WrapKey/UnwrapKey only use point-to-point
// request-reply, so no JetStream data is needed — JetStream is enabled (with a
// per-test StoreDir) purely to match the repo-wide embedded-NATS fixture shape.
func newVaultRPCConn(t *testing.T) (*substrate.Conn, *nats.Conn, context.Context) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
		_ = server.VERSION
	})

	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("substrate.Wrap: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return conn, nc, ctx
}

// sampleEnvelope is a non-zero piiKey Envelope so the round-trip assertions
// exercise a realistic request payload (not a zero value that JSON would omit).
func sampleEnvelope() vault.Envelope {
	return vault.Envelope{
		WrappedDEK: []byte{0xDE, 0xAD, 0xBE, 0xEF},
		KeyID:      "vtx.identity.I1AAAAAAAAAAAAAAAAAA",
		KEKVersion: "kek-v1",
		Alg:        ContentEncryptionAlgo,
	}
}

// TestWrapKey_RoundTrip pins the full WrapKey seam against a fake Vault: the
// request marshals identityKey/envelope/key faithfully and the reply's
// Ciphertext is surfaced unchanged.
func TestWrapKey_RoundTrip(t *testing.T) {
	conn, nc, ctx := newVaultRPCConn(t)

	identityKey := "vtx.identity.I1AAAAAAAAAAAAAAAAAA"
	env := sampleEnvelope()
	cek := bytes.Repeat([]byte{0x11}, CEKSize)
	wantCT := vault.Ciphertext{CT: []byte{9, 8, 7}, Nonce: []byte{1, 2, 3, 4}, KeyID: identityKey}

	sub, err := nc.Subscribe(vault.WrapKeySubject, func(msg *nats.Msg) {
		var req vault.WrapKeyRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			t.Errorf("responder: unmarshal request: %v", err)
			_ = msg.Respond(mustJSON(t, vault.WrapKeyResponse{Error: "bad request"}))
			return
		}
		if req.IdentityKey != identityKey {
			t.Errorf("responder: IdentityKey = %q, want %q", req.IdentityKey, identityKey)
		}
		if !bytes.Equal(req.Key, cek) {
			t.Errorf("responder: Key = %v, want %v", req.Key, cek)
		}
		if req.Envelope.KeyID != env.KeyID || !bytes.Equal(req.Envelope.WrappedDEK, env.WrappedDEK) {
			t.Errorf("responder: Envelope = %+v, want %+v", req.Envelope, env)
		}
		_ = msg.Respond(mustJSON(t, vault.WrapKeyResponse{Ciphertext: wantCT}))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	got, err := WrapKey(ctx, conn, identityKey, env, cek)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	if !bytes.Equal(got.CT, wantCT.CT) || !bytes.Equal(got.Nonce, wantCT.Nonce) || got.KeyID != wantCT.KeyID {
		t.Errorf("WrapKey ciphertext = %+v, want %+v", got, wantCT)
	}
}

// TestUnwrapKey_RoundTrip is the read-side counterpart: the request carries the
// wrapped ciphertext and the reply's Key is surfaced unchanged.
func TestUnwrapKey_RoundTrip(t *testing.T) {
	conn, nc, ctx := newVaultRPCConn(t)

	identityKey := "vtx.identity.I1AAAAAAAAAAAAAAAAAA"
	env := sampleEnvelope()
	wrapped := vault.Ciphertext{CT: []byte{9, 8, 7}, Nonce: []byte{1, 2, 3, 4}, KeyID: identityKey}
	wantKey := bytes.Repeat([]byte{0x22}, CEKSize)

	sub, err := nc.Subscribe(vault.UnwrapKeySubject, func(msg *nats.Msg) {
		var req vault.UnwrapKeyRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			t.Errorf("responder: unmarshal request: %v", err)
			_ = msg.Respond(mustJSON(t, vault.UnwrapKeyResponse{Error: "bad request"}))
			return
		}
		if req.IdentityKey != identityKey {
			t.Errorf("responder: IdentityKey = %q, want %q", req.IdentityKey, identityKey)
		}
		if !bytes.Equal(req.Wrapped.CT, wrapped.CT) || !bytes.Equal(req.Wrapped.Nonce, wrapped.Nonce) {
			t.Errorf("responder: Wrapped = %+v, want %+v", req.Wrapped, wrapped)
		}
		_ = msg.Respond(mustJSON(t, vault.UnwrapKeyResponse{Key: wantKey}))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	got, err := UnwrapKey(ctx, conn, identityKey, env, wrapped)
	if err != nil {
		t.Fatalf("UnwrapKey: %v", err)
	}
	if !bytes.Equal(got, wantKey) {
		t.Errorf("UnwrapKey = %v, want %v", got, wantKey)
	}
}

// TestWrapKey_VaultErrorSurfaced pins the security-relevant branch: a Vault
// that refuses the wrap (resp.Error set) must propagate that error, never
// silently return an empty/zero Ciphertext the caller might treat as success.
func TestWrapKey_VaultErrorSurfaced(t *testing.T) {
	conn, nc, ctx := newVaultRPCConn(t)

	const vaultErr = "wrapKey: identity not authorized"
	sub, err := nc.Subscribe(vault.WrapKeySubject, func(msg *nats.Msg) {
		_ = msg.Respond(mustJSON(t, vault.WrapKeyResponse{Error: vaultErr}))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	got, err := WrapKey(ctx, conn, "vtx.identity.I1AAAAAAAAAAAAAAAAAA", sampleEnvelope(), bytes.Repeat([]byte{0x11}, CEKSize))
	if err == nil {
		t.Fatalf("WrapKey returned nil error on a Vault error reply; got ciphertext %+v", got)
	}
	if err.Error() != vaultErr {
		t.Errorf("WrapKey error = %q, want %q", err.Error(), vaultErr)
	}
}

// TestUnwrapKey_VaultErrorSurfaced is UnwrapKey's counterpart — a refused
// unwrap must propagate, not return a zero-length key.
func TestUnwrapKey_VaultErrorSurfaced(t *testing.T) {
	conn, nc, ctx := newVaultRPCConn(t)

	const vaultErr = "unwrapKey: shredded"
	sub, err := nc.Subscribe(vault.UnwrapKeySubject, func(msg *nats.Msg) {
		_ = msg.Respond(mustJSON(t, vault.UnwrapKeyResponse{Error: vaultErr}))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	got, err := UnwrapKey(ctx, conn, "vtx.identity.I1AAAAAAAAAAAAAAAAAA", sampleEnvelope(), vault.Ciphertext{CT: []byte{1}, Nonce: []byte{2}})
	if err == nil {
		t.Fatalf("UnwrapKey returned nil error on a Vault error reply; got key %v", got)
	}
	if err.Error() != vaultErr {
		t.Errorf("UnwrapKey error = %q, want %q", err.Error(), vaultErr)
	}
}

// TestWrapKey_TransportError pins the no-responder path: with no Vault serving
// the subject the RPC times out and the error is surfaced (a fast context so
// the test does not wait on NATS's default 10s request timeout).
func TestWrapKey_TransportError(t *testing.T) {
	conn, _, _ := newVaultRPCConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if _, err := WrapKey(ctx, conn, "vtx.identity.I1AAAAAAAAAAAAAAAAAA", sampleEnvelope(), bytes.Repeat([]byte{0x11}, CEKSize)); err == nil {
		t.Fatal("WrapKey with no Vault responder must fail")
	}
}

// TestUnwrapKey_TransportError is UnwrapKey's no-responder counterpart.
func TestUnwrapKey_TransportError(t *testing.T) {
	conn, _, _ := newVaultRPCConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if _, err := UnwrapKey(ctx, conn, "vtx.identity.I1AAAAAAAAAAAAAAAAAA", sampleEnvelope(), vault.Ciphertext{CT: []byte{1}, Nonce: []byte{2}}); err == nil {
		t.Fatal("UnwrapKey with no Vault responder must fail")
	}
}

// TestWrapKey_MalformedReply pins the parse-error branch: a reply that is not a
// valid WrapKeyResponse must be rejected, not misread as a success.
func TestWrapKey_MalformedReply(t *testing.T) {
	conn, nc, ctx := newVaultRPCConn(t)

	sub, err := nc.Subscribe(vault.WrapKeySubject, func(msg *nats.Msg) {
		_ = msg.Respond([]byte("this is not json"))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	if _, err := WrapKey(ctx, conn, "vtx.identity.I1AAAAAAAAAAAAAAAAAA", sampleEnvelope(), bytes.Repeat([]byte{0x11}, CEKSize)); err == nil {
		t.Fatal("WrapKey with a malformed reply must fail")
	}
}

// TestUnwrapKey_MalformedReply is UnwrapKey's parse-error counterpart.
func TestUnwrapKey_MalformedReply(t *testing.T) {
	conn, nc, ctx := newVaultRPCConn(t)

	sub, err := nc.Subscribe(vault.UnwrapKeySubject, func(msg *nats.Msg) {
		_ = msg.Respond([]byte("{not-json"))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	if _, err := UnwrapKey(ctx, conn, "vtx.identity.I1AAAAAAAAAAAAAAAAAA", sampleEnvelope(), vault.Ciphertext{CT: []byte{1}, Nonce: []byte{2}}); err == nil {
		t.Fatal("UnwrapKey with a malformed reply must fail")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	return b
}
