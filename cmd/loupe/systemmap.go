package main

import (
	"sort"
	"time"
)

// mapNode is one vertex of the system map. Kind is "component" (a declared
// engine that heartbeats to Health KV), "app" (a curated vertical on the F14
// door band — clinic-app, loftspace-app), "client" (an undeclared heartbeat
// group discovered at runtime; rendered as a chip on the clients shelf, no
// skeleton edges), "lens" (a Refractor projection), "infra" (a core stream /
// KV store — the spine the components hang off), or "ingress" (the
// external-actors marker above the door band — a plain non-interactive chip,
// no heartbeat). Status carries the live overlay: a component/client/app is
// "green" / "stale" / "absent" — or "offline" for a declared app with no
// heartbeat or an optional up-full-only component not running in this stack
// (workload not started; informational, never degrades the rollup); a lens
// carries its §4.2 renderedState ("projecting" /
// "lagging" / "paused" / "pending-readpath" / "rebuilding" / "fault" /
// "unknown"); infra/ingress is "present" (it exists if Loupe could read
// Health KV). Protected marks a read-path-authorized lens (spec-side truth —
// the ◆ tag renders in every state). Lateral marks a component the map places
// beside its anchor (Vault beside Core KV) instead of in its tier row.
// Component/client/app nodes carry every live instance in Instances; the
// node-level Status is the worst instance's, Freshness the freshest, Detail
// the worst instance's id.
type mapNode struct {
	ID        string        `json:"id"`
	Label     string        `json:"label"`
	Kind      string        `json:"kind"`
	Status    string        `json:"status"`
	Detail    string        `json:"detail,omitempty"`
	Freshness string        `json:"freshness,omitempty"`
	Parent    string        `json:"parent,omitempty"`
	Protected bool          `json:"protected,omitempty"`
	Lateral   bool          `json:"lateral,omitempty"`
	Issues    []string      `json:"issues,omitempty"`
	Instances []mapInstance `json:"instances,omitempty"`
	// ActiveSequence is a lens node's reporter activeSequence — the NATS
	// sequence of the ACTIVE RULE VERSION (it advances on rule
	// activation/update, not on row projection). The pulse feed diffs it
	// between polls to surface rule updates.
	ActiveSequence uint64 `json:"activeSequence,omitempty"`
	// Pkg is a lens node's owning package canonical name — the F14 cluster
	// grouping key (loupe-map-scale-ux.md §1); "kernel" for a lens no
	// installed manifest claims. PkgKey is the owning package's vertex key,
	// empty for the kernel group (which links to the Refractor roster
	// instead of a package page).
	Pkg    string `json:"pkg,omitempty"`
	PkgKey string `json:"pkgKey,omitempty"`
}

// mapInstance is one heartbeat of a component/client node — the per-instance
// truth behind the node's worst-of rollup.
type mapInstance struct {
	Instance  string   `json:"instance"`
	Status    string   `json:"status"`
	Freshness string   `json:"freshness"`
	Issues    []string `json:"issues,omitempty"`
}

// mapEdge is a directed data-flow edge between two node ids. DesignAhead
// marks a door-band edge that draws dashed — the ratified end-state route
// (gateway-external-trust-boundary-design.md F5) a vertical's traffic hasn't
// adopted yet.
type mapEdge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Label       string `json:"label,omitempty"`
	DesignAhead bool   `json:"designAhead,omitempty"`
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
	nodeIngress   = "ingress"
	nodeApp       = "app"
)

const refractorID = "refractor"

// kernelGroup is the curated lens-cluster fallback for a lens no installed
// package manifest claims — the bootstrap kernel-seed family.
const kernelGroup = "kernel"

// The F14 door-band edge labels, applied once across the declaredApps set
// (the returnLabelled label-once precedent) — every other app's matching
// edge carries an empty label so the pair doesn't repeat the same text twice.
const (
	doorDirectLabel  = "submit ops · direct (today)"
	doorGatewayLabel = "user writes · end-state"
)

