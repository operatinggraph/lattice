// consumer_create_driver.mjs — the Node half of the consumer-create wire-form
// parity test (edge-browser-node-design.md §2.3/§5).
//
// It drives the REAL shell transport core (../shell.mjs over the vendored
// ../nats.js.mjs) against an embedded NATS WebSocket server the Go test stands
// up with the production per-identity permission callout
// (internal/gateway/natsauth). Its whole job is to prove the vendored JetStream
// client emits the ACL-granted consumer-create wire form
// ($JS.API.CONSUMER.CREATE.SYNC.<durable>.<filter>): a client emitting any other
// form is denied by the grant and this exits non-zero — so a wire-form drift is
// caught here, loudly, not silently in a user's browser tab.
//
// It prints one machine-readable line to stdout and exits 0 on the expected
// outcome, non-zero otherwise. All diagnostics go to stderr.
//
// Env:
//   WS_URL     ws:// URL of the embedded server's WebSocket listener
//   TOKEN      the bearer JWT the callout authorizes (the connection's identity)
//   IDENTITY   the verified identity id (drives the inbox prefix + subjects)
//   DEVICE     the device id (drives the durable name)
//   STREAM     the JetStream stream to consume (SYNC)
//   FILTER     the consumer's filter subject
//   MODE       "create" (expect the create to succeed) |
//              "create-denied" (expect the create to be denied) |
//              "roundtrip" (create, then receive+ack one published delta)
//
// The shell's other transport method (request) is at parity
// with the trusted Go node's natstransport by construction — this driver pins
// the one method whose wire form the browser client (nats.js) could shape
// differently from nats.go: consumer create.

import { createSyncCore } from "../shell.mjs";

function env(name) {
  const v = process.env[name];
  if (!v) {
    console.error(`consumer_create_driver: missing env ${name}`);
    process.exit(2);
  }
  return v;
}

const wsURL = env("WS_URL");
const token = env("TOKEN");
const identity = env("IDENTITY");
const device = env("DEVICE");
const stream = env("STREAM");
const filter = env("FILTER");
const mode = process.env.MODE || "create";

const durable = `edge-sync-${identity}-${device}`;

const core = createSyncCore({
  url: wsURL,
  identityId: identity,
  deviceId: device,
  getToken: () => token,
  logger: { warn: (...a) => console.error("warn:", ...a), debug: () => {} },
});

let deliveredResolve;
const delivered = new Promise((r) => {
  deliveredResolve = r;
});

if (mode === "roundtrip") {
  // The wasm host's api.deliver, stubbed: acknowledge the first delta and
  // signal it arrived. Proves MSG.NEXT + $JS.ACK are granted too, not just
  // CREATE — the full nats.js consume path under the real grant.
  core.deliver = (subject, body, seq) => {
    deliveredResolve({ subject, seq: Number(seq), len: body?.length ?? 0 });
    return "ack";
  };
}

// A watchdog so a stuck connection produces a diagnostic instead of a bare,
// output-less exit. This exact hang bit once: on a runtime with no global
// WebSocket (Node < 22), nats.js `wsconnect` never settles, the awaits below
// hang, and the process exits 13 ("unsettled top-level await") with no stdout —
// an opaque failure. The timer is deliberately kept referenced: that is what
// holds the event loop open long enough to emit this line instead of exiting
// empty-handed; done() clears it on every success path.
const watchdog = setTimeout(() => {
  console.log("DRIVER_TIMEOUT (connection never settled — check the runtime has a global WebSocket, Node >= 22)");
  process.exit(1);
}, 20_000);

function done(ok, line) {
  clearTimeout(watchdog);
  console.log(line);
  // Give a drained close a moment; never hang the test on cleanup.
  core.close().finally(() => process.exit(ok ? 0 : 1));
}

try {
  if (mode === "create-denied") {
    await core.startConsumer({ stream, durable, filterSubject: filter });
    // The create resolved but should have been denied — the grant is not
    // enforcing the wire form, which is the failure this control case exists
    // to catch.
    done(false, "UNEXPECTED_CREATE_OK");
  } else if (mode === "roundtrip") {
    await core.startConsumer({ stream, durable, filterSubject: filter });
    const timeout = new Promise((_, rej) =>
      setTimeout(() => rej(new Error("no delta delivered within 8s")), 8_000),
    );
    const got = await Promise.race([delivered, timeout]);
    done(true, `DELIVERED subject=${got.subject} seq=${got.seq} len=${got.len}`);
  } else {
    await core.startConsumer({ stream, durable, filterSubject: filter });
    done(true, `CREATE_OK durable=${durable}`);
  }
} catch (err) {
  const msg = String(err?.message ?? err);
  if (mode === "create-denied") {
    // Denied as expected — the grant enforces the filtered-create form.
    done(true, `CREATE_DENIED ${msg}`);
  } else {
    done(false, `CREATE_ERROR ${msg}`);
  }
}
