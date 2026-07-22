package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/packages/augur"
	capabilityauthor "github.com/operatinggraph/lattice/packages/capability-author"
)

// The AI review console (loupe-f16-ai-review-console-ux.md §3, §4): two tabs
// sharing one route shape. GET /api/review/capability(/<id>) lists/fetches
// the capabilityProposals read model; GET /api/review/augur(/<id>) does the
// same over augurProposals. Both are ordinary P5 reads off their own bucket
// (KVListKeys/KVGet, exactly like vault.go's shred fleet view) — no Core-KV
// scan. Augur's approve AND reject reuse the existing POST /api/op path
// (F16.3) — Augur's approve re-validates entirely server-side in the DDL
// script, so unlike capability it carries no client-computed validation
// payload. Capability's reject also reuses POST /api/op; its approve + apply
// (F16.2) get their own endpoints below because approve must re-validate the
// artifact server-side against the live catalog and apply is a two-Processor-
// commit install flow, not a single op relay.

// capabilityProposalCols is the on-the-wire shape of one capability-proposals
// bucket entry — field names mirror capabilityProposalsSpec's RETURN AS
// aliases (packages/capability-author/lenses.go) verbatim, so decoding is a
// direct json.Unmarshal with no field remapping. A row whose reasoning is
// still in flight (RecordCapabilityProposal hasn't run) projects with every
// field past claimedAt empty/zero — that is a valid row, not a decode
// failure; only a missing/empty Key marks a poison entry.
type capabilityProposalCols struct {
	Key                    string  `json:"key"`
	ProposalKey            string  `json:"proposalKey"`
	RequesterID            string  `json:"requesterId"`
	Intent                 string  `json:"intent"`
	ContextRef             string  `json:"contextRef"`
	ClaimedAt              string  `json:"claimedAt"`
	Kind                   string  `json:"kind"`
	Content                string  `json:"content"`
	TargetMode             string  `json:"targetMode"`
	TargetPackageName      string  `json:"targetPackageName"`
	TargetBaseVersion      string  `json:"targetBaseVersion"`
	TargetNewVersion       string  `json:"targetNewVersion"`
	Rationale              string  `json:"rationale"`
	Confidence             float64 `json:"confidence"`
	ValidationState        string  `json:"validationState"`
	ValidationReport       string  `json:"validationReport"`
	ValidationDeltaPreview any     `json:"validationDeltaPreview"`
	ValidationCheckedAt    string  `json:"validationCheckedAt"`
	Model                  string  `json:"model"`
	PromptHash             string  `json:"promptHash"`
	CatalogHash            string  `json:"catalogHash"`
	ReasonedAt             string  `json:"reasonedAt"`
	ReviewState            string  `json:"reviewState"`
	ReviewInvalidReason    string  `json:"reviewInvalidReason"`
	ReviewedAt             string  `json:"reviewedAt"`
	AppliedAt              string  `json:"appliedAt"`
	AppliedByOp            string  `json:"appliedByOp"`
}

// capabilityProposalRow is the GET /api/review/capability(/<id>) wire shape:
// the bucket cols verbatim plus ProposalID, the bare NanoID the UI routes and
// submits ReviewCapabilityProposal with (the bucket only carries the full
// vtx.capabilityproposal.<id> key).
type capabilityProposalRow struct {
	capabilityProposalCols
	ProposalID string `json:"proposalId"`
}

// decodeCapabilityProposalCols decodes one bucket entry, rejecting a
// poison/malformed entry or one missing the Key a well-formed row always
// carries — mirrors flows.go's decodeFlowCols poison-tolerance.
func decodeCapabilityProposalCols(raw []byte) (capabilityProposalCols, bool) {
	var cols capabilityProposalCols
	if json.Unmarshal(raw, &cols) != nil || cols.Key == "" {
		return capabilityProposalCols{}, false
	}
	return cols, true
}

// capabilityProposalIDFromKey extracts the bare NanoID from a
// vtx.capabilityproposal.<id> vertex key; ok is false for any other shape.
func capabilityProposalIDFromKey(key string) (id string, ok bool) {
	const prefix = "vtx.capabilityproposal."
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	id = strings.TrimPrefix(key, prefix)
	if id == "" || strings.Contains(id, ".") {
		return "", false
	}
	return id, true
}

