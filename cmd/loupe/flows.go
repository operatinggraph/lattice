package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// flowRow is one row of the Chronicler's `loomFlowHistory` read model
// (orchestration-history-read-model-design.md §2.6), read from its NATS-KV
// bucket over the P5 path (Loupe reads the lens target, never `loom-state`).
//
// Liveness cross-references a "running" row against the live
// `lattice.ctrl.loom.list` control read — see flowLiveness for the three
// verdicts and why the engine's own status, not mere membership in that list,
// is what decides them. EngineStatus carries Loom's answer verbatim so the
// card can quote both voices when they disagree.
//
// PatternName is the pattern's human name, resolved from its meta-vertex (see
// patternNameFrom). A flow's identity to an operator is which pattern ran; the
// bare `vtx.meta.<NanoID>` ref that the read model stores makes every card
// look like every other card.
type flowRow struct {
	InstanceID    string `json:"instanceId"`
	PatternRef    string `json:"patternRef"`
	PatternName   string `json:"patternName,omitempty"`
	SubjectKey    string `json:"subjectKey"`
	Status        string `json:"status"`
	StartedAt     string `json:"startedAt,omitempty"`
	EndedAt       string `json:"endedAt,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
	Liveness      string `json:"liveness,omitempty"`
	EngineStatus  string `json:"engineStatus,omitempty"`
}

// The liveness verdicts on a history row that still reads "running".
const (
	// livenessLive — Loom is running this instance right now. The history
	// row and the engine agree.
	livenessLive = "live"
	// livenessOrphaned — Loom has no record of the instance at all: the
	// terminal event was lost or the engine died mid-flight. Observational
	// value, not a leak (design §2.7).
	livenessOrphaned = "orphaned"
	// livenessStaleHistory — Loom has the instance and considers it FINISHED
	// while the history row still says running. The flow is not stuck; the
	// projection is behind, or it re-opened a row it should have left closed.
	livenessStaleHistory = "stale-history"
)

// loomTerminal reports whether a Loom instance status is an end state
// (internal/loom/state.go: running | complete | failed).
func loomTerminal(status string) bool {
	return status == "complete" || status == "failed"
}

// flowLiveness classifies a history row against Loom's authoritative instance
// state. Empty means no badge: the row is already terminal (nothing to
// cross-reference), or the control read failed and an unknown must never
// render as a confirmed verdict.
//
// The engine's STATUS decides this, not the row's presence in the instance
// list. Loom keeps an instance record after it finishes, so membership alone
// is true of every completed flow — badging on it makes "live" mean "Loom
// still remembers this id", which is exactly the row this badge exists to
// catch. A row saying running against an engine saying complete is the
// design's own "terminal event never landed" case, and it now says so in its
// own word rather than being waved through as live.
func flowLiveness(rowStatus, engineStatus string, engineKnown, engineHas bool) string {
	if rowStatus != "running" || !engineKnown {
		return ""
	}
	if !engineHas {
		return livenessOrphaned
	}
	if loomTerminal(engineStatus) {
		return livenessStaleHistory
	}
	return livenessLive
}

// flowCols is the Chronicler's on-the-wire read-model row (snake_case,
// orchestration-history-read-model-design.md §2.6) — shared by every handler
// that reads the `orchestration-history` bucket so the decode rule (and its
// poison-tolerance) lives in one place.
type flowCols struct {
	InstanceID    string `json:"instance_id"`
	PatternRef    string `json:"pattern_ref"`
	SubjectKey    string `json:"subject_key"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	EndedAt       string `json:"ended_at"`
	FailureReason string `json:"failure_reason"`
}

// decodeFlowCols decodes one bucket entry, rejecting a poison/malformed entry
// (never fatal to the caller's list) or a row missing the instance_id a
// well-formed row must carry.
func decodeFlowCols(raw []byte) (flowCols, bool) {
	var cols flowCols
	if json.Unmarshal(raw, &cols) != nil || cols.InstanceID == "" {
		return flowCols{}, false
	}
	return cols, true
}

