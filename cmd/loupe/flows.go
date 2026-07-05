package main

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/asolgan/lattice/internal/bootstrap"
)

// flowRow is one row of the Chronicler's `loomFlowHistory` read model
// (orchestration-history-read-model-design.md §2.6), read from its NATS-KV
// bucket over the P5 path (Loupe reads the lens target, never `loom-state`).
// Live is cross-referenced against the live `lattice.ctrl.loom.list` control
// read (§2.7): true when a "running" row still has a matching live instance,
// false when the terminal event was lost or the engine died mid-flight — the
// "orphaned" signal the design calls out as observational value, not a leak.
// nil means "unknown" — either the row isn't "running" (badge doesn't apply)
// or the live control read itself failed, which must NOT render as a false
// "orphaned" (that would misreport a control-plane outage as a stuck flow).
type flowRow struct {
	InstanceID    string `json:"instanceId"`
	PatternRef    string `json:"patternRef"`
	SubjectKey    string `json:"subjectKey"`
	Status        string `json:"status"`
	StartedAt     string `json:"startedAt,omitempty"`
	EndedAt       string `json:"endedAt,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
	Live          *bool  `json:"live,omitempty"`
}

// computeFlows assembles the Flows-tab rows from the orchestration-history
// bucket's keys (each key is a bare instanceId per the Fire-2 as-built row
// key). A row that fails to decode is skipped — a durable read model
// tolerates a poison entry rather than failing the whole list. statusFilter
// "" or "all" returns every row; otherwise only rows whose status matches.
// liveIDs is the set of instanceIds the live `loom.list` control read
// currently reports; liveKnown is false when that control read itself failed
// (§2.5.2: a terminal row is never badged live/orphaned regardless — it is
// just done — and a "running" row stays unbadged, not falsely "orphaned",
// when liveKnown is false).
func computeFlows(keys []string, get kvGetter, liveIDs map[string]bool, liveKnown bool, statusFilter string) []flowRow {
	rows := make([]flowRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var cols struct {
			InstanceID    string `json:"instance_id"`
			PatternRef    string `json:"pattern_ref"`
			SubjectKey    string `json:"subject_key"`
			Status        string `json:"status"`
			StartedAt     string `json:"started_at"`
			EndedAt       string `json:"ended_at"`
			FailureReason string `json:"failure_reason"`
		}
		if json.Unmarshal(raw, &cols) != nil || cols.InstanceID == "" {
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
		if row.Status == "running" && liveKnown {
			live := liveIDs[row.InstanceID]
			row.Live = &live
		}
		rows = append(rows, row)
	}
	// Most-recently-started first (a fresh flow is the operator's likeliest
	// interest); a blank/equal startedAt falls back to instanceId for a
	// stable order.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartedAt != rows[j].StartedAt {
			return rows[i].StartedAt > rows[j].StartedAt
		}
		return rows[i].InstanceID < rows[j].InstanceID
	})
	return rows
}

// liveLoomInstances decodes the `instanceId` set out of a `lattice.ctrl.loom.list`
// raw reply. Loupe's control proxy forwards raw JSON without decoding into
// Loom's typed control structs (control.go's doc comment) — this mirrors that
// idiom, pulling only the one field the badge needs rather than importing
// internal/loom/control. A decode failure yields an empty set (badge omitted,
// never a hard failure of the whole Flows list).
func liveLoomInstances(raw json.RawMessage) map[string]bool {
	var reply struct {
		Instances []struct {
			InstanceID string `json:"instanceId"`
		} `json:"instances"`
	}
	out := make(map[string]bool)
	if len(raw) == 0 || json.Unmarshal(raw, &reply) != nil {
		return out
	}
	for _, inst := range reply.Instances {
		out[inst.InstanceID] = true
	}
	return out
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

	var liveIDs map[string]bool
	liveKnown := false
	if raw, err := s.controlRequest(ctx, conn, "lattice.ctrl.loom.list"); err == nil {
		liveIDs = liveLoomInstances(raw)
		liveKnown = true
	}

	statusFilter := r.URL.Query().Get("status")
	s.writeJSON(w, http.StatusOK, map[string]any{"flows": computeFlows(keys, get, liveIDs, liveKnown, statusFilter)})
}
