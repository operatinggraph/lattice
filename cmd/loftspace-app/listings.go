package main

import (
	"encoding/json"
	"net/http"
	"sort"

	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
)

// kvGetter reads a read-model entry's raw bytes for a key, reporting false when
// the key is absent or unreadable — the seam computeListings is unit-tested over.
type kvGetter func(key string) ([]byte, bool)

// listingProjection is one row of the loftspace-domain `availableListings` lens,
// read from its NATS-KV read-model bucket (P5: an application reads the lens
// projection, never Core KV). Numeric facets are pointers so an absent column
// (e.g. a unit with no address, or an optional bathrooms/sqft) stays absent
// rather than projecting as a misleading zero.
type listingProjection struct {
	UnitKey         string   `json:"unitKey"`
	Status          string   `json:"status"`
	RentAmount      *float64 `json:"rentAmount"`
	RentCurrency    string   `json:"rentCurrency"`
	Bedrooms        *float64 `json:"bedrooms"`
	Bathrooms       *float64 `json:"bathrooms"`
	Sqft            *float64 `json:"sqft"`
	AvailableFrom   string   `json:"availableFrom"`
	LeaseTermMonths *float64 `json:"leaseTermMonths"`
	AddrLine1       string   `json:"addrLine1"`
	AddrLine2       string   `json:"addrLine2"`
	AddrCity        string   `json:"addrCity"`
	AddrRegion      string   `json:"addrRegion"`
	AddrPostal      string   `json:"addrPostal"`
}

// listingRow is the Browse & Apply shape the FE renders: the unit key (the
// CreateLeaseApplication target), the availability status, and the listing /
// address facets reassembled from the projection row.
type listingRow struct {
	UnitKey string          `json:"unitKey"`
	Status  string          `json:"status"`
	Listing json.RawMessage `json:"listing"`
	Address json.RawMessage `json:"address,omitempty"`
}

// computeListings assembles the Browse & Apply rows from the `availableListings`
// lens read model. Each key in the bucket is a listed unit; its value is the
// flattened projection row. statusFilter "" or "all" returns every listing;
// otherwise only listings whose status matches. A row that fails to decode or
// carries no unitKey (a tombstoned projection entry) is skipped.
//
// withdrawn (off-market) units are NEVER returned to the applicant Browse — not
// even under "all" — since this endpoint is applicant-facing; a landlord pulls a
// vacancy to hide it from prospects. The landlord surface reads the lens directly
// (handleUnitApplications), so it still sees withdrawn units to relist them. A
// caller may opt back in only by filtering explicitly for status=withdrawn.
func computeListings(keys []string, get kvGetter, statusFilter string) []listingRow {
	rows := make([]listingRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p listingProjection
		if json.Unmarshal(raw, &p) != nil || p.UnitKey == "" {
			continue
		}
		if p.Status == "withdrawn" && statusFilter != "withdrawn" {
			continue
		}
		if statusFilter != "" && statusFilter != "all" && p.Status != statusFilter {
			continue
		}
		rows = append(rows, p.toRow())
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UnitKey < rows[j].UnitKey })
	return rows
}

// toRow reassembles a projection into the FE's nested listing / address shape,
// omitting absent fields so the FE renders only what the listing actually carries.
func (p listingProjection) toRow() listingRow {
	listing := map[string]any{"status": p.Status}
	if p.RentAmount != nil {
		listing["rentAmount"] = *p.RentAmount
	}
	if p.RentCurrency != "" {
		listing["rentCurrency"] = p.RentCurrency
	}
	if p.Bedrooms != nil {
		listing["bedrooms"] = *p.Bedrooms
	}
	if p.Bathrooms != nil {
		listing["bathrooms"] = *p.Bathrooms
	}
	if p.Sqft != nil {
		listing["sqft"] = *p.Sqft
	}
	if p.AvailableFrom != "" {
		listing["availableFrom"] = p.AvailableFrom
	}
	if p.LeaseTermMonths != nil {
		listing["leaseTermMonths"] = *p.LeaseTermMonths
	}

	address := map[string]any{}
	for k, v := range map[string]string{
		"line1": p.AddrLine1, "line2": p.AddrLine2, "city": p.AddrCity,
		"region": p.AddrRegion, "postal": p.AddrPostal,
	} {
		if v != "" {
			address[k] = v
		}
	}

	row := listingRow{UnitKey: p.UnitKey, Status: p.Status, Listing: mustMarshal(listing)}
	if len(address) > 0 {
		row.Address = mustMarshal(address)
	}
	return row
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// handleListings implements GET /api/listings?status= — the Browse & Apply
// catalog, served from the `availableListings` lens read model (NOT Core KV).
// status defaults to "available"; pass status=all for every listing.
func (s *server) handleListings(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := loftspacedomain.LoftspaceListingsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is loftspace-domain installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
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
