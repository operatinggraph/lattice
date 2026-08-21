package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The real capabilityAuthor adapter's proof. Every test drives the adapter
// against a REAL result bucket on embedded NATS — the ref chain is the protocol,
// so a fixture that memoised attempt state in Go would prove nothing about it —
// with a scripted dispatcher standing in for the model-runner fleet and a
// scripted validator standing in for pkgmgr's deterministic boundary.
//
// The two invariants every case is written around: at most TWO vendor calls per
// authoring request, and the recorded verdict is the validator's, never the
// model's.

const (
	testCatalogBucket = "capability-author-context"
	testHandle        = "AuthClaimHandle12345"

	// Valid 20-char NanoIDs (Contract #1 alphabet: no I, l, O, 0) for the seeded
	// meta vertices. The lens id is load-bearing: the adapter resolves the
	// model's canonicalName choice to exactly this, and files it as the target's
	// lensRef — so a test can assert the resolved value.
	staleLensNanoID     = "LensAAAAAAAAAAAAAAAA"
	existingTargetID    = "TargetBBBBBBBBBBBBBB"
	nudgePatternID      = "PatternCCCCCCCCCCCCC"
	sendReminderMetaID  = "SendReminderDDDDDDDD"
	bareEventMetaID     = "BareEventEEEEEEEEEEE"
	poisonRowNanoID     = "PoisonFFFFFFFFFFFFFF"
	staleLensCanonical  = "staleOnboarding"
	sendReminderCommand = "SendReminder"
)

// --- harness ----------------------------------------------------------------

// fakeRunner is the model-runner stand-in. It records every dispatched request
// (the spend ledger the budget assertions read) and answers with a scripted ack.
// It writes nothing: the tests place result rows themselves, so "what the runner
// has recorded so far" is always explicit at the call site.
type fakeRunner struct {
	mu   sync.Mutex
	reqs []wire.Request
	// ack answers the n-th dispatch (0-based). Nil accepts everything.
	ack func(n int, req wire.Request) (wire.Ack, error)
}

func (f *fakeRunner) Dispatch(_ context.Context, req wire.Request) (wire.Ack, error) {
	f.mu.Lock()
	n := len(f.reqs)
	f.reqs = append(f.reqs, req)
	ack := f.ack
	f.mu.Unlock()
	if ack == nil {
		return wire.Ack{Status: wire.AckAccepted, Ref: req.Ref}, nil
	}
	return ack(n, req)
}

func (f *fakeRunner) calls() []wire.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wire.Request(nil), f.reqs...)
}

// fakeValidator is the deterministic-boundary stand-in. It records the exact
// bytes it judged, so a test can prove the verdict was computed over what is
// recorded rather than over some other rendering of the answer.
type fakeValidator struct {
	mu      sync.Mutex
	seen    [][]byte
	verdict func(n int, kind string, content []byte) (string, string)
}

func (v *fakeValidator) validate(kind string, content []byte) (string, string) {
	v.mu.Lock()
	n := len(v.seen)
	v.seen = append(v.seen, append([]byte(nil), content...))
	verdict := v.verdict
	v.mu.Unlock()
	if verdict == nil {
		return ValidationStateValid, ""
	}
	return verdict(n, kind, content)
}

func (v *fakeValidator) judged() [][]byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([][]byte(nil), v.seen...)
}

// authorFixture is one adapter wired to a live result bucket and a seeded
// catalog.
type authorFixture struct {
	adapter   *CapabilityAuthor
	runner    *fakeRunner
	validator *fakeValidator
	conn      *substrate.Conn
}

func newAuthorFixture(t *testing.T) *authorFixture {
	t.Helper()
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap conn: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, bucket := range []string{testCatalogBucket, wire.ResultsBucket} {
		if _, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatalf("provision %s: %v", bucket, err)
		}
	}
	seedCatalog(t, conn)

	runner := &fakeRunner{}
	validator := &fakeValidator{}
	adapter, err := NewCapabilityAuthor(runner, conn, testCatalogBucket, validator.validate,
		WithCapabilityAuthorClock(func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) }))
	if err != nil {
		t.Fatalf("NewCapabilityAuthor: %v", err)
	}
	return &authorFixture{adapter: adapter, runner: runner, validator: validator, conn: conn}
}

