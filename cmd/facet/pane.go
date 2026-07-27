package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/operatinggraph/lattice/internal/appsession"
)

// The generic server-pane executor (facet-discovery-restoration-design.md
// §2.1). A pane's sections — which Protected read-model table each reads,
// which columns, how rows filter and order, which column is a dispatch
// target — arrive as a `manifest.pane.*` DESCRIPTOR row over the same
// personal SYNC plane as every other manifest row. This host knows no pane,
// no table, and no column by name: it reads the descriptor from the session
// identity's OWN mirror (fed by that identity's authenticated consumer —
// nothing the browser sends is trusted for anything but the pane id),
// validates every identifier against a strict grammar, compiles the SELECTs,
// and runs them under RLS as the signed-in actor — the credentials.go read
// spine, unchanged.
//
// Pane ROWS stay session-scoped server reads: cross-identity and PII-bearing,
// never mirrored, unavailable offline. Only the DESCRIPTOR rides the mirror.
//
// Two independent gates confine a request: the descriptor must exist in the
// caller's mirror (the pane lens projects it only over holdsRole → offeredTo,
// so a caller without the offered role has no descriptor to execute), and RLS
// scopes every row to the workplace tokens the actor's grants carry — no
// compiled query filters by actor or workplace, because the grant IS the
// confinement, and an actor with no grants reads zero rows.

// paneColumn is one declared column of a pane section's read.
type paneColumn struct {
	Name        string            `json:"name"`
	Label       string            `json:"label,omitempty"`
	Kind        string            `json:"kind"`
	Role        string            `json:"role"`
	ValueLabels map[string]string `json:"valueLabels,omitempty"`
	Default     any               `json:"default,omitempty"`
	Fallback    string            `json:"fallback,omitempty"`
	Unit        string            `json:"unit,omitempty"`
	Suffix      string            `json:"suffix,omitempty"`
}

type paneFilter struct {
	Kind   string `json:"kind"`
	Column string `json:"column"`
}

type paneOrderBy struct {
	Column    string `json:"column"`
	NullsLast bool   `json:"nullsLast,omitempty"`
	TieBreak  string `json:"tieBreak,omitempty"`
}

type paneSource struct {
	Table   string       `json:"table"`
	Columns []paneColumn `json:"columns"`
	Filter  *paneFilter  `json:"filter,omitempty"`
	OrderBy *paneOrderBy `json:"orderBy,omitempty"`
	Limit   int          `json:"limit"`
}

type paneDispatch struct {
	TargetColumn string `json:"targetColumn"`
	TargetType   string `json:"targetType"`
}

type paneSection struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	EmptyCopy string        `json:"emptyCopy,omitempty"`
	Source    paneSource    `json:"source"`
	Dispatch  *paneDispatch `json:"dispatch,omitempty"`
}

// paneDescriptorRow is the manifest.pane.* mirror row's data payload.
type paneDescriptorRow struct {
	PaneID   string `json:"paneId"`
	Title    string `json:"title"`
	Sections string `json:"sections"`
}

var (
	paneTableRe  = regexp.MustCompile(`^read_[a-z_]+$`)
	paneColumnRe = regexp.MustCompile(`^[a-z_]+$`)
)

// paneMaxRows caps any section read regardless of what a descriptor asks for.
const paneMaxRows = 200

