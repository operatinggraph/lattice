#!/usr/bin/env bash
# demo-bootstrap.sh <demo-host> — one-time (idempotent) setup of a hosted-demo
# box: Ubuntu 24.04, run as root from this directory. Installs Docker + Go +
# Caddy + tools, binds the stack's published ports to loopback (env.demo →
# .env), installs the Caddyfile for <demo-host>, brings the demo up, and
# enables the systemd boot service + nightly reset timer. Re-run after a
# `git pull` to apply updates.
set -euo pipefail

DEMO_HOST="${1:?usage: demo-bootstrap.sh <demo-host> [loupe-demo-host]}"
DEMO_LOUPE_HOST="${2:-}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"

# go.mod's toolchain line is the source of truth for the Go version.
GO_VERSION="$(sed -n 's/^go //p' "$REPO_ROOT/go.mod")"
ARCH="$(dpkg --print-architecture)"

echo "==> Installing base packages..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -qy --no-install-recommends \
	git make jq curl ca-certificates gnupg docker.io docker-compose-v2

if ! "/usr/local/go/bin/go" version 2>/dev/null | grep -q "go${GO_VERSION}"; then
	echo "==> Installing Go ${GO_VERSION} (${ARCH})..."
	curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tgz
	rm -rf /usr/local/go
	tar -C /usr/local -xzf /tmp/go.tgz
	rm -f /tmp/go.tgz
fi

if ! command -v caddy >/dev/null 2>&1; then
	echo "==> Installing Caddy (official apt repo)..."
	curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' |
		gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
	curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
		>/etc/apt/sources.list.d/caddy-stable.list
	apt-get update -q
	apt-get install -qy caddy
fi

echo "==> Binding stack ports to loopback (.env)..."
cp "$HERE/env.demo" "$REPO_ROOT/.env"

echo "==> Installing Caddyfile for ${DEMO_HOST}..."
sed "s/{\$DEMO_HOST}/${DEMO_HOST}/" "$HERE/Caddyfile" >/etc/caddy/Caddyfile
# The optional second host serves the read-only demo Loupe (F20). Its marker
# file is what demo-up.sh keys the whole Loupe-demo path off; removing the
# argument removes the marker, the vhost and (on the next demo-up) the process.
if [[ -n "$DEMO_LOUPE_HOST" ]]; then
	echo "==> Adding demo-Loupe vhost for ${DEMO_LOUPE_HOST}..."
	cat >>/etc/caddy/Caddyfile <<CADDY

${DEMO_LOUPE_HOST} {
	encode zstd gzip
	# /api/events/stream is SSE — stream unbuffered.
	reverse_proxy 127.0.0.1:7778 {
		flush_interval -1
	}
}
CADDY
	printf '%s\n' "$DEMO_LOUPE_HOST" >"$REPO_ROOT/demo-loupe-host"
else
	rm -f "$REPO_ROOT/demo-loupe-host"
fi
systemctl enable --now docker
systemctl enable caddy
systemctl reload caddy 2>/dev/null || systemctl restart caddy

echo "==> Bringing the demo up..."
"$HERE/demo-up.sh"

echo "==> Installing systemd units..."
for unit in lattice-demo.service lattice-demo-reset.service lattice-demo-reset.timer; do
	sed "s|__REPO__|${REPO_ROOT}|g" "$HERE/systemd/$unit" >"/etc/systemd/system/$unit"
done
systemctl daemon-reload
systemctl enable lattice-demo.service
systemctl enable --now lattice-demo-reset.timer

echo "==> Done. Demo: https://${DEMO_HOST}/login · nightly reset: lattice-demo-reset.timer (09:10 UTC)"
