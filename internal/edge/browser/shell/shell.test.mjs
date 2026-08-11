// createShell unit vectors (edge-browser-node-design.md §3.3) — run with
// `node --test`. They cover the two browser-only coordination mechanisms the
// shell adds on top of a sync core, without a real WebSocket connection or a
// real browser: the Web Locks leader gate (only the leader opens a consumer;
// leadership hands off on the leader's close) and the BroadcastChannel follower
// change-signal (the leader's landed changes reach every other tab, never its
// own). Both drive injected fakes through the shell's `createCore` / `locks` /
// `channel` seams.
//
// leader.test.mjs already proves electLeader in isolation; these prove
// createShell WIRES it correctly — the leader path was silently broken by a
// `.catch` on the non-thenable election handle, uncaught because the shipped
// parity harness only exercises the no-locks path.

import { test } from "node:test";
import assert from "node:assert/strict";
import { createShell, createSyncCore } from "./shell.mjs";
import { JetStreamApiError, JetStreamApiCodes, PermissionViolationError } from "./nats.js.mjs";

// FakeLockManager reproduces the one-relevant Web Locks guarantee: for a given
// name, at most one request's callback runs at a time; the next queued callback
// runs only once the current one's returned promise settles. (Same shape as
// leader.test.mjs's, re-declared to keep the two vector files independent.)
class FakeLockManager {
  constructor() {
    this.queues = new Map();
  }
  request(name, cb) {
    const q = this.queues.get(name) ?? [];
    this.queues.set(name, q);
    let startTurn;
    const turn = new Promise((r) => {
      startTurn = r;
    });
    const settled = turn.then(() => cb());
    settled.finally(() => {
      q.shift();
      if (q.length) q[0].startTurn();
    });
    q.push({ startTurn });
    if (q.length === 1) startTurn();
    return settled;
  }
}

// makeFakeCore records the transport calls the shell delegates so a vector can
// assert whether/when a durable was opened, with no WebSocket.
function makeFakeCore() {
  const calls = { connect: 0, startConsumer: 0, stopConsumer: 0, request: 0, close: 0 };
  let deliverFn = null;
  return {
    calls,
    connect: async () => void calls.connect++,
    startConsumer: async (cfg) => {
      calls.startConsumer++;
      calls.lastConsumerCfg = cfg;
    },
    stopConsumer: async () => void calls.stopConsumer++,
    request: async () => {
      calls.request++;
      return new Uint8Array();
    },
    close: async () => void calls.close++,
    set deliver(fn) {
      deliverFn = fn;
    },
    get deliver() {
      return deliverFn;
    },
  };
}

// makeBroadcastBus models the one BroadcastChannel guarantee the shell relies
// on: a posted message reaches every OTHER open channel on the same name, never
// the posting channel itself, and delivery is asynchronous.
function makeBroadcastBus() {
  const channels = new Set();
  return {
    channel() {
      const ch = {
        onmessage: null,
        _closed: false,
        postMessage(data) {
          if (ch._closed) return;
          const copy = structuredClone(data);
          for (const other of channels) {
            if (other === ch || other._closed) continue;
            queueMicrotask(() => {
              if (!other._closed && other.onmessage) other.onmessage({ data: copy });
            });
          }
        },
        close() {
          ch._closed = true;
          channels.delete(ch);
        },
      };
      channels.add(ch);
      return ch;
    },
  };
}

// flush yields long enough for queued microtasks (leader election, broadcast
// delivery) to run, without a fixed sleep.
async function flush() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

// makeFakeConsumeIter models the shape createSyncCore stores as
// state.consumeIter: async-iterable (empty — these vectors only assert what
// startConsumer sent to jsm/js before the pull loop begins) plus a no-op
// stop() matching the real ConsumerMessages the shell calls in stopConsumer.
function makeFakeConsumeIter() {
  return {
    stop: async () => {},
    [Symbol.asyncIterator]() {
      return { next: async () => ({ done: true, value: undefined }) };
    },
  };
}

