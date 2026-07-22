#!/usr/bin/env bash
# start-demo-loupe.sh — bounded attempt to bring up the read-only demo Loupe
# (F20): no-op if this box has no demo-loupe-host configured, no-op if it's
# already answering, otherwise a single provisioning attempt (bounded by
# PROVISION_TIMEOUT_SECONDS) followed by a start. Safe to call repeatedly —
# demo-up.sh calls it once at bring-up with a short bound so a slow post-reset
# rescan never blocks the rest of bring-up, and lattice-demo-loupe-retry.timer
# calls it every few minutes to pick up the moment the rescan actually drains
# (F21 Fire 3).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
export PATH="/usr/local/go/bin:$PATH"
export HOME="${HOME:-/root}"

loupe_host_file="$REPO_ROOT/demo-loupe-host"
[[ -f "$loupe_host_file" ]] || exit 0
loupe_host="$(cat "$loupe_host_file")"

if curl -fsS -o /dev/null -H "Host: ${loupe_host}" http://127.0.0.1:7778/login 2>/dev/null; then
	echo "==> Demo Loupe already up on :7778."
	exit 0
fi

if ! "$REPO_ROOT/deploy/demo/provision-demo-operator.sh"; then
	echo "start-demo-loupe: provisioning not ready yet — demo Loupe stays down for now" >&2
	exit 1
fi

# PID-file targeted, NOT `pkill -f "bin/loupe"` — that pattern also matches
# the unrelated console-operator Loupe (:7777) that `make up-full` starts and
# manages with its own pkill/restart, which this script must never touch.
pidfile="$REPO_ROOT/loupe-demo.pid"
if [[ -f "$pidfile" ]]; then
	oldpid="$(cat "$pidfile")"
	if [[ -n "$oldpid" ]] && kill -0 "$oldpid" 2>/dev/null \
		&& ps -p "$oldpid" -o comm= 2>/dev/null | grep -q loupe; then
		kill "$oldpid" 2>/dev/null || true
		sleep 1
	fi
	rm -f "$pidfile"
fi

demo_op_key="$(jq -r '.operatorActorKey' "$REPO_ROOT/loupe-demo-operator.json")"
echo "==> Starting the read-only demo Loupe (:7778) for https://${loupe_host}..."
LOUPE_ADDR=127.0.0.1:7778 \
	LOUPE_DEMO_MODE=1 \
	LOUPE_OPERATOR_ACTOR_KEY="$demo_op_key" \
	LOUPE_DEV_AUTH=1 \
	LOUPE_PUBLIC_ORIGIN="https://${loupe_host}" \
	NATS_URL="${NATS_URL:-nats://localhost:4222}" \
	NATS_NKEY=deploy/nkeys/loupe.nk \
	BOOTSTRAP_JSON_PATH="$REPO_ROOT/lattice.bootstrap.json" \
	LOUPE_PG_DSN="${LOUPE_PG_DSN:-postgres://loupe_pg:loupe_pg_dev@localhost:5432/lattice?sslmode=disable}" \
	nohup ./bin/loupe >loupe-demo.log 2>&1 </dev/null &
echo $! >"$pidfile"

for _ in $(seq 1 20); do
	if curl -fsS -o /dev/null -H "Host: ${loupe_host}" http://127.0.0.1:7778/login; then
		echo "==> Demo Loupe up on :7778 (actor=$demo_op_key)"
		exit 0
	fi
	sleep 1
done
echo "start-demo-loupe: Loupe did not become healthy on :7778 (see loupe-demo.log)" >&2
exit 1
