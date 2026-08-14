// Package capabilitykv factors the Contract #6 §6.2 Capability KV doc parser
// and the class-aware platform-key router out of internal/processor so a
// second reader — the control-plane capability checker
// (control-plane-capability-authz-design.md) — can read the same projection
// through the same key-routing without duplicating it. A leaf package: it
// imports only substrate + encoding/json, never internal/processor, so
// there is no import cycle.
package capabilitykv

import (
	"encoding/json"
)

// Doc is the Processor-side parser shape for Contract #6 §6.2 Capability KV
// entries. The producer is `internal/refractor/capabilityenv.NewWrapper`;
// field names + JSON tags here MUST stay in lockstep with that producer (the
// wrapper builds a map[string]any literal). The contract-conformance test
// (`refractor_capability_multi_e2e_test.go`) anchors the round-trip.
//
// Decisions:
//   - We do NOT use jsonDisallowUnknownFields here. Future contract-additive
//     fields must be readable by older readers without forcing a deploy
//     bump. Strictness lives in the conformance test, not the runtime
//     parser.
//   - ProjectedFromRevisions is a map[string]uint64 on the wire (NATS
//     KV revisions are uint64). The producer emits map[string]any with
//     integer-typed values; encoding/json decodes those into float64
//     when the receiver is `any`, but with a typed uint64 map decoding
//     handles the JSON numbers directly.
type Doc struct {
	Key                    string               `json:"key"`
	Actor                  string               `json:"actor"`
	Version                string               `json:"version"`
	ProjectedAt            string               `json:"projectedAt"`
	ProjectedFromRevisions map[string]uint64    `json:"projectedFromRevisions"`
	Lanes                  []string             `json:"lanes"`
	PlatformPermissions    []PlatformPermission `json:"platformPermissions"`
	ServiceAccess          []ServiceAccessEntry `json:"serviceAccess"`
	EphemeralGrants        []EphemeralGrant     `json:"ephemeralGrants"`
	Roles                  []string             `json:"roles"`
}

// PlatformPermission — Contract #6 §6.4. Standing operation permission
// not scoped to any service.
type PlatformPermission struct {
	OperationType string `json:"operationType"`
	Scope         string `json:"scope"`

	// Lanes optionally names the lane(s) this specific grant authorizes,
	// overriding the doc-level Lanes fallback for a matched op (scoped-
	// privileged-lane-grants-design.md). Absent/empty defers to Doc.Lanes.
	Lanes []string `json:"lanes,omitempty"`

	// Origin is the granting permission vertex's provenance (Contract #6
	// §6.1): `package` for a vertex the installer minted from a declared
	// PermissionSpec, `runtime` for one an actor minted through
	// rbac-domain's CreatePermission. Decode-side only — the value arrives
	// entirely from the projecting lens, never from a Go-side literal.
	//
	// EMPTY MEANS `runtime`, not "unknown". A vertex authored before the
	// stamp existed, or a projection that dropped the field, must be
	// governed by the reserved-set refusal rather than exempted from it,
	// so the consuming check reads absence as the restricted value. Do not
	// "fix" this by defaulting it to `package` on decode.
	Origin string `json:"origin,omitempty"`
}

// ServiceAccessEntry — Contract #6 §6.5. The actor's resolved access to
// one service vertex along with the operations they may invoke.
type ServiceAccessEntry struct {
	Service           string             `json:"service"`
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

// ParseCapabilityDoc decodes the raw NATS KV value into a Doc. Returns an
// error on JSON malformedness or schema-version mismatch.
func ParseCapabilityDoc(raw []byte) (*Doc, error) {
	var d Doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
