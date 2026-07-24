package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// sessionCookieName is what the kit derives for this binary — asserted here
// so a rename of appName (or of the kit's derivation) surfaces as a failing
// test rather than as every browser silently signing out.
const sessionCookieName = "facet_session"

// testDevSigner builds a Signer with a throwaway key (never the checked-in
// shared dev key — these tests never verify a JWT against the real trust
// root, only that the handlers build the right envelopes).
func testDevSigner(t *testing.T) *appsession.Signer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return appsession.NewSigner(priv, "test", appsession.DevTokenTTL, time.Now)
}

func testNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

// buildTestVerifier builds an auth.Authenticator trusting pub under kid,
// bound ModeNanoID — the same binding mode the real dev key uses, so a token
// minted for a valid NanoID subject verifies exactly as it would against the
// checked-in shared dev key.
func buildTestVerifier(pub *rsa.PublicKey, kid string) (*auth.Authenticator, error) {
	v, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{kid: pub},
		KeyInfo: map[string]auth.KeyInfo{kid: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		return nil, err
	}
	return auth.NewAuthenticator(v, nil), nil
}

// testSession builds the session kit as run() wires it for this binary, so a
// handler test exercises the same exemptions, cookie name, and persona
// posture production does. mutate may override any field.
func testSession(t *testing.T, mutate func(*appsession.Config)) *appsession.Manager {
	t.Helper()
	cfg := appsession.Config{
		AppName:          appName,
		EnvPrefix:        envPrefix,
		Logger:           slog.Default(),
		GatewayURL:       "http://gw.example:8080",
		LoginPage:        []byte("<html>login</html>"),
		Loopback:         true,
		ExtraExemptPaths: []string{"/api/claim"},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	m, err := appsession.New(cfg)
	require.NoError(t, err)
	return m
}

// withBootIdentity sets the boot-env single-user fallback on both halves that
// production wires from one value in run(): the server's own field and the
// session kit's fallback.
func withBootIdentity(t *testing.T, srv *server, identityID string) {
	t.Helper()
	srv.bootIdentityID = identityID
	srv.session = testSession(t, func(c *appsession.Config) { c.FallbackIdentityID = identityID })
}

func TestSessionCookieName_MatchesTheKitDerivation(t *testing.T) {
	require.Equal(t, sessionCookieName, testSession(t, nil).CookieName())
}
