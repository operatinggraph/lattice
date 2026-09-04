package pipeline

import (
	"sort"
	"strconv"
	"strings"

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
// the anchor whose grant flipped.
//
// The ZERO VALUE IS ScopeAll: a caller that forgets to set one publishes the
// whole actor, which costs bytes and never withholds a row a device needs.
//
// The value is immutable once built — Merge returns a new scope rather than
// growing this one's set — so a scope handed to two reprojections cannot be
// widened underneath either of them.
type PublishScope struct {
	kind    ScopeKind
	anchors map[string]struct{}
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
// ScopeAll and ScopeNone answer without reading the row at all.
func (s PublishScope) Admits(result ruleengine.EvalResult) bool {
	switch s.kind {
	case ScopeKindNone:
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
//	Anchors(A) ⊔ Anchors(B) = Anchors(A ∪ B) while |A ∪ B| <= MaxScopedAnchors, else All
//
// Widening is the only direction the law moves in, which is what makes it safe
// for a coalescing queue: two signals for one actor are answered by one
// reprojection that publishes at least what either signal asked for.
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
	union := make(map[string]struct{}, len(s.anchors)+len(other.anchors))
	for id := range s.anchors {
		union[id] = struct{}{}
	}
	for id := range other.anchors {
		union[id] = struct{}{}
	}
	if len(union) > MaxScopedAnchors {
		return ScopeAll()
	}
	return PublishScope{kind: ScopeKindAnchors, anchors: union}
}

// String names the scope for a log line or a test failure: "all", "none", or
// the anchor set, sorted so two equal scopes print identically.
func (s PublishScope) String() string {
	switch s.kind {
	case ScopeKindNone:
		return "none"
	case ScopeKindAnchors:
		ids := make([]string, 0, len(s.anchors))
		for id := range s.anchors {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return "anchors(" + strconv.Itoa(len(ids)) + "):" + strings.Join(ids, ",")
	default:
		return "all"
	}
}
