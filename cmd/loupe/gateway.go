package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// revocationRow is one revoked actor in the GET /api/gateway/revocations
// reply: the token-revocation bucket entry plus its key. The doc fields come
// from the Gateway materializer's fold of gateway.actorRevoked (who revoked
// whom, when — the kill-switch's audit surface).
type revocationRow struct {
	Actor     string `json:"actor"`
	RevokedAt string `json:"revokedAt,omitempty"`
	By        string `json:"by,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// handleGatewayRevocations implements GET /api/gateway/revocations: the
// current token-revocation kill-switch set, read straight off the bucket the
// Gateway's revocation.Checker consults per request. This is operational
// security state (Health-KV-class), not Core KV — Loupe reads it directly;
// mutations go through the RevokeActor/UnrevokeActor ops (P2), never a Loupe
// bucket write. Rows sort by actor for a stable list; an entry whose doc is
// unparseable still lists (the key alone IS the revocation — the Checker
// refuses on key presence, not doc contents), a key un-revoked between list
// and get drops, and any other read failure fails the response — this is the
// kill-switch audit surface, so a partially-read list must not present as
// complete.
func (s *server) handleGatewayRevocations(w http.ResponseWriter, r *http.Request) {
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

	keys, err := conn.KVListKeys(ctx, revocation.BucketName)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list "+revocation.BucketName+": "+err.Error())
		return
	}
	rows := make([]revocationRow, 0, len(keys))
	for _, k := range keys {
		entry, err := conn.KVGet(ctx, revocation.BucketName, k)
		if errors.Is(err, substrate.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "get "+k+": "+err.Error())
			return
		}
		row := revocationRow{Actor: k}
		var doc struct {
			RevokedAt string `json:"revokedAt"`
			By        string `json:"by"`
			Reason    string `json:"reason"`
		}
		if json.Unmarshal(entry.Value, &doc) == nil {
			row.RevokedAt, row.By, row.Reason = doc.RevokedAt, doc.By, doc.Reason
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Actor < rows[j].Actor })
	s.writeJSON(w, http.StatusOK, map[string]any{"revocations": rows, "count": len(rows)})
}
