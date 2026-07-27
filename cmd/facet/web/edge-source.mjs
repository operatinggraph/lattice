// edge-source adapts the browser-native engine's JS API (internal/edge/browser,
// exposed by the wasm artifact as the object latticeEdge.start() resolves to)
// to the renderer's feed-source interface (cmd/facet/web/app.js). It is the
// literal EDGE.5 W4 renderer swap: the Go host's SSE `/api/feed` frames become
// the engine's `onFrame` frames, and its POST `/api/enqueue` becomes the
// engine's `enqueue`, with nothing else changing — the frame shapes are the
// same struct on both hosts (internal/edge/browser/feed.go == cmd/facet/feed.go).
//
// This module is imported only by the boot module (boot.mjs); app.js consumes
// the source it produces duck-typed through window.__facetBoot, so app.js stays
// a plain classic script and never imports ESM.

// dispatchFrame routes one engine frame to the matching reducer handler by its
// `kind`. On the SSE host the kind is the event name (frame.Kind is json:"-");
// the engine carries it in-body (json:"kind"), so this is the one adaptation
// the swap needs. An unknown kind is ignored rather than thrown: a newer engine
// emitting a frame this renderer does not model must not break the stream.
export function dispatchFrame(handlers, fr) {
  if (!fr || typeof fr.kind !== "string") return;
  switch (fr.kind) {
    case "manifest":
      handlers.manifest(fr);
      break;
    case "outbox":
      handlers.outbox(fr.outbox);
      break;
    case "ready":
      handlers.ready(fr.revision);
      break;
    case "revoked":
      handlers.revoked(fr.reason || "");
      break;
    case "connectivity":
      handlers.connectivity(fr);
      break;
    default:
      // unmodelled frame kind — ignore
      break;
  }
}

// edgeSource wraps a started engine `api` (the object latticeEdge.start()
// resolves to: {onFrame, snapshot, enqueue, stop, ...}) as a feed source.
export function edgeSource(api) {
  let unsub = null;
  return {
    // start subscribes to the live delta stream, then replays the current
    // snapshot — the same burst the SSE host sends on connect: connectivity +
    // manifest rows + outbox, and `ready` only for a mirror already past its
    // catch-up. A cold engine's `ready` arrives live instead, which is what
    // holds the renderer's boot gate until there is a world to paint. Live and
    // snapshot frames both feed the LWW reducer, so their interleaving order
    // does not matter. Returns the snapshot promise so a caller can await the
    // first paint.
    start(handlers) {
      unsub = api.onFrame((fr) => dispatchFrame(handlers, fr));
      return Promise.resolve(api.snapshot()).then((frames) => {
        (frames || []).forEach((fr) => dispatchFrame(handlers, fr));
      });
    },
    // enqueue forwards the descriptor-form request to the engine. The engine
    // resolves {requestId} on accept and rejects on a malformed request; the
    // renderer only inspects body.error, so a resolved value with no error is
    // an accept, matching the SSE source's parsed-body contract.
    enqueue(request) {
      return Promise.resolve(api.enqueue(request));
    },
    // close unsubscribes the frame listener and stops the engine host — the
    // in-page analogue of closing the SSE connection on sign-out (§4.4 purge).
    close() {
      if (unsub) {
        unsub();
        unsub = null;
      }
      return api.stop();
    },
  };
}
