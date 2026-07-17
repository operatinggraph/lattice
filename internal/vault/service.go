package vault

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/asolgan/lattice/internal/substrate"
)

// DecryptSubject is the NATS Services subject the decrypt RPC responds on
// (design §2.3 — the trusted-tool read path: Loupe already holds the
// identity's piiKey Envelope and the aspect's Ciphertext from its Core-KV
// inspector reads; it calls this RPC to obtain plaintext rather than holding
// the master KEK itself).
const DecryptSubject = "lattice.vault.decrypt"

// WrapKeySubject is the NATS Services subject the small-key-envelope wrap RPC
// responds on (object-store-crypto-shred-design.md §3.1 — the blob plane's
// uploader generates a per-object CEK and wraps it under the governing
// identity's DEK via this RPC rather than holding the master KEK itself,
// mirroring DecryptSubject's trust boundary).
const WrapKeySubject = "lattice.vault.wrapkey"

// UnwrapKeySubject is the NATS Services subject the small-key-envelope
// unwrap RPC responds on — the read-side counterpart of WrapKeySubject.
const UnwrapKeySubject = "lattice.vault.unwrapkey"

// IssueSessionKeySubject is the NATS Services subject the transient
// Edge-decrypt session-key RPC responds on (Personal Lens Fire 5,
// personal-secure-lens-design.md §3.6 "Transient Decryption").
const IssueSessionKeySubject = "lattice.vault.issuesessionkey"

// DecryptRefSubject is the NATS Services subject the ref-verified decrypt RPC
// responds on (design sensitive-ref-mac-provenance-design.md §3.3) — the
// external-egress unwrap consumer's (bridge's) sole decrypt authority once
// Fire 2 swaps its natsperm grant off DecryptSubject: unlike the wholesale
// DecryptSubject, this endpoint mandatorily verifies the caller-supplied MAC
// before decrypting, so a fabricated or harvested-and-spliced ref is refused
// rather than honored.
const DecryptRefSubject = "lattice.vault.decryptref"

// decryptServiceName is the NATS Services registration name (exposed via
// $SRV.PING/$SRV.INFO/$SRV.STATS alongside the endpoint).
const decryptServiceName = "vault-decrypt"

// wrapKeyServiceName / unwrapKeyServiceName are the NATS Services endpoint
// names for the wrap/unwrap RPCs, registered on the same micro.Service as
// the decrypt endpoint.
const (
	wrapKeyServiceName         = "vault-wrapkey"
	unwrapKeyServiceName       = "vault-unwrapkey"
	issueSessionKeyServiceName = "vault-issuesessionkey"
	decryptRefServiceName      = "vault-decryptref"
)

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

// WrapKeyRequest is the JSON payload for a WrapKeySubject request — wrap key
// (a per-object CEK, small enough for envelope wrapping) under identityKey's
// DEK. The caller supplies the identity's piiKey Envelope, as with Decrypt.
type WrapKeyRequest struct {
	IdentityKey string   `json:"identityKey"`
	Envelope    Envelope `json:"envelope"`
	Key         []byte   `json:"key"`
}

