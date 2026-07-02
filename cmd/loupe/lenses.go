package main

import (
	"net/http"
	"sort"
)

// lensRow is one lens in the GET /api/lenses roster. Status is the §4.2
// renderedState (projecting/lagging/paused/pending-readpath/rebuilding/fault/
// unknown); TargetType + Protected/GrantTable come from the lens spec join
// (vtx.meta.<id>.spec) — the spec-side truth behind the ◆ protected tag,
// independent of reporter state.
type lensRow struct {
	ID            string   `json:"id"`
	CanonicalName string   `json:"canonicalName,omitempty"`
	Description   string   `json:"description,omitempty"`
	Status        string   `json:"status"`
	Issues        []string `json:"issues,omitempty"`
	TargetType    string   `json:"targetType,omitempty"`
	Protected     bool     `json:"protected,omitempty"`
	GrantTable    bool     `json:"grantTable,omitempty"`
}

// lensSpecInfo is the slice of a lens spec the roster joins in.
type lensSpecInfo struct {
	TargetType string
	Protected  bool
	GrantTable bool
}

// lensSpec reads targetType + the read-path-authorization flags out of a
// lens's vtx.meta.<id>.spec aspect data.
func lensSpec(get kvGetter, id string) lensSpecInfo {
	d := metaData(get, "vtx.meta."+id+".spec")
	if d == nil {
		return lensSpecInfo{}
	}
	info := lensSpecInfo{TargetType: dataString(d, "targetType")}
	if cfg, ok := d["targetConfig"].(map[string]any); ok {
		info.Protected, _ = cfg["protected"].(bool)
		info.GrantTable, _ = cfg["grantTable"].(bool)
	}
	return info
}

// computeLenses assembles the lens roster from the Health KV key set: every
// bare-NanoID lens reporter becomes a row, labeled + spec-joined via the
// resolver callbacks. Rows sort by name (unnamed last), then id.
func computeLenses(
	keys []string,
	readEntry func(string) (map[string]any, bool),
	resolveLens func(id string) (name, desc string),
	resolveSpec func(id string) lensSpecInfo,
) []lensRow {
	rows := make([]lensRow, 0, len(keys))
	for _, k := range keys {
		if _, kind := classifyHealthKey(k); kind != kindLens {
			continue
		}
		doc, ok := readEntry(k)
		if !ok {
			continue
		}
		row := lensRow{ID: k}
		if resolveLens != nil {
			row.CanonicalName, row.Description = resolveLens(k)
		}
		var spec lensSpecInfo
		if resolveSpec != nil {
			spec = resolveSpec(k)
			row.TargetType = spec.TargetType
			row.Protected = spec.Protected
			row.GrantTable = spec.GrantTable
		}
		row.Status, row.Issues, _ = lensRenderedState(doc, spec)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		ni, nj := rows[i].CanonicalName, rows[j].CanonicalName
		if (ni == "") != (nj == "") {
			return ni != ""
		}
		if ni != nj {
			return ni < nj
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// handleLenses implements GET /api/lenses: the refractor roster — every live
// lens reporter with its label, status, and spec-side target/protection flags.
func (s *server) handleLenses(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, readEntry, resolveLens, resolveSpec, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	rows := computeLenses(keys, readEntry, resolveLens, resolveSpec)
	s.writeJSON(w, http.StatusOK, map[string]any{"lenses": rows, "count": len(rows)})
}
