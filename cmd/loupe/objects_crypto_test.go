package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/vault"
)

// TestSHA256Digest_MatchesNATSFormat pins the "SHA-256=<base64url>" wire
// format (a golden value independently computed, since a sensitive upload's
// digest must be directly comparable to a non-sensitive object's
// NATS-computed one — internal/substrate/object_test.go pins the same prefix
// on that side).
func TestSHA256Digest_MatchesNATSFormat(t *testing.T) {
	got := sha256Digest([]byte("hello"))
	if !strings.HasPrefix(got, "SHA-256=") {
		t.Fatalf("digest %q missing SHA-256= prefix", got)
	}
	// echo -n hello | openssl dgst -sha256 -binary | base64 | tr '+/' '-_' | tr -d '='
	want := "SHA-256=LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	if got != want {
		t.Errorf("sha256Digest(%q) = %q, want %q", "hello", got, want)
	}
}

func TestSHA256Digest_DifferentBytesDifferentDigest(t *testing.T) {
	a := sha256Digest([]byte("hello"))
	b := sha256Digest([]byte("world"))
	if a == b {
		t.Error("distinct plaintexts produced the same digest")
	}
}

func TestSealOpenAESGCM_RoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, cekSize)
	plaintext := []byte("the quick brown fox jumps over the lazy dog")

	aad := []byte("oidABC123")

	nonce, ciphertext, err := sealAESGCM(key, plaintext, aad)
	if err != nil {
		t.Fatalf("sealAESGCM: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext equals plaintext — not encrypted")
	}

	got, err := openAESGCM(key, nonce, ciphertext, aad)
	if err != nil {
		t.Fatalf("openAESGCM: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round trip = %q, want %q", got, plaintext)
	}
}

func TestOpenAESGCM_WrongKey_Fails(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, cekSize)
	wrongKey := bytes.Repeat([]byte{0x43}, cekSize)
	aad := []byte("oidABC123")
	nonce, ciphertext, err := sealAESGCM(key, []byte("secret"), aad)
	if err != nil {
		t.Fatalf("sealAESGCM: %v", err)
	}
	if _, err := openAESGCM(wrongKey, nonce, ciphertext, aad); err == nil {
		t.Error("openAESGCM with the wrong key must fail (GCM tag check)")
	}
}

func TestOpenAESGCM_TamperedCiphertext_Fails(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, cekSize)
	aad := []byte("oidABC123")
	nonce, ciphertext, err := sealAESGCM(key, []byte("secret"), aad)
	if err != nil {
		t.Fatalf("sealAESGCM: %v", err)
	}
	tampered := append([]byte{}, ciphertext...)
	tampered[0] ^= 0xFF
	if _, err := openAESGCM(key, nonce, tampered, aad); err == nil {
		t.Error("openAESGCM on tampered ciphertext must fail (GCM tag check)")
	}
}

// TestOpenAESGCM_WrongAAD_Fails pins the object-binding guarantee: a
// ciphertext sealed under one oid's AAD must not decrypt under a different
// oid's AAD, even with the correct key and nonce — this is what stops a
// `.content` document splice (grafting one object's ciphertext/envelope onto
// another's oid, same governing identity) from silently succeeding.
func TestOpenAESGCM_WrongAAD_Fails(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, cekSize)
	nonce, ciphertext, err := sealAESGCM(key, []byte("secret"), []byte("oid-A"))
	if err != nil {
		t.Fatalf("sealAESGCM: %v", err)
	}
	if _, err := openAESGCM(key, nonce, ciphertext, []byte("oid-B")); err == nil {
		t.Error("openAESGCM with the wrong AAD (a different object's oid) must fail (GCM tag check)")
	}
}

func TestEncodeDecodeWrappedCEK_RoundTrip(t *testing.T) {
	ct := vault.Ciphertext{
		Nonce: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		CT:    []byte{100, 101, 102, 103, 104},
		KeyID: "vtx.identity.I1",
	}
	encoded := encodeWrappedCEK(ct)
	if !strings.Contains(encoded, ":") {
		t.Fatalf("encodeWrappedCEK output %q missing ':' delimiter", encoded)
	}
	decoded, err := decodeWrappedCEK(encoded)
	if err != nil {
		t.Fatalf("decodeWrappedCEK: %v", err)
	}
	if !bytes.Equal(decoded.Nonce, ct.Nonce) {
		t.Errorf("nonce = %v, want %v", decoded.Nonce, ct.Nonce)
	}
	if !bytes.Equal(decoded.CT, ct.CT) {
		t.Errorf("CT = %v, want %v", decoded.CT, ct.CT)
	}
}

func TestDecodeWrappedCEK_Malformed_Rejected(t *testing.T) {
	bad := []string{
		"",                  // no delimiter
		"nodelimiterhere",   // no delimiter
		"!!!:validbase64==", // invalid base64 before the delimiter
		"validbase64==:!!!", // invalid base64 after the delimiter
	}
	for _, s := range bad {
		if _, err := decodeWrappedCEK(s); err == nil {
			t.Errorf("decodeWrappedCEK(%q) = nil error, want error", s)
		}
	}
}

func TestGenerateCEK_Size(t *testing.T) {
	cek, err := generateCEK()
	if err != nil {
		t.Fatalf("generateCEK: %v", err)
	}
	if len(cek) != cekSize {
		t.Errorf("len(cek) = %d, want %d", len(cek), cekSize)
	}
}

func TestGenerateCEK_Random(t *testing.T) {
	a, err := generateCEK()
	if err != nil {
		t.Fatalf("generateCEK: %v", err)
	}
	b, err := generateCEK()
	if err != nil {
		t.Fatalf("generateCEK: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Error("two generateCEK calls produced identical keys")
	}
}
