package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// F25.3a — Author: draft, check, export (weaver-target-studio-design.md §6,
// steps 1-3). The canvas edits build a capability artifact — the same
// pkgmgr.WeaverTargetArtifactContent / pkgmgr.LensArtifactContent shapes the
// ratified AI-authored-capabilities lane validates and F16 reviews/applies —
// so a human operator's draft can never carry a shape that lane wouldn't
// already accept. Drafts are browser-local (no new platform state); this
// endpoint is the one server round-trip a draft needs, to run checks that
// only Go code can compute (F25.2's V1 lane pass + V3 interference, and the
// pkgmgr validators). Export never touches the server at all — the browser
// builds the downloadable bundle from the same fields.
//
// F25.3b adds the propose step below: POST /api/weaver/author/propose is the
// one server round-trip Propose needs beyond Check, because a proposal id is
// a Lattice NanoID (internal/substrate/keys' 58-char custom alphabet) and
// only Go code mints one. Both this endpoint and weaverAuthorCheck are POSTs
// carrying no read/write of Loupe's own state, but propose DOES submit a
// platform-mutating op (SubmitCapabilityProposal) through the Gateway, so —
// unlike Check — it needs the console-wide requireOperator gate to have
// resolved a real operator token; demoReadOnly's method default-deny still
// blocks it under the demo posture, and the client additionally hides the
// button via the same data-demo-hide convention as Check/Export.

// weaverAuthorCheckRequest is POST /api/weaver/author/check's body: the
// target draft content always, plus the paired violation-lens content (§6
// step 1) ONLY when the operator is authoring a NEW lens rather than binding
// the target to an already-installed one (Studio apply-path fix — a
// target-only draft has nothing lens-shaped to check). Lens is a pointer so
// an absent key decodes to nil rather than an indistinguishable zero-valued
// LensArtifactContent{} — the same optionality the propose bundle's
// artifacts list carries (one entry vs two). Taken directly as the pkgmgr
// types so this handler can pass them to pkgmgr.ValidateCapabilityArtifact
// without a field-mapping step of its own duplicating what draftTargetBody
// already does for the Go-side checker.
type weaverAuthorCheckRequest struct {
	Target pkgmgr.WeaverTargetArtifactContent `json:"target"`
	Lens   *pkgmgr.LensArtifactContent        `json:"lens,omitempty"`
}

// weaverAuthorValidation mirrors pkgmgr.ArtifactValidationReport for the wire
// (that type's fields are unexported-JSON-tag-free; this pins the shape the
// UI reads).
type weaverAuthorValidation struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
}

// weaverAuthorCheckResponse is the Check stage's verdict: F25.2's V1 lane
// pass + V3 interference run against the draft (joined into the live
// installed corpus, exactly as §6 step 2 specifies — "F25.2's V1/V3 run
// against the draft"), plus the submitted artifacts' pkgmgr verdicts — the
// verdict of record for whether the draft could ever be proposed at all.
// LensValidation is a pointer, omitted entirely when the request carried no
// lens (a target-only draft) — never a fabricated {valid:false} that would
// read as "the lens is invalid" for a lens that was never submitted.
type weaverAuthorCheckResponse struct {
	LaneChecks       []weaverCheck           `json:"laneChecks"`
	Interference     []weaverInterference    `json:"interference"`
	OpCoverage       weaverOpCoverage        `json:"opCoverage"`
	TargetValidation weaverAuthorValidation  `json:"targetValidation"`
	LensValidation   *weaverAuthorValidation `json:"lensValidation,omitempty"`
}

// weaverAuthorDraftKey is the sentinel id a draft is inserted under for V3's
// interference join against the live installed corpus. A real installed
// target using this exact id would collide (its interference findings would
// be misattributed to "the draft") — accepted as a vanishingly unlikely
// naming coincidence, the same class of edge case F25.1's first-writer-wins
// duplicate-targetId handling already accepts (weaver.go's buildWeaverMetaIndex).
const weaverAuthorDraftKey = "__draft"

