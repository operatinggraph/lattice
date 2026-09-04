package pipeline

import (
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// MaxScopedAnchors bounds how many anchors one coalesced scope names before it
// widens to ScopeAll (personal-lens-delta-publication-design.md §4.3).
//
// Past the bound the scope stops being cheaper than the thing it replaces: the
// set costs memory per queued actor, the Admits check is a lookup per row, and
// an actor whose grants moved on 64 anchors at once is being re-granted
// wholesale rather than edited. Widening there is the over-publish direction —
// bytes, never a withheld row.
const MaxScopedAnchors = 64

// ScopeKind names which arm of a PublishScope decides a row.
type ScopeKind uint8

const (
	// ScopeKindAll admits every row. It is the ZERO VALUE, so a caller that
	// passes no scope publishes the whole actor.
	ScopeKindAll ScopeKind = iota
	// ScopeKindNone admits no row; the frame alone goes out.
	ScopeKindNone
	// ScopeKindAnchors admits a row whose "anchor" alias names one of a
	// bounded set of anchor NanoIDs.
	ScopeKindAnchors
	// ScopeKindVertices admits a row whose PROVENANCE — the vertex keys its
	// evaluation read — meets a bounded set of vertex keys.
	ScopeKindVertices
)

// PublishScope decides which of a personal reprojection's rows are published.
// The frame is unaffected: a row the scope withholds is still named by the
// authoritative keyset frame, so the device keeps the copy it already holds
// (personal-lens-delta-publication-design.md §4).
//
// A row is published iff the scope admits it; every surviving row is framed.
// ScopeAll is a hydrate, an interest change, the healer's daily content cycle
// and an unlicensed caller. ScopeNone is the healer's ordinary pass. ScopeAnchors
// is a grant change, which moves the inclusion of exactly the rows anchored at
// the anchor whose grant flipped. ScopeVertices is a CDC event, which moves the
// content of exactly the rows whose evaluation read one of the event's vertices.
//
// The ZERO VALUE IS ScopeAll: a caller that forgets to set one publishes the
// whole actor, which costs bytes and never withholds a row a device needs.
//
// The value is immutable once built — Merge returns a new scope rather than
// growing this one's set — so a scope handed to two reprojections cannot be
// widened underneath either of them.
type PublishScope struct {
	kind ScopeKind
	// anchors is ScopeKindAnchors' set of bare anchor NanoIDs; vertices is
	// ScopeKindVertices' set of Contract #1 vertex keys. Each arm reads only
	// its own, so a scope can never be judged against a set built for the
	// other question.
	anchors  map[string]struct{}
	vertices map[string]struct{}
}

// ScopeAll admits every row.
func ScopeAll() PublishScope { return PublishScope{kind: ScopeKindAll} }

// ScopeNone admits no row, leaving the frame as the pass's whole output.
func ScopeNone() PublishScope { return PublishScope{kind: ScopeKindNone} }

// ScopeAnchors admits the rows anchored at any of anchorIDs — bare NanoIDs, the
// same value substrate.ParseVertexKey recovers from the row's "anchor" alias.
//
// An EMPTY or nil set is ScopeAll, not "admit nothing". A caller that reached
// here holding no anchor knows only that something moved, and an empty set read
// as ScopeNone would silently publish nothing for it — a withheld row that no
// frame corrects, in the direction a reader cannot distinguish from a scope
// nobody set.
//
// A token that is not a valid NanoID is DROPPED for the same reason. The match
// is against the NanoID a row's anchor key parses to, so a malformed token can
// never name a row: a scope built entirely of them would admit nothing while
// reading as ScopeAnchors, which is ScopeNone in effect and the one reading this
// constructor refuses to produce by accident. A set that empties out under the
// filter is therefore ScopeAll — the caller knows something moved and gets the
// whole actor. The producer of these tokens (a lens's own key inversion)
// validates them already; this is the belt on an injected closure.
//
// More than MaxScopedAnchors distinct ids is ScopeAll.
func ScopeAnchors(anchorIDs []string) PublishScope {
	set := make(map[string]struct{}, len(anchorIDs))
	for _, id := range anchorIDs {
		if !substrate.IsValidNanoID(id) {
			continue
		}
		set[id] = struct{}{}
	}
	if len(set) == 0 || len(set) > MaxScopedAnchors {
		return ScopeAll()
	}
	return PublishScope{kind: ScopeKindAnchors, anchors: set}
}

// ScopeVertices admits the rows whose provenance names any of vertexKeys —
// Contract #1 `vtx.<type>.<id>` keys, the granularity a CDC arm names and the
// granularity ProjectionResult.Provenance is folded to.
//
// It is the CDC write loop's scope: an event's own vertices (the event vertex,
// an aspect's parent, a link's two endpoints, the actor's own key), and a row
// is published iff its evaluation read one of them.
//
// An EMPTY or nil set is ScopeAll, for ScopeAnchors' reason: a caller holding
// no vertex knows only that something moved, and an empty set read as ScopeNone
// would withhold every row of an actor with no frame correcting it — a reading
// indistinguishable from a scope nobody set. A token that is not a vertex key is
// DROPPED, and a set that empties out under that filter is ScopeAll: provenance
// carries vertex keys, so a non-vertex token can never name a row, and a scope
// built entirely of them would be ScopeNone wearing this arm's name.
//
// More than MaxScopedAnchors distinct keys is ScopeAll — the same bound and the
// same reason as the anchor arm's, though no CDC arm produces more than two.
func ScopeVertices(vertexKeys []string) PublishScope {
	set := make(map[string]struct{}, len(vertexKeys))
	for _, key := range vertexKeys {
		if _, _, ok := substrate.ParseVertexKey(key); !ok {
			continue
		}
		set[key] = struct{}{}
	}
	if len(set) == 0 || len(set) > MaxScopedAnchors {
		return ScopeAll()
	}
	return PublishScope{kind: ScopeKindVertices, vertices: set}
}

// Kind reports which arm decides a row.
func (s PublishScope) Kind() ScopeKind { return s.kind }

// Admits reports whether result's row is published.
//
// ScopeAnchors parses the row's "anchor" alias and matches on the BARE NanoID,
// discarding the type segment — the same value capabilityread.AnchorSet.Admits
// keys the D1 read-grant decision on, so a grant change for one anchor admits
// exactly the rows that anchor's grant governs.
//
// A row whose anchor is absent, empty or not a Contract #1 vertex key is NOT
// admitted under ScopeAnchors: the scope names anchors, and a row that names
// none is not one of them. That branch is unreachable on the plane —
// projection.personalEnvelopeFn skips a row with no anchor outright and refuses
// one whose anchor does not parse on every lens carrying the read-grant gate, so
// no such row survives into a result — and it is defined here anyway because a
// scope owes an answer for every row it is handed.
//
// ScopeVertices meets the row's provenance against the set. A row carrying NO
// provenance is ADMITTED: an engine or a path that records nothing (a
// pipeline-manufactured result, an engine build that does not populate it) must
// reproduce today's publication rather than silence the device, so the absent
// reading fails OPEN — over-publish, never a withheld row.
//
// ScopeAll and ScopeNone answer without reading the row at all.
func (s PublishScope) Admits(result ruleengine.EvalResult) bool {
	switch s.kind {
	case ScopeKindNone:
		return false
	case ScopeKindVertices:
		if len(result.Provenance) == 0 {
			return true
		}
		for _, vtx := range result.Provenance {
			if _, named := s.vertices[vtx]; named {
				return true
			}
		}
		return false
	case ScopeKindAnchors:
		anchorRaw, _ := result.Row["anchor"].(string)
		if anchorRaw == "" {
			return false
		}
		_, anchorID, ok := substrate.ParseVertexKey(anchorRaw)
		if !ok {
			return false
		}
		_, named := s.anchors[anchorID]
		return named
	default:
		return true
	}
}

// Merge combines two scopes into the one that admits every row either of them
// admits (personal-lens-delta-publication-design.md §4.3):
//
//	All  ⊔ x           = All
//	None ⊔ x           = x
//	Anchors(A) ⊔ Anchors(B)   = Anchors(A ∪ B) while |A ∪ B| <= MaxScopedAnchors, else All
//	Vertices(V) ⊔ Vertices(W) = Vertices(V ∪ W) on the same bound
//	Vertices(V) ⊔ Anchors(A)  = All
//
// Widening is the only direction the law moves in, which is what makes it safe
// for a coalescing queue: two signals for one actor are answered by one
// reprojection that publishes at least what either signal asked for.
//
// The mixed arm is All because the two sets answer different questions — one
// names the vertices an evaluation read, the other the anchor a row is keyed at
// — and no set of either kind expresses their union. It is closure, not a live
// path: only the reprojector's dirty set coalesces scopes, and nothing enqueues
// a vertex scope into it (the CDC write loop publishes inline, and every
// enqueuing producer passes an anchor scope or ScopeAll). Widening is the safe
// answer for the day one does.
func (s PublishScope) Merge(other PublishScope) PublishScope {
	if s.kind == ScopeKindAll || other.kind == ScopeKindAll {
		return ScopeAll()
	}
	if s.kind == ScopeKindNone {
		return other
	}
	if other.kind == ScopeKindNone {
		return s
	}
	if s.kind != other.kind {
		return ScopeAll()
	}
	if s.kind == ScopeKindVertices {
		union := mergeKeySets(s.vertices, other.vertices)
		if union == nil {
			return ScopeAll()
		}
		return PublishScope{kind: ScopeKindVertices, vertices: union}
	}
	union := mergeKeySets(s.anchors, other.anchors)
	if union == nil {
		return ScopeAll()
	}
	return PublishScope{kind: ScopeKindAnchors, anchors: union}
}

// mergeKeySets unions two scope sets, returning nil when the union crosses
// MaxScopedAnchors — the caller's signal to widen to ScopeAll.
func mergeKeySets(a, b map[string]struct{}) map[string]struct{} {
	union := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		union[k] = struct{}{}
	}
	for k := range b {
		union[k] = struct{}{}
	}
	if len(union) > MaxScopedAnchors {
		return nil
	}
	return union
}