// seedCatalog writes the shapes the adapter filters on: a lens and a weaver
// target (spec-bearing artifacts), an operation self-description, and two rows
// that must be dropped — a DDL meta with neither a spec nor commands, and a
// poison row. Every meta key is a valid NanoID so the lens resolves to a
// filable lensRef. The lens spec carries a Postgres targetConfig with a DSN and
// RLS posture, so the sanitisation vector (nothing sensitive reaches the vendor)
// has a real payload to strip.
func seedCatalog(t *testing.T, conn *substrate.Conn) {
	t.Helper()
	for _, id := range []string{staleLensNanoID, existingTargetID, nudgePatternID, sendReminderMetaID, bareEventMetaID} {
		if !substrate.IsValidNanoID(id) {
			t.Fatalf("seed NanoID %q is not a valid NanoID (fix the test constant)", id)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows := map[string]string{
		"vtx.meta." + staleLensNanoID: `{"key":"vtx.meta.` + staleLensNanoID + `","class":"meta.lens","canonicalName":"` + staleLensCanonical + `",` +
			`"description":"onboarding rows that have gone cold",` +
			`"spec":{"canonicalName":"` + staleLensCanonical + `","targetType":"postgres","cypherRule":"MATCH (i:identity) RETURN i.key AS key, true AS missing_reminder",` +
			`"targetConfig":{"dsn":"postgres://user:secret@db.internal/app","table":"onboarding","columns":[{"name":"key","type":"text"}],` +
			`"protected":true,"grantTable":true,"grantSource":"onboarding_grants","secureColumns":[{"column":"ssn","holderTypes":["clinician"]}]}}}`,
		"vtx.meta." + existingTargetID: `{"key":"vtx.meta.` + existingTargetID + `","class":"meta.weaverTarget","canonicalName":"existingTarget",` +
			`"description":"an installed target","spec":{"targetId":"existingTarget","lensRef":"` + staleLensCanonical + `"}}`,
		"vtx.meta." + nudgePatternID: `{"key":"vtx.meta.` + nudgePatternID + `","class":"meta.loomPattern","canonicalName":"nudgePattern",` +
			`"spec":{"patternId":"nudgePattern","subjectType":"identity"}}`,
		"vtx.meta." + sendReminderMetaID: `{"key":"vtx.meta.` + sendReminderMetaID + `","class":"meta.ddl.vertexType","canonicalName":"` + sendReminderCommand + `",` +
			`"description":"sends a reminder","permittedCommands":["` + sendReminderCommand + `"],"inputSchema":{"type":"object"}}`,
		"vtx.meta." + bareEventMetaID: `{"key":"vtx.meta.` + bareEventMetaID + `","class":"meta.ddl.eventType","canonicalName":"SomeEvent","spec":null}`,
		"vtx.meta." + poisonRowNanoID: `not json at all`,
	}
	for key, body := range rows {
		if _, err := conn.KVPut(ctx, testCatalogBucket, key, []byte(body)); err != nil {
			t.Fatalf("seed catalog row %s: %v", key, err)
		}
	}
}

// writeResult places one attempt's row exactly as the runner would.
func (f *authorFixture) writeResult(t *testing.T, ref string, res wire.Result) {
	t.Helper()
	res.Ref = ref
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.conn.KVPut(ctx, wire.ResultsBucket, ref, body); err != nil {
		t.Fatalf("write result %s: %v", ref, err)
	}
}

// goodDraft is a well-formed weaver-target answer.
func goodDraft() modelArtifact {
	return modelArtifact{
		Kind: CapabilityAuthorKind,
		Content: modelTargetContent{
			TargetID:    "coldOnboardingReminder",
			LensRef:     "staleOnboarding",
			Description: "Every cold onboarding gets one reminder.",
			Gaps: []modelGapAction{{
				GapColumn: "missing_reminder",
				Action:    "directOp",
				Operation: "SendReminder",
				Params:    []modelParam{{Key: "identity", Value: "row.key"}, {Key: "channel", Value: "email"}},
				Reads:     []string{"row.key"},
			}},
		},
		Rationale:  "staleOnboarding already marks cold rows; a directOp closes the gap.",
		Confidence: 0.82,
	}
}

// dottedDraft is goodDraft with a targetId the token rules reject — the shape a
// correction pass exists to fix.
func dottedDraft() modelArtifact {
	art := goodDraft()
	art.Content.TargetID = "cold.onboarding.reminder"
	return art
}

// rejectDottedTargets is a validator that judges the CONTENT, like the real one:
// the same artifact always gets the same verdict, so re-polling a draft can
// never flip it.
func rejectDottedTargets(_ int, _ string, content []byte) (string, string) {
	if bytes.Contains(content, []byte("cold.onboarding.reminder")) {
		return ValidationStateInvalid, `targetId "cold.onboarding.reminder" is not a valid single KV-key segment`
	}
	return ValidationStateValid, ""
}

// completed renders a model answer as the runner's completed result row.
func completed(t *testing.T, art modelArtifact, model string) wire.Result {
	t.Helper()
	out, err := json.Marshal(art)
	if err != nil {
		t.Fatalf("marshal model artifact: %v", err)
	}
	return wire.Result{State: wire.StateCompleted, Output: out, Model: model}
}

// decodeProposal unwraps the adapter's opaque Detail.
func decodeProposal(t *testing.T, d Dispatch) CapabilityAuthorProposal {
	t.Helper()
	if d.Disposition != Resolved {
		t.Fatalf("disposition = %v, want Resolved", d.Disposition)
	}
	if d.Result.Status != OutcomeCompleted {
		t.Fatalf("status = %q, want %q (detail: %s)", d.Result.Status, OutcomeCompleted, d.Result.Detail)
	}
	var p CapabilityAuthorProposal
	if err := json.Unmarshal([]byte(d.Result.Detail), &p); err != nil {
		t.Fatalf("decode proposal detail %q: %v", d.Result.Detail, err)
	}
	return p
}

func authoringRequest(intent string) Request {
	return Request{
		IdempotencyKey: testHandle,
		Operation:      "RecordCapabilityProposal",
		Subject:        testHandle,
		Params:         map[string]string{"requesterId": "vtx.identity.Op1aaaaaaaaaaaaaaaa", "intent": intent},
	}
}

// --- the happy path ---------------------------------------------------------

func TestCapabilityAuthor_HappyPath(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	d, err := f.adapter.Execute(ctx, authoringRequest("remind identities whose onboarding has gone cold"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if d.Disposition != Pending || d.Ref != testHandle {
		t.Fatalf("Execute = %+v, want Pending on ref %q", d, testHandle)
	}

	calls := f.runner.calls()
	if len(calls) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(calls))
	}
	req := calls[0]
	if req.Ref != testHandle {
		t.Errorf("dispatch ref = %q, want %q", req.Ref, testHandle)
	}
	if req.MaxTokens != 0 {
		t.Errorf("maxTokens = %d, want 0 (the runner's own ceiling governs)", req.MaxTokens)
	}
	if req.Tool.Name != capabilityAuthorToolName {
		t.Errorf("tool = %q, want %q", req.Tool.Name, capabilityAuthorToolName)
	}
	if !strings.Contains(req.Prompt, "onboarding has gone cold") {
		t.Error("the user turn does not carry the operator's intent")
	}
	// The catalog reached the prompt, filtered: the spec-bearing artifacts and
	// the operation are in, the specless DDL and the poison row are out.
	for _, want := range []string{"staleOnboarding", "existingTarget", "nudgePattern", "SendReminder"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("the user turn does not carry catalog entry %q", want)
		}
	}
	if strings.Contains(req.Prompt, "SomeEvent") {
		t.Error("a meta with neither a spec nor permittedCommands reached the prompt")
	}
	// The lens's cypher shape reaches the vendor; its DSN and RLS posture never
	// do — the sanitiser strips them before the spec enters the prompt.
	if !strings.Contains(req.Prompt, "missing_reminder") {
		t.Error("the lens cypher (which the author needs) did not reach the prompt")
	}
	for _, leak := range []string{"secret@db.internal", "secureColumns", "grantSource", "grantTable", "\"protected\""} {
		if strings.Contains(req.Prompt, leak) {
			t.Errorf("the catalog leaked %q into the prompt sent to the vendor", leak)
		}
	}

	// The runner answers.
	f.writeResult(t, testHandle, completed(t, goodDraft(), "claude-opus-5-20260101"))

	d, err = f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	p := decodeProposal(t, d)

	if p.Kind != CapabilityAuthorKind {
		t.Errorf("kind = %q, want %q", p.Kind, CapabilityAuthorKind)
	}
	// The target must be one the apply path accepts: newPackage into a fresh,
	// handle-derived package at 0.1.0. (mode "install" — the Studio's own bundle
	// value — never applies.)
	if p.Target.Mode != "newPackage" {
		t.Errorf("target.mode = %q, want newPackage (the apply path rejects install)", p.Target.Mode)
	}
	if p.Target.PackageName != "ai-target-"+testHandle {
		t.Errorf("target.packageName = %q, want the handle-derived fresh package", p.Target.PackageName)
	}
	if p.Target.NewVersion != "0.1.0" {
		t.Errorf("target.newVersion = %q, want 0.1.0", p.Target.NewVersion)
	}
	if p.Validation.State != ValidationStateValid {
		t.Errorf("validation.state = %q, want %q (report %q)", p.Validation.State, ValidationStateValid, p.Validation.Report)
	}
	if p.Confidence != 0.82 {
		t.Errorf("confidence = %v, want 0.82", p.Confidence)
	}
	if p.Provenance.Model != "claude-opus-5-20260101" {
		t.Errorf("provenance.model = %q, want the model that ANSWERED", p.Provenance.Model)
	}
	if p.Provenance.PromptHash == "" || p.Provenance.CatalogHash == "" {
		t.Errorf("provenance hashes are empty: %+v", p.Provenance)
	}
	if p.Provenance.ReasonedAt != "2026-08-21T12:00:00Z" {
		t.Errorf("provenance.reasonedAt = %q, want the injected clock", p.Provenance.ReasonedAt)
	}

	// The recorded content is the assembled artifact: the gap list folded into
	// the missing_<gap> object, params folded into an object.
	var content map[string]any
	if err := json.Unmarshal([]byte(p.Content), &content); err != nil {
		t.Fatalf("content is not JSON: %v (%s)", err, p.Content)
	}
	gaps, ok := content["gaps"].(map[string]any)
	if !ok || len(gaps) != 1 {
		t.Fatalf("content.gaps = %#v, want one keyed entry", content["gaps"])
	}
	gap, ok := gaps["missing_reminder"].(map[string]any)
	if !ok {
		t.Fatalf("content.gaps has no missing_reminder entry: %#v", gaps)
	}
	if gap["action"] != "directOp" || gap["operation"] != "SendReminder" {
		t.Errorf("gap = %#v, want the directOp action carried through", gap)
	}
	if _, present := gap["pattern"]; present {
		t.Error("a blank field the chosen action does not use was recorded as an authored value")
	}
	params, ok := gap["params"].(map[string]any)
	if !ok || params["identity"] != "row.key" || params["channel"] != "email" {
		t.Errorf("gap.params = %#v, want the param list folded into an object", gap["params"])
	}
	if content["description"] != "Every cold onboarding gets one reminder." {
		t.Errorf("content.description = %#v, want the model's own prose", content["description"])
	}
	// The model chose the lens by canonicalName; the recorded lensRef is the
	// installed lens's NanoID — the only form the apply path resolves.
	if content["lensRef"] != staleLensNanoID {
		t.Errorf("content.lensRef = %#v, want the resolved NanoID %q", content["lensRef"], staleLensNanoID)
	}

	// The validator judged exactly the bytes that were recorded.
	judged := f.validator.judged()
	if len(judged) != 1 {
		t.Fatalf("validator calls = %d, want 1", len(judged))
	}
	if string(judged[0]) != p.Content {
		t.Errorf("the validator judged %s but %s was recorded", judged[0], p.Content)
	}

	// One answer, one vendor call.
	if got := len(f.runner.calls()); got != 1 {
		t.Errorf("dispatches = %d, want 1 (a resolved poll never re-dispatches)", got)
	}
}

// --- refusal ----------------------------------------------------------------

func TestCapabilityAuthor_RefusalIsTerminalAndFilesNoProposal(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	if _, err := f.adapter.Execute(ctx, authoringRequest("author something the model will decline")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, wire.Result{State: wire.StateRefused, RefusalCategory: "policy"})

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if d.Disposition != Resolved || d.Result.Status != OutcomeFailed {
		t.Fatalf("Poll = %+v, want a terminal failed verdict", d)
	}
	if !strings.Contains(d.Result.Detail, "policy") {
		t.Errorf("detail = %q, want the refusal category", d.Result.Detail)
	}
	if strings.Contains(d.Result.Detail, "\"kind\"") {
		t.Errorf("a refusal filed a proposal: %q", d.Result.Detail)
	}
	if got := len(f.validator.judged()); got != 0 {
		t.Errorf("validator calls = %d, want 0 (there is nothing to validate)", got)
	}
	if got := len(f.runner.calls()); got != 1 {
		t.Errorf("dispatches = %d, want 1 (a refusal is never retried)", got)
	}
}

// --- the repair budget ------------------------------------------------------

func TestCapabilityAuthor_InvalidDraftRepairsToValid(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()
	f.validator.verdict = rejectDottedTargets

	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, completed(t, dottedDraft(), "model-a"))

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (repair dispatch): %v", err)
	}
	if d.Disposition != Pending {
		t.Fatalf("Poll = %+v, want Pending while the correction pass runs", d)
	}

	calls := f.runner.calls()
	if len(calls) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(calls))
	}
	repair := calls[1]
	if repair.Ref != testHandle+repairRefSuffix {
		t.Errorf("repair ref = %q, want %q", repair.Ref, testHandle+repairRefSuffix)
	}
	if !wire.ValidRef(repair.Ref) {
		t.Errorf("repair ref %q is not a usable result-bucket key", repair.Ref)
	}
	if !strings.Contains(repair.Prompt, "is not a valid single KV-key segment") {
		t.Error("the correction turn does not carry the validator's errors")
	}
	if !strings.Contains(repair.Prompt, "cold.onboarding.reminder") {
		t.Error("the correction turn does not carry the rejected draft")
	}
	if !strings.Contains(repair.Prompt, "remind cold onboardings") {
		t.Error("the correction turn dropped the original intent")
	}
	if repair.System != calls[0].System {
		t.Error("the correction pass changed the authoring rules")
	}

	// A poll while the correction is still in flight neither dispatches nor
	// resolves.
	f.writeResult(t, repair.Ref, wire.Result{State: wire.StateInflight})
	d, err = f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (in flight): %v", err)
	}
	if d.Disposition != Pending {
		t.Fatalf("Poll = %+v, want Pending", d)
	}
	if got := len(f.runner.calls()); got != 2 {
		t.Fatalf("dispatches = %d, want 2 (an in-flight repair is not re-dispatched)", got)
	}

	f.writeResult(t, repair.Ref, completed(t, goodDraft(), "model-b"))
	d, err = f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (repaired): %v", err)
	}
	p := decodeProposal(t, d)
	if p.Validation.State != ValidationStateValid {
		t.Errorf("validation.state = %q, want %q", p.Validation.State, ValidationStateValid)
	}
	if p.Provenance.Model != "model-b" {
		t.Errorf("provenance.model = %q, want the model that answered the CORRECTION", p.Provenance.Model)
	}
	if p.Provenance.PromptHash == calls[0].Ref {
		t.Error("prompt hash is not a hash")
	}
	if got := len(f.runner.calls()); got != 2 {
		t.Errorf("dispatches = %d, want 2 (the budget is two calls per request)", got)
	}
}

