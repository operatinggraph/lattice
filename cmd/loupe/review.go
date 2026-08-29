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
		// op-name: (submits) reviewCapabilityApprove submits this once the proposal's artifact re-validates against the current catalog, carrying the operator's approve verdict from the Loupe console.
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
		resolved, err := s.targetInstall(ctx, conn, proposalKey, cols)
		switch {
		case err != nil:
			// Not knowing is its own answer, and the wrong one to guess at:
			// proceeding into the plan builder tells an operator whose install
			// DID commit to Apply, and Apply then refuses because the name is
			// live. mark-applied already reports this as a 502, so this
			// endpoint does too and the two stay consistent.
			s.writeError(w, http.StatusBadGateway,
				"cannot determine whether this proposal's install committed: "+err.Error())
			return
		case unprovenNewPackageClose(cols, resolved):
			// Neither appliable (the name is taken, which is what the plan
			// builder refuses on) nor closable (nothing shows this proposal
			// wrote what is there). It must therefore NOT be advertised
			// resumable: that flag is what sends the operator to mark-applied,
			// and mark-applied is exactly the close being refused.
			s.writeError(w, http.StatusConflict, unprovenNewPackageReason(id, proposalKey, cols, resolved))
			return
		case resolved.Installed:
			s.writeJSON(w, http.StatusConflict, map[string]any{
				"error": fmt.Sprintf(
					"%s is already installed at version %s, so this proposal's install has already committed — close it with mark-applied rather than re-applying",
					cols.TargetPackageName, resolved.Version),
				"resumable":  true,
				"packageKey": resolved.PackageKey,
			})
			return
		case resolved.ReceiptStale && resolved.PackageKey != "" && resolved.PackageKey != resolved.ReceiptPackageKey:
			// A DIFFERENT live package holds this proposal's target name, so an
			// apply would write into an artifact this proposal did not produce.
			// Not resumable and not re-appliable: say which, here, rather than
			// letting the plan builder answer with a refusal that never
			// mentions the receipt.
			//
			// A stale receipt on its own is NOT a refusal. Its commonest shape
			// is a receipted package rolled back out of band to the version an
			// upgradeExisting proposal declares as its baseVersion — a plan the
			// builder builds and an apply that rolls forward. Re-appliability
			// is the plan builder's question, and refusing here would answer it
			// for a case it answers yes to.
			s.writeError(w, http.StatusConflict, staleReceiptReason(cols, resolved))
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

	status, body := s.closeApply(ctx, id, proposalKey, cols, res)
	s.writeJSON(w, status, body)
}

// applyInstallRequestID is the audit pointer an apply stamps as appliedByOp.
//
// The Processor reply's own InstallRequestID is preferred whenever the apply
// committed one, because it names the actual commit: it tells this apply apart
// from any other write landing at the same package name and version. The
// composed "<action>:<name>@<version>" string is what a result carrying no
// receipt has to offer — an arm that committed nothing (a skip, a dry run) —
// and it cannot make that distinction, since name and version are all it holds.
func applyInstallRequestID(res *pkgmgr.ApplyResult) string {
	if res.InstallRequestID != "" {
		return res.InstallRequestID
	}
	return res.Action + ":" + res.PackageName + "@" + res.ToVersion
}

// The three outcomes a close reports for its install receipt. They are distinct
// facts an operator acts on differently, which is why they are not a bool:
// "not-applicable" is an apply arm that committed nothing, so there was never
// anything to bind; "failed" is a receipt that was submitted and refused, and
// it is terminal — .install is create-only and the op requires an approved
// proposal, which the mark-applied submit right behind it flips to applied, so
// the binding cannot be obtained afterwards.
const (
	receiptRecorded      = "recorded"
	receiptNotApplicable = "not-applicable"
	receiptFailed        = "failed"
)

