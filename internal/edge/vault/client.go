// Package vault is the Edge node's Vault Proxy client
// (edge-lattice-full-design.md §3.6, EDGE.4 increment 2): requests a
// transient session key from the Personal Lens control plane's "sessionkey"
// op (internal/refractor/control), TTL-caches it in memory, and decrypts
// ciphertext-shaped aspect data from the local mirror on read. Plaintext
// exists only in the value a caller's Read returns — nothing here ever
// writes it back to the Local VAL Store (internal/edge/store); the cached
// session key itself is memory-only and is re-requested once its TTL
// elapses. A shredded identity's key request fails with the Vault's
// ErrKeyShredded message and nothing is cached, so every subsequent read
// fails the same way — "remote shredding renders all local copies
// permanent gibberish" (design §3.6).
package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/edge/overlay"
	"github.com/asolgan/lattice/internal/edge/transport"
	"github.com/asolgan/lattice/internal/refractor/control/controlwire"
	"github.com/asolgan/lattice/internal/substrate/keys"
	corevault "github.com/asolgan/lattice/internal/vault/vaultwire"
)

// sessionKeyMargin is subtracted from a cached session key's ExpiresAt when
// deciding whether it is still usable — a key that landed with only a
// sliver of TTL left would otherwise round-trip to the control plane again
// on its very next use.
const sessionKeyMargin = 5 * time.Second

// Config configures a Client.
type Config struct {
	// IdentityID is this node's identity NanoID (edge-lattice-full-design.md
	// §3.6 — the same identity Config.IdentityID names in
	// internal/edge/sync.Config). Required; must be a valid Contract #1
	// NanoID.
	IdentityID string
	// ActorHeader is stamped as the Lattice-Actor header on every
	// "sessionkey" control request — the same trusted-posture / verified-JWT
	// value internal/edge/sync.Config.ActorHeader carries. Empty sends no
	// header, matching the control plane's self-asserted-actor default.
	ActorHeader string
	// TTL is the session-key lifetime requested from the control plane;
	// <=0 lets it pick its own default/ceiling (internal/vault's
	// maxSessionKeyTTL, currently 1h).
	TTL    time.Duration
	Logger *slog.Logger
}

// Client requests and TTL-caches a transient Vault session key for one
// identity, and decrypts ciphertext-shaped aspect data with it.
type Client struct {
	ctrl        transport.ControlClient
	cfg         Config
	identityKey string
	logger      *slog.Logger

	mu  sync.Mutex
	key corevault.SessionKey // zero value (empty Key) means "no cached key".
}

// New creates a Client. Returns an error if cfg.IdentityID is empty or not a
// valid Contract #1 NanoID.
func New(ctrl transport.ControlClient, cfg Config) (*Client, error) {
	if !keys.IsValidNanoID(cfg.IdentityID) {
		return nil, fmt.Errorf("edge/vault: IdentityID %q is not a valid NanoID", cfg.IdentityID)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		ctrl:        ctrl,
		cfg:         cfg,
		identityKey: keys.VertexKey("identity", cfg.IdentityID),
		logger:      logger,
	}, nil
}

// Decrypt returns the plaintext for a ciphertext-shaped aspect value,
// requesting and caching a session key as needed. ct must be the
// Contract #3 §3.10 { ct, nonce, keyId } envelope this identity's sensitive
// aspect was sealed under.
func (c *Client) Decrypt(ctx context.Context, ct corevault.Ciphertext) ([]byte, error) {
	key, err := c.sessionKey(ctx)
	if err != nil {
		return nil, err
	}
	return corevault.OpenWithSessionKey(key, c.identityKey, ct)
}

// sessionKey returns the cached session key if it still has more than
// sessionKeyMargin left on its TTL, else requests and caches a fresh one.
// A failed request clears any stale cached key rather than leaving it in
// place — a caller must never silently keep using a key the control plane
// just refused to reissue (e.g. a shred that landed between calls).
func (c *Client) sessionKey(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.key.Key) > 0 && time.Now().Add(sessionKeyMargin).Before(c.key.ExpiresAt) {
		return c.key.Key, nil
	}
	sk, err := c.requestSessionKey(ctx)
	if err != nil {
		c.key = corevault.SessionKey{}
		return nil, err
	}
	c.key = sk
	return sk.Key, nil
}