func TestCapabilityAuthor_StillInvalidAfterRepairFilesTheInvalidVerdict(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()
	f.validator.verdict = func(_ int, _ string, _ []byte) (string, string) {
		return ValidationStateInvalid, "lensRef \"staleOnboarding\" is not installed"
	}

	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, completed(t, goodDraft(), "model-a"))
	if _, err := f.adapter.Poll(ctx, testHandle); err != nil {
		t.Fatalf("Poll (repair dispatch): %v", err)
	}
	f.writeResult(t, testHandle+repairRefSuffix, completed(t, goodDraft(), "model-b"))

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (final): %v", err)
	}
	p := decodeProposal(t, d)
	if p.Validation.State != ValidationStateInvalid {
		t.Errorf("validation.state = %q, want %q — a still-invalid draft records visibly", p.Validation.State, ValidationStateInvalid)
	}
	if !strings.Contains(p.Validation.Report, "not installed") {
		t.Errorf("validation.report = %q, want the validator's own errors", p.Validation.Report)
	}
	if p.Content == "" {
		t.Error("an invalid proposal recorded no artifact for the operator to fix")
	}

	// The budget is spent: a further poll re-files the same verdict without a
	// third call.
	if _, err := f.adapter.Poll(ctx, testHandle); err != nil {
		t.Fatalf("Poll (repeat): %v", err)
	}
	if got := len(f.runner.calls()); got != 2 {
		t.Errorf("dispatches = %d, want 2", got)
	}
}

