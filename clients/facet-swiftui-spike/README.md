# FacetSwiftUISpike

Second-renderer spike for the Facet edge showcase app
(`_bmad-output/implementation-artifacts/edge-showcase-app-design.md` §7 Fire 5): a native SwiftUI
client that hydrates from the exact same `manifest.*` feed the PWA renderer (`cmd/facet/web`) renders
from, over `cmd/facet`'s already-shipped browser-facing HTTP+SSE surface. No NATS/crypto client of its
own — the Go host owns auth and the wire connection; this is proof that any renderer speaking HTTP+SSE
can hydrate from it, not just a browser.

## Honest scope caveat

This machine has Xcode Command Line Tools but no full Xcode.app and no iOS Simulator SDK
(`xcrun --sdk iphonesimulator --show-sdk-path` fails). The package therefore targets **macOS 13**, not
iOS — the closest buildable/runnable proxy available here. It uses the identical SwiftUI framework and
declarative paradigm an iOS build would, and the manifest-consuming code
(`Sources/FacetManifestKit`) has zero platform-specific API in it, so the actual claim under test —
that the manifest/descriptor vocabulary is renderer-neutral, not that this exact bundle runs on an
iPhone — is proven. A literal iOS build (device or simulator) needs a machine with full Xcode installed
and is unstarted; that is the actual remaining gap before the design's FORK-1 freeze trigger is
completely satisfied, not a re-architecture.

## Layout

- `Sources/FacetManifestKit` — platform-agnostic: `JSONValue` (loosely-typed JSON, mirroring the Go
  host's `json.RawMessage` posture), `ManifestFrame` (mirrors `cmd/facet/feed.go`'s `frame` struct),
  `SSEDecoder` (pure line-based SSE parser, unit-tested), `FeedClient` (dev-login + live SSE stream
  against a running `cmd/facet` host).
- `Sources/FacetSwiftUISpike` — the SwiftUI app: `ManifestStore` (last-write-wins reducer over the
  frame stream, the same reducer shape `app.js`'s manifest handler uses), `ContentView` (renders
  Services/Catalog/Tasks/My Instances sections straight off manifest row fields — no manifest-specific
  text anywhere in the view code), `FacetSwiftUISpikeApp` (entry point).
- `Tests/FacetManifestKitTests` — XCTest coverage of `SSEDecoder`/`ManifestFrame`/`JSONValue`. **Could
  not run in this sandbox** (no XCTest module without full Xcode — see caveat above); will run under a
  normal Xcode toolchain. The same assertions were verified live via a throwaway `swift run` smoke
  check during this fire (not checked in) — see the Fire 5 §7 build note in the design doc for the
  transcript.

## Running it

```
cd clients/facet-swiftui-spike
swift build
FACET_BASE_URL=http://127.0.0.1:7810 FACET_IDENTITY_ID=<20-char-NanoID> swift run FacetSwiftUISpike
```

The identity id is a `make seed-showcase` tenant (`FACET_TENANT1_NANOID`/`FACET_TENANT2_NANOID` from its
output) — `up-facet` runs with `FACET_DEV_AUTH=1` and no boot identity, so a session must be established
via `POST /api/dev-login` first; `FeedClient.devLogin` does this before opening the feed.

## A note on `URLSession.bytes(for:)`

`FeedClient.stream()` uses a delegate-based `URLSessionDataTask`, not the newer
`URLSession.bytes(for:)` async-sequence API. `bytes(for:)` was tried first and, live against a running
`cmd/facet` host, never yielded a single line for this endpoint — a long-lived SSE connection that never
closes and pings every 20s (`curl` streamed it instantly; `AsyncBytes.lines` sat empty past a 10s
timeout in the same process). The delegate's `urlSession(_:dataTask:didReceive:)` callback fires as
bytes actually arrive and does not have this problem. Worth knowing if a future increment touches this
file.
