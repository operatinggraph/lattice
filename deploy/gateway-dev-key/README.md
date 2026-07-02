# Gateway dev signing key — DEV/CI ONLY

`dev-private.pem` / `dev-public.pem` is an RSA keypair checked into the repo, like
`deploy/nkeys/*.nk` (committed dev credentials, same posture as `POSTGRES_PASSWORD: lattice_dev`).

- The Gateway loads `dev-public.pem` (kid `"dev"`) **only** when started with `GATEWAY_DEV_MODE=true`
  (see `cmd/gateway/main.go`). A production deployment never sets that flag, so this key never loads
  there — see the fail-closed gate in `docs/components/gateway.md`.
- `bin/gateway dev-token -sub <identityNanoID>` signs a token with `dev-private.pem` for local
  testing against a `GATEWAY_DEV_MODE=true` Gateway.
- **Never use this keypair for anything beyond local dev / CI.** It is not a secret in the
  confidentiality sense (it signs *test* tokens only) — the sensitive property is that a production
  Gateway must never be configured to trust it.

Regenerate with: `openssl genrsa -out dev-private.pem 2048 && openssl rsa -in dev-private.pem -pubout -out dev-public.pem`.
