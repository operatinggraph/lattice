package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/asolgan/lattice/internal/bootstrap"
)

// applicationKeyPrefix is the OutputKeyPattern prefix of the lease-signing
// `leaseApplicationComplete` convergence lens (Contract #10 §10.2:
// "leaseApplicationComplete.{actorSuffix}"). The My Applications tracker reads
// these rows out of the shared weaver-targets read model — never Core KV (P5).
const applicationKeyPrefix = "leaseApplicationComplete."

// applicationRow is one projected `leaseApplicationComplete` row, the live state
// of a single lease application. The gap booleans drive the FE stepper; the
// inflight_ companion distinguishes "in progress" from "to do"; the unit columns
// are the informational "what am I leasing" header. maxretries_<g> is the lens's
// CONSTANT integer retry-budget cap baked onto every row (a count, not a flag —
// it is an int, not a bool: typing it bool drops every row on decode). unitRent
// is a pointer so an absent listing rent stays absent rather than 0.
type applicationRow struct {
	EntityKey         string   `json:"entityKey"`
	Applicant         string   `json:"applicant"`
	Violating         bool     `json:"violating"`
	MissingOnboarding bool     `json:"missing_onboarding"`
	MissingBgcheck    bool     `json:"missing_bgcheck"`
	MissingPayment    bool     `json:"missing_payment"`
	MissingSignature  bool     `json:"missing_signature"`
	InflightBgcheck   bool     `json:"inflight_bgcheck"`
	InflightPayment   bool     `json:"inflight_payment"`
	MaxretriesBgcheck int      `json:"maxretries_bgcheck"`
	MaxretriesPayment int      `json:"maxretries_payment"`
	UnitKey           string   `json:"unitKey"`
	UnitAddress       string   `json:"unitAddress"`
	UnitRent          *float64 `json:"unitRent"`
	FreshUntil        string   `json:"freshUntil"`
}

// computeApplications assembles the My Applications rows from the
// `leaseApplicationComplete` lens read model. It keeps only keys under the
// convergence prefix, decodes each row, and — when applicant is non-empty —
// keeps only that applicant's applications (the trusted-tool view scope; §2 of
// the UX design). A row that fails to decode or carries no entityKey (a
// tombstoned projection) is skipped. Rows sort by entityKey for a stable view.
func computeApplications(keys []string, get kvGetter, applicant string) []applicationRow {
	rows := make([]applicationRow, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, applicationKeyPrefix) {
			continue
		}
		raw, ok := get(k)
		if !ok {
			continue
		}
		var row applicationRow
		if json.Unmarshal(raw, &row) != nil || row.EntityKey == "" {
			continue
		}
		if applicant != "" && row.Applicant != applicant {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].EntityKey < rows[j].EntityKey })
	return rows
}

// handleApplications implements GET /api/applications?applicant= — the My
// Applications status tracker, served from the `leaseApplicationComplete` lens
// rows in the shared weaver-targets read model (NOT Core KV; P5). applicant
// scopes the rows to one applicant identity; omit it to list every application.
func (s *server) handleApplications(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := bootstrap.WeaverTargetsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is lease-signing installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	applicant := strings.TrimSpace(r.URL.Query().Get("applicant"))
	rows := computeApplications(keys, get, applicant)
	s.writeJSON(w, http.StatusOK, map[string]any{"applications": rows, "count": len(rows)})
}