// WrapKeyResponse is the JSON reply for a WrapKeySubject request. Exactly
// one of Ciphertext or Error is set.
type WrapKeyResponse struct {
	Ciphertext Ciphertext `json:"ciphertext,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// UnwrapKeyRequest is the JSON payload for an UnwrapKeySubject request — the
// read-side counterpart of WrapKeyRequest: unwrap Wrapped back to the
// original key bytes under identityKey's DEK.
type UnwrapKeyRequest struct {
	IdentityKey string     `json:"identityKey"`
	Envelope    Envelope   `json:"envelope"`
	Wrapped     Ciphertext `json:"wrapped"`
}

// UnwrapKeyResponse is the JSON reply for an UnwrapKeySubject request.
// Exactly one of Key or Error is set.
type UnwrapKeyResponse struct {
	Key   []byte `json:"key,omitempty"`
	Error string `json:"error,omitempty"`
}

// IssueSessionKeyRequest is the JSON payload for an IssueSessionKeySubject
// request — mint a transient decryption key for identityKey's DEK. AspectScope
// is carried for audit/API-shape only (personal-secure-lens-design.md §3.6);
// TTLSeconds <= 0 lets the backend pick its own default/ceiling.
type IssueSessionKeyRequest struct {
	IdentityKey string   `json:"identityKey"`
	Envelope    Envelope `json:"envelope"`
	AspectScope string   `json:"aspectScope,omitempty"`
	TTLSeconds  int64    `json:"ttlSeconds,omitempty"`
}

// IssueSessionKeyResponse is the JSON reply for an IssueSessionKeySubject
// request. Exactly one of Key or Error is set.
type IssueSessionKeyResponse struct {
	Key       []byte    `json:"key,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// DecryptRefRequest is the JSON payload for a DecryptRefSubject request — the
// bridge's egress unwrap supplies the sensitive-ref marker's fields plus the
// minting operation's requestId (read from the external event's envelope,
// never caller-chosen at unwrap time) and its own live-fetched piiKey
// Envelope. Unlike DecryptRequest, there is no IdentityKey field: it is
// derived server-side from Ref once the MAC verifies (design §3.3) — one
// less attacker-controlled input.
type DecryptRefRequest struct {
	Ref        string     `json:"ref"`
	RequestID  string     `json:"requestId"`
	Envelope   Envelope   `json:"envelope"`
	Ciphertext Ciphertext `json:"ciphertext"`
	MAC        []byte     `json:"mac"`
}

// DecryptRefResponse is the JSON reply for a DecryptRefSubject request.
// Exactly one of Plaintext or Error is set.
type DecryptRefResponse struct {
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
	if err := svc.AddEndpoint(wrapKeyServiceName,
		micro.HandlerFunc(func(req micro.Request) { s.handleWrapKey(req) }),
		micro.WithEndpointSubject(WrapKeySubject)); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("vault: AddEndpoint %q: %w", WrapKeySubject, err)
	}
	if err := svc.AddEndpoint(unwrapKeyServiceName,
		micro.HandlerFunc(func(req micro.Request) { s.handleUnwrapKey(req) }),
		micro.WithEndpointSubject(UnwrapKeySubject)); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("vault: AddEndpoint %q: %w", UnwrapKeySubject, err)
	}
	if err := svc.AddEndpoint(issueSessionKeyServiceName,
		micro.HandlerFunc(func(req micro.Request) { s.handleIssueSessionKey(req) }),
		micro.WithEndpointSubject(IssueSessionKeySubject)); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("vault: AddEndpoint %q: %w", IssueSessionKeySubject, err)
	}
	if err := svc.AddEndpoint(decryptRefServiceName,
		micro.HandlerFunc(func(req micro.Request) { s.handleDecryptRef(req) }),
		micro.WithEndpointSubject(DecryptRefSubject)); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("vault: AddEndpoint %q: %w", DecryptRefSubject, err)
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

