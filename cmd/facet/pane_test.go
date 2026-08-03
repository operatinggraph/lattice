package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// shippedPaneSections decodes every pane descriptor the edge-manifest package
// actually ships, so the executor invariants below are pinned against the
// REAL descriptors, not hand-copied fixtures that could drift from them.
func shippedPaneSections(t *testing.T) []paneSection {
	t.Helper()
	var all []paneSection
	for _, pane := range edgemanifest.Panes() {
		var sections []paneSection
		require.NoError(t, json.Unmarshal([]byte(pane.Sections), &sections))
		for _, sec := range sections {
			require.NoError(t, validateSection(sec), "shipped descriptor must pass the executor grammar")
			all = append(all, sec)
		}
	}
	require.NotEmpty(t, all)
	return all
}

// TestPaneSQL_CarriesNoWorkplacePredicate pins the read spine: confinement is
// RLS over the tokens the actor's grants carry, NOT a WHERE clause. A
// hand-written workplace predicate would look like a tightening while
// actually replacing the enforced boundary with an advisory one — and an
// actor holding no grants must read zero rows because it holds no tokens,
// not because a filter happened to match nothing. Checked against every
// shipped descriptor AND enforced structurally: the compiler has no code
// path that could emit such a predicate.
func TestPaneSQL_CarriesNoWorkplacePredicate(t *testing.T) {
	for _, sec := range shippedPaneSections(t) {
		sql, _ := compileSectionSQL(sec)
		lower := strings.ToLower(sql)
		// The session variable is never a legitimate identifier in a pane
		// query — not in the predicate, not in the projection.
		require.NotContains(t, lower, "lattice.actor_id",
			"a pane query must never name the RLS session variable itself")
		// Scanned over the PREDICATE, not the whole statement. A projected
		// column may legitimately be named for an actor —
		// `credential_actor_key` is signInMethods' dispatch target — and
		// reading one is not scoping by one. A confinement expressed as a
		// filter can only live after WHERE: compileSectionSQL emits no joins
		// and no subqueries.
		predicate := ""
		if i := strings.Index(lower, " where "); i >= 0 {
			predicate = lower[i:]
		}
		for _, banned := range []string{"actor", "landlord_id", "building", "workplace", "works_at", "clinic_id"} {
			require.NotContains(t, predicate, banned,
				"a pane query must not filter by actor or workplace — RLS is the boundary (%q)", banned)
		}
	}
}

// TestPaneSQL_StateColumnsAreNeverFiltered — a section that carries a `state`
// column (the ops' visibleWhen input) must read BOTH states: filtering to one
// would make rows in the other state permanently unreachable to the op that
// flips them back. The shipped descriptors declare no filter on any state
// column; this pins that.
func TestPaneSQL_StateColumnsAreNeverFiltered(t *testing.T) {
	for _, sec := range shippedPaneSections(t) {
		for _, c := range sec.Source.Columns {
			if c.Role != "state" {
				continue
			}
			if f := sec.Source.Filter; f != nil {
				require.NotEqual(t, c.Name, f.Column,
					"section %s filters on its own state column %q", sec.ID, c.Name)
			}
			sql, _ := compileSectionSQL(sec)
			require.Contains(t, strings.ToLower(sql), c.Name,
				"the state column itself must be selected or visibleWhen evaluates against nothing")
		}
	}
}

// TestCompileSectionSQL_Golden pins the compiled shape for one full-featured
// section: quoting, isNull filter, NULLS LAST, and the limit.
func TestCompileSectionSQL_Golden(t *testing.T) {
	sec := paneSection{
		ID: "g",
		Source: paneSource{
			Table: "read_things",
			Columns: []paneColumn{
				{Name: "thing_id", Kind: "text", Role: "id"},
				{Name: "signed_at", Kind: "datetime", Role: "meta"},
			},
			Filter:  &paneFilter{Kind: "isNull", Column: "decided"},
			OrderBy: &paneOrderBy{Column: "signed_at", NullsLast: true, TieBreak: "thing_id"},
			Limit:   50,
		},
	}
	sql, needsDay := compileSectionSQL(sec)
	require.False(t, needsDay)
	require.Equal(t,
		`SELECT "thing_id", "signed_at" FROM "read_things" WHERE "decided" IS NULL ORDER BY "signed_at" NULLS LAST, "thing_id" LIMIT 50`,
		sql)

	sec.Source.Filter = &paneFilter{Kind: "utcDay", Column: "starts_at"}
	sec.Source.OrderBy = &paneOrderBy{Column: "starts_at"}
	sql, needsDay = compileSectionSQL(sec)
	require.True(t, needsDay)
	require.Equal(t,
		`SELECT "thing_id", "signed_at" FROM "read_things" WHERE "starts_at" >= $1 AND "starts_at" < $2 ORDER BY "starts_at" LIMIT 50`,
		sql)
}

