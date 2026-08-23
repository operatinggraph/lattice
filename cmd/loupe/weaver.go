package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The Weaver Target Studio's Observe stage (weaver-target-studio-design.md §4):
// one target-shaped model of the convergence plane, joining the pieces an
// operator otherwise joins by hand — the meta.weaverTarget body (structure),
// the weaver-targets rows (which gaps are true), the weaver-state marks (what
// is in flight, with what retry budget), the control plane's list (registered?
// disabled?), and the Weaver heartbeat (contraction trend, issues).
//
// Three routes, each a strict superset of the previous one's scan:
//
//	GET /api/weaver/targets                              — the roster
//	GET /api/weaver/target/<targetId>                    — the target map
//	GET /api/weaver/target/<targetId>/entity/<entityId>  — the entity drill
//
// Read-only throughout. weaver-targets is an ordinary lens read model;
// weaver-state is a direct operational-KV read under Loupe's inspector charter
// (the same charter corekv.go/health.go read under) and is never written here.

const (
	// weaverRowScanCap bounds one target's weaver-targets prefix scan. Every
	// selected key costs a KVGet, and a target's candidate set is entity data
	// (unbounded by design), so the aggregate gap counts are computed over at
	// most this many rows and reported as partial past it — a truncated count
	// that claimed to be total would be the exact false-green this console
	// exists to avoid.
	weaverRowScanCap = 2000
	// weaverEntityPage bounds how many entity rows the target map returns.
	// The counts above are computed over the whole scan; this only bounds the
	// roster rendered under them.
	weaverEntityPage = 200
	// weaverDefaultDirectOpBudget mirrors the engine's fallback retry budget
	// for a directOp gap whose row declares neither dispatch-suppression
	// companion (Contract #10 §10.2 — "falls back to the engine's default
	// retry budget (3 dispatches)"). Every other action with no declared
	// maxretries_<g> is unbounded, and reads as an unknown budget here.
	weaverDefaultDirectOpBudget = 3
)

// weaverTargetRow is one roster entry on #/weaver: the control plane's own
// summary plus the heartbeat-derived overlay. Contraction is a trend CLASS,
// never a count — the Weaver samples a ring of open-gap counts and publishes
// only the classification (internal/weaver/contraction.go), so there is no
// aggregate count to show here, and an all-targets row scan is exactly the
// per-render cost the single-target map exists to confine.
type weaverTargetRow struct {
	TargetID string `json:"targetId"`
	// Description is the target's authored prose, read from its meta-vertex's
	// sibling `.description` aspect. Empty for a target whose package declared
	// none, and for an orphan `__control` marker, which has no meta-vertex at all.
	Description string `json:"description,omitempty"`
	LensRef     string `json:"lensRef,omitempty"`
	Gaps        int    `json:"gaps"`
	State       string `json:"state"`
	Contraction string `json:"contraction,omitempty"`
	Frozen      bool   `json:"frozen"`
	Issues      int    `json:"issues"`
	Registered  bool   `json:"registered"`
}

// weaverBinding is one templated `row.<column>` reference an action makes, and
// whether the live rows actually carry that column. Observed is evidence, not
// proof: a target whose lens has no candidate entities yet has no rows to
// observe any column in, which is why the caller renders the distinction
// rather than a bare red.
type weaverBinding struct {
	Param  string `json:"param"`
	Column string `json:"column"`
	// Aspect is set when the binding resolved through the strategist's
	// derived-aspect form for a `reads` entry — `row.<column>.<aspect>`, where
	// the column resolves to a vertex root key and <aspect> is joined onto it.
	// Column then names the column that WAS observed, not the whole template.
	Aspect   string `json:"aspect,omitempty"`
	Observed bool   `json:"observed"`
}

// weaverAction renders one dispatch binding — a gap's explicit action, one of
// its candidates, or one entry of a goal gap's catalog. The three share the
// §10.8 action-contract shape, so they share this view.
type weaverAction struct {
	Ref           string            `json:"ref,omitempty"`
	Action        string            `json:"action"`
	Pattern       string            `json:"pattern,omitempty"`
	PatternRef    string            `json:"patternRef,omitempty"`
	PatternKnown  bool              `json:"patternKnown"`
	Subject       string            `json:"subject,omitempty"`
	Operation     string            `json:"operation,omitempty"`
	Assignee      string            `json:"assignee,omitempty"`
	Target        string            `json:"target,omitempty"`
	Adapter       string            `json:"adapter,omitempty"`
	IssueCode     string            `json:"issueCode,omitempty"`
	IssueSeverity string            `json:"issueSeverity,omitempty"`
	Params        map[string]string `json:"params,omitempty"`
	Reads         []string          `json:"reads,omitempty"`
	Cost          int               `json:"cost,omitempty"`
	Bindings      []weaverBinding   `json:"bindings,omitempty"`
	// Pre/Effects are the planner-facing triple a candidate or catalog entry
	// carries (§10.8 Planner extension) — an explicit gap action has neither.
	// Passed through verbatim (F25.2 V2): they are guard-grammar documents,
	// and rendering them structurally is this stage's job, same as Goal.
	Pre     json.RawMessage   `json:"pre,omitempty"`
	Effects []json.RawMessage `json:"effects,omitempty"`
}

// weaverGap is one gap node of the target map: the playbook entry, how it
// dispatches, and the live aggregate state over the scanned rows.
type weaverGap struct {
	Column string `json:"column"`
	// Dispatch is how the strategist resolves this gap: "action" (explicit —
	// always wins), "candidates" (planner selection), "goal" (planner
	// synthesis over the catalog), or "none" (a gaps entry that binds nothing,
	// which install validation rejects — surfaced rather than hidden).
	Dispatch   string         `json:"dispatch"`
	Action     *weaverAction  `json:"action,omitempty"`
	Candidates []weaverAction `json:"candidates,omitempty"`
	Actions    []weaverAction `json:"actions,omitempty"`
	// Goal is the raw guard body of a goal gap, passed through verbatim: it is
	// a guard-grammar document, and rendering it structurally is F25.2's job.
	Goal        json.RawMessage   `json:"goal,omitempty"`
	GoalColumns map[string]string `json:"goalColumns,omitempty"`

	Open      int  `json:"open"`
	Inflight  int  `json:"inflight"`
	Exhausted int  `json:"exhausted"`
	Observed  bool `json:"observed"`
}

// weaverEntityRow is one candidate entity on the target map's roster, ordered
// worst-first by the caller.
type weaverEntityRow struct {
	EntityID  string   `json:"entityId"`
	EntityKey string   `json:"entityKey,omitempty"`
	Violating bool     `json:"violating"`
	Open      []string `json:"open,omitempty"`
	Inflight  []string `json:"inflight,omitempty"`
	Exhausted []string `json:"exhausted,omitempty"`
}

