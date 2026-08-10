// Package taxonomy resolves a lens label's `*` expansion sigil against the
// platform's dynamic subtypeOf graph (dynamic-type-taxonomy-design.md §4).
//
// A Resolver holds a snapshot of every declared vertexType meta vertex
// (canonicalName, abstract flag) plus the subtypeOf edges between them, and
// answers Expand with the reflexive-transitive downward closure a `*`-suffixed
// label admits. Evaluation-time matching stays a pure set-membership test
// (§5.3): the resolver is consulted once per lens activation/re-derivation
// (internal/refractor/pipeline's useFullEngineBranches), never per binding.
//
// This package is the resolver + its resolve-time cycle/depth authority
// (§14 Fire A item 3). Loading a live snapshot from Core KV and arming the
// resolver is driven by cmd/refractor/main.go, fed by
// internal/refractor/lens.CoreKVSource's taxonomy callback (item 4, §6.1):
// on every taxonomy-relevant CDC event the source rebuilds and hands over
// a []TypeSnapshot, which main.go installs via InstallSnapshot.
//
// Arming is a SEPARATE signal from that snapshot, and deliberately so. The
// two halves of §4.2's armed test answer two different questions — "do I
// have a graph to walk" (InstallSnapshot) and "is the consumer that feeds me
// live AND current" (SetArmed) — and only the source that owns the consumer
// can answer the second. It reports it through main.go's liveness callback:
// true once its per-boot durable has drained the whole boot replay (so the
// snapshot describes the WHOLE taxonomy, not the prefix of it that had been
// replayed so far), false the moment the NATS connection drops under it or
// its subscription dies. A Resolver constructed by New and never touched
// again stays permanently disarmed with no snapshot, which is the
// fail-closed posture a `*`-carrying lens sees before its process's
// CoreKVSource has delivered a first taxonomy event.
//
// # Only a `*`-carrying label is a taxonomy vocabulary lookup
//
// This resolver is consulted for a label ONLY when the query pattern that
// names it carries the `*` sigil (full.CompiledRule.ExpansionLabels — every
// caller in internal/refractor/pipeline builds its input set from exactly
// that). A BARE label (no `*`) is never passed to Expand at all: it stays an
// uninterpreted vertex key-type string, matched structurally by the existing
// address-comparison sites (executor.go's nodeMatches and its five
// siblings).
//
// This is deliberate, not an oversight this package's own vocabulary could
// close. A lens node label names a vertex KEY-TYPE segment; this resolver's
// vocabulary is keyed by the CANONICALNAME of a meta.ddl.vertexType meta
// vertex — and the two namespaces are provably different in the live corpus.
// `packages/maintenance-domain/ddls.go` declares CanonicalName "workOrder"
// for instances keyed `vtx.workorder.<id>` (canonicalName and key-type
// segment differ only in case, but differ). Several live lens labels —
// `role`, `permission`, `retentionclass`, `identityindex`, `meta` — name
// kernel/primordial types with no package-declared vertexType meta under
// that name at all, so this resolver's byName map would never contain them
// regardless of how complete the taxonomy graph itself is. A runtime
// "unresolvable bare label ⇒ refuse activation" gate would therefore be
// unable to distinguish an author's typo from a perfectly correct kernel
// label: it cannot tell "unknown because this resolver's vocabulary is
// incomplete with respect to key types" from "unknown because nothing was
// ever meant to exist here" — and refusing on the former takes live,
// correct lenses dark (a census found this reachable across at least
// clinic-domain, edge-manifest, identity-domain, privacy-base and
// capability-author). Silent-empty-match is a real hazard for a bare label
// too, but the fix for it is authoring-time, not this resolver: a
// lint/install-time check over lens specs against an authoritative key-type
// vocabulary the platform does not yet have, once one exists. Until then a
// bare label is out of this package's scope entirely.
package taxonomy

import (
	"fmt"
	"sort"
	"sync"
)