// draftTargetBody converts the restricted capability-artifact content into
// the full §10.8 shape F25.2's checkers read. Field-by-field, not a type
// conversion — mirrors pkgmgr's own weaverTargetArtifactDefinition, since
// WeaverTargetArtifactContent is deliberately narrower than weaverTargetBody
// (no mode/augur/admission; a gap has no candidates/goal/actions catalog —
// the studio's v1 scope guard, design §6).
func draftTargetBody(t pkgmgr.WeaverTargetArtifactContent) *weaverTargetBody {
	gaps := make(map[string]weaverGapAction, len(t.Gaps))
	for col, ga := range t.Gaps {
		gaps[col] = weaverGapAction{weaverActionContract: weaverActionContract{
			Action:        ga.Action,
			Pattern:       ga.Pattern,
			Subject:       ga.Subject,
			Adapter:       ga.Adapter,
			Operation:     ga.Operation,
			Assignee:      ga.Assignee,
			Target:        ga.Target,
			Params:        ga.Params,
			Reads:         ga.Reads,
			IssueCode:     ga.IssueCode,
			IssueSeverity: ga.IssueSeverity,
		}}
	}
	return &weaverTargetBody{TargetID: t.TargetID, LensRef: t.LensRef, Gaps: gaps}
}

// weaverAuthorCheck implements POST /api/weaver/author/check. Read-only
// throughout: it computes verdicts over the posted draft plus the live
// installed corpus, and never writes anything — no proposal, no op, no
// platform state (§6: "Drafts are browser-local until proposed — no new
// platform state").
func (s *server) weaverAuthorCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	var req weaverAuthorCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}

	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	body := draftTargetBody(req.Target)
	laneChecks := computeLaneChecks(body)
	if laneChecks == nil {
		laneChecks = []weaverCheck{}
	}

	readers, err := s.weaverCoreReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv metas: "+err.Error())
		return
	}
	index := buildWeaverMetaIndex(readers.metaKeys, readers.coreGet)
	opPaths := buildOpEffectsIndex(readers.metaKeys, readers.coreGet)

	merged := make(map[string]*weaverTargetBody, len(index.Bodies)+1)
	for id, b := range index.Bodies {
		merged[id] = b
	}
	merged[weaverAuthorDraftKey] = body

	var interference []weaverInterference
	for _, row := range computeInterference(merged, opPaths) {
		if containsTarget(row.Targets, weaverAuthorDraftKey) {
			interference = append(interference, row)
		}
	}
	if interference == nil {
		interference = []weaverInterference{}
	}

	opCoverage := computeOpCoverage(map[string]*weaverTargetBody{weaverAuthorDraftKey: body}, opPaths)

	targetContent, err := json.Marshal(req.Target)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal target content: "+err.Error())
		return
	}
	targetReport, err := pkgmgr.ValidateCapabilityArtifact("weaverTarget", targetContent, loupeCypherParser{}, nil, nil)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "target artifact: "+err.Error())
		return
	}

	var lensValidation *weaverAuthorValidation
	if req.Lens != nil {
		lensContent, err := json.Marshal(*req.Lens)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "marshal lens content: "+err.Error())
			return
		}
		lensReport, err := pkgmgr.ValidateCapabilityArtifact("lens", lensContent, loupeCypherParser{}, nil, nil)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "lens artifact: "+err.Error())
			return
		}
		lensValidation = &weaverAuthorValidation{Valid: lensReport.Valid, Errors: nonNilStrings(lensReport.Errors)}
	}

	s.writeJSON(w, http.StatusOK, weaverAuthorCheckResponse{
		LaneChecks:       laneChecks,
		Interference:     interference,
		OpCoverage:       opCoverage,
		TargetValidation: weaverAuthorValidation{Valid: targetReport.Valid, Errors: nonNilStrings(targetReport.Errors)},
		LensValidation:   lensValidation,
	})
}