// weaverTargetDetail is the whole target map document.
type weaverTargetDetail struct {
	TargetID string `json:"targetId"`
	// Description is the prose on the target meta-vertex's sibling
	// `.description` aspect — the same text the roster card carries.
	Description string `json:"description,omitempty"`
	MetaKey     string `json:"metaKey,omitempty"`
	LensRef     string `json:"lensRef,omitempty"`
	LensName    string `json:"lensName,omitempty"`
	// Registered reports whether the engine's control plane lists this target.
	// An unregistered id with rows or a __control marker is a real state —
	// a package uninstalled out from under its projections, or an orphan
	// trial marker — so the map renders it instead of 404ing.
	Registered  bool   `json:"registered"`
	State       string `json:"state"`
	Mode        string `json:"mode,omitempty"`
	Admission   any    `json:"admission,omitempty"`
	Augur       any    `json:"augur,omitempty"`
	Contraction string `json:"contraction,omitempty"`

	Gaps []weaverGap `json:"gaps"`
	// Unhandled lists live `missing_*` columns with no gaps entry — the
	// engine's own GapWithoutPlaybook condition, visible here statically.
	Unhandled  []string `json:"unhandled,omitempty"`
	RowColumns []string `json:"rowColumns"`

	Rows      int  `json:"rows"`
	Violating int  `json:"violating"`
	Truncated bool `json:"truncated"`

	Entities []weaverEntityRow `json:"entities"`

	Issues []weaverIssue `json:"issues"`

	// Checks is F25.2's Verify stage: read-only, view-time V1 (structural) and
	// V2 (install-verdict surfacing) findings over this one target — never a
	// stored verdict. See weaververify.go.
	Checks []weaverCheck `json:"checks"`

	// EditContext says whether this target can be re-described in natural
	// language, and why not when it cannot. Absent only when the id resolves to
	// no meta-vertex at all, since there is then nothing an edit could name.
	// See weaverauthor.go.
	EditContext *weaverEditContext `json:"editContext,omitempty"`
}