// validateSection checks every identifier and enum in one section against
// the executor's grammar. The descriptor is package data — trusted the same
// way lens DDL is — but this executor revalidates independently so a
// malformed descriptor fails loudly here rather than reaching the database.
func validateSection(sec paneSection) error {
	if sec.ID == "" {
		return fmt.Errorf("section without id")
	}
	if !paneTableRe.MatchString(sec.Source.Table) {
		return fmt.Errorf("section %s: table %q outside the read_* grammar", sec.ID, sec.Source.Table)
	}
	if len(sec.Source.Columns) == 0 {
		return fmt.Errorf("section %s: no columns", sec.ID)
	}
	declared := make(map[string]bool, len(sec.Source.Columns))
	for _, c := range sec.Source.Columns {
		if !paneColumnRe.MatchString(c.Name) {
			return fmt.Errorf("section %s: column %q outside the identifier grammar", sec.ID, c.Name)
		}
		declared[c.Name] = true
	}
	if f := sec.Source.Filter; f != nil {
		if f.Kind != "isNull" && f.Kind != "utcDay" {
			return fmt.Errorf("section %s: filter kind %q not in the fixed set", sec.ID, f.Kind)
		}
		if !paneColumnRe.MatchString(f.Column) {
			return fmt.Errorf("section %s: filter column %q outside the identifier grammar", sec.ID, f.Column)
		}
	}
	if o := sec.Source.OrderBy; o != nil {
		if !paneColumnRe.MatchString(o.Column) {
			return fmt.Errorf("section %s: orderBy column %q outside the identifier grammar", sec.ID, o.Column)
		}
		if o.TieBreak != "" && !paneColumnRe.MatchString(o.TieBreak) {
			return fmt.Errorf("section %s: tieBreak column %q outside the identifier grammar", sec.ID, o.TieBreak)
		}
	}
	if d := sec.Dispatch; d != nil && !declared[d.TargetColumn] {
		return fmt.Errorf("section %s: dispatch targetColumn %q is not a declared column", sec.ID, d.TargetColumn)
	}
	return nil
}

// compileSectionSQL renders one section's SELECT. Every identifier was
// grammar-validated (lowercase letters and underscores only) and is
// double-quoted anyway; values reach the query exclusively as bind
// parameters. Deliberately NO actor/workplace predicate: RLS confines rows,
// a WHERE never does (pane_test.go pins that absence).
func compileSectionSQL(sec paneSection) (sql string, needsDay bool) {
	quoted := make([]string, len(sec.Source.Columns))
	for i, c := range sec.Source.Columns {
		quoted[i] = `"` + c.Name + `"`
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(quoted, ", "))
	b.WriteString(` FROM "` + sec.Source.Table + `"`)
	if f := sec.Source.Filter; f != nil {
		switch f.Kind {
		case "isNull":
			b.WriteString(` WHERE "` + f.Column + `" IS NULL`)
		case "utcDay":
			b.WriteString(` WHERE "` + f.Column + `" >= $1 AND "` + f.Column + `" < $2`)
			needsDay = true
		}
	}
	if o := sec.Source.OrderBy; o != nil {
		b.WriteString(` ORDER BY "` + o.Column + `"`)
		if o.NullsLast {
			b.WriteString(" NULLS LAST")
		}
		if o.TieBreak != "" {
			b.WriteString(`, "` + o.TieBreak + `"`)
		}
	}
	limit := sec.Source.Limit
	if limit <= 0 || limit > paneMaxRows {
		limit = paneMaxRows
	}
	fmt.Fprintf(&b, " LIMIT %d", limit)
	return b.String(), needsDay
}

// normalizePaneValue maps one scanned column value onto the JSON shape the
// renderer (and op visibleWhen evaluation) consumes. A SQL NULL becomes the
// column's declared default when it has one — a null costs a FIELD, never the
// row — and is omitted otherwise.
func normalizePaneValue(v any) (any, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case []byte:
		return string(t), true
	case time.Time:
		return t.UTC().Format(time.RFC3339), true
	case pgtype.Numeric:
		f, err := t.Float64Value()
		if err != nil || !f.Valid {
			return nil, false
		}
		return f.Float64, true
	default:
		return v, true
	}
}