// eventPublishScope is the scope a CDC arm hands its writeResults call for an
// event that touched vertices (personal-lens-delta-publication-design.md §4.2).
//
// Only a personal target is scoped. Every other lens shape — a plain
// projection, an auth-plane actor aggregate — publishes exactly what it
// publishes today, byte for byte, because this returns ScopeAll for it here at
// the producer and writeResults refuses to scope it again at the consumer. The
// classification is read the way reprojectActors reads it: a HotReloadInto only
// ever swaps between adapters of the same target type, so it cannot flip
// mid-event even when the instance does.
//
// The eligibility refusal is logged ONCE per lens, not per event: it is a
// property of the compiled rule, so every event of a refused lens would
// otherwise print the same line.
func (p *Pipeline) eventPublishScope(rs ruleState, vertices []string) PublishScope {
	if _, isPersonal := p.currentAdapter().(adapter.KeySetPublisher); !isPersonal {
		return ScopeAll()
	}
	if refusal := rs.publishScopeRefusal(); refusal != "" {
		p.publishScopeRefusedOnce.Do(func() {
			slog.Info("pipeline: personal lens publishes the whole actor per event — the publication scope is refused",
				"ruleId", p.ruleID, "reason", refusal)
		})
		return ScopeAll()
	}
	return ScopeVertices(vertices)
}

// String names the scope for a log line or a test failure: "all", "none", or
// the set, sorted so two equal scopes print identically.
func (s PublishScope) String() string {
	switch s.kind {
	case ScopeKindNone:
		return "none"
	case ScopeKindAnchors:
		return "anchors" + renderKeySet(s.anchors)
	case ScopeKindVertices:
		return "vertices" + renderKeySet(s.vertices)
	default:
		return "all"
	}
}

// renderKeySet prints a scope set as "(n):a,b,c" with the members sorted.
func renderKeySet(set map[string]struct{}) string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return "(" + strconv.Itoa(len(ids)) + "):" + strings.Join(ids, ",")
}