// notFoundError constructs the vendored client's own not-found signal:
// jsm.consumers.delete rejects with a JetStreamApiError whose `code` getter
// returns the server's structured err_code (nats.js.mjs:9558-9582), not a
// message the shell would have to substring-match.
function notFoundError() {
  return new JetStreamApiError({
    err_code: JetStreamApiCodes.ConsumerNotFound,
    description: "consumer not found",
    code: 404,
  });
}

// makeFakeJetStream fakes the two objects createSyncCore.connect() stores as
// state.jsm/state.js, driven through the connectImpl/jetstreamManagerImpl/
// jetstreamImpl injection seam so a vector can call createSyncCore's real
// startConsumer with no WebSocket. jsm.consumers.{delete,add} record the wire
// calls in arrival order so a vector can pin delete-before-create; js's
// consumers.get(...).consume() returns an inert iterator. deleteThrowsFirst
// rejects the leading delete attempts from its list and lets the next one
// through, so a vector can drive the retry; deleteThrows rejects every attempt.
function makeFakeJetStream({ deleteThrows, deleteThrowsFirst = [] } = {}) {
  const calls = { order: [], deleteArgs: null, addArgs: null, deleteAttempts: 0 };
  return {
    calls,
    connectImpl: async () => ({}),
    jetstreamManagerImpl: async () => ({
      consumers: {
        delete: async (stream, durable) => {
          calls.order.push("delete");
          calls.deleteArgs = { stream, durable };
          calls.deleteAttempts++;
          if (calls.deleteAttempts <= deleteThrowsFirst.length) {
            throw deleteThrowsFirst[calls.deleteAttempts - 1];
          }
          if (deleteThrows) throw deleteThrows;
        },
        add: async (stream, cfg) => {
          calls.order.push("add");
          calls.addArgs = { stream, cfg };
        },
      },
    }),
    jetstreamImpl: () => ({
      consumers: {
        get: async () => ({ consume: async () => makeFakeConsumeIter() }),
      },
    }),
  };
}

function newFakeSyncCore(fake, overrides = {}) {
  return createSyncCore({
    url: "ws://test",
    identityId: "U",
    deviceId: "D",
    getToken: () => "token",
    connectImpl: fake.connectImpl,
    jetstreamManagerImpl: fake.jetstreamManagerImpl,
    jetstreamImpl: fake.jetstreamImpl,
    ...overrides,
  });
}

