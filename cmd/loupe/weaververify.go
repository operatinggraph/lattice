package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

// F25.2 — Verify: the target checker (weaver-target-studio-design.md §5).
// Read-only computation at view time; no stored verdicts, no new platform
// state. Three tiers, each labeled with its evidence class so a green never
// overclaims:
//
//   - V1 — structural (exact, static). Per-target (computeTargetChecks,
//     reached through the target map's own row/pattern data — the
//     gaps↔missing_<gap> column seam, pattern refs + subjectType, templated
//     bindings) plus lane-wide (computeLaneChecks, body only — no row scan):
//     what validateTarget/parseGoalColumns enforce at install (the reserved
//     expectedRevision param, a "none"-dispatch gap, the goalColumns
//     aspect-qualified/referenced-in-goal bridge).
//   - V2 — install-verdict surfacing (exact, existing). TargetRejected Health
//     issues, attributed to the rejecting meta vertex — its message names the
//     vertex id, never the targetId, so issuesNaming's whole-token match on
//     targetID cannot find these (laneRejectedIssues, rejectedIssuesNaming).
//     The planner surface itself (Pre/Effects, ranked candidates) is rendered
//     by weaver.go's renderAction/rankActionsByCostThenRef.
//   - V3 — interference (advisory, declared-effects-based). The static twin
//     of the runtime oscillation detector's join: every pair of installed
//     targets whose actions dispatch an op with overlapping declared
//     `.effects` aspect paths (buildOpEffectsIndex, computeInterference) —
//     computed from one full vtx.meta. corpus scan per render (schema-bounded,
//     not entity data), never from any live dispatch. The runtime detector
//     remains authoritative; this is prevention-best-effort, not a gate.
//
// GET /api/weaver/verify is the lane-wide summary (V1 lane checks + V2's
// rejected-issue roundup + V3's interference table). It never touches
// weaver-targets/weaver-state: those row/mark scans are per-target and stay
// on the target map's own Checks (computeTargetChecks, called from
// buildTargetDetail).

// weaverCheck is one Verify finding. Tier and Evidence are the honesty
// labels: Evidence "static" means the finding follows from the installed
// body alone (as certain as validateTarget's own rejection would be);
// "observed" means it depends on the scanned rows, which is real evidence
// but never proof of an unbounded candidate set; "advisory" is the weakest —
// V3's declared-effects join, or a single-sample subjectType check — real
// signal, but never a gate.
type weaverCheck struct {
	Tier     string `json:"tier"` // "v1" | "v2"
	Code     string `json:"code"`
	Severity string `json:"severity"` // "warn" | "bad"
	Message  string `json:"message"`
	Evidence string `json:"evidence"` // "static" | "observed" | "advisory"
}

// rankActionsByCostThenRef orders a gap's candidates/catalog for structural
// display (F25.2 V2 — "candidates ranked with their pres"): Cost ascending,
// Ref lexicographic on ties, the same canonical tie-break
// internal/weaver/planner.Action.Ref documents. Display only — the studio
// never dispatches, so this never picks a live winner.
func rankActionsByCostThenRef(actions []weaverAction) {
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].Cost != actions[j].Cost {
			return actions[i].Cost < actions[j].Cost
		}
		return actions[i].Ref < actions[j].Ref
	})
}

// ---------------------------------------------------------------------------
// V1 (per-target) + V2 (per-target attribution)
// ---------------------------------------------------------------------------

// computeTargetChecks aggregates F25.2's per-target findings from fields
// buildTargetDetail already computed (Observed, Unhandled, PatternKnown,
// Bindings) into one flat, named checklist — plus this target's
// TargetRejected attribution (V2). Exception-first, matching the rest of
// this console: an empty result is a clean pass, not silence about nothing
// having run.
func computeTargetChecks(d weaverTargetDetail, scan weaverRowScan, patternSubject map[string]string, hbs []weaverHeartbeat) []weaverCheck {
	var out []weaverCheck
	for _, g := range d.Gaps {
		if !g.Observed {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "ColumnNeverObserved", Severity: "warn", Evidence: "observed",
				Message: "gap " + g.Column + ": column never observed in the scanned rows — the lens may not project it, or has no candidate entities yet",
			})
		}
		for _, a := range gapActions(g) {
			out = append(out, checkAction(g.Column, a, patternSubject, scan.Samples)...)
		}
	}
	for _, col := range d.Unhandled {
		out = append(out, weaverCheck{
			Tier: "v1", Code: "GapWithoutPlaybook", Severity: "bad", Evidence: "observed",
			Message: "column " + col + " is live but the target's gaps map declares no playbook entry for it",
		})
	}
	if d.MetaKey != "" {
		bareID := strings.TrimPrefix(d.MetaKey, "vtx.meta.")
		for _, iss := range rejectedIssuesNaming(hbs, bareID) {
			out = append(out, weaverCheck{
				Tier: "v2", Code: "TargetRejected", Severity: "bad", Evidence: "exact",
				Message: iss.Message,
			})
		}
	}
	return out
}

