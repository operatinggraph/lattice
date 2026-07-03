package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
)

// The Lens page backend (loupe-2-ux-design.md §6): GET /api/lens/<id>
// assembles the four-panel document — definition (the DDL resolved from the
// graph), reporter state + the Refractor heartbeat overlay, and the owning
// package — and GET /api/lens/<id>/rows browses the target read model: a
// nats_kv target's bucket, or a postgres target's table through the read-only
// LOUPE_PG_DSN seam (pg.go; without a configured DSN a postgres target
// answers the designed pg-pending shape). Control actions go through the
// existing allow-listed /api/control/refractor/<id>/<op> proxy, not through
// here.

const (
	defaultLensRowsLimit = 200
	// maxLensRowsLimit bounds a hand-built ?limit= — each selected row costs a
	// KVGet, and the value is used as a slice capacity, so an unbounded int is
	// both a DoS and a makeslice panic.
	maxLensRowsLimit = 1000
)

// lensFullSpec is the rich parse of a lens's vtx.meta.<id>.spec aspect data —
// everything the DEFINITION panel renders. Target holds the raw targetConfig
// object; renderLensTarget picks the honest subset (never the DSN).
type lensFullSpec struct {
	Found          bool
	Engine         string
	ProjectionKind string
	CypherRule     string
	TargetType     string
	OutputSchema   any
	Target         map[string]any
}

// readLensFullSpec parses the spec aspect's data document. A missing or
// malformed aspect degrades to Found=false — the page renders "(no spec)"
// rather than erroring, since a live reporter without a spec is still worth
// showing.
func readLensFullSpec(get kvGetter, id string) lensFullSpec {
	d := metaData(get, "vtx.meta."+id+".spec")
	if d == nil {
		return lensFullSpec{}
	}
	spec := lensFullSpec{
		Found:          true,
		Engine:         dataString(d, "engine"),
		ProjectionKind: dataString(d, "projectionKind"),
		CypherRule:     dataString(d, "cypherRule"),
		TargetType:     dataString(d, "targetType"),
		OutputSchema:   d["outputSchema"],
	}
	if cfg, ok := d["targetConfig"].(map[string]any); ok {
		spec.Target = cfg
	}
	return spec
}

// specInfo reduces the full spec to the lensSpecInfo slice the shared
// renderedState derivation consumes.
func (s lensFullSpec) specInfo() lensSpecInfo {
	info := lensSpecInfo{TargetType: s.TargetType}
	if s.Target != nil {
		info.Protected, _ = s.Target["protected"].(bool)
		info.GrantTable, _ = s.Target["grantTable"].(bool)
	}
	return info
}

