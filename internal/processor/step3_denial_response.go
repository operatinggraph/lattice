// Story 3.4 — Structured Denial Response (FR22).
//
// DenialResponseBuilder enriches the OperationReply.Error.Details map for
// auth-denial responses per Contract #2 §2.6 + Contract #6 §6.12 + FR22.
//
// On an AuthDenied denial the builder:
//  1. Populates the standard structural fields: decision, reason, operationType,
//     evaluatedSection, requestId.
//  2. Reads cap.role-by-operation.<operationType> from Capability KV (single GET)
//     to populate rolesCarryingPermission without graph traversal on the hot path.
//  3. Populates actorRoles from the CapabilityDoc.Roles field (already parsed at
//     step 3 — no extra read).
//
// For AuthContextMismatch and AuthFreshnessExceeded the actor-role and
// role-coverage fields are omitted (denial is not about role coverage per AC);
// a diagnosticHint is included instead.
//
// NFR-S6: no other actors' identities, role membership lists, graph paths, or
// internal vertex keys leak through this response. actorRoles uses the raw role
// keys from the actor's own doc; rolesCarryingPermission uses public role names
// only. No per-actor data about other identities is included.
//
// Phase 2 placeholder: escalationPath and routingTo are reserved field names;
// not emitted in Phase 1.
package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/asolgan/lattice/internal/substrate"
)

// RoleByOperationDoc is the shape of cap.role-by-operation.<operationType>
// entries per Contract #6 §6.1 secondary index. Produced by the
// vtx.meta.lens.capabilityRoleIndex Lens.
type RoleByOperationDoc struct {
	Roles       []string `json:"roles"`
	ProjectedAt string   `json:"projectedAt"`
}

// DenialResponseBuilder constructs FR22-structured denial details for
// rejected OperationReply envelopes.
//
// The builder is a thin stateless type (one reader, one bucket, one logger)
// that is called inline from commit_path.go on every auth denial. The KV
// read is performed only on the denial path — allowed operations incur zero
// overhead from this code.
type DenialResponseBuilder struct {
	reader CapabilityReader
	bucket string
	logger *slog.Logger
}

