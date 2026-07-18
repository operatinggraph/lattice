package objectcrypto

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/vault"
)

// TestDigest_MatchesNATSFormat pins the "SHA-256=<base64url>" wire format (a
// golden value independently computed, since a sensitive upload's digest
// must be directly comparable to a non-sensitive object's NATS-computed one
// — internal/substrate/object_test.go pins the same prefix on that side).
func TestDigest_MatchesNATSFormat(t *testing.T) {
	got := Digest([]byte("hello"))
	if !strings.HasPrefix(got, "SHA-256=") {
		t.Fatalf("digest %q missing SHA-256= prefix", got)
	}
	// echo -n hello | openssl dgst -sha256 -binary | base64 | tr '+/' '-_' | tr -d '='
	want := "SHA-256=LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	if got != want {
		t.Errorf("Digest(%q) = %q, want %q", "hello", got, want)
	}
}

func TestDigest_DifferentBytesDifferentDigest(t *testing.T) {
	a := Digest([]byte("hello"))
	b := Digest([]byte("world"))
	if a == b {
		t.Error("distinct plaintexts produced the same digest")
	}
}

func TestSealOpen_RoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, CEKSize)
	plaintext := []byte("the quick brown fox jumps over the lazy dog")

	aad := []byte("oidABC123")

	nonce, ciphertext, err := Seal(key, plaintext, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext equals plaintext — not encrypted")
	}

	got, err := Open(key, nonce, ciphertext, aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round trip = %q, want %q", got, plaintext)
	}
}

func TestOpen_WrongKey_Fails(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, CEKSize)
	wrongKey := bytes.Repeat([]byte{0x43}, CEKSize)
	aad := []byte("oidABC123")
	nonce, ciphertext, err := Seal(key, []byte("secret"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(wrongKey, nonce, ciphertext, aad); err == nil {
		t.Error("Open with the wrong key must fail (GCM tag check)")
	}
}

func TestOpen_TamperedCiphertext_Fails(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, CEKSize)
	aad := []byte("oidABC123")
	nonce, ciphertext, err := Seal(key, []byte("secret"), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	tampered := append([]byte{}, ciphertext...)
	tampered[0] ^= 0xFF
	if _, err := Open(key, nonce, tampered, aad); err == nil {
		t.Error("Open on tampered ciphertext must fail (GCM tag check)")
	}
}

// TestOpen_WrongAAD_Fails pins the object-binding guarantee: a ciphertext
// sealed under one oid's AAD must not decrypt under a different oid's AAD,
// even with the correct key and nonce — this is what stops a `.content`
// document splice (grafting one object's ciphertext/envelope onto another's
// oid, same governing identity) from silently succeeding.
func TestOpen_WrongAAD_Fails(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, CEKSize)
	nonce, ciphertext, err := Seal(key, []byte("secret"), []byte("oid-A"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(key, nonce, ciphertext, []byte("oid-B")); err == nil {
		t.Error("Open with the wrong AAD (a different object's oid) must fail (GCM tag check)")
	}
}

func TestEncodeDecodeWrappedCEK_RoundTrip(t *testing.T) {
	ct := vault.Ciphertext{
		Nonce: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		CT:    []byte{100, 101, 102, 103, 104},
		KeyID: "vtx.identity.I1",
	}
	encoded := EncodeWrappedCEK(ct)
	if !strings.Contains(encoded, ":") {
		t.Fatalf("EncodeWrappedCEK output %q missing ':' delimiter", encoded)
	}
	decoded, err := DecodeWrappedCEK(encoded)
	if err != nil {
		t.Fatalf("DecodeWrappedCEK: %v", err)
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
		if _, err := DecodeWrappedCEK(s); err == nil {
			t.Errorf("DecodeWrappedCEK(%q) = nil error, want error", s)
		}
	}
}

// TestSeal_BadKeyLength_Fails pins the newAESGCM error branch reached through
// Seal: AES-256-GCM requires a 32-byte key, so a mis-sized CEK must surface an
// error rather than seal under a silently-truncated/invalid key.
func TestSeal_BadKeyLength_Fails(t *testing.T) {
	badKey := bytes.Repeat([]byte{0x42}, 10) // not 16/24/32
	if _, _, err := Seal(badKey, []byte("secret"), []byte("oid")); err == nil {
		t.Error("Seal with an invalid key length must fail")
	}
}

// TestOpen_BadKeyLength_Fails is Open's counterpart to the newAESGCM branch.
func TestOpen_BadKeyLength_Fails(t *testing.T) {
	badKey := bytes.Repeat([]byte{0x42}, 10)
	if _, err := Open(badKey, bytes.Repeat([]byte{0}, 12), []byte("ct"), []byte("oid")); err == nil {
		t.Error("Open with an invalid key length must fail")
	}
}

// TestOpen_BadNonceLength_Fails pins the explicit nonce-length guard: a nonce
// whose length does not match the GCM nonce size is rejected with a clear
// error (not passed into gcm.Open, which would panic).
func TestOpen_BadNonceLength_Fails(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, CEKSize)
	nonce, ciphertext, err := Seal(key, []byte("secret"), []byte("oid"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	shortNonce := nonce[:len(nonce)-1]
	if _, err := Open(key, shortNonce, ciphertext, []byte("oid")); err == nil {
		t.Error("Open with a wrong-length nonce must fail the length guard")
	}
}

func TestGenerateCEK_Size(t *testing.T) {
	cek, err := GenerateCEK()
	if err != nil {
		t.Fatalf("GenerateCEK: %v", err)
	}
	if len(cek) != CEKSize {
		t.Errorf("len(cek) = %d, want %d", len(cek), CEKSize)
	}
}

func TestGenerateCEK_Random(t *testing.T) {
	a, err := GenerateCEK()
	if err != nil {
		t.Fatalf("GenerateCEK: %v", err)
	}
	b, err := GenerateCEK()
	if err != nil {
		t.Fatalf("GenerateCEK: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Error("two GenerateCEK calls produced identical keys")
	}
}
