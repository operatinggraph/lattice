package vault

import (
	"encoding/binary"
)

// RefMACPurpose is the frozen Vault.MAC purpose label for a sensitive-ref
// marker's provenance stamp (design sensitive-ref-mac-provenance-design.md
// §3.1/§3.2). Both the mint site (the Processor's egress hydration) and the
// verify site (DecryptRefSubject's responder) derive their MAC key from this
// same purpose, so they land on the same key.
const RefMACPurpose = "sensitive-ref/v1"

// RefMACInput builds the canonical byte string a sensitive-ref marker's MAC
// is computed over: length-prefixed ref, requestId, and the ciphertext's
// keyId/nonce/ct fields — never their JSON re-serialization, which is not
// canonical (design §3.2). Mint and verify must call this with byte-identical
// inputs (ref, requestId, and ct's raw decoded bytes, never the base64-string
// generic-map shape) or the MAC silently diverges.
func RefMACInput(ref, requestID string, ct Ciphertext) []byte {
	size := 4 + len(ref) + 4 + len(requestID) + 4 + len(ct.KeyID) + 4 + len(ct.Nonce) + 4 + len(ct.CT)
	buf := make([]byte, 0, size)
	buf = appendLengthPrefixed(buf, []byte(ref))
	buf = appendLengthPrefixed(buf, []byte(requestID))
	buf = appendLengthPrefixed(buf, []byte(ct.KeyID))
	buf = appendLengthPrefixed(buf, ct.Nonce)
	buf = appendLengthPrefixed(buf, ct.CT)
	return buf
}

// appendLengthPrefixed appends a big-endian uint32 length prefix followed by
// b, making the concatenation of several fields unambiguous regardless of
// their individual contents (design §3.2's "never JSON" requirement — JSON
// re-serialization is not canonical since the bridge re-parses the marker
// before forwarding).
func appendLengthPrefixed(buf, b []byte) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	buf = append(buf, lenBuf[:]...)
	return append(buf, b...)
}
