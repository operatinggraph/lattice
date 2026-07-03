package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
)

// componentInstance is one live heartbeat of a component — one card on the
// component page. Doc carries the raw Contract #5 heartbeat verbatim so the UI
// can render component-appropriate metrics without the server curating them.
type componentInstance struct {
	Key       string         `json:"key"`
	Instance  string         `json:"instance"`
	Status    string         `json:"status"`
	Freshness string         `json:"freshness"`
	Issues    []string       `json:"issues,omitempty"`
	Doc       map[string]any `json:"doc,omitempty"`
}

// componentEvent is one component-scoped Health KV event key
// (health.<comp>.<instance>.<kind>[.<qualifier>…]). Kind groups the events
// section; Tail is everything after the component segment (the readable key
// tail); Doc is the raw event document.
type componentEvent struct {
	Key       string         `json:"key"`
	Kind      string         `json:"kind"`
	Tail      string         `json:"tail"`
	Freshness string         `json:"freshness,omitempty"`
	Doc       map[string]any `json:"doc,omitempty"`
}

// componentPage is the GET /api/component/<id> response. Status is the
// worst-of across instances (the header pill); Control carries the component's
// allow-listed control-plane read replies (loom/weaver) so the page renders in
// one fetch — nil for components without list reads.
type componentPage struct {
	Component string                     `json:"component"`
	Label     string                     `json:"label"`
	Declared  bool                       `json:"declared"`
	Status    string                     `json:"status"`
	Instances []componentInstance        `json:"instances"`
	Events    []componentEvent           `json:"events"`
	Control   map[string]json.RawMessage `json:"control,omitempty"`
}

// eventTime extracts a best-effort timestamp from an event doc for newest-first
// ordering. Event emitters stamp different fields; all known ones are tried.
func eventTime(doc map[string]any) (time.Time, bool) {
	for _, field := range []string{"at", "timestamp", "emittedAt", "updatedAt", "heartbeatAt"} {
		if ts, ok := parseHealthTime(doc, field); ok {
			return ts, true
		}
	}
	return time.Time{}, false
}

// computeComponent assembles one component's page from the Health KV key set:
// every health.<id>.<instance> heartbeat becomes an instance card (plural — no
// last-write-wins collapse) and every deeper health.<id>.… key becomes an
// event row. Page status is the worst instance's status; a declared component
// with no heartbeats reports "absent".
func computeComponent(
	id string,
	keys []string,
	readEntry func(string) (map[string]any, bool),
	staleThreshold time.Duration,
) componentPage {
	page := componentPage{
		Component: id,
		Label:     id,
		Status:    "absent",
		Instances: []componentInstance{},
		Events:    []componentEvent{},
	}
	for _, dc := range declaredComponents {
		if dc.id == id {
			page.Label = dc.label
			page.Declared = true
			// A designAhead component with no heartbeat is "design-ahead"
			// (surface built, backend not yet deployed), matching its map
			// node; any live instance overwrites this with the worst-of.
			if dc.designAhead {
				page.Status = "design-ahead"
			}
		}
	}

	levels := make(map[string]int)
	for _, k := range keys {
		group, kind := classifyHealthKey(k)
		if group != id {
			continue
		}
		switch kind {
		case kindComponent:
			doc, ok := readEntry(k)
			if !ok {
				continue
			}
			inst := componentInstance{Key: k, Doc: doc}
			if s, ok := doc["instance"].(string); ok {
				inst.Instance = s
			}
			if inst.Instance == "" {
				inst.Instance = strings.TrimPrefix(k, "health."+id+".")
			}
			inst.Status, inst.Freshness, inst.Issues, levels[k] = componentLiveness(doc, staleThreshold)
			page.Instances = append(page.Instances, inst)

		case kindEvent:
			tail := strings.TrimPrefix(k, "health."+id+".")
			if tail == k {
				// A bare health.<id> key has no instance segment — not a
				// Contract #5 event key; skip rather than render garbage.
				continue
			}
			// The tail is <instance>.<kind>[.<qualifier>…] — the event kind is
			// the segment after the emitting instance.
			segs := strings.Split(tail, ".")
			ev := componentEvent{Key: k, Tail: tail}
			if len(segs) >= 2 {
				ev.Kind = segs[1]
			} else {
				ev.Kind = segs[0]
			}
			if doc, ok := readEntry(k); ok {
				ev.Doc = doc
				if ts, ok := eventTime(doc); ok {
					ev.Freshness = freshness(ts)
				}
			}
			page.Events = append(page.Events, ev)
		}
	}

	sort.Slice(page.Instances, func(i, j int) bool {
		if page.Instances[i].Instance != page.Instances[j].Instance {
			return page.Instances[i].Instance < page.Instances[j].Instance
		}
		return page.Instances[i].Key < page.Instances[j].Key
	})
	// Worst-of over the SORTED order so the header pill is deterministic and
	// agrees with the map node (applyBeats uses the same instance-id order).
	worstLevel := -1
	for _, inst := range page.Instances {
		if lvl := levels[inst.Key]; lvl > worstLevel {
			worstLevel = lvl
			page.Status = inst.Status
		}
	}
	// Events render newest-first; keys without a parseable timestamp sort after
	// dated ones, by key for stability.
	sort.SliceStable(page.Events, func(i, j int) bool {
		ti, oki := eventTime(page.Events[i].Doc)
		tj, okj := eventTime(page.Events[j].Doc)
		if oki != okj {
			return oki
		}
		if oki && !ti.Equal(tj) {
			return ti.After(tj)
		}
		return page.Events[i].Key < page.Events[j].Key
	})
	return page
}

// handleComponent implements GET /api/component/<id>: the component's plural
// instances + events from Health KV, plus (for components with list reads) the
// control-plane read replies the page's control column renders.
func (s *server) handleComponent(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	id := strings.Trim(r.URL.Path[len("/api/component/"):], "/")
	if id == "" || strings.ContainsAny(id, "./") {
		s.writeError(w, http.StatusBadRequest,
			"expected GET /api/component/<id> — a component id is a single path segment with no '.' or '/'")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	readEntry := func(k string) (map[string]any, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, k)
		if err != nil {
			return nil, false
		}
		var doc map[string]any
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return nil, false
		}
		return doc, true
	}

	page := computeComponent(id, keys, readEntry, staleThreshold)

	// Attach the component's control-plane reads (same subjects controlRead
	// serves) so the page needs one fetch. A per-subject failure degrades to
	// that read's {"error": …}, not a failed page.
	if reads, ok := readSubjects(id); ok && len(reads) > 0 {
		page.Control = make(map[string]json.RawMessage, len(reads))
		for name, subject := range reads {
			rctx, rcancel := s.reqContext(r)
			raw, err := s.controlRequest(rctx, conn, subject)
			rcancel()
			if err != nil {
				page.Control[name] = mustJSON(map[string]string{"error": err.Error()})
				continue
			}
			if !json.Valid(raw) {
				// A malformed plane reply must degrade to this read's error,
				// not corrupt the whole page's JSON encoding mid-stream.
				page.Control[name] = mustJSON(map[string]string{"error": "control plane returned non-JSON reply"})
				continue
			}
			page.Control[name] = raw
		}
	}
	s.writeJSON(w, http.StatusOK, page)
}