func containsTarget(targets []string, id string) bool {
	i := sort.SearchStrings(targets, id)
	return i < len(targets) && targets[i] == id
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// weaverAuthorProposeArtifact is one artifact of the propose request — the
// exact shape logic/weaverauthor.js's exportBundle already builds (target
// and validation are carried through verbatim as the op's own wire shape
// takes them, design §6.4 point 1), minus proposalId: the browser never
// mints a Lattice NanoID itself, so the server mints one per artifact.
type weaverAuthorProposeArtifact struct {
	Kind       string          `json:"kind"`
	Content    string          `json:"content"`
	Target     json.RawMessage `json:"target"`
	Rationale  string          `json:"rationale"`
	Validation json.RawMessage `json:"validation"`
}

type weaverAuthorProposeRequest struct {
	Artifacts []weaverAuthorProposeArtifact `json:"artifacts"`
}

// weaverAuthorProposeResult is one artifact's outcome: a minted proposalId
// plus either the relayed reply (which itself may carry a Processor
// rejection — the caller must check reply.status, opRejected's job) or a
// transport-level Error. Two artifacts propose independently, so one
// artifact's failure never blocks or unwinds the other's.
type weaverAuthorProposeResult struct {
	Kind       string                    `json:"kind"`
	ProposalID string                    `json:"proposalId"`
	Reply      *processor.OperationReply `json:"reply,omitempty"`
	Error      string                    `json:"error,omitempty"`
}

type weaverAuthorProposeResponse struct {
	Results []weaverAuthorProposeResult `json:"results"`
}

// proposeIntentCap bounds the queue row label. The label sits inline in the
// review list, so a paragraph pasted into the description must not push the
// row's own state chips off the line.
const proposeIntentCap = 120

// proposedTargetDescription returns the prose on the bundle's weaverTarget
// artifact, refusing the submission when that artifact carries none.
//
// The requirement is Loupe's, not the platform's: pkgmgr keeps `description`
// optional (a package author may install a target without one, and the
// unknown-field scan admits it either way), while the studio holds a target
// entering the human review queue to a higher bar — a reviewer reading a
// proposal has nothing but a targetId and cypher otherwise. Check stays
// shape-only, so a draft can be validated long before it has prose.
func proposedTargetDescription(artifacts []weaverAuthorProposeArtifact) (string, error) {
	for _, a := range artifacts {
		if a.Kind != "weaverTarget" {
			continue
		}
		var wc pkgmgr.WeaverTargetArtifactContent
		if err := json.Unmarshal([]byte(a.Content), &wc); err != nil {
			return "", errors.New("weaverTarget artifact content is not decodable: " + err.Error())
		}
		desc := strings.TrimSpace(wc.Description)
		if desc == "" {
			return "", errors.New("weaverTarget artifact requires a description — say in plain language what this target ensures, so a reviewer can judge the proposal without reading its cypher")
		}
		return desc, nil
	}
	return "", nil
}

// proposeIntent derives the SubmitCapabilityProposal `intent` — the review
// queue's row label, which the op otherwise defaults to the whole rationale
// text (packages/capability-author/ddls.go). One label is computed for the
// bundle and stamped on every artifact in it, so the target and the lens it
// pairs with read as one submission in the queue rather than two unrelated
// rows. The target's own description is the label of record; a bundle with no
// weaverTarget artifact falls back to the first rationale present.
func proposeIntent(targetDescription string, artifacts []weaverAuthorProposeArtifact) string {
	source := targetDescription
	if source == "" {
		for _, a := range artifacts {
			if strings.TrimSpace(a.Rationale) != "" {
				source = a.Rationale
				break
			}
		}
	}
	return firstLineCapped(source, proposeIntentCap)
}

// firstLineCapped takes the first non-empty line of s and bounds it to limit
// RUNES (never bytes — a mid-rune cut would emit invalid UTF-8 into the
// proposal), marking an elision with an ellipsis that counts against the limit.
func firstLineCapped(s string, limit int) string {
	line := ""
	for _, candidate := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(candidate); line != "" {
			break
		}
	}
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// weaverTargetNeedsLensResolution reports whether artifacts contains a
// target-only weaverTarget (no paired "lens" artifact in the SAME bundle —
// the co-authored path is left alone, see resolveWeaverTargetLensRefs) whose
// content.lensRef is non-empty and not already a bare NanoID. That is the
// one shape that needs a live Core KV lookup before it can propose
// meaningfully; every other shape (a co-authored bundle, an empty LensRef, a
// LensRef already resolved — e.g. a hydrated-then-unedited "Load into
// Author" re-propose) needs no connection at all, so weaverAuthorPropose's
// dependency on NATS connectivity stays conditional rather than blocking
// every propose call on Core KV being reachable.
func weaverTargetNeedsLensResolution(artifacts []weaverAuthorProposeArtifact) bool {
	for _, a := range artifacts {
		if a.Kind == "lens" {
			return false
		}
	}
	for _, a := range artifacts {
		if a.Kind != "weaverTarget" {
			continue
		}
		var wc pkgmgr.WeaverTargetArtifactContent
		if json.Unmarshal([]byte(a.Content), &wc) != nil {
			continue // malformed content — resolveWeaverTargetLensRefs reports this properly
		}
		if wc.LensRef != "" && !substrate.IsValidNanoID(wc.LensRef) {
			return true
		}
	}
	return false
}

// resolveWeaverTargetLensRefs rewrites, in place, each weaverTarget
// artifact's content.lensRef from an authored canonicalName to the
// installed lens's bare NanoID — the resolution internal/pkgmgr/build.go's
// resolveLensRef needs at apply time. A Studio proposal applies as its OWN
// single-artifact Definition (weaverTargetArtifactDefinition never
// populates a Lenses list of its own), so resolveLensRef's in-Definition
// canonicalName match can never fire for anything this handler proposes —
// left unresolved, a canonicalName-valued LensRef only surfaces as a 409
// "matches no declared lens canonicalName and is not a valid NanoID" at the
// final apply click, with nothing here to have named the omission.
//
// Never called for a bundle that also proposes a "lens" artifact (the
// co-authored path, gated by weaverTargetNeedsLensResolution above): that
// lens has no NanoID yet — one is minted only when ITS OWN proposal is
// applied — so there is nothing installed yet to cross-resolve against; the
// operator applies the lens proposal first and re-proposes the target once
// its id is known (an accepted, unchanged limitation of that path). An
// empty LensRef passes through untouched (resolveLensRef's own "no lens
// binding declared" case), and a LensRef already shaped as a NanoID needs no
// lookup.
func (s *server) resolveWeaverTargetLensRefs(ctx context.Context, conn *substrate.Conn, artifacts []weaverAuthorProposeArtifact) error {
	readers, err := s.weaverCoreReaders(ctx, conn)
	if err != nil {
		return fmt.Errorf("list core-kv metas to resolve lensRef: %w", err)
	}
	index := buildLensCanonicalIndex(readers.metaKeys, readers.coreGet)

	for i := range artifacts {
		if artifacts[i].Kind != "weaverTarget" {
			continue
		}
		var wc pkgmgr.WeaverTargetArtifactContent
		if err := json.Unmarshal([]byte(artifacts[i].Content), &wc); err != nil {
			return fmt.Errorf("weaverTarget artifact content is not decodable: %w", err)
		}
		if wc.LensRef == "" || substrate.IsValidNanoID(wc.LensRef) {
			continue
		}
		resolved, ok := index[wc.LensRef]
		if !ok {
			return fmt.Errorf("weaverTarget %q binds lensRef %q, which matches no installed lens's canonicalName — install the lens first, or propose it alongside this target", wc.TargetID, wc.LensRef)
		}
		wc.LensRef = resolved
		rewritten, err := json.Marshal(wc)
		if err != nil {
			return fmt.Errorf("re-marshal weaverTarget content after resolving lensRef: %w", err)
		}
		artifacts[i].Content = string(rewritten)
	}
	return nil
}

// weaverAuthorPropose implements POST /api/weaver/author/propose (§6 step
// 4). Each artifact becomes its own SubmitCapabilityProposal op — the DDL
// mints the whole proposal vertex from one op, reads nothing, and rejects
// synchronously on a malformed payload (submit_test.go), so the artifact's
// SHAPE is not re-checked here; the studio already ran Check before enabling
// the button, and the op records whatever validation verdict this request
// carries. The two things this handler does refuse are a weaverTarget with
// no description (proposedTargetDescription — a queue-readability rule of
// Loupe's own, not a platform one) and, for a target-only bundle, a
// canonicalName-valued lensRef that resolves to no installed lens
// (resolveWeaverTargetLensRefs — the Studio apply-path fix, so an unappliable
// proposal is never minted).
//
// The requester is stamped by the Gateway from the caller's own verified
// Bearer token (never asserted by this handler), so the review queue's
// requestedBy always names the real operator regardless of what a payload
// might claim.
func (s *server) weaverAuthorPropose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	var req weaverAuthorProposeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	if len(req.Artifacts) == 0 {
		s.writeError(w, http.StatusBadRequest, "artifacts must not be empty")
		return
	}

	targetDescription, err := proposedTargetDescription(req.Artifacts)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	intent := proposeIntent(targetDescription, req.Artifacts)

	if weaverTargetNeedsLensResolution(req.Artifacts) {
		conn, ok := s.requireConn(w)
		if !ok {
			return
		}
		resolveCtx, resolveCancel := s.reqContext(r)
		err := s.resolveWeaverTargetLensRefs(resolveCtx, conn, req.Artifacts)
		resolveCancel()
		if err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	ctx, cancel := s.gatewaySubmitContext(r)
	defer cancel()

	results := make([]weaverAuthorProposeResult, 0, len(req.Artifacts))
	for _, a := range req.Artifacts {
		id, err := substrate.NewNanoID()
		if err != nil {
			results = append(results, weaverAuthorProposeResult{Kind: a.Kind, Error: "generate proposal id: " + err.Error()})
			continue
		}
		fields := map[string]any{
			"proposalId": id,
			"kind":       a.Kind,
			"content":    a.Content,
			"target":     a.Target,
			"rationale":  a.Rationale,
			"validation": a.Validation,
		}
		if intent != "" {
			fields["intent"] = intent
		}
		payload, err := json.Marshal(fields)
		if err != nil {
			results = append(results, weaverAuthorProposeResult{Kind: a.Kind, ProposalID: id, Error: "marshal payload: " + err.Error()})
			continue
		}
		reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
			OperationType: "SubmitCapabilityProposal",
			Lane:          string(processor.LaneDefault),
			Payload:       payload,
		})
		res := weaverAuthorProposeResult{Kind: a.Kind, ProposalID: id}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Reply = reply
		}
		results = append(results, res)
	}
	s.writeJSON(w, http.StatusOK, weaverAuthorProposeResponse{Results: results})
}

