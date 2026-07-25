#!/usr/bin/env bash
# demo-up.sh — bring the hosted demo up on this box (see README.md): the
# up-facet chain (full stack, provisions, showcase installs, idempotent seed),
# with Facet started under the demo-persona posture instead of up-facet's
# plain dev start. Safe to re-run against a live stack.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
export PATH="/usr/local/go/bin:$PATH"
# systemd runs the demo units with no HOME; Go derives GOPATH/GOMODCACHE/GOCACHE
# from it, so an unset HOME kills every `go build` in the up chain.
export HOME="${HOME:-/root}"

make up-full
make provision-gateway-identity-provisioner
make install-showcase-domains
make install-edge-manifest
make provision-facet-role
make provision-readpath

echo "==> Building facet binary..."
go build -o bin/facet ./cmd/facet

echo "==> Loading the showcase dataset (idempotent)..."
# Under systemd fd 2 is a journal socket, which open(2) refuses (ENXIO), so a
# /dev/stderr path-open cannot carry the live copy — tee to a temp file: the
# pipeline's stdout streams to the journal, the file feeds the id extraction.
seed_log="$(mktemp)"
make seed-showcase 2>&1 | tee "$seed_log"
tenant1="$(sed -n 's/^FACET_TENANT1_NANOID=//p' "$seed_log")"
tenant2="$(sed -n 's/^FACET_TENANT2_NANOID=//p' "$seed_log")"
staff="$(sed -n 's/^FACET_STAFF_NANOID=//p' "$seed_log")"
rm -f "$seed_log"
if [[ -z "$tenant1" || -z "$tenant2" || -z "$staff" ]]; then
	echo "demo-up: seed-showcase did not print all three persona ids" >&2
	exit 1
fi

# Labels match scripts/seed-showcase.go's personas (Riley in unit1, Sam in
# unit2, Dana the frontOfHouse staff — her world composes from worksAt +
# holdsRole, the staff-worlds spine). The ids rotate with every fresh world,
# so they are fed per-start rather than checked in anywhere.
personas="$(printf '[{"id":"%s","label":"Riley Chen","sub":"Resident · Unit 1"},{"id":"%s","label":"Sam Okafor","sub":"Resident · Unit 2"},{"id":"%s","label":"Dana Whitfield","sub":"Staff · Front of house"}]' "$tenant1" "$tenant2" "$staff")"

# Hosted read-only Loupe (F20): provisioned + started only when the box
# declares a public host for it (demo-bootstrap.sh's second argument). Bounded,
# non-blocking attempt — a fresh-world rescan routinely outlasts any timeout
# reasonable to wait on here, so this never holds up the rest of bring-up (or
# the nightly reset unit); lattice-demo-loupe-retry.timer keeps retrying every
# few minutes until the rescan actually drains (F21 Fire 3). A provisioning
# failure degrades to no Loupe demo for now — never a failed bring-up, Facet
# is the primary surface.
PROVISION_TIMEOUT_SECONDS="${PROVISION_TIMEOUT_SECONDS:-90}" \
	"$REPO_ROOT/deploy/demo/start-demo-loupe.sh" ||
	echo "demo-up: demo Loupe not ready yet; lattice-demo-loupe-retry.timer will keep trying" >&2

echo "==> Restarting facet in demo-persona posture..."
pkill -f "bin/facet" 2>/dev/null || true
sleep 1
# The public origin Caddy serves Facet at, written by demo-bootstrap.sh. Facet
# binds loopback behind TLS termination, so this declaration is what admits the
# browser's own Origin on every write and puts Secure on the session cookie.
# Absent, Facet still starts and serves reads, but every write 403s — so say so
# loudly rather than leaving a silent half-working demo.
# A proxied box with no marker is a hard stop, not a warning: Caddy being active
# says this box IS served publicly, so bringing Facet up undeclared would publish
# a demo whose pages render and whose every write — sign-in included — 403s, with
# nothing in the UI to say why. Refusing here is louder than a visitor finding out.
facet_public_origin=""
if [[ -f "$REPO_ROOT/demo-host" ]]; then
	facet_public_origin="https://$(cat "$REPO_ROOT/demo-host")"
elif command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet caddy; then
	echo "demo-up: caddy is serving this box but there is no demo-host marker, so Facet cannot be told its public origin" >&2
	echo "demo-up: run 'deploy/demo/demo-bootstrap.sh <host> [loupe-host]' — a pull alone does not write the marker" >&2
	exit 1
else
	echo "demo-up: no demo-host marker and no local proxy — starting Facet for loopback access only" >&2
fi
FACET_STORE_DIR=./facet-store \
	FACET_PUBLIC_ORIGIN="$facet_public_origin" \
	NATS_URL="${NATS_URL:-nats://localhost:4222}" \
	EDGE_GATEWAY_URL="${EDGE_GATEWAY_URL:-http://localhost:8080}" \
	FACET_DEV_AUTH=1 \
	FACET_PG_DSN="${FACET_PG_DSN:-postgres://facet_app:facet_app_dev@localhost:5432/lattice?sslmode=disable}" \
	FACET_DEMO_PERSONAS="$personas" \
	NATS_NKEY=deploy/nkeys/facet.nk \
	nohup ./bin/facet >facet.log 2>&1 </dev/null &

for _ in $(seq 1 30); do
	if curl -fsS -o /dev/null http://127.0.0.1:7810/login; then
		echo "==> Demo up: facet healthy on :7810 (Riley=$tenant1 Sam=$tenant2)"
		exit 0
	fi
	sleep 1
done
echo "demo-up: facet did not become healthy on :7810 (see facet.log)" >&2
exit 1
