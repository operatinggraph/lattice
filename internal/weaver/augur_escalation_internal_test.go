package weaver

import "testing"

// registerAugurTarget loads a meta.weaverTarget (no gaps[unplannableCol] entry)
// carrying the supplied augur block, returning the source, the resolved targetId,
// and the owning meta vertex key the escalation must thread.
func registerAugurTarget(t *testing.T, targetID string, augur map[string]any) (*targetSource, string) {
	t.Helper()
	s := newTestSource(t)
	id := testNanoID(t)
	spec := targetSpecFixture(targetID)
	if augur != nil {
		spec["augur"] = augur
	}
	s.handle(vertexEvent(t, id, weaverTargetClass))
	s.handle(specEvent(t, id, spec))
	if _, ok := s.target(targetID); !ok {
		t.Fatalf("target %q not registered", targetID)
	}
	return s, "vtx.meta." + id
}

// TestAugurEscalation_BuildsDirectOp is the step-3b″ core: an unplannable gap on
// an augur-enabled target yields a directOp GapAction carrying the TRUSTED gap
// context (the full meta + candidate vertex keys, the gapColumn + trigger
// literals) and the no-orphan reads — dispatchable straight to the bridge with no
// Loom wrapper (Option F). Defaults fill op/adapter/replyOp when the block omits
// them.
func TestAugurEscalation_BuildsDirectOp(t *testing.T) {
	t.Parallel()
	const targetID = "leaseApproval"
	const entityID = "AAapplicantHJKMNPQR"
	const entityKey = "vtx.leaseapp.AAapplicantHJKMNPQR"
	const gapColumn = "missing_approval"

	s, metaKey := registerAugurTarget(t, targetID, map[string]any{
		"escalate": []any{"unplannable"},
	})

	ga, ok := augurEscalation(s, mustTarget(t, s, targetID), escalateUnplannable, targetID, entityID, entityKey, gapColumn)
	if !ok {
		t.Fatalf("augurEscalation: expected escalation for an unplannable-escalating target")
	}
	if ga.Action != actionDirectOp {
		t.Fatalf("action = %q want %q", ga.Action, actionDirectOp)
	}
	if ga.Operation != defaultAugurOp {
		t.Fatalf("operation = %q want default %q", ga.Operation, defaultAugurOp)
	}
	if ga.Target != metaKey {
		t.Fatalf("authTarget = %q want the target meta key %q", ga.Target, metaKey)
	}
	want := map[string]string{
		"instanceKey": deriveAugurHandle(targetID, entityID, gapColumn),
		"adapter":     defaultAugurAdapter,
		"replyOp":     defaultAugurReplyOp,
		"targetId":    metaKey,
		"entityId":    entityKey,
		"gapColumn":   gapColumn,
		"trigger":     escalateUnplannable,
	}
	for k, v := range want {
		if ga.Params[k] != v {
			t.Fatalf("param %q = %q want %q", k, ga.Params[k], v)
		}
	}
	// The instanceKey must be a bare handle (no dots) so "vtx.augurproposal." +
	// handle is a single well-formed key (required_bare_handle in the op script).
	for _, bad := range []string{".", " ", "*", ">"} {
		if containsRune(ga.Params["instanceKey"], bad) {
			t.Fatalf("instanceKey %q must carry no key delimiters", ga.Params["instanceKey"])
		}
	}
	if len(ga.Reads) != 2 || ga.Reads[0] != entityKey || ga.Reads[1] != metaKey {
		t.Fatalf("reads = %v want [%s %s] (the no-orphan alive endpoints)", ga.Reads, entityKey, metaKey)
	}

	// The synthesized GapAction must plan as a directOp through the normal path
	// (buildPlan(actionDirectOp) → fireEpisode), with expectedRevision injected.
	pl, perr := buildPlan(s, fixtureActorKey, targetID, entityID, gapColumn, ga, map[string]any{"entityKey": entityKey}, 42)
	if perr != nil {
		t.Fatalf("buildPlan over the escalation GapAction: %v", perr)
	}
	if pl.operationType != defaultAugurOp {
		t.Fatalf("planned op = %q want %q", pl.operationType, defaultAugurOp)
	}
	payload := pl.payload("ignored-claimid")
	if payload["targetId"] != metaKey || payload["entityId"] != entityKey ||
		payload["gapColumn"] != gapColumn || payload["trigger"] != escalateUnplannable {
		t.Fatalf("payload dropped trusted gap context: %+v", payload)
	}
	if payload["expectedRevision"] != uint64(42) {
		t.Fatalf("payload expectedRevision = %v want 42 (the OCC row revision)", payload["expectedRevision"])
	}
	if len(pl.reads) != 2 || pl.reads[0] != entityKey || pl.reads[1] != metaKey {
		t.Fatalf("plan reads = %v want the hydrated no-orphan endpoints", pl.reads)
	}
}