func TestCapabilityAuthor_VendorFailureSpendsTheRetrySlot(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, wire.Result{State: wire.StateFailed, Error: "upstream timeout"})

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (retry dispatch): %v", err)
	}
	if d.Disposition != Pending {
		t.Fatalf("Poll = %+v, want Pending while the retry runs", d)
	}
	calls := f.runner.calls()
	if len(calls) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(calls))
	}
	if calls[1].Ref != testHandle+repairRefSuffix {
		t.Errorf("retry ref = %q, want the repair slot %q", calls[1].Ref, testHandle+repairRefSuffix)
	}
	if calls[1].Prompt != calls[0].Prompt {
		t.Error("a vendor failure produced no draft, so its retry must re-send the original turn unchanged")
	}

	f.writeResult(t, calls[1].Ref, completed(t, goodDraft(), "model-b"))
	d, err = f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (retried): %v", err)
	}
	p := decodeProposal(t, d)
	if p.Validation.State != ValidationStateValid {
		t.Errorf("validation.state = %q, want %q", p.Validation.State, ValidationStateValid)
	}
	if got := len(f.runner.calls()); got != 2 {
		t.Errorf("dispatches = %d, want 2", got)
	}
}

func TestCapabilityAuthor_VendorFailureBudgetExhausted(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, wire.Result{State: wire.StateFailed, Error: "upstream timeout"})
	if _, err := f.adapter.Poll(ctx, testHandle); err != nil {
		t.Fatalf("Poll (retry dispatch): %v", err)
	}
	f.writeResult(t, testHandle+repairRefSuffix, wire.Result{State: wire.StateFailed, Error: "upstream timeout again"})

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (final): %v", err)
	}
	if d.Disposition != Resolved || d.Result.Status != OutcomeFailed {
		t.Fatalf("Poll = %+v, want a terminal failed verdict", d)
	}
	if !strings.Contains(d.Result.Detail, "upstream timeout") || !strings.Contains(d.Result.Detail, "again") {
		t.Errorf("detail = %q, want both attempts' errors", d.Result.Detail)
	}
	if got := len(f.runner.calls()); got != 2 {
		t.Errorf("dispatches = %d, want 2 (never a third)", got)
	}
}

// --- ack handling -----------------------------------------------------------

