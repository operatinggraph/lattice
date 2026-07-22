package processor

import (
	"github.com/operatinggraph/lattice/internal/capabilitykv"
)

// CapabilityDoc, PlatformPermission, ServiceAccessEntry, AllowedOperation,
// and EphemeralGrant are aliases onto internal/capabilitykv's Contract #6
// §6.2 parser types — factored out so the control-plane capability checker
// (control-plane-capability-authz-design.md) reads the same projection
// through the same parser without a second, divergent definition. The alias
// keeps every existing `processor.CapabilityDoc` reference (in this package
// and its consumers) valid unchanged.
type (
	CapabilityDoc      = capabilitykv.Doc
	PlatformPermission = capabilitykv.PlatformPermission
	ServiceAccessEntry = capabilitykv.ServiceAccessEntry
	AllowedOperation   = capabilitykv.AllowedOperation
	EphemeralGrant     = capabilitykv.EphemeralGrant
)

// ParseCapabilityDoc decodes the raw NATS KV value into a CapabilityDoc.
// Returns an error on JSON malformedness or schema-version mismatch.
func ParseCapabilityDoc(raw []byte) (*CapabilityDoc, error) {
	return capabilitykv.ParseCapabilityDoc(raw)
}