// TestAugurEscalation_HonoursOverrides proves an explicit op/adapter/replyOp
// override is threaded verbatim (a package may name a bespoke reasoning op).
func TestAugurEscalation_HonoursOverrides(t *testing.T) {
	t.Parallel()
	const targetID = "custom"
	s, _ := registerAugurTarget(t, targetID, map[string]any{
		"escalate": []any{"unplannable"},
		"op":       "CustomReasoningOp",
		"adapter":  "augurpro",
		"replyOp":  "CustomReply",
	})
	ga, ok := augurEscalation(s, mustTarget(t, s, targetID), escalateUnplannable, targetID, "e", "vtx.t.e", "missing_x")
	if !ok {
		t.Fatalf("expected escalation")
	}
	if ga.Operation != "CustomReasoningOp" || ga.Params["adapter"] != "augurpro" || ga.Params["replyOp"] != "CustomReply" {
		t.Fatalf("overrides not threaded: op=%q adapter=%q replyOp=%q", ga.Operation, ga.Params["adapter"], ga.Params["replyOp"])
	}
}

// TestAugurEscalation_FailsClosed: a target with NO augur block, or one that does
// not escalate this trigger, returns ok=false — the caller then raises
// GapWithoutPlaybook (the frozen-contract dead-end), never silently escalating.
func TestAugurEscalation_FailsClosed(t *testing.T) {
	t.Parallel()

	sNone, _ := registerAugurTarget(t, "noAugur", nil)
	if _, ok := augurEscalation(sNone, mustTarget(t, sNone, "noAugur"), escalateUnplannable, "noAugur", "e", "vtx.t.e", "missing_x"); ok {
		t.Fatalf("a target with no augur block must not escalate")
	}

	sOther, _ := registerAugurTarget(t, "onlyExhausted", map[string]any{"escalate": []any{"exhausted"}})
	if _, ok := augurEscalation(sOther, mustTarget(t, sOther, "onlyExhausted"), escalateUnplannable, "onlyExhausted", "e", "vtx.t.e", "missing_x"); ok {
		t.Fatalf("a target escalating only `exhausted` must not escalate an `unplannable` trigger")
	}
}

