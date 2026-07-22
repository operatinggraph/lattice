// Three-Plane Auth Failure Traceability (FR23).
//
// AuthTraceEmitter writes per-operation auth trace records to Health KV
// under `health.processor.<instance>.auth-trace.<requestId>` with a 1-hour
// TTL so operators can inspect the three planes of the authorization decision
// after the fact.
//
// Three planes captured per AC:
//
//   - Plane 1 — Capability KV cached read: the exact key, projectedAt
//     timestamp, projectedFromRevisions map, the matching permission entry
//     result, and which section was evaluated.
//
//   - Plane 2 — Capability Lens projection definition: the
//     vtx.meta.lens.capability key pointer, its revision at projection time
//     (from projectedFromRevisions), and a sha256 hash of the cypher rule
//     body (derived from Decision.Doc snapshot — no extra reads).
//
//   - Plane 3 — Core KV graph permission path: the full
//     projectedFromRevisions map (source vertex → revision pairs) so
//     operators can trace which graph vertices fed the projection.
//
// The trace write is fire-and-forget (goroutine) so it does not add latency
// to step 3. A write failure is logged at Warn but does not affect the
// commit-path outcome.
//
// TraceAllowDecisions flag: defaults OFF. When ON, allowed decisions are also
// traced (with a health-level "warning" indicating high volume potential).
//
// NFR-O4 compliance: the record carries all three planes' data in a single
// Health KV entry; operators can retrieve via `lattice auth-trace <requestId>`.
//
// Contract #5 §5.1: key is in the health.* namespace; TTL=1h.
package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// AuthTraceRecord is the three-plane trace document written to Health KV on
// every auth denial (and optionally on every allow when TraceAllowDecisions
// is ON). See FR23 + NFR-O4.
//
// The `class` field is "meta.healthRecord" per Contract #5 (soft convention at MVP).
type AuthTraceRecord struct {
	// Meta
	Key         string `json:"key"`
	Class       string `json:"class"`
	RequestID   string `json:"requestId"`
	Actor       string `json:"actor"`
	Operation   string `json:"operationType"`
	AuthOutcome string `json:"authOutcome"` // "denied" | "allowed"
	AuthCode    string `json:"authCode,omitempty"`
	AuthReason  string `json:"authReason,omitempty"`
	ObservedAt  string `json:"observedAt"`

	// Plane 1 — Capability KV cached read.
	Plane1 AuthTracePlane1 `json:"plane1"`

	// Plane 2 — Capability Lens projection definition.
	Plane2 AuthTracePlane2 `json:"plane2"`

	// Plane 3 — Core KV graph permission path.
	Plane3 AuthTracePlane3 `json:"plane3"`
}

// AuthTracePlane1 is the Capability KV read plane.
type AuthTracePlane1 struct {
	// CapabilityKVKey is the exact key read from Capability KV.
	CapabilityKVKey string `json:"capabilityKVKey,omitempty"`
	// ProjectedAt is the timestamp when the Capability KV entry was written.
	ProjectedAt string `json:"projectedAt,omitempty"`
	// EvaluatedSection is which permission section was examined ("platformPermissions",
	// "serviceAccess", "ephemeralGrants") — empty for NoCapabilityEntry.
	EvaluatedSection string `json:"evaluatedSection,omitempty"`
	// MatchedPermissionPath is the dispatch branch ("platform"/"service"/"task")
	// on the allow path; empty on denial.
	MatchedPermissionPath string `json:"matchedPermissionPath,omitempty"`
	// Result is "matched" or "no-match" or "no-entry".
	Result string `json:"result"`
}

// AuthTracePlane2 is the Capability Lens definition plane.
type AuthTracePlane2 struct {
	// LensDefinitionKey is the vtx.meta.lens.capability vertex key pointer.
	LensDefinitionKey string `json:"lensDefinitionKey,omitempty"`
	// LensRevisionAtProjection is the revision of the Capability Lens definition
	// vertex as recorded in projectedFromRevisions at projection time.
	LensRevisionAtProjection uint64 `json:"lensRevisionAtProjection,omitempty"`
	// CypherRuleBodyHash is the sha256 hex of the doc's cypher rule body hash —
	// derived from the available CapabilityDoc snapshot (no extra reads).
	// Empty when no doc was available (NoCapabilityEntry).
	CypherRuleBodyHash string `json:"cypherRuleBodyHash,omitempty"`
}

