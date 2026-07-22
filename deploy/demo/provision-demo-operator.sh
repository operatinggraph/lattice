#!/usr/bin/env bash
# provision-demo-operator.sh — the hosted read-only Loupe's actor (F20): install
# the demo-operator grant package, mint a fresh identity holding ONLY the
# demoOperator role, wait until the platform actually authorizes it, and persist
# the key to loupe-demo-operator.json for demo-up.sh.
#
# The showcase world (and with it this identity) dies with every nightly reset,
# so demo-up.sh runs this on every bring-up. Idempotent per-world: a marker key
# that still authorizes is reused; a stale one (fresh world) is replaced. The
# minted email carries a per-attempt random suffix — email is an identity-index
# dedup key, so a fixed address would wedge every re-mint on the same world.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
export PATH="/usr/local/go/bin:$PATH"
export HOME="${HOME:-/root}"

NATS_URL="${NATS_URL:-nats://localhost:4222}"
NKEY_CLI="deploy/nkeys/lattice.nk"
NKEY_PKG="deploy/nkeys/lattice-pkg.nk"
BOOTSTRAP_JSON="${BOOTSTRAP_JSON_PATH:-$REPO_ROOT/lattice.bootstrap.json}"
MARKER="$REPO_ROOT/loupe-demo-operator.json"

# authorizes <actorKey>: submits a class-less ctrl.weaver.read as the actor.
# Step-3 authorization precedes step-4 hydration, so AuthDenied means the
# grant is not live while ANY other outcome (the class-less envelope's
# HydrationFailed included) proves the capability doc authorized the read.
authorizes() {
	local out
	out=$(NATS_URL="$NATS_URL" NATS_NKEY="$NKEY_CLI" ./bin/lattice op submit \
		--operation-type ctrl.weaver.read --actor "$1" --output json --payload '{}' 2>&1 || true)
	! grep -q "AuthDenied" <<<"$out"
}

if [[ -f "$MARKER" ]]; then
	existing="$(jq -r '.operatorActorKey // empty' "$MARKER")"
	if [[ -n "$existing" ]] && authorizes "$existing"; then
		echo "==> Demo operator already provisioned + authorized: $existing"
		exit 0
	fi
	echo "==> Stale demo-operator marker (fresh world) — re-provisioning."
fi

echo "==> Installing demo-operator grant package (idempotent)..."
NATS_URL="$NATS_URL" NATS_NKEY="$NKEY_PKG" BOOTSTRAP_JSON_PATH="$BOOTSTRAP_JSON" \
	./bin/lattice-pkg install packages/demo-operator
make provision-readpath >/dev/null
echo "==> Read-path provisioned for demoOperatorReadGrants."

ADMIN_KEY="vtx.identity.$(jq -r '.primordialIDs.bootstrapIdentity' "$BOOTSTRAP_JSON")"
ROLE_KEY="$(go run ./scripts/print-role-id.go demo-operator demoOperator)"
suffix="$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')"
OP_KEY="$(NATS_URL="$NATS_URL" NATS_NKEY="$NKEY_CLI" ./bin/lattice identity create-unclaimed \
	--actor "$ADMIN_KEY" --output json \
	--payload "{\"name\":\"Demo Operator\",\"email\":\"demo-operator-${suffix}@demo.lattice.local\"}" \
	| jq -r '.data.primaryKey')"
[[ -n "$OP_KEY" && "$OP_KEY" != "null" ]] || { echo "provision-demo-operator: identity mint failed" >&2; exit 1; }
echo "==> Demo operator identity: $OP_KEY"
NATS_URL="$NATS_URL" NATS_NKEY="$NKEY_CLI" ./bin/lattice op submit \
	--operation-type AssignRole --actor "$ADMIN_KEY" --output json \
	--payload "{\"actorKey\":\"$OP_KEY\",\"roleKey\":\"$ROLE_KEY\"}" \
	--context-hint-reads "$OP_KEY,$ROLE_KEY" >/dev/null
echo "==> demoOperator assigned; waiting for the capability projection..."

deadline=$((SECONDS + 240))
until authorizes "$OP_KEY"; do
	if ((SECONDS >= deadline)); then
		echo "provision-demo-operator: grant never authorized within 4m (capability projection gap?)" >&2
		exit 1
	fi
	sleep 5
done
printf '{"operatorActorKey":"%s"}\n' "$OP_KEY" >"$MARKER"
echo "==> Demo operator ready: $OP_KEY (persisted to $MARKER)"
