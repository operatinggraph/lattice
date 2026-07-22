package controlauth

import (
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/operatinggraph/lattice/internal/testutil"
)

func TestActorFromRequest_HeaderPresent(t *testing.T) {
	srv := startEchoService(t)
	defer srv.Stop()

	nc, err := nats.Connect(srv.natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	reply, err := nc.RequestMsg(NewActorRequestMsg("controlauth.test.echo", "vtx.identity.OPERATOR"), 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := string(reply.Data); got != "vtx.identity.OPERATOR" {
		t.Fatalf("got actor %q, want vtx.identity.OPERATOR", got)
	}
}

func TestActorFromRequest_HeaderAbsent(t *testing.T) {
	srv := startEchoService(t)
	defer srv.Stop()

	nc, err := nats.Connect(srv.natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	reply, err := nc.RequestMsg(NewActorRequestMsg("controlauth.test.echo", ""), 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := string(reply.Data); got != "" {
		t.Fatalf("got actor %q, want empty", got)
	}
}

// echoService is a minimal micro.Service that replies with the extracted
// HeaderActor value, proving the header survives a real micro request/reply
// round-trip (not just the server-side getter in isolation).
type echoService struct {
	natsURL string
	svc     micro.Service
	nc      *nats.Conn
}

func (e *echoService) Stop() {
	_ = e.svc.Stop()
	e.nc.Close()
}

func startEchoService(t *testing.T) *echoService {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	svc, err := micro.AddService(nc, micro.Config{Name: "controlauth-echo-test", Version: "0.0.1"})
	if err != nil {
		t.Fatalf("micro.AddService: %v", err)
	}
	if err := svc.AddEndpoint("echo",
		micro.HandlerFunc(func(req micro.Request) {
			_ = req.Respond([]byte(ActorFromRequest(req)))
		}),
		micro.WithEndpointSubject("controlauth.test.echo")); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}

	return &echoService{natsURL: url, svc: svc, nc: nc}
}
