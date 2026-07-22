package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/weaver/planner"
)

// targetSourceDurablePrefix is the JetStream durable-consumer name prefix for
// the meta.weaverTarget registry source. A per-engine instance segment plus a
// per-boot nonce (see start) are appended so each boot replays the full
// installed target set via IncludeHistory: the registry is derived in-memory
// state rebuilt by CDC replay, exactly the one class of in-memory cache the
// engine sanctions.
//
// The nonce is load-bearing, not just the instance segment: JetStream only
// honors DeliverPolicy when a durable is first created — CreateOrUpdateConsumer
// against an EXISTING durable of the same name resumes from its persisted ack
// floor regardless of the DeliverPolicy requested, so a stable, operator-set
// Instance (recommended for dashboards/alerting across restarts) would
// silently defeat full-replay-on-every-connect and leave a crash-restarted
// engine's registry cold. Appending a fresh nonce each boot guarantees the
// durable has never existed before, independent of whether Instance is
// stable or auto-generated.
const targetSourceDurablePrefix = "weaver-target-source"

// Canonical envelope classes the registry source routes. Other meta classes
// under vtx.meta.> are probed for an op meta-vertex (data.operationType) and
// otherwise skipped.
const (
	weaverTargetClass = "meta.weaverTarget"
	loomPatternClass  = "meta.loomPattern"
)

// Augur escalation triggers (Contract #10 §10.8 "Augur escalation"): the
// stuck-gap conditions a target's `augur.escalate` may redirect to AI
// reasoning. `unplannable` = a missing_* column with no gaps[col] entry;
// `exhausted` = a gap whose retry budget is spent. Restated here (not imported)
// so the registry validates the block without an extra dependency.
const (
	escalateUnplannable = "unplannable"
	escalateExhausted   = "exhausted"
)

// Planner-extension target modes (Contract #10 §10.8 "Planner extension",
// Fire 4): absent (empty string) is the default — frozen table-only
// behavior, byte-identical to every target installed before this fire.
// "shadow" computes the planner's pick for each gap declaring candidates and
// compares it against the table's actual dispatch (never dispatching it);
// "planned" dispatches the planner's pick for real — Fire 5 for a
// candidates-only gap, Fire 6 for a goal-authored one (resolvePlannedAction).
const (
	targetModeShadow  = "shadow"
	targetModePlanned = "planned"
)

