package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHandleStaffWorklist_RefusesTheBootFallbackSession — the worklist serves
// OTHER people's names, addresses and appointments. The boot-env fallback
// identity proves nothing about who is connecting, so it must be refused here
// for the same reason credentials.go refuses it: RLS would still confine the
// rows to the boot identity's grants, but it cannot tell that the caller is
// not that identity.
func TestHandleStaffWorklist_RefusesTheBootFallbackSession(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handleStaffWorklist(w, withBootSession(
		httptest.NewRequest(http.MethodGet, "/api/staff/worklist", nil), "staffnano0123456789x"))
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleStaffWorklist_RequiresSession(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handleStaffWorklist(w, httptest.NewRequest(http.MethodGet, "/api/staff/worklist", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleStaffWorklist_ReportsUnconfiguredReadModel(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handleStaffWorklist(w, withSession(
		httptest.NewRequest(http.MethodGet, "/api/staff/worklist", nil), "staffnano0123456789x"))
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "FACET_PG_DSN")
}

// TestStaffWorklistSQL_CarriesNoWorkplacePredicate pins design §3.5's spine:
// confinement is RLS over the building tokens staffReadGrants projects, NOT a
// WHERE clause. A hand-written workplace predicate would look like a tightening
// while actually replacing the enforced boundary with an advisory one — and a
// staff actor holding no worksAt link must read zero rows because it holds no
// tokens, not because a filter happened to match nothing.
func TestStaffWorklistSQL_CarriesNoWorkplacePredicate(t *testing.T) {
	for _, sql := range []string{selectStaffApplicationsSQL, selectStaffScheduleSQL} {
		lower := strings.ToLower(sql)
		for _, banned := range []string{"actor", "landlord_id", "building", "workplace", "works_at", "clinic_id", "lattice.actor_id"} {
			require.NotContains(t, lower, banned,
				"the worklist queries must not filter by actor or workplace — RLS is the boundary (%q)", banned)
		}
	}
}

// TestStaffScheduleSQL_ExcludesClinicalContent — a front-desk worklist's
// business is visit existence and timing. `reason` and the encounter-derived
// signals are clinical content; widening the column list is a PHI decision, so
// it must fail here first rather than silently ship behind a display tweak.
func TestStaffScheduleSQL_ExcludesClinicalContent(t *testing.T) {
	lower := strings.ToLower(selectStaffScheduleSQL)
	for _, phi := range []string{"reason", "documented_at", "follow_up_requested", "follow_up_date", "status_note"} {
		require.NotContains(t, lower, phi,
			"clinical column %q must not reach the front-desk pane without a PHI decision", phi)
	}
}

// TestUTCDayBounds_IsAHalfOpenISODay — starts_at is ISO-8601 text, so the day
// filter is a lexicographic range and must be half-open. The 23:59 vector is
// the one that matters: an inclusive upper bound, or a bound built from local
// time, would roll the last appointment of the day into tomorrow.
func TestUTCDayBounds_IsAHalfOpenISODay(t *testing.T) {
	start, end := utcDayBounds(time.Date(2026, 7, 20, 23, 59, 59, 0, time.UTC))
	require.Equal(t, "2026-07-20T00:00:00Z", start)
	require.Equal(t, "2026-07-21T00:00:00Z", end)

	// A 23:59 instant sits inside its own day, not the next one.
	last := "2026-07-20T23:59:00Z"
	require.True(t, last >= start && last < end, "the last minute of the day must fall inside the day's bounds")

	// Midnight is the first instant of its own day, never the tail of the prior.
	midnight := "2026-07-20T00:00:00Z"
	require.True(t, midnight >= start && midnight < end)

	// The bounds abut exactly — no gap that could drop an appointment.
	_, prevEnd := utcDayBounds(time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC))
	require.Equal(t, start, prevEnd)
}

// TestPgtypeScan_NullCostsAFieldNeverARow — every display column on both models
// is optional (an unlinked unit, an unnamed provider, an unsigned application).
// A null must degrade one field to its zero value; returning an error would drop
// the whole row and quietly shrink a worklist the reader is relying on.
func TestPgtypeScan_NullCostsAFieldNeverARow(t *testing.T) {
	var s pgtypeText
	require.NoError(t, s.Scan(nil))
	require.Equal(t, "", s.val)
	require.NoError(t, s.Scan("hello"))
	require.Equal(t, "hello", s.val)
	require.NoError(t, s.Scan([]byte("bytes")))
	require.Equal(t, "bytes", s.val)

	var f pgtypeFloat
	require.NoError(t, f.Scan(nil))
	require.Equal(t, float64(0), f.val)
	require.NoError(t, f.Scan(2400.0))
	require.Equal(t, 2400.0, f.val)
}

func TestHandleStaffWorklist_RejectsNonGET(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handleStaffWorklist(w, withSession(
		httptest.NewRequest(http.MethodPost, "/api/staff/worklist", nil), "staffnano0123456789x"))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