// weaverIssue is one Weaver heartbeat issue attributed to a target or entity.
type weaverIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Since    string `json:"since,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// weaverMark is a parsed weaver-state in-flight mark (Contract #10 §10.3).
type weaverMark struct {
	TargetID       string `json:"targetId,omitempty"`
	EntityKey      string `json:"entityKey,omitempty"`
	Gap            string `json:"gap,omitempty"`
	Action         string `json:"action,omitempty"`
	ClaimID        string `json:"claimId,omitempty"`
	ClaimedAt      string `json:"claimedAt,omitempty"`
	LeaseExpiresAt string `json:"leaseExpiresAt,omitempty"`
	HeldBy         string `json:"heldBy,omitempty"`
}

// weaverStateKeys is one target's weaver-state prefix scan, split by the
// reserved key shapes (Contract #10 §10.3 "Reserved (non-mark) key shapes").
// The shapes are structurally disjoint — a NanoID entityId carries no
// underscore and a gap column carries the `missing_` prefix — so a `__` token
// can never be mistaken for a mark segment.
type weaverStateKeys struct {
	// Marks maps entityId → gapColumn → the mark's full key.
	Marks map[string]map[string]string
	// Counts maps entityId → gapColumn → the __count key.
	Counts map[string]map[string]string
	// Control reports whether <targetId>.__control exists (the durable
	// dispatch-disable marker).
	Control bool
}

// splitWeaverStateKeys classifies one target's weaver-state keys.
func splitWeaverStateKeys(targetID string, keys []string) weaverStateKeys {
	out := weaverStateKeys{
		Marks:  map[string]map[string]string{},
		Counts: map[string]map[string]string{},
	}
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, targetID+".")
		if !ok {
			continue
		}
		segs := strings.Split(rest, ".")
		switch {
		case len(segs) == 1 && segs[0] == weaverControlSuffix:
			out.Control = true
		case segs[0] == weaverEffectToken:
			// <targetId>.__effect.<gapColumn>.<actionRef> — per-(gap, action)
			// effect bookkeeping, not per-entity state.
		case len(segs) == 3 && segs[2] == weaverCountToken:
			putNested(out.Counts, segs[0], segs[1], k)
		case len(segs) == 2:
			putNested(out.Marks, segs[0], segs[1], k)
		}
	}
	return out
}

const (
	weaverControlSuffix = "__control"
	weaverCountToken    = "__count"
	weaverEffectToken   = "__effect"
)

func putNested(m map[string]map[string]string, a, b, v string) {
	inner := m[a]
	if inner == nil {
		inner = map[string]string{}
		m[a] = inner
	}
	inner[b] = v
}

// orphanControlMarkers returns the targetIds carrying a `<targetId>.__control`
// disabled marker that no registered target answers to. Nothing engine-side
// sweeps one (the reconciler skips __control keys and clears markers only for
// registered targets), and a stale marker silently disables a future target
// installed under that id — so the roster surfaces them first-class.
func orphanControlMarkers(stateKeys []string, registered map[string]bool) []string {
	var out []string
	for _, k := range stateKeys {
		id, ok := strings.CutSuffix(k, "."+weaverControlSuffix)
		if !ok || id == "" || strings.Contains(id, ".") {
			continue
		}
		if !registered[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// weaverTargetSummary mirrors internal/weaver's control-plane TargetSummary —
// the reply shape of lattice.ctrl.weaver.list. Loupe decodes it here rather
// than forwarding it raw (the control relay's usual posture) because the
// roster joins it against the heartbeat and the state bucket.
type weaverTargetSummary struct {
	TargetID string   `json:"targetId"`
	LensRef  string   `json:"lensRef"`
	Gaps     []string `json:"gaps"`
	State    string   `json:"state"`
}

// weaverHeartbeat is the per-instance slice of the Weaver heartbeat this
// console reads: the contraction trajectory classes and the standing issues.
type weaverHeartbeat struct {
	Instance   string
	Trajectory map[string]string
	Issues     []weaverIssue
}

// readWeaverHeartbeats reads every live health.weaver.<instance> document.
// Trajectories are per-PROCESS in-memory state (a restart resets the ring), so
// they are reported per instance and never merged — a merged class would be
// one no process actually holds.
func readWeaverHeartbeats(keys []string, readEntry func(string) (map[string]any, bool)) []weaverHeartbeat {
	var out []weaverHeartbeat
	for _, k := range keys {
		group, inst := classifyHealthKey(k)
		if group != "weaver" || inst == "" {
			continue
		}
		doc, ok := readEntry(k)
		if !ok {
			continue
		}
		hb := weaverHeartbeat{Instance: inst}
		if m, ok := doc["metrics"].(map[string]any); ok {
			if traj, ok := m["contractionTrajectory"].(map[string]any); ok {
				hb.Trajectory = map[string]string{}
				for id, v := range traj {
					if s, ok := v.(string); ok {
						hb.Trajectory[id] = s
					}
				}
			}
		}
		if raw, ok := doc["issues"].([]any); ok {
			for _, e := range raw {
				it, ok := e.(map[string]any)
				if !ok {
					continue
				}
				hb.Issues = append(hb.Issues, weaverIssue{
					Code:     stringField(it, "code"),
					Severity: stringField(it, "severity"),
					Message:  stringField(it, "message"),
					Since:    stringField(it, "since"),
					Instance: inst,
				})
			}
		}
		out = append(out, hb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}

func stringField(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// contractionFor returns the trajectory class the instances agree on for a
// target, or "mixed" when two live instances disagree. Absent from every
// heartbeat reads as "" — not yet sampled, which is not the same as steady.
func contractionFor(hbs []weaverHeartbeat, targetID string) string {
	seen := ""
	for _, hb := range hbs {
		v, ok := hb.Trajectory[targetID]
		if !ok || v == "" {
			continue
		}
		if seen == "" {
			seen = v
			continue
		}
		if seen != v {
			return "mixed"
		}
	}
	return seen
}

// issuesNaming selects the heartbeat issues whose message names token as a
// whole word. Weaver's issue documents carry no structured target field (the
// issueCache key that holds it is not published), so attribution is by the
// message text the engine authors — every target-scoped message writes
// "target <targetId> …". Matching whole tokens, not substrings, keeps a target
// named "lease" from claiming "leaseRenewal"'s issues; the panel labels these
// as issues NAMING the target rather than issues OF it, because that is
// exactly the strength of this evidence.
func issuesNaming(hbs []weaverHeartbeat, token string) []weaverIssue {
	var out []weaverIssue
	for _, hb := range hbs {
		for _, iss := range hb.Issues {
			if messageNamesToken(iss.Message, token) {
				out = append(out, iss)
			}
		}
	}
	return out
}

// messageNamesToken reports whether msg contains token as a whole word,
// treating anything outside the NanoID/identifier alphabet as a boundary.
func messageNamesToken(msg, token string) bool {
	if token == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(msg[i:], token)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(token)
		if !identByte(msg, start-1) && !identByte(msg, end) {
			return true
		}
		i = start + 1
		if i >= len(msg) {
			return false
		}
	}
}

// identByte reports whether the byte at idx is part of an identifier token
// (letter, digit, underscore). Out-of-range reads as a boundary.
func identByte(s string, idx int) bool {
	if idx < 0 || idx >= len(s) {
		return false
	}
	c := s[idx]
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// frozenBy reports whether any TargetOscillation issue names the target. The
// issue's message is rendered verbatim by the caller: F18 deliberately declined
// to parse the fighting pair back out of the free text as brittle, and this
// surface follows that call rather than re-proposing it.
func frozenBy(issues []weaverIssue) bool {
	for _, iss := range issues {
		if iss.Code == "TargetOscillation" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The §10.8 target body
// ---------------------------------------------------------------------------

// weaverTargetBody is the parsed meta.weaverTarget spec (Contract #10 §10.8).
// Only the frozen shapes are parsed, in this one place — a shape change is a
// contract event, not silent drift here.
type weaverTargetBody struct {
	TargetID string                     `json:"targetId"`
	LensRef  string                     `json:"lensRef"`
	Gaps     map[string]weaverGapAction `json:"gaps"`
	Mode     string                     `json:"mode,omitempty"`

	Augur     json.RawMessage `json:"augur,omitempty"`
	Admission json.RawMessage `json:"admission,omitempty"`
}

// weaverGapAction is one gaps entry: the explicit action contract, plus the
// planner-extension surfaces (candidates / goal / actions catalog) which this
// stage renders read-only.
type weaverGapAction struct {
	weaverActionContract

	Candidates  []weaverActionContract `json:"candidates,omitempty"`
	Goal        json.RawMessage        `json:"goal,omitempty"`
	GoalColumns map[string]string      `json:"goalColumns,omitempty"`
	Actions     []weaverActionContract `json:"actions,omitempty"`
}

// weaverActionContract is the §10.8 action-contract shape shared by an explicit
// gap action, a candidate, and a catalog entry.
type weaverActionContract struct {
	Ref           string            `json:"ref,omitempty"`
	Action        string            `json:"action,omitempty"`
	Pattern       string            `json:"pattern,omitempty"`
	Subject       string            `json:"subject,omitempty"`
	Adapter       string            `json:"adapter,omitempty"`
	Operation     string            `json:"operation,omitempty"`
	Assignee      string            `json:"assignee,omitempty"`
	Target        string            `json:"target,omitempty"`
	IssueCode     string            `json:"issueCode,omitempty"`
	IssueSeverity string            `json:"issueSeverity,omitempty"`
	Params        map[string]string `json:"params,omitempty"`
	Reads         []string          `json:"reads,omitempty"`
	Cost          int               `json:"cost,omitempty"`
	// Pre/Effects are set only on a GapCandidate/ActionCatalogEntry (§10.8
	// Planner extension) — an explicit GapAction carries neither field at all,
	// so both decode as nil there, exactly mirroring the engine's Go shapes.
	Pre     json.RawMessage   `json:"pre,omitempty"`
	Effects []json.RawMessage `json:"effects,omitempty"`
}

// dispatchKind classifies how the strategist resolves a gap. The explicit
// action always wins (Contract #10 §10.8 planner extension: candidates are
// "consulted only when Action is empty").
func (g weaverGapAction) dispatchKind() string {
	switch {
	case g.Action != "":
		return "action"
	case len(g.Candidates) > 0:
		return "candidates"
	case len(g.Goal) > 0:
		return "goal"
	default:
		return "none"
	}
}

// renderAction turns one action contract into its view, resolving a
// triggerLoom pattern ref and flagging every `row.<column>` binding against the
// columns the live rows carry.
func renderAction(a weaverActionContract, observed map[string]bool, patterns map[string]string) weaverAction {
	v := weaverAction{
		Ref:           a.Ref,
		Action:        a.Action,
		Pattern:       a.Pattern,
		Subject:       a.Subject,
		Operation:     a.Operation,
		Assignee:      a.Assignee,
		Target:        a.Target,
		Adapter:       a.Adapter,
		IssueCode:     a.IssueCode,
		IssueSeverity: a.IssueSeverity,
		Params:        a.Params,
		Reads:         a.Reads,
		Cost:          a.Cost,
		Pre:           a.Pre,
		Effects:       a.Effects,
	}
	if a.Pattern != "" {
		if ref, ok := patterns[a.Pattern]; ok {
			v.PatternRef = ref
			v.PatternKnown = true
		}
	}
	// Exact row-column lookups: the strategist substitutes row.<column>
	// verbatim for these and a dotted name is simply a column that does not
	// exist ("Params/Target/Operation stay exact row-column lookups, since a
	// param value isn't necessarily a composable key").
	exact := map[string]string{}
	if a.Subject != "" {
		exact["subject"] = a.Subject
	}
	if a.Assignee != "" {
		exact["assignee"] = a.Assignee
	}
	if a.Target != "" {
		exact["target"] = a.Target
	}
	for k, p := range a.Params {
		exact["params."+k] = p
	}
	keys := make([]string, 0, len(exact))
	for k := range exact {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if b, ok := templateBinding(k, exact[k], observed, false); ok {
			v.Bindings = append(v.Bindings, b)
		}
	}
	for _, r := range a.Reads {
		if b, ok := templateBinding("reads."+r, r, observed, true); ok {
			v.Bindings = append(v.Bindings, b)
		}
	}
	return v
}

// templateBinding classifies one templated param value against the observed
// row columns. A literal (no `row.` prefix) is not a binding at all.
//
// derivedAspectOK mirrors the strategist's read-only fallback: for a `reads`
// entry ONLY, `row.<column>.<aspect>` is legal — the column resolves to a
// vertex root key and <aspect> is joined onto it (the Starlark `unit +
// ".listing"` idiom). Without that precedence a perfectly working read like
// `row.unitKey.listing` reports its whole dotted text as an unobserved column
// and the panel paints a false red on a live target — which is exactly what
// the first live run of this surface did.
func templateBinding(param, value string, observed map[string]bool, derivedAspectOK bool) (weaverBinding, bool) {
	col, ok := strings.CutPrefix(value, "row.")
	if !ok {
		return weaverBinding{}, false
	}
	if observed[col] {
		return weaverBinding{Param: param, Column: col, Observed: true}, true
	}
	if derivedAspectOK {
		if base, aspect, found := strings.Cut(col, "."); found && observed[base] {
			return weaverBinding{Param: param, Column: base, Aspect: aspect, Observed: true}, true
		}
	}
	return weaverBinding{Param: param, Column: col}, true
}

// ---------------------------------------------------------------------------
// Row scan
// ---------------------------------------------------------------------------