// toCapabilityProposalRow pairs decoded cols with the id extracted from Key;
// ok is false when Key isn't a well-formed capabilityproposal vertex key (a
// poison entry the caller should skip).
func toCapabilityProposalRow(cols capabilityProposalCols) (capabilityProposalRow, bool) {
	id, ok := capabilityProposalIDFromKey(cols.Key)
	if !ok {
		return capabilityProposalRow{}, false
	}
	return capabilityProposalRow{capabilityProposalCols: cols, ProposalID: id}, true
}

// computeCapabilityProposals assembles the queue's row list from the bucket's
// keys. Rows are returned key-sorted for a deterministic wire order; the
// pending-first / newest-first triage sort is the goja logic tier's job
// (logic/review.js's proposalRows), per the design's "decision logic lives in
// the logic tier" rule.
func computeCapabilityProposals(keys []string, get kvGetter) []capabilityProposalRow {
	rows := make([]capabilityProposalRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		cols, ok := decodeCapabilityProposalCols(raw)
		if !ok {
			continue
		}
		row, ok := toCapabilityProposalRow(cols)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ProposalID < rows[j].ProposalID })
	return rows
}

// handleReview routes GET /api/review/{capability,augur}(/<id>) to the two
// tabs' queue/detail handlers, plus the capability tab's two POST action
// endpoints (F16.2): /api/review/capability/<id>/{approve,apply}. Augur's
// approve/reject need no dedicated endpoint — they reuse POST /api/op
// directly (§4.4) since Augur's verdict carries no server-computed payload.
func (s *server) handleReview(w http.ResponseWriter, r *http.Request) {
	parts := splitNonEmpty(strings.TrimPrefix(r.URL.Path, "/api/review/"))
	if len(parts) == 0 || (parts[0] != "capability" && parts[0] != "augur") {
		s.writeError(w, http.StatusBadRequest, "expected GET /api/review/{capability,augur} or GET /api/review/{capability,augur}/<id>")
		return
	}
	tab := parts[0]
	switch len(parts) {
	case 1:
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusBadRequest, "GET required")
			return
		}
		if tab == "augur" {
			s.reviewAugurQueue(w, r)
		} else {
			s.reviewCapabilityQueue(w, r)
		}
	case 2:
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusBadRequest, "GET required")
			return
		}
		if tab == "augur" {
			s.reviewAugurDetail(w, r, parts[1])
		} else {
			s.reviewCapabilityDetail(w, r, parts[1])
		}
	case 3:
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusBadRequest, "POST required")
			return
		}
		if tab != "capability" {
			s.writeError(w, http.StatusBadRequest, "only capability proposals support an approve/apply endpoint")
			return
		}
		switch parts[2] {
		case "approve":
			s.reviewCapabilityApprove(w, r, parts[1])
		case "apply":
			s.reviewCapabilityApply(w, r, parts[1])
		default:
			s.writeError(w, http.StatusBadRequest, "expected POST /api/review/capability/<id>/{approve,apply}")
		}
	default:
		s.writeError(w, http.StatusBadRequest, "expected GET /api/review/{capability,augur} or GET /api/review/{capability,augur}/<id>")
	}
}

func (s *server) reviewCapabilityQueue(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, capabilityauthor.CapabilityProposalsBucket)
	if err != nil {
		if substrate.IsBucketNotFound(err) {
			// The read-model bucket is provisioned by the capability-author
			// package's lens DDL — absent bucket = that package isn't installed
			// on this stack, so the capability-authoring loop simply isn't
			// present. Report that as an unprovisioned empty console, not a
			// gateway fault (the UI renders a "install to enable" empty state).
			s.writeUnprovisionedReview(w, "capability-author")
			return
		}
		s.writeError(w, http.StatusBadGateway, "list "+capabilityauthor.CapabilityProposalsBucket+": "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, capabilityauthor.CapabilityProposalsBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows := computeCapabilityProposals(keys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"proposals": rows, "count": len(rows)})
}

// writeUnprovisionedReview answers a review-queue read whose read-model bucket
// does not exist yet: a 200 carrying an empty proposal set plus the
// unprovisioned flag + the package that would provision it, so the UI (and the
// shell badge, which reads count) treat it as an empty console rather than an
// error. packageName is the package an operator installs to light the tab up.
func (s *server) writeUnprovisionedReview(w http.ResponseWriter, packageName string) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"proposals":     []any{},
		"count":         0,
		"unprovisioned": true,
		"packageName":   packageName,
	})
}

