package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

//go:embed web
var webFS embed.FS

// server holds the HTTP handlers' shared dependencies. Unlike the other
// vertical apps (which proxy writes browser-direct to the Gateway), facet's
// browser never talks to the Gateway or NATS itself — every read is the SSE
// feed and every write is POST /api/enqueue, both mediated by this process's
// own embedded edge engine (design §4's Stage 0: "the browser only ever
// talks to cmd/facet's own localhost HTTP surface"). Inc 2 (§7.2) replaced
// the single process-lifetime engine Fire 2/3 held here with one engine per
// signed-in identity, multiplexed by engines and resolved per-request by
// requireSession — see engine.go/enginemanager.go/session.go.
type server struct {
	logger *slog.Logger
	// gatewayURL and devSigner back /api/claim (claim.go, Fire 3) and
	// /api/dev-login (session.go, Inc 2) — standalone Gateway calls / engine
	// credentials authenticated by a freshly-minted JWT, independent of any
	// one engine.
	gatewayURL string
	devSigner  *devSigner
	// authn verifies a session cookie's token (session.go); nil when
	// devSigner is nil (no minter configured ⇒ nothing to verify).
	authn *auth.Authenticator
	// refreshAuthn is authn's sliding-session sibling — same trusted key,
	// sessionRefreshGrace tolerance instead of the strict default — and
	// backs ONLY POST /api/session/refresh (session.go). Nil under the same
	// condition as authn.
	refreshAuthn *auth.Authenticator
	engines      *engineManager
	// bootIdentityID is the boot-env EDGE_IDENTITY_ID fallback identity —
	// see resolveSessionIdentity.
	bootIdentityID string
	// loopback gates the session cookie's Secure flag, mirroring
	// cmd/loupe/readauth.go's setOperatorSessionCookie.
	loopback bool
	// pgPool backs GET /api/credentials (credentials.go, Inc 3) — the
	// identityCredentialsRead Protected Postgres lens, mirroring
	// cmd/loftspace-app's read boundary. Nil when FACET_PG_DSN is unset;
	// handleCredentials reports the read model as unconfigured rather than
	// failing the whole process (same optional-dependency posture as
	// loftspace-app's own pgPool).
	pgPool *pgxpool.Pool
	// browserEngine, when non-nil (FACET_BROWSER_ENGINE), turns cmd/facet into
	// a static host for the in-page wasm engine: it serves the wasm + shell
	// assets and injects window.__EDGE_BOOT__ so the browser runs the engine
	// itself over WebSocket (EDGE.5 W4 inc 4, browserengine.go). Nil = the
	// shipped Go host, unchanged.
	browserEngine *browserEngineConfig
	// bootToken is the process's EDGE_TOKEN — the credential the boot-env
	// single-user fallback identity connects with. In browser-native mode it
	// is the token injected for that identity (there is no cookie to read it
	// from). Empty in a login-only deployment.
	bootToken string
	// personas, when non-empty, puts the process in the demo-persona posture
	// (FACET_DEMO_PERSONAS, session.go): the login page offers exactly these
	// identities, dev-login refuses all others, and /api/claim is disabled.
	personas []demoPersona
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("facet: embed web sub-fs: " + err.Error())
	}
	inner := http.NewServeMux()
	fileServer := http.FileServer(http.FS(sub))
	if s.browserEngine != nil {
		// Browser-native mode: the index is rewritten to carry __EDGE_BOOT__
		// and the wasm/shell assets are served; all other static files still
		// come from the embedded file server via serveBrowserIndex's delegate.
		inner.Handle("/", s.serveBrowserIndex(fileServer))
		s.registerBrowserEngineRoutes(inner)
	} else {
		inner.Handle("/", fileServer)
	}
	inner.HandleFunc("/api/feed", s.handleFeed)
	inner.HandleFunc("/api/enqueue", s.handleEnqueue)
	inner.HandleFunc("/api/claim", s.handleClaim)
	inner.HandleFunc("/api/credentials", s.handleCredentials)
	inner.HandleFunc("/api/credentials/link", s.handleCredentialsLink)
	inner.HandleFunc("/api/credentials/unlink", s.handleCredentialsUnlink)
	inner.HandleFunc("/api/staff/worklist", s.handleStaffWorklist)
	inner.HandleFunc(loginPagePath, s.handleLoginPage)
	inner.HandleFunc(loginOptionsPath, s.handleLoginOptions)
	inner.HandleFunc(devLoginPath, s.handleDevLogin)
	inner.HandleFunc(logoutPath, s.handleLogout)
	inner.HandleFunc(whoamiPath, s.handleWhoami)
	inner.HandleFunc(sessionRefreshPath, s.handleSessionRefresh)
	mux.Handle("/", s.requireSession(inner))
}

