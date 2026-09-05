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
	// ScopeKindSilent admits no row AND publishes no frame. It is the only
	// kind that withholds the frame, and the only one whose pass puts nothing
	// at all on the wire.
	ScopeKindSilent
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
// ScopeSilent is a CDC event handled while this lens's rebuild is replaying, and
// it is the one exception to the second half of the rule: it frames nothing
// either (§4.5).
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

// ScopeSilent withholds the whole publication: no row, no Delete, no frame.
//
// It is the CDC write loop's scope while this lens's rebuild is replaying
// (personal-lens-delta-publication-design.md §4.5). A rebuild resets the
// durable to DeliverLastPerSubject and re-delivers every Core KV entry at its
// ORIGINAL revision, so the write loop would republish the whole read model one
// replayed revision at a time — and every one of those messages sits below the
// high-water mark a connected device already holds, so the device drops all of
// it. Nothing on the wire is nothing to drop.
//
// It withholds the FRAME as well, which no other scope does, and the frame is
// the one thing the design otherwise never withholds. The reason the usual
// argument does not apply: a frame is authoritative for its (lens, actor) as of
// its revision, and a replayed frame carries a revision from the middle of the
// stream's history. It cannot prune anything (the client drops a frame below
// frameHW), so it buys no retraction — it is one more message per replayed
// revision per actor, in a flood the design exists to remove. The rebuilt shape
// reaches the device once, at a live revision, from the content cycle the
// rebuild's completion requests (Pipeline.SetRebuildCompleteSink).
//
// A Delete is withheld for the same reason and converges the same way: the
// content cycle republishes rows AND the authoritative frame, and a key that
// frame omits is what actually retracts the row on the device.
func ScopeSilent() PublishScope { return PublishScope{kind: ScopeKindSilent} }

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

// Frames reports whether a pass under this scope publishes the authoritative
// keyset frame. Every scope frames except ScopeSilent — see that constructor
// for why the one exception exists.
//
// It is asked positively, rather than each caller testing for the silent kind,
// so that the frame's condition reads the same at every publisher and a kind
// added later has one place to answer.
func (s PublishScope) Frames() bool { return s.kind != ScopeKindSilent }

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
	case ScopeKindNone, ScopeKindSilent:
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
//	All    ⊔ x         = All
//	Silent ⊔ x         = x
//	None   ⊔ x         = x
//	Anchors(A) ⊔ Anchors(B)   = Anchors(A ∪ B) while |A ∪ B| <= MaxScopedAnchors, else All
//	Vertices(V) ⊔ Vertices(W) = Vertices(V ∪ W) on the same bound
//	Vertices(V) ⊔ Anchors(A)  = All
//
// Silent is the bottom of the lattice — it publishes strictly less than None,
// which still frames — so it yields to everything, All included.
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
	if s.kind == ScopeKindSilent {
		return other
	}
	if other.kind == ScopeKindSilent {
		return s
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
// The eligibility refusal is logged when the REASON CHANGES, not per event: it
// is a property of the compiled rule, so every event of a refused lens would
// otherwise print the same line — while a hot reload that swaps one refused
// rule for a differently-refused one has a new answer to give, and an operator
// reading the old conjunct would be reading about a rule that is no longer
// installed. See publishScopeRefusalChanged.
//
// THE REBUILD FLAG IS READ HERE, ONCE PER EVENT, and the scope it produces is
// the single value the arm threads to its one writeResults call. That is the
// invariant: an event's rows and its frame are decided by the SAME observation
// of RebuildInFlight, so a rebuild that finishes mid-event can never leave the
// event half-published — every row withheld and a frame emitted, or the reverse.
// The flag read at writeResults instead would be a second observation of a value
// the completion watcher clears asynchronously, and the two arms of one event
// could disagree. Which side of the transition an event falls on does not matter
// and is not pinned: an event decided as silent published nothing at all, and an
// event decided as scoped publishes exactly what a live event publishes, at a
// revision at or above the rebuild's last.
//
// It is read AHEAD of the eligibility refusal because the refusal widens to
// ScopeAll — which during a replay is the whole flood this exists to stop, one
// whole-actor republish per replayed Core KV entry.
func (p *Pipeline) eventPublishScope(rs ruleState, vertices []string) PublishScope {
	if _, isPersonal := p.currentAdapter().(adapter.KeySetPublisher); !isPersonal {
		return ScopeAll()
	}
	if p.RebuildInFlight() {
		return ScopeSilent()
	}
	refusal := rs.publishScopeRefusal()
	if p.publishScopeRefusalChanged(refusal) && refusal != "" {
		slog.Info("pipeline: personal lens publishes the whole actor per event — the publication scope is refused",
			"ruleId", p.ruleID, "reason", refusal)
	}
	if refusal != "" {
		return ScopeAll()
	}
	return ScopeVertices(vertices)
}

// publishScopeRefusalChanged records reason as the lens's current publication-
// scope refusal and reports whether it differs from the one already recorded.
//
// It is called with the refusal of EVERY event, "" included, which is what
// re-arms the log across a reload in both directions: a lens reloaded from
// refused to scopeable clears the record, so a later re-refusal is a change
// again, and a lens reloaded from one conjunct to another logs the new one.
// A sync.Once could do neither — it would have printed conjunct A's line and
// then stayed silent for the life of the process while the running rule was
// refused for conjunct B.
//
// It lives on the PIPELINE, which outlives its rules and its adapters, and
// under ruleMu with the rule it describes: the reason is a property of the
// published rule, so the record of it belongs beside the publication that can
// invalidate it.
func (p *Pipeline) publishScopeRefusalChanged(reason string) bool {
	p.ruleMu.RLock()
	same := p.publishScopeRefusalLogged == reason
	p.ruleMu.RUnlock()
	if same {
		return false
	}
	p.ruleMu.Lock()
	defer p.ruleMu.Unlock()
	// Re-read under the write lock: two goroutines can reach here with
	// different reasons, and only the one that actually moves the record may
	// claim the line.
	if p.publishScopeRefusalLogged == reason {
		return false
	}
	p.publishScopeRefusalLogged = reason
	return true
}

// LogValue renders the scope for slog as the string String builds.
//
// Without it the JSON handler production installs (cmd/refractor's
// slog.NewJSONHandler) ships `"publishScope":{}` — its Any arm marshals the
// value and never consults fmt.Stringer, and every field of this struct is
// unexported. slog.LogValuer is the interface BOTH handlers honour, so the
// attr reads the same in a text-handler test and on the wire.
func (s PublishScope) LogValue() slog.Value { return slog.StringValue(s.String()) }

// String names the scope for a log line or a test failure: "all", "none", or
// the set, sorted so two equal scopes print identically.
func (s PublishScope) String() string {
	switch s.kind {
	case ScopeKindNone:
		return "none"
	case ScopeKindSilent:
		return "silent"
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
