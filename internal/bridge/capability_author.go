package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Validation states the capability-author record path recognises. The
// RecordCapabilityProposal DDL admits a proposal to review.state=pending only
// when the caller-supplied verdict is exactly ValidationStateValid; anything
// else records the proposal as invalid — auditable, never silently dropped.
const (
	ValidationStateValid   = "valid"
	ValidationStateInvalid = "invalid"
)

// CapabilityAuthorKind is the single artifact kind this adapter authors. The
// capture→dispatch→record pipeline carries ONE artifact per authoring request,
// so an intent that also needs a new lens gets the target plus the lens sketched
// in the proposal's rationale for the operator to finish in the Studio.
const CapabilityAuthorKind = "weaverTarget"

// The install target every AI-authored proposal files. It must be one the
// apply path accepts: pkgmgr.CapabilityApplyPlanForProposal admits only
// "newPackage"/"upgradeExisting" and refuses an empty packageName
// (internal/pkgmgr/capabilityapply.go:156-197), so the Studio's own
// operator-authored `{mode:"install"}` bundle never actually applies — a latent
// gap this adapter must not reproduce. Every authoring request mints its own
// fresh package: a proposal-handle-derived name is unique (newPackage refuses a
// name already installed), never platform-protected (the deny-list is a fixed
// set of known names, none under this prefix), and re-derivable so a redelivery
// files the identical target.
const (
	capabilityAuthorTargetMode = "newPackage"
	authoredPackagePrefix      = "ai-target-"
	authoredPackageVersion     = "0.1.0"
)

// repairRefSuffix names the SECOND (and last) model call an authoring request
// may spend. The attempt refs are deterministic in the claim handle — attempt 1
// is the handle itself, attempt 2 is the handle + this suffix — so the whole
// attempt chain is derivable from the Poll ref alone, with no adapter-side
// bookkeeping. The mapping is injective: handles are dot-free 20-char NanoIDs
// whose alphabet excludes '-', so no handle can collide with another handle's
// repair ref. '#' is deliberately not used: wire.ValidRef excludes it.
const repairRefSuffix = "-r2"

// maxDistilledSentences / maxDistilledDescription bound the roster description —
// the opening one or two sentences, capped. distill() enforces both, and it is
// applied to a MODEL-supplied description as much as to the intent-derived
// fallback: neither reaches the roster unbounded.
const (
	maxDistilledSentences   = 2
	maxDistilledDescription = 280
)

// Input bounds. The whole generate request is one NATS message under the
// server's max_payload (1 MiB by default); an uncapped catalog or intent would
// silently cross it and every dispatch would fail nats:max_payload as an
// unbounded transient. The catalog budget sits well under the wall with room
// for the system prompt and tool schema; the intent cap is generous for plain
// language and only trims abuse.
const (
	maxCatalogBytes = 512 * 1024
	maxCatalogRows  = 400
	maxIntentBytes  = 8 * 1024
)

// ArtifactValidator computes the deterministic record-time verdict for one
// proposed capability artifact: "valid" / "invalid" plus a human-readable
// report. It is injected rather than called directly so internal/bridge stays
// free of internal/pkgmgr — the bridge is a substrate-only leaf, and the
// composition root (cmd/bridge) owns the wiring of the real
// pkgmgr.ValidateCapabilityArtifact path.
//
// This function IS the verdict. The model's own claim about its output is never
// consulted: a draft that fails here records as invalid, visibly, exactly like a
// malformed result would.
type ArtifactValidator func(kind string, content []byte) (state string, report string)

// ModelDispatcher submits one generation request to the model-runner fleet and
// returns the runner's immediate ack. *wire.Client is the production
// implementation; the interface exists so the adapter's tests drive the
// dispatch leg without a runner.
type ModelDispatcher interface {
	Dispatch(ctx context.Context, req wire.Request) (wire.Ack, error)
}

// CapabilityAuthor is the model-backed `capabilityAuthor` adapter: it turns an
// operator's plain-language intent into a proposed weaver target by asking the
// model-runner fleet, validating the answer deterministically, and filing the
// result as a CapabilityAuthorProposal for human review.
//
// It is asynchronous by construction. Execute assembles the catalog and the
// prompt, dispatches to the runner, and returns Pending immediately — a model
// turn takes minutes, and the bridge's events consumer must not block on one.
// Poll resolves the request by reading the runner's result bucket.
//
// It holds no vendor credential and speaks no vendor protocol: the runner is
// the platform's only external-model egress. Its two KV reads are both
// read-model reads — the capability-author catalog lens target, and the runner's
// own result bucket (bridge↔runner operational state, which the runner alone
// writes).
type CapabilityAuthor struct {
	runner        ModelDispatcher
	conn          *substrate.Conn
	contextBucket string
	validate      ArtifactValidator
	now           func() time.Time

	mu       sync.Mutex
	episodes map[string]*authoringEpisode
}

