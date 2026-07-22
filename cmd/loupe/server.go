package main

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
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
	conn       *substrate.Conn
	adminActor string
	// operatorActorKey is stamped as the Lattice-Actor header on every
	// outbound control-plane request (control-plane-capability-authz
	// -design.md §3.6). Defaults to adminActor when LOUPE_OPERATOR_ACTOR_KEY
	// is unset.
	operatorActorKey string
	// operatorActorToken, when non-empty, carries a signed actor JWT (Fire
	// 2 — verified-actor mode) and is stamped in place of operatorActorKey.
	// Empty means the control-plane server has not been switched to
	// verified-actor mode (or Loupe hasn't been given a token yet) — the
	// Fire 1 self-asserted key stays in effect.
	operatorActorToken string
	logger             *slog.Logger
	natsTimeout        time.Duration
	// uploadCap bounds a single object upload (OBJECTS_MAX_UPLOAD_BYTES);
	// substrate.ObjectPut enforces it at the stream layer.
	uploadCap int64
	// eventClients counts live /api/events/stream tails (bounded at
	// maxEventStreamClients).
	eventClients atomic.Int32
	// pg is the read-only Postgres pool for the lens-contents seam
	// (LOUPE_PG_DSN, pg.go); nil when the seam is not configured, in which
	// case a postgres-target lens answers the pg-pending shape. pgDSNInvalid
	// distinguishes "never configured" from "configured but unparseable" so
	// the latter surfaces as an error, not the friendly pending state.
	pg           *pgxpool.Pool
	pgDSNInvalid bool
	// bindHost is the host part of the listen address; the same-origin gate
	// accepts it alongside loopback hosts (the non-loopback opt-in).
	bindHost string
	// publicOrigin is the declared external origin the console is served at
	// through a TLS-terminating reverse proxy (LOUPE_PUBLIC_ORIGIN,
	// publicorigin.go). nil — the default — means undeclared, and every path
	// that consults it behaves exactly as it did before the declaration
	// existed.
	publicOrigin *publicOrigin
	// cookieSecure is the session cookie's Secure flag, computed once at boot:
	// set when a public origin is declared (always https) or the bind is
	// non-loopback. Derived from the bind alone it would fail OPEN behind a
	// TLS-terminating proxy on a loopback bind.
	cookieSecure bool
	// credLimiter throttles the three unauthenticated credential-exchange
	// endpoints (credlimit.go). nil disables throttling.
	credLimiter *credentialLimiter
	// maxEventStreamClients bounds concurrent SSE tails, resolved at boot from
	// the posture (eventStreamMax). Read through eventStreamCap.
	maxEventStreamClients int
	// everLive remembers which components this process has seen heartbeating
	// (snapshotEverLive / noteEverLive) so an optional component that was
	// running and then crashed reads absent-red, not "offline".
	everLiveMu sync.Mutex
	everLive   map[string]bool
	// authn verifies the operator's Bearer JWT for the console's front door
	// (requireOperator, readauth.go). nil means no auth posture is configured,
	// which fails every gated request closed with 401.
	authn *auth.Authenticator
	// devSigner mints operator dev-tokens (POST /api/operator/dev-token) when
	// the loopback dev-auth posture is enabled; nil otherwise.
	devSigner *devSigner
	// gatewayURL is the Gateway's base URL — op-submissions relay the
	// requesting operator's own verified Bearer token here
	// (loupe-operator-auth-lift-design.md §3.2) instead of Loupe stamping
	// adminActor. Unlike loftspace-app/clinic-app (browser-direct to the
	// Gateway), Loupe's own backend calls it, since some ops (the
	// pkg-lifecycle batch) are assembled server-side.
	gatewayURL string
	// demoMode runs the hosted-demo read-only posture (LOUPE_DEMO_MODE,
	// demo.go): every non-GET request is refused and the shell renders a
	// visitor banner. Defense in depth only — the guarantee is the demo
	// operator identity's capability grants.
	demoMode bool
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
	mux.HandleFunc("/api/vertices", s.handleVertices)
	mux.HandleFunc("/api/vertex", s.handleVertex)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/demo", s.handleDemo)
	mux.HandleFunc("/api/systemmap", s.handleSystemMap)
	mux.HandleFunc("/api/component/", s.handleComponent)
	mux.HandleFunc("/api/lenses", s.handleLenses)
	mux.HandleFunc("/api/events/stream", s.handleEventStream)
	mux.HandleFunc("/api/lens/", s.handleLens)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/flows", s.handleFlows)
	mux.HandleFunc("/api/history/timeline", s.handleHistoryTimeline)
	mux.HandleFunc("/api/gateway/revocations", s.handleGatewayRevocations)
	mux.HandleFunc("/api/edge/fleet", s.handleEdgeFleet)
	mux.HandleFunc("/api/vault/shreds", s.handleVaultShreds)
	mux.HandleFunc("/api/vault/decrypt", s.handleVaultDecrypt)
	mux.HandleFunc("/api/review/", s.handleReview)
	mux.HandleFunc("/api/control/", s.handleControl)
	mux.HandleFunc("/api/packages", s.handlePackages)
	mux.HandleFunc("/api/package", s.handlePackage)
	mux.HandleFunc("/api/packages/install", s.handlePackagesInstall)
	mux.HandleFunc("/api/packages/upgrade", s.handlePackagesUpgrade)
	mux.HandleFunc("/api/packages/uninstall", s.handlePackagesUninstall)
	mux.HandleFunc("/api/ops", s.handleOps)
	mux.HandleFunc("/api/op", s.handleOp)
	// Objects: POST /api/objects (upload), GET/DELETE /api/objects/<oid>. Both
	// the bare and trailing-segment patterns route to the same handler.
	mux.HandleFunc("/api/objects", s.handleObjects)
	mux.HandleFunc("/api/objects/", s.handleObjects)
	// The three credential-exchange endpoints are the console's only handlers
	// reachable with no credential at all, so they carry the rate limiter
	// (credlimit.go) — wrapped here so a throttled caller costs no body read
	// and no signing work.
	mux.HandleFunc(operatorDevTokenPath, s.limitCredentialExchange(s.handleOperatorDevToken))
	mux.HandleFunc(operatorSessionPath, s.limitCredentialExchange(s.handleOperatorSession))
	mux.HandleFunc(operatorLogoutPath, s.limitCredentialExchange(s.handleOperatorLogout))
	mux.HandleFunc(loginPagePath, s.handleLoginPage)
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

