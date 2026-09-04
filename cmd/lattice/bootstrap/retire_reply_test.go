package bootstrap

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/processor"
)

// A ProtectedKey rejection of a stranded-epoch revocation has exactly one
// cause: a Processor replica that has not restarted since lattice.bootstrap.json
// was regenerated, still holding the retired epoch's edges as its own protected
// set. Rendered as a generic rejection it reads as "this retirement is
// impossible", which is the wrong conclusion and the wrong action; the epoch-skew
// wording names the remedy.
func TestRevocationRejection_ProtectedKeyIsReportedAsEpochSkew(t *testing.T) {
	linkKey := "lnk.identity.Kd4rTm7pXb2wQn9jFv3s.holdsRole.role.Rp8kWn3tYb5mQx2vJd7h"

	err := revocationRejection(processor.ErrCodeProtectedKey, "ProtectedKey: tombstone on "+linkKey, linkKey)
	require.Error(t, err)
	require.Contains(t, err.Error(), "epoch skew")
	require.Contains(t, err.Error(), linkKey, "the diagnosis must name the edge that was refused")
	require.Contains(t, strings.ToLower(err.Error()), "restart every processor replica",
		"the diagnosis must name the operator action that clears the skew")
}

// Every other rejection code keeps the plain rendering — the epoch-skew reading
// is a claim about ONE code, and attaching it to an AuthDenied or a
// RevisionConflict would send the operator to roll a fleet over an unrelated
// failure.
func TestRevocationRejection_OtherCodesStayGeneric(t *testing.T) {
	for _, code := range []processor.ErrorCode{
		processor.ErrCodeAuthDenied,
		processor.ErrCodeRevisionConflict,
		processor.ErrCodeDDLViolation,
		processor.ErrCodePackageScope,
	} {
		err := revocationRejection(code, "some message", "lnk.identity.Kd4rTm7pXb2wQn9jFv3s.holdsRole.role.Rp8kWn3tYb5mQx2vJd7h")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "epoch skew", "code %s must not be diagnosed as epoch skew", code)
		require.Contains(t, err.Error(), string(code))
		require.Contains(t, err.Error(), "some message")
	}
}
