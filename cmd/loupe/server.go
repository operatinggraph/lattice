package main

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

//go:embed web
var webFS embed.FS

const (
	defaultCoreKVLimit = 500
	staleThreshold     = 60 * time.Second
)

// server holds the dependencies the HTTP handlers share. conn may be nil when
// NATS was unreachable at startup; every handler checks requireConn first and
// returns a JSON error rather than dereferencing a nil connection.
type server struct {
	conn        *substrate.Conn
	adminActor  string
	logger      *slog.Logger
	natsTimeout time.Duration
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	// Static UI from the embedded web/ dir, served at the site root.
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Embed guarantees web/ exists at build time; a failure here is a
		// programmer error, not a runtime condition.
		panic("loupe: embed web sub-fs: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/corekv", s.handleCoreKVList)
	mux.HandleFunc("/api/corekv/entry", s.handleCoreKVEntry)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/control/", s.handleControl)
	mux.HandleFunc("/api/packages", s.handlePackages)
	mux.HandleFunc("/api/op", s.handleOp)
}

// writeJSON encodes v as JSON with the given status code.
func (s *server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("encode response", "error", err)
	}
}

// writeError sends {"error": msg} with the given status code. The UI renders
// the error field; status is 502 for an upstream/NATS failure and 400 for a
// bad request.
func (s *server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

// requireConn returns the live connection, or writes a JSON 502 and returns
// false when NATS was never connected. This is the single guard that keeps a
// NATS-down deployment from panicking on a nil *substrate.Conn.
func (s *server) requireConn(w http.ResponseWriter) (*substrate.Conn, bool) {
	if s.conn == nil {
		s.writeError(w, http.StatusBadGateway, "NATS is not connected; check NATS_URL and that the deployment is up")
		return nil, false
	}
	return s.conn, true
}

// reqContext bounds a handler's NATS work by the server's per-request timeout,
// derived from the incoming request's context so a client disconnect cancels.
func (s *server) reqContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.natsTimeout)
}

// handleCoreKVList implements GET /api/corekv?prefix=&limit=. Keys are listed
// from core-kv, filtered by prefix, classified by Contract #1 shape, and capped
// at limit (default 500) so a large bucket cannot hang the UI.
func (s *server) handleCoreKVList(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	prefix := r.URL.Query().Get("prefix")
	limit := defaultCoreKVLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv: "+err.Error())
		return
	}
	rows, truncated := filterAndClassify(keys, prefix, limit)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"keys":      rows,
		"count":     len(rows),
		"truncated": truncated,
		"limit":     limit,
	})
}

// handleCoreKVEntry implements GET /api/corekv/entry?key=. It returns the raw
// envelope JSON for one key plus the surfaced isDeleted flag.
func (s *server) handleCoreKVEntry(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		s.writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "get "+key+": "+err.Error())
		return
	}

	// Pass the envelope through as raw JSON so the UI can pretty-print it
	// exactly as stored; additionally surface isDeleted and the class.
	var envelope json.RawMessage = entry.Value
	if !json.Valid(entry.Value) {
		envelope = nil
	}
	var meta struct {
		IsDeleted bool `json:"isDeleted"`
	}
	_ = json.Unmarshal(entry.Value, &meta)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"key":       key,
		"class":     classifyKey(key),
		"revision":  entry.Revision,
		"isDeleted": meta.IsDeleted,
		"envelope":  envelope,
	})
}

// handleHealth implements GET /api/health. It lists the health-kv bucket,
// classifies + freshness-stamps each component entry, and returns a rollup the
// UI renders as component cards.
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	readEntry := func(k string) (map[string]any, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, k)
		if err != nil {
			return nil, false
		}
		var doc map[string]any
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return nil, false
		}
		return doc, true
	}
	rollup := computeHealth(keys, readEntry, staleThreshold)
	s.writeJSON(w, http.StatusOK, rollup)
}