// declaredComponent is an engine the deployment is expected to run. Its id is
// the Health KV group name (classifyHealthKey) so the overlay matches by id.
// optional marks a component the deployment runs only under `make up-full`
// (Gateway/Vault/Chronicler), not kernel-only `make up`: with no heartbeat and
// never-seen-alive it renders the informational "offline" state (dim, no rollup
// contribution — the pending-readpath precedent). Once it has heartbeated this
// process, a later disappearance is an honest absent-red (deployed-then-crashed,
// via everLive), so the flag is moot for a component that is currently live.
type declaredComponent struct {
	id       string
	label    string
	optional bool
}

// declaredComponents is the engine set that heartbeats to Health KV
// (architecture-overview.md, "heartbeat" edges). Order is the render order.
// Gateway/Vault/Chronicler are optional: deployed and heartbeating under
// `make up-full`, skipped by kernel-only `make up`, so their absence in a
// kernel-only stack is honest "offline", not degradation.
var declaredComponents = []declaredComponent{
	{id: "processor", label: "Processor"},
	{id: refractorID, label: "Refractor"},
	{id: "weaver", label: "Weaver"},
	{id: "loom", label: "Loom"},
	{id: "bridge", label: "Bridge"},
	{id: "object-store-manager", label: "Object Store Mgr"},
	{id: "gateway", label: "Gateway", optional: true},
	{id: "vault", label: "Vault", optional: true},
	{id: "chronicler", label: "Chronicler", optional: true},
}

// infraNodes is the core stream / store spine the components flow through.
// object-store is the archive plane (objects-base) — distinct from the
// object-store-manager component that manages it.
var infraNodes = []mapNode{
	{ID: "core-operations", Label: "core-operations", Kind: nodeInfra, Status: "present"},
	{ID: "core-events", Label: "core-events", Kind: nodeInfra, Status: "present"},
	{ID: "core-kv", Label: "Core KV", Kind: nodeInfra, Status: "present"},
	{ID: "object-store", Label: "Object Store", Kind: nodeInfra, Status: "present"},
}

// ingressNodes marks where external traffic enters: the non-interactive
// external-actors chip above the Gateway (tier -1, the door band).
var ingressNodes = []mapNode{
	{ID: "external", Label: "external actors · Bearer JWT", Kind: nodeIngress, Status: "present"},
}

// declaredApp is a vertical application curated onto the door band
// (loupe-map-scale-ux.md §2) — the map stays curated, so adding a vertical is
// a one-line edit here, mirroring declaredComponents.
type declaredApp struct {
	id    string
	label string
}

// declaredApps are the vertical apps curated onto the ingress door band.
// Verticals are optional workloads: a declared app with no heartbeat renders
// "offline" (dim, zero rollup contribution), never absent-red — kernel-only
// `make up` must stay green regardless of which verticals are running.
var declaredApps = []declaredApp{
	{id: "clinic-app", label: "Clinic"},
	{id: "loftspace-app", label: "LoftSpace"},
	{id: "cafe-app", label: "Café"},
	{id: "wellness-app", label: "Wellness"},
}

// skeletonEdges is the canonical data flow (architecture-overview.md §"data
// flow"): operations land on core-operations → Processor commits to Core KV and
// publishes business events to core-events → Loom / Weaver / Bridge /
// object-store-manager consume core-events and submit new ops back; the
// Refractor is the CDC materializer, projecting lenses off Core KV's backing
// stream; a Loom externalTask dispatches through the Bridge. Per-lens edges
// are retired (F14) — the lens shelf clusters by package client-side and
// draws one synthetic refractor→cluster edge itself; the door-band edges for
// declaredApps are appended in computeSystemMap.
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
	// Gateway — the external door (write-path only).
	{From: "external", To: "gateway", Label: ""},
	{From: "gateway", To: "core-operations", Label: "stamp + publish"},
	// Vault — key custody, lateral to Core KV (Processor encrypts on commit,
	// decrypts on read).
	{From: "processor", To: "vault", Label: "encrypt / decrypt"},
	// Chronicler — the mirror materializer: inbound from every stream,
	// outbound to its history read-models + (archive mode) the object store.
	{From: "core-operations", To: "chronicler", Label: "archive"},
	{From: "core-events", To: "chronicler", Label: "history"},
	{From: "core-kv", To: "chronicler", Label: "CDC"},
	{From: "chronicler", To: "object-store", Label: "archive segments"},
}