func TestCapabilityAuthor_BusyAckIsTransient(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()
	f.runner.ack = func(n int, req wire.Request) (wire.Ack, error) {
		if n == 0 {
			return wire.Ack{Status: wire.AckBusy, Ref: req.Ref, Reason: "daily call cap reached"}, nil
		}
		return wire.Ack{Status: wire.AckAccepted, Ref: req.Ref}, nil
	}

	_, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings"))
	if err == nil {
		t.Fatal("Execute: want a transient error on a busy ack, got nil")
	}
	if !errors.Is(err, wire.ErrBusy) {
		t.Errorf("Execute error = %v, want it to wrap wire.ErrBusy", err)
	}

	// Nothing was memoised, so the bridge's re-drive really re-dispatches.
	d, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings"))
	if err != nil {
		t.Fatalf("Execute (re-drive): %v", err)
	}
	if d.Disposition != Pending {
		t.Fatalf("Execute = %+v, want Pending", d)
	}
	if got := len(f.runner.calls()); got != 2 {
		t.Errorf("dispatches = %d, want 2 (one refused for capacity, one accepted)", got)
	}
}

func TestCapabilityAuthor_InvalidAckIsTerminal(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()
	f.runner.ack = func(_ int, req wire.Request) (wire.Ack, error) {
		return wire.Ack{Status: wire.AckInvalid, Ref: req.Ref, Reason: "tool.name is required"}, nil
	}

	d, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings"))
	if err != nil {
		t.Fatalf("Execute: want a terminal verdict, got error %v", err)
	}
	if d.Disposition != Resolved || d.Result.Status != OutcomeFailed {
		t.Fatalf("Execute = %+v, want a terminal failed verdict (a malformed request is a bug, not back-pressure)", d)
	}
}

func TestCapabilityAuthor_UnusableHandleIsTerminal(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	req := authoringRequest("remind cold onboardings")
	req.IdempotencyKey = "has spaces and *wildcards"

	d, err := f.adapter.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: want a terminal verdict, got error %v", err)
	}
	if d.Disposition != Resolved || d.Result.Status != OutcomeFailed {
		t.Fatalf("Execute = %+v, want a terminal failed verdict", d)
	}
	if got := len(f.runner.calls()); got != 0 {
		t.Errorf("dispatches = %d, want 0 — an unusable ref never reaches the runner", got)
	}
}

func TestCapabilityAuthor_MissingIntentIsTerminal(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)

	d, err := f.adapter.Execute(context.Background(), authoringRequest("   "))
	if err != nil {
		t.Fatalf("Execute: want a terminal verdict, got error %v", err)
	}
	if d.Disposition != Resolved || d.Result.Status != OutcomeFailed {
		t.Fatalf("Execute = %+v, want a terminal failed verdict", d)
	}
	if got := len(f.runner.calls()); got != 0 {
		t.Errorf("dispatches = %d, want 0 — an empty intent is never worth a vendor call", got)
	}
}

// --- idempotency + recovery -------------------------------------------------

func TestCapabilityAuthor_ExecuteIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()
	req := authoringRequest("remind cold onboardings")

	first, err := f.adapter.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	second, err := f.adapter.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute (redelivered): %v", err)
	}
	if first != second {
		t.Errorf("redelivery returned %+v, want the first dispatch %+v verbatim", second, first)
	}
	if got := len(f.runner.calls()); got != 1 {
		t.Errorf("dispatches = %d, want 1 — a redelivered event costs no second vendor call", got)
	}
}

func TestCapabilityAuthor_AbsentResultRedispatchesTheFirstAttempt(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// No row at all: the runner that claimed the ref died and its in-flight
	// marker's TTL reaped it.
	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if d.Disposition != Pending {
		t.Fatalf("Poll = %+v, want Pending after a re-dispatch", d)
	}
	calls := f.runner.calls()
	if len(calls) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(calls))
	}
	if calls[1].Ref != testHandle {
		t.Errorf("re-dispatch ref = %q, want the FIRST attempt %q (the repair slot stays unspent)", calls[1].Ref, testHandle)
	}
	if calls[1].Prompt != calls[0].Prompt {
		t.Error("the re-dispatch changed the prompt")
	}
}

func TestCapabilityAuthor_ColdEpisodeStillFilesALandedAnswer(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	// A fresh adapter over the same buckets: the bridge restarted after the
	// request went out, so the prompt is gone but the answer is in KV.
	restarted, err := NewCapabilityAuthor(f.runner, f.conn, testCatalogBucket, f.validator.validate)
	if err != nil {
		t.Fatalf("NewCapabilityAuthor: %v", err)
	}
	f.writeResult(t, testHandle, completed(t, goodDraft(), "model-a"))

	d, err := restarted.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	p := decodeProposal(t, d)
	if p.Validation.State != ValidationStateValid {
		t.Errorf("validation.state = %q, want %q — the verdict comes from the answer, not from memory", p.Validation.State, ValidationStateValid)
	}
	if p.Provenance.Model != "model-a" {
		t.Errorf("provenance.model = %q, want the model that answered", p.Provenance.Model)
	}
	if p.Provenance.PromptHash != "" || p.Provenance.CatalogHash != "" {
		t.Errorf("provenance = %+v, want the unknown hashes left ABSENT rather than fabricated", p.Provenance)
	}
	if got := len(f.runner.calls()); got != 0 {
		t.Errorf("dispatches = %d, want 0 — a landed answer needs no call", got)
	}
}

func TestCapabilityAuthor_ColdEpisodeWithNoAnswerIsTerminal(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)

	restarted, err := NewCapabilityAuthor(f.runner, f.conn, testCatalogBucket, f.validator.validate)
	if err != nil {
		t.Fatalf("NewCapabilityAuthor: %v", err)
	}
	d, err := restarted.Poll(context.Background(), testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if d.Disposition != Resolved || d.Result.Status != OutcomeFailed {
		t.Fatalf("Poll = %+v, want a terminal failed verdict rather than a poll chain that can never resolve", d)
	}
	if got := len(f.runner.calls()); got != 0 {
		t.Errorf("dispatches = %d, want 0 — there is no prompt to re-send", got)
	}
}

// --- the verdict is the validator's -----------------------------------------

