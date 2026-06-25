package main

import (
	"sort"
	"time"
)

// mapNode is one vertex of the system map. Kind is "component" (an engine that
// heartbeats to Health KV), "lens" (a Refractor projection), or "infra" (a core
// stream / KV store — the spine the components hang off). Status carries the
// live overlay: a component is "green" / "stale" / "absent"; a lens reuses the
// Health-tab vocabulary ("active" / "yellow" / "paused" / "rebuilding" /
// "unknown"); infra is "present" (it exists if Loupe could read Health KV).
type mapNode struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Detail    string   `json:"detail,omitempty"`
	Freshness string   `json:"freshness,omitempty"`
	Parent    string   `json:"parent,omitempty"`
	Issues    []string `json:"issues,omitempty"`
}

// mapEdge is a directed data-flow edge between two node ids.
type mapEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// systemMap is the GET /api/systemmap response: the canonical component
// topology (the deployed subset of architecture-overview.md) with the live
// Health KV overlay applied, plus an overall rollup. It is self-truthing — the
// skeleton is curated, but every component's presence / freshness and every
// lens node is derived from Health KV at request time, never hardcoded.
type systemMap struct {
	Nodes   []mapNode `json:"nodes"`
	Edges   []mapEdge `json:"edges"`
	Overall string    `json:"overall"`
}

// Node kinds.
const (
	nodeComponent = "component"
	nodeLens      = "lens"
	nodeInfra     = "infra"
)

const refractorID = "refractor"

// declaredComponent is an engine the deployment is expected to run. Its id is
// the Health KV group name (classifyHealthKey) so the overlay matches by id.
type declaredComponent struct {
	id    string
	label string
}

// declaredComponents is the engine set that heartbeats to Health KV
// (architecture-overview.md, "heartbeat" edges). Order is the render order.
var declaredComponents = []declaredComponent{
	{"processor", "Processor"},
	{refractorID, "Refractor"},
	{"weaver", "Weaver"},
	{"loom", "Loom"},
	{"bridge", "Bridge"},
	{"object-store-manager", "Object Store Mgr"},
}

// infraNodes is the core stream / store spine the components flow through.
var infraNodes = []mapNode{
	{ID: "core-operations", Label: "core-operations", Kind: nodeInfra, Status: "present"},
	{ID: "core-events", Label: "core-events", Kind: nodeInfra, Status: "present"},
	{ID: "core-kv", Label: "Core KV", Kind: nodeInfra, Status: "present"},
}

// skeletonEdges is the canonical data flow (architecture-overview.md §"data
// flow"): operations land on core-operations → Processor commits to Core KV and
// publishes business events to core-events → Loom / Weaver / Bridge /
// object-store-manager consume core-events and submit new ops back; the
// Refractor is the CDC materializer, projecting lenses off Core KV's backing
// stream; a Loom externalTask dispatches through the Bridge. Lens edges
// (refractor → <lens>) are added per live lens in computeSystemMap.
var skeletonEdges = []mapEdge{
	{From: "core-operations", To: "processor", Label: "ops"},
	{From: "processor", To: "core-kv", Label: "commit"},
	{From: "processor", To: "core-events", Label: "outbox"},
	{From: "core-events", To: "loom", Label: "consume"},
	{From: "core-events", To: "weaver", Label: "consume"},
	{From: "core-events", To: "bridge", Label: "consume"},
	{From: "core-events", To: "object-store-manager", Label: "consume"},
	{From: "loom", To: "core-operations", Label: "submit ops"},
	{From: "weaver", To: "core-operations", Label: "submit ops"},
	{From: "bridge", To: "core-operations", Label: "submit ops"},
	{From: "object-store-manager", To: "core-operations", Label: "submit ops"},
	{From: "core-kv", To: refractorID, Label: "CDC"},
	{From: "loom", To: "bridge", Label: "externalTask"},
}

