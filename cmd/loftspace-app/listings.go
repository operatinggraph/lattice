package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/asolgan/lattice/internal/bootstrap"
)

// kvGetter reads a Core KV envelope's raw bytes for a key, reporting false when
// the key is absent or unreadable — the seam computeListings / computeIdentities
// are unit-tested over.
type kvGetter func(key string) ([]byte, bool)

// listingRow is one leasable unit the Browse & Apply view renders: the unit's
// key (the CreateLeaseApplication target), its availability status, and the raw
// .listing / .address aspect data forwarded verbatim so the FE renders rent /
// bedrooms / address without a typed coupling to the package's aspect shape.
type listingRow struct {
	UnitKey string          `json:"unitKey"`
	Status  string          `json:"status"`
	Listing json.RawMessage `json:"listing"`
	Address json.RawMessage `json:"address,omitempty"`
}

// identityRow is one applicant identity the switcher offers. Label is a
// best-effort human name from a .profile / .canonicalName aspect; the FE falls
// back to the key when it is empty.
type identityRow struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
}

// aspectEnvelope is the slice of a Core KV envelope these assemblers read: the
// soft-delete flag and the aspect's data object (kept raw to forward verbatim).
type aspectEnvelope struct {
	IsDeleted bool            `json:"isDeleted"`
	Data      json.RawMessage `json:"data"`
}

// decodeAspect parses key's envelope, returning its raw data and false when the
// key is absent, unreadable, or soft-deleted (a tombstoned listing is not live).
func decodeAspect(get kvGetter, key string) (json.RawMessage, bool) {
	raw, ok := get(key)
	if !ok {
		return nil, false
	}
	var env aspectEnvelope
	if json.Unmarshal(raw, &env) != nil || env.IsDeleted {
		return nil, false
	}
	return env.Data, true
}

// computeListings assembles the Browse & Apply rows from the Core KV key list.
// It selects every live vtx.unit.<id>.listing aspect, reads the sibling
// .address, and filters by status (statusFilter "" or "all" returns every
// listing; otherwise only listings whose .listing.status matches). The unit key
// is the .listing key with the ".listing" localName stripped.
func computeListings(keys []string, get kvGetter, statusFilter string) []listingRow {
	rows := make([]listingRow, 0)
	for _, k := range keys {
		unitKey, ok := unitOfListingKey(k)
		if !ok {
			continue
		}
		data, ok := decodeAspect(get, k)
		if !ok {
			continue
		}
		var meta struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(data, &meta)
		if statusFilter != "" && statusFilter != "all" && meta.Status != statusFilter {
			continue
		}
		row := listingRow{UnitKey: unitKey, Status: meta.Status, Listing: data}
		if addr, ok := decodeAspect(get, unitKey+".address"); ok {
			row.Address = addr
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UnitKey < rows[j].UnitKey })
	return rows
}

// unitOfListingKey returns the unit vertex key for a vtx.unit.<id>.listing
// aspect key (exactly 4 segments, type "unit", localName "listing"), reporting
// false for anything else.
func unitOfListingKey(key string) (string, bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 4 || parts[0] != "vtx" || parts[1] != "unit" || parts[3] != "listing" || parts[2] == "" {
		return "", false
	}
	return strings.Join(parts[:3], "."), true
}

// computeIdentities lists the live applicant identities (vtx.identity.<id>
// roots) with a best-effort label from a .profile or .canonicalName aspect.
func computeIdentities(keys []string, get kvGetter) []identityRow {
	rows := make([]identityRow, 0)
	for _, k := range keys {
		parts := strings.Split(k, ".")
		if len(parts) != 3 || parts[0] != "vtx" || parts[1] != "identity" || parts[2] == "" {
			continue
		}
		if _, ok := decodeAspect(get, k); !ok {
			continue // absent or tombstoned root
		}
		rows = append(rows, identityRow{Key: k, Label: identityLabel(get, k)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows
}

// identityLabel reads a human name for an identity from a .profile or
// .canonicalName aspect, trying the common field spellings; "" when none.
func identityLabel(get kvGetter, identityKey string) string {
	for _, probe := range []struct {
		key    string
		fields []string
	}{
		{identityKey + ".profile", []string{"displayName", "name", "fullName", "value"}},
		{identityKey + ".canonicalName", []string{"value", "name", "canonicalName"}},
	} {
		data, ok := decodeAspect(get, probe.key)
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		for _, f := range probe.fields {
			if s, ok := m[f].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// handleListings implements GET /api/listings?status= — the Browse & Apply
// catalog. status defaults to "available"; pass status=all for every listing.
func (s *server) handleListings(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = "available"
	}
	rows := computeListings(keys, get, statusFilter)
	s.writeJSON(w, http.StatusOK, map[string]any{"listings": rows, "count": len(rows)})
}

// handleIdentities implements GET /api/identities — the applicant switcher's
// source of existing identities.
func (s *server) handleIdentities(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows := computeIdentities(keys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"identities": rows, "count": len(rows)})
}
