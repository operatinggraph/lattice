package main

import (
	"encoding/json"
	"net/http"
	"sort"

	clinicdomain "github.com/asolgan/lattice/packages/clinic-domain"
)

// siteRow is one row of the clinic-domain `clinicSites` lens read model (P5: an
// application reads the lens projection, never Core KV) — a location-domain
// building carrying a clinic `.site` profile (SetSiteProfile).
type siteRow struct {
	SiteKey string `json:"siteKey"`
	Name    string `json:"name"`
}

// computeSites assembles the site directory from the `clinicSites` lens read
// model. A row that fails to decode or carries no siteKey (a tombstoned
// projection entry) is skipped. Rows are sorted by name for a stable picker.
func computeSites(keys []string, get kvGetter) []siteRow {
	rows := make([]siteRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var s siteRow
		if json.Unmarshal(raw, &s) != nil || s.SiteKey == "" {
			continue
		}
		rows = append(rows, s)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].SiteKey < rows[j].SiteKey
	})
	return rows
}

// handleSites implements GET /api/sites — the site directory, served from the
// `clinicSites` lens read model (NOT Core KV).
func (s *server) handleSites(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := clinicdomain.ClinicSitesBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is clinic-domain installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows := computeSites(keys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"sites": rows, "count": len(rows)})
}

// providerSiteRow is one row of the clinic-domain `providerSites` lens read
// model — one row per (provider, site) practicesAt pair (AssignProviderSite).
type providerSiteRow struct {
	ProviderKey  string `json:"providerKey"`
	SiteKey      string `json:"siteKey"`
	ProviderName string `json:"providerName"`
	SiteName     string `json:"siteName"`
}

// computeProviderSites assembles the provider×site join from the
// `providerSites` lens read model. A row that fails to decode or is missing
// either endpoint key (a tombstoned projection entry) is skipped.
func computeProviderSites(keys []string, get kvGetter) []providerSiteRow {
	rows := make([]providerSiteRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var ps providerSiteRow
		if json.Unmarshal(raw, &ps) != nil || ps.ProviderKey == "" || ps.SiteKey == "" {
			continue
		}
		rows = append(rows, ps)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ProviderKey != rows[j].ProviderKey {
			return rows[i].ProviderKey < rows[j].ProviderKey
		}
		return rows[i].SiteKey < rows[j].SiteKey
	})
	return rows
}

// handleProviderSites implements GET /api/provider-sites — which providers
// practice at which sites, served from the `providerSites` lens read model
// (NOT Core KV). Backs the booking picker's site filter and the site
// directory admin page's assignment list.
func (s *server) handleProviderSites(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := clinicdomain.ClinicProviderSitesBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is clinic-domain installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows := computeProviderSites(keys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"providerSites": rows, "count": len(rows)})
}
