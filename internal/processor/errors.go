package processor

import (
	"errors"
	"fmt"
)

// errUnknownAuthMode wraps the offending mode value.
func errUnknownAuthMode(m AuthMode) error {
	return fmt.Errorf("processor: unknown LATTICE_AUTH_MODE %q (expected 'stub' or 'capability')", m)
}

// errCapabilityModeRequiresReader fires at startup when capability mode
// is selected without a CapabilityReader + bucket pair. Better to fail
// loud than to silently degrade.
var errCapabilityModeRequiresReader = errors.New(
	"processor: LATTICE_AUTH_MODE=capability requires SelectAuthorizerOpts.Reader and CapabilityBucket")
