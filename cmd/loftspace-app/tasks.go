package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

// myTasksRow is one projected `my-tasks` lens entry: a single identity's OPEN
// tasks. The lens (orchestration-base) keys one row per identity at
// my-tasks.<actorSuffix>; openTasks carries the link-sourced, self-describing
// task list (Contract #10 §10.1 — task relationships are links, not fields). The
// applicant app reads THIS projection, never Core KV (P5): Loupe's /api/tasks
// scans Core KV only because it is the admin/debug inspector P5 carves out.
type myTasksRow struct {
	ActorKey  string        `json:"actorKey"`
	OpenTasks []openTaskRow `json:"openTasks"`
}

// openTaskRow is one open task inside a my-tasks row. operationName /
// operationDescription are aspect-hops the lens already resolved off the bound op
// meta-vertex, so the inbox renders a human prompt without a second read.
// scopedTo is the entity the granted op acts on (for a userTask the §10.5
// invariant holds: assignee == scopedTo == the subject), so completion targets
// it. expiresAt is the grant horizon.
type openTaskRow struct {
	TaskKey              string `json:"taskKey"`
	Assignee             string `json:"assignee"`
	ForOperation         string `json:"forOperation"`
	OperationName        string `json:"operationName"`
	OperationDescription string `json:"operationDescription"`
	ScopedTo             string `json:"scopedTo"`
	ExpiresAt            string `json:"expiresAt"`
}

// taskRow is the inbox shape the FE renders: one open task with its self-describing
// op label, the entity it is scoped to, and the expiry. operation is the bound op
// meta-vertex key; operationName / operationDescription are its human label.
type taskRow struct {
	TaskKey              string `json:"taskKey"`
	Assignee             string `json:"assignee"`
	Operation            string `json:"operation,omitempty"`
	OperationName        string `json:"operationName,omitempty"`
	OperationDescription string `json:"operationDescription,omitempty"`
	ScopedTo             string `json:"scopedTo,omitempty"`
	ExpiresAt            string `json:"expiresAt,omitempty"`
}

// computeTasks flattens the applicant's open tasks out of the `my-tasks` lens read
// model. It keeps only the row for the selected applicant (matched on actorKey —
// the per-identity projection key), then emits one taskRow per open task. The lens
// collect can leave a degenerate {taskKey:null} artifact when an identity has no
// open task; an entry without a taskKey is dropped. When applicant is empty every
// identity's open tasks are returned (the operator-wide view). Rows sort by soonest
// expiry, then taskKey, for a stable, actionable order.
func computeTasks(keys []string, get kvGetter, applicant string) []taskRow {
	rows := make([]taskRow, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var mt myTasksRow
		if json.Unmarshal(raw, &mt) != nil || mt.ActorKey == "" {
			continue
		}
		if applicant != "" && mt.ActorKey != applicant {
			continue
		}
		for _, t := range mt.OpenTasks {
			if t.TaskKey == "" {
				continue
			}
			rows = append(rows, taskRow{
				TaskKey:              t.TaskKey,
				Assignee:             t.Assignee,
				Operation:            t.ForOperation,
				OperationName:        t.OperationName,
				OperationDescription: t.OperationDescription,
				ScopedTo:             t.ScopedTo,
				ExpiresAt:            t.ExpiresAt,
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ExpiresAt != rows[j].ExpiresAt {
			return rows[i].ExpiresAt < rows[j].ExpiresAt
		}
		return rows[i].TaskKey < rows[j].TaskKey
	})
	return rows
}

// handleTasks implements GET /api/tasks?applicant= — the applicant task inbox,
// served from the `my-tasks` lens read model (NOT Core KV; P5). applicant scopes
// the rows to one applicant identity; omit it to list every open task. The tasks
// are self-describing (op name + description aspect-hopped by the lens), so the FE
// renders an actionable prompt and drives completion through POST /api/op.
func (s *server) handleTasks(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := orchestrationbase.MyTasksBucket
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
	applicant := strings.TrimSpace(r.URL.Query().Get("applicant"))
	rows := computeTasks(keys, get, applicant)
	s.writeJSON(w, http.StatusOK, map[string]any{"tasks": rows, "count": len(rows)})
}