// closeApply performs the two submits an apply's close is made of, in order:
// RecordCapabilityInstallReceipt, which binds this proposal to the install the
// Processor just committed, then MarkCapabilityProposalApplied, which flips
// review.state. It returns the HTTP status and body the endpoint answers with.
//
// The receipt never gates the close. Its failure leaves the package live and
// the proposal closable — recovery then resolves the install by package name
// and version, which is what a proposal carrying no receipt gets — so a failed
// receipt is reported and the mark-applied submit still runs.
//
// Every response says which of the three receipt outcomes happened, and a
// non-recorded one is logged as well as returned: the close can otherwise
// succeed end to end while the provenance binding silently never lands, which
// is a state no later reader can distinguish from a proposal that never had a
// receipt at all.
//
// cols is read for one decision only — whether the mark-applied recovery this
// endpoint's failure reply points at can actually run for THIS proposal, which
// turns on its declared target mode.
func (s *server) closeApply(ctx context.Context, id, proposalKey string, cols capabilityProposalCols, res *pkgmgr.ApplyResult) (int, map[string]any) {
	installRequestID := applyInstallRequestID(res)
	receipt, receiptFailure := s.submitInstallReceipt(ctx, id, proposalKey, res)
	if receipt != receiptRecorded {
		s.logger.Warn("capability install receipt not recorded — this proposal's provenance binding is missing, so any later recovery resolves its install by package name and version alone",
			"proposalId", id, "packageKey", res.PackageKey, "receipt", receipt, "reason", receiptFailure)
	}

	markPayload, err := json.Marshal(map[string]any{
		"proposalId":       id,
		"packageKey":       res.PackageKey,
		"installRequestId": installRequestID,
	})
	if err != nil {
		return http.StatusInternalServerError, map[string]any{"error": "marshal mark-applied payload: " + err.Error()}
	}
	reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
		// op-name: (submits) reviewCapabilityApply submits this immediately after ApplyCapabilityPlan installs/upgrades the target package, closing the loop on the same request that just committed the install.
		OperationType: "MarkCapabilityProposalApplied",
		Lane:          string(processor.LaneDefault),
		Payload:       markPayload,
		Reads:         capabilityCloseReads(proposalKey, res.PackageKey),
	})
	body := map[string]any{
		"installRequestId": installRequestID,
		"receipt":          receipt,
	}
	if receiptFailure != "" {
		body["receiptFailure"] = receiptFailure
	}
	if failure := markOpFailure(reply, err); failure != "" {
		body["packageKey"] = res.PackageKey
		// `resumable` is not a description of the state, it is an INSTRUCTION:
		// the console latches the mark-applied control on it. So it may only be
		// set when that control can actually finish the job. A newPackage
		// proposal left with no receipt is precisely what the recovery refuses
		// — a package at this name and version proves nothing about which
		// writer produced it — so arming the control here would hand the
		// operator a button whose only possible outcome is that refusal, on the
		// one path where no other exit is offered.
		if cols.TargetMode == "newPackage" && receipt != receiptRecorded {
			body["error"] = fmt.Sprintf(
				"apply succeeded (packageKey=%s, installRequestId=%s) but MarkCapabilityProposalApplied failed: %s%s. "+
					"This proposal declares mode newPackage and carries no install receipt, so the console's mark-applied "+
					"recovery refuses it rather than closing it over a package nothing shows it wrote. %s",
				res.PackageKey, installRequestID, failure, receiptRecoveryNote(receipt, receiptFailure),
				manualCloseRemedy(id, proposalKey, res.PackageKey))
			return http.StatusBadGateway, body
		}
		body["error"] = fmt.Sprintf(
			"apply succeeded (packageKey=%s, installRequestId=%s) but MarkCapabilityProposalApplied failed: %s%s — the package IS already installed; recover with mark-applied rather than re-applying",
			res.PackageKey, installRequestID, failure, receiptRecoveryNote(receipt, receiptFailure))
		body["resumable"] = true
		return http.StatusBadGateway, body
	}
	body["apply"] = applyReply(res)
	body["markApplied"] = reply
	return http.StatusOK, body
}

// receiptRecoveryNote is the clause the resumable error carries whenever this
// close leaves no receipt behind — a failed submit and an apply arm that had
// nothing to bind alike. Both leave recovery resolving the install by package
// name and version, which cannot tell this proposal's install from any other
// write at that name and version, and that is what the operator reading a
// half-committed apply needs to know.
//
// A failed submit is deliberately reported as unconfirmed rather than as
// absent: the submission may have reached the Processor and committed with the
// reply lost on the way back, and asserting "it did not land" about a message
// already published is a claim this side cannot make.
func receiptRecoveryNote(receipt, receiptFailure string) string {
	switch receipt {
	case receiptRecorded:
		return ""
	case receiptNotApplicable:
		return " (this apply committed nothing to bind, so the recovery resolves this proposal's install by package name and version alone)"
	default:
		return " (the install receipt was not confirmed either: " + receiptFailure +
			" — unless it committed with the reply lost, the recovery resolves this proposal's install by package name and version alone)"
	}
}

