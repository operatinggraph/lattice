package main

import (
	"context"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestControlRequest_StampsOperatorActorKey verifies controlRequest carries
// s.operatorActorKey as the Lattice-Actor header on every outbound
// control-plane request (control-plane-capability-authz-design.md §3.6).
func TestControlRequest_StampsOperatorActorKey(t *testing.T) {
	url := testutil.StartEmbeddedNATS(t)

	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	var gotActor string
	svc, err := micro.AddService(nc, micro.Config{Name: "echo-actor-test", Version: "0.0.1"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Stop() })
	require.NoError(t, svc.AddEndpoint("echo",
		micro.HandlerFunc(func(req micro.Request) {
			gotActor = controlauth.ActorFromRequest(req)
			_ = req.Respond([]byte(`{}`))
		}),
		micro.WithEndpointSubject("lattice.ctrl.test.echo")))

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	srv := &server{conn: conn, operatorActorKey: "vtx.identity.OPERATOR"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = srv.controlRequest(ctx, conn, "lattice.ctrl.test.echo")
	require.NoError(t, err)

	require.Equal(t, "vtx.identity.OPERATOR", gotActor)
}
