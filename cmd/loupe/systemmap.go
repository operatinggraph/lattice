package main

import (
	"sort"
	"time"
)

// mapNode is one vertex of the system map. Kind is "component" (a declared
// engine that heartbeats to Health KV), "client" (an undeclared heartbeat
// group discovered at runtime — e.g. a vertical app's reporter; rendered as a
// chip on the clients shelf, no skeleton edges), "lens" (a Refractor
// projection), or "infra" (a core stream / KV store — the spine the components
// hang off). Status carries the live overlay: a component/client is "green" /
// "stale" / "absent"; a lens carries its §4.2 renderedState ("projecting" /
// "lagging" / "paused" / "pending-readpath" / "rebuilding" / "fault" /
// "unknown"); infra is "present" (it exists if Loupe could read Health KV).
// Protected marks a read-path-authorized lens (spec-side truth — the ◆ tag
// renders in every state). Component/client nodes carry every live instance
// in Instances; the node-level Status is the worst instance's, Freshness the
// freshest, Detail the worst instance's id.
type mapNode struct {
	ID        string        `json:"id"`
	Label     string        `json:"label"`
	Kind      string        `json:"kind"`
	Status    string        `json:"status"`
	Detail    string        `json:"detail,omitempty"`
	Freshness string        `json:"freshness,omitempty"`
	Parent    string        `json:"parent,omitempty"`
	Protected bool          `json:"protected,omitempty"`
	Issues    []string      `json:"issues,omitempty"`
	Instances []mapInstance `json:"instances,omitempty"`
}