func (s *server) reviewCapabilityDetail(w http.ResponseWriter, r *http.Request, id string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if err := validateControlName(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "proposal id: "+err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	key := "vtx.capabilityproposal." + id
	entry, err := conn.KVGet(ctx, capabilityauthor.CapabilityProposalsBucket, key)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "capability proposal "+id+" not found: "+err.Error())
		return
	}
	cols, ok := decodeCapabilityProposalCols(entry.Value)
	if !ok {
		s.writeError(w, http.StatusBadGateway, "capability proposal "+id+": malformed read-model row")
		return
	}
	row, ok := toCapabilityProposalRow(cols)
	if !ok {
		s.writeError(w, http.StatusBadGateway, "capability proposal "+id+": row key does not resolve to this id")
		return
	}
	s.writeJSON(w, http.StatusOK, row)
}

// augurProposalCols is the on-the-wire shape of one augur-proposals bucket
// entry — field names mirror augurProposalsSpec's RETURN AS aliases
// (packages/augur/lenses.go) verbatim, so decoding is a direct
// json.Unmarshal with no field remapping. A row whose reasoning is still in
// flight (RecordProposal hasn't run) projects with reviewState empty and
// every model-derived column zero/empty — that is a valid row (the claim
// vertex, gap context only), not a decode failure; only a missing/empty Key
// marks a poison entry.
type augurProposalCols struct {
	Key            string  `json:"key"`
	ProposalKey    string  `json:"proposalKey"`
	TargetID       string  `json:"targetId"`
	EntityID       string  `json:"entityId"`
	GapColumn      string  `json:"gapColumn"`
	Trigger        string  `json:"trigger"`
	ProposedAction string  `json:"proposedAction"`
	ProposedParams any     `json:"proposedParams"`
	Rationale      string  `json:"rationale"`
	Confidence     float64 `json:"confidence"`
	Model          string  `json:"model"`
	ReasonedAt     string  `json:"reasonedAt"`
	ReviewState    string  `json:"reviewState"`
	InvalidReason  string  `json:"invalidReason"`
	ReviewedAt     string  `json:"reviewedAt"`
	DispatchedAt   string  `json:"dispatchedAt"`
}

// augurProposalRow is the GET /api/review/augur(/<id>) wire shape: the bucket
// cols verbatim plus ProposalID, the bare handle the UI routes with and
// submits ReviewProposal's externalRef with (the bucket only carries the
// full vtx.augurproposal.<handle> key).
type augurProposalRow struct {
	augurProposalCols
	ProposalID string `json:"proposalId"`
}

// decodeAugurProposalCols decodes one bucket entry, rejecting a
// poison/malformed entry or one missing the Key a well-formed row always
// carries — mirrors decodeCapabilityProposalCols.
func decodeAugurProposalCols(raw []byte) (augurProposalCols, bool) {
	var cols augurProposalCols
	if json.Unmarshal(raw, &cols) != nil || cols.Key == "" {
		return augurProposalCols{}, false
	}
	return cols, true
}

// augurProposalIDFromKey extracts the bare handle from a
// vtx.augurproposal.<handle> vertex key; ok is false for any other shape.
func augurProposalIDFromKey(key string) (id string, ok bool) {
	const prefix = "vtx.augurproposal."
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	id = strings.TrimPrefix(key, prefix)
	if id == "" || strings.Contains(id, ".") {
		return "", false
	}
	return id, true
}

// toAugurProposalRow pairs decoded cols with the id extracted from Key; ok is
// false when Key isn't a well-formed augurproposal vertex key (a poison entry
// the caller should skip).
func toAugurProposalRow(cols augurProposalCols) (augurProposalRow, bool) {
	id, ok := augurProposalIDFromKey(cols.Key)
	if !ok {
		return augurProposalRow{}, false
	}
	return augurProposalRow{augurProposalCols: cols, ProposalID: id}, true
}

// computeAugurProposals assembles the queue's row list from the bucket's
// keys. Rows are returned key-sorted for a deterministic wire order; the
// pending-first/confidence-descending triage sort is the goja logic tier's
// job (logic/review.js's augurProposalRows), per the design's "decision
// logic lives in the logic tier" rule.
func computeAugurProposals(keys []string, get kvGetter) []augurProposalRow {
	rows := make([]augurProposalRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		cols, ok := decodeAugurProposalCols(raw)
		if !ok {
			continue
		}
		row, ok := toAugurProposalRow(cols)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ProposalID < rows[j].ProposalID })
	return rows
}