// queryPaneSections runs every section of one pane inside ONE transaction
// with a single txn-local actor session variable, so every section observes
// the same grant state — the multi-read shape the worklist established, now
// descriptor-driven.
func queryPaneSections(ctx context.Context, pool pgxBeginner, actorID string, sections []paneSection, dayStart, dayEnd string) ([]map[string]any, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, len(sections))
	for _, sec := range sections {
		sql, needsDay := compileSectionSQL(sec)
		var args []any
		if needsDay {
			args = []any{dayStart, dayEnd}
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, fmt.Errorf("section %s: %w", sec.ID, err)
		}
		secRows := []map[string]any{}
		fields := rows.FieldDescriptions()
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("section %s: %w", sec.ID, err)
			}
			row := make(map[string]any, len(vals))
			for i, v := range vals {
				name := string(fields[i].Name)
				if nv, ok := normalizePaneValue(v); ok {
					row[name] = nv
				} else if i < len(sec.Source.Columns) && sec.Source.Columns[i].Default != nil {
					row[name] = sec.Source.Columns[i].Default
				}
			}
			secRows = append(secRows, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("section %s: %w", sec.ID, err)
		}
		rows.Close()

		entry := map[string]any{"id": sec.ID, "rows": secRows}
		if needsDay {
			entry["day"] = dayStart[:10]
		}
		out = append(out, entry)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// findPaneDescriptor scans the session identity's own mirror for the
// manifest.pane row whose paneId matches. The mirror is the host-side store
// fed by this identity's authenticated sync consumer — the descriptor is
// exactly as trusted as every other projected row, and its presence IS the
// role gate (the pane lens only projects over holdsRole → offeredTo).
func (s *server) findPaneDescriptor(identityID, paneID string) (*paneDescriptorRow, []paneSection, error) {
	eng, err := s.engines.Acquire(identityID)
	if err != nil {
		return nil, nil, err
	}
	defer s.engines.Release(identityID)

	entries, err := eng.store.ScanPrefix("manifest.pane.")
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		v, ok, err := eng.overlay.Read(e.Key)
		if err != nil || !ok || v.Deleted {
			continue
		}
		var row paneDescriptorRow
		if err := json.Unmarshal(v.Data, &row); err != nil {
			continue
		}
		if row.PaneID != paneID {
			continue
		}
		var sections []paneSection
		if err := json.Unmarshal([]byte(row.Sections), &sections); err != nil {
			return nil, nil, fmt.Errorf("pane %s: sections do not parse: %w", paneID, err)
		}
		for _, sec := range sections {
			if err := validateSection(sec); err != nil {
				return nil, nil, fmt.Errorf("pane %s: %w", paneID, err)
			}
		}
		return &row, sections, nil
	}
	return nil, nil, nil
}

// handlePane implements GET /api/pane?key=<paneId> — the generic Protected
// server-pane read.
func (s *server) handlePane(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	identityID, ok := appsession.Identity(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "no session identity")
		return
	}
	// Pane rows are OTHER people's data. The boot-env fallback identity
	// proves nothing about who is connecting, so it must never reach a
	// cross-identity surface — the same rule credentials.go states for the
	// SENSITIVE credential set. RLS would still confine the rows; it cannot
	// tell that the caller isn't the boot identity.
	if !appsession.ViaCookie(r.Context()) {
		s.writeError(w, http.StatusForbidden, "sign in to view this pane")
		return
	}
	paneID := r.URL.Query().Get("key")
	if paneID == "" {
		s.writeError(w, http.StatusBadRequest, "key required")
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set FACET_PG_DSN and ensure the protected lenses are up)")
		return
	}

	desc, sections, err := s.findPaneDescriptor(identityID, paneID)
	if err != nil {
		s.logger.Error("facet: resolve pane descriptor", "identityId", identityID, "pane", paneID, "err", err)
		s.writeError(w, http.StatusBadGateway, "pane descriptor unavailable")
		return
	}
	if desc == nil {
		s.writeError(w, http.StatusNotFound, "no such pane in your world")
		return
	}

	dayStart, dayEnd := utcDayBounds(time.Now().UTC())
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	secResults, err := queryPaneSections(ctx, s.pgPool, identityID, sections, dayStart, dayEnd)
	if err != nil {
		s.logger.Error("facet: read pane", "identityId", identityID, "pane", paneID, "err", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected pane models")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"paneId":   desc.PaneID,
		"sections": secResults,
	})
}

// utcDayBounds returns the half-open [start, end) ISO-8601 bounds of t's UTC day.
func utcDayBounds(t time.Time) (string, string) {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return start.Format(time.RFC3339), start.AddDate(0, 0, 1).Format(time.RFC3339)
}
