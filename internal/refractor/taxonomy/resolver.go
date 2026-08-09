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
// This package builds only the resolver + its resolve-time cycle/depth
// authority (§14 Fire A item 3). Loading a live snapshot from Core KV and
// arming the resolver from the meta-watch CDC consumer is item 4's job — a
// Resolver constructed by New and never touched again stays permanently
// disarmed with no snapshot, which is the fail-closed posture every
// `*`-carrying lens must see until then.
package taxonomy

import "sync"

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
// armed — Expand on it always answers StatusUnknown, which is the
// inertness invariant increments 1 and 2 already established: nothing in
// the shipped corpus declares an abstract type yet, so nothing depends on
// this resolver knowing anything until Fire B's first consumer lands.
type Resolver struct {
	mu sync.RWMutex

	byID       map[string]typeInfo // meta NanoID -> resolved identity
	byName     map[string]string   // canonicalName -> meta NanoID
	childrenOf map[string][]string // parent NanoID -> child NanoIDs (subtypeOf edges, downward)
	poisoned   map[string]bool     // meta NanoID -> its CanonicalName collided with another entry's

	loaded bool
	armed  bool
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
// Item 4's CDC consumer is the only production caller, rebuilding the
// snapshot from the meta-watch's boot replay and every live subtypeOf/type
// edit; this design's own snapshot is empty (the resolver ships disarmed),
// so only tests call this today.
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
	for name, count := range nameCount {
		if count <= 1 {
			continue
		}
		delete(byName, name)
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
	r.loaded = true
	// A reload must never carry a previous life's armed flag forward — a
	// stale true here would report StatusArmed on the very next Expand call
	// before item 4's consumer has confirmed this new snapshot is
	// live-current. SetArmed is the only path that may set it true again.
	r.armed = false
}

// SetArmed marks the resolver's invalidation consumer live (true) or dead
// (false) — the other half of §4.2's "armed" test alongside a loaded
// snapshot. A Resolver never calls this itself; item 4's CDC consumer flips
// it once live at boot and again if it ever dies.
func (r *Resolver) SetArmed(armed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed = armed
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
// (nil, StatusUnknown) — never a partial map silently missing that one
// label's entry, which a caller iterating the result could not distinguish
// from "this label genuinely has no subtypes."
func (r *Resolver) Expand(labels map[string]struct{}) (map[string]map[string]struct{}, Status) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.loaded {
		return nil, StatusUnknown
	}

	out := make(map[string]map[string]struct{}, len(labels))
	for label := range labels {
		set, ok := r.expandOneLocked(label)
		if !ok {
			return nil, StatusUnknown
		}
		out[label] = set
	}
	if r.armed {
		return out, StatusArmed
	}
	return out, StatusStale
}

// expandOneLocked computes one label's reflexive-transitive downward
// closure. Called with r.mu held. ok is false when label does not resolve
// to a known type, or the downward walk detects a cycle or exceeds
// maxDepth.
func (r *Resolver) expandOneLocked(label string) (set map[string]struct{}, ok bool) {
	id, known := r.byName[label]
	if !known {
		return nil, false
	}
	set = map[string]struct{}{}
	onPath := map[string]bool{id: true}
	if !r.collectDownLocked(id, set, onPath, 0) {
		return nil, false
	}
	return set, true
}

// collectDownLocked walks id's subtypeOf children depth-first, adding every
// CONCRETE descendant's (and, when concrete, id's own) canonicalName to set.
// onPath is mutated and restored around each branch — mirroring
// internal/pkgmgr/taxonomy.go's walkTaxonomyNoCycle — so a legitimate
// multi-parent diamond (two branches converging on a shared descendant) is
// never mistaken for a cycle; only a genuine back-edge to a current-path
// ancestor is. Returns false on a cycle, a chain deeper than maxDepth, or a
// poisoned id (InstallSnapshot's doc) reached anywhere in the walk — id is
// never the walk's own starting point (a poisoned name is already absent
// from byName), so this only fires when a poisoned entry is a DESCENDANT of
// some other, unambiguous label, which is exactly the path byName's
// deletion alone cannot guard.
func (r *Resolver) collectDownLocked(id string, set map[string]struct{}, onPath map[string]bool, depth int) bool {
	if r.poisoned[id] {
		return false
	}
	info, known := r.byID[id]
	if !known {
		return false
	}
	if !info.abstract {
		set[info.canonicalName] = struct{}{}
	}
	for _, child := range r.childrenOf[id] {
		if onPath[child] {
			return false
		}
		if depth+1 > maxDepth {
			return false
		}
		onPath[child] = true
		if !r.collectDownLocked(child, set, onPath, depth+1) {
			return false
		}
		delete(onPath, child)
	}
	return true
}
