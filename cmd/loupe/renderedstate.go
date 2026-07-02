package main

import (
	"fmt"
	"sort"
	"strings"
)

// The shared lens status vocabulary (loupe-2-ux-design.md §4.2): one
// server-derived renderedState per lens, joined from its Health-KV reporter
// doc and its vtx.meta.<id>.spec, rendered identically by the map, the
// refractor roster, and the health rollups.
const (
	lensStateFault      = "fault"            // errors reported, or a structural pause
	lensStatePaused     = "paused"           // paused (operator "manual", or infra/unattributed)
	lensStatePendingRP  = "pending-readpath" // protected pg lens fail-closed before out-of-band verify
	lensStateRebuilding = "rebuilding"
	lensStateLagging    = "lagging"
	lensStateProjecting = "projecting"
	lensStateUnknown    = "unknown"
)

// lensRenderedState derives a lens's renderedState, its issue lines, and its
// rollup severity. Precedence follows the §4.2 table top→bottom. Two edges the
// table's wording leaves open, decided here:
//
//   - fault requires errorCount > 0 AND a live lastError — the reporter's
//     errorCount is cumulative (never reset) while SetActive nulls lastError,
//     so the conjunct is what lets a recovered lens stop rendering fault. A
//     historical error count on a healthy lens surfaces as an issue line only.
//   - a paused NON-protected lens whose pauseReason is neither "manual" nor
//     "structural" (an "infra" pause, a malformed reason) renders "paused" —
//     not "unknown" — since the pause itself is certain even when its
//     attribution is not.
//
// pending-readpath is a paused protected/grant-table Postgres lens — the
// fail-closed verify-and-pause activation gate. The Refractor re-probes it on
// the infra-pause loop and auto-resumes once the operator provisions the
// table, so the spec join alone decides it (the §4.2 rule): any
// unverifiable read path on such a lens — never-provisioned or a later
// Postgres outage — parks here, with lastError carrying the probe's detail
// and real write errors escalating to fault. Expected, potentially
// long-lived, not degradation — so it contributes NOTHING to the rollup
// (sevGreen) and is surfaced as its own grouping instead of a degraded count.
// A fault contributes yellow overall (red-worthy detail, matching the
// pre-existing worst-of behavior).
func lensRenderedState(doc map[string]any, spec lensSpecInfo) (state string, issues []string, level int) {
	status, _ := doc["status"].(string)
	pauseReason, _ := doc["pauseReason"].(string)
	consumerLag, _ := doc["consumerLag"].(float64)
	errorCount, _ := doc["errorCount"].(float64)
	lastError, _ := doc["lastError"].(string)

	if consumerLag > 0 {
		issues = append(issues, fmt.Sprintf("consumerLag=%.0f", consumerLag))
	}
	if errorCount > 0 {
		issues = append(issues, fmt.Sprintf("errorCount=%.0f", errorCount))
	}
	if lastError != "" {
		issues = append(issues, "lastError: "+lastError)
	}

	pendingReadPath := spec.TargetType == "postgres" && (spec.Protected || spec.GrantTable)

	switch {
	case (errorCount > 0 && lastError != "") || pauseReason == "structural":
		return lensStateFault, issues, sevYellow
	case status == "paused" && pauseReason == "manual":
		return lensStatePaused, issues, sevYellow
	case status == "paused" && pendingReadPath:
		return lensStatePendingRP, issues, sevGreen
	case status == "paused":
		return lensStatePaused, issues, sevYellow
	case status == "rebuilding":
		return lensStateRebuilding, issues, sevYellow
	case status == "active" && consumerLag > 0:
		return lensStateLagging, issues, sevYellow
	case status == "active":
		return lensStateProjecting, issues, sevGreen
	default:
		return lensStateUnknown, issues, sevYellow
	}
}

// mapGate is one phase-gate chip on the map rail's gates panel. A declared
// gate with no Health-KV marker renders Present:false (dim "—") — absence is
// informational, not degraded: the markers are written by the proof-gate test
// suites, not by deploys.
type mapGate struct {
	Gate      string `json:"gate"`
	Present   bool   `json:"present"`
	Passed    bool   `json:"passed"`
	Timestamp string `json:"timestamp,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

// declaredGates is the phase-1 proof-gate set whose chips always render:
// bypass (gate2), capability-adversarial (gate3), rollback (gate4), and
// health-completeness (gate5). Gate 1 is the bootstrap key, covered by the
// alert strip.
var declaredGates = []string{"gate2", "gate3", "gate4", "gate5"}

// computeGates joins health.gates.* markers onto the declared gate set, in
// declared order; undeclared markers found in the bucket append after,
// sorted, so a future gate is visible without a Loupe change. The gate name
// is the key's last segment (health.gates.phase1.gate2 → gate2); keys are
// processed in sorted order and the first marker per name wins, so a name
// collision (a future phase2.gate2) renders deterministically. The suites
// stamp the completion time as "timestamp" or "completedAt" depending on the
// gate — both are read.
func computeGates(keys []string, readEntry func(string) (map[string]any, bool)) []mapGate {
	found := make(map[string]mapGate)
	declared := make(map[string]bool, len(declaredGates))
	for _, name := range declaredGates {
		declared[name] = true
	}
	gateKeys := make([]string, 0)
	for _, k := range keys {
		if _, kind := classifyHealthKey(k); kind == kindGate {
			gateKeys = append(gateKeys, k)
		}
	}
	sort.Strings(gateKeys)
	extras := make([]string, 0)
	for _, k := range gateKeys {
		segs := strings.Split(k, ".")
		name := segs[len(segs)-1]
		if name == "" {
			continue
		}
		if _, seen := found[name]; seen {
			continue
		}
		doc, ok := readEntry(k)
		if !ok {
			continue
		}
		g := mapGate{Gate: name, Present: true}
		g.Passed, _ = doc["passed"].(bool)
		g.Timestamp, _ = doc["timestamp"].(string)
		if g.Timestamp == "" {
			g.Timestamp, _ = doc["completedAt"].(string)
		}
		g.Commit, _ = doc["commit"].(string)
		if !declared[name] {
			extras = append(extras, name)
		}
		found[name] = g
	}
	sort.Strings(extras)

	out := make([]mapGate, 0, len(declaredGates)+len(extras))
	for _, name := range declaredGates {
		if g, ok := found[name]; ok {
			out = append(out, g)
		} else {
			out = append(out, mapGate{Gate: name})
		}
	}
	for _, name := range extras {
		out = append(out, found[name])
	}
	return out
}
