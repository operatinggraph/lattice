// The Edge browser node's transport shell (edge-browser-node-design.md §3.3).
//
// The wasm host (internal/edge/browser) holds the engine's SEMANTICS — LWW
// mirror, overlay, intent queue, reconcile — single-sourced in Go for both the
// trusted node and the browser. This module holds only the TRANSPORT: the NATS
// WebSocket connection, the durable JetStream delta feed, and the control-plane
// request-reply. It is the JS half of FORK-W A′: nats.go has no browser
// transport, so the connection lives here over the vendored `nats.js`.
//
// `createSyncCore` is the object the wasm host's jsTransport seam calls out to
// (internal/edge/browser/jstransport.go):
//
//	startConsumer({stream, durable, filterSubject}) -> Promise<void>
//	stopConsumer()                                  -> Promise<void>
//	firstSequence(stream)                           -> Promise<number>
//	request(subject, Uint8Array, actor)             -> Promise<Uint8Array>
//
// `createShell` wraps a core with the browser-only coordination the seam
// deliberately does not model — Web Locks leader election so multiple tabs of
// one identity do not split one durable across two consumers, and a
// storage-persistence request — and is the object the page hands to
// `latticeEdge.start({shell})`.

import {
  wsconnect,
  tokenAuthenticator,
  headers,
  jetstream,
  jetstreamManager,
  AckPolicy,
} from "./nats.js.mjs";
import { electLeader } from "./leader.mjs";

// controlTimeoutMs bounds a control-plane request-reply. The Refractor control
// planes are core-NATS micro-services; a request that outlives this is treated
// as a transport failure by the caller (the Go Sync Manager), which retries.
const controlTimeoutMs = 15_000;

// defaultInactiveThresholdMs is how long the server keeps an idle durable before
// reaping it — new in the browser host and deliberately bounded: a tab that is
// closed without draining leaves a durable behind, and unlike the long-lived Go
// node there can be many short-lived browser durables. The server reaps one no
// tab has pulled from within the window; a returning tab simply recreates it
// (create is idempotent by durable name, and the local cursor resumes the gap
// path). 30 minutes is comfortably longer than any real reconnect blip.
const defaultInactiveThresholdMs = 30 * 60 * 1_000;