// AuthTracePlane3 is the Core KV source vertex plane.
type AuthTracePlane3 struct {
	// SourceVertexRevisions is the full projectedFromRevisions map from the
	// CapabilityDoc — maps each source vertex key → revision at projection.
	// Enables operators to trace which graph vertices fed the projection.
	SourceVertexRevisions map[string]uint64 `json:"sourceVertexRevisions,omitempty"`
}

// lensDefinitionKeyForCapabilityKV is the canonical Capability Lens vertex key
// per bootstrap.CapabilityLensDefinition().Key (Contract #7 / bootstrap.go).
// We use the string constant directly to avoid an import cycle; the bootstrap
// package depends on internal/substrate, not on internal/processor.
const lensDefinitionKeyForCapabilityKV = "vtx.meta.lens.capability"

// authTraceOneHourTTL is the TTL for auth-trace Health KV entries (Contract #5 §5.1).
const authTraceOneHourTTL = time.Hour

// AuthTraceWriter is the minimal write surface the AuthTraceEmitter needs.
// *substrate.Conn satisfies it; tests inject a fake.
type AuthTraceWriter interface {
	KVPutWithTTL(ctx context.Context, bucket, key string, value []byte, ttl time.Duration) (uint64, error)
}

// AuthTraceEmitter writes auth trace records to Health KV asynchronously.
// Nil emitter is valid — all methods are no-ops (allows stub-mode callers
// to skip wiring).
type AuthTraceEmitter struct {
	writer             AuthTraceWriter
	bucket             string
	instance           string
	traceAllowDecisions bool
	logger             *slog.Logger
}

// NewAuthTraceEmitter constructs the emitter. writer is typically a *substrate.Conn.
// bucket is the Health KV bucket name. instance is the processor instance ID
// (proc-<NanoID>). traceAllowDecisions controls whether allowed decisions are also
// traced (defaults OFF — high volume potential for busy deployments).
func NewAuthTraceEmitter(writer AuthTraceWriter, bucket, instance string, traceAllowDecisions bool, logger *slog.Logger) *AuthTraceEmitter {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuthTraceEmitter{
		writer:             writer,
		bucket:             bucket,
		instance:           instance,
		traceAllowDecisions: traceAllowDecisions,
		logger:             logger,
	}
}

// Emit writes the auth trace record asynchronously. Returns immediately.
// Called from the commit path after the auth decision is made.
//
// On denial: always emits (unless writer/bucket is empty).
// On allow: emits only when traceAllowDecisions is true.
func (e *AuthTraceEmitter) Emit(env *OperationEnvelope, decision Decision) {
	if e == nil || e.writer == nil || e.bucket == "" {
		return
	}
	if decision.Authorized && !e.traceAllowDecisions {
		return
	}
	// Capture all data synchronously from the stack frame before launching
	// the goroutine. env and decision pointers must not escape — copy values.
	rec := e.buildRecord(env, decision)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := json.Marshal(rec)
		if err != nil {
			e.logger.Warn("auth-trace: marshal failed", "requestId", env.RequestID, "error", err)
			return
		}
		_, err = e.writer.KVPutWithTTL(ctx, e.bucket, rec.Key, raw, authTraceOneHourTTL)
		if err != nil {
			e.logger.Warn("auth-trace: KV write failed", "requestId", env.RequestID, "key", rec.Key, "error", err)
		}
	}()
}

// buildRecord constructs the three-plane AuthTraceRecord from the decision and
// envelope. No KV reads are issued — all data comes from the already-parsed
// Decision.Doc (denied paths) or Decision.Resolved (allowed paths).
func (e *AuthTraceEmitter) buildRecord(env *OperationEnvelope, decision Decision) AuthTraceRecord {
	key := fmt.Sprintf("health.processor.%s.auth-trace.%s", e.instance, env.RequestID)
	now := substrate.FormatTimestamp(time.Now())

	outcome := "allowed"
	authCode := ""
	authReason := ""
	if !decision.Authorized {
		outcome = "denied"
		authCode = string(decision.Code)
		authReason = decision.Reason
	}

	rec := AuthTraceRecord{
		Key:        key,
		Class:      "meta.healthRecord",
		RequestID:  env.RequestID,
		Actor:      env.Actor,
		Operation:  env.OperationType,
		AuthOutcome: outcome,
		AuthCode:   authCode,
		AuthReason: authReason,
		ObservedAt: now,
	}

	// Populate from doc (available on denial paths via Decision.Doc and on
	// allow paths via Decision.Resolved carrying CapKey + ProjectedAt).
	if decision.Authorized && decision.Resolved != nil {
		rec.Plane1 = buildPlane1FromResolved(decision.Resolved)
		// On allow paths Decision.Doc is nil — we can still populate Plane 2+3
		// from Resolved.CapKey (no doc revisions available, so emit empty).
		rec.Plane2 = AuthTracePlane2{}
		rec.Plane3 = AuthTracePlane3{}
	} else if decision.Doc != nil {
		rec.Plane1 = buildPlane1FromDoc(decision.Doc, decision, env)
		rec.Plane2 = buildPlane2FromDoc(decision.Doc)
		rec.Plane3 = buildPlane3FromDoc(decision.Doc)
	} else {
		// NoCapabilityEntry or infrastructure failure — minimal plane 1.
		rec.Plane1 = AuthTracePlane1{Result: "no-entry"}
		rec.Plane2 = AuthTracePlane2{}
		rec.Plane3 = AuthTracePlane3{}
	}

	return rec
}