// installReceiptRequestID is the receipt op's Contract #4 requestId, derived
// rather than minted so a retry of the same close is COLLAPSED by the requestId
// tracker instead of arriving as a second submission.
//
// A second submission is not harmless here: .install is create-only, so a retry
// bearing a fresh requestId is refused by the commit batch's conditioning, and
// the caller then reports "no receipt landed" while a perfectly valid receipt
// sits in KV. The inputs are exactly the receipt's own content — the proposal
// is write-once and binds to one package — so a genuine retry derives the same
// id and dedups, while a receipt naming a DIFFERENT package derives a different
// one and is left to create-only conditioning to refuse, which is the
// arbitration that belongs there.
func installReceiptRequestID(proposalKey, packageKey string) string {
	// derived-key: the receipt op's Contract #4 requestId, not a declared read —
	// nothing is addressed by it, and the DDL never sees it (the Processor tracks
	// the envelope by it before the script runs). It is derived rather than minted
	// so a retry of the same close submits the same id and the tracker collapses
	// it; a minted one would reach .install's create-only conditioning and be
	// refused, which reads to the caller as "no receipt landed".
	return substrate.SHA256NanoID("RecordCapabilityInstallReceipt:" + proposalKey + ":" + packageKey)
}

// capabilityCloseReads is the exact read set BOTH ops closing an apply declare
// — the receipt and MarkCapabilityProposalApplied alike, which run the same
// guards over the same four keys: the proposal's review state and target, and
// the package root plus its manifest, both of which each op checks live before
// binding the proposal to that package. Each op hydrates from these keys alone,
// so a dispatcher declaring a different set fails the whole op on a hydration
// miss — which is why the set is written once here rather than at each site.
func capabilityCloseReads(proposalKey, packageKey string) []string {
	return []string{
		proposalKey + ".review",
		proposalKey + ".target",
		packageKey,
		packageKey + ".manifest",
	}
}

// submitInstallReceipt relays RecordCapabilityInstallReceipt for an apply that
// committed, stamping the Processor's own receipt onto the proposal's vertex so
// a later reader can name the package this proposal actually wrote.
//
// A result carrying no observed InstallRequestID is not applicable rather than
// failed: that arm committed nothing, so there is no install to bind, and a
// receipt written from a reconstructed pointer would record a fiction.
//
// The outcome comes back as one of the three receipt states plus a reason,
// never as an error, because the receipt is not part of the apply's success —
// its caller reports it and carries on.
func (s *server) submitInstallReceipt(ctx context.Context, id, proposalKey string, res *pkgmgr.ApplyResult) (receipt, failure string) {
	if res.InstallRequestID == "" {
		return receiptNotApplicable, ""
	}
	raw, err := json.Marshal(map[string]any{
		"proposalId":       id,
		"packageKey":       res.PackageKey,
		"installRequestId": res.InstallRequestID,
	})
	if err != nil {
		return receiptFailed, "marshal receipt payload: " + err.Error()
	}
	reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
		RequestID: installReceiptRequestID(proposalKey, res.PackageKey),
		// op-name: (submits) reviewCapabilityApply submits this the moment ApplyCapabilityPlan commits and before the mark-applied close, binding the proposal to the exact install the Processor recorded.
		OperationType: "RecordCapabilityInstallReceipt",
		Lane:          string(processor.LaneDefault),
		Payload:       raw,
		Reads:         capabilityCloseReads(proposalKey, res.PackageKey),
	})
	if failure := markOpFailure(reply, err); failure != "" {
		return receiptFailed, failure
	}
	return receiptRecorded, ""
}

