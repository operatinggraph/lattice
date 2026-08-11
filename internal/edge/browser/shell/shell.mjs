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
//	startConsumer({stream, durable, filterSubject, startSeq}) -> Promise<void>
//	stopConsumer()                                  -> Promise<void>
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
  DeliverPolicy,
  JetStreamApiCodes,
  JetStreamApiError,
  PermissionViolationError,
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

// deleteAttempts bounds startConsumer's durable-delete retry, and
// deleteRetryBackoffMs is the pause before the first retry, doubled each time.
// Three attempts against the vendored client's 5s API timeout cap the delete
// phase of an attach at roughly 16s — long enough to ride out a blip on a host
// that gets one attach per page, short enough that a genuinely unreachable
// control plane surfaces rather than hangs.
const deleteAttempts = 3;
const deleteRetryBackoffMs = 150;

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
//   connectImpl, jetstreamManagerImpl, jetstreamImpl
//                     optional overrides for the vendored wsconnect /
//                     jetstreamManager / jetstream functions, each defaulting
//                     to the real one. The injection seam a unit vector uses
//                     to drive startConsumer's delete/create wire calls
//                     against a fake JetStream manager+client, with no
//                     WebSocket (shell.test.mjs).
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
    connectImpl = wsconnect,
    jetstreamManagerImpl = jetstreamManager,
    jetstreamImpl = jetstream,
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
    // Memoises the in-flight connect so two callers (e.g. request and
    // startConsumer) racing before the first resolves share one dial rather
    // than opening two connections.
    connecting: null,
    // The current consume iterator, so stopConsumer can end the pull loop
    // without deleting the durable. The durable itself is left in place —
    // the resume position lives in the wasm host's persisted cursor, not in
    // the durable's ack floor, so the next startConsumer() deletes and
    // recreates it positioned at whatever the caller names then.
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
      const nc = await connectImpl({
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
      state.jsm = await jetstreamManagerImpl(nc, jsOpts);
      state.js = jetstreamImpl(nc, jsOpts);
      state.nc = nc;
      return nc;
    })();
    return state.connecting;
  }

  // deleteDurable removes the durable ahead of every create, tolerating the two
  // answers that are not a failure to delete: "consumer not found" (there was
  // nothing to remove — the same idempotence substrate.DeleteStreamConsumer
  // keeps) and a transient transport failure, which is retried.
  //
  // The retry is what the browser host's shape demands. It runs ONE Run per page
  // lifetime with no restart loop (the Go host retries with capped backoff in
  // cmd/facet/engine.go), and the vendored client's JetStream API request
  // carries a 5s timeout with no retry of its own (nats.js.mjs:9620, :9679), so
  // a single API timeout on this delete would otherwise end sync for the whole
  // page until the user reloads.
  //
  // Classification is by the vendored client's own error types, never a message
  // substring. A JetStreamApiError or a PermissionViolationError means the
  // server ANSWERED — a rejection or an ACL denial is a verdict, not a blip, so
  // it propagates on the first attempt and the attach fails loudly. Anything
  // else (the API timeout, a dropped socket) never reached a verdict.
  async function deleteDurable(stream, durable) {
    let backoff = deleteRetryBackoffMs;
    for (let attempt = 1; ; attempt++) {
      try {
        await state.jsm.consumers.delete(stream, durable);
        return;
      } catch (err) {
        if (err instanceof JetStreamApiError) {
          if (err.code === JetStreamApiCodes.ConsumerNotFound) return;
          throw err;
        }
        if (err instanceof PermissionViolationError) throw err;
        if (attempt >= deleteAttempts) throw err;
        logger.warn?.(
          "edge/shell: durable delete failed, retrying",
          err?.message ?? err,
        );
        await new Promise((resolve) => setTimeout(resolve, backoff));
        backoff *= 2;
      }
    }
  }

  async function startConsumer({ stream, durable, filterSubject, startSeq }) {
    await connect();

    // Delete the durable before every create, whatever startSeq says — the
    // same seam the Go host's natstransport.RunDurableConsumer implements and
    // for the same reason: the server refuses to change an existing
    // consumer's DeliverPolicy or OptStartSeq in either direction
    // (nats-server 2.14 server/consumer.go:2435,:2438), so a conditional
    // delete would leave a node that resolves to startSeq === 0 unable to
    // attach at all once a positioned durable already exists under this name.
    await deleteDurable(stream, durable);

    // Idempotent by durable name: the delete above means this is always a
    // create. The wire subject this emits is the ACL-granted filtered-create
    // form ($JS.API.CONSUMER.CREATE.SYNC.<durable>.<filter>); a client
    // emitting a different form fails closed here, which is exactly what the
    // parity test pins (edge-browser-node-design.md §2.3). Adding
    // deliver_policy/opt_start_seq to the body does not change that wire
    // subject — only the durable name and filter subject appear in it.
    const consumerCfg = {
      durable_name: durable,
      filter_subject: filterSubject,
      ack_policy: AckPolicy.Explicit,
      inactive_threshold: inactiveThresholdNs,
    };
    if (startSeq > 0) {
      consumerCfg.deliver_policy = DeliverPolicy.StartSequence;
      consumerCfg.opt_start_seq = startSeq;
    }
    await state.jsm.consumers.add(stream, consumerCfg);

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
      // Stop pulling but leave the durable on the server; nothing here reads
      // its ack floor. The wasm host's persisted cursor is the resume point
      // (edge-cold-signin-delivery-position-design.md §3.4), and startConsumer
      // deletes-then-recreates the durable positioned at that cursor on the
      // next attach regardless of what this durable's floor holds.
      await iter.stop();
    }
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
// Followers do not consume, so they never see a JetStream message; the leader
// signals each landed change over a BroadcastChannel and every other tab
// re-reads the touched key from the shared store (§3.3's follower change-signal).
//
// config adds to createSyncCore's:
//   locks             optional LockManager (defaults to navigator.locks); the
//                     injection seam the leader-election unit vectors drive
//   persist           optional () => Promise; storage-persistence request
//                     (defaults to navigator.storage.persist)
//   channel           optional BroadcastChannel for the follower change-signal
//                     (defaults to one named per identity); the injection seam
//                     the peer-change unit vectors drive
//   createCore        optional core factory (defaults to createSyncCore); the
//                     injection seam that lets the multi-tab vectors observe
//                     leadership gating without opening a real WS connection
export function createShell(config) {
  const makeCore = config.createCore ?? createSyncCore;
  const core = makeCore(config);
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

  // The follower change-signal (edge-browser-node-design.md §3.3). Only the
  // leader tab holds a consumer and writes deltas into the shared per-identity
  // IndexedDB; follower tabs read that same store but receive no JetStream
  // message, so nothing tells them the mirror moved. The leader posts each
  // landed change here; every other tab of the origin hears it and re-reads the
  // touched key. A BroadcastChannel never echoes to the posting context, so a
  // tab never hears its own signal — the leader's own renderer already updated
  // from the delta itself.
  const channel =
    config.channel ??
    (typeof globalThis.BroadcastChannel === "function"
      ? new globalThis.BroadcastChannel("lattice-edge-sync-" + identityId)
      : null);
  const peerHandlers = new Set();
  if (channel) {
    channel.onmessage = (ev) => {
      const d = ev?.data;
      if (!d || typeof d.key !== "string") return;
      for (const fn of peerHandlers) {
        try {
          fn(d.key, !!d.deleted);
        } catch (err) {
          logger.warn?.("edge/shell: onPeerChange handler threw", err?.message ?? err);
        }
      }
    };
  }

  // leadership resolves once this tab holds the sync lease. Two callers await
  // it: `awaitLeadership`, which the wasm host awaits before it resolves
  // anything about its delivery position (jstransport.go's AwaitAttachReady —
  // a position computed at page boot can be days stale by the time a follower
  // is promoted), and `startConsumer`, so no path opens a durable from a
  // follower even if a host skips the gate. The handle is kept so an explicit
  // close() (e.g. sign-out in a still-open tab) releases the lease and hands
  // leadership to a follower, rather than waiting for the tab to close.
  let leadership = null;
  let leaderHandle = null;
  function ensureLeadership() {
    if (leadership) return leadership;
    if (!locks) {
      // No Web Locks (a single-context host, e.g. the parity harness or a
      // browser without the API): this context is trivially the sole leader.
      leadership = Promise.resolve();
      return leadership;
    }
    leadership = new Promise((resolve) => {
      leaderHandle = electLeader({
        lockName: "lattice-edge-sync-" + identityId,
        locks,
        onAcquire: () => {
          logger.debug?.("edge/shell: acquired sync leadership");
          resolve();
        },
        onRelease: () => {
          logger.debug?.("edge/shell: released sync leadership");
        },
      });
      // electLeader returns a handle, not a promise: leadership is signalled by
      // onAcquire → resolve() above, and `settled` resolves only when the lease
      // is *lost* (the callback returns) or rejects if the lock request itself
      // fails. Watch it for that failure so a broken election surfaces instead
      // of hanging startConsumer forever. `settled` is the only awaitable here —
      // the handle itself has no `.catch`, so watching the handle would throw a
      // TypeError on every Web-Locks host rather than report the failure.
      leaderHandle.settled.catch((err) =>
        logger.warn?.("edge/shell: leader election failed", err?.message ?? err));
    });
    return leadership;
  }

  return {
    connect: () => core.connect(),
    // awaitLeadership is the transport seam's optional readiness step
    // (internal/edge/transport's AttachGate, reached through jstransport.go):
    // it resolves when this tab is entitled to attach. ensureLeadership
    // memoizes, so awaiting it here and again in startConsumer runs one
    // election.
    awaitLeadership: () => ensureLeadership(),
    startConsumer: async (cfg) => {
      await ensureLeadership();
      return core.startConsumer(cfg);
    },
    stopConsumer: () => core.stopConsumer(),
    request: (subject, data, actor) => core.request(subject, data, actor),
    // getToken re-exposes config's own getter — the SAME live source the WS
    // transport's authenticator re-reads on every reconnect (createSyncCore's
    // connect(), above) — so the wasm host's Gateway-write submitter
    // (internal/edge/browser/host.go's shellGetTokenFunc) can pull the
    // current token too instead of the value it was started with. One
    // source of truth for "what's the current token", read by both
    // transports.
    getToken: () => config.getToken(),
    // signalChange is the leader's call after a delta lands, so followers learn
    // the shared store moved. A no-op when there is no channel (single-context
    // hosts) or the key is empty.
    signalChange: (key, deleted) => {
      if (channel && typeof key === "string" && key) {
        channel.postMessage({ key, deleted: !!deleted });
      }
    },
    // onPeerChange registers a follower handler and returns an unsubscribe fn.
    onPeerChange: (fn) => {
      if (typeof fn !== "function") return () => {};
      peerHandlers.add(fn);
      return () => peerHandlers.delete(fn);
    },
    close: () => {
      if (channel) {
        channel.onmessage = null;
        try {
          channel.close();
        } catch {
          /* already closed */
        }
      }
      peerHandlers.clear();
      if (leaderHandle) {
        leaderHandle.release();
        leaderHandle = null;
      }
      return core.close();
    },
    set deliver(fn) {
      core.deliver = fn;
    },
    get deliver() {
      return core.deliver;
    },
  };
}