// keyColumns normalizes a targetConfig "key" (a string or an array of
// strings) into a []string.
func keyColumns(cfg map[string]any) []string {
	switch v := cfg["key"].(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// renderLensTarget picks the honest, renderable subset of a lens targetConfig
// for the DEFINITION panel's target row. The DSN is secret-shaped and is never
// forwarded — only the fact that one is configured.
func renderLensTarget(targetType string, cfg map[string]any) map[string]any {
	out := map[string]any{}
	if cfg == nil {
		return out
	}
	if b, _ := cfg["bucket"].(string); b != "" {
		out["bucket"] = b
	}
	if t, _ := cfg["table"].(string); t != "" {
		out["table"] = t
	}
	if cols := keyColumns(cfg); len(cols) > 0 {
		out["keyColumns"] = cols
	}
	if dm, _ := cfg["deleteMode"].(string); dm != "" {
		out["deleteMode"] = dm
	}
	for _, flag := range []string{"protected", "public", "grantTable"} {
		if v, _ := cfg[flag].(bool); v {
			out[flag] = true
		}
	}
	if targetType == "postgres" {
		dsn, _ := cfg["dsn"].(string)
		out["dsnConfigured"] = dsn != ""
	}
	return out
}

// lensPackageRef names the installed package whose manifest declared this
// lens's meta-vertex.
type lensPackageRef struct {
	Key     string `json:"key"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// findOwningPackage scans installed package manifests (the
// vtx.package.<id>.manifest aspect's declaredKeys list) for the one that
// declared vtx.meta.<lensID>. Keys are scanned in sorted order so a
// (pathological) double claim resolves deterministically. Nil means no package
// claims the lens — the page renders "kernel (bootstrap-seeded)".
func findOwningPackage(coreKeys []string, get kvGetter, lensID string) *lensPackageRef {
	want := "vtx.meta." + lensID
	manifests := make([]string, 0, 4)
	for _, k := range coreKeys {
		if strings.HasPrefix(k, "vtx.package.") && strings.HasSuffix(k, ".manifest") && classifyKey(k) == classAspect {
			manifests = append(manifests, k)
		}
	}
	sort.Strings(manifests)
	for _, mk := range manifests {
		d := metaData(get, mk)
		if d == nil {
			continue
		}
		for _, dk := range dataStrings(d, "declaredKeys") {
			if dk == want {
				return &lensPackageRef{
					Key:     strings.TrimSuffix(mk, ".manifest"),
					Name:    dataString(d, "name"),
					Version: dataString(d, "version"),
				}
			}
		}
	}
	return nil
}

// refractorLensOverlay pulls this lens's slice of the Refractor heartbeat —
// metrics.lensLags + metrics.lensLatency — merged per metric from the
// freshest refractor instance carrying that metric (with plural instances,
// one may report the lag and another the latency; taking one whole doc would
// silently drop the other's data). Entries are keyed by canonicalName; the
// lens id is also tried for forward-compatibility. Nil when no instance
// reports the lens.
func refractorLensOverlay(healthKeys []string, readEntry func(string) (map[string]any, bool), id, canonicalName string) map[string]any {
	var (
		lag, latency       any
		lagAt, latAt       time.Time
		lagFound, latFound bool
	)
	for _, k := range healthKeys {
		group, kind := classifyHealthKey(k)
		if kind != kindComponent || group != "refractor" {
			continue
		}
		doc, ok := readEntry(k)
		if !ok {
			continue
		}
		metrics, _ := doc["metrics"].(map[string]any)
		if metrics == nil {
			continue
		}
		at, _ := componentHeartbeat(doc)
		if lags, ok := metrics["lensLags"].(map[string]any); ok {
			for _, name := range []string{canonicalName, id} {
				if v, ok := lags[name]; ok && name != "" {
					if !lagFound || at.After(lagAt) {
						lag, lagAt, lagFound = v, at, true
					}
					break
				}
			}
		}
		if lat, ok := metrics["lensLatency"].(map[string]any); ok {
			for _, name := range []string{canonicalName, id} {
				if v, ok := lat[name].(map[string]any); ok && name != "" {
					if !latFound || at.After(latAt) {
						latency, latAt, latFound = v, at, true
					}
					break
				}
			}
		}
	}
	if !lagFound && !latFound {
		return nil
	}
	out := map[string]any{}
	if lagFound {
		out["lag"] = lag
	}
	if latFound {
		out["latency"] = latency
	}
	return out
}

// buildLensDetail assembles the GET /api/lens/<id> document from the two
// buckets' contents. found is false when the id resolves to neither a
// meta-vertex nor a live Health-KV lens reporter — a 404, not an empty page.
func buildLensDetail(
	id string,
	healthKeys []string,
	readEntry func(string) (map[string]any, bool),
	coreKeys []string,
	coreGet kvGetter,
) (map[string]any, bool) {
	metaKey := "vtx.meta." + id
	metaRaw, metaFound := coreGet(metaKey)
	spec := readLensFullSpec(coreGet, id)
	doc, reporterFound := readEntry(id)
	if !metaFound && !reporterFound {
		return nil, false
	}

	name := dataString(metaData(coreGet, metaKey+".canonicalName"), "value", "name", "canonicalName")
	desc := dataString(metaData(coreGet, metaKey+".description"), "value", "text", "description")

	out := map[string]any{
		"id":            id,
		"metaKey":       metaKey,
		"canonicalName": name,
		"description":   desc,
	}
	if metaFound {
		var env struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if json.Unmarshal(metaRaw, &env) == nil && env.IsDeleted {
			out["isDeleted"] = true
		}
	}

	if reporterFound {
		state, issues, _ := lensRenderedState(doc, spec.specInfo())
		out["status"] = state
		if len(issues) > 0 {
			out["issues"] = issues
		}
		reporter := map[string]any{"found": true}
		for _, f := range []string{"status", "pauseReason", "consumerLag", "errorCount", "lastError", "activeSequence", "lastUpdated", "ruleEngine"} {
			if v, ok := doc[f]; ok {
				reporter[f] = v
			}
		}
		if ts, ok := parseHealthTime(doc, "lastUpdated"); ok {
			reporter["freshness"] = freshness(ts)
		}
		out["reporter"] = reporter
	} else {
		// No live reporter: the meta-vertex exists but nothing projects it.
		out["status"] = lensStateUnknown
		out["reporter"] = map[string]any{"found": false}
	}

	if spec.Found {
		out["definition"] = map[string]any{
			"engine":         spec.Engine,
			"projectionKind": spec.ProjectionKind,
			"cypherRule":     spec.CypherRule,
			"outputSchema":   spec.OutputSchema,
			"targetType":     spec.TargetType,
			"target":         renderLensTarget(spec.TargetType, spec.Target),
		}
		out["targetType"] = spec.TargetType
	}

	if overlay := refractorLensOverlay(healthKeys, readEntry, id, name); overlay != nil {
		out["refractor"] = overlay
	}
	if pkg := findOwningPackage(coreKeys, coreGet, id); pkg != nil {
		out["package"] = pkg
	}
	return out, true
}

// selectLensRows filters a target bucket's key list by the q substring
// (case-insensitive), sorts for stable windows, and caps at limit.
func selectLensRows(keys []string, q string, limit int) (selected []string, total int, truncated bool) {
	q = strings.ToLower(q)
	selected = make([]string, 0, limit)
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)
	for _, k := range sorted {
		if q != "" && !strings.Contains(strings.ToLower(k), q) {
			continue
		}
		total++
		if len(selected) < limit {
			selected = append(selected, k)
		}
	}
	return selected, total, total > len(selected)
}

// The CONTENTS panel's target decision: a nats_kv target browses its bucket,
// a postgres target routes to the read seam (pg.go — real rows with a
// configured LOUPE_PG_DSN, the pg-pending shape without one), and a
// blank/unknown targetType is an error — a malformed spec must not
// masquerade as either browsable state.
const (
	rowsTargetKV  = "kv"
	rowsTargetPG  = "pg"
	rowsTargetBad = "bad"
)

func lensRowsTarget(spec lensFullSpec) (kind, bucket, errMsg string) {
	switch spec.TargetType {
	case "nats_kv":
		bucket, _ := spec.Target["bucket"].(string)
		if bucket == "" {
			return rowsTargetBad, "", "declares no target bucket"
		}
		return rowsTargetKV, bucket, ""
	case "postgres":
		return rowsTargetPG, "", ""
	default:
		return rowsTargetBad, "", "unknown targetType " + strconv.Quote(spec.TargetType)
	}
}

// handleLens routes GET /api/lens/<id> and GET /api/lens/<id>/rows. The id is
// validated with the same dot-free-token rule as the control proxy, so a
// malformed id never reaches a bucket lookup.
func (s *server) handleLens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	parts := splitNonEmpty(strings.TrimPrefix(r.URL.Path, "/api/lens/"))
	switch {
	case len(parts) == 1:
		s.lensDetail(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "rows":
		s.lensRows(w, r, parts[0])
	default:
		s.writeError(w, http.StatusBadRequest, "expected GET /api/lens/<id> or GET /api/lens/<id>/rows")
	}
}

func (s *server) lensDetail(w http.ResponseWriter, r *http.Request, id string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if err := validateControlName(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "lens id: "+err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	healthKeys, readEntry, _, _, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	coreKeys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv: "+err.Error())
		return
	}
	coreGet := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}

	detail, found := buildLensDetail(id, healthKeys, readEntry, coreKeys, coreGet)
	if !found {
		s.writeError(w, http.StatusNotFound, "lens "+id+" not found (no meta-vertex, no health reporter)")
		return
	}
	s.writeJSON(w, http.StatusOK, detail)
}

// lensRowsLimitQ parses the shared browse parameters: limit (clamped to
// [1, maxLensRowsLimit], default defaultLensRowsLimit) and the q substring.
func lensRowsLimitQ(r *http.Request) (limit int, q string) {
	limit = defaultLensRowsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = min(n, maxLensRowsLimit)
		}
	}
	return limit, r.URL.Query().Get("q")
}

// lensRows implements the CONTENTS panel: a nats_kv target's keys + documents
// (capped, filtered); a postgres target's table rows through the read-only
// seam, or the pg-pending shape when no LOUPE_PG_DSN is configured.
func (s *server) lensRows(w http.ResponseWriter, r *http.Request, id string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if err := validateControlName(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "lens id: "+err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	coreGet := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	spec := readLensFullSpec(coreGet, id)
	if !spec.Found {
		s.writeError(w, http.StatusNotFound, "lens "+id+" has no spec aspect")
		return
	}
	kind, bucket, errMsg := lensRowsTarget(spec)
	limit, q := lensRowsLimitQ(r)
	switch kind {
	case rowsTargetPG:
		s.lensRowsPG(ctx, w, id, spec, limit, q)
		return
	case rowsTargetBad:
		s.writeError(w, http.StatusBadGateway, "lens "+id+": "+errMsg)
		return
	}

	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list "+bucket+": "+err.Error())
		return
	}
	selected, total, truncated := selectLensRows(keys, q, limit)
	rows := make([]map[string]any, 0, len(selected))
	for _, k := range selected {
		row := map[string]any{"key": k}
		switch entry, err := conn.KVGet(ctx, bucket, k); {
		case err != nil:
			// Deleted between list and get, or the request deadline hit
			// mid-window — surfaced per row, not silently key-only.
			row["error"] = err.Error()
		case json.Valid(entry.Value):
			row["doc"] = json.RawMessage(entry.Value)
		}
		rows = append(rows, row)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"targetType": "nats_kv",
		"bucket":     bucket,
		"rows":       rows,
		"count":      len(rows),
		"total":      total,
		"truncated":  truncated,
		"limit":      limit,
	})
}
