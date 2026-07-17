// Web Locks leader election for the multi-tab Edge browser node
// (edge-browser-node-design.md §3.3). Two tabs of one identity share one origin
// and one IndexedDB; without a single elected leader they open two durables that
// split one pull stream and both mirrors diverge. This elects exactly one tab to
// hold the sync connection.
//
// The Web Locks API models exactly this: `locks.request(name, cb)` runs cb while
// holding an exclusive lock and releases it when cb's returned promise settles —
// including when the tab is closed (the browser drops the held lock), at which
// point a queued waiter's cb runs and becomes the new leader. Resume is free:
// the cursor lives in the shared IndexedDB, so a taking-over follower continues
// from the ack floor already stored.
//
// `locks` is a parameter, not a hard reference to navigator.locks, so the
// election logic is exercised by unit vectors against a fake LockManager
// (handoff, cursor-resume) without a browser.

// electLeader requests the named lock and reports leadership through onAcquire.
// It returns a handle:
//   acquired  a Promise that resolves when this context becomes leader
//   release() voluntarily gives up the lock (for tests and explicit teardown);
//             in a real tab the lock is normally released by the tab closing,
//             not by this call
//
// The lock is held until release() is called or the underlying context ends, so
// the callback parks on an internal promise. onRelease fires when leadership is
// lost, whichever way.
export function electLeader({ lockName, locks, onAcquire, onRelease }) {
  if (!lockName) throw new Error("edge/leader: lockName is required");
  if (!locks || typeof locks.request !== "function") {
    throw new Error("edge/leader: locks must be a LockManager (navigator.locks)");
  }

  let releaseHeld;
  const held = new Promise((resolve) => {
    releaseHeld = resolve;
  });
  let acquireResolve;
  const acquired = new Promise((resolve) => {
    acquireResolve = resolve;
  });

  // The request promise settles when the lock is released (callback returns).
  const settled = locks.request(lockName, async () => {
    onAcquire?.();
    acquireResolve();
    try {
      await held;
    } finally {
      onRelease?.();
    }
  });

  return {
    acquired,
    settled,
    release() {
      releaseHeld();
    },
  };
}