// markOpFailure reduces a relayed capability-close op's outcome — the install
// receipt or MarkCapabilityProposalApplied — to one operator-facing reason, or
// "" when the op really committed.
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
	// Only the two statuses that mean "this op's effect is in state" count as
	// success. Reading success as "not rejected" would report an empty or
	// unrecognized status — a reply this console cannot interpret — as a
	// commit, which is the one direction a close must never guess in.
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		return ""
	case processor.ReplyStatusRejected:
		if reply.Error != nil && reply.Error.Message != "" {
			return "rejected by the Processor: " + reply.Error.Message
		}
		return "rejected by the Processor"
	}
	return fmt.Sprintf("the Gateway returned an unrecognized reply status %q", reply.Status)
}

// recoveredInstallRequestID is the audit pointer mark-applied stamps as
// appliedByOp for a proposal carrying no install receipt. It is deliberately
// NOT the "install:<name>@<version>" string a successful apply would have
// produced: with no receipt this path never observed that op, and a
// reconstructed pointer that reads identically to an observed one would put a
// small fiction into the audit record. The "recovered:" prefix says which path
// wrote it.
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

// installResolution is targetInstall's answer: which package a proposal's
// apply produced, whether it is still live at the proposal's target version,
// and — when a receipt supplied it — the Processor's own pointer to the commit
// that produced it.
type installResolution struct {
	// PackageKey is the resolved vtx.package.<id> root. It is set even when
	// Installed is false, for a live package carrying the target name at a
	// different version.
	PackageKey string
	// Version is that package's own recorded .manifest version — the value an
	// operator-facing message must quote, rather than the version the proposal
	// merely declares.
	Version string
	// InstallRequestID is the receipt's observed pointer, empty whenever the
	// resolution came from the name+version fallback — that path observed no
	// op and so has nothing to stamp.
	InstallRequestID string
	Installed        bool
	// ByReceipt says which of the two resolutions answered. A receipt hit is
	// evidence that THIS proposal's apply produced this package; a name+version
	// hit is not evidence of anything but a name and a version, and a caller
	// deciding whether it may bind the proposal to that package needs to know
	// which one it is holding.
	ByReceipt bool

	// ReceiptStale marks a proposal that HAS a receipt whose package no longer
	// answers for it: uninstalled since, or live at a version other than the
	// one this proposal targets. The fields below say which, so a caller can
	// state what actually happened instead of the "never committed" a bare
	// name+version miss would otherwise imply.
	ReceiptStale bool
	// ReceiptPackageKey is the package the stale receipt names, and
	// ReceiptVersion that package's live manifest version — empty when it is
	// no longer live at all.
	ReceiptPackageKey string
	ReceiptVersion    string
}

// installReceipt is the slice of a vtx.capabilityproposal.<id>.install aspect
// this console reads: the package key the proposal's apply committed and the
// Processor request id that commit was recorded under.
type installReceipt struct {
	PackageKey       string
	InstallRequestID string
}

// readInstallReceipt reads a proposal's install receipt from Core KV, reporting
// ok only for a LIVE, well-formed aspect that names a package key.
//
// The isDeleted filter is load-bearing: a tombstone retains the prior document,
// so an unfiltered read hands a revoked receipt back as direct evidence of
// provenance — the strongest claim this resolver makes.
//
// So is the split between an absent key and an unusable one. Absent means this
// proposal has no receipt, and the name+version fallback is the designed answer
// for it. A present but undecodable document, or one recording no packageKey,
// means the receipt exists and cannot be read — falling back there would answer
// a provenance question with the very heuristic the receipt was written to
// replace, silently. That is an error, and the raw bytes are fetched here
// rather than going through readPkgEnvelope precisely because that helper folds
// the two cases into one absent.
func readInstallReceipt(get kvGetter, proposalKey string) (installReceipt, bool, error) {
	key := proposalKey + ".install"
	raw, found := get(key)
	if !found {
		return installReceipt{}, false, nil
	}
	var env pkgEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return installReceipt{}, false, fmt.Errorf("decode install receipt %s: %w", key, err)
	}
	if env.IsDeleted {
		return installReceipt{}, false, nil
	}
	packageKey := dataString(env.Data, "packageKey")
	if packageKey == "" {
		return installReceipt{}, false, fmt.Errorf("install receipt %s records no packageKey", key)
	}
	return installReceipt{
		PackageKey:       packageKey,
		InstallRequestID: dataString(env.Data, "installRequestId"),
	}, true, nil
}

