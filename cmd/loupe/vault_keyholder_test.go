package main

// Reveal resolves a sensitive aspect's key holder from the ciphertext's own
// keyId, so it follows a record whose DEK a retention class holds as readily
// as one held by the anchoring identity. The fixture below anchors on an
// appointment — a vertex with no key of its own — so a handler still deriving
// custody from the aspect key could not decrypt it.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestVaultDecrypt_RetentionClassHolder(t *testing.T) {
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
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	var body struct {
		Plaintext json.RawMessage `json:"plaintext"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(body.Plaintext) != `{"value":"chart note"}` {
		t.Fatalf("plaintext = %s, want the class-held record's plaintext", body.Plaintext)
	}
}