// gapActions flattens a gap's explicit action, candidates and catalog into
// one slice — the three share the action-contract shape, and every check
// here applies identically to all three.
func gapActions(g weaverGap) []weaverAction {
	var out []weaverAction
	if g.Action != nil {
		out = append(out, *g.Action)
	}
	out = append(out, g.Candidates...)
	out = append(out, g.Actions...)
	return out
}

// checkAction runs V1's per-action checks: pattern resolution (+ a
// subjectType cross-check against a sampled row value, when both are
// available) and every templated binding's observed state.
func checkAction(gapColumn string, a weaverAction, patternSubject map[string]string, samples map[string]string) []weaverCheck {
	var out []weaverCheck
	if a.Pattern != "" {
		if !a.PatternKnown {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "PatternNotInstalled", Severity: "bad", Evidence: "observed",
				Message: "gap " + gapColumn + ": pattern \"" + a.Pattern + "\" has no installed meta.loomPattern",
			})
		} else if subjectType, ok := patternSubject[a.PatternRef]; ok {
			if col, isRow := strings.CutPrefix(a.Subject, "row."); isRow {
				if sample, ok := samples[col]; ok {
					if vt, ok := vertexTypeOf(sample); ok && vt != subjectType {
						out = append(out, weaverCheck{
							Tier: "v1", Code: "SubjectTypeMismatch", Severity: "warn", Evidence: "advisory",
							Message: "gap " + gapColumn + ": action's subject (row." + col + ") sampled a " + vt +
								" vertex but pattern \"" + a.Pattern + "\" requires subjectType " + subjectType +
								" — one sampled row, not proof",
						})
					}
				}
			}
		}
	}
	for _, b := range a.Bindings {
		if !b.Observed {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "UnboundBinding", Severity: "warn", Evidence: "observed",
				Message: "gap " + gapColumn + ": " + b.Param + " → row." + b.Column + " not observed in the scanned rows",
			})
		}
	}
	return out
}

// vertexTypeOf returns the Contract #1 vertex type of a full key
// (vtx.<type>.<id>…), or false for anything else (a meta vertex, a malformed
// key). Used only to compare a sampled subject value's type against a
// pattern's declared subjectType — advisory evidence, never a gate.
func vertexTypeOf(key string) (string, bool) {
	segs := strings.SplitN(key, ".", 4)
	if len(segs) < 3 || segs[0] != "vtx" || segs[1] == "" || segs[1] == "meta" {
		return "", false
	}
	return segs[1], true
}