// computeSystemMap overlays the live Health KV state onto the canonical
// topology. readEntry / resolveLens / resolveSpec / staleThreshold mirror
// computeHealth so the assembler is unit-testable without NATS. A declared
// component with no Health KV heartbeat renders "absent" (red) — or
// "offline" (informational, no rollup contribution) when it is declared
// optional AND has never been seen alive by this process (everLive):
// heartbeat keys TTL out of Health KV, so without that memory a
// deployed-then-crashed optional component would silently revert to a
// false "offline" instead of an honest absent-red. A stale heartbeat
// renders "stale" (yellow); each live lens becomes a node parented to
// Refractor carrying its renderedState — a pending-readpath lens contributes
// nothing to the rollup (it is expected fail-closed state, not degradation).
// pkgIndex is the once-per-poll reverse index from lens id to owning package
// (buildLensPackageIndex) that stamps each lens node's Pkg/PkgKey.
func computeSystemMap(
	keys []string,
	readEntry func(string) (map[string]any, bool),
	resolveLens func(id string) (name, desc string),
	resolveSpec func(id string) lensSpecInfo,
	staleThreshold time.Duration,
	everLive map[string]bool,
	pkgIndex map[string]lensPackageRef,
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
			if ref, ok := pkgIndex[k]; ok {
				node.Pkg, node.PkgKey = ref.Name, ref.Key
			} else {
				node.Pkg = kernelGroup
			}
			if seq, ok := doc["activeSequence"].(float64); ok && seq > 0 {
				node.ActiveSequence = uint64(seq)
			}
			worse(level)
			lensNodes = append(lensNodes, node)
		}
	}

	nodes := make([]mapNode, 0, len(declaredComponents)+len(infraNodes)+len(ingressNodes)+len(lensNodes))
	nodes = append(nodes, infraNodes...)
	nodes = append(nodes, ingressNodes...)

	declared := make(map[string]bool, len(declaredComponents))
	taken := make(map[string]bool, len(declaredComponents)+len(infraNodes)+len(ingressNodes)+len(lensNodes))
	for _, in := range infraNodes {
		taken[in.ID] = true
	}
	for _, in := range ingressNodes {
		taken[in.ID] = true
	}
	for _, dc := range declaredComponents {
		declared[dc.id] = true
		taken[dc.id] = true
		node := mapNode{ID: dc.id, Label: dc.label, Kind: nodeComponent, Lateral: dc.id == "vault"}
		if bs, ok := beats[dc.id]; ok {
			worse(applyBeats(&node, bs))
		} else if dc.optional && !everLive[dc.id] {
			node.Status = "offline"
			node.Detail = "up-full only"
			node.Freshness = "-"
		} else {
			node.Status = "absent"
			node.Freshness = "-"
			worse(red)
		}
		nodes = append(nodes, node)
	}

	// F14 door band: declared vertical apps overlay heartbeats exactly like
	// components, but a missing heartbeat renders "offline" (never
	// absent-red, no rollup contribution) — verticals are optional
	// workloads, unlike the platform engines above.
	for _, da := range declaredApps {
		declared[da.id] = true
		taken[da.id] = true
		node := mapNode{ID: da.id, Label: da.label, Kind: nodeApp}
		if bs, ok := beats[da.id]; ok {
			worse(applyBeats(&node, bs))
		} else {
			node.Status = "offline"
			node.Freshness = "-"
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

	edges := make([]mapEdge, 0, len(skeletonEdges)+len(declaredApps)*3)
	edges = append(edges, skeletonEdges...)
	for i, da := range declaredApps {
		edges = append(edges, mapEdge{From: "external", To: da.id})
		direct, gateway := "", ""
		if i == 0 {
			direct, gateway = doorDirectLabel, doorGatewayLabel
		}
		edges = append(edges, mapEdge{From: da.id, To: "core-operations", Label: direct})
		edges = append(edges, mapEdge{From: da.id, To: "gateway", Label: gateway, DesignAhead: true})
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