// authoringEpisode is the in-memory record of ONE authoring request's PROMPT
// INPUTS. It is deliberately not the attempt state (see Poll's state table):
// attempt state is always re-derived from the result bucket, so two bridge
// instances — or one that restarted — agree on where an episode stands. The
// prompt inputs cannot live there (the runner is the bucket's only writer), so
// they live here and degrade honestly when the process did not assemble them.
//
// After an episode resolves, the prompt strings are dropped and only the
// provenance hashes are retained, so a redelivered fired-poll still files the
// real hashes. Retention is bounded by the runner's daily call cap: an episode
// costs a vendor call, so a process can only ever accumulate as many settled
// records as calls the fleet allowed it to make.
//
// The correction pass keeps no prompt of its own: it is a pure function of the
// first turn, the rejected draft and the validator's report, all of which are
// re-derivable at any later Poll. Only its hash is kept, because the draft it
// produced is what gets recorded.
type authoringEpisode struct {
	intent      string
	model       string
	system      string
	prompt      string
	promptHash  string
	catalogHash string
	repairHash  string
	settled     bool
}

// CapabilityAuthorOption adjusts a CapabilityAuthor at construction.
type CapabilityAuthorOption func(*CapabilityAuthor)

// WithCapabilityAuthorClock overrides the clock stamping a proposal's
// reasonedAt.
func WithCapabilityAuthorClock(now func() time.Time) CapabilityAuthorOption {
	return func(a *CapabilityAuthor) {
		if now != nil {
			a.now = now
		}
	}
}

