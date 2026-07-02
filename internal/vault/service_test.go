package vault_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/vault"
)

// startTestServer starts an in-memory JetStream-enabled NATS server and
// returns a connected *nats.Conn.
func startTestServer(t *testing.T) *nats.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func sendDecrypt(t *testing.T, nc *nats.Conn, req vault.DecryptRequest) vault.DecryptResponse {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	reply, err := nc.Request(vault.DecryptSubject, data, 2*time.Second)
	require.NoError(t, err, "NATS request to %s must succeed", vault.DecryptSubject)
	var resp vault.DecryptResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

func TestService_Decrypt_RoundTrip(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)
	ct, err := backend.Encrypt(context.Background(), "identity-1", env, []byte("123-45-6789"))
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		Ciphertext:  ct,
	})

	require.Empty(t, resp.Error)
	assert.Equal(t, []byte("123-45-6789"), resp.Plaintext)
}

func TestService_Decrypt_ShreddedIdentity_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)
	ct, err := backend.Encrypt(context.Background(), "identity-1", env, []byte("pii"))
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(context.Background(), "identity-1"))

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		Ciphertext:  ct,
	})

	require.NotEmpty(t, resp.Error)
	assert.Empty(t, resp.Plaintext)
}

func TestService_Decrypt_MissingIdentityKey_Rejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{})
	require.NotEmpty(t, resp.Error)
}

func TestService_StartNATSListener_DoubleStartRejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))
	require.Error(t, svc.StartNATSListener(ctx, nc))
}

func TestDecryptSubject_Exact(t *testing.T) {
	assert.Equal(t, "lattice.vault.decrypt", vault.DecryptSubject)
}
