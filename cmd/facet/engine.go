package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/edge/agent"
	"github.com/asolgan/lattice/internal/edge/overlay"
	"github.com/asolgan/lattice/internal/edge/store"
	edgesync "github.com/asolgan/lattice/internal/edge/sync"
	"github.com/asolgan/lattice/internal/substrate"
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
	store      *store.Store
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
func newEngine(ctx context.Context, cfg engineConfig, identityID, deviceID, token string) (*engine, error) {
	storePath := filepath.Join(cfg.StoreDir, identityID+".db")
	st, err := store.Open(storePath)
	if err != nil {
		return nil, err
	}

	engCtx, cancel := context.WithCancel(ctx)
	conn, err := substrate.Connect(engCtx, substrate.ConnectOpts{
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
		Token:         token,
		// Must be "_INBOX.edge." (not "_INBOX.facet.") — natsauth's issued
		// permission set grants exactly this literal prefix regardless of
		// which app connects, keyed off the verified identity.
		InboxPrefix: "_INBOX.edge." + identityID,
	})
	if err != nil {
		cancel()
		_ = st.Close()
		return nil, err
	}

	fd := newFeed()
	// Connectivity handlers key the offline banner on this connection's own
	// host↔NATS state (design §4.4), not the browser↔host SSE transport —
	// nats.go calls these on every disconnect/reconnect cycle regardless of
	// how many times the underlying TCP connection actually flaps.
	conn.NATS().SetDisconnectErrHandler(func(_ *nats.Conn, _ error) { fd.setConnected(false) })
	conn.NATS().SetReconnectHandler(func(_ *nats.Conn) { fd.setConnected(true) })
	overlayStore := overlay.New(st)
	mgr, err := edgesync.New(conn, st, edgesync.Config{
		IdentityID: identityID,
		DeviceID:   deviceID,
		// See main.go's identical comment: the control plane's
		// self-asserted-header fallback expects the literal actor key here,
		// not the bearer JWT (that authenticates the NATS connection itself
		// via Token above, and every Gateway submit via
		// agent.GatewaySubmitter).
		ActorHeader: "vtx.identity." + identityID,
		Logger:      cfg.Logger,
		OnChange: func(key string, deleted bool) {
			fd.publishManifestKey(overlayStore, key, deleted)
		},
		OnHydrationComplete: func(revision uint64) {
			fd.publishReady(revision)
		},
	})
	if err != nil {
		cancel()
		conn.Close()
		_ = st.Close()
		return nil, err
	}

	submitter := &trackingSubmitter{
		inner: &agent.GatewaySubmitter{URL: cfg.GatewayURL, Token: token},
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
		if err := mgr.Run(engCtx); err != nil && engCtx.Err() == nil {
			cfg.Logger.Warn("facet engine: sync manager exited", "identityId", identityID, "err", err)
		}
	}()
	return e, nil
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