// maxDepth bounds the downward closure walk, mirroring
// internal/pkgmgr/taxonomy.go's maxTaxonomyDepth and its stated reason: a
// bound plus a visited-path set keeps the walk terminating and abuse-proof
// against a crafted or accidentally very-long chain. The resolver never
// trusts pkgmgr's install-time acyclicity check — a snapshot can in
// principle observe a graph pkgmgr itself would have refused (an uninstall
// race, a future direct-KV fault), so resolve-time detection is the
// authority (§14 Fire A item 3), not a redundant belt-and-braces check.
const maxDepth = 4

// Status reports how much a Resolver's answer to Expand can be trusted — a
// two-tier fail-closed posture (§4.2). Broad delivery (a non-exhaustive
// JetStream filter) only rescues events that still reach the matcher; it
// does nothing for a matcher evaluating against an expansion set that is
// itself wrong, so "known but maybe stale" and "not known at all" are
// different statuses with different consequences — the first may run
// broad, the second must refuse to activate.
type Status int

const (
	// StatusUnknown means the expansion cannot be trusted at any filter
	// width: no snapshot has ever been installed, one of the requested
	// labels does not resolve to a declared type, or the downward walk for
	// one of them hit a cycle or exceeded maxDepth. The caller must refuse
	// activation rather than publish a rule state that could silently
	// project the wrong row set.
	StatusUnknown Status = iota
	// StatusStale means a snapshot is loaded and every requested label
	// resolves to a well-defined set, but the invalidation consumer is not
	// live — the answer is correct as of the last snapshot load, not
	// guaranteed current against a taxonomy edit landing right now. The
	// caller may activate, degraded to a broad (non-exhaustive) filter.
	StatusStale
	// StatusArmed means a snapshot is loaded, every requested label
	// resolves, and the invalidation consumer is live: the answer is fully
	// current and the caller may treat it as exhaustive.
	StatusArmed
)

// typeInfo is one type meta vertex's resolved identity.
type typeInfo struct {
	canonicalName string
	abstract      bool
}

// Resolver holds a Refractor-local snapshot of the platform's subtypeOf
// taxonomy. The zero value returned by New has no snapshot and is not
// armed — Expand on it always answers StatusUnknown.
//
// The corpus DOES declare an abstract type: location-domain ships `location`
// with its unit/building/property leaves, and service-location's
// capabilityServiceAccess is the first lens that expands against it. So a
// pipeline whose rule carries a `*` label depends on this resolver having been
// fed, and StatusUnknown is a REFUSAL to activate rather than an inert
// pass-through (§4.2). A rule with no `*` anywhere never calls Expand at all
// and is unaffected either way.
type Resolver struct {
	mu sync.RWMutex

	byID          map[string]typeInfo // meta NanoID -> resolved identity
	byName        map[string]string   // canonicalName -> meta NanoID
	childrenOf    map[string][]string // parent NanoID -> child NanoIDs (subtypeOf edges, downward)
	poisoned      map[string]bool     // meta NanoID -> its CanonicalName collided with another entry's
	poisonedNames map[string]bool     // canonicalName -> true, for a name deleted from byName above: lets a DIRECT query of that name report the ambiguity rather than "unknown"

	// loaded records that a snapshot has been installed at least once. Set
	// by InstallSnapshot, never cleared: a resolver that has once seen the
	// graph keeps answering from its last snapshot, which is the whole point
	// of StatusStale existing as a tier distinct from StatusUnknown.
	loaded bool
	// consumerLive is the other half of §4.2's armed test: the claim that the
	// invalidation consumer feeding InstallSnapshot is alive AND has nothing
	// left to deliver, so the loaded snapshot is current rather than merely
	// last-known.
	//
	// Its lifetime, at every boundary, because a latch with no stated one is
	// how a false StatusArmed gets built:
	//   - starts false — New ships disarmed, and nothing here ever infers
	//     liveness from having been fed;
	//   - set true ONLY by SetArmed(true), which its own doc bounds to a
	//     caller that has proved the feed drained;
	//   - set false by SetArmed(false) on a connection loss or a dead
	//     subscription, from whatever goroutine observes it — disarming is
	//     the always-safe direction, so it is never deferred for ordering;
	//   - CARRIED, deliberately, across InstallSnapshot (see its doc);
	//   - reset on a process restart by construction — a new process builds a
	//     new Resolver via New, so nothing survives a restart and the boot
	//     replay must re-prove liveness from scratch.
	consumerLive bool
}