// weaverAuthorRequestBody is POST /api/weaver/author/request's body — the
// Describe panel's one field plus an optional pointer to bounded context,
// RequestCapabilityAuthoring's own wire shape (packages/capability-author/
// ddls.go) minus proposalId, which this handler mints the same way propose
// mints one per artifact.
type weaverAuthorRequestBody struct {
	Intent     string `json:"intent"`
	ContextRef string `json:"contextRef,omitempty"`
}

// weaverAuthorRequestResponse is the Describe panel's submit outcome: the
// minted proposalId plus the relayed reply (which may itself carry a
// Processor rejection — the caller checks reply.status, same as propose's
// per-artifact result).
type weaverAuthorRequestResponse struct {
	ProposalID string                    `json:"proposalId"`
	Reply      *processor.OperationReply `json:"reply,omitempty"`
}

// weaverAuthorRequest implements POST /api/weaver/author/request (design
// §3.4's Describe panel) — the AI-authoring counterpart to
// weaverAuthorPropose's human-authored SubmitCapabilityProposal. An operator
// types a plain-language intent; this handler mints a proposal id and relays
// RequestCapabilityAuthoring through the Gateway under the caller's own
// operator token. The bridge's async reasoning loop (the capabilityAuthor
// adapter) eventually records an AI-authored draft onto the same
// vtx.capabilityproposal.<id> vertex this mints, so the id handed back here
// is exactly what the review queue will show it under.
//
// RequestCapabilityAuthoring's DDL script performs no kv.Read of its own (it
// only mints the proposal vertex write-ahead) — like propose, this relay
// carries no Reads; the DDL declares its own posture.
func (s *server) weaverAuthorRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	var req weaverAuthorRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		s.writeError(w, http.StatusBadRequest, "intent must not be blank — describe what the Weaver should keep true")
		return
	}

	id, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate proposal id: "+err.Error())
		return
	}

	fields := map[string]any{"proposalId": id, "intent": intent}
	if contextRef := strings.TrimSpace(req.ContextRef); contextRef != "" {
		fields["contextRef"] = contextRef
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal payload: "+err.Error())
		return
	}

	ctx, cancel := s.gatewaySubmitContext(r)
	defer cancel()
	reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
		OperationType: "RequestCapabilityAuthoring",
		Lane:          string(processor.LaneDefault),
		Payload:       payload,
	})
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "submit op: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, weaverAuthorRequestResponse{ProposalID: id, Reply: reply})
}
