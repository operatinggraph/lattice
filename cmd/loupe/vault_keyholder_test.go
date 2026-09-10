package main

// Reveal resolves a sensitive aspect's key holder from the ciphertext's own
// keyId and then holds that holder to the Reveal rule (Contract #3 §3.10): a
// decrypt carrying no actor and no declared purpose opens identity-custodied
// records only. The fixture anchors the record on an appointment — a vertex
// with no key of its own — and seals it under a retention-class holder, so the
// refusal can name that holder only if the handler read it off the ciphertext;
// a handler still deriving custody from the aspect key could not.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/vault"
)

func TestVaultDecrypt_RetentionClassHolder_Refused(t *testing.T) {
	hs, backend, conn := vaultDecryptFixture(t)
	ctx := context.Background()

	const holderKey = "vtx.retentionclass.LoupeCLassHoLderAAAA"
	const aspectKey = "vtx.appointment.LoupeApptAnchorAAAAA.encounter"
	putSensitiveAspect(t, ctx, conn, backend, holderKey, aspectKey, []byte(`{"value":"chart note"}`))

	res, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"`+aspectKey+`"}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", res.StatusCode, raw)
	}
	var body struct {
		Error     string          `json:"error"`
		Plaintext json.RawMessage `json:"plaintext"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Plaintext) != 0 {
		t.Fatalf("plaintext = %s, want none — a retained record is never revealed here", body.Plaintext)
	}
	if !strings.Contains(body.Error, holderKey) || !strings.Contains(body.Error, "(a retentionclass holder") {
		t.Fatalf("error = %q, want it to name the retention-class holder read off the ciphertext", body.Error)
	}
}

// TestVaultDecrypt_SurfacesTheRPCRefusal drives the Vault RPC's own refusal
// through Loupe's reply handling: a responder that answers ErrRevealDenied for
// an identity holder — the one kind Loupe's own check admits, so the request
// actually goes out — surfaces as 403 with the reason, never as a generic
// gateway failure.
func TestVaultDecrypt_SurfacesTheRPCRefusal(t *testing.T) {
	hs, backend, conn := newVaultDecryptFixture(t, false)
	ctx := context.Background()

	const holderKey = "vtx.identity.LoupeRefusedAAAAAAAA"
	const aspectKey = holderKey + ".ssn"
	putSensitiveAspect(t, ctx, conn, backend, holderKey, aspectKey, []byte(`{"value":"123-45-6789"}`))
	sub, err := conn.NATS().Subscribe(vault.DecryptSubject, func(msg *nats.Msg) {
		reply, _ := json.Marshal(vault.DecryptResponse{Error: vault.ErrRevealDenied.Error()})
		_ = msg.Respond(reply)
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	res, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"`+aspectKey+`"}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", res.StatusCode, raw)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.Error, vault.ErrRevealDenied.Error()) || !strings.Contains(body.Error, aspectKey) {
		t.Fatalf("error = %q, want the RPC's refusal, naming the aspect", body.Error)
	}
}