// New returns a Resolver with no snapshot loaded and not armed.
func New() *Resolver {
	return &Resolver{}
}

// TypeSnapshot is one type meta vertex as InstallSnapshot consumes it.
type TypeSnapshot struct {
	// ID is the type meta vertex's NanoID (vtx.meta.<ID>).
	ID string
	// CanonicalName is the value of its .canonicalName aspect — the name a
	// lens pattern's label names.
	CanonicalName string
	// Abstract mirrors MetaVertexRef.Abstract (internal/processor/ddl_cache.go):
	// true means this type names no instance (§3.2).
	Abstract bool
	// SubtypeOf lists the canonicalNames of every type this one declares a
	// subtypeOf link to (§3.4 — multiple parents are allowed).
	SubtypeOf []string
}

// InstallSnapshot replaces the resolver's whole taxonomy with snap under one
// write lock, so a concurrent Expand never observes a half-loaded graph.
// cmd/refractor/main.go's taxonomy callback is the production caller,
// invoked with the snapshot internal/refractor/lens.CoreKVSource rebuilds
// from the meta-watch's boot replay and every live subtypeOf/type edit
// thereafter (§6.1).
//
// A SubtypeOf entry naming a canonicalName absent from snap contributes no
// edge — mirroring §3.5's uninstall hazard posture (a link whose target meta
// is absent or tombstoned does not contribute), never a guess. An entry with
// an empty CanonicalName is skipped outright: no query label can ever be the
// empty string (the visitor/query grammar never produces one), so admitting
// it into byName would make an empty-string label spuriously resolvable.
//
// A CanonicalName shared by two or more entries in snap is made
// UNRESOLVABLE rather than resolved to an arbitrary last-write-wins winner —
// naively keeping one would silently drop the OTHER registrant's whole
// subtree from every Expand answer while still reporting StatusArmed/
// StatusStale, the same class of silent-narrowing hazard the resolve-time
// cycle/depth authority exists to catch. A rename lands as create-new +
// tombstone-old, so item 4's incremental rebuild has a real window where
// both are briefly live; this makes that window fail closed instead of
// guessing.
//
// The two colliding entries are unresolvable EVERYWHERE, not merely as a
// direct query label: byID (keyed by NanoID, so it keeps both) is what
// collectDownLocked reads while walking an unrelated ancestor's closure, and
// without a separate guard a colliding entry reached that way would still
// silently contribute — or, if concrete, add — its (ambiguous) canonicalName
// to that ancestor's answer, an asymmetry between "queried directly" and
// "reached transitively" that byName's deletion alone does not close. Every
// colliding entry's NanoID is recorded in poisoned; collectDownLocked
// refuses (§ below) the instant it reaches one, on any path, concrete or
// abstract, leaf or not — so a collision is unresolvable both ways, never
// one and not the other.
//
// The collided NAME itself survives in poisonedNames even though byName no
// longer carries it: a direct query of that name still needs to say WHY it
// is unresolvable (an ambiguous canonicalName) rather than report the
// generic "does not resolve to any declared vertex type" a genuine typo
// gets — the whole point of a reason string is that it names the real
// cause, and "unresolvable" and "ambiguous" are different causes an
// operator acts on differently.
func (r *Resolver) InstallSnapshot(snap []TypeSnapshot) {
	byID := make(map[string]typeInfo, len(snap))
	byName := make(map[string]string, len(snap))
	nameCount := make(map[string]int, len(snap))
	for _, s := range snap {
		if s.CanonicalName == "" {
			continue
		}
		byID[s.ID] = typeInfo{canonicalName: s.CanonicalName, abstract: s.Abstract}
		byName[s.CanonicalName] = s.ID
		nameCount[s.CanonicalName]++
	}
	poisoned := make(map[string]bool)
	poisonedNames := make(map[string]bool)
	for name, count := range nameCount {
		if count <= 1 {
			continue
		}
		delete(byName, name)
		poisonedNames[name] = true
	}
	for _, s := range snap {
		if s.CanonicalName != "" && nameCount[s.CanonicalName] > 1 {
			poisoned[s.ID] = true
		}
	}

	childrenOf := make(map[string][]string)
	for _, s := range snap {
		if s.CanonicalName == "" {
			// Never registered in byID above — leaving it as a phantom
			// child here would make collectDownLocked's byID lookup fail
			// and take the whole containing query to StatusUnknown instead
			// of this one dangling entry simply not contributing.
			continue
		}
		for _, parentName := range s.SubtypeOf {
			parentID, ok := byName[parentName]
			if !ok {
				continue
			}
			childrenOf[parentID] = append(childrenOf[parentID], s.ID)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID = byID
	r.byName = byName
	r.childrenOf = childrenOf
	r.poisoned = poisoned
	r.poisonedNames = poisonedNames
	r.loaded = true
	// consumerLive is deliberately untouched. It describes the FEED, not the
	// snapshot: every snapshot reaching this method was rebuilt and handed
	// over by that same feed, so a new one is evidence the feed is working,
	// never a reason to doubt it. Clearing it here would also make the flag
	// unusable in the shape §4.2 needs — the consumer reports liveness on
	// EDGES (drained; connection lost), so a reload that cleared the flag
	// would leave every taxonomy edit after the first permanently reporting
	// StatusStale with no event left to re-arm it. What actually guards
	// against a stale true is that nothing infers liveness from being fed:
	// only SetArmed moves it, and the mid-replay snapshots this method
	// installs during a boot replay arrive while it is still false.
}

// SetArmed marks the resolver's invalidation consumer live (true) or not
// (false) — the other half of §4.2's "armed" test alongside a loaded
// snapshot. A Resolver never calls this itself; the CDC consumer that feeds
// InstallSnapshot owns the signal.
//
// What true has to mean, and what it must not be read to mean. StatusArmed
// tells useFullEngineBranches the expansion set is exhaustive, which lets a
// lens publish a NARROWED consumer filter and a client gate that
// acks-and-drops everything outside it — so a true here that is not backed
// is the one unacceptable state (§6.5), while a false costs only a broad
// filter on a lens that still runs. A caller may therefore pass true only
// once it has positively established BOTH that the feed is connected and
// that it has delivered everything it owes:
//
//   - not merely "an event arrived". The feed replays the whole taxonomy on
//     every boot, so its first events describe a PARTIAL graph, and a `*`
//     lens activating against one narrows on a downward closure whose leaves
//     have simply not been read yet.
//   - not merely "the connection came back". A reconnect resumes the durable
//     from its ack floor; the edits that landed during the blind window are
//     still in flight at that instant.
//
// internal/refractor/lens.CoreKVSource's liveness callback is the production
// caller and carries the barrier it uses (substrate.Conn.ConsumerCaughtUp,
// routed through its own dispatch goroutine).
//
// false may be passed from any goroutine at any time: disarming can only
// move an answer from StatusArmed to StatusStale, which is the safe
// direction, so it is never ordered behind anything.
func (r *Resolver) SetArmed(armed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consumerLive = armed
}

// maxReasonNameLen bounds how much of a raw name Expand's reason strings
// interpolate. A query label is authored, grammar-bound text, but a
// canonicalName is a `.canonicalName` aspect's raw `data.value`, validated
// only on the pkgmgr-mediated install path — abstractscope.go's own doc
// names the raw core-operations submit that bypasses it — so a reason
// string built from one must not assume any length or charset bound. reason
// feeds an activation error and a slog.Warn (internal/refractor/pipeline),
// so both stay safe to log and surface verbatim regardless of what a
// bypassed write put in Core KV.
const maxReasonNameLen = 80

// truncateReasonName bounds s for interpolation into an Expand reason
// string, marking a truncation rather than performing it silently.
func truncateReasonName(s string) string {
	if len(s) <= maxReasonNameLen {
		return s
	}
	return s[:maxReasonNameLen] + "…(truncated)"
}

// Expand resolves each label in labels to the set of concrete vertex key
// types it admits: itself when concrete (reflexive — a concrete type may
// also have subtypes, amendment A5, so omitting itself would silently drop
// its own direct instances), or its reflexive-transitive downward closure of
// CONCRETE subtypeOf descendants when abstract (an abstract mid-type
// contributes its leaves but never itself, §3.4's expanded-set row — an
// abstract type has no instances, so including it would add a filter
// subject that can never match).
//
// Status reports how far the answer can be trusted (see the Status
// constants). A single unresolvable label, or one whose downward walk hits
// a cycle or exceeds maxDepth, degrades the WHOLE call to
// (nil, nil, StatusUnknown, reason) — never a partial map silently missing
// that one label's entry, which a caller iterating the result could not
// distinguish from "this label genuinely has no subtypes." reason names the
// offending label and the cause (unresolvable name, an ambiguous
// canonicalName, a cycle, or over-depth); it is only ever non-empty
// alongside StatusUnknown, and every interpolated name is bounded by
// truncateReasonName, so it is always safe to surface verbatim in an error
// or a log line.
//
// labels is scanned in sorted order, not map order, so which label's reason
// is reported is deterministic when more than one is unresolvable — an
// error message that names a different label on every call for the same
// input would be its own small debugging hazard.
//
// inert names every requested label whose resolved closure is EXACTLY
// {label} because label names a CONCRETE type (amendment A3, §14 Fire A
// item 5): the `*` sigil bought nothing over the bare label, whether
// because the type has no subtypeOf children at all or because every
// child it does have is itself abstract with no concrete leaves of its
// own — the test is the computed CLOSURE (len(set) == 1 for a concrete
// label), not the direct-child count, so both shapes are caught alike.
// inert is populated only alongside a successful (StatusArmed or
// StatusStale) answer: Expand ALWAYS returns the closure for a resolvable
// label and never refuses on inertness itself — what to DO about an inert
// label is entirely the caller's decision, and the two production callers
// make opposite ones on purpose. An ACTIVATION
// (pipeline.useFullEngineBranches, liveReDerivation=false) refuses on it:
// an author's `*` asserting a polymorphism the taxonomy does not currently
// have is exactly the authoring mistake amendment A3 exists to catch. A
// LIVE RE-DERIVATION (liveReDerivation=true) must not: §6.5 forbids
// treating "the taxonomy just shrank under a running lens" — a live,
// correct `:unit*` losing its last child to another package's uninstall —
// the same as an author's mistake. {label} is the truthful, merely
// un-widened answer for that type right now, and the lens's own instances
// keep projecting.
func (r *Resolver) Expand(labels map[string]struct{}) (map[string]map[string]struct{}, map[string]struct{}, Status, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.loaded {
		return nil, nil, StatusUnknown, "no taxonomy snapshot has ever been loaded"
	}

	ordered := make([]string, 0, len(labels))
	for l := range labels {
		ordered = append(ordered, l)
	}
	sort.Strings(ordered)

	out := make(map[string]map[string]struct{}, len(labels))
	var inert map[string]struct{}
	for _, label := range ordered {
		set, labelInert, ok, reason := r.expandOneLocked(label)
		if !ok {
			return nil, nil, StatusUnknown, reason
		}
		out[label] = set
		if labelInert {
			if inert == nil {
				inert = map[string]struct{}{}
			}
			inert[label] = struct{}{}
		}
	}
	if r.consumerLive {
		return out, inert, StatusArmed, ""
	}
	return out, inert, StatusStale, ""
}

// expandOneLocked computes one label's reflexive-transitive downward
// closure. Called with r.mu held. ok is false when label does not resolve
// to a known type — including a name poisonedNames records as ambiguous,
// which reports that specific cause rather than a generic "unknown" — or
// the downward walk detects a cycle, a poisoned descendant, or exceeds
// maxDepth; reason then names label and the specific cause. inert reports
// whether label's computed closure is exactly {label} because label names a
// CONCRETE type — see Expand's doc for what that means and who acts on it.
// inert is always false when ok is false.
func (r *Resolver) expandOneLocked(label string) (set map[string]struct{}, inert bool, ok bool, reason string) {
	id, known := r.byName[label]
	if !known {
		if r.poisonedNames[label] {
			return nil, false, false, fmt.Sprintf(
				"label %q is ambiguous: its canonicalName is declared by more than one type meta vertex",
				truncateReasonName(label))
		}
		return nil, false, false, fmt.Sprintf("label %q does not resolve to any declared vertex type", truncateReasonName(label))
	}
	info := r.byID[id] // guaranteed present: byName and byID are populated together, and a poisoned id is removed from byName (never leaves `known` true).
	set = map[string]struct{}{}
	onPath := map[string]bool{id: true}
	if walkOK, walkReason := r.collectDownLocked(id, set, onPath, 0); !walkOK {
		return nil, false, false, fmt.Sprintf("label %q: %s", truncateReasonName(label), walkReason)
	}
	// A concrete label's closure always contains at least {label} itself
	// (collectDownLocked's own reflexivity, below); len == 1 therefore means
	// nothing ELSE was added, at any depth — the sigil is a no-op over the
	// bare label. An abstract label is never reflexive (§3.4), so this can
	// never fire for one regardless of how small its closure is.
	inert = !info.abstract && len(set) == 1
	return set, inert, true, ""
}

// collectDownLocked walks id's subtypeOf children depth-first, adding every
// CONCRETE descendant's (and, when concrete, id's own) canonicalName to set.
// onPath is mutated and restored around each branch — mirroring
// internal/pkgmgr/taxonomy.go's walkTaxonomyNoCycle — so a legitimate
// multi-parent diamond (two branches converging on a shared descendant) is
// never mistaken for a cycle; only a genuine back-edge to a current-path
// ancestor is. ok is false on a cycle, a chain deeper than maxDepth, or a
// poisoned id (InstallSnapshot's doc) reached anywhere in the walk — id is
// never the walk's own starting point (a poisoned name is already absent
// from byName), so this only fires when a poisoned entry is a DESCENDANT of
// some other, unambiguous label, which is exactly the path byName's
// deletion alone cannot guard. reason is empty exactly when ok is true.
func (r *Resolver) collectDownLocked(id string, set map[string]struct{}, onPath map[string]bool, depth int) (ok bool, reason string) {
	if r.poisoned[id] {
		return false, fmt.Sprintf("reaches type %q, whose canonicalName is declared by more than one type meta vertex (ambiguous)", truncateReasonName(r.byID[id].canonicalName))
	}
	info, known := r.byID[id]
	if !known {
		return false, "reaches a subtypeOf edge whose target is not in the loaded snapshot"
	}
	if !info.abstract {
		set[info.canonicalName] = struct{}{}
	}
	for _, child := range r.childrenOf[id] {
		if onPath[child] {
			return false, fmt.Sprintf("cycle in the subtypeOf graph (reaches %q again)", truncateReasonName(r.byID[child].canonicalName))
		}
		if depth+1 > maxDepth {
			return false, fmt.Sprintf("subtypeOf chain exceeds maxDepth=%d", maxDepth)
		}
		onPath[child] = true
		if ok, reason := r.collectDownLocked(child, set, onPath, depth+1); !ok {
			return false, reason
		}
		delete(onPath, child)
	}
	return true, ""
}
