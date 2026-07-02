package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// DecryptSubject is the NATS Services subject the decrypt RPC responds on
// (design §2.3 — the trusted-tool read path: Loupe already holds the
// identity's piiKey Envelope and the aspect's Ciphertext from its Core-KV
// inspector reads; it calls this RPC to obtain plaintext rather than holding
// the master KEK itself).
const DecryptSubject = "lattice.vault.decrypt"

// decryptServiceName is the NATS Services registration name (exposed via
// $SRV.PING/$SRV.INFO/$SRV.STATS alongside the endpoint).
const decryptServiceName = "vault-decrypt"

// handlerTimeout bounds a single decrypt call so a wedged backend fails the
// request with an error reply instead of leaving the caller to time out.
const handlerTimeout = 5 * time.Second

// DecryptRequest is the JSON payload for a DecryptSubject request. The
// caller supplies everything the Vault needs to decrypt — its own
// identityKey, the Envelope from that identity's piiKey aspect, and the
// Ciphertext from the sensitive aspect's data — since the Vault itself holds
// no durable per-identity state beyond the master KEK.
type DecryptRequest struct {
	IdentityKey string     `json:"identityKey"`
	Envelope    Envelope   `json:"envelope"`
	Ciphertext  Ciphertext `json:"ciphertext"`
}

// DecryptResponse is the JSON reply for a DecryptSubject request. Exactly
// one of Plaintext or Error is set.
type DecryptResponse struct {
	Plaintext []byte `json:"plaintext,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Service is the NATS Services responder exposing a Vault's Decrypt method
// to trusted-tool callers (Loupe).
type Service struct {
	vault  Vault
	logger *slog.Logger

	mu       sync.Mutex
	microSvc micro.Service // set by StartNATSListener; nil until started
}

// NewService constructs a Service wrapping v. logger may be nil — slog's
// default logger is used in that case.
func NewService(v Vault, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{vault: v, logger: logger}
}

// StartNATSListener registers the decrypt RPC as a NATS micro-service on nc.
// The service is stopped when ctx is cancelled. Returns an error if the
// service cannot be created or if already started.
func (s *Service) StartNATSListener(ctx context.Context, nc *nats.Conn) error {
	s.mu.Lock()
	if s.microSvc != nil {
		s.mu.Unlock()
		return fmt.Errorf("vault: NATS listener already started")
	}
	s.mu.Unlock()

	svc, err := micro.AddService(nc, micro.Config{
		Name:        decryptServiceName,
		Version:     "1.0.0",
		Description: "Vault decrypt RPC for trusted-tool PII reads (lattice.vault.decrypt)",
	})
	if err != nil {
		return fmt.Errorf("vault: micro.AddService: %w", err)
	}

	if err := svc.AddEndpoint(decryptServiceName,
		micro.HandlerFunc(func(req micro.Request) { s.handleDecrypt(req) }),
		micro.WithEndpointSubject(DecryptSubject)); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("vault: AddEndpoint %q: %w", DecryptSubject, err)
	}

	s.mu.Lock()
	s.microSvc = svc
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		if err := svc.Stop(); err != nil {
			s.logger.Error("vault: stop micro service", "err", err)
		}
	}()
	return nil
}

// handleDecrypt is reachable with arbitrary caller-controlled JSON over
// NATS, so it recovers from any panic inside the pluggable Vault backend
// (a backend bug must not take down the whole process hosting this
// responder) and never echoes a backend error's full detail back over the
// wire — only a generic, non-identifying message. Full detail is logged
// server-side for operator diagnosis.
func (s *Service) handleDecrypt(req micro.Request) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("vault: decrypt handler panic", "panic", r)
			s.respond(req, DecryptResponse{Error: "vault: decrypt failed"})
		}
	}()

	var in DecryptRequest
	if err := json.Unmarshal(req.Data(), &in); err != nil {
		s.respond(req, DecryptResponse{Error: "vault: invalid request"})
		return
	}
	if in.IdentityKey == "" {
		s.respond(req, DecryptResponse{Error: "vault: identityKey required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	plaintext, err := s.vault.Decrypt(ctx, in.IdentityKey, in.Envelope, in.Ciphertext)
	if err != nil {
		s.logger.Warn("vault: decrypt request failed", "identityKey", in.IdentityKey, "err", err)
		if errors.Is(err, ErrKeyShredded) {
			s.respond(req, DecryptResponse{Error: ErrKeyShredded.Error()})
			return
		}
		s.respond(req, DecryptResponse{Error: "vault: decrypt failed"})
		return
	}
	s.respond(req, DecryptResponse{Plaintext: plaintext})
}

func (s *Service) respond(req micro.Request, resp DecryptResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("vault: marshal response", "err", err)
		fallback, fErr := json.Marshal(DecryptResponse{Error: "vault: failed to marshal response: " + err.Error()})
		if fErr != nil {
			fallback = []byte(`{"error":"vault: response marshal failure"}`)
		}
		if rErr := req.Respond(fallback); rErr != nil {
			s.logger.Error("vault: send error response", "err", rErr)
		}
		return
	}
	if err := req.Respond(data); err != nil {
		s.logger.Error("vault: send response", "err", err)
	}
}