// mapInstance is one heartbeat of a component/client node — the per-instance
// truth behind the node's worst-of rollup.
type mapInstance struct {
	Instance  string   `json:"instance"`
	Status    string   `json:"status"`
	Freshness string   `json:"freshness"`
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
// Health KV overlay applied, plus the phase-gate chips and an overall rollup.
// It is self-truthing — the skeleton is curated, but every component's
// presence / freshness and every lens node is derived from Health KV at
// request time, never hardcoded.
type systemMap struct {
	Nodes   []mapNode `json:"nodes"`
	Edges   []mapEdge `json:"edges"`
	Gates   []mapGate `json:"gates"`
	Overall string    `json:"overall"`
}

// Node kinds.
const (
	nodeComponent = "component"
	nodeClient    = "client"
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
// topology. readEntry / resolveLens / resolveSpec / staleThreshold mirror
// computeHealth so the assembler is unit-testable without NATS. A declared
// component with no Health KV heartbeat renders "absent" (red); a stale
// heartbeat renders "stale" (yellow); each live lens becomes a node parented
// to Refractor carrying its renderedState — a pending-readpath lens
// contributes nothing to the rollup (it is expected fail-closed state, not
// degradation).
func computeSystemMap(
	keys []string,
	readEntry func(string) (map[string]any, bool),
	resolveLens func(id string) (name, desc string),
	resolveSpec func(id string) lensSpecInfo,
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

	// Index the live component heartbeats (all of them — a group may run
	// several instances) and lens reporters by group. Alert severity and the
	// bootstrap marker fold into the overall too — the map banner and the
	// topbar pill are the same rollup in two homes, and must never disagree
	// on one screen.
	beats := make(map[string][]instanceBeat)
	lensNodes := make([]mapNode, 0)
	bootstrapPresent := false

	for _, k := range keys {
		group, kind := classifyHealthKey(k)
		switch kind {
		case kindBootstrap:
			bootstrapPresent = true

		case kindAlert:
			doc, ok := readEntry(k)
			if !ok {
				continue
			}
			switch severity, _ := doc["severity"].(string); severity {
			case "error":
				worse(red)
			case "warning":
				worse(yellow)
			}

		case kindComponent:
			doc, ok := readEntry(k)
			if !ok {
				continue
			}
			b := instanceBeat{}
			if inst, ok := doc["instance"].(string); ok {
				b.instance = inst
			}
			b.status, b.freshness, b.issues, b.level = componentLiveness(doc, staleThreshold)
			b.ts, b.hasTS = componentHeartbeat(doc)
			beats[group] = append(beats[group], b)

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
			var spec lensSpecInfo
			if resolveSpec != nil {
				spec = resolveSpec(k)
			}
			var level int
			node.Status, node.Issues, level = lensRenderedState(doc, spec)
			node.Protected = spec.Protected || spec.GrantTable
			worse(level)
			lensNodes = append(lensNodes, node)
		}
	}

	nodes := make([]mapNode, 0, len(declaredComponents)+len(infraNodes)+len(lensNodes))
	nodes = append(nodes, infraNodes...)

	declared := make(map[string]bool, len(declaredComponents))
	taken := make(map[string]bool, len(declaredComponents)+len(infraNodes)+len(lensNodes))
	for _, in := range infraNodes {
		taken[in.ID] = true
	}
	for _, dc := range declaredComponents {
		declared[dc.id] = true
		taken[dc.id] = true
		node := mapNode{ID: dc.id, Label: dc.label, Kind: nodeComponent}
		if bs, ok := beats[dc.id]; ok {
			worse(applyBeats(&node, bs))
		} else {
			node.Status = "absent"
			node.Freshness = "-"
			worse(red)
		}
		nodes = append(nodes, node)
	}

	// Undeclared heartbeat groups (e.g. a vertical app's reporter) render as
	// client chips — same per-instance overlay, no skeleton edges. A group
	// whose name collides with an infra or lens node id is not rendered (a
	// duplicate node id would corrupt edge measurement), but its severity
	// still feeds the rollup — a live health signal is never silently
	// dropped. Clients degrade the rollup like any heartbeat; absence is
	// impossible by construction (they only exist while a heartbeat does).
	for _, ln := range lensNodes {
		taken[ln.ID] = true
	}
	clientIDs := make([]string, 0, len(beats))
	for group := range beats {
		if declared[group] {
			continue // already rolled up via its component node above
		}
		if taken[group] {
			var discard mapNode
			worse(applyBeats(&discard, beats[group]))
			continue
		}
		clientIDs = append(clientIDs, group)
	}
	sort.Strings(clientIDs)
	for _, id := range clientIDs {
		node := mapNode{ID: id, Label: id, Kind: nodeClient}
		worse(applyBeats(&node, beats[id]))
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

	if !bootstrapPresent {
		worse(red)
	}

	return systemMap{
		Nodes:   nodes,
		Edges:   edges,
		Gates:   computeGates(keys, readEntry),
		Overall: [...]string{"green", "yellow", "red"}[overall],
	}
}

// instanceBeat is one component/client heartbeat's derived overlay, kept with
// its parsed timestamp so freshest-of aggregation compares times, not strings.
type instanceBeat struct {
	instance  string
	freshness string
	status    string
	issues    []string
	level     int
	ts        time.Time
	hasTS     bool
}

// applyBeats fills a node's live overlay from its instance beats: Status /
// Detail / Issues come from the worst instance (first-seen breaking ties),
// Freshness from the freshest heartbeat, and Instances lists every beat sorted
// by instance id. Returns the worst severity level for the overall rollup.
func applyBeats(node *mapNode, bs []instanceBeat) int {
	if len(bs) == 0 {
		return sevGreen
	}
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].instance < bs[j].instance })
	wi := 0
	fi := 0
	for i, b := range bs {
		if b.level > bs[wi].level {
			wi = i
		}
		if b.hasTS && (!bs[fi].hasTS || b.ts.After(bs[fi].ts)) {
			fi = i
		}
	}
	node.Status = bs[wi].status
	node.Detail = bs[wi].instance
	node.Issues = bs[wi].issues
	node.Freshness = bs[fi].freshness
	node.Instances = make([]mapInstance, 0, len(bs))
	for _, b := range bs {
		node.Instances = append(node.Instances, mapInstance{
			Instance:  b.instance,
			Status:    b.status,
			Freshness: b.freshness,
			Issues:    b.issues,
		})
	}
	return bs[wi].level
}