// weaverRowScan is the aggregate over one target's weaver-targets rows.
type weaverRowScan struct {
	Rows      int
	Violating int
	Truncated bool
	// Columns is every column name observed across the scanned rows.
	Columns map[string]bool
	// OpenByGap counts rows whose missing_<gap> column is true.
	OpenByGap map[string]int
	// Entities holds the per-entity summary, in scan order.
	Entities []weaverEntityRow
	// Budgets maps entityId → gapColumn → the row's declared maxretries_<g>
	// (absent when the row declares none).
	Budgets map[string]map[string]int
	// Samples holds the first observed string value per column, across
	// scanned rows in order — F25.2 V1's evidence for a `row.<column>` subject
	// binding's TYPE (the classified vertex type of the sampled key), since no
	// column carries a declared type anywhere in the playbook. One sample is
	// weak evidence (a lens can legitimately mix key shapes across rows only
	// if the playbook itself is wrong), never proof — the caller labels it so.
	Samples map[string]string
}

// scanWeaverRows folds one target's rows into the aggregate the target map
// renders. docs is keyed by entityId; a row that failed to decode is counted in
// Rows but contributes no columns, so a malformed row is never silently
// dropped from the total.
func scanWeaverRows(docs map[string]map[string]any, order []string, truncated bool) weaverRowScan {
	scan := weaverRowScan{
		Truncated: truncated,
		Columns:   map[string]bool{},
		OpenByGap: map[string]int{},
		Budgets:   map[string]map[string]int{},
		Samples:   map[string]string{},
	}
	for _, id := range order {
		doc := docs[id]
		scan.Rows++
		row := weaverEntityRow{EntityID: id}
		if doc == nil {
			scan.Entities = append(scan.Entities, row)
			continue
		}
		row.EntityKey, _ = doc["entityKey"].(string)
		row.Violating, _ = doc["violating"].(bool)
		if row.Violating {
			scan.Violating++
		}
		for col, v := range doc {
			scan.Columns[col] = true
			if s, ok := v.(string); ok && s != "" {
				if _, have := scan.Samples[col]; !have {
					scan.Samples[col] = s
				}
			}
			if gapCol, ok := strings.CutPrefix(col, "maxretries_"); ok {
				if n, ok := numericField(v); ok && n > 0 {
					putNestedInt(scan.Budgets, id, "missing_"+gapCol, n)
				}
				continue
			}
			if !strings.HasPrefix(col, "missing_") {
				continue
			}
			if b, _ := v.(bool); b {
				scan.OpenByGap[col]++
				row.Open = append(row.Open, col)
			}
		}
		sort.Strings(row.Open)
		scan.Entities = append(scan.Entities, row)
	}
	return scan
}

func numericField(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func putNestedInt(m map[string]map[string]int, a, b string, v int) {
	inner := m[a]
	if inner == nil {
		inner = map[string]int{}
		m[a] = inner
	}
	inner[b] = v
}

// gapBudget returns the retry budget in force for one (entity, gap), and
// whether it is known. A declared maxretries_<g> always wins, however small
// (Contract #10 §10.2); a directOp gap declaring neither companion falls back
// to the engine's default; every other undeclared gap is unbounded, which
// reads as unknown rather than as a fabricated ceiling.
func gapBudget(declared map[string]int, gapColumn, action string) (int, bool) {
	if n, ok := declared[gapColumn]; ok && n > 0 {
		return n, true
	}
	if action == "directOp" {
		return weaverDefaultDirectOpBudget, true
	}
	return 0, false
}

// buildTargetDetail joins the parsed body, the row scan, the state keys, the
// count values and the heartbeat into the target map document.
func buildTargetDetail(
	targetID string,
	body *weaverTargetBody,
	metaKey, lensName, description string,
	summary *weaverTargetSummary,
	scan weaverRowScan,
	state weaverStateKeys,
	counts map[string]map[string]int,
	patterns map[string]string,
	patternSubject map[string]string,
	hbs []weaverHeartbeat,
) weaverTargetDetail {
	d := weaverTargetDetail{
		TargetID:    targetID,
		Description: description,
		MetaKey:     metaKey,
		LensName:    lensName,
		Rows:        scan.Rows,
		Violating:   scan.Violating,
		Truncated:   scan.Truncated,
		Contraction: contractionFor(hbs, targetID),
		Issues:      issuesNaming(hbs, targetID),
	}
	if summary != nil {
		d.Registered = true
		d.State = summary.State
		d.LensRef = summary.LensRef
	} else if state.Control {
		// Unregistered but carrying the durable disable marker: a future
		// registration under this id starts disabled (weaver.md's revoke
		// semantics), which is the whole point of surfacing it.
		d.State = "disabled"
	}
	if body != nil {
		d.Mode = body.Mode
		if d.LensRef == "" {
			d.LensRef = body.LensRef
		}
		if len(body.Augur) > 0 {
			d.Augur = json.RawMessage(body.Augur)
		}
		if len(body.Admission) > 0 {
			d.Admission = json.RawMessage(body.Admission)
		}
	}

	// In-flight is per (entity, gap): the mark's existence IS the in-flight
	// fact, so no per-mark GET is needed for the aggregate.
	inflightByGap := map[string]int{}
	for _, gaps := range state.Marks {
		for col := range gaps {
			inflightByGap[col]++
		}
	}
	exhaustedByGap := map[string]int{}
	exhaustedRows := map[string]map[string]bool{}

	gapAction := map[string]string{}
	if body != nil {
		for col, g := range body.Gaps {
			gapAction[col] = g.Action
		}
	}
	for entityID, gaps := range counts {
		for col, n := range gaps {
			budget, known := gapBudget(scan.Budgets[entityID], col, gapAction[col])
			if !known || n < budget {
				continue
			}
			exhaustedByGap[col]++
			if exhaustedRows[entityID] == nil {
				exhaustedRows[entityID] = map[string]bool{}
			}
			exhaustedRows[entityID][col] = true
		}
	}

	observed := scan.Columns
	if body != nil {
		cols := make([]string, 0, len(body.Gaps))
		for col := range body.Gaps {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, col := range cols {
			g := body.Gaps[col]
			view := weaverGap{
				Column:      col,
				Dispatch:    g.dispatchKind(),
				Goal:        g.Goal,
				GoalColumns: g.GoalColumns,
				Open:        scan.OpenByGap[col],
				Inflight:    inflightByGap[col],
				Exhausted:   exhaustedByGap[col],
				Observed:    observed[col],
			}
			if g.Action != "" {
				a := renderAction(g.weaverActionContract, observed, patterns)
				view.Action = &a
			}
			for _, c := range g.Candidates {
				view.Candidates = append(view.Candidates, renderAction(c, observed, patterns))
			}
			for _, c := range g.Actions {
				view.Actions = append(view.Actions, renderAction(c, observed, patterns))
			}
			// F25.2 V2 — "candidates ranked with their pres": Cost ascending,
			// Ref lexicographic on ties, the same canonical tie-break
			// internal/weaver/planner applies (planner.Action.Ref doc).
			// Structural display only — the studio never ranks a live pick.
			rankActionsByCostThenRef(view.Candidates)
			rankActionsByCostThenRef(view.Actions)
			d.Gaps = append(d.Gaps, view)
		}
	} else if summary != nil {
		// No readable body (the meta vertex is gone, or its spec aspect is
		// unreadable) — the control plane still knows the gap columns, so the
		// map degrades to structure-less gap nodes rather than an empty page.
		for _, col := range summary.Gaps {
			d.Gaps = append(d.Gaps, weaverGap{
				Column:   col,
				Dispatch: "none",
				Open:     scan.OpenByGap[col],
				Inflight: inflightByGap[col],
				Observed: observed[col],
			})
		}
	}

	declared := map[string]bool{}
	for _, g := range d.Gaps {
		declared[g.Column] = true
	}
	for col := range observed {
		if strings.HasPrefix(col, "missing_") && !declared[col] {
			d.Unhandled = append(d.Unhandled, col)
		}
	}
	sort.Strings(d.Unhandled)

	d.RowColumns = make([]string, 0, len(observed))
	for col := range observed {
		d.RowColumns = append(d.RowColumns, col)
	}
	sort.Strings(d.RowColumns)

	// Per-entity in-flight / exhausted, then worst-first ordering.
	for i := range scan.Entities {
		e := &scan.Entities[i]
		for col := range state.Marks[e.EntityID] {
			e.Inflight = append(e.Inflight, col)
		}
		sort.Strings(e.Inflight)
		for col := range exhaustedRows[e.EntityID] {
			e.Exhausted = append(e.Exhausted, col)
		}
		sort.Strings(e.Exhausted)
	}
	sortEntitiesWorstFirst(scan.Entities)
	if len(scan.Entities) > weaverEntityPage {
		scan.Entities = scan.Entities[:weaverEntityPage]
	}
	d.Entities = scan.Entities
	if d.Entities == nil {
		d.Entities = []weaverEntityRow{}
	}
	if d.Gaps == nil {
		d.Gaps = []weaverGap{}
	}
	if d.Issues == nil {
		d.Issues = []weaverIssue{}
	}
	d.Checks = computeTargetChecks(d, scan, patternSubject, hbs)
	return d
}

// sortEntitiesWorstFirst puts the entities an operator must look at first:
// budget-exhausted (remediation has given up), then still-open gaps, then
// in-flight, then violating, then by id. A closed, non-violating row sorts
// last — it is the state the target is converging toward.
func sortEntitiesWorstFirst(rows []weaverEntityRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if len(a.Exhausted) != len(b.Exhausted) {
			return len(a.Exhausted) > len(b.Exhausted)
		}
		if len(a.Open) != len(b.Open) {
			return len(a.Open) > len(b.Open)
		}
		if len(a.Inflight) != len(b.Inflight) {
			return len(a.Inflight) > len(b.Inflight)
		}
		if a.Violating != b.Violating {
			return a.Violating
		}
		return a.EntityID < b.EntityID
	})
}