// handleControl implements both:
//
//	GET  /api/control/<comp>                  → run every allowed read subject
//	POST /api/control/<comp>/<name>/<op>      → a per-name mutation
//
// The component's raw JSON reply bytes are forwarded to the browser verbatim —
// Loupe never decodes into a control plane's typed structs.
func (s *server) handleControl(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/control/"):]
	parts := splitNonEmpty(rest)

	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		s.controlRead(w, r, parts[0])
	case r.Method == http.MethodPost && len(parts) == 3:
		s.controlMutate(w, r, parts[0], parts[1], parts[2])
	default:
		s.writeError(w, http.StatusBadRequest,
			"expected GET /api/control/<comp> or POST /api/control/<comp>/<name>/<op>")
	}
}

// controlRead runs each allowed read subject for comp and returns the raw JSON
// replies keyed by read name. A per-subject NATS failure is captured as that
// read's {"error": ...} rather than failing the whole response, so one dead
// plane does not blank the panel.
func (s *server) controlRead(w http.ResponseWriter, r *http.Request, comp string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	reads, ok := readSubjects(comp)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "unknown control component "+comp)
		return
	}

	out := make(map[string]json.RawMessage, len(reads))
	for name, subject := range reads {
		ctx, cancel := s.reqContext(r)
		raw, err := s.controlRequest(ctx, conn, subject)
		cancel()
		if err != nil {
			out[name] = mustJSON(map[string]string{"error": err.Error()})
			continue
		}
		out[name] = raw
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"reads": out})
}

// controlMutate validates comp/name/op against the per-component allow-list,
// builds the canonical mutate subject, sends the plain-NATS request, and
// forwards the raw JSON reply.
func (s *server) controlMutate(w http.ResponseWriter, r *http.Request, comp, name, op string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	subject, err := mutateSubject(comp, name, op)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()
	raw, err := s.controlRequest(ctx, conn, subject)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// controlRequest issues a PLAIN NATS request (not JetStream) to subject and
// returns the raw reply bytes. Control planes are micro-services over core
// NATS; a nil request body matches what the CLI sends.
func (s *server) controlRequest(ctx context.Context, conn *substrate.Conn, subject string) (json.RawMessage, error) {
	reply, err := conn.NATS().RequestWithContext(ctx, subject, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(reply.Data), nil
}

// handlePackages implements GET /api/packages, listing installed packages via
// the pkgmgr installer.
func (s *server) handlePackages(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	pkgs, err := pkgmgr.NewInstaller(conn, s.adminActor).List(ctx)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list packages: "+err.Error())
		return
	}
	rows := make([]map[string]string, 0, len(pkgs))
	for _, p := range pkgs {
		rows = append(rows, map[string]string{
			"name":    p.PackageName(),
			"version": p.PackageVersion(),
			"key":     p.PackageKey(),
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"packages": rows, "count": len(rows)})
}

// handleOp implements POST /api/op. It parses the body into an opRequest,
// builds a processor.OperationEnvelope (stamping a fresh request id + the admin
// actor), submits it via output.SubmitOp, and returns the OperationReply.
func (s *server) handleOp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if s.adminActor == "" {
		s.writeError(w, http.StatusBadGateway,
			"admin actor not loaded; a valid bootstrap file (BOOTSTRAP_JSON_PATH) is required to submit ops")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req opRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}

	requestID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "generate request id: "+err.Error())
		return
	}
	env, err := buildEnvelope(req, requestID, s.adminActor, time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	reply, err := output.SubmitOp(ctx, conn, env)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "submit op: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, reply)
}

// compile-time assurance that the reply type the UI consumes is the processor's.
var _ = (*processor.OperationReply)(nil)

// mustJSON marshals v, returning a hand-built error object only if marshalling
// fails (which it cannot for the small maps used here).
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"error":"internal: marshal failed"}`)
	}
	return b
}
