package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/asolgan/lattice/internal/bootstrap"
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
type taskRow struct {
	Key       string        `json:"key"`
	Status    string        `json:"status"`
	ExpiresAt string        `json:"expiresAt,omitempty"`
	Assignee  string        `json:"assignee,omitempty"`
	Operation taskOperation `json:"operation"`
	ScopedTo  string        `json:"scopedTo,omitempty"`
}

// computeTasks assembles the task-inbox rows from the Core KV key list. For each
// vtx.task.<id> root it reads {status, expiresAt} and walks the task's links to
// source assignedTo (assignee identity), forOperation (the op meta-vertex), and
// scopedTo (the grant target). The op's human label is resolved from the meta
// vertex's root operationType (with a .canonicalName-aspect fallback) and its
// optional .description aspect via get — the data is reachable today through the
// forOperation link, so no prompt aspect is stamped on the task. statusFilter
// limits the rows to one status (open|complete|cancelled); "" returns every task.
func computeTasks(keys []string, get kvGetter, statusFilter string) []taskRow {
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
			case "forOperation":
				row.Operation.Key = lr.OtherKey
			case "scopedTo":
				row.ScopedTo = lr.OtherKey
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
	// Open tasks first (the actionable inbox), then by soonest expiry, then key
	// for a stable order.
	sort.Slice(rows, func(i, j int) bool {
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
// lists every vtx.task.<id>, sources its assignee / operation / target from the
// task's links, and resolves the operation's human label from its meta-vertex,
// so the UI can render an actionable inbox and link each task to its op form.
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
	s.writeJSON(w, http.StatusOK, map[string]any{"tasks": computeTasks(keys, get, statusFilter)})
}