// crossOriginBlocked rejects a state-changing request whose Origin header
// names a different site — the cheap same-origin gate for a loopback-bound
// operator console (a hostile web page can form-POST to a well-known local
// port; the browser always attaches Origin to cross-origin POSTs), defense in
// depth alongside the operator login gate (requireOperator, readauth.go).
// Requests without an Origin (curl, same-site GET-initiated) pass.
// Every mutating endpoint checks this before doing any work: the op submit,
// the control planes, object upload/detach, and the package installer family.
//
// Matching Origin against r.Host alone is rebindable — under DNS rebinding
// both headers carry the attacker's name and agree by construction — so the
// Origin's host must ALSO be one the console is legitimately served from: a
// loopback host, or the explicitly-configured bind host (the warned-about
// non-loopback opt-in). Origin "null" (sandboxed iframe, some redirects) has
// no host and fails closed.
//
// Behind a TLS-terminating reverse proxy neither of those holds — the browser's
// Origin names the public site while the bind is loopback — so a declared
// public origin (LOUPE_PUBLIC_ORIGIN, publicorigin.go) is accepted as its own
// branch. That branch does not consult r.Host at all; see publicOrigin.matches
// for why equality against a boot-time constant keeps the rebinding hardening.
func (s *server) crossOriginBlocked(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if s.publicOrigin.matches(origin) {
		return false
	}
	if origin == "http://"+r.Host || origin == "https://"+r.Host {
		if u, err := url.Parse(origin); err == nil {
			if h := u.Hostname(); isLoopbackHost(h) || (s.bindHost != "" && h == s.bindHost) {
				return false
			}
		}
	}
	s.writeError(w, http.StatusForbidden, "cross-origin request blocked (Origin "+origin+")")
	return true
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

// gatewaySubmitContextTimeout bounds an op-submission relay call. It is
// deliberately longer than the Gateway's own internal wait-for-Processor
// timeout (internal/gateway/gateway.go's defaultReqTimeout, 8s) — Loupe's
// clock starts before the HTTP hop to the Gateway even begins, so a Loupe
// deadline equal to or shorter than the Gateway's own would routinely win
// the race and tear the request down before the Gateway's designed
// 202-with-requestId fallback could ever be produced or received, leaving
// the caller with a bare "context deadline exceeded" and no requestId to
// poll Core KV with. Giving Loupe's side comfortable headroom lets the
// Gateway's own timeout fire first, so its fallback actually reaches here.
const gatewaySubmitContextTimeout = 20 * time.Second

// gatewaySubmitContext bounds a handler that relays an op through the
// Gateway (handleOp, object attach/detach). See gatewaySubmitContextTimeout.
func (s *server) gatewaySubmitContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), gatewaySubmitContextTimeout)
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

