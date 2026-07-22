package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
)

// shredRow is one shredded identity's row in the GET /api/vault/shreds reply
// — the shredStatus lens's per-identity finalization ledger
// (packages/privacy-base Lenses(): shredded/vaultKeyDestroyed/
// projectionsNullified each flip false→true as the async finalization
// listeners record their step). The Vault component page's shred-status
// summary (loupe-platform-edges-ux.md §3.1) is this bucket's fleet view.
type shredRow struct {
	IdentityKey            string `json:"identityKey"`
	Shredded               bool   `json:"shredded"`
	ShreddedAt             string `json:"shreddedAt,omitempty"`
	VaultKeyDestroyed      bool   `json:"vaultKeyDestroyed"`
	VaultKeyDestroyedAt    string `json:"vaultKeyDestroyedAt,omitempty"`
	ProjectionsNullified   bool   `json:"projectionsNullified"`
	ProjectionsNullifiedAt string `json:"projectionsNullifiedAt,omitempty"`
}

// handleVaultShreds implements GET /api/vault/shreds: the shredStatus lens's
// privacy-shreds bucket rows, read straight off the bucket (P5 — a lens
// target, not Core KV, the ordinary KVListKeys/KVGet read every P5 consumer
// uses). A row whose doc fails to parse still lists under its bucket key (the
// key alone names a shredded identity), and a key removed between list and
// get drops rather than failing the whole page.
func (s *server) handleVaultShreds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, privacybase.ShredStatusBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list "+privacybase.ShredStatusBucket+": "+err.Error())
		return
	}
	rows := make([]shredRow, 0, len(keys))
	for _, k := range keys {
		entry, err := conn.KVGet(ctx, privacybase.ShredStatusBucket, k)
		if errors.Is(err, substrate.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "get "+k+": "+err.Error())
			return
		}
		row := shredRow{IdentityKey: k}
		var doc struct {
			Shredded               bool   `json:"shredded"`
			ShreddedAt             string `json:"shreddedAt"`
			VaultKeyDestroyed      bool   `json:"vaultKeyDestroyed"`
			VaultKeyDestroyedAt    string `json:"vaultKeyDestroyedAt"`
			ProjectionsNullified   bool   `json:"projectionsNullified"`
			ProjectionsNullifiedAt string `json:"projectionsNullifiedAt"`
		}
		if json.Unmarshal(entry.Value, &doc) == nil {
			row.Shredded = doc.Shredded
			row.ShreddedAt = doc.ShreddedAt
			row.VaultKeyDestroyed = doc.VaultKeyDestroyed
			row.VaultKeyDestroyedAt = doc.VaultKeyDestroyedAt
			row.ProjectionsNullified = doc.ProjectionsNullified
			row.ProjectionsNullifiedAt = doc.ProjectionsNullifiedAt
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].IdentityKey < rows[j].IdentityKey })
	s.writeJSON(w, http.StatusOK, map[string]any{"shreds": rows, "count": len(rows)})
}

// vaultDecryptRequest is the POST /api/vault/decrypt body: the caller names
// the sensitive aspect to reveal by key; Loupe re-reads both the ciphertext
// and the anchoring identity's wrapped-DEK envelope server-side rather than
// trusting client-supplied crypto material (loupe-platform-edges-ux.md §3.2).
type vaultDecryptRequest struct {
	AspectKey string `json:"aspectKey"`
}

// aspectCiphertext extracts the Contract #3 §3.10 { ct, nonce, keyId }
// envelope from a sensitive aspect's stored data, mirroring
// internal/refractor/pipeline's ciphertextFromMap (and the JS mirror,
// logic/sensitive.js's isSealedAspect). It rejects a plaintext or malformed
// aspect (any of the three fields empty) rather than forwarding a partial
// envelope to the Vault RPC.
func aspectCiphertext(data map[string]any) (vault.Ciphertext, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return vault.Ciphertext{}, errors.New("marshal aspect data: " + err.Error())
	}
	var ct vault.Ciphertext
	if err := json.Unmarshal(raw, &ct); err != nil {
		return vault.Ciphertext{}, errors.New("parse ciphertext envelope: " + err.Error())
	}
	if len(ct.CT) == 0 || len(ct.Nonce) == 0 || ct.KeyID == "" {
		return vault.Ciphertext{}, errors.New("not a sensitive aspect (no ciphertext envelope)")
	}
	return ct, nil
}

