# Vendored `nats.js` — regeneration recipe

`nats.js.mjs` is a **single-graph ESM bundle** of the NATS project's official
browser client, checked in as a static file. The repo has no npm toolchain and
the Facet PWA is served as plain files (edge-browser-node-design.md §3.3), so
the dependency ships as vendored output, not as a `package.json` + a bundler in
the tree. Authority + pin live in [`docs/vendors.md`](../../../../docs/vendors.md).

## Pins

| Package | Version |
|---|---|
| `@nats-io/nats-core` | 3.4.0 |
| `@nats-io/jetstream` | 3.4.0 |
| `@nats-io/nkeys` (transitive) | 2.0.3 |
| `@nats-io/nuid` (transitive) | 3.0.0 |
| `esbuild` (bundler, build-time only) | 0.24.2 |

The two `@nats-io` packages must move together — `@nats-io/jetstream` layers
over the same `@nats-io/nats-core` instance the shell's `wsconnect` opens, so a
single bundle keeps one copy of core in the graph (two copies break the
`instanceof` the JetStream client does on the connection).

## Regenerate

Run in a scratch directory (nothing here touches the repo tree but the output):

```sh
npm init -y
npm i @nats-io/nats-core@3.4.0 @nats-io/jetstream@3.4.0 esbuild@0.24.2

cat > entry.mjs <<'EOF'
export { wsconnect, tokenAuthenticator, headers } from "@nats-io/nats-core";
export { jetstream, jetstreamManager, AckPolicy } from "@nats-io/jetstream";
EOF

BANNER='// Vendored nats.js browser client — DO NOT EDIT BY HAND.
// Regenerate with the recipe in ./VENDOR.md.
// @nats-io/nats-core 3.4.0 + @nats-io/jetstream 3.4.0 (transitive: @nats-io/nkeys 2.0.3, @nats-io/nuid 3.0.0)
// Bundled with esbuild 0.24.2 (--bundle --format=esm --platform=browser --target=es2022).
// Authority: https://github.com/nats-io/nats.js (docs/vendors.md). NATS server pin: 2.14.'

./node_modules/.bin/esbuild entry.mjs --bundle --format=esm --platform=browser \
  --target=es2022 --banner:js="$BANNER" \
  --outfile=<repo>/internal/edge/browser/shell/nats.js.mjs
```

The only exports the shell (and its parity test) reach are the six named in
`entry.mjs`. `esbuild` is deterministic for a given input, so a regeneration on
the same pins reproduces the file byte-for-byte apart from the banner.

## What proves it still works

`make test-edge-consumer-parity` (CI job `edge-consumer-parity`) drives this
bundle from Node against an embedded NATS server standing up the **real**
`internal/gateway/natsauth` per-identity permission callout, and asserts the
JetStream client emits exactly the granted consumer-create wire form
(`$JS.API.CONSUMER.CREATE.SYNC.<durable>.<filter>`). A pin bump that changed
that wire form would fail closed there, not silently in a user's tab.