// healthReaders lists the health-kv bucket and builds the reader closures the
// health-derived endpoints (health, systemmap, lenses) share: readEntry
// decodes a Health KV doc; resolveLens surfaces a lens reporter's
// canonicalName + description from its vtx.meta.<id>.* aspects (a lens's
// Health KV key is its meta.lens vertex id, a bare NanoID); resolveSpec joins
// the lens spec for the renderedState derivation.
func (s *server) healthReaders(ctx context.Context, conn *substrate.Conn) (
	keys []string,
	readEntry func(string) (map[string]any, bool),
	resolveLens func(id string) (name, desc string),
	resolveSpec func(id string) lensSpecInfo,
	err error,
) {
	keys, err = conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	readEntry = func(k string) (map[string]any, bool) {
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
	coreGet := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	resolveLens = func(id string) (name, desc string) {
		metaKey := "vtx.meta." + id
		name = dataString(metaData(coreGet, metaKey+".canonicalName"), "value", "name", "canonicalName")
		desc = dataString(metaData(coreGet, metaKey+".description"), "value", "text", "description")
		return name, desc
	}
	resolveSpec = func(id string) lensSpecInfo { return lensSpec(coreGet, id) }
	return keys, readEntry, resolveLens, resolveSpec, nil
}

// handleHealth implements GET /api/health. It lists the health-kv bucket,
// classifies + freshness-stamps each component entry, and returns the rollup
// behind the shell's topbar pill + alert strip (and the component cards).
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, readEntry, resolveLens, resolveSpec, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	rollup := computeHealth(keys, readEntry, resolveLens, resolveSpec, staleThreshold)
	s.writeJSON(w, http.StatusOK, rollup)
}

