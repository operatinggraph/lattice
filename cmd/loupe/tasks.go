package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// taskOperation is the human-facing rendering of a task's forOperation target.
// Key is the op meta-vertex key (vtx.meta.<id>); Name is the op's root
// operationType (the field every op DDL carries), falling back to a
// .canonicalName aspect for the handful of primordial metas that have one;
// Description is the op's optional .description aspect. A task-inbox surface uses
// Key to link straight to the op's form (GET /api/op) so the assignee can
// complete it.
type taskOperation struct {
	Key         string `json:"key"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// taskRow is one task the operator inbox renders. The relationships are sourced
// from the task's links (Contract #10 §10.1 — task relationships are links, not
// fields); status / expiresAt are the only scalars on the task root.
//
// Assignment reflects the FR28 queue plane: an open task carries exactly one of
// assignedTo (push, an identity) or queuedFor (pull, a role any holder claims via
// ClaimTask). Available is the assignee's routing gate (the .availability aspect,
// absent == available) and is nil for a role-queued task — a role queue has no
// single assignee. Stuck flags the FR29 unrouted case an operator must resolve:
// an open, role-queued task past its own expiry with no claim (the Loupe-local
// mirror of the unroutedTasks target's missing_claim gap).
type taskRow struct {
	Key        string        `json:"key"`
	Status     string        `json:"status"`
	ExpiresAt  string        `json:"expiresAt,omitempty"`
	Assignee   string        `json:"assignee,omitempty"`
	QueuedFor  string        `json:"queuedFor,omitempty"`
	Assignment string        `json:"assignment,omitempty"`
	Available  *bool         `json:"available,omitempty"`
	Stuck      bool          `json:"stuck,omitempty"`
	Operation  taskOperation `json:"operation"`
	ScopedTo   string        `json:"scopedTo,omitempty"`
}

// computeTasks assembles the task-inbox rows from the Core KV key list. For each
// vtx.task.<id> root it reads {status, expiresAt} and walks the task's links to
// source assignedTo (assignee identity), queuedFor (the role a pull-task is
// queued to), forOperation (the op meta-vertex), and scopedTo (the grant target).
// The op's human label is resolved from the meta vertex's root operationType
// (with a .canonicalName-aspect fallback) and its optional .description aspect via
// get. For an assigned task it reads the assignee's .availability aspect (absent
// == available) to expose the routing gate. now is injected so the stuck check
// (open + queued + past expiry) stays deterministic. statusFilter limits the rows
// to one status (open|complete|cancelled); "" returns every task.
func computeTasks(keys []string, get kvGetter, statusFilter string, now time.Time) []taskRow {
	rows := make([]taskRow, 0)
	for _, k := range keys {
		if classifyKey(k) != classVertex || vertexType(k) != "task" {
			continue
		}
		data := metaData(get, k)
		row := taskRow{
			Key:       k,
			Status:    dataString(data, "status"),
			ExpiresAt: dataString(data, "expiresAt"),
		}
		if statusFilter != "" && row.Status != statusFilter {
			continue
		}
		for _, lk := range keys {
			if !strings.HasPrefix(lk, "lnk.") {
				continue
			}
			lr, ok := linkForVertex(lk, k)
			if !ok {
				continue
			}
			switch lr.Relation {
			case "assignedTo":
				row.Assignee = lr.OtherKey
			case "queuedFor":
				row.QueuedFor = lr.OtherKey
			case "forOperation":
				row.Operation.Key = lr.OtherKey
			case "scopedTo":
				row.ScopedTo = lr.OtherKey
			}
		}
		// FR28 assignment kind — an open task carries exactly one of assignedTo /
		// queuedFor; a completed/cancelled task may have neither once its links
		// tombstone. The UI renders from this rather than re-inferring.
		switch {
		case row.Assignee != "":
			row.Assignment = "assigned"
		case row.QueuedFor != "":
			row.Assignment = "queued"
		}
		// The assignee's routing gate: the .availability aspect's `available` bool
		// (Fire 2 SetAvailability), absent aspect == available. Only meaningful for
		// an assigned task — a role queue has no single assignee to attribute.
		if row.Assignee != "" {
			avail := true
			if av := metaData(get, row.Assignee+".availability"); av != nil {
				if b, ok := av["available"].(bool); ok {
					avail = b
				}
			}
			row.Available = &avail
		}
		// FR29 unrouted: an open, role-queued task past its own expiry with no
		// claim is stuck — surface-only, so the operator is the remediation.
		if row.Status == "open" && row.QueuedFor != "" && row.ExpiresAt != "" {
			if exp, err := time.Parse(time.RFC3339, row.ExpiresAt); err == nil && now.After(exp) {
				row.Stuck = true
			}
		}
		if row.Operation.Key != "" {
			// A dispatched userTask's forOperation points at the operation's DDL
			// meta-vertex, whose name lives on the root as data.operationType.
			// Prefer a .canonicalName aspect (only primordial metas have one) and
			// fall back to the root operationType so package ops still render a name.
			row.Operation.Name = dataString(metaData(get, row.Operation.Key+".canonicalName"), "value", "name", "canonicalName")
			if row.Operation.Name == "" {
				row.Operation.Name = dataString(metaData(get, row.Operation.Key), "operationType", "name", "canonicalName")
			}
			row.Operation.Description = dataString(metaData(get, row.Operation.Key+".description"), "value", "text", "description")
		}
		rows = append(rows, row)
	}
	// Stuck/unrouted work first (the operator must resolve it), then open tasks
	// (the actionable inbox), then by soonest expiry, then key for a stable order.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Stuck != rows[j].Stuck {
			return rows[i].Stuck
		}
		oi, oj := rows[i].Status == "open", rows[j].Status == "open"
		if oi != oj {
			return oi
		}
		if rows[i].ExpiresAt != rows[j].ExpiresAt {
			return rows[i].ExpiresAt < rows[j].ExpiresAt
		}
		return rows[i].Key < rows[j].Key
	})
	return rows
}

// handleTasks implements GET /api/tasks?status= — the operator task inbox. It
// lists every vtx.task.<id>, sources its assignee / queue role / operation /
// target from the task's links, exposes the FR28/FR29 queue plane (assignment
// kind, assignee availability, stuck/unrouted flag), and resolves the operation's
// human label from its meta-vertex, so the UI can render an actionable inbox and
// link each task to its op form.
func (s *server) handleTasks(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv: "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	statusFilter := r.URL.Query().Get("status")
	s.writeJSON(w, http.StatusOK, map[string]any{"tasks": computeTasks(keys, get, statusFilter, time.Now().UTC())})
}
