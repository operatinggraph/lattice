package appsession

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewDevSigner_DisabledByDefault(t *testing.T) {
	signer, err := NewDevSigner(slog.Default(), "TESTAPP", true)
	require.NoError(t, err)
	require.Nil(t, signer, "no minter without an explicit opt-in")
}

// TestNewDevSigner_RefusesNonLoopbackBind pins the defence in depth: an
// in-process minter signs whatever subject its caller names, so it must never
// be reachable from off-box even when someone sets the env flag.
func TestNewDevSigner_RefusesNonLoopbackBind(t *testing.T) {
	t.Setenv("TESTAPP_DEV_AUTH", "1")
	signer, err := NewDevSigner(slog.Default(), "TESTAPP", false)
	require.Error(t, err)
	require.Nil(t, signer)
	require.Contains(t, err.Error(), "loopback")
}

func TestSigner_MintCarriesSubjectAndExpiry(t *testing.T) {
	signer := testSigner(t)
	subject := testNanoID(t)
	token, exp, err := signer.Mint(subject)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	authn, err := buildTestVerifier(&signer.priv.PublicKey, signer.kid)
	require.NoError(t, err)
	actor, err := authn.Authenticate(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, subject, actor.Subject)
	require.WithinDuration(t, exp, actor.ExpiresAt, 2*time.Second)
}