// handleVaultDecrypt implements POST /api/vault/decrypt — Reveal (Signature
// #1, loupe-platform-edges-ux.md §3.2). Loupe is a named trusted plaintext
// consumer of the lattice.vault.decrypt RPC; this handler proxies a single
// aspect's decrypt, never batches, and never writes (P2 intact). A shredded
// identity's key reports {"shredded":true} rather than an error, so the UI
// can render "permanently unreadable" instead of a generic failure.
func (s *server) handleVaultDecrypt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	if s.crossOriginBlocked(w, r) {
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req vaultDecryptRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}
	if classifyKey(req.AspectKey) != classAspect {
		s.writeError(w, http.StatusBadRequest, "aspectKey must be a 4-segment vtx.<type>.<id>.<localName> aspect key")
		return
	}
	segs := strings.SplitN(req.AspectKey, ".", 4)
	identityKey := strings.Join(segs[:3], ".")
	if vertexType(identityKey) != "identity" {
		s.writeError(w, http.StatusBadRequest,
			req.AspectKey+" does not anchor to an identity vertex — a sensitive aspect's DEK is always custodied by its anchoring identity (Contract #1 §1.6)")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	aspectEntry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, req.AspectKey)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "get "+req.AspectKey+": "+err.Error())
		return
	}
	var aspectDoc struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(aspectEntry.Value, &aspectDoc); err != nil {
		s.writeError(w, http.StatusBadGateway, "parse "+req.AspectKey+": "+err.Error())
		return
	}
	ct, err := aspectCiphertext(aspectDoc.Data)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, req.AspectKey+": "+err.Error())
		return
	}

	piiEntry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, identityKey+".piiKey")
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			s.writeError(w, http.StatusBadGateway, identityKey+" has a sensitive aspect but no piiKey — key custody invariant violated")
			return
		}
		s.writeError(w, http.StatusBadGateway, "get "+identityKey+".piiKey: "+err.Error())
		return
	}
	var piiDoc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(piiEntry.Value, &piiDoc); err != nil {
		s.writeError(w, http.StatusBadGateway, "parse "+identityKey+".piiKey: "+err.Error())
		return
	}

	reqBody, err := json.Marshal(vault.DecryptRequest{IdentityKey: identityKey, Envelope: piiDoc.Data, Ciphertext: ct})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal decrypt request: "+err.Error())
		return
	}
	msg, err := conn.NATS().RequestWithContext(ctx, vault.DecryptSubject, reqBody)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "vault decrypt RPC: "+err.Error())
		return
	}
	var resp vault.DecryptResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		s.writeError(w, http.StatusBadGateway, "parse vault decrypt reply: "+err.Error())
		return
	}
	if resp.Error != "" {
		if resp.Error == vault.ErrKeyShredded.Error() {
			s.writeJSON(w, http.StatusOK, map[string]any{"shredded": true})
			return
		}
		s.writeError(w, http.StatusBadGateway, resp.Error)
		return
	}
	if len(resp.Plaintext) == 0 {
		// The wire contract (vault.DecryptResponse) guarantees exactly one of
		// Plaintext/Error set; an empty reply body unmarshals to both fields
		// zero-valued, which is never a genuine "decrypted to nothing" — treat
		// it as a malformed reply rather than a silently empty reveal.
		s.writeError(w, http.StatusBadGateway, "vault decrypt RPC: empty reply")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"plaintext": json.RawMessage(resp.Plaintext)})
}
