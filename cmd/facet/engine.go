package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/edge/overlay"
	"github.com/operatinggraph/lattice/internal/edge/store"
	edgesync "github.com/operatinggraph/lattice/internal/edge/sync"
	"github.com/operatinggraph/lattice/internal/edge/transport/natstransport"
	edgevault "github.com/operatinggraph/lattice/internal/edge/vault"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// engineConfig is the process-wide wiring every engine shares, resolved once
// from the environment in main.go's run() — everything that does NOT vary
// per identity.
type engineConfig struct {
	NATSURL    string
	GatewayURL string
	StoreDir   string
	Logger     *slog.Logger
}

// engine is one identity's embedded edge stack — local store, sync.Manager,
// agent, and SSE feed — everything server.go's handlers read/write through.
// Fire 2/3 built exactly one of these, constructed once in main.go's run()
// and held on *server for the process's whole lifetime; Inc 2 (edge-showcase-
// app-design.md §7.2) needs one per logged-in identity, so this is that same
// wiring extracted into a constructor an engineManager can call repeatedly —
// nothing about the wiring itself changed, only its cardinality.
type engine struct {
	identityID string
	deviceID   string
	conn       *substrate.Conn
	store      store.Store
	overlay    *overlay.Overlay
	agent      *agent.Agent
	feed       *feed

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newEngine dials NATS as identityID/deviceID (authenticated by token), opens
// that identity's local store under cfg.StoreDir, and starts its sync.Manager
// + agent drain loop. ctx bounds the engine's background goroutines; callers
// must call Close to stop them and release the store/connection.
//
// tokenSource, when non-nil, re-mints a fresh credential for identityID on
// every NATS (re)connect attempt and every Gateway submit — the engine can
// outlive the login-time JWT's TTL (engineManager's warm-resume idle window
// is unbounded while a holder stays attached), and both the NATS auth-callout
// grant and the Gateway's own verifier are exp-bounded by that same JWT.
// Without it, nats.go replays the original token on every reconnect and
// aborts permanently after two identical auth errors on the same server
// (nats.go's processAuthError), and the Gateway submitter keeps presenting a
// 401-doomed token forever (agent.ErrCredentialRejected, sticky sign-out).
// nil is the boot-env fallback's posture (enginemanager.go's Seed): that
// credential was minted OUTSIDE this process, so there is nothing to
// re-mint — token stays static.
func newEngine(ctx context.Context, cfg engineConfig, identityID, deviceID, token string, tokenSource func() (string, error)) (*engine, error) {
	storePath := filepath.Join(cfg.StoreDir, identityID+".db")
	st, err := store.Open(storePath)
	if err != nil {
		return nil, err
	}

	connOpts := substrate.ConnectOpts{
		URL: cfg.NATSURL,
		// Must be the BARE device id: natsauth.go's Handle reads
		// req.ClientInformation.Name (this CONNECT option) directly as
		// deviceID and splices it into the allowed durable-consumer subject
		// as fmt.Sprintf("edge-sync-%s-%s", identityID, deviceID) —
		// sync.Manager's own "edge-sync-"+IdentityID+"-"+DeviceID durable
		// name must match it exactly (see main.go's identical comment,
		// carried here verbatim since this is the same connection this
		// project's boot path used to open directly).
		Name:          deviceID,
		MaxReconnects: -1,
		ReconnectWait: 2 * time.Second,
		// Must be "_INBOX.edge." (not "_INBOX.facet.") — natsauth's issued
		// permission set grants exactly this literal prefix regardless of
		// which app connects, keyed off the verified identity.
		InboxPrefix: "_INBOX.edge." + identityID,
	}
	if tokenSource != nil {
		connOpts.TokenHandler = func() string {
			t, err := tokenSource()
			if err != nil {
				// mint() is a local RSA sign with no I/O; a failure here is not
				// expected to recur. Fall back to the original token rather than
				// an empty string — both are refused by the server if genuinely
				// stale, but an empty token guarantees it. Never mutate the
				// closed-over `token`: this callback can run from nats.go's
				// internal reconnect goroutine, and the fallback must stay the
				// same known-good value on every invocation, not a moving target.
				cfg.Logger.Warn("facet engine: refresh NATS token failed; presenting the original login-time token", "identityId", identityID, "err", err)
				return token
			}
			return t
		}
	} else {
		connOpts.Token = token
	}
	engCtx, cancel := context.WithCancel(ctx)
	conn, err := substrate.Connect(engCtx, connOpts)
	if err != nil {
		cancel()
		_ = st.Close()
		return nil, err
	}

	// The actor key the control plane's self-asserted-actor fallback expects,
	// shared verbatim by the sync manager below and the Vault client — both
	// address that same control plane.
	actorHeader := "vtx.identity." + identityID
	ctrl := natstransport.New(conn)

	// A Vault session-key client for this identity, so the sealed `name`
	// aspect the manifest.me row carries can be decrypted in memory for
	// display (display-name-convention-design.md §3 N3). A construction
	// failure is not fatal: its only consequence is that the renderer keeps
	// painting its typed fallback instead of the resident's name — the same
	// degraded state a shredded identity produces by design.
	var selfName *edgevault.SelfName
	if vaultClient, verr := edgevault.New(ctrl, edgevault.Config{
		IdentityID:  identityID,
		ActorHeader: actorHeader,
		Logger:      cfg.Logger,
	}); verr != nil {
		cfg.Logger.Warn("facet engine: vault client unavailable; self-name stays sealed", "identityId", identityID, "err", verr)
	} else {
		selfName = edgevault.NewSelfName(vaultClient)
	}

	fd := newFeed(selfName)
	// Connectivity handlers key the offline banner on this connection's own
	// host↔NATS state (design §4.4), not the browser↔host SSE transport —
	// nats.go calls these on every disconnect/reconnect cycle regardless of
	// how many times the underlying TCP connection actually flaps.
	conn.NATS().SetDisconnectErrHandler(func(_ *nats.Conn, _ error) { fd.setConnected(false) })
	conn.NATS().SetReconnectHandler(func(_ *nats.Conn) { fd.setConnected(true) })
	overlayStore := overlay.New(st)
	mgr, err := edgesync.New(ctrl, st, edgesync.Config{
		IdentityID: identityID,
		DeviceID:   deviceID,
		// See main.go's identical comment: the control plane's
		// self-asserted-header fallback expects the literal actor key here,
		// not the bearer JWT (that authenticates the NATS connection itself
		// via Token above, and every Gateway submit via
		// agent.GatewaySubmitter).
		ActorHeader: actorHeader,
		Logger:      cfg.Logger,
		OnChange: func(key string, deleted bool) {
			fd.publishManifestKey(overlayStore, key, deleted)
		},
		OnHydrationComplete: func(revision uint64) {
			fd.publishReady(revision)
		},
		OnRunEstablished: func() {
			fd.setSyncDegraded(false)
		},
	})
	if err != nil {
		cancel()
		conn.Close()
		_ = st.Close()
		return nil, err
	}

	gwSubmitter := &agent.GatewaySubmitter{URL: cfg.GatewayURL, Token: token}
	if tokenSource != nil {
		gwSubmitter.TokenSource = tokenSource
	}
	submitter := &trackingSubmitter{
		inner: gwSubmitter,
		feed:  fd,
	}
	ag := agent.New(submitter, st, overlayStore, mgr, agent.Config{
		Logger: cfg.Logger,
		Conflict: func(c agent.ConflictInfo) {
			cfg.Logger.Warn("facet engine: intent rejected", "identityId", identityID, "requestId", c.RequestID, "keys", c.Keys)
		},
	})

	e := &engine{
		identityID: identityID,
		deviceID:   deviceID,
		conn:       conn,
		store:      st,
		overlay:    overlayStore,
		agent:      ag,
		feed:       fd,
		cancel:     cancel,
	}
	e.wg.Add(2)
	go func() {
		defer e.wg.Done()
		runAgentLoop(engCtx, ag, fd, cfg.Logger)
	}()
	go func() {
		defer e.wg.Done()
		runSyncLoop(engCtx, mgr, fd, identityID, cfg.Logger)
	}()
	return e, nil
}

// syncDegradedMarker is the seam runSyncLoop marks degraded/recovered sync
// through — *feed in production; a recorder in tests.
type syncDegradedMarker interface {
	setSyncDegraded(degraded bool)
}

// runSyncLoop runs the Edge Sync Manager, restarting it with capped
// exponential backoff whenever Run returns while the engine context is still
// live. Run can now fail on a transient control-plane unavailability at warm
// boot — the personal.syncgap gap check fails closed if Refractor (or its
// personal lens rule) is briefly unavailable at boot
// (edge-syncgap-control-rpc-design.md §7) — so a single exit must not leave
// sync dead for the whole session, the pre-existing log-and-abandon behaviour
// this design turns from latent to likely. A cancelled engine context ends the
// loop cleanly; local store reads keep serving throughout (offline-first).
//
// Each error exit marks the feed's syncDegraded axis before backing off, so
// the browser shows a stale-world banner instead of a healthy-looking stale
// world; the manager's OnRunEstablished (wired in newEngine) clears it when a
// retry gets past its freshness gate.
func runSyncLoop(ctx context.Context, mgr *edgesync.Manager, fd syncDegradedMarker, identityID string, logger *slog.Logger) {
	const (
		baseBackoff = 500 * time.Millisecond
		maxBackoff  = 30 * time.Second
	)
	backoff := baseBackoff
	for {
		err := mgr.Run(ctx)
		if ctx.Err() != nil {
			return // engine shutting down — a clean exit, not a failure
		}
		if err == nil {
			return // Run returned without error and without cancellation — nothing to restart
		}
		fd.setSyncDegraded(true)
		logger.Warn("facet engine: sync manager exited, restarting", "identityId", identityID, "backoff", backoff, "err", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Close stops the engine's background goroutines and releases its store and
// NATS connection. Safe to call exactly once per engine — engineManager only
// ever calls it on an entry it has just removed from its own map, so no two
// callers can race a Close on the same *engine.
func (e *engine) Close() {
	e.cancel()
	e.wg.Wait()
	e.conn.Close()
	_ = e.store.Close()
}