// computeFlows assembles the Flows-tab rows from the orchestration-history
// bucket's keys (each key is a bare instanceId per the Fire-2 as-built row
// key). A row that fails to decode is skipped — a durable read model
// tolerates a poison entry rather than failing the whole list. statusFilter
// "" or "all" returns every row; otherwise only rows whose status matches.
// engineStatuses maps instanceId to the status Loom's `loom.list` control read
// reports for it; engineKnown is false when that control read itself failed
// (§2.5.2: a terminal row is never badged regardless — it is just done — and a
// "running" row stays unbadged, not falsely "orphaned", when the engine's
// answer is unavailable). patternName resolves a patternRef to its human
// name; nil leaves every name empty.
func computeFlows(keys []string, get kvGetter, engineStatuses map[string]string, engineKnown bool, statusFilter string, patternName func(string) string) []flowRow {
	rows := make([]flowRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		cols, ok := decodeFlowCols(raw)
		if !ok {
			continue
		}
		if statusFilter != "" && statusFilter != "all" && cols.Status != statusFilter {
			continue
		}
		row := flowRow{
			InstanceID:    cols.InstanceID,
			PatternRef:    cols.PatternRef,
			SubjectKey:    cols.SubjectKey,
			Status:        cols.Status,
			StartedAt:     cols.StartedAt,
			EndedAt:       cols.EndedAt,
			FailureReason: cols.FailureReason,
		}
		engineStatus, engineHas := engineStatuses[row.InstanceID]
		row.Liveness = flowLiveness(row.Status, engineStatus, engineKnown, engineHas)
		if engineHas {
			row.EngineStatus = engineStatus
		}
		if patternName != nil {
			row.PatternName = patternName(row.PatternRef)
		}
		rows = append(rows, row)
	}
	// Most-recently-started first, as the deterministic base order; a
	// blank/equal startedAt falls back to instanceId. The exception-first
	// triage sort and the per-pattern grouping are the logic tier's job
	// (logic/flows.js), the same split computeCapabilityProposals uses.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartedAt != rows[j].StartedAt {
			return rows[i].StartedAt > rows[j].StartedAt
		}
		return rows[i].InstanceID < rows[j].InstanceID
	})
	return rows
}

// loomInstanceStatuses decodes instanceId → status out of a
// `lattice.ctrl.loom.list` raw reply. Loupe's control proxy forwards raw JSON
// without decoding into Loom's typed control structs (control.go's doc
// comment) — this mirrors that idiom, pulling only the two fields the badge
// needs rather than importing internal/loom/control.
//
// The status is half the answer, not decoration: the list carries FINISHED
// instances too, so a decoder that kept only the ids could not tell a running
// flow from a remembered one. A decode failure yields an empty map (no badge,
// never a hard failure of the whole Flows list).
func loomInstanceStatuses(raw json.RawMessage) map[string]string {
	var reply struct {
		Instances []struct {
			InstanceID string `json:"instanceId"`
			Status     string `json:"status"`
		} `json:"instances"`
	}
	out := make(map[string]string)
	if len(raw) == 0 || json.Unmarshal(raw, &reply) != nil {
		return out
	}
	for _, inst := range reply.Instances {
		if inst.InstanceID != "" {
			out[inst.InstanceID] = inst.Status
		}
	}
	return out
}

// loomPatternSpec is the pinned pattern definition behind a flow, read from
// the meta-vertex's `.spec` aspect (pkgmgr's loomPatternSpecBody). The step
// list is the part `inspect` cannot give: the control plane resolves only the
// CURRENT step, so the sequence a flow is walking has to come from the
// definition itself.
type loomPatternSpec struct {
	PatternID         string           `json:"patternId,omitempty"`
	SubjectType       string           `json:"subjectType,omitempty"`
	CompletionDomains []string         `json:"completionDomains,omitempty"`
	Steps             []map[string]any `json:"steps,omitempty"`
}

// readLoomPatternSpec decodes one pattern's `.spec` aspect. A missing or
// malformed spec yields nil — the detail panel then renders the instance
// facts alone rather than failing, since the engine's answer is the more
// important half and does not depend on this read.
func readLoomPatternSpec(get kvGetter, patternRef string) *loomPatternSpec {
	data := metaData(get, patternRef+".spec")
	if data == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var spec loomPatternSpec
	if json.Unmarshal(raw, &spec) != nil {
		return nil
	}
	return &spec
}