// computeSystemMap overlays the live Health KV state onto the canonical
// topology. readEntry / resolveLens / staleThreshold mirror computeHealth so the
// assembler is unit-testable without NATS. A declared component with no Health
// KV heartbeat renders "absent" (red); a stale heartbeat renders "stale"
// (yellow); each live lens becomes a node parented to Refractor.
func computeSystemMap(
	keys []string,
	readEntry func(string) (map[string]any, bool),
	resolveLens func(id string) (name, desc string),
	staleThreshold time.Duration,
) systemMap {
	const (
		green  = 0
		yellow = 1
		red    = 2
	)
	overall := green
	worse := func(lvl int) {
		if lvl > overall {
			overall = lvl
		}
	}

	// Index the live component heartbeats and lens reporters by group.
	type beat struct {
		instance  string
		freshness string
		status    string
		issues    []string
	}
	beats := make(map[string]beat)
	lensNodes := make([]mapNode, 0)

	for _, k := range keys {
		group, kind := classifyHealthKey(k)
		switch kind {
		case kindComponent:
			doc, ok := readEntry(k)
			if !ok {
				continue
			}
			b := beat{}
			if inst, ok := doc["instance"].(string); ok {
				b.instance = inst
			}
			if ts, ok := componentHeartbeat(doc); ok {
				b.freshness = freshness(ts)
				if time.Since(ts) > staleThreshold {
					b.status = "stale"
					b.issues = append(b.issues, "heartbeat older than "+staleThreshold.String())
				} else {
					b.status = "green"
				}
			} else {
				b.status = "unknown"
				b.freshness = "-"
			}
			beats[group] = b

		case kindLens:
			doc, ok := readEntry(k)
			if !ok {
				continue
			}
			node := mapNode{ID: k, Label: k, Kind: nodeLens, Parent: refractorID, Detail: "lens"}
			if resolveLens != nil {
				if name, desc := resolveLens(k); name != "" {
					node.Label = name
					if desc != "" {
						node.Detail = desc
					}
				}
			}
			node.Status, node.Issues = lensStatus(doc)
			if node.Status != "active" {
				worse(yellow)
			}
			lensNodes = append(lensNodes, node)
		}
	}

	nodes := make([]mapNode, 0, len(declaredComponents)+len(infraNodes)+len(lensNodes))
	nodes = append(nodes, infraNodes...)

	for _, dc := range declaredComponents {
		node := mapNode{ID: dc.id, Label: dc.label, Kind: nodeComponent}
		if b, ok := beats[dc.id]; ok {
			node.Status = b.status
			node.Detail = b.instance
			node.Freshness = b.freshness
			node.Issues = b.issues
			if b.status != "green" {
				worse(yellow)
			}
		} else {
			node.Status = "absent"
			node.Freshness = "-"
			worse(red)
		}
		nodes = append(nodes, node)
	}

	sort.Slice(lensNodes, func(i, j int) bool {
		if lensNodes[i].Label != lensNodes[j].Label {
			return lensNodes[i].Label < lensNodes[j].Label
		}
		return lensNodes[i].ID < lensNodes[j].ID
	})
	nodes = append(nodes, lensNodes...)

	edges := make([]mapEdge, 0, len(skeletonEdges)+len(lensNodes))
	edges = append(edges, skeletonEdges...)
	for _, ln := range lensNodes {
		edges = append(edges, mapEdge{From: refractorID, To: ln.ID, Label: "project"})
	}

	return systemMap{
		Nodes:   nodes,
		Edges:   edges,
		Overall: [...]string{"green", "yellow", "red"}[overall],
	}
}

// lensStatus maps a lens reporter's Health KV doc to the map vocabulary,
// mirroring computeHealth's lens branch: an active lens with consumer lag or a
// nonzero error count is "yellow"; paused / rebuilding pass through; anything
// else is "unknown".
func lensStatus(doc map[string]any) (status string, issues []string) {
	raw, _ := doc["status"].(string)
	consumerLag, _ := doc["consumerLag"].(float64)
	errorCount, _ := doc["errorCount"].(float64)
	switch raw {
	case "active":
		status = "active"
		if consumerLag > 0 {
			status = "yellow"
			issues = append(issues, "consumerLag")
		}
	case "paused", "rebuilding":
		status = raw
	default:
		status = "unknown"
	}
	if errorCount > 0 {
		if status == "active" {
			status = "yellow"
		}
		issues = append(issues, "errorCount")
	}
	return status, issues
}