func TestCapabilityAuthor_ValidatorOverridesAConfidentModel(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()
	f.validator.verdict = func(_ int, kind string, _ []byte) (string, string) {
		if kind != CapabilityAuthorKind {
			t.Errorf("validator saw kind %q, want %q", kind, CapabilityAuthorKind)
		}
		return ValidationStateInvalid, "targetId is not a valid single KV-key segment"
	}

	art := goodDraft()
	art.Confidence = 1
	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, completed(t, art, "model-a"))
	if _, err := f.adapter.Poll(ctx, testHandle); err != nil {
		t.Fatalf("Poll (repair dispatch): %v", err)
	}
	f.writeResult(t, testHandle+repairRefSuffix, completed(t, art, "model-a"))

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (final): %v", err)
	}
	p := decodeProposal(t, d)
	if p.Validation.State != ValidationStateInvalid {
		t.Errorf("validation.state = %q — a confident model does not get to grade itself", p.Validation.State)
	}
	if p.Confidence != 1 {
		t.Errorf("confidence = %v, want the model's own claim carried through as DATA (never as the verdict)", p.Confidence)
	}
}

func TestCapabilityAuthor_ForeignKindIsNeverPassedThrough(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	art := goodDraft()
	art.Kind = "lens"
	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, completed(t, art, "model-a"))

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if d.Disposition != Pending {
		t.Fatalf("Poll = %+v, want the repair path (a foreign kind is an invalid draft)", d)
	}
	f.writeResult(t, testHandle+repairRefSuffix, completed(t, art, "model-a"))
	d, err = f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (final): %v", err)
	}
	p := decodeProposal(t, d)
	if p.Kind != CapabilityAuthorKind {
		t.Errorf("kind = %q — the model's claimed kind must never become the recorded kind", p.Kind)
	}
	if p.Validation.State != ValidationStateInvalid {
		t.Errorf("validation.state = %q, want %q", p.Validation.State, ValidationStateInvalid)
	}
	if !strings.Contains(p.Validation.Report, "authors only") {
		t.Errorf("validation.report = %q, want it to name the kind mismatch", p.Validation.Report)
	}
}

func TestCapabilityAuthor_UndecodableOutputIsInvalid(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, wire.Result{State: wire.StateCompleted, Output: json.RawMessage(`"not an object"`), Model: "model-a"})
	if _, err := f.adapter.Poll(ctx, testHandle); err != nil {
		t.Fatalf("Poll (repair dispatch): %v", err)
	}
	f.writeResult(t, testHandle+repairRefSuffix, wire.Result{State: wire.StateCompleted, Output: json.RawMessage(`"still not an object"`), Model: "model-a"})

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll (final): %v", err)
	}
	p := decodeProposal(t, d)
	if p.Validation.State != ValidationStateInvalid {
		t.Errorf("validation.state = %q, want %q", p.Validation.State, ValidationStateInvalid)
	}
	if !strings.Contains(p.Validation.Report, "did not decode") {
		t.Errorf("validation.report = %q, want the decode failure", p.Validation.Report)
	}
}

// TestCapabilityAuthor_BusyAtRepairFilesInvalidDraft pins A3: when the first
// draft failed validation and the correction dispatch cannot be accepted (a busy
// or unreachable fleet), the adapter files the invalid draft it already holds
// rather than blocking the request on a second call that may never come.
func TestCapabilityAuthor_BusyAtRepairFilesInvalidDraft(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()
	f.validator.verdict = rejectDottedTargets
	f.runner.ack = func(n int, req wire.Request) (wire.Ack, error) {
		if n == 0 {
			return wire.Ack{Status: wire.AckAccepted, Ref: req.Ref}, nil
		}
		// The correction dispatch is refused for capacity.
		return wire.Ack{Status: wire.AckBusy, Ref: req.Ref, Reason: "no worker slot"}, nil
	}

	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, completed(t, dottedDraft(), "model-a"))

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: want the held draft filed, got error %v", err)
	}
	p := decodeProposal(t, d)
	if p.Validation.State != ValidationStateInvalid {
		t.Errorf("validation.state = %q, want %q — the held draft is filed for the operator to fix", p.Validation.State, ValidationStateInvalid)
	}
	if p.Content == "" {
		t.Error("no artifact filed for the operator to fix in the Studio")
	}
	if got := len(f.runner.calls()); got != 2 {
		t.Errorf("dispatches = %d, want 2 (one accepted, one refused) — never a blocking wait on a third", got)
	}
}

// TestCapabilityAuthor_ModelDescriptionIsCapped pins A5: a model-supplied
// description is bounded exactly like the intent-derived fallback — an
// over-long one never reaches the roster verbatim.
func TestCapabilityAuthor_ModelDescriptionIsCapped(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	art := goodDraft()
	art.Content.Description = strings.Repeat("word ", 400) // ~2000 chars, far over the cap
	if _, err := f.adapter.Execute(ctx, authoringRequest("remind cold onboardings")); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.writeResult(t, testHandle, completed(t, art, "model-a"))

	d, err := f.adapter.Poll(ctx, testHandle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	p := decodeProposal(t, d)
	var content map[string]any
	if err := json.Unmarshal([]byte(p.Content), &content); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	desc, _ := content["description"].(string)
	if len(desc) > maxDistilledDescription {
		t.Errorf("recorded description is %d bytes, want <= %d (a model description must be capped too)", len(desc), maxDistilledDescription)
	}
}

// TestCapabilityAuthor_OversizedInputsStayUnderTheWall pins A6: neither an
// enormous catalog nor an enormous intent lets the dispatched request cross the
// NATS payload wall. The whole marshaled request must stay well under 1 MiB, and
// the catalog notes its truncation so the model treats absent entries as unshown.
func TestCapabilityAuthor_OversizedInputsStayUnderTheWall(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	// Flood the catalog with far more rows than the cap admits, each carrying a
	// bulky spec, so both the row-count and byte ceilings bite.
	seedCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	bulk := strings.Repeat("x", 4000)
	for i := 0; i < 800; i++ {
		id, err := substrate.NewNanoID()
		if err != nil {
			t.Fatalf("mint flood id: %v", err)
		}
		row := fmt.Sprintf(`{"key":"vtx.meta.%s","class":"meta.lens","canonicalName":"flood%d","spec":{"cypherRule":"%s"}}`, id, i, bulk)
		if _, err := f.conn.KVPut(seedCtx, testCatalogBucket, "vtx.meta."+id, []byte(row)); err != nil {
			t.Fatalf("seed flood row %d: %v", i, err)
		}
	}

	hugeIntent := strings.Repeat("please remind everyone about everything. ", 2000) // ~80 KB
	if _, err := f.adapter.Execute(ctx, authoringRequest(hugeIntent)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	calls := f.runner.calls()
	if len(calls) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(calls))
	}
	req := calls[0]

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	const natsDefaultMaxPayload = 1 << 20
	if len(body) >= natsDefaultMaxPayload {
		t.Errorf("dispatched request is %d bytes, must stay under the %d NATS payload wall", len(body), natsDefaultMaxPayload)
	}
	if len(req.Prompt) < 1000 {
		t.Errorf("prompt shrank to %d bytes — the cap should trim, not gut, the catalog", len(req.Prompt))
	}
	if !strings.Contains(req.Prompt, "truncated") {
		t.Error("a capped catalog must tell the model it was truncated")
	}
	if strings.Contains(req.Prompt, hugeIntent) {
		t.Error("the oversized intent reached the prompt uncapped")
	}
}

