package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/asolgan/lattice/internal/substrate"
	privacybase "github.com/asolgan/lattice/packages/privacy-base"
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
