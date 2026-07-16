package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/asolgan/lattice/internal/edge/agent"
	"github.com/asolgan/lattice/internal/edge/overlay"
	"github.com/asolgan/lattice/internal/edge/store"
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
// talks to cmd/facet's own localhost HTTP surface").
type server struct {
	conn       *substrate.Conn
	store      *store.Store
	overlay    *overlay.Overlay
	agent      *agent.Agent
	feed       *feed
	logger     *slog.Logger
	identityID string
	// gatewayURL and devSigner back /api/claim (claim.go, Fire 3) — a
	// standalone Gateway call authenticated by a freshly-minted throwaway
	// credential, independent of this process's own identityID/agent.
	gatewayURL string
	devSigner  *devSigner
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("facet: embed web sub-fs: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/feed", s.handleFeed)
	mux.HandleFunc("/api/enqueue", s.handleEnqueue)
	mux.HandleFunc("/api/claim", s.handleClaim)
}

// handleFeed implements GET /api/feed (SSE) — see feed.go's writeSSE.
func (s *server) handleFeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	writeSSE(w, r, s.logger, s.feed, func() []frame {
		entries, err := s.store.ScanPrefix("manifest.")
		if err != nil {
			s.logger.Error("facet: scan manifest prefix failed", "err", err)
			return nil
		}
		frames := make([]frame, 0, len(entries))
		for _, e := range entries {
			v, ok, err := s.overlay.Read(e.Key)
			if err != nil || !ok {
				continue
			}
			frames = append(frames, frame{Kind: "manifest", Key: e.Key, Deleted: v.Deleted, Pending: v.Pending, Data: v.Data})
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
// Gateway round-trip").
func (s *server) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
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

	requestID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate requestId: "+err.Error())
		return
	}

	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: req.OperationType,
		Actor:         s.identityID,
		Payload:       req.Payload,
		Class:         req.Class,
		AuthContext:   req.AuthContext,
	}
	if len(req.Reads) > 0 || len(req.OptionalReads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: req.Reads, OptionalReads: req.OptionalReads}
	}

	var touched []string
	if req.TouchedKey != "" {
		if err := s.overlay.Apply(req.TouchedKey, requestID, req.Payload, false); err != nil {
			s.logger.Warn("facet: optimistic overlay apply failed, continuing without it", "key", req.TouchedKey, "err", err)
		} else {
			touched = []string{req.TouchedKey}
		}
	}

	if err := s.agent.Enqueue(env, touched); err != nil {
		s.writeError(w, http.StatusInternalServerError, "enqueue failed: "+err.Error())
		return
	}

	s.feed.enqueueOutbox(&outboxEntry{
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