// NewCapabilityAuthor builds the adapter over a model-runner dispatcher, the
// substrate connection it reads both KV surfaces through, the capability-author
// catalog bucket, and the deterministic artifact validator. A missing
// dependency is a wiring bug in the composition root, surfaced here rather than
// nil-panicking on the first authoring request.
func NewCapabilityAuthor(runner ModelDispatcher, conn *substrate.Conn, contextBucket string, validate ArtifactValidator, opts ...CapabilityAuthorOption) (*CapabilityAuthor, error) {
	if runner == nil {
		return nil, fmt.Errorf("bridge: capabilityAuthor: model dispatcher is required")
	}
	if conn == nil {
		return nil, fmt.Errorf("bridge: capabilityAuthor: substrate connection is required")
	}
	if contextBucket == "" {
		return nil, fmt.Errorf("bridge: capabilityAuthor: catalog bucket name is required")
	}
	if validate == nil {
		return nil, fmt.Errorf("bridge: capabilityAuthor: artifact validator is required (the model's own verdict is never trusted)")
	}
	a := &CapabilityAuthor{
		runner:        runner,
		conn:          conn,
		contextBucket: contextBucket,
		validate:      validate,
		now:           time.Now,
		episodes:      make(map[string]*authoringEpisode),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// Execute assembles the catalog snapshot and the authoring prompt, dispatches
// one model call under the claim handle as its ref, and returns Pending.
//
// The idempotencyKey (the claim handle) IS the model-runner ref, which makes the
// double-spend guard structural rather than adapter-side: a redelivered event
// re-dispatches the same ref, the runner's CAS-created in-flight marker finds it
// already claimed, and it acks accepted without a second vendor call. The
// in-process memo below collapses the common case one step earlier still.
//
// Error posture matches the bridge's: a returned error is transient (the bridge
// Naks with delay and re-drives on the same key), while a terminal OutcomeFailed
// is a definitive verdict the bridge records and never retries. A busy runner is
// transient by definition; an invalid-ack is a malformed request this adapter
// built, so it is terminal and visible rather than an infinite Nak loop.
func (a *CapabilityAuthor) Execute(ctx context.Context, req Request) (Dispatch, error) {
	ref := req.IdempotencyKey
	if !wire.ValidRef(ref) {
		return terminalFailure(fmt.Sprintf(
			"capabilityAuthor: idempotencyKey %q cannot be a model-runner ref (the claim handle must be a bare KV-key token)", ref)), nil
	}
	if a.episode(ref) != nil {
		// This process already spent the call for this handle. A redelivered
		// event costs nothing further: the same ref goes back, and the poll
		// chain that is already armed resolves it.
		return Dispatch{Disposition: Pending, Ref: ref}, nil
	}

	intent := capIntent(strings.TrimSpace(req.Params["intent"]))
	if intent == "" {
		return terminalFailure("capabilityAuthor: the authoring request carries no intent; nothing to author"), nil
	}

	// A catalog that is empty or unreadable is the "capability-author not
	// provisioned" gap, and redelivery cannot fix it: the Weaver gap is already
	// closed by the claim, and the bridge arms its CallDeadline only on a
	// Pending outcome, so returning a transient error here would hang the
	// request invisibly forever. Fail terminally and visibly instead. A busy or
	// unreachable RUNNER stays transient below — that self-resolves when the
	// fleet returns, and the JetStream event preserves the work.
	catalog, err := a.readCatalog(ctx)
	if err != nil {
		return terminalFailure("capabilityAuthor: authoring context unavailable (" + err.Error() +
			") — check that capability-author is installed (make install-ai) and its context lens is projecting"), nil
	}

	ep := &authoringEpisode{
		intent:      intent,
		model:       req.Params["model"],
		system:      capabilityAuthorSystemPrompt,
		prompt:      authoringPrompt(intent, catalog.serialized, catalog.truncated),
		catalogHash: catalog.hash,
	}
	ep.promptHash = promptDigest(ep.system, ep.prompt)

	if err := a.dispatchAttempt(ctx, ref, ep.model, ep.system, ep.prompt); err != nil {
		if errors.Is(err, wire.ErrInvalid) {
			return terminalFailure("capabilityAuthor: the model runner rejected the authoring request as malformed: " + err.Error()), nil
		}
		return Dispatch{}, err
	}
	a.remember(ref, ep)
	return Dispatch{Disposition: Pending, Ref: ref}, nil
}

// Poll resolves an authoring request by reading the model-runner's result
// bucket. It is the whole state machine: nothing about where an episode stands
// is remembered between calls.
//
// Attempt-state lifetime — created / carried / terminal / reaped:
//
//	model-results.<ref>  — attempt 1's outcome.
//	  created   the runner CAS-creates the in-flight marker, before the vendor call
//	  carried   every Poll and every bridge restart read it: it is KV, not memory
//	  terminal  completed | refused | failed
//	  reaped    the bucket's per-key TTL (in-flight: 2x the vendor timeout, so a
//	            runner killed mid-call never strands its ref; terminal: 7d)
//
//	model-results.<ref>-r2  — attempt 2's outcome. Identical lifetime; created only
//	  if this adapter dispatches the one retry it is allowed.
//
//	episodes[<ref>]  — this process's prompt inputs, NOT attempt state.
//	  created   Execute, in whichever bridge instance assembled the prompt
//	  carried   nothing: not another instance, not a restart
//	  terminal  settled at the first terminal answer — the prompt strings are
//	            dropped there, the provenance hashes and the intent are kept so a
//	            redelivered fired-poll re-files identical provenance
//	  reaped    process exit
//
// Nothing is ever deleted from the result bucket: the runner is its only writer
// and the reader only reads, so a consumed ref is reaped by TTL rather than by a
// second party who could otherwise erase an outcome mid-episode.
//
// The budget is at most TWO ANSWERED calls per authoring request — two refs,
// and a ref costs at most one vendor call because the runner CAS-claims it. The
// second is shared between the two reasons another call is worth spending: a
// draft that failed deterministic validation (a correction pass carrying the
// errors), and a vendor failure that produced no draft at all (a plain retry).
// Whichever consumes the `-r2` slot first, the other cannot.
//
// A call that records NOTHING is outside that count and is re-driven on its own
// ref: a runner killed between claiming a ref and recording its outcome leaves
// the marker to expire, and the work has to be re-offered or the request can
// never finish. Repeated deaths therefore mean repeated attempts on the same
// ref — bounded not here but by the fleet's own daily call cap, which is where a
// spend ceiling belongs.
//
// A cold episode — this process did not run Execute for the ref (a bridge
// restart mid-episode) — can still finish an episode whose answer has landed:
// the verdict comes from the model's own output, which is in KV. It cannot
// re-dispatch, because the prompt is not recoverable from the result bucket; the
// answer there is a terminal, visible failure rather than a silent stall.
func (a *CapabilityAuthor) Poll(ctx context.Context, ref string) (Dispatch, error) {
	first, err := a.readResult(ctx, ref)
	if err != nil {
		return Dispatch{}, err
	}
	repair := ref + repairRefSuffix

	switch {
	case first == nil:
		// The in-flight marker expired without a terminal result: the runner
		// that claimed this ref died mid-call. Re-dispatch attempt 1 — the
		// runner's CAS keeps that once-only if another instance got there first.
		ep := a.prompts(ref)
		if ep == nil {
			a.settle(ref)
			return terminalFailure(
				"capabilityAuthor: the model call left no result and this bridge no longer holds the prompt (restarted mid-request); re-request the authoring"), nil
		}
		if err := a.dispatchAttempt(ctx, ref, ep.model, ep.system, ep.prompt); err != nil {
			if errors.Is(err, wire.ErrInvalid) {
				return terminalFailure("capabilityAuthor: the model runner rejected the re-dispatched authoring request as malformed: " + err.Error()), nil
			}
			return Dispatch{}, err
		}
		return Dispatch{Disposition: Pending, Ref: ref}, nil

	case first.State == wire.StateInflight:
		return Dispatch{Disposition: Pending, Ref: ref}, nil

	case first.State == wire.StateRefused:
		// A refusal is the model declining on policy grounds: terminal, and
		// never a proposal. Retrying it unchanged would decline identically.
		a.settle(ref)
		return terminalFailure(refusalDetail(*first)), nil

	case first.State == wire.StateFailed:
		return a.afterVendorFailure(ctx, ref, repair, *first)

	case first.State == wire.StateCompleted:
		return a.afterDraft(ctx, ref, repair, *first)

	default:
		return Dispatch{}, fmt.Errorf("capabilityAuthor: model result %s carries unknown state %q", ref, first.State)
	}
}

// afterDraft resolves an authoring request whose first model call produced a
// draft: validate it, file it when it passes, and otherwise spend the one repair
// call on a correction pass carrying the validator's own errors.
func (a *CapabilityAuthor) afterDraft(ctx context.Context, ref, repair string, first wire.Result) (Dispatch, error) {
	ep := a.episode(ref)
	// The lens index resolves the model's canonicalName choice to the installed
	// lens's NanoID (assembly needs it whether the episode is warm or cold). A
	// read failure here is transient — the poll re-arms and CallDeadline is the
	// backstop — but it never reaches an empty-map special case: an empty index
	// simply resolves nothing, and the draft records invalid.
	lensIndex, err := a.lensIndex(ctx)
	if err != nil {
		return Dispatch{}, err
	}
	draft := a.assess(first, ep, lensIndex)
	if draft.state == ValidationStateValid {
		return a.file(ref, draft, first.Model, promptHashOf(ep, false), catalogHashOf(ep))
	}

	second, err := a.readResult(ctx, repair)
	if err != nil {
		return Dispatch{}, err
	}
	switch {
	case second == nil:
		live := a.prompts(ref)
		if live == nil {
			// No prompt to correct against; the draft on hand is the final
			// answer, filed with its computed invalid verdict so the operator
			// sees what was proposed and why it failed.
			return a.file(ref, draft, first.Model, promptHashOf(ep, false), catalogHashOf(ep))
		}
		prompt := correctionPrompt(live.prompt, draft.content, draft.report)
		if err := a.dispatchAttempt(ctx, repair, live.model, live.system, prompt); err != nil {
			// Any dispatch failure here — malformed request (ErrInvalid), a busy
			// fleet, or no responders — leaves us holding a validated invalid
			// draft. Filing it (invalid, visible) lets the operator fix it in the
			// Studio, which beats blocking the whole request on a second model
			// call that a saturated or absent fleet may never accept.
			return a.file(ref, draft, first.Model, promptHashOf(ep, false), catalogHashOf(ep))
		}
		a.recordRepair(ref, promptDigest(live.system, prompt))
		return Dispatch{Disposition: Pending, Ref: ref}, nil

	case second.State == wire.StateInflight:
		return Dispatch{Disposition: Pending, Ref: ref}, nil

	case second.State == wire.StateCompleted:
		// The budget is spent either way: the correction pass's verdict — valid
		// or still invalid — is the final one.
		corrected := a.assess(*second, ep, lensIndex)
		return a.file(ref, corrected, second.Model, promptHashOf(ep, true), catalogHashOf(ep))

	default:
		// The correction pass was refused or failed. The first draft is still
		// the best answer available, filed with its invalid verdict rather than
		// discarded — the operator can fix it in the Studio.
		return a.file(ref, draft, first.Model, promptHashOf(ep, false), catalogHashOf(ep))
	}
}

// afterVendorFailure resolves an authoring request whose first model call failed
// at the vendor (transport, timeout, a truncated turn). No draft exists, so the
// repair slot is spent on a plain retry of the same prompt.
func (a *CapabilityAuthor) afterVendorFailure(ctx context.Context, ref, repair string, first wire.Result) (Dispatch, error) {
	ep := a.episode(ref)

	second, err := a.readResult(ctx, repair)
	if err != nil {
		return Dispatch{}, err
	}
	switch {
	case second == nil:
		live := a.prompts(ref)
		if live == nil {
			a.settle(ref)
			return terminalFailure("capabilityAuthor: the model call failed (" + first.Error +
				") and this bridge no longer holds the prompt to retry it (restarted mid-request); re-request the authoring"), nil
		}
		if err := a.dispatchAttempt(ctx, repair, live.model, live.system, live.prompt); err != nil {
			if errors.Is(err, wire.ErrInvalid) {
				return terminalFailure("capabilityAuthor: the model call failed (" + first.Error +
					") and the retry was rejected as malformed: " + err.Error()), nil
			}
			return Dispatch{}, err
		}
		a.recordRepair(ref, live.promptHash)
		return Dispatch{Disposition: Pending, Ref: ref}, nil

	case second.State == wire.StateInflight:
		return Dispatch{Disposition: Pending, Ref: ref}, nil

	case second.State == wire.StateCompleted:
		lensIndex, err := a.lensIndex(ctx)
		if err != nil {
			return Dispatch{}, err
		}
		corrected := a.assess(*second, ep, lensIndex)
		return a.file(ref, corrected, second.Model, promptHashOf(ep, true), catalogHashOf(ep))

	case second.State == wire.StateRefused:
		a.settle(ref)
		return terminalFailure(refusalDetail(*second)), nil

	default:
		a.settle(ref)
		return terminalFailure("capabilityAuthor: both model calls failed (" + first.Error + "; " + second.Error + ")"), nil
	}
}

// file builds the proposal from a validated draft and returns it as the
// adapter's terminal Detail. The Status is Completed whether the verdict is
// valid or invalid: the reasoning ran to an answer, and the record path stores
// an invalid artifact as a visible, auditable proposal rather than discarding it.
//
// handle is the claim handle (the Poll ref): it names the fresh package the
// target installs into, so a redelivery files the identical, appliable target.
func (a *CapabilityAuthor) file(handle string, d authoredDraft, model, promptHash, catalogHash string) (Dispatch, error) {
	proposal := CapabilityAuthorProposal{
		Kind:    CapabilityAuthorKind,
		Content: string(d.content),
		Target: CapabilityAuthorTarget{
			Mode:        capabilityAuthorTargetMode,
			PackageName: authoredPackageName(handle),
			NewVersion:  authoredPackageVersion,
		},
		Rationale:  d.rationale,
		Confidence: d.confidence,
		Validation: CapabilityAuthorValidation{State: d.state, Report: d.report},
		Provenance: CapabilityAuthorProvenance{
			Model:       model,
			PromptHash:  promptHash,
			CatalogHash: catalogHash,
			ReasonedAt:  a.now().UTC().Format(time.RFC3339),
		},
	}
	detail, err := proposal.Encode()
	if err != nil {
		return Dispatch{}, fmt.Errorf("capabilityAuthor: encode proposal for %s: %w", handle, err)
	}
	a.settle(handle)
	return Dispatch{Disposition: Resolved, Result: Result{Status: OutcomeCompleted, Detail: detail}}, nil
}

// authoredPackageName derives the fresh package name an authoring request's
// target installs into. Unique per handle (so newPackage never collides), and
// never platform-protected (the prefix is under no protected name).
func authoredPackageName(handle string) string {
	return authoredPackagePrefix + handle
}

// capIntent bounds the operator intent so the assembled request stays under the
// NATS payload wall. Truncation is rune-safe and only trims abuse — a
// plain-language intent sits far below the cap.
func capIntent(intent string) string {
	if len(intent) <= maxIntentBytes {
		return intent
	}
	cut := intent[:maxIntentBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// dispatchAttempt submits one model call. A non-nil error is the caller's
// branch point: wire.ErrBusy and any transport error are transient, wire.ErrInvalid
// is terminal.
func (a *CapabilityAuthor) dispatchAttempt(ctx context.Context, ref, model, system, prompt string) error {
	ack, err := a.runner.Dispatch(ctx, wire.Request{
		Ref:    ref,
		Model:  model,
		System: system,
		Prompt: prompt,
		Tool:   capabilityAuthorTool(),
	})
	if err != nil {
		return fmt.Errorf("capabilityAuthor: dispatch %s to the model runner: %w", ref, err)
	}
	if err := ack.Err(); err != nil {
		return fmt.Errorf("capabilityAuthor: model runner did not accept %s (%s): %w", ref, ack.Reason, err)
	}
	return nil
}

// readResult reads one attempt's result row. A nil Result with a nil error means
// the key is absent — a state of the protocol, not a failure.
func (a *CapabilityAuthor) readResult(ctx context.Context, ref string) (*wire.Result, error) {
	entry, err := a.conn.KVGet(ctx, wire.ResultsBucket, ref)
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("capabilityAuthor: read model result %s: %w", ref, err)
	}
	var res wire.Result
	if err := json.Unmarshal(entry.Value, &res); err != nil {
		return nil, fmt.Errorf("capabilityAuthor: decode model result %s: %w", ref, err)
	}
	return &res, nil
}

// terminalFailure is the adapter's definitive business rejection: err == nil, so
// the bridge records it and never retries.
func terminalFailure(detail string) Dispatch {
	return Dispatch{Disposition: Resolved, Result: Result{Status: OutcomeFailed, Detail: detail}}
}

// refusalDetail renders a vendor policy refusal for the failed replyOp.
func refusalDetail(res wire.Result) string {
	if res.RefusalCategory != "" {
		return "capabilityAuthor: the model declined to propose (refusal: " + res.RefusalCategory + ")"
	}
	return "capabilityAuthor: the model declined to propose (refusal)"
}

// --- episode memo -----------------------------------------------------------

// episode returns the record this process holds for ref, settled or not — the
// provenance source, which outlives the episode's resolution.
func (a *CapabilityAuthor) episode(ref string) *authoringEpisode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.episodes[ref]
}

// prompts returns the episode only while it is still a usable prompt source: nil
// once it has settled (the prompts are dropped) or when this process never
// assembled it.
func (a *CapabilityAuthor) prompts(ref string) *authoringEpisode {
	a.mu.Lock()
	defer a.mu.Unlock()
	ep, ok := a.episodes[ref]
	if !ok || ep.settled {
		return nil
	}
	return ep
}

func (a *CapabilityAuthor) remember(ref string, ep *authoringEpisode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.episodes[ref] = ep
}

// recordRepair stores the hash of the second call's prompt, so a proposal filed
// from that call's answer names the prompt that actually produced it.
func (a *CapabilityAuthor) recordRepair(ref, hash string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ep, ok := a.episodes[ref]; ok {
		ep.repairHash = hash
	}
}

// settle drops an episode's prompt strings once it has reached a terminal
// answer, retaining only the provenance hashes so a redelivered fired-poll
// re-files the same proposal with the same provenance rather than a blank one.
func (a *CapabilityAuthor) settle(ref string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ep, ok := a.episodes[ref]
	if !ok {
		return
	}
	ep.system = ""
	ep.prompt = ""
	ep.settled = true
}

// promptHashOf returns the recorded artifact's prompt hash: the correction
// pass's when the artifact came from it, otherwise the first call's. A cold
// episode has neither, and an absent hash is left absent rather than filled with
// a hash of a prompt this process never sent.
func promptHashOf(ep *authoringEpisode, repaired bool) string {
	if ep == nil {
		return ""
	}
	if repaired {
		return ep.repairHash
	}
	return ep.promptHash
}

func catalogHashOf(ep *authoringEpisode) string {
	if ep == nil {
		return ""
	}
	return ep.catalogHash
}

// --- the model's answer -----------------------------------------------------

// modelArtifact is the tool input the model is forced to answer through — the
// adapter's output contract, mirrored by capabilityAuthorTool's schema.
type modelArtifact struct {
	Kind       string             `json:"kind"`
	Content    modelTargetContent `json:"content"`
	Rationale  string             `json:"rationale"`
	Confidence float64            `json:"confidence"`
}

// modelTargetContent is the weaver-target body the model proposes. Its gaps are
// a LIST rather than the `{missing_<gap>: action}` object the artifact itself
// carries: a strict tool schema closes every object it declares, which leaves no
// way to describe a map whose keys are chosen by the model. The assembler below
// folds the list into the artifact's object form.
type modelTargetContent struct {
	TargetID    string           `json:"targetId"`
	LensRef     string           `json:"lensRef"`
	Description string           `json:"description"`
	Gaps        []modelGapAction `json:"gaps"`
}

// modelGapAction is one gap's remediation, field-for-field the JSON shape a
// weaverTarget artifact's gap entry carries plus the gapColumn that keys it.
type modelGapAction struct {
	GapColumn     string       `json:"gapColumn"`
	Action        string       `json:"action"`
	Pattern       string       `json:"pattern"`
	Subject       string       `json:"subject"`
	Adapter       string       `json:"adapter"`
	Operation     string       `json:"operation"`
	Assignee      string       `json:"assignee"`
	Target        string       `json:"target"`
	Params        []modelParam `json:"params"`
	Reads         []string     `json:"reads"`
	IssueCode     string       `json:"issueCode"`
	IssueSeverity string       `json:"issueSeverity"`
}

// modelParam is one dispatch param. Same reason as the gaps list: a param map's
// keys are the model's to choose, which a closed schema cannot express.
type modelParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// authoredDraft is one model answer after the adapter has assembled it into an
// artifact and had it validated: the exact bytes that would be recorded, and the
// verdict on them.
type authoredDraft struct {
	content    []byte
	rationale  string
	confidence float64
	state      string
	report     string
}

// assess turns one completed model result into the artifact that will be
// recorded and the verdict on it.
//
// The recorded content is the adapter's own assembly of the model's structured
// output, never a passthrough: what the validator sees is byte-for-byte what is
// recorded and what would install. The assembly emits only the fields a
// weaverTarget artifact enables, so an out-of-scope posture cannot ride along
// even if a future vendor stops honouring the closed schema.
//
// The verdict comes from the injected validator. Three failure classes it
// cannot see — output that does not decode at all, structural defects that make
// an assembled artifact not represent the answer (a gap with no column, a
// duplicate column), and a lensRef that record-time validation lets through but
// install rejects — are computed here and reported alongside it. Neither is the
// model's own claim about its work: nothing in the output is ever taken as a
// verdict.
//
// lensIndex resolves the model's canonicalName lens choice to the installed
// lens's NanoID (assembly files the NanoID, the only form the apply path
// resolves for a single-artifact target).
func (a *CapabilityAuthor) assess(res wire.Result, ep *authoringEpisode, lensIndex map[string]string) authoredDraft {
	var art modelArtifact
	if err := json.Unmarshal(res.Output, &art); err != nil {
		return authoredDraft{
			content: append([]byte(nil), res.Output...),
			state:   ValidationStateInvalid,
			report:  "the model's output did not decode as a capability proposal: " + err.Error(),
		}
	}

	var problems []string
	if art.Kind != CapabilityAuthorKind {
		problems = append(problems, fmt.Sprintf("the model answered with kind %q; this adapter authors only %q", art.Kind, CapabilityAuthorKind))
	}

	fallback := ""
	if ep != nil {
		fallback = distill(ep.intent)
	}
	if fallback == "" {
		fallback = distill(art.Rationale)
	}
	content, assemblyProblems := assembleTargetContent(art.Content, fallback, lensIndex)
	problems = append(problems, assemblyProblems...)

	state, report := a.validate(CapabilityAuthorKind, content)
	if report != "" {
		problems = append(problems, report)
	}
	if state != ValidationStateValid {
		state = ValidationStateInvalid
	}
	if len(problems) > 0 {
		state = ValidationStateInvalid
	}

	return authoredDraft{
		content:    content,
		rationale:  art.Rationale,
		confidence: art.Confidence,
		state:      state,
		report:     strings.Join(problems, "; "),
	}
}

// assembleTargetContent folds the model's structured answer into a weaverTarget
// artifact's content JSON: the model's canonicalName lens choice resolves to the
// installed lens's NanoID, the gaps list becomes the `missing_<gap>` keyed
// object, each gap's param list becomes an object, and every field the model
// left blank is omitted so an empty string never reads as an authored value.
//
// The returned problems are defects that would make the assembled artifact
// misrepresent the answer or fail at install: an unresolvable lens, a gap with
// no column, a duplicate column, a nameless param. The offending entry is
// dropped rather than guessed at, and the draft records invalid.
func assembleTargetContent(c modelTargetContent, fallbackDescription string, lensIndex map[string]string) ([]byte, []string) {
	var problems []string

	// The model names a lens by its catalog canonicalName; the artifact must
	// carry the installed lens's NanoID (resolveLensRef only passes a NanoID
	// through for a single-artifact target, and record-time validation does NOT
	// check lensRef shape — a dotted/wildcard/unicode value would record "valid"
	// and fail only at install). So resolve here, and never pass a model-supplied
	// string through unresolved.
	lensRef := ""
	named := strings.TrimSpace(c.LensRef)
	switch id, ok := lensIndex[named]; {
	case named == "":
		problems = append(problems, "no lens named — bind the target to a lens from the catalog by its canonicalName")
	case ok && substrate.IsValidNanoID(id):
		lensRef = id
	default:
		problems = append(problems, fmt.Sprintf("lens %q is not in the catalog — bind to an installed lens by its canonicalName", named))
	}

	// A model-supplied description is capped exactly like the intent-derived
	// fallback (both run through distill); neither reaches the roster unbounded.
	description := distill(c.Description)
	if description == "" {
		description = fallbackDescription
	}

	gaps := make(map[string]any, len(c.Gaps))
	for i, g := range c.Gaps {
		col := strings.TrimSpace(g.GapColumn)
		if col == "" {
			problems = append(problems, fmt.Sprintf("gap entry %d names no gap column", i))
			continue
		}
		if _, dup := gaps[col]; dup {
			problems = append(problems, fmt.Sprintf("gap column %q is declared more than once", col))
			continue
		}
		body := map[string]any{"action": strings.TrimSpace(g.Action)}
		for field, value := range map[string]string{
			"pattern":       g.Pattern,
			"subject":       g.Subject,
			"adapter":       g.Adapter,
			"operation":     g.Operation,
			"assignee":      g.Assignee,
			"target":        g.Target,
			"issueCode":     g.IssueCode,
			"issueSeverity": g.IssueSeverity,
		} {
			if v := strings.TrimSpace(value); v != "" {
				body[field] = v
			}
		}
		if params, paramProblems := assembleParams(col, g.Params); len(params) > 0 || len(paramProblems) > 0 {
			problems = append(problems, paramProblems...)
			if len(params) > 0 {
				body["params"] = params
			}
		}
		if reads := trimmedNonEmpty(g.Reads); len(reads) > 0 {
			body["reads"] = reads
		}
		gaps[col] = body
	}

	content := map[string]any{
		"targetId": strings.TrimSpace(c.TargetID),
		"lensRef":  lensRef,
		"gaps":     gaps,
	}
	if description != "" {
		content["description"] = description
	}

	// json.Marshal sorts map keys, so the same answer always assembles to the
	// same bytes — the artifact a reviewer reads and the artifact the validator
	// judged are one string, stable across re-files.
	body, err := json.Marshal(content)
	if err != nil {
		return []byte("{}"), append(problems, "the proposed target could not be encoded: "+err.Error())
	}
	return body, problems
}

// assembleParams folds a gap's param list into the artifact's param object.
func assembleParams(col string, list []modelParam) (map[string]string, []string) {
	if len(list) == 0 {
		return nil, nil
	}
	var problems []string
	params := make(map[string]string, len(list))
	for _, p := range list {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			problems = append(problems, fmt.Sprintf("gap column %q declares a param with no name", col))
			continue
		}
		if _, dup := params[key]; dup {
			problems = append(problems, fmt.Sprintf("gap column %q declares param %q more than once", col, key))
			continue
		}
		params[key] = p.Value
	}
	return params, problems
}

// trimmedNonEmpty drops blank entries from a string list.
func trimmedNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// distill reduces free prose to the one-or-two-sentence description a weaver
// target carries on the roster: the opening sentences of the source, whitespace
// normalised, then bounded. A source with no sentence break at all is truncated
// on a word boundary rather than mid-word.
func distill(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if s == "" {
		return ""
	}
	sentences := 0
	for i, r := range s {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		sentences++
		if sentences == maxDistilledSentences {
			s = s[:i+1]
			break
		}
	}
	if len(s) <= maxDistilledDescription {
		return s
	}
	// Hard cap with a trailing ellipsis, reserving room for it so the result
	// never exceeds maxDistilledDescription, and never splitting a UTF-8 rune.
	const ellipsis = "…"
	cut := s[:maxDistilledDescription-len(ellipsis)]
	if idx := strings.LastIndexByte(cut, ' '); idx > 0 {
		cut = cut[:idx]
	} else {
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
	}
	return strings.TrimSpace(cut) + ellipsis
}