func (s *server) reviewAugurQueue(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, augur.AugurProposalsBucket)
	if err != nil {
		if substrate.IsBucketNotFound(err) {
			// Provisioned by the augur package's lens DDL — absent bucket = the
			// Augur escalation loop isn't installed on this stack. Empty
			// console, not a fault (§ same as the capability tab).
			s.writeUnprovisionedReview(w, "augur")
			return
		}
		s.writeError(w, http.StatusBadGateway, "list "+augur.AugurProposalsBucket+": "+err.Error())
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, augur.AugurProposalsBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	rows := computeAugurProposals(keys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"proposals": rows, "count": len(rows)})
}

func (s *server) reviewAugurDetail(w http.ResponseWriter, r *http.Request, id string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if err := validateControlName(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "proposal id: "+err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	key := "vtx.augurproposal." + id
	entry, err := conn.KVGet(ctx, augur.AugurProposalsBucket, key)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "augur proposal "+id+" not found: "+err.Error())
		return
	}
	cols, ok := decodeAugurProposalCols(entry.Value)
	if !ok {
		s.writeError(w, http.StatusBadGateway, "augur proposal "+id+": malformed read-model row")
		return
	}
	row, ok := toAugurProposalRow(cols)
	if !ok {
		s.writeError(w, http.StatusBadGateway, "augur proposal "+id+": row key does not resolve to this id")
		return
	}
	s.writeJSON(w, http.StatusOK, row)
}

// loupeCypherParser adapts ruleengine/full.Engine to pkgmgr.CypherParser —
// the same adapter cmd/lattice/capability/cypherparser.go wires for the CLI's
// own re-validation path. Living here (not in internal/pkgmgr) avoids the
// import cycle pkgmgr.CypherParser's doc explains: full's own test binary
// transitively imports pkgmgr, so pkgmgr itself cannot import full directly.
// cmd/loupe is an independent leaf package, so it can wire the two together
// exactly as the CLI does.
type loupeCypherParser struct{}

func (loupeCypherParser) Parse(ruleBody string) error {
	_, err := full.New().Parse(ruleBody)
	return err
}

var _ pkgmgr.CypherParser = loupeCypherParser{}

// heldPermissionsForCapabilityActor reads actor's live Contract #6 §6.1
// capability projection from the capability-kv bucket
// (bootstrap.CapabilityKVBucket — the platform-scope "cap.<rest>" key and the
// role-derived "cap.roles.<rest>" key) and returns the union of both docs'
// platformPermissions as HeldPermission — the §5 "grant" kind's scope check
// needs this for the proposal's own REQUESTER (never the approving operator —
// a grant proposal widens what the requester may already do, so the
// requester's held permissions are what bounds it). Mirrors
// cmd/lattice/capability's own helper of the same purpose. A key that doesn't
// exist contributes no permissions (deny-closed union, not an error).
func heldPermissionsForCapabilityActor(ctx context.Context, conn *substrate.Conn, actor string) ([]pkgmgr.HeldPermission, error) {
	rest, ok := strings.CutPrefix(actor, "vtx.")
	if !ok {
		return nil, fmt.Errorf("actor %q lacks vtx. prefix", actor)
	}
	var held []pkgmgr.HeldPermission
	for _, key := range []string{"cap." + rest, "cap.roles." + rest} {
		entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue // absent key = no permissions from this source, not an error
			}
			return nil, fmt.Errorf("read %s: %w", key, err)
		}
		doc, err := processor.ParseCapabilityDoc(entry.Value)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", key, err)
		}
		for _, p := range doc.PlatformPermissions {
			held = append(held, pkgmgr.HeldPermission{OperationType: p.OperationType, Scope: p.Scope})
		}
	}
	return held, nil
}

// ddlCacheSensitiveResolver adapts internal/processor.DDLCache to
// pkgmgr.SensitiveAspectResolver: an aspectType DDL's CanonicalName IS the
// bare aspect local name, so Lookup(aspectLocalName).Sensitive is exactly the
// live authority the §5 sensitive-aspect check needs.
type ddlCacheSensitiveResolver struct {
	cache *processor.DDLCache
}

func (r ddlCacheSensitiveResolver) IsSensitiveAspect(aspectLocalName string) bool {
	ref, ok := r.cache.Lookup(aspectLocalName)
	return ok && ref.Sensitive
}