const consumerCfg = { stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U" };

test("the leader opens exactly one consumer (regression: the .catch election bug)", async () => {
  const lm = new FakeLockManager();
  const core = makeFakeCore();
  const shell = createShell({
    identityId: "U",
    locks: lm,
    channel: null,
    createCore: () => core,
  });

  // Pins that ensureLeadership watches `settled` rather than the handle:
  // electLeader's handle has no `.catch`, so watching it would reject this
  // promise with a TypeError and make startConsumer throw on every Web-Locks
  // host.
  await shell.startConsumer(consumerCfg);
  assert.equal(core.calls.startConsumer, 1, "the leader opened its durable exactly once");
  assert.deepEqual(core.calls.lastConsumerCfg, consumerCfg);

  shell.close();
  await flush();
});

test("a follower opens no consumer until the leader releases", async () => {
  const lm = new FakeLockManager();
  const coreA = makeFakeCore();
  const coreB = makeFakeCore();

  const a = createShell({ identityId: "U", locks: lm, channel: null, createCore: () => coreA });
  await a.startConsumer(consumerCfg);
  assert.equal(coreA.calls.startConsumer, 1, "A leads and opens the durable");

  const b = createShell({ identityId: "U", locks: lm, channel: null, createCore: () => coreB });
  const bStarted = b.startConsumer(consumerCfg); // do not await: B must block on leadership
  await flush();
  assert.equal(coreB.calls.startConsumer, 0, "B must not open a second durable while A leads");

  // A's tab closes (or signs out): the lease releases and B takes over, resuming
  // from the cursor in the shared store (nothing in the election carries it).
  a.close();
  await bStarted;
  assert.equal(coreB.calls.startConsumer, 1, "B opens the durable once it becomes leader");

  b.close();
  await flush();
});

test("awaitLeadership gates on the same lease startConsumer does", async () => {
  // The wasm host awaits this BEFORE it resolves its cursor, its retention-gap
  // check and its delivery floor (jstransport.go's AwaitAttachReady): a follower
  // that resolved those at page boot would attach, whenever it is promoted, at a
  // position the stream may no longer retain — which JetStream clamps up, a
  // silent skip.
  const lm = new FakeLockManager();
  const a = createShell({ identityId: "U", locks: lm, channel: null, createCore: makeFakeCore });
  const b = createShell({ identityId: "U", locks: lm, channel: null, createCore: makeFakeCore });

  await a.awaitLeadership();

  let bReady = false;
  const bWait = b.awaitLeadership().then(() => {
    bReady = true;
  });
  await flush();
  assert.equal(bReady, false, "a follower is not entitled to attach while the leader holds the lease");

  a.close();
  await bWait;
  assert.equal(bReady, true, "the follower becomes ready once it is promoted");

  b.close();
  await flush();
});

test("awaitLeadership and startConsumer share one election", async () => {
  const lm = new FakeLockManager();
  const core = makeFakeCore();
  const shell = createShell({ identityId: "U", locks: lm, channel: null, createCore: () => core });

  await shell.awaitLeadership();
  await shell.awaitLeadership();
  await shell.startConsumer(consumerCfg);

  assert.equal(core.calls.startConsumer, 1, "the memoized lease is not re-elected per await");
  assert.deepEqual(core.calls.lastConsumerCfg, consumerCfg);

  shell.close();
  await flush();
});

test("no Web Locks host is trivially the sole leader", async () => {
  // With no `locks` in config and no navigator.locks (Node), the shell takes the
  // single-context path and starts immediately.
  const core = makeFakeCore();
  const shell = createShell({ identityId: "U", channel: null, createCore: () => core });
  await shell.startConsumer(consumerCfg);
  assert.equal(core.calls.startConsumer, 1);
  shell.close();
});

test("signalChange reaches other tabs, never the sender", async () => {
  const bus = makeBroadcastBus();
  const leader = createShell({ identityId: "U", channel: bus.channel(), createCore: makeFakeCore });
  const follower = createShell({ identityId: "U", channel: bus.channel(), createCore: makeFakeCore });

  const heardByFollower = [];
  const heardByLeader = [];
  follower.onPeerChange((key, deleted) => heardByFollower.push([key, deleted]));
  leader.onPeerChange((key, deleted) => heardByLeader.push([key, deleted]));

  leader.signalChange("manifest.svc.abc", false);
  leader.signalChange("manifest.op.xyz", true);
  await flush();

  assert.deepEqual(
    heardByFollower,
    [["manifest.svc.abc", false], ["manifest.op.xyz", true]],
    "the follower hears both changes with the deleted flag intact",
  );
  assert.deepEqual(heardByLeader, [], "a tab never hears its own signal");

  leader.close();
  follower.close();
});

test("onPeerChange unsubscribe stops delivery; close tears the channel down", async () => {
  const bus = makeBroadcastBus();
  const leader = createShell({ identityId: "U", channel: bus.channel(), createCore: makeFakeCore });
  const followerCore = makeFakeCore();
  const follower = createShell({ identityId: "U", channel: bus.channel(), createCore: () => followerCore });

  const heard = [];
  const unsub = follower.onPeerChange((key) => heard.push(key));

  leader.signalChange("manifest.svc.one", false);
  await flush();
  assert.deepEqual(heard, ["manifest.svc.one"]);

  unsub();
  leader.signalChange("manifest.svc.two", false);
  await flush();
  assert.deepEqual(heard, ["manifest.svc.one"], "an unsubscribed handler stops receiving");

  // close() tears down the follower's channel: a still-open leader's later
  // signal reaches nobody, and the core is closed.
  follower.close();
  const reReg = [];
  follower.onPeerChange((key) => reReg.push(key)); // handler on a closed shell
  leader.signalChange("manifest.svc.three", false);
  await flush();
  assert.deepEqual(reReg, [], "a closed shell delivers no further peer changes");
  assert.equal(followerCore.calls.close, 1, "close() closes the underlying core");

  leader.close();
});

test("getToken re-exposes config's own live getter, not a snapshot", async () => {
  // cmd/facet's wasm host (internal/edge/browser/host.go's shellGetTokenFunc)
  // pulls the current token from THIS method so the Gateway-write path shares
  // the same rotating credential the WS transport's reconnect authenticator
  // already reads from config.getToken (createSyncCore, above) — one source
  // of truth for "what's the current token", not two that can drift.
  let current = "token-a";
  const shell = createShell({ identityId: "U", channel: null, createCore: makeFakeCore, getToken: () => current });

  assert.equal(shell.getToken(), "token-a");
  current = "token-b";
  assert.equal(shell.getToken(), "token-b", "a later rotation must reach the shell with no reassembly");

  shell.close();
});

test("an empty or missing key is not broadcast", async () => {
  const bus = makeBroadcastBus();
  const leader = createShell({ identityId: "U", channel: bus.channel(), createCore: makeFakeCore });
  const follower = createShell({ identityId: "U", channel: bus.channel(), createCore: makeFakeCore });

  const heard = [];
  follower.onPeerChange((key) => heard.push(key));

  leader.signalChange("", false);
  leader.signalChange(undefined, false);
  await flush();
  assert.deepEqual(heard, [], "an empty key carries no useful change and is dropped");

  leader.close();
  follower.close();
});

// The vectors below drive createSyncCore's startConsumer directly (not
// through createShell — its fakes stub the core itself, not jsm/js), pinning
// the delete-then-create reposition (edge-cold-signin-delivery-position-design.md
// §3.3) at the level shell.mjs actually implements it.

test("startConsumer deletes the durable before creating it", async () => {
  const fake = makeFakeJetStream();
  const core = newFakeSyncCore(fake);

  await core.startConsumer({ stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 0 });

  assert.deepEqual(fake.calls.order, ["delete", "add"], "delete must precede create, every attach");
  assert.deepEqual(fake.calls.deleteArgs, { stream: "SYNC", durable: "edge-sync-U-D" });
});

test("startConsumer creates a positioned consumer when startSeq is given", async () => {
  const fake = makeFakeJetStream();
  const core = newFakeSyncCore(fake);

  await core.startConsumer({
    stream: "SYNC",
    durable: "edge-sync-U-D",
    filterSubject: "lattice.sync.user.U",
    startSeq: 42,
  });

  assert.equal(fake.calls.addArgs.stream, "SYNC");
  assert.equal(fake.calls.addArgs.cfg.deliver_policy, "by_start_sequence");
  assert.equal(fake.calls.addArgs.cfg.opt_start_seq, 42);
  assert.equal(fake.calls.addArgs.cfg.durable_name, "edge-sync-U-D");
  assert.equal(fake.calls.addArgs.cfg.filter_subject, "lattice.sync.user.U");
});

test("startConsumer omits deliver_policy/opt_start_seq when startSeq is 0 or absent", async () => {
  for (const cfg of [
    { stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 0 },
    { stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U" },
  ]) {
    const fake = makeFakeJetStream();
    const core = newFakeSyncCore(fake);
    await core.startConsumer(cfg);

    assert.equal("deliver_policy" in fake.calls.addArgs.cfg, false, "no override ⇒ today's unpositioned create");
    assert.equal("opt_start_seq" in fake.calls.addArgs.cfg, false);
  }
});

test("a not-found delete does not abort the attach", async () => {
  const fake = makeFakeJetStream({ deleteThrows: notFoundError() });
  const core = newFakeSyncCore(fake);

  await core.startConsumer({ stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 7 });

  assert.deepEqual(fake.calls.order, ["delete", "add"], "the create still runs after a not-found delete");
  assert.equal(fake.calls.addArgs.cfg.opt_start_seq, 7);
});

test("a delete error that is NOT not-found aborts the attach", async () => {
  // Proves the positive vector first: a not-found delete (above) does not
  // abort. This is the negative — any other failure (permission denied, a
  // transport error) must propagate, and the create must never run.
  const denied = new JetStreamApiError({ err_code: 10036, description: "permissions violation", code: 400 });
  const fake = makeFakeJetStream({ deleteThrows: denied });
  const core = newFakeSyncCore(fake);

  await assert.rejects(
    () => core.startConsumer({ stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 7 }),
    (err) => err === denied,
  );
  assert.deepEqual(fake.calls.order, ["delete"], "create must not run when the delete fails for a non-not-found reason");
});

// transientError stands in for the failures the vendored client raises when a
// JetStream API request never reaches a verdict — its 5s TimeoutError, a
// RequestError on a dropped socket. Neither is a JetStreamApiError nor a
// PermissionViolationError, which is exactly the property startConsumer
// classifies on, so a plain Error carries the same meaning here.
function transientError() {
  const err = new Error("TIMEOUT");
  err.name = "TimeoutError";
  return err;
}

test("a delete that times out is retried, and the attach completes", async () => {
  // The browser host runs one Run per page lifetime with no restart loop, so an
  // un-retried timeout on this delete would end sync until the user reloads.
  const fake = makeFakeJetStream({ deleteThrowsFirst: [transientError()] });
  const core = newFakeSyncCore(fake);

  await core.startConsumer({
    stream: "SYNC",
    durable: "edge-sync-U-D",
    filterSubject: "lattice.sync.user.U",
    startSeq: 9,
  });

  assert.equal(fake.calls.deleteAttempts, 2, "the timed-out delete is tried again");
  assert.deepEqual(fake.calls.order, ["delete", "delete", "add"], "the create runs once the retry succeeds");
  assert.equal(fake.calls.addArgs.cfg.opt_start_seq, 9, "the retried attach still creates the positioned consumer");
});

test("a delete that keeps timing out gives up bounded and aborts the attach", async () => {
  const timeout = transientError();
  const fake = makeFakeJetStream({ deleteThrows: timeout });
  const core = newFakeSyncCore(fake);

  await assert.rejects(
    () => core.startConsumer({ stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 9 }),
    (err) => err === timeout,
  );
  assert.equal(fake.calls.deleteAttempts, 3, "the retry is bounded, not endless");
  assert.equal(fake.calls.order.includes("add"), false, "an undeleted durable must never be created over");
});

test("a permissions denial aborts immediately and is never retried", async () => {
  // The ACL shape a denied $JS.API.CONSUMER.DELETE takes on the wire: the core
  // client parses the server's -ERR into a PermissionViolationError carrying the
  // operation and subject (nats.js.mjs:212-243). The server ANSWERED, so this is
  // a verdict, not a blip — retrying it would only delay a loud failure.
  const denied = new PermissionViolationError(
    'Permissions Violation for Publish to "$JS.API.CONSUMER.DELETE.SYNC.edge-sync-U-D"',
    "publish",
    "$JS.API.CONSUMER.DELETE.SYNC.edge-sync-U-D",
  );
  const fake = makeFakeJetStream({ deleteThrows: denied });
  const core = newFakeSyncCore(fake);

  await assert.rejects(
    () => core.startConsumer({ stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 7 }),
    (err) => err === denied,
  );
  assert.equal(fake.calls.deleteAttempts, 1, "a denial must fail fast, on the first attempt");
  assert.deepEqual(fake.calls.order, ["delete"], "create must not run when the delete was denied");
});

test("a JetStream API rejection that is not not-found is never retried either", async () => {
  const rejected = new JetStreamApiError({ err_code: 10036, description: "bad request", code: 400 });
  const fake = makeFakeJetStream({ deleteThrows: rejected });
  const core = newFakeSyncCore(fake);

  await assert.rejects(
    () => core.startConsumer({ stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 7 }),
    (err) => err === rejected,
  );
  assert.equal(fake.calls.deleteAttempts, 1, "the server answered; there is nothing transient to ride out");
});

test("a not-found delete is not retried — there is nothing left to delete", async () => {
  const fake = makeFakeJetStream({ deleteThrows: notFoundError() });
  const core = newFakeSyncCore(fake);

  await core.startConsumer({ stream: "SYNC", durable: "edge-sync-U-D", filterSubject: "lattice.sync.user.U", startSeq: 7 });

  assert.equal(fake.calls.deleteAttempts, 1);
  assert.deepEqual(fake.calls.order, ["delete", "add"]);
});
