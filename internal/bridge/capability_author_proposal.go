package bridge

import (
	"encoding/json"
	"fmt"
)

// CapabilityAuthorTarget names where a proposed artifact would install
// (design ai-authored-capabilities-design.md §3.1's .target aspect shape).
type CapabilityAuthorTarget struct {
	Mode        string `json:"mode"`
	PackageName string `json:"packageName,omitempty"`
	BaseVersion string `json:"baseVersion,omitempty"`
	NewVersion  string `json:"newVersion,omitempty"`
}

// CapabilityAuthorValidation carries the §5 record-time deterministic-
// validation verdict — ALREADY COMPUTED by the trusted caller (the bridge, in
// the full design; a test harness's own pkgmgr.ValidateCapabilityArtifact call
// today) before RecordCapabilityProposal is submitted. The DDL script trusts
// this verdict verbatim; it never re-runs the parser/validateAll itself (no
// parser/registry access from Starlark).
type CapabilityAuthorValidation struct {
	State        string `json:"state"`
	Report       string `json:"report,omitempty"`
	DeltaPreview string `json:"deltaPreview,omitempty"`
}

// CapabilityAuthorProvenance records exactly what was reasoned over, for the
// audit trail + stale-proposal detection.
type CapabilityAuthorProvenance struct {
	Model       string `json:"model,omitempty"`
	PromptHash  string `json:"promptHash,omitempty"`
	CatalogHash string `json:"catalogHash,omitempty"`
	ReasonedAt  string `json:"reasonedAt,omitempty"`
}

// CapabilityAuthorProposal is the model's STRUCTURED OUTPUT for one
// AI-authored-capabilities reasoning call — the artifact Claude proposes for a
// capability request. It is the payload a `capabilityAuthor` adapter returns,
// carried verbatim in the bridge Dispatch's Result.Detail string (the bridge
// treats Detail as opaque). Kind/Content is the proposed artifact itself
// (design §3.2's deterministic-validatability spine — "lens" only in this
// fire); Target names where it would install; Validation is the
// ALREADY-COMPUTED §5 verdict (see CapabilityAuthorValidation); Provenance
// records the reasoning context. Mirrors AugurProposal's shape/role exactly.
type CapabilityAuthorProposal struct {
	Kind       string                     `json:"kind"`
	Content    string                     `json:"content"`
	Target     CapabilityAuthorTarget     `json:"target"`
	Rationale  string                     `json:"rationale,omitempty"`
	Confidence float64                    `json:"confidence"`
	Validation CapabilityAuthorValidation `json:"validation"`
	Provenance CapabilityAuthorProvenance `json:"provenance,omitempty"`
}

// Encode marshals the proposal to the JSON string the bridge carries in
// Result.Detail (the RecordCapabilityProposal `result` payload field). It
// never fails for a well-formed CapabilityAuthorProposal; a marshal error is
// surfaced rather than silently producing a blank Detail, so a wiring bug is
// loud.
func (p CapabilityAuthorProposal) Encode() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("bridge: encode capability-author proposal: %w", err)
	}
	return string(b), nil
}