// rejectedIssuesNaming selects the TargetRejected heartbeat issues whose
// message names bareID as a whole token — mirrors weaver.go's issuesNaming,
// specialized to the one code whose attribution needs the META VERTEX id
// rather than the targetId (rejectTarget's message names vtx.meta.<id>, and
// the targetId a rejected spec claimed is exactly the fact in question).
func rejectedIssuesNaming(hbs []weaverHeartbeat, bareID string) []weaverIssue {
	var out []weaverIssue
	for _, hb := range hbs {
		for _, iss := range hb.Issues {
			if iss.Code == "TargetRejected" && messageNamesToken(iss.Message, bareID) {
				out = append(out, iss)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// V1 (lane-wide, body-only — no row scan)
// ---------------------------------------------------------------------------

// weaverSingleTokenPattern mirrors internal/weaver's singleTokenPattern (a
// single NATS KV key segment) — the same rule validateTarget enforces on
// targetId and every gaps key.
var weaverSingleTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// computeLaneChecks runs F25.2's lane-wide V1 pass over one target's
// installed body alone — no weaver-targets/weaver-state read, so this is
// cheap enough to run for every target on one /api/weaver/verify render.
// Mirrors validateTarget/parseGoalColumns's install-time enforcement
// structurally: a finding here is something install would have rejected,
// not a live-state observation.
func computeLaneChecks(body *weaverTargetBody) []weaverCheck {
	var out []weaverCheck
	if body == nil {
		return out
	}
	if !weaverSingleTokenPattern.MatchString(body.TargetID) {
		out = append(out, weaverCheck{
			Tier: "v1", Code: "InvalidTargetID", Severity: "bad", Evidence: "static",
			Message: "targetId \"" + body.TargetID + "\" is not a valid single KV-key segment",
		})
	}
	cols := make([]string, 0, len(body.Gaps))
	for col := range body.Gaps {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	for _, col := range cols {
		g := body.Gaps[col]
		if _, reserved := g.Params["expectedRevision"]; reserved {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "ReservedParamCollision", Severity: "bad", Evidence: "static",
				Message: "gap " + col + ": param \"expectedRevision\" is reserved — the engine writes the OCC revision-condition under that payload field",
			})
		}
		if g.dispatchKind() == "none" {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "GapBindsNothing", Severity: "bad", Evidence: "static",
				Message: "gap " + col + " has no action, candidates, or goal bound — install validation rejects this target",
			})
		}
		if len(g.GoalColumns) > 0 {
			out = append(out, checkGoalColumns(col, g)...)
		}
	}
	return out
}

// checkGoalColumns mirrors parseGoalColumns's install-time rules
// structurally: goal must be present, every value must parse as a
// well-formed, aspect-qualified guard-grammar path, values must be unique,
// and every parsed path must actually be referenced somewhere in goal.
func checkGoalColumns(col string, g weaverGapAction) []weaverCheck {
	var out []weaverCheck
	if len(g.Goal) == 0 {
		return append(out, weaverCheck{
			Tier: "v1", Code: "GoalColumnsWithoutGoal", Severity: "bad", Evidence: "static",
			Message: "gap " + col + ": goalColumns is set but goal is empty — nothing references the declared aspect paths",
		})
	}
	goal, err := guardgrammar.Parse(g.Goal)
	if err != nil {
		return append(out, weaverCheck{
			Tier: "v1", Code: "GoalMalformed", Severity: "bad", Evidence: "static",
			Message: "gap " + col + ": goal does not parse as a guard-grammar document: " + err.Error(),
		})
	}
	goalPaths := guardAllPaths(goal)
	seen := map[guardgrammar.Path]string{}
	columns := make([]string, 0, len(g.GoalColumns))
	for c := range g.GoalColumns {
		columns = append(columns, c)
	}
	sort.Strings(columns)
	for _, column := range columns {
		raw := g.GoalColumns[column]
		p, err := guardgrammar.ParsePath(raw)
		if err != nil {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "GoalColumnMalformed", Severity: "bad", Evidence: "static",
				Message: "gap " + col + ": goalColumns[" + column + "] does not parse: " + err.Error(),
			})
			continue
		}
		if p.Aspect == "" {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "GoalColumnNotAspectQualified", Severity: "bad", Evidence: "static",
				Message: "gap " + col + ": goalColumns[" + column + "] is root-shaped — redundant, rowState already addresses it at subject.data." + p.Field,
			})
			continue
		}
		if other, dup := seen[p]; dup {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "GoalColumnAliased", Severity: "bad", Evidence: "static",
				Message: "gap " + col + ": goalColumns[" + column + "] and [" + other + "] map to the same path",
			})
			continue
		}
		seen[p] = column
		if !goalPaths[p] {
			out = append(out, weaverCheck{
				Tier: "v1", Code: "GoalColumnUnreferenced", Severity: "warn", Evidence: "static",
				Message: "gap " + col + ": goalColumns[" + column + "] is never referenced in goal",
			})
		}
	}
	return out
}

// guardAllPaths collects every subject-path a guard tree references —
// present/absent/equals leaves, recursing through allOf/anyOf/not — mirrors
// internal/weaver's install-time collectGuardPaths. Deliberately broader than
// effectLeafPaths (V3's join): a goal/precondition predicate can legitimately
// gate on a path via anyOf/not, where an effect cannot concretely assert one.
func guardAllPaths(g *guardgrammar.Guard) map[guardgrammar.Path]bool {
	out := map[guardgrammar.Path]bool{}
	collectAllPaths(g, out)
	return out
}

