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

	err := revocationRejection(processor.ErrCodeProtectedKey, "ProtectedKey: tombstone on "+linkKey, "RevokeRole", linkKey)
	require.Error(t, err)
	require.Contains(t, err.Error(), "epoch skew")
	require.Contains(t, err.Error(), linkKey, "the diagnosis must name the edge that was refused")
	require.Contains(t, err.Error(), "RevokeRole", "the diagnosis must name the verb that was refused")
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
		err := revocationRejection(code, "some message", "RevokePermission", "lnk.identity.Kd4rTm7pXb2wQn9jFv3s.holdsRole.role.Rp8kWn3tYb5mQx2vJd7h")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "epoch skew", "code %s must not be diagnosed as epoch skew", code)
		require.Contains(t, err.Error(), string(code))
		require.Contains(t, err.Error(), "some message")
	}
}

// A rejected reply is not obliged to carry an error object, and the caller reads
// code and message off a nil one — so the empty pair reaches here. The scan
// revokes many edges in one run, so a rendering that carried only those two
// would name nothing at all: the operator would be told a revocation failed
// without being told which. The op and the link key are therefore not part of
// the diagnosis, they are the identity of the failure.
func TestRevocationRejection_NamesTheOpAndEdgeWithNoCodeOrMessage(t *testing.T) {
	linkKey := "lnk.permission.Pm5tXk9wRb3nQv7jHd2y.grantedBy.role.Rp8kWn3tYb5mQx2vJd7h"

	err := revocationRejection(processor.ErrorCode(""), "", "RevokePermission", linkKey)
	require.Error(t, err)
	require.Contains(t, err.Error(), linkKey, "an empty reply error must still name the edge that was refused")
	require.Contains(t, err.Error(), "RevokePermission", "an empty reply error must still name the verb")
	require.Contains(t, err.Error(), "no code", "the absent code must read as absent, not as an empty field")
	require.Contains(t, err.Error(), "no message", "the absent message must read as absent, not as an empty field")

	// A whitespace-only message is the same absence wearing a different
	// costume — a reply built with a blank string renders identically.
	blank := revocationRejection(processor.ErrorCode(""), "   ", "RevokeRole", linkKey)
	require.Contains(t, blank.Error(), "no message")
}