// ---------------------------------------------------------------------------
// Entity drill
// ---------------------------------------------------------------------------

// weaverEntityGap is one gap's state for a single candidate entity.
type weaverEntityGap struct {
	Column string `json:"column"`
	// State is one of open / inflight / exhausted / closed.
	State  string        `json:"state"`
	Action *weaverAction `json:"action,omitempty"`
	Mark   *weaverMark   `json:"mark,omitempty"`
	// Dispatches is the recorded __count for this (entity, gap); Budget is the
	// ceiling in force, omitted when unbounded.
	Dispatches  int  `json:"dispatches,omitempty"`
	Budget      int  `json:"budget,omitempty"`
	BudgetKnown bool `json:"budgetKnown"`
	// Artifact links an IN-FLIGHT dispatch to what it produced. A closed gap's
	// mark is deleted (that is what closed means), so a completed dispatch is
	// never re-derived here — it is reached through the Flows/Tasks views.
	Artifact *weaverArtifact `json:"artifact,omitempty"`
}

// weaverArtifact is the dispatched artifact a live mark points at, its id
// derived the way the engine derived it (Contract #10 §10.3 claimId seeding).
//
// Live reports whether that id is present in the ENGINE's own live state — a
// loom-state instance record, a Core KV task vertex — and is deliberately not
// a gate on the link. Two different things make it false and both are worth
// seeing: the derivation mirror has drifted from the engine's (the link then
// goes nowhere, which is the failure this check exists to expose rather than
// hide), or the artifact has terminated while its gap stayed open (Contract
// #10 §10.3: a still-open gap whose instance terminated clears by
// level-reconciled mark-clearing, not by re-triggering). The Chronicler's flow
// history outlives Loom's live record, so the link is still worth offering in
// the second case — the caller labels it instead of suppressing it.
type weaverArtifact struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Href string `json:"href,omitempty"`
	Live bool   `json:"live"`
}

// weaverEntityDetail is the entity drill document.
type weaverEntityDetail struct {
	TargetID  string            `json:"targetId"`
	EntityID  string            `json:"entityId"`
	EntityKey string            `json:"entityKey,omitempty"`
	Found     bool              `json:"found"`
	Violating bool              `json:"violating"`
	Row       map[string]any    `json:"row,omitempty"`
	Gaps      []weaverEntityGap `json:"gaps"`
	Issues    []weaverIssue     `json:"issues"`
}

// deriveWeaverID mirrors internal/weaver's deterministic id derivation
// (actuator.go deriveID): a namespaced SHA-256 over the seed, folded onto the
// Contract #1 NanoID alphabet. Loupe holds a reader's copy — the engine is the
// only writer — and every derived id is confirmed to exist before it is
// rendered as a link, so a drift in this mirror shows up as "not found by
// derivation", never as a dead link presented as live.
func deriveWeaverID(namespace, seed string, revision uint64) string {
	var rev [8]byte
	binary.BigEndian.PutUint64(rev[:], revision)
	sum := sha256.Sum256(append([]byte(namespace+seed+":"), rev[:]...))
	id := make([]byte, substrate.NanoIDLength)
	digest := sum[:]
	di := 0
	for i := 0; i < substrate.NanoIDLength; i++ {
		if di >= len(digest) {
			next := sha256.Sum256(digest)
			digest = next[:]
			di = 0
		}
		id[i] = substrate.Alphabet[int(digest[di])%len(substrate.Alphabet)]
		di++
	}
	return string(id)
}

// deriveWeaverTaskID / deriveWeaverInstanceID mirror the open-episode-keyed
// derivations an assignTask / triggerLoom dispatch supplies (Contract #10
// §10.3): seeded on the mark's claimId, which is minted at the mark's
// CAS-create and preserved verbatim across every reclaim — so the id computed
// from the LIVE mark is the id the dispatch used. An external gap's reclaim
// re-mints claimId, which is exactly why this is never cached.
func deriveWeaverTaskID(targetID, entityID, gapColumn, claimID string) string {
	return deriveWeaverID("task:", targetID+"\x00"+entityID+"\x00"+gapColumn+"\x00"+claimID, 0)
}

func deriveWeaverInstanceID(targetID, entityID, gapColumn, claimID string) string {
	return deriveWeaverID("instance:", targetID+"\x00"+entityID+"\x00"+gapColumn+"\x00"+claimID, 0)
}

// markArtifact derives the artifact an in-flight mark's dispatch produced. The
// mark's own `action` is authoritative (it records the actionRef chosen at
// CAS-create, which for a planner-resolved gap need not be the playbook's
// first entry); the playbook action is the fallback for a legacy mark that
// carries none. Returns nil for an action that produces no addressable
// artifact (directOp submits an op, surface dispatches nothing).
func markArtifact(targetID, entityID, gapColumn string, mark *weaverMark, playbookAction string) *weaverArtifact {
	if mark == nil {
		return nil
	}
	action := mark.Action
	if action == "" {
		action = playbookAction
	}
	switch action {
	case "triggerLoom":
		id := deriveWeaverInstanceID(targetID, entityID, gapColumn, mark.ClaimID)
		return &weaverArtifact{Kind: "flow", ID: id, Href: "#/flows/" + id}
	case "assignTask":
		id := deriveWeaverTaskID(targetID, entityID, gapColumn, mark.ClaimID)
		return &weaverArtifact{Kind: "task", ID: id, Href: "#/graph/vtx.task." + id}
	}
	return nil
}