// handleFeed implements GET /api/feed (SSE) — see feed.go's writeSSE.
// Acquires the session identity's engine for the SSE connection's whole
// lifetime (a long-lived hold, not a per-request one — released only when
// the browser disconnects), so its manifest reads always hit the SAME warm
// engine that OnChange publishes into.
func (s *server) handleFeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	identityID, ok := sessionIdentity(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "no session identity")
		return
	}
	eng, err := s.engines.Acquire(identityID)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "start engine: "+err.Error())
		return
	}
	defer s.engines.Release(identityID)

	writeSSE(w, r, s.logger, eng.feed, func() []frame {
		entries, err := eng.store.ScanPrefix("manifest.")
		if err != nil {
			s.logger.Error("facet: scan manifest prefix failed", "identityId", identityID, "err", err)
			return nil
		}
		frames := make([]frame, 0, len(entries))
		for _, e := range entries {
			v, ok, err := eng.overlay.Read(e.Key)
			if err != nil || !ok {
				continue
			}
			frames = append(frames, eng.feed.manifestFrame(e.Key, v))
		}
		return frames
	})
}

// enqueueRequest is what the browser POSTs to /api/enqueue: the fully
// client-built Contract #2 envelope (facet-app-ux.md §3.6 step 5 — the
// descriptor form renderer already resolved dispatch.reads templates and
// dispatch.contextParams substitutions before this call).
type enqueueRequest struct {
	OperationType string                 `json:"operationType"`
	Class         string                 `json:"class"`
	Payload       json.RawMessage        `json:"payload"`
	Reads         []string               `json:"reads,omitempty"`
	OptionalReads []string               `json:"optionalReads,omitempty"`
	AuthContext   *processor.AuthContext `json:"authContext,omitempty"`
	// TouchedKey, if set, is a Contract #1 key this write's optimistic
	// effect should overlay immediately (design R3) — only meaningful for
	// an update to an already-known key (e.g. a task's own key); a create
	// op (RequestService — the server mints the new instance's key) has no
	// predictable target and leaves this empty, per facet-app-ux.md §3.4a's
	// "Pending chip" being opportunistic, not universal.
	TouchedKey string `json:"touchedKey,omitempty"`
}

// handleEnqueue implements POST /api/enqueue: builds the envelope, applies
// the optional optimistic overlay, durably queues the intent (agent.Enqueue
// — must run after overlay.Apply, per that method's doc), and returns the
// requestId immediately. The actual Gateway round-trip happens on the
// existing drain loop; its outcome arrives back over the SSE feed as an
// "outbox" frame (design §4's "the browser does not block on the actual
// Gateway round-trip"). Acquires the session identity's engine only for the
// duration of this one request — unlike handleFeed, there is no long-lived
// connection to hold it open for, but a live SSE connection for the same
// identity keeps it warm regardless (ref-counted).
func (s *server) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	identityID, ok := sessionIdentity(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "no session identity")
		return
	}
	var req enqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	if req.OperationType == "" {
		s.writeError(w, http.StatusBadRequest, "operationType is required")
		return
	}

	eng, err := s.engines.Acquire(identityID)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "start engine: "+err.Error())
		return
	}
	defer s.engines.Release(identityID)

	requestID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate requestId: "+err.Error())
		return
	}

	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: req.OperationType,
		Actor:         identityID,
		Payload:       req.Payload,
		Class:         req.Class,
		AuthContext:   req.AuthContext,
	}
	if len(req.Reads) > 0 || len(req.OptionalReads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: req.Reads, OptionalReads: req.OptionalReads}
	}

	var touched []string
	if req.TouchedKey != "" {
		if err := eng.overlay.Apply(req.TouchedKey, requestID, req.Payload, false); err != nil {
			s.logger.Warn("facet: optimistic overlay apply failed, continuing without it", "key", req.TouchedKey, "err", err)
		} else {
			touched = []string{req.TouchedKey}
		}
	}

	if err := eng.agent.Enqueue(env, touched); err != nil {
		s.writeError(w, http.StatusInternalServerError, "enqueue failed: "+err.Error())
		return
	}

	eng.feed.enqueueOutbox(&outboxEntry{
		RequestID:     requestID,
		OperationType: req.OperationType,
		Payload:       req.Payload,
		Reads:         req.Reads,
		OptionalReads: req.OptionalReads,
		AuthContext:   req.AuthContext,
		State:         "queued",
	})

	s.writeJSON(w, http.StatusAccepted, map[string]string{"requestId": requestID})
}

func (s *server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("facet: encode response failed", "err", err)
	}
}

func (s *server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}