// handleSystemMap implements GET /api/systemmap. It overlays the live Health KV
// state onto the canonical component topology and returns the self-truthing
// node / edge graph the landing "system map" view renders, plus the phase-gate
// chips for the map rail.
func (s *server) handleSystemMap(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, readEntry, resolveLens, resolveSpec, err := s.healthReaders(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list health-kv: "+err.Error())
		return
	}
	// The F14 lens-cluster grouping key (Loupe is the P5 inspector exception):
	// one package-manifest pass per poll, not per lens. The listing is
	// server-side scoped to the `vtx.package.` subtree — the only keys
	// buildLensPackageIndex consults — because this handler renders the
	// landing view inside the shared per-request budget: an unscoped listing
	// walks the whole Core KV corpus first and, on a large bucket, exhausts
	// the budget before a single health entry is read, which the assembler
	// then renders as every component absent-red (a false total outage).
	coreKeys, err := conn.KVListKeysPrefix(ctx, bootstrap.CoreKVBucket, "vtx.package.")
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list core-kv packages: "+err.Error())
		return
	}
	coreGet := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	pkgIndex := buildLensPackageIndex(coreKeys, coreGet)
	m := computeSystemMap(keys, readEntry, resolveLens, resolveSpec, staleThreshold, s.snapshotEverLive(), pkgIndex)
	for _, n := range m.Nodes {
		if n.Kind == nodeComponent && len(n.Instances) > 0 {
			s.noteEverLive(n.ID)
		}
	}
	s.writeJSON(w, http.StatusOK, m)
}

// snapshotEverLive copies the set of components this process has observed
// with live heartbeats. It gates the optional-component rendering: heartbeats
// TTL out of Health KV, so "no heartbeat key" alone cannot distinguish
// never-started from started-then-crashed.
func (s *server) snapshotEverLive() map[string]bool {
	s.everLiveMu.Lock()
	defer s.everLiveMu.Unlock()
	out := make(map[string]bool, len(s.everLive))
	for id := range s.everLive {
		out[id] = true
	}
	return out
}

// noteEverLive records components observed with at least one live instance.
func (s *server) noteEverLive(ids ...string) {
	s.everLiveMu.Lock()
	defer s.everLiveMu.Unlock()
	if s.everLive == nil {
		s.everLive = make(map[string]bool)
	}
	for _, id := range ids {
		s.everLive[id] = true
	}
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
		if s.crossOriginBlocked(w, r) {
			return
		}
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
// NATS. The request carries s.operatorActorToken (Fire 2 verified-actor mode)
// when configured, else s.operatorActorKey (Fire 1 self-asserted, empty when
// neither LOUPE_OPERATOR_ACTOR_KEY nor the bootstrap admin actor is
// configured, which the CLI's own unset --actor default also tolerates) —
// control-plane-capability-authz-design.md §3.6.
func (s *server) controlRequest(ctx context.Context, conn *substrate.Conn, subject string) (json.RawMessage, error) {
	actorHeader := s.operatorActorKey
	if s.operatorActorToken != "" {
		actorHeader = s.operatorActorToken
	}
	reply, err := conn.NATS().RequestMsgWithContext(ctx, controlauth.NewActorRequestMsg(subject, actorHeader))
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
		row := map[string]string{
			"name":    p.PackageName(),
			"version": p.PackageVersion(),
			"key":     p.PackageKey(),
		}
		// installedAt is the package vertex's createdAt (§9.1 column); a
		// failed read leaves the cell empty rather than failing the list.
		if entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, p.PackageKey()); err == nil {
			var env struct {
				CreatedAt string `json:"createdAt"`
			}
			if json.Unmarshal(entry.Value, &env) == nil {
				row["installedAt"] = env.CreatedAt
			}
		}
		rows = append(rows, row)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"packages": rows, "count": len(rows)})
}

// handleOp implements POST /api/op. It parses the body into an opRequest,
// validates + shapes it into an envelope (stamping a fresh request id), and
// relays it to the Gateway under the requesting operator's own verified
// Bearer token (loupe-operator-auth-lift-design.md §3.2) — Loupe itself
// stamps no actor and needs no NATS connection to submit an op.
func (s *server) handleOp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	if s.crossOriginBlocked(w, r) {
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
	// buildEnvelope's actor param is unused by the relay path (the Gateway
	// stamps the caller's verified token, never anything Loupe asserts) —
	// passed empty rather than changing a signature op_test.go pins.
	env, err := buildEnvelope(req, requestID, "", time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := s.gatewaySubmitContext(r)
	defer cancel()

	reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayRequestFromEnvelope(env))
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
