package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

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
	Source                 string  `json:"source"`
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
// tabs' queue/detail handlers, plus the capability tab's three POST action
// endpoints: /api/review/capability/<id>/{approve,apply,mark-applied}. Augur's
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
			s.writeError(w, http.StatusBadRequest, "only capability proposals have approve/apply/mark-applied endpoints")
			return
		}
		switch parts[2] {
		case "approve":
			s.reviewCapabilityApprove(w, r, parts[1])
		case "apply":
			s.reviewCapabilityApply(w, r, parts[1])
		case "mark-applied":
			s.reviewCapabilityMarkApplied(w, r, parts[1])
		default:
			s.writeError(w, http.StatusBadRequest, "expected POST /api/review/capability/<id>/{approve,apply,mark-applied}")
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

// loupeCypherParser adapts ruleengine/full to pkgmgr.CypherParser — the same
// adapter cmd/lattice/capability/cypherparser.go wires for the CLI's own
// re-validation path. Living here (not in internal/pkgmgr) avoids the import
// cycle pkgmgr.CypherParser's doc explains: full's own test binary transitively
// imports pkgmgr, so pkgmgr itself cannot import full directly. cmd/loupe is an
// independent leaf package, so it can wire the two together exactly as the CLI
// does.
type loupeCypherParser struct{}

func (loupeCypherParser) Parse(ruleBody string) (pkgmgr.SpecLabels, error) {
	facts, err := full.SpecLabels(ruleBody)
	if err != nil {
		return pkgmgr.SpecLabels{}, err
	}
	return pkgmgr.SpecLabels{
		Referenced: facts.Referenced,
		Exhaustive: facts.Exhaustive,
		Expansion:  facts.Expansion,
	}, nil
}

var _ pkgmgr.CypherParser = loupeCypherParser{}

// newInstaller is Loupe's only installer constructor, so every handler gets the
// same wiring — including the spec parser the install-time narrowed-filter
// budget gate needs (pkgmgr.Installer.SpecParser,
// dynamic-type-taxonomy-design.md §10.2). Calling pkgmgr.NewInstaller directly
// from a handler would silently drop that gate for whichever path forgot, which
// is exactly the kind of per-call-site divergence a single constructor removes.
// Per-handler wiring that is NOT common to all of them (RoleIDs, Submit) stays
// at the call sites.
func newInstaller(conn *substrate.Conn, adminActor string) *pkgmgr.Installer {
	inst := pkgmgr.NewInstaller(conn, adminActor)
	inst.SpecParser = loupeCypherParser{}
	return inst
}

// systemActorSet holds the graph-derived system-actor set
// (bootstrap.SystemActorKeys) for the process. That set decides Capability-KV
// key routing, and discovering it costs a full core-kv KVListKeys, so it is
// resolved once and held — the posture every platform daemon takes at start-up
// (cmd/processor/main.go:142, cmd/loom/main.go:181, cmd/weaver/main.go:184),
// and the reason a handler must never re-derive it per request.
//
// Loupe resolves it at boot but memoizes only a NON-EMPTY result, because two
// causes give a not-yet answer that must not be latched: a Loupe whose NATS was
// down at start-up still serves the UI and reconnects in the background, and a
// kernel that has not finished seeding its primordial holdsRole links answers
// with an empty set rather than an error. Latching either would route every
// actor as ordinary for the process lifetime, under-reporting a real system
// actor's held permissions on every later request. An empty result therefore
// stays unresolved and the next use retries; a deployment that genuinely has no
// system actors pays one listing per capability-grant approve, the rarest path
// in the console.
//
// One cause is NOT retryable and the retry loop cannot fix it: if this process
// started without a readable lattice.bootstrap.json, bootstrap.Load failed
// (non-fatally, main.go) and SystemActorKeys returns ErrPrimordialIDsUnloaded
// before it ever reaches the substrate — so every capability-GRANT approve
// fails for the life of the process, while every other console path keeps
// working. That is fail-closed and correct, and the fix is operational: give
// the process the file (BOOTSTRAP_JSON_PATH) and restart it.
type systemActorSet struct {
	mu       sync.Mutex
	keys     []string
	resolved bool
}

// get returns the memoized set, resolving it on the first non-empty success.
//
// The mutex is held across the whole discovery — one core-kv KVListKeys plus a
// KVGet per candidate holdsRole link — and that call does not abandon early on
// a caller's cancelled ctx, so a concurrent handler arriving mid-resolution can
// block for up to the resolving caller's own deadline (10s at boot,
// s.natsTimeout on a request path) past its own. That is bounded and deliberate:
// serializing costs one listing for a burst instead of one per request, and the
// only path that reaches it is a capability-proposal approve.
func (s *systemActorSet) get(ctx context.Context, conn *substrate.Conn) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolved {
		return s.keys, nil
	}
	keys, err := bootstrap.SystemActorKeys(ctx, conn)
	if err != nil {
		if errors.Is(err, bootstrap.ErrPrimordialIDsUnloaded) {
			return nil, fmt.Errorf("discover system actor keys: %w — this process started without a readable lattice.bootstrap.json; set BOOTSTRAP_JSON_PATH and restart Loupe", err)
		}
		return nil, fmt.Errorf("discover system actor keys: %w", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	s.keys, s.resolved = keys, true
	return s.keys, nil
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
//
// The held permissions are the proposal's own REQUESTER's, never the approving
// operator's — a grant proposal widens what the requester may already do, so
// the requester's own grant set is what bounds it (pkgmgr.ReadHeldPermissions).
func (s *server) freshCapabilityVerdict(ctx context.Context, conn *substrate.Conn, cols capabilityProposalCols) (pkgmgr.ArtifactValidationReport, error) {
	var held []pkgmgr.HeldPermission
	if cols.Kind == "grant" {
		systemActorKeys, sErr := s.systemActors.get(ctx, conn)
		if sErr != nil {
			return pkgmgr.ArtifactValidationReport{}, sErr
		}
		var err error
		held, err = pkgmgr.ReadHeldPermissions(ctx, conn, systemActorKeys, cols.RequesterID)
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

	report, err := s.freshCapabilityVerdict(ctx, conn, cols)
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
// installed but the proposal still "approved, not applied". Re-running apply
// cannot recover that: CapabilityApplyPlanForProposal binds a newPackage
// target to the LIVE catalog and refuses once the name is installed, which is
// a guard worth keeping (an AI-authored name must never diff-apply into an
// unrelated package). So the reply says so structurally — resumable marks the
// half-committed state and points at the mark-applied endpoint below, which
// re-derives everything server-side.
func (s *server) reviewCapabilityApply(w http.ResponseWriter, r *http.Request, id string) {
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
	// A half-committed apply survives the page that produced it, so this
	// recognizes it on a COLD load too — otherwise the operator returning to a
	// proposal that reads "approved, not applied" clicks Apply, gets the plan
	// builder's bare refusal, and is told nothing about the recovery that
	// actually finishes it. The reply carries the same resumable marker the
	// in-session failure below does, so one classifier handles both. A proposal
	// whose read-model row cannot be read at all is left to the plan builder,
	// which is the authority regardless.
	cols, haveRow := s.capabilityRow(ctx, conn, proposalKey)
	// Refuse a platform-protected target before the resumable classification
	// below: that branch answers a 409 with resumable:true and sends the
	// operator to mark-applied, which stamps review.state=applied over the
	// platform package's real vertex without the plan builder — whose deny-list
	// would otherwise be the boundary — ever running.
	if haveRow && pkgmgr.PlatformProtectedPackage(cols.TargetPackageName) {
		s.writeError(w, http.StatusConflict, fmt.Sprintf(
			"proposal %s targets %q, a platform-protected package that no AI-authored proposal may install, upgrade or close over",
			id, cols.TargetPackageName))
		return
	}
	// A proposal the plan builder would refuse must be refused rather than
	// reported recoverable. The recovery classification below answers "is this
	// package live at the target version" — which is the same state an
	// upgradeExisting proposal declaring newVersion == the installed version
	// produces WITHOUT ever having applied, so with the plan builder running
	// only afterwards that proposal is closed over an artifact that never
	// landed. Asking the preconditions here puts the refusal back in front of
	// the classification it would otherwise be mistaken for.
	if err := pkgmgr.ValidateCapabilityApplyTarget(ctx, conn, proposalKey); err != nil {
		s.writeError(w, http.StatusConflict, "build apply plan: "+err.Error())
		return
	}
	if haveRow && cols.ReviewState == "approved" {
		if packageKey, installed, err := s.targetInstall(ctx, conn, cols); err == nil && installed {
			s.writeJSON(w, http.StatusConflict, map[string]any{
				"error": fmt.Sprintf(
					"%s is already installed at version %s, so this proposal's install has already committed — close it with mark-applied rather than re-applying",
					cols.TargetPackageName, targetInstallVersion(cols)),
				"resumable":  true,
				"packageKey": packageKey,
			})
			return
		}
	}
	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		s.writeError(w, http.StatusConflict, "build apply plan: "+err.Error())
		return
	}

	inst := newInstaller(conn, s.adminActor)
	inst.RoleIDs = kernelRoleIDs()
	inst.Submit = s.pkgmgrSubmit

	res, err := inst.ApplyCapabilityPlan(ctx, plan)
	if err != nil {
		s.writeError(w, packageApplyStatus(err), "apply "+plan.PackageName+": "+err.Error())
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
	if failure := markOpFailure(reply, err); failure != "" {
		s.writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": fmt.Sprintf(
				"apply succeeded (packageKey=%s, installRequestId=%s) but MarkCapabilityProposalApplied failed: %s — the package IS already installed; recover with mark-applied rather than re-applying",
				res.PackageKey, installRequestID, failure),
			"resumable":  true,
			"packageKey": res.PackageKey,
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"apply":            applyReply(res),
		"markApplied":      reply,
		"installRequestId": installRequestID,
	})
}

// markOpFailure reduces a relayed MarkCapabilityProposalApplied outcome to one
// operator-facing reason, or "" when the op really committed.
//
// A transport error is only half of it. submitOpViaGateway returns (reply,
// nil) whenever the Gateway shaped a reply at all, so a Processor REJECTION —
// the likelier failure, and the one every guard in the DDL script produces —
// arrives with a nil error and a rejected status. Branching on err alone would
// report the console's central "the second commit can fail" case as success,
// which is the thing this whole path exists to notice.
func markOpFailure(reply *processor.OperationReply, err error) string {
	if err != nil {
		return err.Error()
	}
	if reply == nil {
		return "the Gateway returned no reply"
	}
	if reply.Status != processor.ReplyStatusRejected {
		return ""
	}
	if reply.Error != nil && reply.Error.Message != "" {
		return "rejected by the Processor: " + reply.Error.Message
	}
	return "rejected by the Processor"
}

// recoveredInstallRequestID is the audit pointer mark-applied stamps as
// appliedByOp. It is deliberately NOT the "install:<name>@<version>" string a
// successful apply would have produced: this path never observed that op, and
// a reconstructed pointer that reads identically to an observed one would put
// a small fiction into the audit record. The "recovered:" prefix says which
// path wrote it.
func recoveredInstallRequestID(packageName, version string) string {
	return "recovered:" + packageName + "@" + version
}

// findInstalledPackageByName resolves the live vtx.package.<id> vertex whose
// .manifest aspect records name, returning its key and installed version.
//
// It is the mark-applied path's answer to "did the install half actually
// commit", and it checks isDeleted on BOTH the manifest aspect and the package
// root: an uninstall tombstones them rather than removing the keys, and a
// tombstoned manifest still decodes. Marking a proposal applied against an
// uninstalled package would record an appliedAs link to a dead vertex. The op
// itself re-verifies liveness independently — this check exists so the console
// refuses with an explanation instead of relaying a doomed op.
//
// Manifests are scanned in sorted key order so a (pathological) double claim
// on one name resolves deterministically, matching findOwningPackage.
//
// Name alone never establishes that a package is a given proposal's install —
// see targetInstallVersion for the version callers must also match.
func findInstalledPackageByName(coreKeys []string, get kvGetter, name string) (key, version string, ok bool) {
	if name == "" {
		return "", "", false
	}
	manifests := make([]string, 0, 4)
	for _, k := range coreKeys {
		if strings.HasPrefix(k, "vtx.package.") && strings.HasSuffix(k, ".manifest") && classifyKey(k) == classAspect {
			manifests = append(manifests, k)
		}
	}
	sort.Strings(manifests)
	for _, mk := range manifests {
		manifest, found := readPkgEnvelope(get, mk)
		if !found || manifest.IsDeleted || dataString(manifest.Data, "name") != name {
			continue
		}
		rootKey := strings.TrimSuffix(mk, ".manifest")
		root, found := readPkgEnvelope(get, rootKey)
		if !found || root.IsDeleted {
			continue
		}
		return rootKey, dataString(manifest.Data, "version"), true
	}
	return "", "", false
}

// capabilityRow reads one proposal's read-model row, absence-tolerant: ok is
// false for a missing row, an unprovisioned bucket, a read failure, or a
// poison entry alike. Callers that need to distinguish those read the bucket
// themselves; the ones using this treat "cannot see the row" as "no opinion",
// which is why it swallows the error rather than returning it.
func (s *server) capabilityRow(ctx context.Context, conn *substrate.Conn, proposalKey string) (capabilityProposalCols, bool) {
	entry, err := conn.KVGet(ctx, capabilityauthor.CapabilityProposalsBucket, proposalKey)
	if err != nil {
		return capabilityProposalCols{}, false
	}
	return decodeCapabilityProposalCols(entry.Value)
}

// targetInstallVersion is the version a proposal's apply lands: its declared
// target.newVersion, or the same "0.1.0" default CapabilityApplyPlanForProposal
// substitutes when the proposal declares none.
//
// Matching on it is what separates "this proposal's install committed" from "a
// package of that name was already there". Name alone cannot make that
// distinction, and for an upgradeExisting target it is not even close: a
// package of that name is installed BEFORE the apply by definition — that is
// the mode's own precondition — so a name-only check would report every
// never-applied upgrade proposal as recoverable and close it over an artifact
// that was never installed.
func targetInstallVersion(cols capabilityProposalCols) string {
	if cols.TargetNewVersion != "" {
		return cols.TargetNewVersion
	}
	return "0.1.0"
}

// coreKVGetter returns a kvGetter over Core KV plus an accessor for the first
// read error it swallowed.
//
// kvGetter's (value, ok) shape cannot tell "no such key" from "the read
// failed", and here the two lead to opposite advice: absent means the install
// never ran, so run Apply; a timeout means the console does not know, and
// telling the operator to run Apply on a proposal whose install DID commit
// sends them to a refusal that flatly contradicts this one. Recording the
// error lets the caller say which it was.
func coreKVGetter(ctx context.Context, conn *substrate.Conn) (get kvGetter, readErr func() error) {
	var first error
	get = func(key string) ([]byte, bool) {
		e, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			if first == nil && !errors.Is(err, substrate.ErrKeyNotFound) {
				first = err
			}
			return nil, false
		}
		return e.Value, true
	}
	return get, func() error { return first }
}

// targetInstall reports whether the package a proposal's apply would install
// is ALREADY live at that proposal's target version — i.e. whether the install
// half of the two-commit apply has landed.
//
// It is the single source of that answer for both halves of the flow: apply
// consults it to recognize a proposal it can no longer install, and
// mark-applied consults it to refuse closing a proposal whose install never
// ran. Deriving it once keeps the two from disagreeing about the same state.
func (s *server) targetInstall(ctx context.Context, conn *substrate.Conn, cols capabilityProposalCols) (packageKey string, installed bool, err error) {
	if cols.TargetPackageName == "" {
		return "", false, nil
	}
	coreKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.package.")
	if err != nil {
		return "", false, fmt.Errorf("list core-kv packages: %w", err)
	}
	get, readErr := coreKVGetter(ctx, conn)
	key, version, found := findInstalledPackageByName(coreKeys, get, cols.TargetPackageName)
	if rErr := readErr(); rErr != nil {
		return "", false, fmt.Errorf("read core-kv package catalog: %w", rErr)
	}
	if !found || version != targetInstallVersion(cols) {
		return key, false, nil
	}
	return key, true, nil
}

// reviewCapabilityMarkApplied implements POST /api/review/capability/<id>/
// mark-applied: the recovery half of apply, for a proposal whose package
// install committed but whose closing MarkCapabilityProposalApplied did not.
//
// Apply cannot repair that state: CapabilityApplyPlanForProposal's
// newPackage-vs-live-catalog guard refuses precisely because the package IS
// installed, and an upgrade would re-run an install that already landed. This
// is the door out, and it has to work on a cold load — the ordinary case is an
// operator who closed the tab after the failed apply.
//
// It takes NO request body. Every field the op needs is re-derived here from
// the proposal's own read-model row plus the live package catalog, so there is
// no client-supplied provenance to trust and the in-session retry and the
// post-reload recovery are the same call. The op re-verifies all of it anyway
// (an approved-only transition, a live .manifest, a name matching this
// proposal's target.packageName), so this handler's checks exist to refuse
// with an operator-legible reason rather than to be the boundary — with one
// exception it owns alone: the op cannot check the VERSION, so "a package of
// this name exists" versus "this proposal's install committed" is decided
// here or nowhere.
func (s *server) reviewCapabilityMarkApplied(w http.ResponseWriter, r *http.Request, id string) {
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
	if cols.ReviewState == "applied" {
		s.writeError(w, http.StatusConflict,
			"proposal "+id+" is already applied — nothing to recover")
		return
	}
	if cols.ReviewState != "approved" {
		s.writeError(w, http.StatusConflict, fmt.Sprintf(
			"proposal %s is %q, not approved — mark-applied only recovers an approved proposal whose install already committed",
			id, cols.ReviewState))
		return
	}
	if cols.TargetPackageName == "" {
		s.writeError(w, http.StatusConflict,
			"proposal "+id+" records no target.packageName, so the installed package it would close over cannot be resolved")
		return
	}
	// Refuse a platform-protected target before the install is resolved: this
	// endpoint never runs the plan builder that owns the deny-list, and what it
	// relays stamps review.state=applied with a real appliedAs link into the
	// named package's vertex — an audit record of an AI-authored artifact
	// entering a platform-trust package, whether or not an install ever ran.
	if pkgmgr.PlatformProtectedPackage(cols.TargetPackageName) {
		s.writeError(w, http.StatusConflict, fmt.Sprintf(
			"proposal %s targets %q, a platform-protected package that no AI-authored proposal may install, upgrade or close over",
			id, cols.TargetPackageName))
		return
	}

	// Loupe reads the package catalog straight from Core KV as the console
	// inspector (P5's named exception, the same read handleSystemMap and
	// findOwningPackage already make), scoped to the vtx.package. subtree.
	packageKey, installed, err := s.targetInstall(ctx, conn, cols)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !installed {
		s.writeError(w, http.StatusConflict, fmt.Sprintf(
			"no package named %q is installed at version %s — this proposal's install never committed, so run Apply rather than mark-applied",
			cols.TargetPackageName, targetInstallVersion(cols)))
		return
	}

	installRequestID := recoveredInstallRequestID(cols.TargetPackageName, targetInstallVersion(cols))
	markPayload, err := json.Marshal(map[string]any{
		"proposalId":       id,
		"packageKey":       packageKey,
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
		Reads:         []string{proposalKey + ".review", proposalKey + ".target", packageKey + ".manifest"},
	})
	if failure := markOpFailure(reply, err); failure != "" {
		// A rejection is a refusal by the Processor's own guards (the proposal
		// moved on, the package does not match), not an upstream fault, so it
		// reads as a conflict; only a transport failure is a 502.
		status := http.StatusBadGateway
		if err == nil {
			status = http.StatusConflict
		}
		s.writeError(w, status, "mark applied: "+failure)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"markApplied":      reply,
		"packageKey":       packageKey,
		"installRequestId": installRequestID,
	})
}