// livePackageAt returns the recorded manifest version of packageKey when that
// package is live and its .manifest still records name — the facts a receipt
// cannot vouch for, because the package it names can be uninstalled, upgraded
// past the version this proposal produced, or renamed out from under it.
//
// Both the root and the manifest are checked for isDeleted, exactly as
// findInstalledPackageByName does: an uninstall tombstones them rather than
// removing the keys, and either one can be tombstoned on its own.
func livePackageAt(get kvGetter, packageKey, name string) (version string, ok bool) {
	if packageKey == "" || name == "" {
		return "", false
	}
	root, found := readPkgEnvelope(get, packageKey)
	if !found || root.IsDeleted {
		return "", false
	}
	manifest, found := readPkgEnvelope(get, packageKey+".manifest")
	if !found || manifest.IsDeleted {
		return "", false
	}
	if dataString(manifest.Data, "name") != name {
		return "", false
	}
	return dataString(manifest.Data, "version"), true
}

// targetInstall resolves the package a proposal's apply produced and reports
// whether it is live at that proposal's target version — i.e. whether the
// install half of the two-commit apply has landed and still stands.
//
// The receipt answers first. A live vtx.capabilityproposal.<id>.install aspect
// names the package key the apply wrote, so it settles PROVENANCE, which the
// name+version pair cannot: a package at that name and version installed by
// anything else satisfies the pair exactly. The receipted package must still be
// live, still carry the target name, AND still be at the target version — the
// receipt's own installRequestId is caller-supplied and verified by nothing, so
// dropping the version comparison here would let whoever can submit the receipt
// op bind an approved-but-never-applied proposal to any live package of that
// name and have this console close it with no version check at all. Provenance
// NARROWS the heuristic; it does not replace its guard.
//
// A proposal with no receipt falls back to name+version alone: a live package
// whose .manifest name is the proposal's target.packageName at
// targetInstallVersion. So does a proposal whose receipt has gone STALE, but
// that case is flagged rather than laundered — the receipt is standing evidence
// that this proposal's install was some other package, so a name+version hit
// here is a different package than the one it wrote, and closing over it is the
// exact falsified audit record the receipt exists to prevent. Installed stays
// false and the caller is handed what it needs to say so precisely.
//
// It is the single source of that answer for both halves of the flow: apply
// consults it to recognize a proposal it can no longer install, and
// mark-applied consults it to refuse closing a proposal whose install never
// ran. Deriving it once keeps the two from disagreeing about the same state.
func (s *server) targetInstall(ctx context.Context, conn *substrate.Conn, proposalKey string, cols capabilityProposalCols) (installResolution, error) {
	if cols.TargetPackageName == "" {
		return installResolution{}, nil
	}
	get, readErr := coreKVGetter(ctx, conn)

	receipt, haveReceipt, err := readInstallReceipt(get, proposalKey)
	if rErr := readErr(); rErr != nil {
		return installResolution{}, fmt.Errorf("read core-kv install receipt: %w", rErr)
	}
	if err != nil {
		return installResolution{}, err
	}
	if !haveReceipt {
		return resolveByNameAndVersion(ctx, conn, get, readErr, cols)
	}

	version, live := livePackageAt(get, receipt.PackageKey, cols.TargetPackageName)
	if rErr := readErr(); rErr != nil {
		return installResolution{}, fmt.Errorf("read core-kv package catalog: %w", rErr)
	}
	if live && version == targetInstallVersion(cols) {
		return installResolution{
			PackageKey:       receipt.PackageKey,
			Version:          version,
			InstallRequestID: receipt.InstallRequestID,
			Installed:        true,
			ByReceipt:        true,
		}, nil
	}
	// The receipted package cannot answer for this proposal any more. The name
	// scan still runs, because what it finds is what the operator has to be
	// told about — but its hit closes nothing here.
	stale, err := resolveByNameAndVersion(ctx, conn, get, readErr, cols)
	if err != nil {
		return installResolution{}, err
	}
	stale.Installed = false
	stale.InstallRequestID = ""
	stale.ReceiptStale = true
	stale.ReceiptPackageKey = receipt.PackageKey
	stale.ReceiptVersion = version
	return stale, nil
}