// TestAugurEscalation_ExhaustedTriggerSymmetric proves `exhausted` escalates
// exactly like `unplannable` when a target's augur block opts it in (Fire 9,
// the weaver-exhausted-escalation-and-model backlog item) — and, symmetrically
// with TestAugurEscalation_FailsClosed's "onlyExhausted" case, that a target
// escalating only `unplannable` does NOT escalate an `exhausted` trigger.
func TestAugurEscalation_ExhaustedTriggerSymmetric(t *testing.T) {
	t.Parallel()
	s, metaKey := registerAugurTarget(t, "budgetAware", map[string]any{
		"escalate": []any{"exhausted"},
	})
	ga, ok := augurEscalation(s, mustTarget(t, s, "budgetAware"), escalateExhausted, "budgetAware", "e", "vtx.t.e", "missing_x")
	if !ok {
		t.Fatalf("a target escalating \"exhausted\" must escalate an exhausted trigger")
	}
	if ga.Action != actionDirectOp || ga.Operation != defaultAugurOp {
		t.Fatalf("exhausted escalation must build the same directOp shape as unplannable: action=%q operation=%q", ga.Action, ga.Operation)
	}
	if ga.Params["trigger"] != escalateExhausted {
		t.Fatalf("trigger param = %q want %q", ga.Params["trigger"], escalateExhausted)
	}
	if ga.Target != metaKey {
		t.Fatalf("authTarget = %q want %q", ga.Target, metaKey)
	}

	sOnlyUnplannable, _ := registerAugurTarget(t, "onlyUnplannable", map[string]any{"escalate": []any{"unplannable"}})
	if _, ok := augurEscalation(sOnlyUnplannable, mustTarget(t, sOnlyUnplannable, "onlyUnplannable"), escalateExhausted, "onlyUnplannable", "e", "vtx.t.e", "missing_x"); ok {
		t.Fatalf("a target escalating only \"unplannable\" must not escalate an \"exhausted\" trigger")
	}
}

// TestAugurEscalation_ThreadsModel proves the target's optional augur.model
// override (Contract #10 §10.8) reaches the escalation GapAction's Params —
// closing the "model is consumed by nothing" half of the
// weaver-exhausted-escalation-and-model finding. Present verbatim when set;
// genuinely ABSENT (not an empty string) when unset, so a real adapter's own
// "omit means default" posture (mirroring Op/Adapter/ReplyOp) is preserved.
func TestAugurEscalation_ThreadsModel(t *testing.T) {
	t.Parallel()
	sSet, _ := registerAugurTarget(t, "modelSet", map[string]any{
		"escalate": []any{"unplannable"},
		"model":    "claude-sonnet-4-6",
	})
	ga, ok := augurEscalation(sSet, mustTarget(t, sSet, "modelSet"), escalateUnplannable, "modelSet", "e", "vtx.t.e", "missing_x")
	if !ok {
		t.Fatalf("expected escalation")
	}
	if ga.Params["model"] != "claude-sonnet-4-6" {
		t.Fatalf("model param = %q want the augur.model override threaded verbatim", ga.Params["model"])
	}

	sUnset, _ := registerAugurTarget(t, "modelUnset", map[string]any{
		"escalate": []any{"unplannable"},
	})
	ga2, ok := augurEscalation(sUnset, mustTarget(t, sUnset, "modelUnset"), escalateUnplannable, "modelUnset", "e", "vtx.t.e", "missing_x")
	if !ok {
		t.Fatalf("expected escalation")
	}
	if _, present := ga2.Params["model"]; present {
		t.Fatalf("model param must be ABSENT when the target sets no override (so the adapter's own default applies), got %q", ga2.Params["model"])
	}
}

// TestDeriveAugurHandle_StableAndDistinct: the handle is deterministic in
// (targetId, entityId, gapColumn) — so a redelivery / reclaim collapses on the
// same claim — and distinct across each coordinate.
func TestDeriveAugurHandle_StableAndDistinct(t *testing.T) {
	t.Parallel()
	base := deriveAugurHandle("t", "e", "g")
	if base != deriveAugurHandle("t", "e", "g") {
		t.Fatalf("handle not deterministic")
	}
	for _, other := range []string{
		deriveAugurHandle("t2", "e", "g"),
		deriveAugurHandle("t", "e2", "g"),
		deriveAugurHandle("t", "e", "g2"),
	} {
		if other == base {
			t.Fatalf("handle collided across distinct coordinates")
		}
	}
}

func mustTarget(t *testing.T, s *targetSource, targetID string) *Target {
	t.Helper()
	tgt, ok := s.target(targetID)
	if !ok {
		t.Fatalf("target %q not registered", targetID)
	}
	return tgt
}

func containsRune(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