// TestValidateSection_RefusesTheGrammarEdges — the executor revalidates
// descriptors independently of the package test, so a malformed descriptor
// fails loudly before any SQL is built.
func TestValidateSection_RefusesTheGrammarEdges(t *testing.T) {
	base := func() paneSection {
		return paneSection{
			ID: "s",
			Source: paneSource{
				Table:   "read_ok",
				Columns: []paneColumn{{Name: "a_col", Kind: "text", Role: "id"}},
				Limit:   10,
			},
		}
	}

	sec := base()
	sec.Source.Table = "core_kv"
	require.Error(t, validateSection(sec), "non-read_* table must be refused")

	sec = base()
	sec.Source.Table = `read_x"; DROP TABLE users; --`
	require.Error(t, validateSection(sec), "injection-shaped table must be refused")

	sec = base()
	sec.Source.Columns[0].Name = "Bad-Column"
	require.Error(t, validateSection(sec))

	sec = base()
	sec.Source.Filter = &paneFilter{Kind: "raw", Column: "a_col"}
	require.Error(t, validateSection(sec), "filter kinds outside the fixed set must be refused")

	sec = base()
	sec.Dispatch = &paneDispatch{TargetColumn: "not_declared", TargetType: "thing"}
	require.Error(t, validateSection(sec), "a dispatch target must be a declared column")

	require.NoError(t, validateSection(base()))
}

// TestCompileSectionSQL_ClampsTheLimit — a descriptor cannot ask the executor
// for more than paneMaxRows, and a degenerate limit falls back to the cap.
func TestCompileSectionSQL_ClampsTheLimit(t *testing.T) {
	sec := paneSection{ID: "s", Source: paneSource{
		Table:   "read_ok",
		Columns: []paneColumn{{Name: "a", Kind: "text", Role: "id"}},
		Limit:   9999,
	}}
	sql, _ := compileSectionSQL(sec)
	require.Contains(t, sql, "LIMIT 200")
	sec.Source.Limit = 0
	sql, _ = compileSectionSQL(sec)
	require.Contains(t, sql, "LIMIT 200")
}

// TestNormalizePaneValue_NullCostsAFieldNeverARow — every display column is
// optional. A SQL NULL degrades one field (to the declared default via the
// caller, or absence), never the row.
func TestNormalizePaneValue_NullCostsAFieldNeverARow(t *testing.T) {
	_, ok := normalizePaneValue(nil)
	require.False(t, ok)

	v, ok := normalizePaneValue([]byte("bytes"))
	require.True(t, ok)
	require.Equal(t, "bytes", v)

	v, ok = normalizePaneValue("hello")
	require.True(t, ok)
	require.Equal(t, "hello", v)

	v, ok = normalizePaneValue(true)
	require.True(t, ok)
	require.Equal(t, true, v)

	var n pgtype.Numeric
	require.NoError(t, n.Scan("2400.5"))
	v, ok = normalizePaneValue(n)
	require.True(t, ok)
	require.Equal(t, 2400.5, v)
}

func TestHandlePane_RefusesTheBootFallbackSession(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handlePane(w, withBootSession(
		httptest.NewRequest(http.MethodGet, "/api/pane?key=x", nil), "staffnano0123456789x"))
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandlePane_RequiresSession(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handlePane(w, httptest.NewRequest(http.MethodGet, "/api/pane?key=x", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandlePane_RequiresAKey(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handlePane(w, withSession(
		httptest.NewRequest(http.MethodGet, "/api/pane", nil), "staffnano0123456789x"))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlePane_ReportsUnconfiguredReadModel(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handlePane(w, withSession(
		httptest.NewRequest(http.MethodGet, "/api/pane?key=x", nil), "staffnano0123456789x"))
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "FACET_PG_DSN")
}

func TestHandlePane_RejectsNonGET(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handlePane(w, withSession(
		httptest.NewRequest(http.MethodPost, "/api/pane?key=x", nil), "staffnano0123456789x"))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// TestUTCDayBounds_IsAHalfOpenISODay — day-filtered columns are ISO-8601
// text, so the filter is a lexicographic range and must be half-open. The
// 23:59 vector is the one that matters: an inclusive upper bound, or a bound
// built from local time, would roll the last row of the day into tomorrow.
func TestUTCDayBounds_IsAHalfOpenISODay(t *testing.T) {
	start, end := utcDayBounds(time.Date(2026, 7, 20, 23, 59, 59, 0, time.UTC))
	require.Equal(t, "2026-07-20T00:00:00Z", start)
	require.Equal(t, "2026-07-21T00:00:00Z", end)

	last := "2026-07-20T23:59:00Z"
	require.True(t, last >= start && last < end, "the last minute of the day must fall inside the day's bounds")

	midnight := "2026-07-20T00:00:00Z"
	require.True(t, midnight >= start && midnight < end)

	_, prevEnd := utcDayBounds(time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC))
	require.Equal(t, start, prevEnd)
}