// handleWrapKey is handleDecrypt's counterpart for WrapKeySubject — same
// panic recovery + generic-error-over-the-wire posture, delegating the
// crypto to the pluggable Vault backend's WrapKey (envelope-encryption of a
// small key, object-store-crypto-shred-design.md §3.1).
func (s *Service) handleWrapKey(req micro.Request) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("vault: wrapKey handler panic", "panic", r)
			s.respondWrapKey(req, WrapKeyResponse{Error: "vault: wrap failed"})
		}
	}()

	var in WrapKeyRequest
	if err := json.Unmarshal(req.Data(), &in); err != nil {
		s.respondWrapKey(req, WrapKeyResponse{Error: "vault: invalid request"})
		return
	}
	if in.IdentityKey == "" {
		s.respondWrapKey(req, WrapKeyResponse{Error: "vault: identityKey required"})
		return
	}
	if len(in.Key) == 0 {
		s.respondWrapKey(req, WrapKeyResponse{Error: "vault: key required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	wrapped, err := s.vault.WrapKey(ctx, in.IdentityKey, in.Envelope, in.Key)
	if err != nil {
		s.logger.Warn("vault: wrapKey request failed", "identityKey", in.IdentityKey, "err", err)
		if errors.Is(err, ErrKeyShredded) {
			s.respondWrapKey(req, WrapKeyResponse{Error: ErrKeyShredded.Error()})
			return
		}
		s.respondWrapKey(req, WrapKeyResponse{Error: "vault: wrap failed"})
		return
	}
	s.respondWrapKey(req, WrapKeyResponse{Ciphertext: wrapped})
}

func (s *Service) respondWrapKey(req micro.Request, resp WrapKeyResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("vault: marshal wrapKey response", "err", err)
		if rErr := req.Respond([]byte(`{"error":"vault: response marshal failure"}`)); rErr != nil {
			s.logger.Error("vault: send error response", "err", rErr)
		}
		return
	}
	if err := req.Respond(data); err != nil {
		s.logger.Error("vault: send response", "err", err)
	}
}

// handleUnwrapKey is handleWrapKey's read-side counterpart for
// UnwrapKeySubject, delegating to the Vault backend's UnwrapKey.
func (s *Service) handleUnwrapKey(req micro.Request) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("vault: unwrapKey handler panic", "panic", r)
			s.respondUnwrapKey(req, UnwrapKeyResponse{Error: "vault: unwrap failed"})
		}
	}()

	var in UnwrapKeyRequest
	if err := json.Unmarshal(req.Data(), &in); err != nil {
		s.respondUnwrapKey(req, UnwrapKeyResponse{Error: "vault: invalid request"})
		return
	}
	if in.IdentityKey == "" {
		s.respondUnwrapKey(req, UnwrapKeyResponse{Error: "vault: identityKey required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	key, err := s.vault.UnwrapKey(ctx, in.IdentityKey, in.Envelope, in.Wrapped)
	if err != nil {
		s.logger.Warn("vault: unwrapKey request failed", "identityKey", in.IdentityKey, "err", err)
		if errors.Is(err, ErrKeyShredded) {
			s.respondUnwrapKey(req, UnwrapKeyResponse{Error: ErrKeyShredded.Error()})
			return
		}
		s.respondUnwrapKey(req, UnwrapKeyResponse{Error: "vault: unwrap failed"})
		return
	}
	s.respondUnwrapKey(req, UnwrapKeyResponse{Key: key})
}

func (s *Service) respondUnwrapKey(req micro.Request, resp UnwrapKeyResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("vault: marshal unwrapKey response", "err", err)
		if rErr := req.Respond([]byte(`{"error":"vault: response marshal failure"}`)); rErr != nil {
			s.logger.Error("vault: send error response", "err", rErr)
		}
		return
	}
	if err := req.Respond(data); err != nil {
		s.logger.Error("vault: send response", "err", err)
	}
}

// handleIssueSessionKey is handleUnwrapKey's counterpart for
// IssueSessionKeySubject, delegating to the Vault backend's IssueSessionKey.
func (s *Service) handleIssueSessionKey(req micro.Request) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("vault: issueSessionKey handler panic", "panic", r)
			s.respondIssueSessionKey(req, IssueSessionKeyResponse{Error: "vault: issue session key failed"})
		}
	}()

	var in IssueSessionKeyRequest
	if err := json.Unmarshal(req.Data(), &in); err != nil {
		s.respondIssueSessionKey(req, IssueSessionKeyResponse{Error: "vault: invalid request"})
		return
	}
	if in.IdentityKey == "" {
		s.respondIssueSessionKey(req, IssueSessionKeyResponse{Error: "vault: identityKey required"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	sk, err := s.vault.IssueSessionKey(ctx, in.IdentityKey, in.Envelope, in.AspectScope, time.Duration(in.TTLSeconds)*time.Second)
	if err != nil {
		s.logger.Warn("vault: issueSessionKey request failed", "identityKey", in.IdentityKey, "err", err)
		if errors.Is(err, ErrKeyShredded) {
			s.respondIssueSessionKey(req, IssueSessionKeyResponse{Error: ErrKeyShredded.Error()})
			return
		}
		s.respondIssueSessionKey(req, IssueSessionKeyResponse{Error: "vault: issue session key failed"})
		return
	}
	s.respondIssueSessionKey(req, IssueSessionKeyResponse{Key: sk.Key, ExpiresAt: sk.ExpiresAt})
}

func (s *Service) respondIssueSessionKey(req micro.Request, resp IssueSessionKeyResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("vault: marshal issueSessionKey response", "err", err)
		if rErr := req.Respond([]byte(`{"error":"vault: response marshal failure"}`)); rErr != nil {
			s.logger.Error("vault: send error response", "err", rErr)
		}
		return
	}
	if err := req.Respond(data); err != nil {
		s.logger.Error("vault: send response", "err", err)
	}
}

// handleDecryptRef is DecryptRefSubject's responder (design
// sensitive-ref-mac-provenance-design.md §3.3): (1) parse the request and
// validate Ref is a well-formed identity-anchored aspect key, deriving
// identityKey from it server-side (the caller no longer supplies one — one
// less attacker-controlled field); (2) recompute the MAC over
// {ref, requestId, ciphertext} and reject on mismatch or an empty MAC with
// ErrRefUnverified — checked BEFORE any decrypt attempt, so a fabricated ref
// never reaches the shred/DEK-unwrap machinery; (3) delegate to the same
// Vault.Decrypt the wholesale RPC uses, unchanged — the live-envelope shred
// gate, DEK unwrap, and AAD check all apply identically, so a shredded
// identity is refused even with a genuinely valid MAC. Same panic-recovery +
// generic-error-detail posture as handleDecrypt.
func (s *Service) handleDecryptRef(req micro.Request) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("vault: decryptRef handler panic", "panic", r)
			s.respondDecryptRef(req, DecryptRefResponse{Error: "vault: decrypt failed"})
		}
	}()

	var in DecryptRefRequest
	if err := json.Unmarshal(req.Data(), &in); err != nil {
		s.respondDecryptRef(req, DecryptRefResponse{Error: "vault: invalid request"})
		return
	}
	identityKey, vertexType, _, _, ok := substrate.ParseAspectKey(in.Ref)
	if !ok || vertexType != "identity" {
		s.respondDecryptRef(req, DecryptRefResponse{Error: "vault: ref is not a well-formed identity-anchored aspect key"})
		return
	}

	macCtx, macCancel := context.WithTimeout(context.Background(), handlerTimeout)
	expected, err := s.vault.MAC(macCtx, RefMACPurpose, RefMACInput(in.Ref, in.RequestID, in.Ciphertext))
	macCancel()
	if err != nil {
		s.logger.Error("vault: decryptRef MAC recompute failed", "ref", in.Ref, "err", err)
		s.respondDecryptRef(req, DecryptRefResponse{Error: "vault: decrypt failed"})
		return
	}
	if len(in.MAC) == 0 || !hmac.Equal(expected, in.MAC) {
		s.respondDecryptRef(req, DecryptRefResponse{Error: ErrRefUnverified.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	plaintext, err := s.vault.Decrypt(ctx, identityKey, in.Envelope, in.Ciphertext)
	if err != nil {
		s.logger.Warn("vault: decryptRef request failed", "ref", in.Ref, "err", err)
		if errors.Is(err, ErrKeyShredded) {
			s.respondDecryptRef(req, DecryptRefResponse{Error: ErrKeyShredded.Error()})
			return
		}
		s.respondDecryptRef(req, DecryptRefResponse{Error: "vault: decrypt failed"})
		return
	}
	s.respondDecryptRef(req, DecryptRefResponse{Plaintext: plaintext})
}

func (s *Service) respondDecryptRef(req micro.Request, resp DecryptRefResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("vault: marshal decryptRef response", "err", err)
		if rErr := req.Respond([]byte(`{"error":"vault: response marshal failure"}`)); rErr != nil {
			s.logger.Error("vault: send error response", "err", rErr)
		}
		return
	}
	if err := req.Respond(data); err != nil {
		s.logger.Error("vault: send response", "err", err)
	}
}
