// Leader-election unit vectors (edge-browser-node-design.md §5) — run with
// `node --test`. They drive electLeader against a fake LockManager that models
// the Web Locks API's one-holder-at-a-time queuing, so the multi-tab handoff
// logic is proven without a browser: exactly one tab holds the sync lease, and
// a follower takes over only when the leader releases (a real tab does that by
// closing; here we call release()).

import { test } from "node:test";
import assert from "node:assert/strict";
import { electLeader } from "./leader.mjs";

// FakeLockManager reproduces the one-relevant Web Locks guarantee: for a given
// name, at most one request's callback runs at a time; the next queued callback
// runs only once the current one's returned promise settles (the lock releases).
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
    if (q.length === 1) startTurn(); // head of line runs immediately
    return settled;
  }
}

test("exactly one tab leads; a follower waits until the leader releases", async () => {
  const lm = new FakeLockManager();
  const events = [];

  const a = electLeader({
    lockName: "sync-U",
    locks: lm,
    onAcquire: () => events.push("A-acquire"),
    onRelease: () => events.push("A-release"),
  });
  await a.acquired;
  assert.deepEqual(events, ["A-acquire"], "A becomes leader immediately");

  // B requests the same lock while A holds it: it must not acquire yet.
  let bAcquired = false;
  const b = electLeader({
    lockName: "sync-U",
    locks: lm,
    onAcquire: () => {
      bAcquired = true;
      events.push("B-acquire");
    },
    onRelease: () => events.push("B-release"),
  });
  // Let any spurious acquisition flush.
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(bAcquired, false, "B must not lead while A holds the lease");

  // A's tab closes → release → the lease hands off to B, which resumes from the
  // cursor already in the shared store (nothing in the election needs to carry
  // it — that is the point of storing the cursor, not holding it in the leader).
  a.release();
  await b.acquired;
  assert.equal(bAcquired, true, "B leads after A releases");
  assert.deepEqual(
    events,
    ["A-acquire", "A-release", "B-acquire"],
    "handoff order: A leads, A releases, then B leads — never two leaders at once",
  );

  b.release();
  await b.settled;
});

test("a single tab leads with no contention", async () => {
  const lm = new FakeLockManager();
  let led = false;
  const only = electLeader({
    lockName: "sync-solo",
    locks: lm,
    onAcquire: () => {
      led = true;
    },
  });
  await only.acquired;
  assert.equal(led, true);
  only.release();
  await only.settled;
});

test("electLeader rejects a missing or malformed LockManager", () => {
  assert.throws(() => electLeader({ lockName: "x", locks: null }), /LockManager/);
  assert.throws(() => electLeader({ lockName: "x", locks: {} }), /LockManager/);
  assert.throws(() => electLeader({ lockName: "", locks: new FakeLockManager() }), /lockName/);
});
