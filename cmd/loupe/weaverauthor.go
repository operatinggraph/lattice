package main

import (
	"encoding/json"
	"errors"
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

// weaverAuthorCheckRequest is POST /api/weaver/author/check's body: the two
// artifact contents a target draft carries (§6 step 1 — "the paired
// violation-Lens cypher"), taken directly as the pkgmgr types so this handler
// can pass them to pkgmgr.ValidateCapabilityArtifact without a field-mapping
// step of its own duplicating what draftTargetBody already does for the
// Go-side checker.
type weaverAuthorCheckRequest struct {
	Target pkgmgr.WeaverTargetArtifactContent `json:"target"`
	Lens   pkgmgr.LensArtifactContent         `json:"lens"`
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
// against the draft"), plus the two artifacts' pkgmgr verdicts — the verdict
// of record for whether the draft could ever be proposed at all.
type weaverAuthorCheckResponse struct {
	LaneChecks       []weaverCheck          `json:"laneChecks"`
	Interference     []weaverInterference   `json:"interference"`
	OpCoverage       weaverOpCoverage       `json:"opCoverage"`
	TargetValidation weaverAuthorValidation `json:"targetValidation"`
	LensValidation   weaverAuthorValidation `json:"lensValidation"`
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

	lensContent, err := json.Marshal(req.Lens)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal lens content: "+err.Error())
		return
	}
	lensReport, err := pkgmgr.ValidateCapabilityArtifact("lens", lensContent, loupeCypherParser{}, nil, nil)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "lens artifact: "+err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, weaverAuthorCheckResponse{
		LaneChecks:       laneChecks,
		Interference:     interference,
		OpCoverage:       opCoverage,
		TargetValidation: weaverAuthorValidation{Valid: targetReport.Valid, Errors: nonNilStrings(targetReport.Errors)},
		LensValidation:   weaverAuthorValidation{Valid: lensReport.Valid, Errors: nonNilStrings(lensReport.Errors)},
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

// weaverAuthorPropose implements POST /api/weaver/author/propose (§6 step
// 4). Each artifact becomes its own SubmitCapabilityProposal op — the DDL
// mints the whole proposal vertex from one op, reads nothing, and rejects
// synchronously on a malformed payload (submit_test.go), so the artifact's
// SHAPE is not re-checked here; the studio already ran Check before enabling
// the button, and the op records whatever validation verdict this request
// carries. The one thing this handler does refuse is a weaverTarget with no
// description (proposedTargetDescription) — a queue-readability rule of
// Loupe's own, not a platform one.
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
