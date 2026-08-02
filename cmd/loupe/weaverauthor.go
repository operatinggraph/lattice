package main

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
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
// No propose step here — that is F25.3b, gated on SubmitCapabilityProposal
// (packages/capability-author, shipped 6d2614fb). This fire never submits an
// op, so it needs no capability check beyond the console-wide requireOperator
// gate every route already sits behind (F15), and nothing here writes
// anything a demo/read-only posture must specially suppress: the server side
// is GET/analysis-shaped work behind a POST body, which the client hides by
// its own affordance-suppression convention (data-demo-hide on the Author
// nav link) and which demoReadOnly's method default-deny would 403 anyway.

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