// NewDenialResponseBuilder constructs the builder. Uses the same
// CapabilityReader + bucket as CapabilityAuthorizer — wired by
// cmd/processor/main.go via MakePipeline.
func NewDenialResponseBuilder(reader CapabilityReader, bucket string, logger *slog.Logger) *DenialResponseBuilder {
	if reader == nil {
		panic("processor: DenialResponseBuilder requires a CapabilityReader")
	}
	if bucket == "" {
		panic("processor: DenialResponseBuilder requires a bucket name")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DenialResponseBuilder{
		reader: reader,
		bucket: bucket,
		logger: logger,
	}
}

// DenialDetails is the FR22 structured denial payload placed into
// OperationReply.Error.Details. Always included in the denial reply.
//
// Phase 2 note: escalationPath and routingTo are reserved names for
// Phase 2+ but are not emitted in Phase 1 (per AC and Contract §6.12).
type DenialDetails struct {
	// Standard fields — always present.
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	OperationType string `json:"operationType"`
	RequestID     string `json:"requestId"`

	// EvaluatedSection — which permission section was examined.
	// One of "platformPermissions", "serviceAccess", "ephemeralGrants".
	// Omitted for NoCapabilityEntry (no doc to evaluate) and
	// AuthFreshnessExceeded (freshness check precedes dispatch).
	EvaluatedSection string `json:"evaluatedSection,omitempty"`

	// Role-coverage fields — present for AuthDenied/OperationNotPermitted denials only.
	// Omitted for AuthContextMismatch and AuthFreshnessExceeded per AC.
	ActorRoles              []string `json:"actorRoles,omitempty"`
	RolesCarryingPermission []string `json:"rolesCarryingPermission,omitempty"`

	// DiagnosticHint — operator-actionable text for AuthContextMismatch +
	// AuthFreshnessExceeded where role-coverage context is inapplicable.
	DiagnosticHint string `json:"diagnosticHint,omitempty"`
}

// BuildDenialDetails constructs the FR22 DenialDetails for an auth denial.
// `doc` may be nil when the denial was NoCapabilityEntry (no doc parsed).
//
// The method performs a single KV GET for cap.role-by-operation.<operationType>
// when the denial requires role-coverage information. The GET is suppressed for
// AuthContextMismatch and AuthFreshnessExceeded (per AC — those denials omit
// role-coverage fields).
func (b *DenialResponseBuilder) BuildDenialDetails(
	ctx context.Context,
	env *OperationEnvelope,
	dec Decision,
	doc *CapabilityDoc, // may be nil for NoCapabilityEntry
) DenialDetails {
	details := DenialDetails{
		Decision:      "denied",
		Reason:        denialReason(dec),
		OperationType: env.OperationType,
		RequestID:     env.RequestID,
	}

	switch dec.Code {
	case ErrCodeAuthContextMismatch:
		details.DiagnosticHint = diagnosticHintForMismatch(dec, env)
		return details

	case ErrCodeAuthFreshnessExceeded:
		details.DiagnosticHint = fmt.Sprintf(
			"The Capability KV projection for actor %q is too stale. "+
				"Wait for the Refractor to re-project the actor's permissions before retrying.",
			env.Actor,
		)
		return details
	}

	// AuthDenied paths — include evaluated section and role-coverage.
	details.EvaluatedSection = resolveEvaluatedSection(dec, doc, env)

	// actorRoles: sourced from the parsed CapabilityDoc.Roles (no fresh read).
	// If no doc (NoCapabilityEntry), return empty slice (nothing to report).
	if doc != nil && len(doc.Roles) > 0 {
		details.ActorRoles = doc.Roles
	} else {
		details.ActorRoles = []string{}
	}

	// rolesCarryingPermission: single GET from the role-by-operation index.
	details.RolesCarryingPermission = b.fetchRolesCarryingPermission(ctx, env.OperationType)

	return details
}

// denialReason maps the Decision to the canonical FR22 reason string.
// The AC enumerates: NoCapabilityEntry, OperationNotPermitted, AuthContextMismatch,
// AuthFreshnessExceeded.
func denialReason(dec Decision) string {
	switch {
	case dec.Reason == "NoCapabilityEntry":
		return "NoCapabilityEntry"
	case dec.Code == ErrCodeAuthContextMismatch:
		return "AuthContextMismatch"
	case dec.Code == ErrCodeAuthFreshnessExceeded:
		return "AuthFreshnessExceeded"
	default:
		// All other AuthDenied reasons normalise to OperationNotPermitted.
		return "OperationNotPermitted"
	}
}

// resolveEvaluatedSection determines which Capability KV section was examined.
// If the Decision carries a Resolved path (can happen when dispatch reached the
// section and found an op-mismatch), use it. Otherwise infer from authContext.
func resolveEvaluatedSection(dec Decision, _ *CapabilityDoc, env *OperationEnvelope) string {
	// Resolved path — set by dispatch even on deny (e.g., service-found/op-absent
	// sets resolved.Path="service" in a future enhancement; today Resolved is nil
	// on all denials per Story 3.3 AC).
	if dec.Resolved != nil && dec.Resolved.Path != "" {
		return pathToSection(dec.Resolved.Path)
	}
	// Infer from authContext.
	if env.AuthContext != nil && env.AuthContext.Task != "" {
		return "ephemeralGrants"
	}
	if env.AuthContext != nil && env.AuthContext.Service != "" {
		return "serviceAccess"
	}
	// Platform path or NoCapabilityEntry.
	if dec.Reason == "NoCapabilityEntry" {
		return "" // no section evaluated
	}
	return "platformPermissions"
}

func pathToSection(path string) string {
	switch path {
	case "platform":
		return "platformPermissions"
	case "service":
		return "serviceAccess"
	case "task":
		return "ephemeralGrants"
	}
	return ""
}

// fetchRolesCarryingPermission reads cap.role-by-operation.<operationType>
// from Capability KV. Returns [] on missing key or any read failure (both are
// non-fatal for the denial path — the denial itself is already being returned).
func (b *DenialResponseBuilder) fetchRolesCarryingPermission(ctx context.Context, operationType string) []string {
	key := "cap.role-by-operation." + operationType
	entry, err := b.reader.KVGet(ctx, b.bucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// Missing index key — operation type unknown or recently deprecated.
			// Return empty per AC.
			b.logger.Debug("denial response: no role-by-operation index for operation",
				"operationType", operationType)
			return []string{}
		}
		// Infrastructure failure — log and return empty rather than propagating
		// (the auth denial is already underway; this is observability-only).
		b.logger.Warn("denial response: failed to read role-by-operation index",
			"key", key, "error", err)
		return []string{}
	}
	var doc RoleByOperationDoc
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		b.logger.Warn("denial response: failed to parse role-by-operation doc",
			"key", key, "error", err)
		return []string{}
	}
	if doc.Roles == nil {
		return []string{}
	}
	return doc.Roles
}

// diagnosticHintForMismatch returns an operator-actionable hint for
// AuthContextMismatch denials.
func diagnosticHintForMismatch(dec Decision, env *OperationEnvelope) string {
	ac := env.AuthContext
	if ac != nil && ac.Service != "" && ac.Task != "" {
		return "authContext.service and authContext.task are mutually exclusive; " +
			"set exactly one for the appropriate auth path."
	}
	if ac != nil && ac.Task != "" {
		return fmt.Sprintf(
			"No matching ephemeral grant for task %q on operation %q targeting %q. "+
				"Verify the task is active, the target matches the grant, and the grant has not expired.",
			ac.Task, env.OperationType, ac.Target,
		)
	}
	if ac != nil && ac.Service != "" {
		return fmt.Sprintf(
			"Service %q is not present in the actor's Capability KV projection. "+
				"Verify the actor has an active service-access grant for this service "+
				"and the Refractor has projected it.",
			ac.Service,
		)
	}
	// Platform-path mismatch (self/specific scope issues).
	return dec.Reason
}

// DenialDetailsAsMap converts DenialDetails to a map[string]any for use in
// BuildRejectedReply's details parameter. This avoids changing the signature
// of BuildRejectedReply while allowing structured FR22 fields.
func DenialDetailsAsMap(d DenialDetails) map[string]any {
	// Marshal → unmarshal via JSON to produce a generic map. The round-trip
	// is on the denial path only (not the hot committed path) and is
	// acceptably cheap compared to the KV GET that preceded it.
	raw, err := json.Marshal(d)
	if err != nil {
		// Fallback: minimal map with the essential fields.
		return map[string]any{
			"decision":      d.Decision,
			"reason":        d.Reason,
			"operationType": d.OperationType,
			"requestId":     d.RequestID,
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{
			"decision":      d.Decision,
			"reason":        d.Reason,
			"operationType": d.OperationType,
			"requestId":     d.RequestID,
		}
	}
	return m
}