// resolveByNameAndVersion is the provenance-blind fallback: the live package
// whose .manifest records the proposal's target.packageName, installed at the
// version the proposal's apply would land. It cannot tell this proposal's
// install from any other write at that name and version — which is why it is
// the fallback and not the answer.
func resolveByNameAndVersion(ctx context.Context, conn *substrate.Conn, get kvGetter, readErr func() error, cols capabilityProposalCols) (installResolution, error) {
	coreKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.package.")
	if err != nil {
		return installResolution{}, fmt.Errorf("list core-kv packages: %w", err)
	}
	key, version, found := findInstalledPackageByName(coreKeys, get, cols.TargetPackageName)
	if rErr := readErr(); rErr != nil {
		return installResolution{}, fmt.Errorf("read core-kv package catalog: %w", rErr)
	}
	return installResolution{
		PackageKey: key,
		Version:    version,
		Installed:  found && version == targetInstallVersion(cols),
	}, nil
}

// unprovenNewPackageClose reports the board's own headline state: a newPackage
// proposal that a live package of its target name and version resolves for, on
// the name+version fallback alone.
//
// A newPackage proposal declares that its apply MINTS the package. So a package
// already sitting at that name is, by that proposal's own declaration, not
// something its apply could have produced unless the apply already ran — and if
// it ran, it left a receipt. The fallback matching therefore means one of two
// things, and cannot say which: this proposal's install committed before
// receipts existed, or some other writer — another proposal, an operator's
// `lattice-pkg install`, a second proposal declaring the same target — put it
// there. Closing on that match binds the proposal to an artifact it may never
// have written, with an appliedAs link and an audit pointer to prove it.
//
// It is not a rare collision either: targetInstallVersion substitutes "0.1.0"
// for a proposal that declares no newVersion, which is the first version most
// hand-installed packages carry.
//
// ApplyCapabilityPlan already reaches the same verdict from the other side — it
// refuses a newPackage whose name was claimed before the apply ran, saying the
// artifact did NOT land — so this only stops the console from advising the
// opposite of the platform it fronts.
//
// upgradeExisting is deliberately excluded. A live package of that name is that
// mode's own PRECONDITION, present before the apply by definition, so its
// presence carries no provenance signal to lose; the version preconditions in
// ValidateCapabilityApplyTarget are what own that mode.
func unprovenNewPackageClose(cols capabilityProposalCols, resolved installResolution) bool {
	return cols.TargetMode == "newPackage" && resolved.Installed && !resolved.ByReceipt
}

// manualCloseRemedy is the one exit from a close this console will not make on
// an operator's behalf: submitting MarkCapabilityProposalApplied themselves,
// naming the package explicitly.
//
// `lattice op submit` is named because it is the only tool that can actually do
// it. `lattice-pkg apply-proposal` cannot: it runs ApplyCapabilityPlan first,
// and for a newPackage proposal whose name is already claimed that returns
// ErrPackageNameClaimed and never reaches the close at all — so pointing an
// operator there would be a remedy that defeats itself.
//
// The four read keys are spelled out rather than left implied: the op hydrates
// from its declared set alone, so a submission missing any of them dies on a
// hydration miss, and an operator following a remedy that fails on its own
// instructions is worse off than one told nothing.
func manualCloseRemedy(id, proposalKey, packageKey string) string {
	return fmt.Sprintf(
		"If you know this proposal's install DID commit, submit the close yourself, naming the package explicitly: "+
			"lattice op submit --operation-type MarkCapabilityProposalApplied "+
			"--payload '{\"proposalId\":\"%s\",\"packageKey\":\"%s\",\"installRequestId\":\"<the requestId of the install op that landed it>\"}' "+
			"--context-hint-reads '%s' — all four read keys are required, because the op hydrates from its declared "+
			"set alone and fails on a hydration miss without them. Naming that package is your decision to make and "+
			"to sign; it is not one this console may guess at.",
		id, packageKey, strings.Join(capabilityCloseReads(proposalKey, packageKey), ","))
}

