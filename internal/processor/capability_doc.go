package processor

import (
	"encoding/json"
)

// CapabilityDoc is the Processor-side parser shape for Contract #6 §6.2
// Capability KV entries. The producer is
// `internal/refractor/capabilityenv.NewWrapper`; field names + JSON tags
// here MUST stay in lockstep with that producer (the wrapper builds a
// map[string]any literal). Story 3.2b's contract-conformance test
// (`refractor_capability_multi_e2e_test.go`) anchors the round-trip.
//
// Decisions:
//   - We do NOT use jsonDisallowUnknownFields here. Future contract-additive
//     fields must be readable by older Processors without forcing a deploy
//     bump. Strictness lives in the conformance test, not the runtime
//     parser.
//   - ProjectedFromRevisions is a map[string]uint64 on the wire (NATS
//     KV revisions are uint64). The producer emits map[string]any with
//     integer-typed values; encoding/json decodes those into float64
//     when the receiver is `any`, but with a typed uint64 map decoding
//     handles the JSON numbers directly.
type CapabilityDoc struct {
	Key                    string                  `json:"key"`
	Actor                  string                  `json:"actor"`
	Version                string                  `json:"version"`
	ProjectedAt            string                  `json:"projectedAt"`
	ProjectedFromRevisions map[string]uint64       `json:"projectedFromRevisions"`
	Lanes                  []string                `json:"lanes"`
	PlatformPermissions    []PlatformPermission    `json:"platformPermissions"`
	ServiceAccess          []ServiceAccessEntry    `json:"serviceAccess"`
	EphemeralGrants        []EphemeralGrant        `json:"ephemeralGrants"`
	Roles                  []string                `json:"roles"`
}

// PlatformPermission — Contract #6 §6.4. Standing operation permission
// not scoped to any service.
type PlatformPermission struct {
	OperationType string `json:"operationType"`
	Scope         string `json:"scope"`
}

// ServiceAccessEntry — Contract #6 §6.5. The actor's resolved access to
// one service vertex along with the operations they may invoke.
type ServiceAccessEntry struct {
	Service           string             `json:"service"`
	ServiceClass      string             `json:"serviceClass"`
	ResolvedVia       []string           `json:"resolvedVia"`
	AllowedOperations []AllowedOperation `json:"allowedOperations"`
}

// AllowedOperation is one entry under serviceAccess[].allowedOperations[].
type AllowedOperation struct {
	OperationType string `json:"operationType"`
}

// EphemeralGrant — Contract #6 §6.6. Time-bounded, target-specific grant
// derived from a task assignment (FR56).
type EphemeralGrant struct {
	Source        string `json:"source"`
	TaskKey       string `json:"taskKey"`
	OperationType string `json:"operationType"`
	Target        string `json:"target"`
	ExpiresAt     string `json:"expiresAt"`
}

// ParseCapabilityDoc decodes the raw NATS KV value into a CapabilityDoc.
// Returns an error on JSON malformedness or schema-version mismatch.
func ParseCapabilityDoc(raw []byte) (*CapabilityDoc, error) {
	var d CapabilityDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