// buildEntityDetail joins one row against its marks and counts.
func buildEntityDetail(
	targetID, entityID string,
	row map[string]any,
	body *weaverTargetBody,
	summaryGaps []string,
	marks map[string]*weaverMark,
	counts map[string]int,
	patterns map[string]string,
	hbs []weaverHeartbeat,
) weaverEntityDetail {
	d := weaverEntityDetail{TargetID: targetID, EntityID: entityID, Row: row}
	d.Found = row != nil || len(marks) > 0
	if row != nil {
		d.EntityKey, _ = row["entityKey"].(string)
		d.Violating, _ = row["violating"].(bool)
	}
	for _, m := range marks {
		if d.EntityKey == "" && m != nil {
			d.EntityKey = m.EntityKey
		}
	}

	observed := map[string]bool{}
	budgets := map[string]int{}
	for col, v := range row {
		observed[col] = true
		if gapCol, ok := strings.CutPrefix(col, "maxretries_"); ok {
			if n, ok := numericField(v); ok && n > 0 {
				budgets["missing_"+gapCol] = n
			}
		}
	}

	cols := gapColumnsOf(body, summaryGaps)
	// A mark or count for a column the playbook no longer declares is real
	// residue — render it rather than dropping it.
	for col := range marks {
		if !containsString(cols, col) {
			cols = append(cols, col)
		}
	}
	for col := range counts {
		if !containsString(cols, col) {
			cols = append(cols, col)
		}
	}
	sort.Strings(cols)

	for _, col := range cols {
		g := weaverEntityGap{Column: col}
		var playbookAction string
		if body != nil {
			if ga, ok := body.Gaps[col]; ok {
				playbookAction = ga.Action
				if ga.Action != "" {
					a := renderAction(ga.weaverActionContract, observed, patterns)
					g.Action = &a
				}
			}
		}
		g.Mark = marks[col]
		g.Dispatches = counts[col]
		if n, known := gapBudget(budgets, col, playbookAction); known {
			g.Budget = n
			g.BudgetKnown = true
		}
		open, _ := row[col].(bool)
		switch {
		case g.BudgetKnown && g.Dispatches >= g.Budget:
			g.State = "exhausted"
		case g.Mark != nil:
			g.State = "inflight"
		case open:
			g.State = "open"
		default:
			g.State = "closed"
		}
		if g.Mark != nil {
			g.Artifact = markArtifact(targetID, entityID, col, g.Mark, playbookAction)
		}
		d.Gaps = append(d.Gaps, g)
	}
	if d.Gaps == nil {
		d.Gaps = []weaverEntityGap{}
	}
	d.Issues = issuesNaming(hbs, entityID)
	if d.Issues == nil {
		d.Issues = []weaverIssue{}
	}
	return d
}

func gapColumnsOf(body *weaverTargetBody, fallback []string) []string {
	if body != nil {
		out := make([]string, 0, len(body.Gaps))
		for col := range body.Gaps {
			out = append(out, col)
		}
		return out
	}
	return append([]string(nil), fallback...)
}

func containsString(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *server) handleWeaver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	parts := splitNonEmpty(strings.TrimPrefix(r.URL.Path, "/api/weaver/"))
	switch {
	case len(parts) == 1 && parts[0] == "targets":
		s.weaverTargets(w, r)
	case len(parts) == 1 && parts[0] == "verify":
		s.weaverVerify(w, r)
	case len(parts) == 2 && parts[0] == "target":
		s.weaverTargetMap(w, r, parts[1])
	case len(parts) == 4 && parts[0] == "target" && parts[2] == "entity":
		s.weaverEntity(w, r, parts[1], parts[3])
	default:
		s.writeError(w, http.StatusBadRequest,
			"expected GET /api/weaver/targets, /api/weaver/verify, /api/weaver/target/<targetId>, or /api/weaver/target/<targetId>/entity/<entityId>")
	}
}