func collectAllPaths(g *guardgrammar.Guard, out map[guardgrammar.Path]bool) {
	if g == nil {
		return
	}
	switch g.Kind {
	case guardgrammar.KindPresent, guardgrammar.KindAbsent, guardgrammar.KindEquals:
		out[g.Path] = true
	case guardgrammar.KindAllOf, guardgrammar.KindAnyOf, guardgrammar.KindNot:
		for _, c := range g.Children {
			collectAllPaths(c, out)
		}
	}
}

// ---------------------------------------------------------------------------
// V2 (lane-wide) — the TargetRejected roundup
// ---------------------------------------------------------------------------

// weaverRejectedIssue is one TargetRejected issue, lane-wide, labeled with
// the meta vertex id its message names and — when that vertex currently owns
// a registered target — the targetId, so an operator can tell "still broken"
// from "fixed since, this vertex is now target X".
type weaverRejectedIssue struct {
	weaverIssue
	MetaID   string `json:"metaId"`
	TargetID string `json:"targetId,omitempty"`
}

// weaverRejectedIDPattern pulls the meta vertex id out of the engine's fixed
// TargetRejected message shape (registry.go rejectTarget: "meta.weaverTarget
// vtx.meta.<id> rejected: <reason>") — the only place this id is available,
// since the issueCache key that carries it structurally is never published.
var weaverRejectedIDPattern = regexp.MustCompile(`vtx\.meta\.([A-Za-z0-9_-]+) rejected:`)

func extractRejectedMetaID(msg string) string {
	m := weaverRejectedIDPattern.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	return m[1]
}

