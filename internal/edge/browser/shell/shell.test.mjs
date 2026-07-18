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
import { createShell } from "./shell.mjs";

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

  // Before the fix this rejected: electLeader returns a handle with no `.catch`,
  // so ensureLeadership's promise rejected with a TypeError and startConsumer
  // threw on every Web-Locks host.
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