// --- assembly ---------------------------------------------------------------

func TestAssembleTargetContent_StructuralDefectsAreReported(t *testing.T) {
	t.Parallel()
	index := map[string]string{"someLens": "aaaaaaaaaaaaaaaaaaaa"}
	content, problems := assembleTargetContent(modelTargetContent{
		TargetID: "someTarget",
		LensRef:  "someLens",
		Gaps: []modelGapAction{
			{GapColumn: "", Action: "surface", IssueCode: "x"},
			{GapColumn: "missing_a", Action: "surface", IssueCode: "x"},
			{GapColumn: "missing_a", Action: "surface", IssueCode: "y"},
			{GapColumn: "missing_b", Action: "directOp", Operation: "Op", Params: []modelParam{{Key: "", Value: "v"}}},
		},
	}, "distilled from the intent", index)

	if len(problems) != 3 {
		t.Fatalf("problems = %#v, want the unnamed column, the duplicate column and the unnamed param", problems)
	}
	var decoded map[string]any
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	gaps := decoded["gaps"].(map[string]any)
	if len(gaps) != 2 {
		t.Errorf("gaps = %#v, want the two well-formed entries only", gaps)
	}
	if got := gaps["missing_a"].(map[string]any)["issueCode"]; got != "x" {
		t.Errorf("duplicate column resolved to %v, want the FIRST entry kept", got)
	}
	if _, present := gaps["missing_b"].(map[string]any)["params"]; present {
		t.Error("a param with no name was recorded")
	}
	if decoded["description"] != "distilled from the intent" {
		t.Errorf("description = %v, want the fallback when the model omits one", decoded["description"])
	}
}

func TestAssembleTargetContent_IsByteStable(t *testing.T) {
	t.Parallel()
	src := goodDraft().Content
	index := map[string]string{staleLensCanonical: staleLensNanoID}
	first, _ := assembleTargetContent(src, "", index)
	second, _ := assembleTargetContent(src, "", index)
	if string(first) != string(second) {
		t.Errorf("the same answer assembled to %s and %s", first, second)
	}
}

// TestAssembleTargetContent_UnresolvedLensRefIsRejected pins A7: a lensRef the
// catalog does not resolve (a dotted/wildcard/made-up value the model returned)
// must never be filed — record-time validation does not check lensRef shape, so
// it would record "valid" and fail only at install.
func TestAssembleTargetContent_UnresolvedLensRefIsRejected(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"not.a.lens", "someLens*", "", "unknownCanonical"} {
		src := goodDraft().Content
		src.LensRef = ref
		content, problems := assembleTargetContent(src, "", map[string]string{staleLensCanonical: staleLensNanoID})
		if len(problems) == 0 {
			t.Errorf("lensRef %q: want a problem recorded, got none", ref)
		}
		var decoded map[string]any
		if err := json.Unmarshal(content, &decoded); err != nil {
			t.Fatalf("content is not JSON: %v", err)
		}
		if decoded["lensRef"] != "" {
			t.Errorf("lensRef %q: recorded lensRef = %#v, want empty (never the model's raw value)", ref, decoded["lensRef"])
		}
	}
}

// --- the catalog digest -----------------------------------------------------

func TestCatalogDigest_IsStableAcrossReads(t *testing.T) {
	t.Parallel()
	f := newAuthorFixture(t)
	ctx := context.Background()

	// Read repeatedly: KVGetMulti hands back a MAP, so a digest computed over
	// its iteration order would drift between otherwise identical reads.
	first, err := f.adapter.readCatalog(ctx)
	if err != nil {
		t.Fatalf("readCatalog: %v", err)
	}
	for i := 0; i < 8; i++ {
		next, err := f.adapter.readCatalog(ctx)
		if err != nil {
			t.Fatalf("readCatalog %d: %v", i, err)
		}
		if next.hash != first.hash {
			t.Fatalf("catalogHash drifted between reads: %q then %q", first.hash, next.hash)
		}
		if next.serialized != first.serialized {
			t.Fatalf("catalog serialisation drifted between reads")
		}
	}
}