// newLiveSensitiveAspectResolver builds a pkgmgr.SensitiveAspectResolver
// backed by a one-shot DDLCache scan of the live catalog — the approve-time
// freshness re-check §5 requires for an "opMeta" kind proposal (the
// record-time verdict may be stale by the time an operator approves).
func newLiveSensitiveAspectResolver(ctx context.Context, conn *substrate.Conn) (pkgmgr.SensitiveAspectResolver, error) {
	cache := processor.NewDDLCache(conn, bootstrap.CoreKVBucket, nil)
	if err := cache.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("refresh DDL cache: %w", err)
	}
	return ddlCacheSensitiveResolver{cache: cache}, nil
}

// freshCapabilityVerdict re-runs the §5 deterministic-validation boundary
// against the LIVE catalog/registry for a pending capability proposal
// (ai-authored-capabilities-design.md §5 point 3 — record-time and
// approve-time can drift) and returns the ArtifactValidationReport the
// approve op's fresh-validation payload requires. Kept separate from the HTTP
// handler so the decision logic is unit-testable without a live substrate for
// the kinds that need no live read (lens/weaverTarget/loomPattern/
// vertexTypeDDL — held/sensitiveAspects both nil). Mirrors the CLI's
// freshApprovalVerdict (cmd/lattice/capability): only "grant" reads the
// requester's live held permissions; only "opMeta" needs the live
// sensitive-aspect resolver.
func freshCapabilityVerdict(ctx context.Context, conn *substrate.Conn, cols capabilityProposalCols) (pkgmgr.ArtifactValidationReport, error) {
	var held []pkgmgr.HeldPermission
	if cols.Kind == "grant" {
		var err error
		held, err = heldPermissionsForCapabilityActor(ctx, conn, cols.RequesterID)
		if err != nil {
			return pkgmgr.ArtifactValidationReport{}, fmt.Errorf("read requester %s held permissions: %w", cols.RequesterID, err)
		}
	}
	var sensitiveAspects pkgmgr.SensitiveAspectResolver
	if cols.Kind == "opMeta" {
		var err error
		sensitiveAspects, err = newLiveSensitiveAspectResolver(ctx, conn)
		if err != nil {
			return pkgmgr.ArtifactValidationReport{}, fmt.Errorf("load live DDL catalog for sensitive-aspect check: %w", err)
		}
	}
	return pkgmgr.ValidateCapabilityArtifact(cols.Kind, json.RawMessage(cols.Content), loupeCypherParser{}, held, sensitiveAspects)
}

// reviewCapabilityApprove implements POST /api/review/capability/<id>/approve
// (§3.3 — F16's one real architectural fork). Approve is never a blind POST
// of the stored verdict: the operator's approve must carry a FRESH
// pkgmgr.ValidateCapabilityArtifact verdict re-computed against the CURRENT
// catalog (Option A, adjudicated §8.1) — record-time and approve-time can
// drift. If the fresh verdict is invalid, the failure is returned to the UI
// and NO op is submitted (the design's recommended default) — the proposal
// stays pending; the operator can reject it or wait for a corrected
// re-proposal. Only when the fresh verdict is valid does this submit
// ReviewCapabilityProposal{verdict:approve, validation:{state:"valid"}}
// through the same Gateway-relay path every other op submit uses, so the
// reviewer identity is the logged-in operator automatically (Loupe stamps no
// actor — the Gateway stamps the verified token's subject).
func (s *server) reviewCapabilityApprove(w http.ResponseWriter, r *http.Request, id string) {
	if s.crossOriginBlocked(w, r) {
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if err := validateControlName(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "proposal id: "+err.Error())
		return
	}
	ctx, cancel := s.pkgContext(r)
	defer cancel()

	proposalKey := "vtx.capabilityproposal." + id
	entry, err := conn.KVGet(ctx, capabilityauthor.CapabilityProposalsBucket, proposalKey)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "capability proposal "+id+" not found: "+err.Error())
		return
	}
	cols, ok := decodeCapabilityProposalCols(entry.Value)
	if !ok {
		s.writeError(w, http.StatusBadGateway, "capability proposal "+id+": malformed read-model row")
		return
	}
	if cols.ReviewState != "pending" {
		s.writeError(w, http.StatusConflict, "capability proposal "+id+" is "+cols.ReviewState+", not pending")
		return
	}
	if cols.Kind == "" {
		s.writeError(w, http.StatusConflict, "capability proposal "+id+" has no recorded artifact yet (reasoning still in flight)")
		return
	}

	report, err := freshCapabilityVerdict(ctx, conn, cols)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "re-validate artifact: "+err.Error())
		return
	}
	if !report.Valid {
		// The proposal no longer validates against the current catalog.
		// Block client-side and DO NOT submit — the operator sees why and
		// can reject or wait for a corrected re-proposal (design §3.3).
		s.writeJSON(w, http.StatusOK, map[string]any{
			"blocked":          true,
			"validationState":  "invalid",
			"validationReport": strings.Join(report.Errors, "; "),
		})
		return
	}

	payload, err := json.Marshal(map[string]any{
		"proposalId": id,
		"verdict":    "approve",
		"validation": map[string]any{"state": "valid"},
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal payload: "+err.Error())
		return
	}
	reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
		OperationType: "ReviewCapabilityProposal",
		Lane:          string(processor.LaneDefault),
		Payload:       payload,
		Reads:         []string{proposalKey + ".review"},
	})
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "submit approve: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, reply)
}