// createSyncCore opens one WebSocket connection for one identity+device and
// exposes the four transport methods the wasm host's jsTransport calls. It owns
// no leader election and no rendering — createShell adds those.
//
// config:
//   url               ws:// URL of the NATS WebSocket listener
//   identityId        the verified identity (drives the inbox prefix)
//   deviceId          stable per-device name (the connection's `name`)
//   getToken          () => string, the current bearer JWT; re-read on every
//                     (re)connect so a refreshed token survives a reconnect
//   inactiveThresholdMs  optional override for the durable's InactiveThreshold
//   logger            optional {warn, debug} sink (defaults to console)
//
// The pushed-delta target (`deliver`) is set AFTER construction via
// `core.deliver = fn`, because the wasm host resolves it (`api.deliver`) only
// once `latticeEdge.start` returns, which is after this object is built. Until
// it is set, a delivered message is nak'ed so the server redelivers it rather
// than the shell dropping it — the same fail-safe the Go seam takes for a
// message that arrives before its handler registers (jstransport.go Deliver).
export function createSyncCore(config) {
  const {
    url,
    identityId,
    deviceId,
    getToken,
    inactiveThresholdMs = defaultInactiveThresholdMs,
    logger = console,
  } = config;

  if (!url) throw new Error("edge/shell: url is required");
  if (!identityId) throw new Error("edge/shell: identityId is required");
  if (!deviceId) throw new Error("edge/shell: deviceId is required");
  if (typeof getToken !== "function") {
    throw new Error("edge/shell: getToken must be a function returning the current token");
  }

  const inboxPrefix = "_INBOX.edge." + identityId;
  const inactiveThresholdNs = Math.round(inactiveThresholdMs) * 1_000_000;

  const state = {
    nc: null,
    jsm: null,
    js: null,
    // Memoises the in-flight connect so two callers (e.g. firstSequence and
    // startConsumer) racing before the first resolves share one dial rather
    // than opening two connections.
    connecting: null,
    // The current consume iterator, so stopConsumer can end the pull loop
    // without deleting the durable (the ack floor must survive for resume).
    consumeIter: null,
    // Set by the page to the wasm host's api.deliver once start() resolves.
    deliver: null,
  };

  // connect is idempotent: the first call dials, concurrent calls share that
  // dial, and later calls return the open connection. tokenAuthenticator is
  // given the getter, not a fixed string, so nats.js re-invokes it on every
  // reconnect and picks up a refreshed token (the server drops the connection
  // at authz expiry, ≤15m).
  function connect() {
    if (state.nc) return Promise.resolve(state.nc);
    if (state.connecting) return state.connecting;
    state.connecting = (async () => {
      const nc = await wsconnect({
        servers: url,
        name: deviceId,
        inboxPrefix,
        authenticator: tokenAuthenticator(() => getToken()),
      });
      // checkAPI: false suppresses nats.js's default `$JS.API.INFO` account
      // probe on first JetStream use. That subject is NOT in the Edge grant
      // (internal/gateway/natsauth PermissionsFor grants only the per-durable
      // CONSUMER.* + ACK subjects), and nats.go — the trusted Go node's client
      // — never probes it, so leaving the probe on would make the browser
      // client fail closed where the Go node succeeds. Suppressing it keeps
      // this client speaking exactly the subjects the grant allows, at parity
      // with nats.go. (The consumer-create wire-form parity test pins this.)
      const jsOpts = { checkAPI: false };
      state.jsm = await jetstreamManager(nc, jsOpts);
      state.js = jetstream(nc, jsOpts);
      state.nc = nc;
      return nc;
    })();
    return state.connecting;
  }

  async function startConsumer({ stream, durable, filterSubject }) {
    await connect();
    // Idempotent by durable name: create if absent, no-op-update if present.
    // The wire subject this emits is the ACL-granted filtered-create form
    // ($JS.API.CONSUMER.CREATE.SYNC.<durable>.<filter>); a client emitting a
    // different form fails closed here, which is exactly what the parity test
    // pins (edge-browser-node-design.md §2.3).
    await state.jsm.consumers.add(stream, {
      durable_name: durable,
      filter_subject: filterSubject,
      ack_policy: AckPolicy.Explicit,
      inactive_threshold: inactiveThresholdNs,
    });

    const consumer = await state.js.consumers.get(stream, durable);
    const iter = await consumer.consume();
    state.consumeIter = iter;

    // Drive the pull loop in the background; startConsumer resolves once the
    // feed is running, matching the Go seam where RunDurableConsumer returns
    // control after the feed starts (jstransport.go).
    (async () => {
      try {
        for await (const m of iter) {
          await dispatch(m);
        }
      } catch (err) {
        if (state.consumeIter === iter) {
          logger.warn?.("edge/shell: consume loop ended", err?.message ?? err);
        }
      }
    })();
  }

  // dispatch hands one JetStream message to the wasm engine and applies its
  // verdict. The three verdicts are the transport seam's, unchanged: "ack"
  // advances the durable, "nak" asks for redelivery, "term" drops permanently.
  async function dispatch(m) {
    const deliver = state.deliver;
    if (!deliver) {
      // The push target is not wired yet; redeliver rather than drop.
      m.nak();
      return;
    }
    let verdict;
    try {
      verdict = await deliver(m.subject, m.data, m.seq);
    } catch (err) {
      logger.warn?.("edge/shell: deliver threw, redelivering", err?.message ?? err);
      m.nak();
      return;
    }
    switch (verdict) {
      case "ack":
        m.ack();
        break;
      case "term":
        m.term();
        break;
      default:
        m.nak();
    }
  }

  async function stopConsumer() {
    const iter = state.consumeIter;
    state.consumeIter = null;
    if (iter) {
      // Stop pulling but leave the durable on the server: its ack floor is the
      // resume point (edge-browser-node-design.md §3.2's "brief disconnect").
      await iter.stop();
    }
  }

  async function firstSequence(stream) {
    await connect();
    const info = await state.jsm.streams.info(stream);
    return info.state.first_seq;
  }

  async function request(subject, data, actor) {
    await connect();
    const opts = { timeout: controlTimeoutMs };
    if (actor) {
      const h = headers();
      h.set("Lattice-Actor", actor);
      opts.headers = h;
    }
    const reply = await state.nc.request(subject, data, opts);
    return reply.data;
  }

  async function close() {
    await stopConsumer().catch(() => {});
    if (state.nc) {
      await state.nc.drain().catch(() => {});
      state.nc = null;
    }
    state.connecting = null;
  }

  const core = {
    connect,
    startConsumer,
    stopConsumer,
    firstSequence,
    request,
    close,
    // deliver is a settable slot (see the doc above); expose it as a property.
    set deliver(fn) {
      state.deliver = fn;
    },
    get deliver() {
      return state.deliver;
    },
  };
  return core;
}

