package main

import (
	"encoding/json"
	"net/http"
	"sort"

	clinicdomain "github.com/asolgan/lattice/packages/clinic-domain"
)

// kvGetter reads a read-model entry's raw bytes for a key, reporting false when
// the key is absent or unreadable — the seam the compute* assemblers are
// unit-tested over.
type kvGetter func(key string) ([]byte, bool)

// timeOffRange is one date-specific blackout window on a provider's .timeOff
// aspect (projected verbatim by the clinicProviders lens). From / To are
// canonical-UTC RFC3339 instants with From < To (the op normalizes them); the
// time-off manager UI read-modify-writes this list via SetProviderTimeOff.
type timeOffRange struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// hoursWindow is one recurring-weekly availability window on a provider's .hours
// aspect (projected verbatim by the clinicProviders lens). Day is the UTC weekday
// (0=Sun..6=Sat); OpenSec / CloseSec are UTC seconds-of-day (0..86400) with
// OpenSec < CloseSec. The booking slot picker reads these to compute the
// provider's open slots for a chosen date.
type hoursWindow struct {
	Day      int `json:"day"`
	OpenSec  int `json:"openSec"`
	CloseSec int `json:"closeSec"`
}

// providerRow is one row of the clinic-domain `clinicProviders` lens read model
// (P5: an application reads the lens projection, never Core KV). The booking UI
// renders these as the provider picker; TimeOff carries the provider's declared
// blackout ranges and Hours its availability windows (null/empty when none) so the
// manager UI can edit them and the booking slot picker can suggest open slots.
type providerRow struct {
	ProviderKey string         `json:"providerKey"`
	Name        string         `json:"name"`
	Specialty   string         `json:"specialty"`
	Credentials string         `json:"credentials,omitempty"`
	Bio         string         `json:"bio,omitempty"`
	TimeOff     []timeOffRange `json:"timeOff,omitempty"`
	Hours       []hoursWindow  `json:"hours,omitempty"`
}

// computeProviders assembles the provider roster from the `clinicProviders` lens
// read model. A row that fails to decode or carries no providerKey (a tombstoned
// projection entry) is skipped. Rows are sorted by name for a stable picker.
func computeProviders(keys []string, get kvGetter) []providerRow {
	rows := make([]providerRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p providerRow
		if json.Unmarshal(raw, &p) != nil || p.ProviderKey == "" {
			continue
		}
		rows = append(rows, p)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].ProviderKey < rows[j].ProviderKey
	})
	return rows
}

// handleProviders implements GET /api/providers — the booking picker, served from
// the `clinicProviders` lens read model (NOT Core KV).
func (s *server) handleProviders(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := clinicdomain.ClinicProvidersBucket
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
	rows := computeProviders(keys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"providers": rows, "count": len(rows)})
}