// reviewCapabilityApply implements POST /api/review/capability/<id>/apply
// (§3.3 — "after approve → apply, the real boundary of F16"). Apply is not an
// op relay: it is the same two-Processor-commit platform flow
// `cmd/lattice-pkg apply-proposal` already drives —
// pkgmgr.CapabilityApplyPlanForProposal materializes the SAME Definition
// already validated at record/approve time, Installer.Apply installs/
// upgrades it through the existing, unmodified F-004 path (reusing the same
// Installer wiring cmd/loupe/pkg.go's package install/uninstall endpoints
// already use — s.adminActor for provenance, s.pkgmgrSubmit relaying every
// submitted op through the Gateway), then MarkCapabilityProposalApplied
// closes the loop. A failure between the two commits leaves the package
// installed but the proposal still "approved, not applied" — the error
// message says so explicitly, mirroring the CLI's own guidance, so an
// operator retries the mark-applied step rather than re-running apply.
func (s *server) reviewCapabilityApply(w http.ResponseWriter, r *http.Request, id string) {
	if s.crossOriginBlocked(w, r) {
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if s.adminActor == "" {
		s.writeError(w, http.StatusBadGateway,
			"admin actor not loaded; a valid bootstrap file (BOOTSTRAP_JSON_PATH) is required to apply a capability proposal")
		return
	}
	if err := validateControlName(id); err != nil {
		s.writeError(w, http.StatusBadRequest, "proposal id: "+err.Error())
		return
	}
	ctx, cancel := s.pkgContext(r)
	defer cancel()

	proposalKey := "vtx.capabilityproposal." + id
	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		s.writeError(w, http.StatusConflict, "build apply plan: "+err.Error())
		return
	}

	inst := pkgmgr.NewInstaller(conn, s.adminActor)
	inst.RoleIDs = kernelRoleIDs()
	inst.Submit = s.pkgmgrSubmit

	res, err := inst.Apply(ctx, plan.Definition, pkgmgr.ApplyOptions{})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, pkgmgr.ErrNotInstalled) || errors.Is(err, pkgmgr.ErrCanonicalNameCollision) {
			status = http.StatusConflict
		}
		s.writeError(w, status, "apply "+plan.PackageName+": "+err.Error())
		return
	}

	installRequestID := res.Action + ":" + res.PackageName + "@" + res.ToVersion
	markPayload, err := json.Marshal(map[string]any{
		"proposalId":       id,
		"packageKey":       res.PackageKey,
		"installRequestId": installRequestID,
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal mark-applied payload: "+err.Error())
		return
	}
	reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
		OperationType: "MarkCapabilityProposalApplied",
		Lane:          string(processor.LaneDefault),
		Payload:       markPayload,
		Reads:         []string{proposalKey + ".review", proposalKey + ".target", res.PackageKey + ".manifest"},
	})
	if err != nil {
		s.writeError(w, http.StatusBadGateway, fmt.Sprintf(
			"apply succeeded (packageKey=%s, installRequestId=%s) but MarkCapabilityProposalApplied failed: %s — the package IS already applied; retry MarkCapabilityProposalApplied alone rather than re-applying",
			res.PackageKey, installRequestID, err.Error()))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"apply":            applyReply(res),
		"markApplied":      reply,
		"installRequestId": installRequestID,
	})
}