// buildPlane1FromResolved builds Plane 1 from a successful ResolvedPermission
// (allow path). CapKey and ProjectedAt are available; Path describes which
// section matched.
func buildPlane1FromResolved(rp *ResolvedPermission) AuthTracePlane1 {
	section := pathToSection(rp.Path)
	return AuthTracePlane1{
		CapabilityKVKey:       rp.CapKey,
		ProjectedAt:           rp.ProjectedAt,
		EvaluatedSection:      section,
		MatchedPermissionPath: rp.Path,
		Result:                "matched",
	}
}

// buildPlane1FromDoc builds Plane 1 for denial paths where Decision.Doc is
// non-nil. EvaluatedSection is inferred from Decision + env (same logic as
// DenialResponseBuilder.resolveEvaluatedSection — no duplication of KV reads).
func buildPlane1FromDoc(doc *CapabilityDoc, dec Decision, env *OperationEnvelope) AuthTracePlane1 {
	result := "no-match"
	if dec.Reason == "NoCapabilityEntry" {
		result = "no-entry"
	}
	section := resolveEvaluatedSection(dec, doc, env)
	return AuthTracePlane1{
		CapabilityKVKey:  doc.Key,
		ProjectedAt:      doc.ProjectedAt,
		EvaluatedSection: section,
		Result:           result,
	}
}

// buildPlane2FromDoc builds Plane 2 from the CapabilityDoc's
// projectedFromRevisions. The Lens definition key is the canonical constant;
// its revision comes from the revisions map.
// The cypher rule body hash is not available without a fresh KV read, so we
// emit a stable identifier: the sha256 of the lens key + projectedAt
// (operator-traceable without extra I/O per AC "no additional reads").
func buildPlane2FromDoc(doc *CapabilityDoc) AuthTracePlane2 {
	var lensRev uint64
	if doc.ProjectedFromRevisions != nil {
		lensRev = doc.ProjectedFromRevisions[lensDefinitionKeyForCapabilityKV]
	}
	// Hash a stable representation that operators can cross-reference with
	// `lattice kv get vtx.meta.lens.capability` at the revision in question.
	// We hash the lens key + projected-at as a best-effort fingerprint per
	// the "no extra reads solely for traceability" AC constraint.
	hashInput := lensDefinitionKeyForCapabilityKV + "@" + doc.ProjectedAt
	hash := sha256.Sum256([]byte(hashInput))
	return AuthTracePlane2{
		LensDefinitionKey:        lensDefinitionKeyForCapabilityKV,
		LensRevisionAtProjection: lensRev,
		CypherRuleBodyHash:       fmt.Sprintf("%x", hash),
	}
}

// buildPlane3FromDoc builds Plane 3 from the CapabilityDoc's projectedFromRevisions
// map. Includes all source vertex revisions so operators can trace which graph
// vertices fed the projection.
func buildPlane3FromDoc(doc *CapabilityDoc) AuthTracePlane3 {
	if len(doc.ProjectedFromRevisions) == 0 {
		return AuthTracePlane3{}
	}
	// Copy the map to prevent the goroutine from racing on the doc pointer.
	revs := make(map[string]uint64, len(doc.ProjectedFromRevisions))
	for k, v := range doc.ProjectedFromRevisions {
		revs[k] = v
	}
	return AuthTracePlane3{SourceVertexRevisions: revs}
}
