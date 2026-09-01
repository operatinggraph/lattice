package auth

import (
	"context"
	"crypto"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// jwksServer serves whatever body is currently stored in it, and can flip
// between a healthy JWKS response and a failure on demand — used to prove
// JWKSPoller's fail-safe (keep the last-known-good set) behavior.
type jwksServer struct {
	body   atomic.Pointer[[]byte]
	fail   atomic.Bool
	hits   atomic.Int64
	server *httptest.Server
}

func newJWKSServer(t *testing.T, initialBody []byte) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	s.body.Store(&initialBody)
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if s.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(*s.body.Load())
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *jwksServer) setBody(b []byte) { s.body.Store(&b) }
func (s *jwksServer) setFail(v bool)   { s.fail.Store(v) }
func (s *jwksServer) url() string      { return s.server.URL }

func TestJWKSPoller_FetchOnce_PopulatesVerifier(t *testing.T) {
	kp := newRSA(t)
	srv := newJWKSServer(t, marshalJWKS(t, rsaJWK("k1", kp.pub)))

	v := verifierFor(t, map[string]crypto.PublicKey{"placeholder": newRSA(t).pub})
	poller, err := NewJWKSPoller(srv.url(), v, nil, nil, testIss, 0, nil)
	if err != nil {
		t.Fatalf("NewJWKSPoller: %v", err)
	}

	if err := poller.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce: %v", err)
	}
	tok := signRS256(t, kp.priv, "k1", claims())
	if _, err := v.Verify(tok); err != nil {
		t.Fatalf("Verify after FetchOnce: %v", err)
	}
	// The placeholder key from construction must be gone — FetchOnce fully
	// replaces the JWKS-sourced portion (rotation removes retired keys).
	other := signRS256(t, kp.priv, "placeholder", claims())
	if _, err := v.Verify(other); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("placeholder kid should be retired after a JWKS swap, got err=%v", err)
	}
}

func TestJWKSPoller_FetchOnce_MergesStaticKeys(t *testing.T) {
	jwksKP := newRSA(t)
	devKP := newRSA(t)
	srv := newJWKSServer(t, marshalJWKS(t, rsaJWK("idp1", jwksKP.pub)))

	v := verifierFor(t, map[string]crypto.PublicKey{"dev": devKP.pub})
	poller, err := NewJWKSPoller(srv.url(), v, map[string]crypto.PublicKey{"dev": devKP.pub},
		map[string]BindingSpec{"dev": {Mode: ModeNanoID}}, testIss, 0, nil)
	if err != nil {
		t.Fatalf("NewJWKSPoller: %v", err)
	}

	if err := poller.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce: %v", err)
	}

	if _, err := v.Verify(signRS256(t, jwksKP.priv, "idp1", claims())); err != nil {
		t.Errorf("Verify(idp1) after FetchOnce: %v", err)
	}
	if _, err := v.Verify(signRS256(t, devKP.priv, "dev", claims())); err != nil {
		t.Errorf("Verify(dev) after FetchOnce: %v — static keys must survive every swap", err)
	}
}

func TestJWKSPoller_FetchOnce_FailureLeavesVerifierUnchanged(t *testing.T) {
	kp := newRSA(t)
	srv := newJWKSServer(t, marshalJWKS(t, rsaJWK("k1", kp.pub)))

	v := verifierFor(t, map[string]crypto.PublicKey{"seed": kp.pub})
	poller, err := NewJWKSPoller(srv.url(), v, nil, nil, testIss, 0, nil)
	if err != nil {
		t.Fatalf("NewJWKSPoller: %v", err)
	}

	srv.setFail(true)
	if err := poller.FetchOnce(context.Background()); err == nil {
		t.Fatal("expected an error from a failing JWKS endpoint")
	}

	// The pre-existing "seed" key (from construction, not from this poller)
	// must still verify — FetchOnce's failure must not have swapped in an
	// empty or partial key set.
	tok := signRS256(t, kp.priv, "seed", claims())
	if _, err := v.Verify(tok); err != nil {
		t.Errorf("Verify(seed) after a failed FetchOnce: %v — a failed poll must not clobber the trusted set", err)
	}
}

func TestJWKSPoller_Rotation(t *testing.T) {
	kp1 := newRSA(t)
	kp2 := newRSA(t)
	srv := newJWKSServer(t, marshalJWKS(t, rsaJWK("k1", kp1.pub)))

	v := verifierFor(t, map[string]crypto.PublicKey{"seed": kp1.pub})
	poller, err := NewJWKSPoller(srv.url(), v, nil, nil, testIss, 0, nil)
	if err != nil {
		t.Fatalf("NewJWKSPoller: %v", err)
	}

	if err := poller.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce (round 1): %v", err)
	}
	tok1 := signRS256(t, kp1.priv, "k1", claims())
	if _, err := v.Verify(tok1); err != nil {
		t.Fatalf("Verify(k1) round 1: %v", err)
	}

	// IdP rotates: k1 retired, k2 is now the only published key.
	srv.setBody(marshalJWKS(t, rsaJWK("k2", kp2.pub)))
	if err := poller.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce (round 2): %v", err)
	}
	tok2 := signRS256(t, kp2.priv, "k2", claims())
	if _, err := v.Verify(tok2); err != nil {
		t.Errorf("Verify(k2) round 2: %v", err)
	}
	if _, err := v.Verify(tok1); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("Verify(k1) round 2: err = %v, want ErrUnknownKey (k1 was retired)", err)
	}
}