func TestCatalogDigest_IsIndependentOfRowDeliveryOrder(t *testing.T) {
	t.Parallel()
	rows := map[string]string{
		"vtx.meta.LensRowAaaaaaaaaaaa": `{"key":"vtx.meta.LensRowAaaaaaaaaaaa","class":"meta.lens","canonicalName":"a","spec":{"spec":"x"}}`,
		"vtx.meta.LensRowBbbbbbbbbbbb": `{"key":"vtx.meta.LensRowBbbbbbbbbbbb","class":"meta.lens","canonicalName":"b","spec":{"spec":"y"}}`,
		"vtx.meta.OpRowCcccccccccccca": `{"key":"vtx.meta.OpRowCcccccccccccca","class":"meta.ddl.vertexType","canonicalName":"C","permittedCommands":["C"]}`,
	}
	value := func(key string) []byte { return []byte(rows[key]) }

	sorted := []string{"vtx.meta.LensRowAaaaaaaaaaaa", "vtx.meta.LensRowBbbbbbbbbbbb", "vtx.meta.OpRowCcccccccccccca"}
	sortedView, _ := buildCatalogView(sorted, value)
	want, err := json.Marshal(sortedView)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The permutations a map-ordered walk would produce must not change the
	// view: readCatalog sorts the key list, and buildCatalogView walks the list.
	for _, perm := range [][]string{
		{"vtx.meta.OpRowCcccccccccccca", "vtx.meta.LensRowBbbbbbbbbbbb", "vtx.meta.LensRowAaaaaaaaaaaa"},
		{"vtx.meta.LensRowBbbbbbbbbbbb", "vtx.meta.OpRowCcccccccccccca", "vtx.meta.LensRowAaaaaaaaaaaa"},
	} {
		shuffled := append([]string(nil), perm...)
		sortStrings(shuffled)
		shuffledView, _ := buildCatalogView(shuffled, value)
		got, err := json.Marshal(shuffledView)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("catalog view differs for permutation %v:\n got %s\nwant %s", perm, got, want)
		}
	}
}

// sortStrings is the sort readCatalog applies to its key listing before the
// batch read, applied here so the permutation test exercises the same pipeline.
func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

// TestCatalogRead_EmptyBucketIsTerminal pins A2: an empty (or lens-less) catalog
// is the "capability-author not provisioned" gap, which redelivery cannot fix.
// The bridge arms its CallDeadline only on a Pending outcome, so a transient
// error here would hang the request invisibly forever — Execute returns a
// terminal, visible failure instead.
func TestCatalogRead_EmptyBucketIsTerminal(t *testing.T) {
	t.Parallel()
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap conn: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: testCatalogBucket}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	runner := &fakeRunner{}
	adapter, err := NewCapabilityAuthor(runner, conn, testCatalogBucket, (&fakeValidator{}).validate)
	if err != nil {
		t.Fatalf("NewCapabilityAuthor: %v", err)
	}

	d, err := adapter.Execute(ctx, authoringRequest("remind cold onboardings"))
	if err != nil {
		t.Fatalf("Execute: want a terminal verdict, got error %v", err)
	}
	if d.Disposition != Resolved || d.Result.Status != OutcomeFailed {
		t.Fatalf("Execute = %+v, want a terminal failed verdict (an unprovisioned catalog never self-resolves)", d)
	}
	if !strings.Contains(d.Result.Detail, "context unavailable") {
		t.Errorf("detail = %q, want it to name the unavailable authoring context", d.Result.Detail)
	}
	if got := len(runner.calls()); got != 0 {
		t.Errorf("dispatches = %d, want 0 — a model is never asked to bind a target to a catalog it was not shown", got)
	}
}

// --- construction -----------------------------------------------------------

func TestNewCapabilityAuthor_RequiresEveryDependency(t *testing.T) {
	t.Parallel()
	conn := &substrate.Conn{}
	runner := &fakeRunner{}
	validate := (&fakeValidator{}).validate

	for name, build := range map[string]func() (*CapabilityAuthor, error){
		"no dispatcher": func() (*CapabilityAuthor, error) {
			return NewCapabilityAuthor(nil, conn, testCatalogBucket, validate)
		},
		"no conn": func() (*CapabilityAuthor, error) {
			return NewCapabilityAuthor(runner, nil, testCatalogBucket, validate)
		},
		"no bucket": func() (*CapabilityAuthor, error) {
			return NewCapabilityAuthor(runner, conn, "", validate)
		},
		"no validator": func() (*CapabilityAuthor, error) {
			return NewCapabilityAuthor(runner, conn, testCatalogBucket, nil)
		},
	} {
		name, build := name, build
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := build(); err == nil {
				t.Errorf("%s: want a construction error, got nil", name)
			}
		})
	}
}

// --- the tool schema --------------------------------------------------------

func TestCapabilityAuthorTool_IsStrictShaped(t *testing.T) {
	t.Parallel()
	tool := capabilityAuthorTool()
	if tool.Name == "" || len(tool.InputSchema.Properties) == 0 {
		t.Fatal("the tool schema is empty; the runner would reject the request as invalid")
	}
	// Every object the schema declares closes itself and requires everything it
	// declares — the shape strict tool use expects. The top-level object's own
	// closure is the runner's to supply.
	var walk func(path string, obj map[string]any)
	walk = func(path string, obj map[string]any) {
		if obj["type"] != "object" {
			if items, ok := obj["items"].(map[string]any); ok {
				walk(path+"[]", items)
			}
			return
		}
		if closed, ok := obj["additionalProperties"].(bool); !ok || closed {
			t.Errorf("%s: object does not declare additionalProperties:false", path)
		}
		props, _ := obj["properties"].(map[string]any)
		required, _ := obj["required"].([]string)
		if len(props) != len(required) {
			t.Errorf("%s: %d properties but %d required — strict tool use requires every declared property", path, len(props), len(required))
		}
		for _, name := range required {
			if _, ok := props[name]; !ok {
				t.Errorf("%s: required names %q, which is not a declared property", path, name)
			}
		}
		for name, raw := range props {
			if child, ok := raw.(map[string]any); ok {
				walk(path+"."+name, child)
			}
		}
	}
	for name, raw := range tool.InputSchema.Properties {
		if child, ok := raw.(map[string]any); ok {
			walk(name, child)
		}
	}
	// The kind is pinned to the one artifact this adapter authors.
	kind := tool.InputSchema.Properties["kind"].(map[string]any)
	enum, ok := kind["enum"].([]string)
	if !ok || len(enum) != 1 || enum[0] != CapabilityAuthorKind {
		t.Errorf("kind enum = %#v, want exactly [%q]", kind["enum"], CapabilityAuthorKind)
	}
}
