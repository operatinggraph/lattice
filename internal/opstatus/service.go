// Package opstatus hosts the Processor-side lattice.op.status responder — a
// sanctioned way for any op-submitting component to ask "did my operation
// land?" (op-status-read-surface-design.md §2) without a Core-KV read grant.
// The Processor is the sole sanctioned Core-KV reader (P2); this RPC projects
// the Contract #4 idempotency tracker (vtx.op.<requestId>) to callers over a
// subject-scoped NATS Services responder, mirroring internal/vault.Service's
// transport-gated shape.
package opstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Subject is the NATS Services subject the op-status RPC responds on.
const Subject = "lattice.op.status"

// serviceName is the NATS Services registration name (exposed via
// $SRV.PING/$SRV.INFO/$SRV.STATS alongside the endpoint).
const serviceName = "op-status"

// handlerTimeout bounds a single tracker lookup so a wedged backend fails
// the request with an error reply instead of leaving the caller to time out.
const handlerTimeout = 5 * time.Second

// Request is the JSON payload for a Subject request.
type Request struct {
	RequestID string `json:"requestId"`
}

// Response is the JSON reply for a Subject request — a projection of the
// Contract #4 tracker. Committed is the §4.3/§4.4 dedup verdict (found AND
// NOT isDeleted, the same landed test internal/bridge's resultAlreadyLanded
// applies); Found:false is the contracted answer for an absent or
// TTL-expired tracker, same as today's raw KVGet.
type Response struct {
	Found       bool   `json:"found"`
	Committed   bool   `json:"committed"`
	IsDeleted   bool   `json:"isDeleted"`
	CommittedAt string `json:"committedAt,omitempty"`
	Class       string `json:"class,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Service is the NATS Services responder projecting the Contract #4 op
// tracker.
type Service struct {
	conn   *substrate.Conn
	bucket string
	logger *slog.Logger

	mu       sync.Mutex
	microSvc micro.Service // set by StartNATSListener; nil until started
}

// NewService constructs a Service reading the Contract #4 tracker off
// bucket via conn. logger may be nil — slog's default logger is used in
// that case.
func NewService(conn *substrate.Conn, bucket string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{conn: conn, bucket: bucket, logger: logger}
}

// StartNATSListener registers the op-status RPC as a NATS micro-service on
// nc. The service is stopped when ctx is cancelled. Returns an error if the
// service cannot be created or if already started.
func (s *Service) StartNATSListener(ctx context.Context, nc *nats.Conn) error {
	s.mu.Lock()
	if s.microSvc != nil {
		s.mu.Unlock()
		return fmt.Errorf("opstatus: NATS listener already started")
	}
	s.mu.Unlock()

	svc, err := micro.AddService(nc, micro.Config{
		Name:        serviceName,
		Version:     "1.0.0",
		Description: "Op-status RPC projecting the Contract #4 idempotency tracker (lattice.op.status)",
	})
	if err != nil {
		return fmt.Errorf("opstatus: micro.AddService: %w", err)
	}
	if err := svc.AddEndpoint(serviceName,
		micro.HandlerFunc(func(req micro.Request) { s.handle(req) }),
		micro.WithEndpointSubject(Subject)); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("opstatus: AddEndpoint %q: %w", Subject, err)
	}

	s.mu.Lock()
	s.microSvc = svc
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		if err := svc.Stop(); err != nil {
			s.logger.Error("opstatus: stop micro service", "err", err)
		}
	}()
	return nil
}

// handle is reachable with arbitrary caller-controlled JSON over NATS, so it
// recovers from any panic and never echoes a backend error's full detail
// back over the wire — only a generic, non-identifying message. Full detail
// is logged server-side for operator diagnosis.
func (s *Service) handle(req micro.Request) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("opstatus: handler panic", "panic", r)
			s.respond(req, Response{Error: "opstatus: lookup failed"})
		}
	}()

	var in Request
	if err := json.Unmarshal(req.Data(), &in); err != nil {
		s.respond(req, Response{Error: "opstatus: invalid request"})
		return
	}
	if !isBareID(in.RequestID) {
		s.respond(req, Response{Error: "opstatus: requestId required and must be a bare id"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	resp, err := s.lookup(ctx, in.RequestID)
	if err != nil {
		s.logger.Warn("opstatus: tracker lookup failed", "requestId", in.RequestID, "err", err)
		s.respond(req, Response{Error: "opstatus: lookup failed"})
		return
	}
	s.respond(req, resp)
}

// isBareID reports whether s is a non-empty token with no dots / wildcards /
// whitespace — the responder never lets a caller shape an arbitrary key
// (mirrors internal/bridge's isBareHandle discipline for the same requestId
// token).
func isBareID(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, ".*> \t\n")
}

// lookup reads vtx.op.<requestId> — the responder's ONE sanctioned key
// shape, never any other — and projects it to the Contract #4 verdict,
// mirroring internal/bridge/dispatch.go's resultAlreadyLanded. An
// unparseable tracker is not trustworthy landed evidence (defensive parity
// with the bridge's prior direct-read posture): logged and reported as
// not-found rather than a hard RPC error, so a caller's redelivery/skip
// logic degrades to "proceed and let the adapter dedup" instead of an
// unbounded retry loop.
func (s *Service) lookup(ctx context.Context, requestID string) (Response, error) {
	entry, err := s.conn.KVGet(ctx, s.bucket, "vtx.op."+requestID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return Response{Found: false}, nil
		}
		return Response{}, fmt.Errorf("opstatus: get tracker %q: %w", requestID, err)
	}
	var env substrate.DocumentEnvelope
	if uerr := json.Unmarshal(entry.Value, &env); uerr != nil {
		s.logger.Warn("opstatus: tracker unparseable; reporting not-found", "requestId", requestID, "err", uerr)
		return Response{Found: false}, nil
	}
	return Response{
		Found:       true,
		Committed:   !env.IsDeleted,
		IsDeleted:   env.IsDeleted,
		CommittedAt: trackerCommittedAt(env.Data),
		Class:       env.Class,
	}, nil
}

// trackerCommittedAt extracts data.committedAt (Contract #4 §4.2) from the
// tracker's data payload; a missing or non-string value yields "" rather
// than an error — committedAt is informational, not load-bearing for the
// dedup verdict.
func trackerCommittedAt(data map[string]any) string {
	s, _ := data["committedAt"].(string)
	return s
}

func (s *Service) respond(req micro.Request, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("opstatus: marshal response", "err", err)
		if rErr := req.Respond([]byte(`{"error":"opstatus: response marshal failure"}`)); rErr != nil {
			s.logger.Error("opstatus: send error response", "err", rErr)
		}
		return
	}
	if err := req.Respond(data); err != nil {
		s.logger.Error("opstatus: send response", "err", err)
	}
}