func TestJWKSPoller_FetchOnce_RecordsProvenanceAndCounters(t *testing.T) {
	jwksKP := newRSA(t)
	devKP := newRSA(t)
	jwk1 := rsaJWK("idp1", jwksKP.pub)
	jwk1.Alg = "RS256"
	srv := newJWKSServer(t, marshalJWKS(t, jwk1))

	v := verifierFor(t, map[string]crypto.PublicKey{"dev": devKP.pub})
	poller, err := NewJWKSPoller(srv.url(), v, map[string]crypto.PublicKey{"dev": devKP.pub},
		map[string]BindingSpec{"dev": {Mode: ModeNanoID}}, testIss, 0, nil)
	if err != nil {
		t.Fatalf("NewJWKSPoller: %v", err)
	}

	if !poller.LastPollAt().IsZero() {
		t.Fatalf("LastPollAt before any fetch = %v, want zero", poller.LastPollAt())
	}

	if err := poller.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce (round 1): %v", err)
	}
	if poller.LastPollAt().IsZero() {
		t.Error("LastPollAt after a successful fetch is still zero")
	}
	if got := poller.Swaps(); got != 1 {
		t.Errorf("Swaps after the first fetch = %d, want 1 (seed placeholder → idp1+dev)", got)
	}

	info := v.Info()
	idp1 := info["idp1"]
	if idp1.Source != "jwks" || idp1.Alg != "RS256" || idp1.AddedAt.IsZero() {
		t.Errorf("info[idp1] = %+v, want Source=jwks Alg=RS256 AddedAt set", idp1)
	}
	dev := info["dev"]
	if dev.Source != "static" || dev.AddedAt.IsZero() {
		t.Errorf("info[dev] = %+v, want Source=static AddedAt set", dev)
	}
	firstDevAddedAt := dev.AddedAt

	// A second fetch of the SAME kid set must not bump swaps and must
	// preserve dev's original AddedAt (it re-enters the merge every poll,
	// but it isn't a newly-trusted key).
	if err := poller.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce (round 2, unchanged): %v", err)
	}
	if got := poller.Swaps(); got != 1 {
		t.Errorf("Swaps after an unchanged-kid-set refetch = %d, want still 1", got)
	}
	if got := v.Info()["dev"].AddedAt; !got.Equal(firstDevAddedAt) {
		t.Errorf("dev.AddedAt changed across an unchanged refetch: %v -> %v", firstDevAddedAt, got)
	}

	// IdP adds a second key: the kid set changes, so swaps increments again.
	jwk2 := rsaJWK("idp2", newRSA(t).pub)
	srv.setBody(marshalJWKS(t, jwk1, jwk2))
	if err := poller.FetchOnce(context.Background()); err != nil {
		t.Fatalf("FetchOnce (round 3, added key): %v", err)
	}
	if got := poller.Swaps(); got != 2 {
		t.Errorf("Swaps after adding a kid = %d, want 2", got)
	}
}

func TestJWKSPoller_IntervalClamping(t *testing.T) {
	v := verifierFor(t, map[string]crypto.PublicKey{"seed": newRSA(t).pub})

	if p, err := NewJWKSPoller("https://example.test", v, nil, nil, testIss, 0, nil); err != nil {
		t.Fatalf("NewJWKSPoller: %v", err)
	} else if p.interval != DefaultJWKSPollInterval {
		t.Errorf("interval = %v, want default %v", p.interval, DefaultJWKSPollInterval)
	}
	if p, err := NewJWKSPoller("https://example.test", v, nil, nil, testIss, MinJWKSPollInterval/2, nil); err != nil {
		t.Fatalf("NewJWKSPoller: %v", err)
	} else if p.interval != MinJWKSPollInterval {
		t.Errorf("interval = %v, want clamped floor %v", p.interval, MinJWKSPollInterval)
	}
}

// TestNewJWKSPoller_NoIssuerErrors — a live external key source with no
// declared issuer is a construction error: it is always opaque-mode and MUST
// pin an expected iss (Contract #11 §11.3, finding A8).
func TestNewJWKSPoller_NoIssuerErrors(t *testing.T) {
	v := verifierFor(t, map[string]crypto.PublicKey{"seed": newRSA(t).pub})
	if _, err := NewJWKSPoller("https://example.test", v, nil, nil, "", 0, nil); err == nil {
		t.Fatal("NewJWKSPoller with no issuer: want error, got nil")
	}
}