// laneRejectedIssues collects every TargetRejected issue across every live
// instance, deduplicated by (metaID, message, instance) — a standing issue is
// republished on every heartbeat sample, not once.
func laneRejectedIssues(hbs []weaverHeartbeat, index weaverMetaIndex) []weaverRejectedIssue {
	reverse := make(map[string]string, len(index.Targets))
	for tid, mk := range index.Targets {
		reverse[mk] = tid
	}
	seen := map[string]bool{}
	var out []weaverRejectedIssue
	for _, hb := range hbs {
		for _, iss := range hb.Issues {
			if iss.Code != "TargetRejected" {
				continue
			}
			id := extractRejectedMetaID(iss.Message)
			if id == "" {
				continue
			}
			key := id + "|" + iss.Message + "|" + iss.Instance
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, weaverRejectedIssue{
				weaverIssue: iss,
				MetaID:      id,
				TargetID:    reverse["vtx.meta."+id],
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MetaID != out[j].MetaID {
			return out[i].MetaID < out[j].MetaID
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// ---------------------------------------------------------------------------
// V3 — interference
// ---------------------------------------------------------------------------

// buildOpEffectsIndex indexes every op-meta vertex's declared `.effects`
// aspect, keyed by operationType — mirrors internal/weaver's
// indexOpMeta/indexOpEffects join (registry.go), Loupe-side: an op meta's
// operationType lives on its OWN root envelope (data.operationType, not a
// `.spec` aspect — unlike lens/pattern/target metas), and `.effects` is a
// separate CDC key that may arrive before or after it, so this joins them by
// id at read time over one full vtx.meta. scan rather than assuming an
// arrival order. An op with no declared effects (today, most of the corpus)
// is simply absent here — V3's caller reports that as "unanalyzable", never
// as "asserts nothing".
func buildOpEffectsIndex(metaKeys []string, get kvGetter) map[string][]guardgrammar.Path {
	opType := map[string]string{}
	for _, k := range metaKeys {
		if classifyKey(k) != classMeta {
			continue
		}
		d := metaData(get, k)
		if d == nil {
			continue
		}
		if ot, _ := d["operationType"].(string); ot != "" {
			opType[strings.TrimPrefix(k, "vtx.meta.")] = ot
		}
	}
	out := map[string][]guardgrammar.Path{}
	for _, k := range metaKeys {
		root, ok := strings.CutSuffix(k, ".effects")
		if !ok || classifyKey(k) != classAspect {
			continue
		}
		id := strings.TrimPrefix(root, "vtx.meta.")
		ot, known := opType[id]
		if !known {
			continue
		}
		raw, ok := get(k)
		if !ok {
			continue
		}
		body, err := unwrapAspectEnvelope(raw, "guards")
		if err != nil {
			continue
		}
		var probe struct {
			Guards []json.RawMessage `json:"guards"`
		}
		if json.Unmarshal(body, &probe) != nil {
			continue
		}
		var paths []guardgrammar.Path
		malformed := false
		for _, rg := range probe.Guards {
			g, err := guardgrammar.Parse(rg)
			if err != nil {
				malformed = true
				break
			}
			paths = append(paths, effectLeafPaths(g)...)
		}
		// A malformed effects body is pkgmgr's validateEffects rejecting this
		// shape at install time — defense-in-depth here, never a live path
		// (mirrors internal/weaver's indexOpEffects).
		if !malformed && len(paths) > 0 {
			out[ot] = paths
		}
	}
	return out
}

// unwrapAspectEnvelope mirrors internal/weaver's unwrapSpecBody: an aspect
// body is either the bare spec object (recognised by sentinelField) or a
// substrate envelope wrapping it under `data` (the Processor write path's
// shape).
func unwrapAspectEnvelope(body []byte, sentinelField string) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, err
	}
	if _, ok := probe[sentinelField]; ok {
		return body, nil
	}
	if data, ok := probe["data"]; ok {
		return data, nil
	}
	return body, nil
}

// effectLeafPaths mirrors internal/weaver's own effectLeafPaths (registry.go
// — the oscillation detector's join): the concrete paths a guard asserts —
// present/absent/equals leaves, recursing into allOf; anyOf/not name no
// definite written path and contribute nothing.
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

// formatGuardPath renders a guardgrammar.Path back into its §10.5
// subject-path string, for display only — mirrors internal/weaver's
// unexported formatPath.
func formatGuardPath(p guardgrammar.Path) string {
	if p.Aspect == "" {
		return "subject.data." + p.Field
	}
	return "subject." + p.Aspect + ".data." + p.Field
}

// targetReferencedOps is the set of operationTypes any of body's dispatched
// actions (explicit, candidate, or catalog) names via Operation — directOp's
// own operation, or assignTask's forOperation binding. triggerLoom/surface/
// proposedOp name no single operationType and contribute nothing; that
// narrowing (not "every op the pattern's steps might eventually submit") is
// exactly why V3 states plainly that it is advisory, not exhaustive.
func targetReferencedOps(body *weaverTargetBody) map[string]bool {
	out := map[string]bool{}
	if body == nil {
		return out
	}
	for _, g := range body.Gaps {
		collectOp(out, g.weaverActionContract)
		for _, c := range g.Candidates {
			collectOp(out, c)
		}
		for _, c := range g.Actions {
			collectOp(out, c)
		}
	}
	return out
}

func collectOp(out map[string]bool, a weaverActionContract) {
	if a.Operation != "" {
		out[a.Operation] = true
	}
}

// weaverInterference is one aspect path two or more installed targets' actions
// both assert — the exact signal Fire 7's runtime detector freezes a fighting
// pair on (oscillation.go), computed here over declarations, before any
// dispatch. The runtime detector remains authoritative; this is prevention
// best-effort, never a gate — Fork over doctrine (detect-and-recover).
type weaverInterference struct {
	Path    string   `json:"path"`
	Targets []string `json:"targets"`
	Ops     []string `json:"ops"`
}

// computeInterference is V3's pairwise join, over ALL installed targets in
// one pass (not the O(n²) framing the design narrates — grouping by asserted
// path and keeping every path two or more targets reach is the same result
// computed once per path instead of once per pair).
func computeInterference(bodies map[string]*weaverTargetBody, opPaths map[string][]guardgrammar.Path) []weaverInterference {
	type hit struct {
		targets map[string]bool
		ops     map[string]bool
	}
	hits := map[guardgrammar.Path]*hit{}

	targetIDs := make([]string, 0, len(bodies))
	for id := range bodies {
		targetIDs = append(targetIDs, id)
	}
	sort.Strings(targetIDs)

	for _, id := range targetIDs {
		ops := targetReferencedOps(bodies[id])
		opNames := make([]string, 0, len(ops))
		for op := range ops {
			opNames = append(opNames, op)
		}
		sort.Strings(opNames)
		for _, op := range opNames {
			for _, p := range opPaths[op] {
				h := hits[p]
				if h == nil {
					h = &hit{targets: map[string]bool{}, ops: map[string]bool{}}
					hits[p] = h
				}
				h.targets[id] = true
				h.ops[op] = true
			}
		}
	}

	var out []weaverInterference
	for p, h := range hits {
		if len(h.targets) < 2 {
			continue
		}
		out = append(out, weaverInterference{
			Path:    formatGuardPath(p),
			Targets: sortedKeys(h.targets),
			Ops:     sortedKeys(h.ops),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// weaverOpCoverage states V3's honesty framing plainly: today, only a
// fraction of the ops any target dispatches declare `.effects` at all, so
// interference can only ever be found among the declared subset. Listing
// what is unanalyzable is the adoption nudge, not a suppressed warning.
type weaverOpCoverage struct {
	ReferencedOps   int      `json:"referencedOps"`
	DeclaredOps     int      `json:"declaredEffectsOps"`
	UnanalyzableOps []string `json:"unanalyzableOps"`
}

func computeOpCoverage(bodies map[string]*weaverTargetBody, opPaths map[string][]guardgrammar.Path) weaverOpCoverage {
	referenced := map[string]bool{}
	for _, b := range bodies {
		for op := range targetReferencedOps(b) {
			referenced[op] = true
		}
	}
	declared := 0
	var unanalyzable []string
	for op := range referenced {
		if _, ok := opPaths[op]; ok {
			declared++
		} else {
			unanalyzable = append(unanalyzable, op)
		}
	}
	sort.Strings(unanalyzable)
	return weaverOpCoverage{
		ReferencedOps:   len(referenced),
		DeclaredOps:     declared,
		UnanalyzableOps: unanalyzable,
	}
}

// ---------------------------------------------------------------------------
// GET /api/weaver/verify
// ---------------------------------------------------------------------------

// weaverVerifyTarget is one lane-wide roster row on #/weaver/verify.
type weaverVerifyTarget struct {
	TargetID   string        `json:"targetId"`
	MetaKey    string        `json:"metaKey,omitempty"`
	Registered bool          `json:"registered"`
	Checks     []weaverCheck `json:"checks"`
}

// weaverVerify is the F25.2 lane-wide summary: V1's structural pass over
// every installed target's body, V2's TargetRejected roundup, and V3's
// interference table + op-effects coverage — one full vtx.meta. corpus scan
// plus a heartbeat read, never a weaver-targets/weaver-state scan (those stay
// on the per-target Checks, computeTargetChecks via buildTargetDetail).
func (s *server) weaverVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
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

	readers, err := s.weaverCoreReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv metas: "+err.Error())
		return
	}
	index := buildWeaverMetaIndex(readers.metaKeys, readers.coreGet)
	opPaths := buildOpEffectsIndex(readers.metaKeys, readers.coreGet)

	healthKeys, readEntry, _, _, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	hbs := readWeaverHeartbeats(healthKeys, readEntry)

	targetIDs := make([]string, 0, len(index.Bodies))
	for id := range index.Bodies {
		targetIDs = append(targetIDs, id)
	}
	sort.Strings(targetIDs)

	targets := make([]weaverVerifyTarget, 0, len(targetIDs))
	for _, id := range targetIDs {
		targets = append(targets, weaverVerifyTarget{
			TargetID:   id,
			MetaKey:    index.Targets[id],
			Registered: registered[id],
			Checks:     computeLaneChecks(index.Bodies[id]),
		})
	}

	rejected := laneRejectedIssues(hbs, index)
	if rejected == nil {
		rejected = []weaverRejectedIssue{}
	}
	interference := computeInterference(index.Bodies, opPaths)
	if interference == nil {
		interference = []weaverInterference{}
	}

	out := map[string]any{
		"targets":        targets,
		"interference":   interference,
		"opCoverage":     computeOpCoverage(index.Bodies, opPaths),
		"rejectedIssues": rejected,
		"instances":      len(hbs),
	}
	if listErr != nil {
		out["listError"] = listErr.Error()
	}
	s.writeJSON(w, http.StatusOK, out)
}