// handleFlowDetail implements GET /api/flows/<instanceId>: one flow's history
// row, the engine's own view of the instance, and the pinned pattern's step
// sequence.
//
// Three sources, deliberately kept separate in the reply rather than merged:
// the durable history row (what the projection believes), the live control
// read (what Loom believes), and the pattern definition (what was supposed to
// happen). Merging them would have to pick a winner, and the whole reason this
// panel is worth having is that when they disagree the disagreement IS the
// finding — the same reason the list's liveness badge names both voices.
//
// The engine read is best-effort: a control-plane outage leaves engineError
// set and the rest of the panel intact, never a failed request.
func (s *server) handleFlowDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	parts := splitNonEmpty(strings.TrimPrefix(r.URL.Path, "/api/flows/"))
	if len(parts) != 1 {
		s.writeError(w, http.StatusBadRequest, "expected GET /api/flows/<instanceId>")
		return
	}
	id := parts[0]
	if err := validateControlName(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "instance id: "+err.Error())
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	entry, err := conn.KVGet(ctx, bootstrap.OrchestrationHistoryBucket, id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "flow "+id+" not found in the history read model: "+err.Error())
		return
	}
	cols, ok := decodeFlowCols(entry.Value)
	if !ok {
		s.writeError(w, http.StatusBadGateway, "flow "+id+": malformed read-model row")
		return
	}

	out := map[string]any{}
	engineStatus, engineHas := "", false
	subject, subjErr := mutateSubject("loom", id, "inspect")
	if subjErr != nil {
		out["engineError"] = subjErr.Error()
	} else if raw, err := s.controlRequest(ctx, conn, subject); err != nil {
		out["engineError"] = err.Error()
	} else {
		var reply struct {
			Instance *struct {
				Instance struct {
					Status string `json:"status"`
				} `json:"instance"`
			} `json:"instance"`
		}
		if json.Unmarshal(raw, &reply) == nil && reply.Instance != nil {
			engineStatus, engineHas = reply.Instance.Instance.Status, true
		}
		out["engine"] = raw
	}

	get := func(key string) ([]byte, bool) {
		e, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return e.Value, true
	}
	row := flowRow{
		InstanceID:    cols.InstanceID,
		PatternRef:    cols.PatternRef,
		PatternName:   patternNameFrom(get, cols.PatternRef),
		SubjectKey:    cols.SubjectKey,
		Status:        cols.Status,
		StartedAt:     cols.StartedAt,
		EndedAt:       cols.EndedAt,
		FailureReason: cols.FailureReason,
	}
	// engineKnown is exactly "the inspect read answered": with no answer the
	// row stays unbadged rather than borrowing a verdict, the same rule the
	// list applies when loom.list fails.
	row.Liveness = flowLiveness(row.Status, engineStatus, out["engineError"] == nil, engineHas)
	if engineHas {
		row.EngineStatus = engineStatus
	}
	out["row"] = row
	if spec := readLoomPatternSpec(get, cols.PatternRef); spec != nil {
		out["pattern"] = spec
	}
	s.writeJSON(w, http.StatusOK, out)
}

// patternNameResolver returns a memoized patternRef → human name lookup over
// Core KV (Loupe as the console inspector — targeted reads, not a scan).
//
// A Loom pattern's name is `patternId` inside its `.spec` aspect
// (pkgmgr's loomPatternSpecBody, Contract #10 §10.5) — NOT the
// `.canonicalName` aspect a lens meta-vertex carries. The two meta families
// look alike and are not: a pattern vertex has no canonicalName aspect at all,
// so the lens page's resolver reads nothing here. canonicalName is still tried
// as a fallback so a pattern that ever grows one resolves without a change.
//
// Memoized because a page of flows is a handful of DISTINCT patterns repeated
// many times over — 26 rows across 3 patterns on the stack this was built
// against — so the per-row cost is one map hit, not one round trip. An
// unresolvable ref yields "" and the card falls back to the raw ref rather
// than showing nothing.
func (s *server) patternNameResolver(ctx context.Context, conn *substrate.Conn) func(string) string {
	seen := make(map[string]string)
	return func(patternRef string) string {
		if patternRef == "" {
			return ""
		}
		if name, ok := seen[patternRef]; ok {
			return name
		}
		get := func(key string) ([]byte, bool) {
			entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
			if err != nil {
				return nil, false
			}
			return entry.Value, true
		}
		name := patternNameFrom(get, patternRef)
		seen[patternRef] = name
		return name
	}
}

