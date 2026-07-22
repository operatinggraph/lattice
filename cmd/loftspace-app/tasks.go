package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/operatinggraph/lattice/internal/substrate"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// myTasksRow is one projected `my-tasks` lens entry: a single identity's OPEN
// tasks. The lens (orchestration-base) keys one row per identity at
// my-tasks.<actorSuffix>; the actor-aggregate envelope stamps the anchor identity
// under the lens's ActorField — `assignee` — at the row root (NOT `actorKey`,
// which is the raw cypher RETURN alias the envelope wrapper renames), and carries
// the link-sourced, self-describing task list under `openTasks` (Contract #10
// §10.1 — task relationships are links, not fields). The applicant app reads THIS
// projection, never Core KV (P5): Loupe's /api/tasks scans Core KV only because it
// is the admin/debug inspector P5 carves out.
type myTasksRow struct {
	Assignee  string        `json:"assignee"`
	OpenTasks []openTaskRow `json:"openTasks"`
}

// openTaskRow is one open task inside a my-tasks row. operationName is the bound
// op's root operationType and operationDescription its optional .description
// aspect — both resolved by the lens off the forOperation meta-vertex, so the
// inbox renders a human label without a second read.
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

// tasksFromRow flattens ONE identity's open tasks out of a decoded `my-tasks`
// lens row into the FE's taskRow shape. The lens collect can leave a degenerate
// {taskKey:null} artifact when an identity has no open task; an entry without a
// taskKey is dropped. Rows sort by soonest expiry, then taskKey, for a stable,
// actionable order.
func tasksFromRow(mt myTasksRow) []taskRow {
	rows := make([]taskRow, 0, len(mt.OpenTasks))
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
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ExpiresAt != rows[j].ExpiresAt {
			return rows[i].ExpiresAt < rows[j].ExpiresAt
		}
		return rows[i].TaskKey < rows[j].TaskKey
	})
	return rows
}

// handleTasks implements GET /api/tasks — the AUTHENTICATED caller's own open-task
// inbox, read from the `my-tasks` lens read model (NOT Core KV; P5) at its own
// identity-keyed row (`my-tasks.identity.<subject>`). The actor comes ONLY from the
// verified JWT; there is no client-supplied applicant filter. The tasks are
// self-describing (op name + description aspect-hopped by the lens), so the FE
// renders an actionable prompt and drives completion through POST /api/op.
func (s *server) handleTasks(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := orchestrationbase.MyTasksBucket
	entry, err := conn.KVGet(ctx, bucket, bucket+".identity."+actor.Subject)
	if errors.Is(err, substrate.ErrKeyNotFound) {
		s.writeJSON(w, http.StatusOK, map[string]any{"tasks": []taskRow{}, "count": 0})
		return
	}
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"read "+bucket+": "+err.Error()+" (is orchestration-base installed and the Refractor projecting?)")
		return
	}
	var mt myTasksRow
	if json.Unmarshal(entry.Value, &mt) != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"tasks": []taskRow{}, "count": 0})
		return
	}
	rows := tasksFromRow(mt)
	s.writeJSON(w, http.StatusOK, map[string]any{"tasks": rows, "count": len(rows)})
}