// listWeaverTargets reads the engine's control-plane roster. A control plane
// that does not answer is reported as such: an empty roster and a live engine
// are different facts, and a silent empty list would read as "no targets".
func (s *server) listWeaverTargets(ctx context.Context, conn *substrate.Conn) ([]weaverTargetSummary, error) {
	raw, err := s.controlRequest(ctx, conn, controlComponents["weaver"].reads["list"])
	if err != nil {
		return nil, err
	}
	var reply struct {
		Targets []weaverTargetSummary `json:"targets"`
		Error   string                `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Error != "" {
		return nil, errString(reply.Error)
	}
	return reply.Targets, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func (s *server) weaverTargets(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	summaries, listErr := s.listWeaverTargets(ctx, conn)
	registered := make(map[string]bool, len(summaries))
	for _, t := range summaries {
		registered[t.TargetID] = true
	}

	healthKeys, readEntry, _, _, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	hbs := readWeaverHeartbeats(healthKeys, readEntry)

	// A key listing (no per-key GET) of weaver-state: __control is a key
	// SUFFIX, so it cannot be prefix-scoped, and the engine itself enumerates
	// this bucket the same way at Start (seedDisabledTargets).
	stateKeys, stateErr := conn.KVListKeys(ctx, bootstrap.WeaverStateBucket)

	// Prose lives in Core KV, not on the control plane's summary, so it is read
	// separately — and a failure to read it degrades the roster to a nameless
	// list rather than 502ing a page whose substance came from the engine. The
	// failure is reported instead of swallowed: a card silently missing an
	// authored description would read as "this target has none".
	var descriptions map[string]string
	readers, descErr := s.weaverCoreReaders(ctx, conn)
	if descErr == nil {
		descriptions = weaverTargetDescriptions(readers.metaKeys, readers.coreGet)
	}

	rows := make([]weaverTargetRow, 0, len(summaries))
	for _, t := range summaries {
		issues := issuesNaming(hbs, t.TargetID)
		rows = append(rows, weaverTargetRow{
			TargetID:    t.TargetID,
			Description: descriptions[t.TargetID],
			LensRef:     t.LensRef,
			Gaps:        len(t.Gaps),
			State:       t.State,
			Contraction: contractionFor(hbs, t.TargetID),
			Frozen:      frozenBy(issues),
			Issues:      len(issues),
			Registered:  true,
		})
	}

	out := map[string]any{
		"targets":   rows,
		"instances": len(hbs),
	}
	if listErr != nil {
		out["listError"] = listErr.Error()
	}
	if descErr != nil {
		out["descriptionError"] = descErr.Error()
	}
	if stateErr != nil {
		out["stateError"] = stateErr.Error()
	} else {
		orphans := orphanControlMarkers(stateKeys, registered)
		if orphans == nil {
			orphans = []string{}
		}
		out["orphanControl"] = orphans
	}
	s.writeJSON(w, http.StatusOK, out)
}

// weaverReaders bundles the per-request Core KV accessors the target model
// needs, so the two detail handlers build them identically.
type weaverReaders struct {
	coreGet  kvGetter
	metaKeys []string
}

func (s *server) weaverCoreReaders(ctx context.Context, conn *substrate.Conn) (weaverReaders, error) {
	metaKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.meta.")
	if err != nil {
		return weaverReaders{}, err
	}
	return weaverReaders{
		coreGet: func(key string) ([]byte, bool) {
			entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
			if err != nil {
				return nil, false
			}
			return entry.Value, true
		},
		metaKeys: metaKeys,
	}, nil
}

// weaverMetaIndex resolves a targetId to its meta.weaverTarget vertex (with the
// body already parsed) and a patternId to its meta.loomPattern vertex.
//
// Both are keyed off the SPEC BODY's own id field, exactly as the engine's
// registry keys them (`targetOwner` off the spec's `targetId`, `patternMeta`
// off `patternId`). canonicalName is the wrong key twice over: a weaverTarget
// meta carries no canonicalName aspect at all, and the lens a target binds
// routinely DOES carry the target's name (`leaseApplicationComplete` is the
// lens's canonicalName on the dev stack) — so a name-keyed index resolves the
// target id to the lens vertex, the playbook silently fails to parse, and every
// gap reads as an unhandled column.
type weaverMetaIndex struct {
	Targets  map[string]string
	Bodies   map[string]*weaverTargetBody
	Patterns map[string]string
	// PatternSubject maps a pattern's vtx.meta.<id> key to its declared
	// subjectType (internal/loom's Pattern.SubjectType) — F25.2 V1's structural
	// twin of the engine's own dispatch-time subject-type check.
	PatternSubject map[string]string
}

// buildWeaverMetaIndex indexes exactly the metas that carry a `spec` aspect —
// lenses, Loom patterns and Weaver targets. That filter is what keeps this off
// the whole meta corpus: the listing is one prefix scan, and each spec-carrying
// meta costs exactly one GET (the spec itself, which also yields the parsed
// body — no second read to open the playbook).
func buildWeaverMetaIndex(metaKeys []string, get kvGetter) weaverMetaIndex {
	index := weaverMetaIndex{
		Targets:        map[string]string{},
		Bodies:         map[string]*weaverTargetBody{},
		Patterns:       map[string]string{},
		PatternSubject: map[string]string{},
	}
	for _, k := range metaKeys {
		root, ok := strings.CutSuffix(k, ".spec")
		if !ok || classifyKey(root) != classMeta {
			continue
		}
		d := metaData(get, k)
		if d == nil {
			continue
		}
		if patternID, _ := d["patternId"].(string); patternID != "" {
			// The engine resolves a playbook's pattern ref by patternId OR by
			// the vertex NanoID (registry.go indexPattern), so both resolve
			// here too — a playbook authored either way must not read as
			// "pattern not installed".
			putIfAbsent(index.Patterns, patternID, root)
			putIfAbsent(index.Patterns, strings.TrimPrefix(root, "vtx.meta."), root)
			if subjectType, _ := d["subjectType"].(string); subjectType != "" {
				index.PatternSubject[root] = subjectType
			}
			continue
		}
		targetID, _ := d["targetId"].(string)
		if targetID == "" {
			continue
		}
		if _, dup := index.Targets[targetID]; dup {
			// targetId uniqueness across installed targets is install-validated
			// (the weaver-targets bucket is shared), so a duplicate is a corpus
			// anomaly — first writer wins rather than a silent overwrite.
			continue
		}
		index.Targets[targetID] = root
		index.Bodies[targetID] = parseWeaverTargetBody(d)
	}
	return index
}

// buildLensCanonicalIndex maps every installed lens's canonicalName to its
// bare NanoID meta id — the resolution a Studio-authored (or hydrated-then-
// re-proposed) weaverTarget's LensRef needs before SubmitCapabilityProposal
// (cmd/loupe/weaverauthor.go's resolveWeaverTargetLensRefs), mirroring
// internal/pkgmgr/build.go's own resolveLensRef precondition: an installed
// target's LensRef is always a bare NanoID, never a canonicalName, because a
// Studio proposal applies as its own single-artifact Definition (no Lenses
// list of its own for resolveLensRef to match against in-Definition).
//
// Walks the SAME spec-carrying metas buildWeaverMetaIndex does (one GET per
// meta, no second read), filtered to the ones carrying NEITHER targetId NOR
// patternId — buildWeaverMetaIndex's own discriminator, applied here so a
// weaverTarget or loomPattern spec (which may incidentally share its name
// with a lens on the dev stack, per that function's own doc comment) can
// never resolve into this index. First writer wins on a canonicalName
// collision, matching buildWeaverMetaIndex's targetId-collision policy.
func buildLensCanonicalIndex(metaKeys []string, get kvGetter) map[string]string {
	index := map[string]string{}
	for _, k := range metaKeys {
		root, ok := strings.CutSuffix(k, ".spec")
		if !ok || classifyKey(root) != classMeta {
			continue
		}
		d := metaData(get, k)
		if d == nil {
			continue
		}
		if patternID, _ := d["patternId"].(string); patternID != "" {
			continue
		}
		if targetID, _ := d["targetId"].(string); targetID != "" {
			continue
		}
		name, _ := d["canonicalName"].(string)
		if name == "" {
			continue
		}
		putIfAbsent(index, name, strings.TrimPrefix(root, "vtx.meta."))
	}
	return index
}

// weaverTargetDescriptions maps each described target's targetId to the prose
// on its meta-vertex's sibling `.description` aspect (pkgmgr emits it from a
// WeaverTargetSpec.Description; the body is the same `{"text": …}` a role's
// description carries).
//
// It reads Core KV directly, which Loupe alone among applications may do — it
// is the console inspector. The scan is deliberately narrower than
// buildWeaverMetaIndex: only a meta carrying BOTH a `.spec` and a
// `.description` key is opened, so the GET count is bounded by the number of
// described targets rather than by the whole spec-carrying corpus. That
// matters because the roster is the Weaver landing page. The two-suffix filter
// is a cheap prefilter, not the identity test — a lens or pattern that ever
// gains a description is still excluded by the `targetId` check below, exactly
// as buildWeaverMetaIndex keys targets off the spec body's own id.
func weaverTargetDescriptions(metaKeys []string, get kvGetter) map[string]string {
	described := make(map[string]bool)
	for _, k := range metaKeys {
		if root, ok := strings.CutSuffix(k, ".description"); ok && classifyKey(root) == classMeta {
			described[root] = true
		}
	}
	out := map[string]string{}
	if len(described) == 0 {
		return out
	}
	for _, k := range metaKeys {
		root, ok := strings.CutSuffix(k, ".spec")
		if !ok || !described[root] {
			continue
		}
		targetID, _ := metaData(get, k)["targetId"].(string)
		if targetID == "" {
			continue
		}
		text := dataString(metaData(get, root+".description"), "text", "value")
		if text == "" {
			continue
		}
		// First writer wins on a duplicate targetId, matching
		// buildWeaverMetaIndex's handling of the same corpus anomaly.
		if _, dup := out[targetID]; !dup {
			out[targetID] = text
		}
	}
	return out
}

func putIfAbsent(m map[string]string, k, v string) {
	if k == "" {
		return
	}
	if _, ok := m[k]; !ok {
		m[k] = v
	}
}

// parseWeaverTargetBody decodes a target meta's spec data — which IS the §10.8
// body (the substrate aspect envelope wraps it under `data`, which metaData
// unwraps) — into the frozen shapes this model reads.
func parseWeaverTargetBody(d map[string]any) *weaverTargetBody {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	var body weaverTargetBody
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	return &body
}

func (s *server) weaverTargetMap(w http.ResponseWriter, r *http.Request, targetID string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if err := validateControlName(targetID); err != nil {
		s.writeError(w, http.StatusBadRequest, "targetId: "+err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	summaries, _ := s.listWeaverTargets(ctx, conn)
	var summary *weaverTargetSummary
	for i := range summaries {
		if summaries[i].TargetID == targetID {
			summary = &summaries[i]
			break
		}
	}

	readers, err := s.weaverCoreReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv metas: "+err.Error())
		return
	}
	index := buildWeaverMetaIndex(readers.metaKeys, readers.coreGet)
	metaKey := index.Targets[targetID]
	body := index.Bodies[targetID]
	lensName := ""
	lensRef := ""
	if summary != nil {
		lensRef = summary.LensRef
	} else if body != nil {
		lensRef = body.LensRef
	}
	if lensRef != "" {
		lensName = dataString(metaData(readers.coreGet, "vtx.meta."+lensRef+".canonicalName"), "value", "name", "canonicalName")
	}

	rowKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.WeaverTargetsBucket, targetID+".")
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list weaver-targets: "+err.Error())
		return
	}
	docs, order, truncated := s.readWeaverRows(ctx, conn, targetID, rowKeys)
	scan := scanWeaverRows(docs, order, truncated)

	stateKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.WeaverStateBucket, targetID+".")
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list weaver-state: "+err.Error())
		return
	}
	state := splitWeaverStateKeys(targetID, stateKeys)
	counts := s.readWeaverCounts(ctx, conn, state.Counts)

	healthKeys, readEntry, _, _, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	hbs := readWeaverHeartbeats(healthKeys, readEntry)

	// Guarded on metaKey: an unregistered id reaching this handler on rows or a
	// stale __control marker alone resolves no meta-vertex, and there is no
	// ".description" key to read.
	description := ""
	if metaKey != "" {
		description = dataString(metaData(readers.coreGet, metaKey+".description"), "text", "value")
	}

	detail := buildTargetDetail(targetID, body, metaKey, lensName, description, summary, scan, state, counts, index.Patterns, index.PatternSubject, hbs)
	if !detail.Registered && body == nil && detail.Rows == 0 && !state.Control {
		s.writeError(w, http.StatusNotFound,
			"target "+targetID+" not found (not registered, no meta-vertex, no rows, no control marker)")
		return
	}
	// After the 404, because the verdict is the most expensive thing this
	// handler reads — a second Core KV listing plus a manifest scan — and a
	// response that is never sent has no use for it.
	detail.EditContext = readWeaverEditContext(ctx, conn, metaKey, readers.coreGet)
	s.writeJSON(w, http.StatusOK, detail)
}

// readWeaverRows GETs one target's rows, capped. Keys are read in listing
// order; the cap is reported so the counts computed over them are never
// presented as totals when they are not.
func (s *server) readWeaverRows(ctx context.Context, conn *substrate.Conn, targetID string, keys []string) (map[string]map[string]any, []string, bool) {
	docs := map[string]map[string]any{}
	order := make([]string, 0, min(len(keys), weaverRowScanCap))
	truncated := false
	for _, k := range keys {
		entityID, ok := strings.CutPrefix(k, targetID+".")
		if !ok || entityID == "" || strings.Contains(entityID, ".") {
			continue
		}
		if len(order) >= weaverRowScanCap {
			truncated = true
			break
		}
		order = append(order, entityID)
		entry, err := conn.KVGet(ctx, bootstrap.WeaverTargetsBucket, k)
		if err != nil {
			docs[entityID] = nil
			continue
		}
		var doc map[string]any
		if json.Unmarshal(entry.Value, &doc) != nil {
			docs[entityID] = nil
			continue
		}
		docs[entityID] = doc
	}
	return docs, order, truncated
}

// readWeaverCounts GETs the __count values for one target. These exist only
// for (entity, gap) pairs with an un-closed dispatch history, so the set is
// bounded by live remediation, not by the candidate corpus.
func (s *server) readWeaverCounts(ctx context.Context, conn *substrate.Conn, keys map[string]map[string]string) map[string]map[string]int {
	out := map[string]map[string]int{}
	for entityID, gaps := range keys {
		for col, key := range gaps {
			entry, err := conn.KVGet(ctx, bootstrap.WeaverStateBucket, key)
			if err != nil {
				continue
			}
			n, ok := parseCountValue(entry.Value)
			if !ok {
				continue
			}
			putNestedInt(out, entityID, col, n)
		}
	}
	return out
}

// parseCountValue reads a __count body. The engine writes a bare integer;
// a JSON object carrying a count field is tolerated so a future shape does not
// silently read as zero dispatches.
func parseCountValue(raw []byte) (int, bool) {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		for _, k := range []string{"count", "dispatches", "n"} {
			if v, ok := numericField(obj[k]); ok {
				return v, true
			}
		}
	}
	return 0, false
}

func (s *server) weaverEntity(w http.ResponseWriter, r *http.Request, targetID, entityID string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if err := validateControlName(targetID); err != nil {
		s.writeError(w, http.StatusBadRequest, "targetId: "+err.Error())
		return
	}
	if err := validateControlName(entityID); err != nil {
		s.writeError(w, http.StatusBadRequest, "entityId: "+err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	summaries, _ := s.listWeaverTargets(ctx, conn)
	var summaryGaps []string
	for _, t := range summaries {
		if t.TargetID == targetID {
			summaryGaps = t.Gaps
			break
		}
	}

	readers, err := s.weaverCoreReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv metas: "+err.Error())
		return
	}
	index := buildWeaverMetaIndex(readers.metaKeys, readers.coreGet)
	body := index.Bodies[targetID]

	var row map[string]any
	if entry, err := conn.KVGet(ctx, bootstrap.WeaverTargetsBucket, targetID+"."+entityID); err == nil {
		var doc map[string]any
		if json.Unmarshal(entry.Value, &doc) == nil {
			row = doc
		}
	}

	stateKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.WeaverStateBucket, targetID+"."+entityID+".")
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list weaver-state: "+err.Error())
		return
	}
	state := splitWeaverStateKeys(targetID, stateKeys)
	marks := map[string]*weaverMark{}
	for col, key := range state.Marks[entityID] {
		entry, err := conn.KVGet(ctx, bootstrap.WeaverStateBucket, key)
		if err != nil {
			continue
		}
		var m weaverMark
		if json.Unmarshal(entry.Value, &m) != nil {
			continue
		}
		marks[col] = &m
	}
	counts := map[string]int{}
	for col, key := range state.Counts[entityID] {
		entry, err := conn.KVGet(ctx, bootstrap.WeaverStateBucket, key)
		if err != nil {
			continue
		}
		if n, ok := parseCountValue(entry.Value); ok {
			counts[col] = n
		}
	}

	healthKeys, readEntry, _, _, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	hbs := readWeaverHeartbeats(healthKeys, readEntry)

	detail := buildEntityDetail(targetID, entityID, row, body, summaryGaps, marks, counts, index.Patterns, hbs)
	if !detail.Found {
		s.writeError(w, http.StatusNotFound,
			"entity "+entityID+" has no row and no marks under target "+targetID)
		return
	}
	// Stamp each derived artifact with whether the engine's own live state
	// holds it — see weaverArtifact.Live for why this labels the link rather
	// than gating it.
	for i := range detail.Gaps {
		a := detail.Gaps[i].Artifact
		if a == nil {
			continue
		}
		a.Live = s.weaverArtifactLive(ctx, conn, a)
	}
	s.writeJSON(w, http.StatusOK, detail)
}

// weaverArtifactLive checks the derived id against the engine's live state. A
// task is a Core KV vertex; a Loom instance lives in loom-state under its own
// `instance.<id>` cursor record (Contract #10 §10.3) — an operational-bucket
// read under the same inspector charter the marks are read under.
func (s *server) weaverArtifactLive(ctx context.Context, conn *substrate.Conn, a *weaverArtifact) bool {
	switch a.Kind {
	case "task":
		_, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, "vtx.task."+a.ID)
		return err == nil
	case "flow":
		_, err := conn.KVGet(ctx, bootstrap.LoomStateBucket, "instance."+a.ID)
		return err == nil
	}
	return false
}