// patternNameFrom reads one pattern meta-vertex's human name. Split out from
// the memoizing resolver so a test can pin the KEY and FIELD it reads — the
// lens family's `.canonicalName` shape resolves nothing on a pattern vertex,
// and injecting a fake resolver at the computeFlows seam cannot catch that.
func patternNameFrom(get kvGetter, patternRef string) string {
	if name := dataString(metaData(get, patternRef+".spec"), "patternId"); name != "" {
		return name
	}
	return dataString(metaData(get, patternRef+".canonicalName"), "value", "name", "canonicalName")
}

// timelineFlow is one flow's liveness span for the map scrubber (F13 §4.2's
// v1 tier — flow-liveness replay). EndedAt empty means still running as of
// the read (the FE treats it as live through "now").
type timelineFlow struct {
	InstanceID string `json:"instanceId"`
	PatternRef string `json:"patternRef"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
	EndedAt    string `json:"endedAt,omitempty"`
}

// computeTimeline assembles the scrubber's flow-liveness rows: every flow
// whose `[started_at, ended_at)` span overlaps `[from, to)`, per the F13 §4.2
// v1 design ("a flow contributes to the frame between its started_at and
// ended_at"). A row with an unparsable started_at is skipped (a durable read
// model tolerates a poison entry rather than failing the whole window); a
// still-running row (empty ended_at) is treated as live through `to` — the
// scrubber's own window bound stands in for "still open" without guessing at
// a real end time. Rows are returned unsorted (the FE's pure frame math sorts
// however it needs).
func computeTimeline(keys []string, get kvGetter, from, to time.Time) []timelineFlow {
	rows := make([]timelineFlow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		cols, ok := decodeFlowCols(raw)
		if !ok {
			continue
		}
		started, err := time.Parse(time.RFC3339, cols.StartedAt)
		if err != nil {
			continue
		}
		ended := to
		if cols.EndedAt != "" {
			e, err := time.Parse(time.RFC3339, cols.EndedAt)
			if err != nil {
				continue
			}
			ended = e
		}
		if started.After(to) || !ended.After(from) {
			continue // the span [started, ended) doesn't overlap [from, to)
		}
		rows = append(rows, timelineFlow{
			InstanceID: cols.InstanceID,
			PatternRef: cols.PatternRef,
			Status:     cols.Status,
			StartedAt:  cols.StartedAt,
			EndedAt:    cols.EndedAt,
		})
	}
	return rows
}

// handleHistoryTimeline implements GET /api/history/timeline?from=&to= (both
// RFC3339, required) — the map scrubber's v1 data source (F13 §4.2). It reads
// the same `orchestration-history` bucket the Flows tab already proves live
// (no new backend dependency): the FE reconstructs replay frames from the
// flow spans client-side (logic/scrubber.js's framesFromFlows).
func (s *server) handleHistoryTimeline(w http.ResponseWriter, r *http.Request) {
	// Query validation is a client error independent of connectivity — it
	// runs before requireConn so a malformed request answers 400 even against
	// a down NATS, instead of masking it behind a misleading 502.
	fromStr, toStr := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "from must be RFC3339: "+err.Error())
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "to must be RFC3339: "+err.Error())
		return
	}
	if !to.After(from) {
		s.writeError(w, http.StatusBadRequest, "to must be after from")
		return
	}

	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := bootstrap.OrchestrationHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is orchestration-base installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"flows": computeTimeline(keys, get, from, to)})
}

// handleFlows implements GET /api/flows?status= — the Chronicler's Loom-flow
// history view. It lists the `orchestration-history` read-model bucket (P5)
// and cross-references the live `lattice.ctrl.loom.list` control read to
// badge a "running" row live vs orphaned (§2.5.2/§2.7). The live cross-check
// is best-effort: a control-plane read failure still returns the history
// rows, just with every "running" row left unbadged (liveKnown=false), since
// the read model is the authoritative list and the live check is enrichment
// only — an outage must never render as a false "orphaned" verdict.
func (s *server) handleFlows(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := bootstrap.OrchestrationHistoryBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is orchestration-base installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}

	var engineStatuses map[string]string
	engineKnown := false
	if raw, err := s.controlRequest(ctx, conn, "lattice.ctrl.loom.list"); err == nil {
		engineStatuses = loomInstanceStatuses(raw)
		engineKnown = true
	}

	statusFilter := r.URL.Query().Get("status")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"flows": computeFlows(keys, get, engineStatuses, engineKnown, statusFilter, s.patternNameResolver(ctx, conn)),
	})
}