// singleTokenPattern accepts a value usable as a single NATS KV key segment,
// subject token, and durable-name segment: no dots, no wildcards, no spaces.
// Applied to targetId and gap-column names at install time and to the engine
// Instance/Lane at startup.
var singleTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// GapAction is one playbook entry of a target's gaps map (Contract #10 §10.8
// action table). Action selects the contract; the remaining fields carry the
// per-action params, each either a literal or a row.<column> template token.
type GapAction struct {
	Action    string            `json:"action"`
	Pattern   string            `json:"pattern,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Adapter   string            `json:"adapter,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Assignee  string            `json:"assignee,omitempty"`
	Target    string            `json:"target,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	// Class pins the dispatched op's DDL canonical name onto the wire envelope
	// (Contract #2 §2.1 operationType→class reverse index), for an Operation
	// admitted by more than one installed vertexType DDL — the Processor's
	// reverse index deliberately excludes an ambiguous operationType rather
	// than guess, so an unpinned directOp against it fails closed
	// (MissingClass) forever.
	Class string `json:"class,omitempty"`
	// IssueCode/IssueSeverity are consulted only when Action == actionSurface
	// (FR29's "surface, never dispatch" gap): the Health-KV issue code/severity
	// raised while the gap stays open, cleared on close. IssueSeverity defaults
	// to "warning" (degraded, never "unhealthy" — a stuck monitor is not
	// Weaver's own failure to fulfil its responsibility) when omitted.
	IssueCode     string `json:"issueCode,omitempty"`
	IssueSeverity string `json:"issueSeverity,omitempty"`
	// Reads are the dispatched op's ContextHint.Reads — bare vertex keys, each a
	// literal or a row.<column> template resolved from the violation row. A
	// directOp that must read its candidate vertex (e.g. TombstoneObject) routes
	// the candidate key from the lens row (row.entityKey) into the op's reads.
	Reads []string `json:"reads,omitempty"`

	// Candidates is the Fire-5 selection surface (§10.8 Planner extension): an
	// explicit, package-authored set of alternative actions the planner ranks
	// and picks ONE from — consulted only when Action is empty (the explicit
	// action always wins). Fire 4 parses + install-validates this list and, on
	// a mode:"shadow" target, independently ranks it to compare against the
	// table's actual dispatch (planner_shadow.go); the pick is never dispatched
	// until Fire 5.
	Candidates []GapCandidate `json:"candidates,omitempty"`
	// Goal is the Fire-6 synthesis target (§10.8 Planner extension): bounded
	// goal regression over this gap's own declared Actions catalog (below).
	// Parsed + install-validated here (goalGuard); a mode:"planned" target
	// resolves it at dispatch time via resolveGoalAction.
	Goal json.RawMessage `json:"goal,omitempty"`

	// GoalColumns resolves the Fire-6 State-schema gap (design
	// weaver-planner-mandate-design.md §Fire-6-checkpoint): Goal may address
	// an ASPECT path (e.g. `subject.signature.data.signedAt`, matching a real
	// op's declared Effect), but a §10.2 lens row flattens every projected
	// column onto a bare name with no aspect tag, and rowState's default
	// mapping (Path{Field: col}) always addresses the ROOT. Without a bridge,
	// Goal would key into planner.State under a different Path than the
	// Effects populate, so Synthesize could never find those Effects
	// satisfying it. GoalColumns declares, for the columns Goal needs, which
	// aspect-qualified path a row column actually represents — map key = the
	// lens BodyColumn name, value = its guard-grammar path string
	// ("subject.<aspect>.data.<field>"). Scoped to THIS gap, not the whole
	// target: two gaps in one target may reuse the same column name for
	// unrelated facts, and a shared target-wide map would silently rebase
	// both onto whichever gap declared it. A column absent from this map is
	// unaffected: it keeps addressing subject.data.<column> (root), exactly
	// today's behavior. Pure row-keying — no new Weaver Core-KV read,
	// consistent with §10.8's "pure function of (row, catalog, __effect
	// window)" framing. Install-validated: every path must be well-formed and
	// aspect-qualified (a root-shaped entry is redundant), values must be
	// unique (two columns mapping to the same path make rowState's result
	// depend on Go's nondeterministic map-iteration order over row), and
	// every declared path must actually be referenced somewhere in Goal — an
	// entry Goal never asks about is exactly as inert as a typo'd column name
	// (there is no lens schema here to check the column name against, so
	// this is the catchable half of that mistake).
	GoalColumns map[string]string `json:"goalColumns,omitempty"`

	// Actions is the gap's planning catalog (design
	// weaver-planner-mandate-design.md revision, 2026-07-05 — the
	// loftspace-lease-renewal-goal-authored-target-design's R1: a per-gap,
	// package-authored catalog rather than a global installed-effects
	// auto-catalog — an op effect alone carries no dispatch binding, so a
	// global catalog would be undispatchable). Required alongside Goal
	// (install rejects a goal gap with an empty catalog, and an Actions
	// catalog with no Goal to synthesize toward). Each entry couples a
	// dispatch binding (the same action-contract shape as GapCandidate) with
	// the planner-facing triple Pre/Effects/Cost. resolveGoalAction
	// (strategist.go) is the consumer: a fresh episode synthesizes a plan
	// over this catalog via planner.Synthesize and dispatches its first
	// step; an in-flight episode reuses its pinned entry verbatim.
	Actions []ActionCatalogEntry `json:"actions,omitempty"`

	// goalGuard is Goal parsed once at install-validation time (nil unless Goal
	// is set — a valid goal always parses, validateTarget rejects the target
	// otherwise). Unexported: read by resolveGoalAction (strategist.go).
	goalGuard *guardgrammar.Guard `json:"-"`

	// goalColumnPaths is GoalColumns parsed once at install-validation time
	// (nil unless GoalColumns is set). Unexported: read by resolveGoalAction
	// and releaseCompletedLeg via rowState's aspectCols bridge.
	goalColumnPaths map[string]guardgrammar.Path `json:"-"`
}

// GapCandidate is one playbook-authored alternative in a gap's `candidates`
// list (§10.8 Planner extension, Fire 5 selection): the same action-contract
// shape as GapAction (action/pattern/subject/... — a chosen candidate
// dispatches exactly like an explicit GapAction), plus an optional
// precondition (Pre) gating eligibility and a Cost the ranking prefers lower.
type GapCandidate struct {
	Action    string            `json:"action"`
	Pattern   string            `json:"pattern,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Adapter   string            `json:"adapter,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Assignee  string            `json:"assignee,omitempty"`
	Target    string            `json:"target,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Reads     []string          `json:"reads,omitempty"`
	// Pre gates this candidate's eligibility, evaluated against the §10.2 row
	// (each row column addressed as subject.data.<column> — the same
	// row-is-State convention internal/weaver/planner's State documents).
	// Omitted means always eligible.
	Pre json.RawMessage `json:"pre,omitempty"`
	// Cost ranks candidates ascending (cheaper preferred); ties break on Action
	// lexicographically — the same canonical tie-break internal/weaver/planner
	// uses.
	Cost int `json:"cost,omitempty"`

	// preGuard is Pre parsed once at install-validation time (nil = always
	// eligible). Unexported: read only by the Fire-4 shadow-compare ranking.
	preGuard *guardgrammar.Guard `json:"-"`
}

// ActionCatalogEntry is one entry of a goal gap's Actions catalog (design
// weaver-planner-mandate-design.md revision, 2026-07-05 — the
// loftspace-lease-renewal-goal-authored-target-design's R1): a dispatch
// binding — the same action-contract shape as GapCandidate (a chosen entry
// dispatches exactly like an explicit GapAction) — coupled with the
// planner-facing triple Synthesize needs: an optional Pre (gates whether this
// action may be applied from a given planner.State), the Effects it entails
// once dispatched, and an optional Cost. Unlike GapCandidate, both Pre and
// Effects MAY address an aspect path this gap's GoalColumns bridges (the goal
// gap's State already carries that bridge; candidates' rowState(row, nil)
// never does).
type ActionCatalogEntry struct {
	// Ref identifies this entry for both the synthesized Plan's Steps and
	// canonical tie-breaking (internal/weaver/planner.Action.Ref doc: cost
	// ascending, then Ref lexicographically). Must be unique within the gap's
	// catalog.
	Ref string `json:"ref"`

	Action    string            `json:"action"`
	Pattern   string            `json:"pattern,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Adapter   string            `json:"adapter,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Assignee  string            `json:"assignee,omitempty"`
	Target    string            `json:"target,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Reads     []string          `json:"reads,omitempty"`

	// Pre gates this entry's eligibility in the search, evaluated against the
	// gap's planner.State (row + GoalColumns bridge). Omitted means always
	// available.
	Pre json.RawMessage `json:"pre,omitempty"`
	// Effects are the atoms this entry's dispatch entails once it commits —
	// required, and each must be a concrete assertion (present/absent/equals,
	// or an allOf of those; planner.ApplyEffects/ErrUnsupportedEffect rejects
	// an anyOf/not effect at install rather than let it silently no-op deep in
	// a search).
	Effects []json.RawMessage `json:"effects"`
	// Cost ranks the search (ascending; ties break on Ref lexicographically —
	// internal/weaver/planner's canonical tie-break). Defaults to 1 when
	// omitted (the zero value): unlike GapCandidate, an unauthored cost here
	// must still contribute a real weight to a multi-step plan's total, so the
	// zero value cannot mean "free."
	Cost int `json:"cost,omitempty"`

	// preGuard/effectGuards are Pre/Effects parsed once at install-validation
	// time. Unexported: read by resolveGoalAction (as the catalog's
	// planner.Action.Precondition/Effects) and releaseCompletedLeg (checking
	// whether the pinned entry's effectGuards now hold in the row).
	preGuard     *guardgrammar.Guard   `json:"-"`
	effectGuards []*guardgrammar.Guard `json:"-"`
}

// Target is a parsed meta.weaverTarget body (Contract #10 §10.8): the binding
// between a violation Lens's weaver-targets row prefix (targetId) and the
// gap → action remediation playbook.
type Target struct {
	TargetID string               `json:"targetId"`
	LensRef  string               `json:"lensRef"`
	Gaps     map[string]GapAction `json:"gaps"`
	// Augur is the optional, default-absent AI-reasoning escalation policy
	// (Contract #10 §10.8 "Augur escalation"). With no augur block a target
	// behaves exactly as the frozen contract — an unplannable gap fails closed.
	// The block redirects that dead-end to the Augur reasoning tier.
	Augur *AugurPolicy `json:"augur,omitempty"`

	// Mode selects the planner-extension posture (§10.8 Planner extension,
	// Fire 4): "" (absent, the default — every target installed before this
	// fire) is frozen table-only behavior, byte-identical; targetModeShadow
	// computes + records the planner's pick per gap but never dispatches it;
	// targetModePlanned dispatches the planner's pick for real (Fire 5
	// candidates, Fire 6 goal).
	Mode string `json:"mode,omitempty"`

	// Admission is the optional Fire-8 dispatch-pacing policy (§10.8 Planner
	// extension "Admission control"): absent (every target before this fire)
	// is unbounded, byte-identical dispatch. See AdmissionPolicy.
	Admission *AdmissionPolicy `json:"admission,omitempty"`
}

// AugurPolicy is a target's parsed `augur` block (Contract #10 §10.8 "Augur
// escalation"): which stuck-gap triggers escalate to AI reasoning, plus the
// optional overrides naming the reasoning op / bridge adapter / replyOp Weaver
// dispatches and the model. The reasoning episode is single-step, so Weaver
// dispatches the reasoning op DIRECTLY as a directOp (Option F — no Loom wrapper);
// Op/Adapter/ReplyOp default to CreateAugurReasoningClaim / augur / RecordProposal
// at dispatch when omitted, so a minimal block is just `escalate`. AutoApply is
// parsed and validated but NOT consumed — the autonomy boundary stays
// human-in-the-loop until Andrew ratifies it (design Fire 3).
type AugurPolicy struct {
	Escalate  []string        `json:"escalate,omitempty"`
	Op        string          `json:"op,omitempty"`
	Adapter   string          `json:"adapter,omitempty"`
	ReplyOp   string          `json:"replyOp,omitempty"`
	Model     string          `json:"model,omitempty"`
	AutoApply *AugurAutoApply `json:"autoApply,omitempty"`
}

// AugurAutoApply is the OPTIONAL auto-apply allow-list (Contract #10 §10.8):
// a proposal whose action ∈ Actions, confidence ≥ MinConfidence, and which
// passes deterministic validation MAY skip the human gate. DESIGNED, not
// enabled — parsed + validated fail-closed so a package cannot smuggle a
// malformed block, but no escalation path acts on it yet.
type AugurAutoApply struct {
	Actions       []string `json:"actions,omitempty"`
	MinConfidence float64  `json:"minConfidence,omitempty"`
}

// targetSource is Weaver's registry loader. It subscribes to Core KV under
// vtx.meta.> via a durable JetStream consumer (Conn.SubscribeKVChanges) and
// routes by envelope class:
//
//   - meta.weaverTarget vertices + their spec aspects load/update/remove
//     registered targets (driving the lane-1 consumer reconcile through the
//     load/update callbacks);
//   - meta.loomPattern vertices + spec aspects feed the patternId →
//     vtx.meta.<id> index the Actuator resolves triggerLoom's
//     authContext.target from (live resolution at dispatch time);
//   - any other meta vertex carrying data.operationType feeds the
//     operationType → vtx.meta.<id> index assignTask resolves forOperation
//     from.
//
// CDC ordering is not guaranteed, so a spec aspect seen before its parent
// vertex's class is buffered and replayed once the class arrives. The buffer
// is bounded: a buffered spec is dropped once the vertex's class is learned to
// be non-routed, evicted on vertex/spec delete, and flagged to Health if it
// stays pending past pendingSpecWarnAfter (an orphaned spec is a config error,
// never silent).
type targetSource struct {
	conn     *substrate.Conn
	bucket   string
	instance string
	logger   *slog.Logger
	issues   *issueCache

	loadCB   func(*Target)
	updateCB func(old, new *Target)

	mu            sync.Mutex
	classes       map[string]string    // vertex id → envelope class (every parsed meta vertex)
	pendingSpecs  map[string][]byte    // spec bodies seen before their parent vertex's class
	pendingSince  map[string]time.Time // when each pending spec started waiting
	targets       map[string]*Target   // targetId → registered target
	targetOwner   map[string]string    // targetId → owning vertex id (duplicate detection)
	ownerTargetID map[string]string    // vertex id → targetId it registered
	patternMeta   map[string]string    // patternId (and vertex id) → vtx.meta.<id>
	patternOwner  map[string][]string
	opMetaByType  map[string]string                // operationType → vtx.meta.<opId>
	opEffects     map[string][]*guardgrammar.Guard // op-meta vertex id → its parsed .effects guards
}

// pendingSpecWarnAfter bounds how long a spec aspect may wait for its parent
// vertex's class before being flagged to Health: CDC replay delivers a vertex
// within moments of its aspects, so a spec still pending after this window is
// orphaned — a config error, surfaced rather than silently buffered.
const pendingSpecWarnAfter = 5 * time.Minute

func newTargetSource(conn *substrate.Conn, bucket, instance string, issues *issueCache, logger *slog.Logger) *targetSource {
	if logger == nil {
		logger = slog.Default()
	}
	return &targetSource{
		conn:          conn,
		bucket:        bucket,
		instance:      instance,
		logger:        logger,
		issues:        issues,
		classes:       make(map[string]string),
		pendingSpecs:  make(map[string][]byte),
		pendingSince:  make(map[string]time.Time),
		targets:       make(map[string]*Target),
		targetOwner:   make(map[string]string),
		ownerTargetID: make(map[string]string),
		patternMeta:   make(map[string]string),
		patternOwner:  make(map[string][]string),
		opMetaByType:  make(map[string]string),
		opEffects:     make(map[string][]*guardgrammar.Guard),
	}
}

func (s *targetSource) setLoadCallback(fn func(*Target))            { s.loadCB = fn }
func (s *targetSource) setUpdateCallback(fn func(old, new *Target)) { s.updateCB = fn }

// start establishes the durable subscription and launches the dispatch
// goroutine. IncludeHistory replays the entire installed meta set on every
// boot — fresh deployment or restart alike (the durable name carries the
// instance segment plus a fresh per-boot nonce, see targetSourceDurablePrefix,
// so it is guaranteed never to have existed before).
//
// Each boot's durable name carries the instance segment (attributability) plus
// a fresh per-boot nonce (uniqueness), so a prior boot's durable is never
// reused and would otherwise linger forever as a parked consumer on
// KV_<bucket>. Before creating its own durable, start prunes any stale
// "<prefix>-*" durables left behind by no-longer-running instances; the
// durable created below is then deleted on clean shutdown (consume's
// ctx.Done branch) so it never becomes next boot's stale entry.
func (s *targetSource) start(ctx context.Context) error {
	bootNonce, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("weaver: target source boot nonce: %w", err)
	}
	durable := targetSourceDurablePrefix + "-" + s.instance + "-" + bootNonce
	if err := s.conn.PruneStaleDurables(ctx, s.bucket, targetSourceDurablePrefix+"-", durable, s.logger); err != nil {
		s.logger.Warn("weaver: prune stale target-source durables failed", "err", err)
	}
	events, err := s.conn.SubscribeKVChanges(
		ctx,
		s.bucket,
		"vtx.meta.",
		durable,
		substrate.SubscribeKVOptions{IncludeHistory: true, Logger: s.logger},
	)
	if err != nil {
		return fmt.Errorf("weaver: subscribe core KV vtx.meta.>: %w", err)
	}
	go s.consume(ctx, events, durable)
	return nil
}

func (s *targetSource) consume(ctx context.Context, events <-chan substrate.KVEvent, durable string) {
	for {
		select {
		case <-ctx.Done():
			s.deleteOwnDurable(durable)
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			s.handle(evt)
		}
	}
}

// deleteOwnDurable removes this boot's per-instance durable on clean shutdown
// so it never lingers as a stale entry for the next boot's
// PruneStaleDurables to clean up. Best-effort: ctx is already cancelled, so a
// fresh background context with a short bound is used for the delete call.
func (s *targetSource) deleteOwnDurable(durable string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.conn.DeleteDurable(ctx, s.bucket, durable); err != nil {
		s.logger.Warn("weaver: delete own target-source durable failed", "durable", durable, "err", err)
	}
}

type classProbe struct {
	Class string `json:"class"`
}

// handle dispatches one KV mutation under vtx.meta.>. A vertex carrying a
// routed class registers it (replaying any buffered spec); an aspect named
// `spec` under a routed vertex is parsed per its class. A spec aspect for a
// vertex whose class is not yet known is buffered until the class arrives; a
// known non-routed class drops the buffer (the vertex will never register a
// target or pattern). Deleting a spec aspect drops the registration it
// produced but keeps the vertex's class entry — only a vertex delete forgets
// the class, so a re-created spec registers immediately instead of buffering.
func (s *targetSource) handle(evt substrate.KVEvent) {
	switch substrate.ClassifyKey(evt.Key) {
	case substrate.KindVertex:
		_, id, ok := substrate.ParseVertexKey(evt.Key)
		if !ok {
			return
		}
		if evt.IsDeleted {
			s.removeVertex(id)
			return
		}
		var probe classProbe
		if err := json.Unmarshal(evt.Value, &probe); err != nil {
			s.logger.Debug("weaver: vertex envelope unmarshal failed", "key", evt.Key, "err", err)
			return
		}
		routed := probe.Class == weaverTargetClass || probe.Class == loomPatternClass
		s.mu.Lock()
		s.classes[id] = probe.Class
		buffered, has := s.pendingSpecs[id]
		if has {
			delete(s.pendingSpecs, id)
			delete(s.pendingSince, id)
		}
		s.mu.Unlock()
		if has {
			s.issues.clear(issueKeyPendingSpec(id))
		}
		if !routed {
			s.indexOpMeta(evt.Key, evt.Value)
			return
		}
		if has {
			s.dispatchSpec(id, probe.Class, buffered)
		}

	case substrate.KindAspect:
		_, _, id, localName, ok := substrate.ParseAspectKey(evt.Key)
		if !ok {
			return
		}
		if localName == "effects" {
			s.indexOpEffects(id, evt)
			return
		}
		if localName != "spec" {
			return
		}
		if evt.IsDeleted {
			s.removeSpec(id)
			return
		}
		s.mu.Lock()
		class, known := s.classes[id]
		if !known {
			s.pendingSpecs[id] = append([]byte(nil), evt.Value...)
			if _, waiting := s.pendingSince[id]; !waiting {
				s.pendingSince[id] = time.Now()
			}
		}
		s.mu.Unlock()
		if known {
			s.dispatchSpec(id, class, evt.Value)
		}
	}
}

func (s *targetSource) dispatchSpec(id, class string, body []byte) {
	switch class {
	case weaverTargetClass:
		s.dispatchTarget(id, body)
	case loomPatternClass:
		s.indexPattern(id, body)
	}
}

// dispatchTarget parses a meta.weaverTarget spec body, runs the §10.8
// install-time validations, and registers the target (firing the load or
// update callback that drives the lane-1 consumer reconcile). A validation
// failure rejects the target loudly: an Error log plus a Health KV issue —
// never a panic, never a silent skip.
func (s *targetSource) dispatchTarget(id string, body []byte) {
	specBody, err := unwrapSpecBody(body, "gaps")
	if err != nil {
		s.rejectTarget(id, fmt.Sprintf("spec unwrap failed: %v", err))
		return
	}
	var t Target
	if err := json.Unmarshal(specBody, &t); err != nil {
		s.rejectTarget(id, fmt.Sprintf("spec unmarshal failed: %v", err))
		return
	}
	if err := validateTarget(&t); err != nil {
		s.rejectTarget(id, err.Error())
		return
	}

	s.mu.Lock()
	// Uniqueness (§10.8): two registered targets with the same targetId is a
	// config error — keep the first, reject the later, alert.
	if owner, taken := s.targetOwner[t.TargetID]; taken && owner != id {
		s.mu.Unlock()
		s.rejectTarget(id, fmt.Sprintf("targetId %q already registered by vtx.meta.%s", t.TargetID, owner))
		return
	}
	// A spec update may rename the vertex's targetId; drop the old registration.
	old, exists := s.removeOwnedTargetLocked(id, t.TargetID)
	s.targets[t.TargetID] = &t
	s.targetOwner[t.TargetID] = id
	s.ownerTargetID[id] = t.TargetID
	s.mu.Unlock()

	s.issues.clear("target:" + id)
	if !exists {
		s.logger.Info("weaver: target loaded", "targetId", t.TargetID, "gaps", len(t.Gaps))
		if s.loadCB != nil {
			s.loadCB(&t)
		}
		return
	}
	s.logger.Info("weaver: target updated", "targetId", t.TargetID, "gaps", len(t.Gaps))
	if s.updateCB != nil {
		s.updateCB(old, &t)
	}
}

// removeOwnedTargetLocked unregisters whatever target the vertex currently
// owns, returning it. When the vertex re-registers the same targetId the entry
// is treated as an update (exists=true); a renamed targetId removes the stale
// entry so the reconcile tears its consumer down. Caller holds s.mu.
func (s *targetSource) removeOwnedTargetLocked(id, newTargetID string) (old *Target, exists bool) {
	prevID, owned := s.ownerTargetID[id]
	if !owned {
		return nil, false
	}
	old = s.targets[prevID]
	if prevID != newTargetID {
		delete(s.targets, prevID)
		delete(s.targetOwner, prevID)
		return old, false
	}
	return old, true
}

func (s *targetSource) rejectTarget(id, reason string) {
	s.logger.Error("weaver: target rejected", "metaVertex", "vtx.meta."+id, "reason", reason)
	s.issues.set("target:"+id, "error", "TargetRejected",
		"meta.weaverTarget vtx.meta."+id+" rejected: "+reason)
}

// validateTarget runs the §10.8 install-time validations: every gaps key
// matches missing_* and is a valid single KV-key segment (it is the third
// segment of the <targetId>.<entityId>.<gapColumn> mark key), no playbook
// params key collides with the engine-owned expectedRevision payload field,
// a surface gap's optional issueSeverity is one aggregateStatus can escalate
// ("warning"|"error"), and targetId is a valid single KV-key segment (it is a
// weaver-targets key prefix and a durable-name segment, so dots are forbidden).
func validateTarget(t *Target) error {
	if t.TargetID == "" {
		return fmt.Errorf("targetId is required")
	}
	if !singleTokenPattern.MatchString(t.TargetID) {
		return fmt.Errorf("targetId %q is not a valid single KV-key segment (must match %s)",
			t.TargetID, singleTokenPattern.String())
	}
	if t.Mode != "" && t.Mode != targetModeShadow && t.Mode != targetModePlanned {
		return fmt.Errorf("mode %q is not a known planner mode (%s | %s)", t.Mode, targetModeShadow, targetModePlanned)
	}
	if err := validateAdmissionPolicy(t.Admission); err != nil {
		return err
	}
	for col, ga := range t.Gaps {
		if len(col) <= len(gapColumnPrefix) || col[:len(gapColumnPrefix)] != gapColumnPrefix {
			return fmt.Errorf("gaps key %q does not match the missing_<gap> column convention", col)
		}
		if !singleTokenPattern.MatchString(col) {
			return fmt.Errorf("gaps key %q contains characters invalid in a KV key segment (it becomes the <targetId>.<entityId>.<gapColumn> mark-key segment; must match %s)",
				col, singleTokenPattern.String())
		}
		if _, reserved := ga.Params["expectedRevision"]; reserved {
			return fmt.Errorf("gaps key %q: param \"expectedRevision\" is reserved (the engine writes the OCC revision-condition under that payload field)", col)
		}
		if ga.Action == actionSurface && ga.IssueSeverity != "" && ga.IssueSeverity != "warning" && ga.IssueSeverity != "error" {
			return fmt.Errorf("gaps key %q action %q issueSeverity %q must be \"warning\" or \"error\" (omit for the \"warning\" default) — aggregateStatus only escalates those two",
				col, ga.Action, ga.IssueSeverity)
		}
		parsedGa, err := validateGapPlannerFields(col, ga)
		if err != nil {
			return err
		}
		t.Gaps[col] = parsedGa
	}
	if err := validateAugurPolicy(t.Augur); err != nil {
		return err
	}
	return nil
}

// guardPaths collects every subject-path an install-validated guard tree
// references — every KindPresent/KindAbsent/KindEquals atom's Path, recursing
// through KindAllOf/KindAnyOf/KindNot children — deduplicated. Install-time
// use only (validation), never the dispatch hot path.
func guardPaths(g *guardgrammar.Guard) map[guardgrammar.Path]bool {
	paths := make(map[guardgrammar.Path]bool)
	collectGuardPaths(g, paths)
	return paths
}

func collectGuardPaths(g *guardgrammar.Guard, out map[guardgrammar.Path]bool) {
	if g == nil {
		return
	}
	switch g.Kind {
	case guardgrammar.KindPresent, guardgrammar.KindAbsent, guardgrammar.KindEquals:
		out[g.Path] = true
	case guardgrammar.KindAllOf, guardgrammar.KindAnyOf, guardgrammar.KindNot:
		for _, c := range g.Children {
			collectGuardPaths(c, out)
		}
	}
}

// formatPath renders a guardgrammar.Path back into its §10.5 subject-path
// string, for error messages only.
func formatPath(p guardgrammar.Path) string {
	if p.Aspect == "" {
		return "subject.data." + p.Field
	}
	return "subject." + p.Aspect + ".data." + p.Field
}

// parseGoalColumns install-validates one gap's GoalColumns (Fire-6
// Increment-2 State-schema bridge): each value must parse as a well-formed
// §10.5 guard-grammar path AND must be aspect-qualified (Aspect != "") — a
// root-shaped entry is redundant (rowState already addresses every column at
// subject.data.<column> by default) and almost certainly a package-author
// mistake. Values must be unique: two columns mapping to the same path would
// make rowState's result depend on Go's nondeterministic map-iteration order
// over the row whenever both source columns are present in it. Every parsed
// path must also appear somewhere in goal — an entry goal never references is
// exactly as inert as a typo'd column name would be (there is no lens schema
// here to check the column name itself against; this is the catchable half
// of that mistake). A malformed gap rejects the whole target — same
// fail-wholesale doctrine as goal/candidates/effects.
func parseGoalColumns(col string, cols map[string]string, goal *guardgrammar.Guard) (map[string]guardgrammar.Path, error) {
	if len(cols) == 0 {
		return nil, nil
	}
	if goal == nil {
		return nil, fmt.Errorf("gaps key %q: goalColumns is set but goal is empty — nothing references the declared aspect paths", col)
	}
	paths := make(map[string]guardgrammar.Path, len(cols))
	seen := make(map[guardgrammar.Path]string, len(cols))
	for column, raw := range cols {
		p, err := guardgrammar.ParsePath(raw)
		if err != nil {
			return nil, fmt.Errorf("gaps key %q: goalColumns[%q]: %w", col, column, err)
		}
		if p.Aspect == "" {
			return nil, fmt.Errorf("gaps key %q: goalColumns[%q]: path %q is root-shaped (subject.data.<field>) — goalColumns is only for aspect-qualified paths; a root column already addresses itself by default", col, column, raw)
		}
		if other, dup := seen[p]; dup {
			return nil, fmt.Errorf("gaps key %q: goalColumns[%q] and [%q] both map to path %q — rowState's result would depend on Go's map-iteration order over the row", col, column, other, raw)
		}
		seen[p] = column
		paths[column] = p
	}
	referenced := guardPaths(goal)
	for column, p := range paths {
		if !referenced[p] {
			return nil, fmt.Errorf("gaps key %q: goalColumns[%q] (%s) is never referenced by goal — remove it or fix the path", col, column, formatPath(p))
		}
	}
	return paths, nil
}

// validateGapPlannerFields runs the §10.8 Planner-extension install-time
// validations on one gap's optional `candidates`/`goal`/`goalColumns` (Fires
// 4/6): each candidate's `pre`, and the gap's `goal`, must parse as a
// well-formed §10.5 guard (guardgrammar.Parse); a Cost must be non-negative.
// `pre` must additionally be root-shaped (Aspect == "") — rankCandidates
// (planner_shadow.go) evaluates it against rowState(row, nil), which never
// bridges an aspect path, so an aspect-shaped `pre` could never be satisfied
// by a real row and would silently make that candidate permanently
// ineligible (the same failure class GoalColumns exists to prevent for
// `goal`, but `candidates` has no analogous bridge — root-only is the
// documented convention, so an aspect-shaped `pre` is a config error, not a
// case to support). Parsed guards are cached on the returned copy
// (preGuard/goalGuard/goalColumnPaths) so the Fire-4 shadow comparison never
// re-parses per dispatch. A malformed guard rejects the WHOLE target — same
// fail-wholesale doctrine as op-DDL effects and pattern load.
func validateGapPlannerFields(col string, ga GapAction) (GapAction, error) {
	for i, cand := range ga.Candidates {
		if cand.Action == "" {
			return ga, fmt.Errorf("gaps key %q: candidates[%d] has no action", col, i)
		}
		if cand.Cost < 0 {
			return ga, fmt.Errorf("gaps key %q: candidates[%d].cost %d must be >= 0", col, i, cand.Cost)
		}
		if len(cand.Pre) > 0 {
			g, err := guardgrammar.Parse(cand.Pre)
			if err != nil {
				return ga, fmt.Errorf("gaps key %q: candidates[%d].pre: %w", col, i, err)
			}
			for p := range guardPaths(g) {
				if p.Aspect != "" {
					return ga, fmt.Errorf("gaps key %q: candidates[%d].pre: path %q is aspect-shaped — rankCandidates evaluates pre against the row's root columns only and can never see an aspect fact; author it against a root column the lens projects instead", col, i, formatPath(p))
				}
			}
			cand.preGuard = g
			ga.Candidates[i] = cand
		}
	}
	if len(ga.Goal) > 0 {
		g, err := guardgrammar.Parse(ga.Goal)
		if err != nil {
			return ga, fmt.Errorf("gaps key %q: goal: %w", col, err)
		}
		ga.goalGuard = g
	}
	if len(ga.GoalColumns) > 0 {
		paths, err := parseGoalColumns(col, ga.GoalColumns, ga.goalGuard)
		if err != nil {
			return ga, err
		}
		ga.goalColumnPaths = paths
	}
	if err := validateActionsCatalog(col, &ga); err != nil {
		return ga, err
	}
	return ga, nil
}

// validateActionsCatalog install-validates one gap's optional Actions catalog
// (design weaver-planner-mandate-design.md revision, 2026-07-05 — the
// loftspace-lease-renewal-goal-authored-target-design's R1). Required
// alongside Goal in both directions — a goal with no catalog can never
// synthesize a plan, and a catalog with no goal has nothing to synthesize
// toward — so either alone is rejected as a package-author config error, the
// same fail-wholesale doctrine as goal/candidates/effects. Each entry's Ref
// must be unique within the gap; its Pre/Effects paths must be
// row-reachable — root-shaped, or aspect-shaped AND present in this gap's
// (already-parsed) goalColumnPaths bridge — an unreachable Effects path would
// make that entry's completion permanently un-satisfiable in the row-derived
// State, and an unreachable Pre would make the entry permanently ineligible;
// each Effects atom must be a concrete assertion (planner.ApplyEffects
// rejects anyOf/not, mirrored here at install time rather than surfacing only
// as a buried search-time error). Parsed guards are cached on the entry
// (preGuard/effectGuards) so resolveGoalAction's dispatch wiring never
// re-parses per episode.
func validateActionsCatalog(col string, ga *GapAction) error {
	if len(ga.Actions) == 0 {
		if len(ga.Goal) > 0 {
			return fmt.Errorf("gaps key %q: goal is set but actions is empty — synthesis has no catalog to plan over", col)
		}
		return nil
	}
	if len(ga.Goal) == 0 {
		return fmt.Errorf("gaps key %q: actions is set but goal is empty — the catalog has no synthesis target", col)
	}
	refs := make(map[string]bool, len(ga.Actions))
	for i, entry := range ga.Actions {
		if entry.Ref == "" {
			return fmt.Errorf("gaps key %q: actions[%d] has no ref", col, i)
		}
		if refs[entry.Ref] {
			return fmt.Errorf("gaps key %q: actions[%d]: ref %q is declared more than once in this gap's catalog", col, i, entry.Ref)
		}
		refs[entry.Ref] = true
		if entry.Action == "" {
			return fmt.Errorf("gaps key %q: actions[%d] (ref %q) has no action", col, i, entry.Ref)
		}
		if entry.Cost < 0 {
			return fmt.Errorf("gaps key %q: actions[%d] (ref %q): cost %d must be >= 0", col, i, entry.Ref, entry.Cost)
		}
		if entry.Cost == 0 {
			entry.Cost = 1
		}
		if len(entry.Pre) > 0 {
			g, err := guardgrammar.Parse(entry.Pre)
			if err != nil {
				return fmt.Errorf("gaps key %q: actions[%d] (ref %q).pre: %w", col, i, entry.Ref, err)
			}
			if err := requireRowReachable(col, "actions["+entry.Ref+"].pre", g, ga.goalColumnPaths); err != nil {
				return err
			}
			entry.preGuard = g
		}
		if len(entry.Effects) == 0 {
			return fmt.Errorf("gaps key %q: actions[%d] (ref %q) has no effects — an entry with nothing it entails can never advance a plan", col, i, entry.Ref)
		}
		effectGuards := make([]*guardgrammar.Guard, len(entry.Effects))
		for j, raw := range entry.Effects {
			g, err := guardgrammar.Parse(raw)
			if err != nil {
				return fmt.Errorf("gaps key %q: actions[%d] (ref %q).effects[%d]: %w", col, i, entry.Ref, j, err)
			}
			if _, err := planner.ApplyEffects(planner.State{}, []*guardgrammar.Guard{g}); err != nil {
				return fmt.Errorf("gaps key %q: actions[%d] (ref %q).effects[%d]: %w", col, i, entry.Ref, j, err)
			}
			if err := requireRowReachable(col, fmt.Sprintf("actions[%s].effects[%d]", entry.Ref, j), g, ga.goalColumnPaths); err != nil {
				return err
			}
			effectGuards[j] = g
		}
		entry.effectGuards = effectGuards
		ga.Actions[i] = entry
	}
	return nil
}

// requireRowReachable rejects a guard tree that addresses a path no live
// planner.State can ever carry: a root path (subject.data.<field>) is always
// reachable (rowState's default mapping), and an aspect path is reachable
// only when it is one of this gap's own goalColumnPaths values (the bridge
// GoalColumns installs) — an aspect path from a DIFFERENT gap's bridge, or no
// bridge at all, can never appear in this gap's State.
func requireRowReachable(col, field string, g *guardgrammar.Guard, goalColumnPaths map[string]guardgrammar.Path) error {
	for p := range guardPaths(g) {
		if p.Aspect == "" {
			continue
		}
		reachable := false
		for _, bridged := range goalColumnPaths {
			if bridged == p {
				reachable = true
				break
			}
		}
		if !reachable {
			return fmt.Errorf("gaps key %q: %s: path %q is aspect-shaped but not bridged by this gap's goalColumns — "+
				"a row-derived State can never carry it, so this entry could never see it as (un)met", col, field, formatPath(p))
		}
	}
	return nil
}

// validateAugurPolicy runs the §10.8 "Augur escalation" structural validations
// on a target's optional augur block. A nil block is the default (the target
// fails closed on an unplannable gap) and is always valid. When present, the
// block must be actionable: at least one escalate trigger (each ∈ {unplannable,
// exhausted}). The reasoning op / adapter / replyOp are optional overrides
// (Op/Adapter/ReplyOp) — omitted, they default to CreateAugurReasoningClaim /
// augur / RecordProposal at dispatch (Option F: Weaver dispatches the reasoning
// op directly as a directOp, no Loom pattern to resolve), so a minimal block is
// just `escalate`. When set, each must be a single token (a literal op /
// adapter / op name, no key delimiters). The optional autoApply block is
// validated fail-closed (its actions ⊆ the §10.8 action table, minConfidence ∈
// [0,1]) even though no escalation path consumes it yet — the autonomy boundary
// stays human-in-the-loop until ratified, but a malformed block must never load.
func validateAugurPolicy(a *AugurPolicy) error {
	if a == nil {
		return nil
	}
	if len(a.Escalate) == 0 {
		return fmt.Errorf("augur block present but escalate is empty (omit the block to disable escalation, or list a trigger)")
	}
	for _, trig := range a.Escalate {
		if trig != escalateUnplannable && trig != escalateExhausted {
			return fmt.Errorf("augur.escalate value %q is not a known trigger (%s | %s)",
				trig, escalateUnplannable, escalateExhausted)
		}
	}
	for field, v := range map[string]string{"op": a.Op, "adapter": a.Adapter, "replyOp": a.ReplyOp, "model": a.Model} {
		if v != "" && !singleTokenPattern.MatchString(v) {
			return fmt.Errorf("augur.%s value %q must be a single token matching %s", field, v, singleTokenPattern.String())
		}
	}
	if a.AutoApply != nil {
		for _, act := range a.AutoApply.Actions {
			if act != actionTriggerLoom && act != actionAssignTask && act != actionDirectOp {
				return fmt.Errorf("augur.autoApply.actions value %q is not a known action (%s | %s | %s)",
					act, actionTriggerLoom, actionAssignTask, actionDirectOp)
			}
		}
		if a.AutoApply.MinConfidence < 0 || a.AutoApply.MinConfidence > 1 {
			return fmt.Errorf("augur.autoApply.minConfidence %v is out of range (must be in [0,1])", a.AutoApply.MinConfidence)
		}
	}
	return nil
}

// validateAdmissionPolicy runs the §10.8 "Admission control" install-time
// validations on a target's optional admission block (Fire 8). A nil block is
// the default (unbounded dispatch, byte-identical to every target before this
// fire) and is always valid. A present block must declare at least one
// positive rate — an empty block is exactly as inert as omitting it and is
// almost certainly a package-author mistake, so it is rejected rather than
// silently accepted as a no-op (the same doctrine GoalColumns' "referenced by
// goal" check applies). Every declared rate, global or per-adapter, must be
// strictly positive: a zero or negative rate is nonsensical (0 already means
// "not declared" via the field's absence) and would either wedge every
// dispatch on this axis forever or panic tokenBucket's capacity math.
func validateAdmissionPolicy(a *AdmissionPolicy) error {
	if a == nil {
		return nil
	}
	if a.GlobalRate < 0 {
		return fmt.Errorf("admission.globalRate %v must be >= 0 (0 means not declared)", a.GlobalRate)
	}
	declared := a.GlobalRate > 0
	for adapter, rate := range a.AdapterRates {
		if adapter == "" {
			return fmt.Errorf("admission.adapterRates has an empty adapter key")
		}
		if rate <= 0 {
			return fmt.Errorf("admission.adapterRates[%q] %v must be > 0 (omit the entry to leave that adapter ungoverned)", adapter, rate)
		}
		declared = true
	}
	if !declared {
		return fmt.Errorf("admission block present but declares no positive rate (omit the block to leave the target unbounded)")
	}
	return nil
}

func (s *targetSource) removeVertex(id string) {
	var removed *Target
	s.mu.Lock()
	delete(s.classes, id)
	delete(s.pendingSpecs, id)
	delete(s.pendingSince, id)
	if targetID, owned := s.ownerTargetID[id]; owned {
		removed = s.targets[targetID]
		delete(s.targets, targetID)
		delete(s.targetOwner, targetID)
		delete(s.ownerTargetID, id)
	}
	s.removePatternLocked(id)
	s.removeOpMetaLocked(id)
	s.mu.Unlock()

	s.issues.clear("target:" + id)
	s.issues.clear(issueKeyPendingSpec(id))
	if removed != nil {
		s.logger.Info("weaver: target removed", "targetId", removed.TargetID)
		if s.updateCB != nil {
			s.updateCB(removed, nil)
		}
	}
}

// removeSpec handles the deletion of a vertex's spec ASPECT: whatever the spec
// produced is dropped (target unregistered, pattern index entries removed,
// any buffered copy evicted), but the vertex's class entry survives — the
// vertex itself still exists, and its class is vertex-lifecycle state. A spec
// later re-created under the same vertex therefore dispatches immediately.
// The op-meta index is envelope-derived, so it is untouched here.
func (s *targetSource) removeSpec(id string) {
	var removed *Target
	s.mu.Lock()
	delete(s.pendingSpecs, id)
	delete(s.pendingSince, id)
	if targetID, owned := s.ownerTargetID[id]; owned {
		removed = s.targets[targetID]
		delete(s.targets, targetID)
		delete(s.targetOwner, targetID)
		delete(s.ownerTargetID, id)
	}
	s.removePatternLocked(id)
	s.mu.Unlock()

	s.issues.clear("target:" + id)
	s.issues.clear(issueKeyPendingSpec(id))
	if removed != nil {
		s.logger.Info("weaver: target spec deleted; target removed", "targetId", removed.TargetID)
		if s.updateCB != nil {
			s.updateCB(removed, nil)
		}
	}
}

func issueKeyPendingSpec(id string) string { return "pendingSpec:" + id }

// flagOrphanedSpecs raises a Health issue for every spec aspect buffered past
// pendingSpecWarnAfter still waiting for its parent vertex's class. Run on the
// heartbeat cadence. The issue clears when the spec drains (the class arrives)
// or is evicted (spec/vertex delete).
func (s *targetSource) flagOrphanedSpecs() {
	s.mu.Lock()
	stale := make([]string, 0)
	for id, since := range s.pendingSince {
		if time.Since(since) >= pendingSpecWarnAfter {
			stale = append(stale, id)
		}
	}
	s.mu.Unlock()
	for _, id := range stale {
		s.issues.set(issueKeyPendingSpec(id), "warning", "OrphanedSpec",
			"spec aspect vtx.meta."+id+".spec has waited over "+pendingSpecWarnAfter.String()+
				" for its parent vertex's class — orphaned spec")
	}
}

// --- meta.loomPattern index (triggerLoom resolution) ------------------------

// patternSpecProbe reads the patternId off a loom-pattern spec body.
type patternSpecProbe struct {
	PatternID string `json:"patternId"`
}

// indexPattern records patternId → vtx.meta.<id> for a meta.loomPattern
// vertex. The vertex id itself is indexed too, so a playbook may name a
// pattern by its canonical patternId or by the vertex NanoID; either resolves
// to the pattern vertex key the Actuator uses as patternRef and
// authContext.target.
func (s *targetSource) indexPattern(id string, body []byte) {
	specBody, err := unwrapSpecBody(body, "steps")
	if err != nil {
		s.logger.Debug("weaver: loom pattern spec unwrap failed", "patternVertex", id, "err", err)
		return
	}
	var probe patternSpecProbe
	if err := json.Unmarshal(specBody, &probe); err != nil {
		s.logger.Debug("weaver: loom pattern spec unmarshal failed", "patternVertex", id, "err", err)
		return
	}
	key := "vtx.meta." + id
	refs := []string{id}
	if probe.PatternID != "" && probe.PatternID != id {
		refs = append(refs, probe.PatternID)
	}
	s.mu.Lock()
	s.removePatternLocked(id)
	for _, ref := range refs {
		s.patternMeta[ref] = key
	}
	s.patternOwner[id] = refs
	s.mu.Unlock()
}

// removePatternLocked drops every patternId entry registered by the deleted
// pattern vertex id. Caller holds s.mu.
func (s *targetSource) removePatternLocked(id string) {
	for _, ref := range s.patternOwner[id] {
		if s.patternMeta[ref] == "vtx.meta."+id {
			delete(s.patternMeta, ref)
		}
	}
	delete(s.patternOwner, id)
}

// patternMetaKey resolves a playbook pattern reference (patternId literal,
// vertex NanoID, or full vtx.meta.<id> key) to the pattern's vtx.meta.<id>
// key, from the LIVE registry at dispatch time.
func (s *targetSource) patternMetaKey(ref string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.patternMeta[ref]; ok {
		return k, true
	}
	for _, k := range s.patternMeta {
		if k == ref {
			return k, true
		}
	}
	return "", false
}

// --- op meta-vertex index (assignTask forOperation resolution) --------------

// opMetaProbe reads the operationType scalar off an op meta-vertex envelope.
type opMetaProbe struct {
	Data struct {
		OperationType string `json:"operationType"`
	} `json:"data"`
}

// indexOpMeta records the operationType → vtx.meta.<opId> mapping for a meta
// vertex carrying data.operationType. assignTask names its bound op by
// operationType (Contract #10 §10.8); the Strategist resolves that to the op's
// meta-vertex key (the CreateTask forOperation endpoint) from this index.
func (s *targetSource) indexOpMeta(vertexKey string, body []byte) {
	var probe opMetaProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return
	}
	if probe.Data.OperationType == "" {
		return
	}
	s.mu.Lock()
	s.opMetaByType[probe.Data.OperationType] = vertexKey
	s.mu.Unlock()
}

// removeOpMetaLocked drops any operationType entry pointing at the deleted op
// meta-vertex id, plus its effects catalog entry. Caller holds s.mu.
func (s *targetSource) removeOpMetaLocked(id string) {
	key := "vtx.meta." + id
	for ot, k := range s.opMetaByType {
		if k == key {
			delete(s.opMetaByType, ot)
		}
	}
	delete(s.opEffects, id)
}

// opMetaKey returns the vtx.meta.<opId> for an operationType, or ("", false)
// when no op meta-vertex with that operationType has been observed.
func (s *targetSource) opMetaKey(operationType string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.opMetaByType[operationType]
	return k, ok
}

// opEffectsProbe reads an op-meta vertex's `.effects` aspect body: a flat list
// of §10.5 guard-grammar predicates (Contract #10 §10.8 Planner extension,
// pkgmgr's buildInstallBatch) the bound operationType's commit entails.
type opEffectsProbe struct {
	Guards []json.RawMessage `json:"guards"`
}

// indexOpEffects records the parsed effect guards for an op-meta vertex's
// (bare) id, independently of indexOpMeta's operationType index: the vertex
// envelope and its `.effects` aspect are separate CDC keys and may arrive in
// either order, so effectsCatalog joins them by id at read time rather than
// requiring a buffered arrival order (unlike a weaverTarget/loomPattern spec,
// an op-meta vertex is never "routed" — it has no class-dependent parse that
// needs the envelope first). A malformed `.effects` body is logged and
// dropped; pkgmgr's validateEffects already rejects this shape at install
// time, so this is defense-in-depth, never a live path.
func (s *targetSource) indexOpEffects(id string, evt substrate.KVEvent) {
	if evt.IsDeleted {
		s.mu.Lock()
		delete(s.opEffects, id)
		s.mu.Unlock()
		return
	}
	body, err := unwrapSpecBody(evt.Value, "guards")
	if err != nil {
		s.logger.Debug("weaver: op-meta effects aspect unwrap failed", "metaVertex", "vtx.meta."+id, "err", err)
		return
	}
	var probe opEffectsProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		s.logger.Debug("weaver: op-meta effects aspect unmarshal failed", "metaVertex", "vtx.meta."+id, "err", err)
		return
	}
	guards := make([]*guardgrammar.Guard, 0, len(probe.Guards))
	for i, raw := range probe.Guards {
		g, err := guardgrammar.Parse(raw)
		if err != nil {
			s.logger.Debug("weaver: op-meta effects guard parse failed",
				"metaVertex", "vtx.meta."+id, "index", i, "err", err)
			return
		}
		guards = append(guards, g)
	}
	s.mu.Lock()
	s.opEffects[id] = guards
	s.mu.Unlock()
}

// effectsCatalog builds a GLOBAL ops-derived goal-regression catalog from every
// op-meta vertex currently carrying both an operationType (indexOpMeta) and a
// parsed `.effects` aspect (indexOpEffects): one planner.Action per
// operationType, deterministically sorted by Ref, uniform Cost 1, nil
// Precondition (dispatch-time re-validation is what gates whether an op may
// fire).
//
// RESERVED, not wired to any live dispatch path. The live goal-regression path
// is the per-gap `actions` catalog a target authors explicitly (goalActions on
// the Target, resolved by resolveGoalAction); Contract #10 §10.8 marks a global
// auto-catalog "reserved, not implied", so this global view is not consulted at
// dispatch. It reads the same live opMetaByType/opEffects index that
// effectPathsFor (the oscillation detector) depends on, and its join semantics
// are exercised by effects_catalog_internal_test.go; it stays as the built,
// tested shape a future global-catalog consumer would adopt.
func (s *targetSource) effectsCatalog() []planner.Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	catalog := make([]planner.Action, 0, len(s.opMetaByType))
	for op, key := range s.opMetaByType {
		id, ok := strings.CutPrefix(key, "vtx.meta.")
		if !ok {
			continue
		}
		effects := s.opEffects[id]
		if len(effects) == 0 {
			continue
		}
		catalog = append(catalog, planner.Action{Ref: op, Cost: 1, Effects: effects})
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Ref < catalog[j].Ref })
	return catalog
}

// effectPathsFor returns the aspect paths that operationType's declared
// `.effects` guards concretely touch (present/absent/equals leaves, walking
// allOf; anyOf/not cannot name a definite written path and are skipped) —
// the oscillation detector's join from a dispatched actionRef to "what did
// this write" (design weaver-planner-mandate-design.md §3.4). Empty when
// operationType has no effects (or is unknown): a fresh dispatch of an
// undeclared-effects op can never be judged fighting anything.
func (s *targetSource) effectPathsFor(operationType string) []guardgrammar.Path {
	s.mu.Lock()
	key, ok := s.opMetaByType[operationType]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	id, ok := strings.CutPrefix(key, "vtx.meta.")
	if !ok {
		s.mu.Unlock()
		return nil
	}
	guards := s.opEffects[id]
	s.mu.Unlock()
	var paths []guardgrammar.Path
	for _, g := range guards {
		paths = append(paths, effectLeafPaths(g)...)
	}
	return paths
}

// effectLeafPaths walks a parsed effect guard collecting the concrete paths
// it asserts — present/absent/equals leaves, recursing into allOf; anyOf/not
// cannot name a definite written path (mirrors planner.ApplyEffects's
// concrete-assertion-only stance) and contribute nothing.
func effectLeafPaths(g *guardgrammar.Guard) []guardgrammar.Path {
	switch g.Kind {
	case guardgrammar.KindPresent, guardgrammar.KindAbsent, guardgrammar.KindEquals:
		return []guardgrammar.Path{g.Path}
	case guardgrammar.KindAllOf:
		var out []guardgrammar.Path
		for _, c := range g.Children {
			out = append(out, effectLeafPaths(c)...)
		}
		return out
	default:
		return nil
	}
}

// --- registry reads ----------------------------------------------------------

// target returns the registered target for targetId.
func (s *targetSource) target(targetID string) (*Target, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[targetID]
	return t, ok
}

// targetMetaKey resolves a registered targetId to its owning meta.weaverTarget
// vertex key (vtx.meta.<id>). The targetId is the row-key prefix / canonicalName,
// not the vertex NanoID, so the full key comes from the owner index. An Augur
// escalation needs it as the reasoning op's targetId param + the forTarget
// no-orphan endpoint. Present for any loaded target.
func (s *targetSource) targetMetaKey(targetID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.targetOwner[targetID]
	if !ok {
		return "", false
	}
	return "vtx.meta." + id, true
}

// ownerVertexID returns the vtx.meta.<id> vertex id that registered targetId,
// the same id the "target:"+id issue-cache key is keyed by (registry.go's
// rejectTarget/load path). Used by Revoke to clear that target's standing
// "target:" issue, if any.
func (s *targetSource) ownerVertexID(targetID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.targetOwner[targetID]
	return id, ok
}

// targetIDs returns the currently-registered target ids (the desired lane-1
// consumer set).
func (s *targetSource) targetIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.targets))
	for id := range s.targets {
		out = append(out, id)
	}
	return out
}

// targetCount reports how many targets are registered (heartbeat metric).
func (s *targetSource) targetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.targets)
}

// unwrapSpecBody returns either the original body (bare spec object,
// recognised by sentinelField) or the `data` sub-object when the body is a
// substrate aspect envelope wrapping the spec under `data` (the form the
// Processor write path produces).
func unwrapSpecBody(body []byte, sentinelField string) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("probe spec body: %w", err)
	}
	if _, ok := probe[sentinelField]; ok {
		return body, nil
	}
	if data, ok := probe["data"]; ok {
		return data, nil
	}
	return body, nil
}
