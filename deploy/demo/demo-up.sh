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
seed_out="$(make seed-showcase 2>&1 | tee /dev/stderr)"
tenant1="$(sed -n 's/^FACET_TENANT1_NANOID=//p' <<<"$seed_out")"
tenant2="$(sed -n 's/^FACET_TENANT2_NANOID=//p' <<<"$seed_out")"
if [[ -z "$tenant1" || -z "$tenant2" ]]; then
	echo "demo-up: seed-showcase did not print both tenant ids" >&2
	exit 1
fi

# Labels match scripts/seed-showcase.go's two tenants (Riley in unit1, Sam in
# unit2). The ids rotate with every fresh world, so they are fed per-start
# rather than checked in anywhere.
personas="$(printf '[{"id":"%s","label":"Riley Chen","sub":"Resident · Unit 1"},{"id":"%s","label":"Sam Okafor","sub":"Resident · Unit 2"}]' "$tenant1" "$tenant2")"

echo "==> Restarting facet in demo-persona posture..."
pkill -f "bin/facet" 2>/dev/null || true
sleep 1
FACET_STORE_DIR=./facet-store \
	NATS_URL="${NATS_URL:-nats://localhost:4222}" \
	EDGE_GATEWAY_URL="${EDGE_GATEWAY_URL:-http://localhost:8080}" \
	FACET_DEV_AUTH=1 \
	FACET_PG_DSN="${FACET_PG_DSN:-postgres://facet_app:facet_app_dev@localhost:5432/lattice?sslmode=disable}" \
	FACET_DEMO_PERSONAS="$personas" \
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
