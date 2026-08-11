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
//   DURABLE    optional durable-name override (default edge-sync-<IDENTITY>-<DEVICE>);
//              only "delete-denied" needs this, to target a durable this
//              token's grant does not own
//   STARTSEQ   optional OptStartSeq for "create-positioned"/"delete" (default 1;
//              any value works — an out-of-range one is clamped, never denied)
//   MODE       "create" (expect the create to succeed) |
//              "create-denied" (expect the create itself to be denied) |
//              "create-positioned" (positioned create, expect success) |
//              "delete" (attach once, detach, reposition on the SAME durable —
//              proves DELETE.SYNC.<durable> is granted against a REAL
//              consumer, not only a not-found one) |
//              "delete-denied" (expect the delete itself to be denied) |
//              "roundtrip" (create, then receive+ack one published delta)
//
// The shell's other transport method (request) is at parity
// with the trusted Go node's natstransport by construction — this driver pins
// the two methods whose wire form the browser client (nats.js) could shape
// differently from nats.go: consumer create and consumer delete.
//
// A denial verdict (CREATE_DENIED / DELETE_DENIED) is reported ONLY on a
// genuine NATS permissions violation for the EXACT subject the call under
// test tried, never on any other failure — a dead WS_URL, a bad TOKEN, or a
// wrong STREAM must fail this driver (verdict ERROR), not report a denial it
// never actually observed. PermissionViolationError is the vendored client's
// own structured signal for this (nats.js.mjs ProtocolHandler.processError /
// .toError, ~line 7108-7161): the server's async -ERR is parsed into
// {operation, subject}, and specifically the pending request whose subject
// matches is rejected with it (MuxSubscriptions.handleError, ~line 1559-1576)
// — not a generic timeout, and nothing here has to substring-match a message.

import { createSyncCore } from "../shell.mjs";
import { wsconnect, tokenAuthenticator, jetstreamManager, PermissionViolationError } from "../nats.js.mjs";

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
const startSeq = Number(process.env.STARTSEQ || "1");

const durable = process.env.DURABLE || `edge-sync-${identity}-${device}`;

// isPermissionDenial is true only for the vendored client's own structured
// "this exact subject was denied" signal — see the module doc above. Any
// other throw (connection refused, auth rejected outright, a timeout, a
// JetStream-level error unrelated to permissions) is NOT a denial verdict.
//
// A request-response call (which is what both consumers.add and
// consumers.delete are, under core-NATS request-reply) never rejects with
// the bare PermissionViolationError — RequestOne.resolver (nats.js.mjs
// ~line 7568-7586) always wraps a non-timeout failure in a RequestError,
// setting the original as `.cause` (the same shape RequestError.isNoResponders()
// reads via `this.cause instanceof NoRespondersError`). The real signal is one
// unwrap away.
function isPermissionDenial(err, expectedSubject) {
  const cause = err instanceof PermissionViolationError ? err : err?.cause;
  return cause instanceof PermissionViolationError && cause.subject === expectedSubject;
}

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

// runDeleteDenied isolates the delete verb: it opens its OWN bare connection
// and calls jsm.consumers.delete directly, with no create anywhere in the
// call. A shared startConsumer (delete-then-create) could not tell which
// verb a denial belonged to when DURABLE is foreign — the create would ALSO
// be denied on that same foreign durable, so a bare "it threw" verdict would
// pass even if the code stopped issuing the delete at all. This function's
// only job is proving DELETE.<stream>.<durable> is denied, and reporting the
// exact subject the server denied it on.
async function runDeleteDenied() {
  const inboxPrefix = "_INBOX.edge." + identity;
  const nc = await wsconnect({
    servers: wsURL,
    name: device,
    inboxPrefix,
    authenticator: tokenAuthenticator(() => token),
  });
  const jsm = await jetstreamManager(nc, { checkAPI: false });
  const expectedSubject = `$JS.API.CONSUMER.DELETE.${stream}.${durable}`;
  try {
    await jsm.consumers.delete(stream, durable);
    await nc.drain().catch(() => {});
    done(false, "UNEXPECTED_DELETE_OK");
  } catch (err) {
    await nc.drain().catch(() => {});
    if (isPermissionDenial(err, expectedSubject)) {
      done(true, `DELETE_DENIED ${expectedSubject}`);
    } else {
      done(false, `ERROR ${String(err?.message ?? err)}`);
    }
  }
}

async function runCreateDenied() {
  // The filtered-create subject form addUpdate emits (nats.js.mjs ~line
  // 10142): $JS.API.CONSUMER.CREATE.<stream>.<durable>.<filter>. durable
  // stays this token's own (create-denied varies only FILTER), so the
  // delete startConsumer issues first targets an authorized subject and
  // never itself risks a denial — only the create can be denied here.
  const expectedSubject = `$JS.API.CONSUMER.CREATE.${stream}.${durable}.${filter}`;
  try {
    await core.startConsumer({ stream, durable, filterSubject: filter });
    done(false, "UNEXPECTED_CREATE_OK");
  } catch (err) {
    if (isPermissionDenial(err, expectedSubject)) {
      done(true, `CREATE_DENIED ${expectedSubject}`);
    } else {
      done(false, `ERROR ${String(err?.message ?? err)}`);
    }
  }
}

try {
  if (mode === "create-denied") {
    await runCreateDenied();
  } else if (mode === "delete-denied") {
    await runDeleteDenied();
  } else if (mode === "roundtrip") {
    await core.startConsumer({ stream, durable, filterSubject: filter });
    const timeout = new Promise((_, rej) =>
      setTimeout(() => rej(new Error("no delta delivered within 8s")), 8_000),
    );
    const got = await Promise.race([delivered, timeout]);
    done(true, `DELIVERED subject=${got.subject} seq=${got.seq} len=${got.len}`);
  } else if (mode === "create-positioned") {
    await core.startConsumer({ stream, durable, filterSubject: filter, startSeq });
    done(true, `CREATE_OK durable=${durable} startSeq=${startSeq}`);
  } else if (mode === "delete") {
    // Attach unpositioned first so a durable actually EXISTS under this name
    // — "create" mode's own implicit delete (every startConsumer deletes
    // first, positioned or not) only ever sees a not-found consumer, so it
    // never proves DELETE.SYNC.<durable> is granted against a real one.
    await core.startConsumer({ stream, durable, filterSubject: filter });
    await core.stopConsumer();
    // Re-attach positioned on the SAME durable. startConsumer deletes it
    // first; if that delete had not actually reached the server (a wire-form
    // drift or an ACL denial silently swallowed), this create-or-update would
    // fail outright — the server refuses to change an existing consumer's
    // DeliverPolicy (nats-server 2.14 server/consumer.go:2435). Success here
    // is therefore itself proof the delete executed on the wire, not a stub.
    await core.startConsumer({ stream, durable, filterSubject: filter, startSeq });
    done(true, `REPOSITIONED durable=${durable} startSeq=${startSeq}`);
  } else {
    await core.startConsumer({ stream, durable, filterSubject: filter });
    done(true, `CREATE_OK durable=${durable}`);
  }
} catch (err) {
  done(false, `CREATE_ERROR ${String(err?.message ?? err)}`);
}