// requestSessionKey issues one "sessionkey" control-plane request, mirroring
// internal/edge/sync.Manager.controlRequest's subject-building + actor pattern.
func (c *Client) requestSessionKey(ctx context.Context) (corevault.SessionKey, error) {
	body := controlwire.ControlRequest{
		IdentityID: c.cfg.IdentityID,
		TTLSeconds: int64(c.cfg.TTL / time.Second),
	}
	data, err := json.Marshal(body)
	if err != nil {
		return corevault.SessionKey{}, fmt.Errorf("edge/vault: marshal sessionkey request: %w", err)
	}
	reply, err := c.ctrl.Request(ctx, controlwire.ControlSubject("personal", "sessionkey"), data, c.cfg.ActorHeader)
	if err != nil {
		return corevault.SessionKey{}, fmt.Errorf("edge/vault: sessionkey request: %w", err)
	}
	var resp controlwire.ControlResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		return corevault.SessionKey{}, fmt.Errorf("edge/vault: decode sessionkey response: %w", err)
	}
	if resp.Error != "" {
		return corevault.SessionKey{}, fmt.Errorf("%s", resp.Error)
	}
	if resp.PersonalSessionKey == nil || len(resp.PersonalSessionKey.Key) == 0 {
		return corevault.SessionKey{}, fmt.Errorf("edge/vault: control plane did not return a session key")
	}
	return corevault.SessionKey{Key: resp.PersonalSessionKey.Key, ExpiresAt: resp.PersonalSessionKey.ExpiresAt}, nil
}

// ciphertextEnvelope detects the Contract #3 §3.10 { ct, nonce, keyId }
// envelope in a stored value's raw data, mirroring cmd/loupe/vault.go's
// aspectCiphertext — duplicated rather than imported: internal/edge is a
// standalone engine with no dependency on cmd/loupe.
func ciphertextEnvelope(data json.RawMessage) (corevault.Ciphertext, bool) {
	var ct corevault.Ciphertext
	if err := json.Unmarshal(data, &ct); err != nil {
		return corevault.Ciphertext{}, false
	}
	if len(ct.CT) == 0 || len(ct.Nonce) == 0 || ct.KeyID == "" {
		return corevault.Ciphertext{}, false
	}
	return ct, true
}

// Reader composes Client's decrypt over an Overlay's local read path
// (edge-lattice-full-design.md §3.6): every Read whose value is
// ciphertext-shaped is transparently decrypted in-memory before returning
// to the caller — the Overlay/Store underneath is never written to.
type Reader struct {
	overlay *overlay.Overlay
	client  *Client
}

// NewReader builds a Reader over ov, decrypting through client.
func NewReader(ov *overlay.Overlay, client *Client) *Reader {
	return &Reader{overlay: ov, client: client}
}

// Read mirrors overlay.Overlay.Read, transparently decrypting a
// ciphertext-shaped value. Non-ciphertext data (the common case) and a
// deleted/absent result pass through unchanged. A decrypt failure
// (session-key request denied, shredded identity, tampered ciphertext) is
// returned as an error rather than falling back to the ciphertext — a
// caller must never mistake denied/failed decryption for the aspect's real
// value.
func (r *Reader) Read(ctx context.Context, key string) (overlay.Value, bool, error) {
	v, ok, err := r.overlay.Read(key)
	if err != nil || !ok || v.Deleted {
		return v, ok, err
	}
	ct, isCiphertext := ciphertextEnvelope(v.Data)
	if !isCiphertext {
		return v, ok, nil
	}
	plaintext, err := r.client.Decrypt(ctx, ct)
	if err != nil {
		return overlay.Value{}, false, fmt.Errorf("edge/vault: decrypt %q: %w", key, err)
	}
	v.Data = json.RawMessage(plaintext)
	return v, true, nil
}