// createShell wraps a sync core with the browser-only multi-tab coordination
// the seam does not model, and returns the object the page hands to
// latticeEdge.start({shell}).
//
// Multi-tab hazard (edge-browser-node-design.md §3.3): two tabs of one identity
// share one origin, one IndexedDB (the store name is per-identity), and would
// otherwise open two durables that split one pull stream — both mirrors then
// diverge. The shell takes a Web Locks lease: exactly one tab is leader and
// runs the connection + consumer; a follower's startConsumer resolves without
// opening a second feed and instead waits to become leader. Because the mirror
// lives in the shared IndexedDB the leader writes, a follower's engine still
// reads current state; on leader death the lock releases and a follower takes
// over, resuming from the cursor already in the store.
//
// config adds to createSyncCore's:
//   locks             optional LockManager (defaults to navigator.locks); the
//                     injection seam the leader-election unit vectors drive
//   persist           optional () => Promise; storage-persistence request
//                     (defaults to navigator.storage.persist)
export function createShell(config) {
  const core = createSyncCore(config);
  const identityId = config.identityId;
  const locks = config.locks ?? (globalThis.navigator && globalThis.navigator.locks);
  const persist =
    config.persist ??
    (globalThis.navigator?.storage?.persist
      ? () => globalThis.navigator.storage.persist()
      : null);
  const logger = config.logger ?? console;

  // The mirror is a disposable cache by design (eviction ⇒ re-hydrate, the same
  // gap path as retention expiry); the intent queue is not, so best-effort ask
  // the browser to keep the origin's storage rather than evict it.
  if (persist) {
    persist().catch(() => {});
  }

  // leadership resolves once this tab holds the sync lease. A follower's
  // startConsumer awaits it, so only the leader opens a durable.
  let leadership = null;
  function ensureLeadership() {
    if (leadership) return leadership;
    if (!locks) {
      // No Web Locks (a single-context host, e.g. the parity harness or a
      // browser without the API): this context is trivially the sole leader.
      leadership = Promise.resolve();
      return leadership;
    }
    leadership = new Promise((resolve) => {
      electLeader({
        lockName: "lattice-edge-sync-" + identityId,
        locks,
        onAcquire: () => {
          logger.debug?.("edge/shell: acquired sync leadership");
          resolve();
        },
        onRelease: () => {
          logger.debug?.("edge/shell: released sync leadership");
        },
      }).catch((err) => logger.warn?.("edge/shell: leader election failed", err?.message ?? err));
    });
    return leadership;
  }

  return {
    connect: () => core.connect(),
    startConsumer: async (cfg) => {
      await ensureLeadership();
      return core.startConsumer(cfg);
    },
    stopConsumer: () => core.stopConsumer(),
    firstSequence: (stream) => core.firstSequence(stream),
    request: (subject, data, actor) => core.request(subject, data, actor),
    close: () => core.close(),
    set deliver(fn) {
      core.deliver = fn;
    },
    get deliver() {
      return core.deliver;
    },
  };
}