// unprovenNewPackageReason is what an operator is told instead. It states the
// gap (no receipt binds this proposal to that package), why the match is not
// evidence (anything can install a name), and the one submission that is a
// decision rather than a guess.
func unprovenNewPackageReason(id, proposalKey string, cols capabilityProposalCols, resolved installResolution) string {
	return fmt.Sprintf(
		"%s is installed at version %s as %s, but nothing records that THIS proposal's install produced it: "+
			"no install receipt is recorded against the proposal, and a package of that name and version can have been "+
			"installed by anything — another proposal, or an operator's own `lattice-pkg install`. This proposal declares "+
			"mode newPackage, so closing it over that package would bind it, permanently and in the audit record, to an "+
			"artifact it may never have written. %s",
		cols.TargetPackageName, resolved.Version, resolved.PackageKey,
		manualCloseRemedy(id, proposalKey, resolved.PackageKey))
}

// staleReceiptReason states what a stale receipt actually means for one
// proposal, in place of the "no package of this name is installed at this
// version" a bare fallback miss would report — which for a receipted proposal
// is false twice over: its install DID commit, and a package of that name may
// well be live.
func staleReceiptReason(cols capabilityProposalCols, resolved installResolution) string {
	var head string
	if resolved.ReceiptVersion == "" {
		head = fmt.Sprintf(
			"this proposal's install committed as %s, which has since been uninstalled",
			resolved.ReceiptPackageKey)
	} else {
		head = fmt.Sprintf(
			"this proposal's install committed as %s, which is now at version %s rather than the %s this proposal targets",
			resolved.ReceiptPackageKey, resolved.ReceiptVersion, targetInstallVersion(cols))
	}
	if resolved.PackageKey != "" && resolved.PackageKey != resolved.ReceiptPackageKey {
		return head + fmt.Sprintf(
			" — %s is now held by %s at version %s, which this proposal did not write, so closing it over that package would record an artifact it never produced. This proposal cannot be closed or re-applied; escalate it.",
			cols.TargetPackageName, resolved.PackageKey, resolved.Version)
	}
	return head + " — there is nothing this proposal can honestly be closed over. This proposal cannot be closed or re-applied; escalate it."
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
// (an approved-only transition, a live package + .manifest, a name matching
// this proposal's target.packageName), so this handler's checks exist to refuse
// with an operator-legible reason rather than to be the boundary — with two
// exceptions it owns alone, both turning on facts the op cannot see. It cannot
// check the VERSION, so "a package of this name exists" versus "this package is
// at the version this proposal's apply would land" is decided here or nowhere.
// And it cannot check PROVENANCE: the packageKey reaching it is the caller's
// word, so whether anything actually binds this proposal to that package — the
// .install receipt — is likewise decided here or nowhere.
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

	// Loupe reads the proposal's install receipt and the package catalog
	// straight from Core KV as the console inspector (P5's named exception, the
	// same read handleSystemMap and findOwningPackage already make), scoped to
	// this proposal's own key and the vtx.package. subtree.
	resolved, err := s.targetInstall(ctx, conn, proposalKey, cols)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !resolved.Installed {
		if resolved.ReceiptStale {
			s.writeError(w, http.StatusConflict, staleReceiptReason(cols, resolved))
			return
		}
		s.writeError(w, http.StatusConflict, fmt.Sprintf(
			"no package named %q is installed at version %s — this proposal's install never committed, so run Apply rather than mark-applied",
			cols.TargetPackageName, targetInstallVersion(cols)))
		return
	}

	if unprovenNewPackageClose(cols, resolved) {
		s.writeError(w, http.StatusConflict, unprovenNewPackageReason(id, proposalKey, cols, resolved))
		return
	}

	packageKey := resolved.PackageKey
	// The receipt's pointer is the one the apply's own Processor commit
	// produced, so a recovery that found one stamps the observed op rather than
	// a reconstruction of it.
	installRequestID := resolved.InstallRequestID
	if installRequestID == "" {
		installRequestID = recoveredInstallRequestID(cols.TargetPackageName, targetInstallVersion(cols))
	}
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
		// op-name: (submits) reviewCapabilityMarkApplied submits this from the standalone mark-applied recovery endpoint, closing over an install it independently confirmed already committed (the Apply request's own close never landed) rather than as part of an in-flight apply.
		OperationType: "MarkCapabilityProposalApplied",
		Lane:          string(processor.LaneDefault),
		Payload:       markPayload,
		Reads:         capabilityCloseReads(proposalKey, packageKey),
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
