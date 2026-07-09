# Lattice Phase 1 — Story 1.3 Dev Harness
# Requires: Docker, Docker Compose, Go 1.26.1+
#
# Quick reference:
#   make up              — start the kernel (NATS + Postgres, bootstrap, refractor, processor) under real capability auth
#   make up-full         — full stack on latest: kernel + orchestration tier + core packages + Loupe
#   make up-full-capability — up-full + a dev-seeded staff identity (capability is now the default; kept for the proving-lane seed)
#   make install-loftspace — add the LoftSpace lease-app vertical onto a running up-full
#   make refresh-clinic  — dev-loop: diff-apply edited clinic packages + restart clinic-app (no teardown)
#   make reinstall-package PKG=packages/<dir> — diff-apply one edited package in place
#   make run-loupe       — build + run Loupe (view/control UI) on http://127.0.0.1:7777
#   make down            — tear down everything cleanly
#   make verify-bootstrap — assert primordial state; exit 0 on success
#   make build           — compile all binaries
#   make vet             — run go vet ./...

SHELL := /bin/bash
NATS_URL ?= nats://localhost:4222
BOOTSTRAP_JSON ?= $(abspath ./lattice.bootstrap.json)
# Persists the standing consoleOperator identity dev-seed-console-operator
# provisions (loupe-operator-auth-lift-design.md mechanism B), mirroring
# BOOTSTRAP_JSON's shape: generated once per deployment, gitignored, read by
# up-full to set LOUPE_OPERATOR_ACTOR_KEY so a fresh Loupe restart keeps using
# the scoped operator instead of silently falling back to root.
LOUPE_OPERATOR_JSON ?= $(abspath ./loupe-operator.json)
# The loftspace-app read-boundary DSN (D1.3): a NON-superuser, SELECT-only role so
# Postgres RLS is enforced (the lattice superuser would bypass it). See
# provision-loftspace-role.
LOFTSPACE_APP_PG_DSN ?= postgres://loftspace_app:loftspace_app_dev@localhost:5432/lattice?sslmode=disable
# The clinic-app read-boundary DSN (D1.5), same NON-superuser SELECT-only posture
# as LOFTSPACE_APP_PG_DSN. See provision-clinic-role.
CLINIC_APP_PG_DSN ?= postgres://clinic_app:clinic_app_dev@localhost:5432/lattice?sslmode=disable
# Loupe's lens-contents read seam (loupe-2-ux-design.md §6.4/F9): a NON-superuser,
# SELECT-only role's DSN — same posture as LOFTSPACE_APP_PG_DSN/CLINIC_APP_PG_DSN/
# GATEWAY_PG_DSN, deliberately NOT BYPASSRLS. Andrew's ratified M5 decision
# (read-path-authorization-d1-design.md §8: "wildcard grant, not bypass") is that
# Loupe's all-access reads stay INSIDE the RLS boundary via a wildcard
# actor_read_grants row, so even admin reads pass through RLS and remain
# attributable/loggable. See provision-loupe-role.
LOUPE_PG_DSN ?= postgres://loupe_pg:loupe_pg_dev@localhost:5432/lattice?sslmode=disable
# The Gateway read-path front's DSN (gateway-external-trust-boundary-design.md
# Fire 3), same NON-superuser SELECT-only posture as LOFTSPACE_APP_PG_DSN /
# CLINIC_APP_PG_DSN. See provision-gateway-role.
GATEWAY_PG_DSN ?= postgres://gateway:gateway_dev@localhost:5432/lattice?sslmode=disable
# Directory of <name>.sql read-model files (Fire 3); each becomes a
# GET /v1/<name> route. See internal/gateway/read.go / cmd/gateway's
# GATEWAY_READ_MODELS_DIR doc comment.
GATEWAY_READ_MODELS_DIR ?= $(abspath ./deploy/gateway-read-models)
# Origins allowed to call POST /v1/operations cross-origin — the vertical apps'
# own dev ports, since the browser-direct write path (real-actor-write-auth-e2e-
# design.md §3.1) has the FE call the Gateway directly from its own origin.
GATEWAY_CORS_ORIGINS ?= http://localhost:7788,http://localhost:7799,http://localhost:7801

# Per-component dev NKey seeds (NATS account-level write restriction, Path A —
# deploy/nats-server.conf's permission matrix). Each binary's NATS_NKEY points at
# its own seed so the auth-enabled dev stack authenticates as the right user
# (only processor may write core-kv; only refractor may write capability-kv /
# lens targets — see the design doc). Dev-only seeds, committed like
# POSTGRES_PASSWORD: lattice_dev; empty NATS_NKEY on any binary falls back to
# anonymous, which the server now rejects — every launch site below sets one.
NKEY_DIR ?= $(abspath ./deploy/nkeys)
NKEY_BOOTSTRAP ?= $(NKEY_DIR)/bootstrap.nk
NKEY_PROCESSOR ?= $(NKEY_DIR)/processor.nk
NKEY_REFRACTOR ?= $(NKEY_DIR)/refractor.nk
NKEY_LOOM ?= $(NKEY_DIR)/loom.nk
NKEY_WEAVER ?= $(NKEY_DIR)/weaver.nk
NKEY_BRIDGE ?= $(NKEY_DIR)/bridge.nk
NKEY_OBJMGR ?= $(NKEY_DIR)/object-store-manager.nk
NKEY_LOUPE ?= $(NKEY_DIR)/loupe.nk
NKEY_LOFTSPACE_APP ?= $(NKEY_DIR)/loftspace-app.nk
NKEY_CLINIC_APP ?= $(NKEY_DIR)/clinic-app.nk
NKEY_CAFE_APP ?= $(NKEY_DIR)/cafe-app.nk
NKEY_WELLNESS_APP ?= $(NKEY_DIR)/wellness-app.nk
NKEY_LATTICE_PKG ?= $(NKEY_DIR)/lattice-pkg.nk
NKEY_LATTICE_CLI ?= $(NKEY_DIR)/lattice.nk
NKEY_GATEWAY ?= $(NKEY_DIR)/gateway.nk
NKEY_CHRONICLER ?= $(NKEY_DIR)/chronicler.nk

# VAULT_KEK_FILE — the Processor's sensitive-aspect crypto master KEK
# (Contract #3 §3.10, internal/vault). UNLIKE the nkey seeds above (transport
# auth, low-value if leaked), this key can decrypt every PII aspect ever
# written, so it is generated locally on first `make up` / `run-processor`
# (see provision-vault-kek below) and gitignored — never committed.
VAULT_KEK_FILE ?= $(abspath ./deploy/vault/master.kek)

# LATTICE_PROCESSOR_AUTH_MODE — the `make up` background Processor's auth mode.
# Capability is the ONLY operational mode: the platform runs under the real
# CapabilityAuthorizer whether or not packages are installed yet (class-aware
# routing degrades gracefully pre-rbac — see cmd/processor/main.go). `stub`
# (allow-all) is retired as a deployable posture — the deployed binaries refuse
# to start in it (see the mains); it survives only as internal test scaffolding.
LATTICE_PROCESSOR_AUTH_MODE ?= capability

# Load .env if it exists (ignored by git).
-include .env

.PHONY: up up-full up-full-capability dev-seed-staff provision-gateway-identity-provisioner test-real-actor-auth up-loftspace orchestration install-packages install-loftspace run-loupe run-gateway run-loftspace-app down verify-kernel verify-package-rbac verify-package-identity verify-package-identity-hygiene verify-package-objects-base verify-package-location-domain verify-package-loftspace-domain verify-package-clinic-domain verify-package-clinic-reminders up-clinic install-clinic refresh-clinic refresh-loftspace provision-loftspace-role provision-clinic-role provision-gateway-role provision-readpath provision-vault-kek reinstall-package verify-package-service-location verify-package-augur verify-conformance build vet lint-conventions lint-board install-skills test test-rollback test-lease-convergence test-object-gc test-crypto-shred test-system-actor-capability test-control-plane-authz test-augur-convergence test-unrouted-convergence test-cli test-hello-lattice test-health-completeness processor run-processor clean logs ps

## up — Bring up NATS + Postgres, run bootstrap binary, block until readiness gate.
## Detects an already-healthy kernel first and reuses it — invoking this against a
## stack that's already serving other work used to unconditionally kill and
## restart the live processor/refractor out from under it (and, pre-Compose-
## project-pin, mint a colliding second stack from a different worktree).
## The reuse branch also verifies $(BOOTSTRAP_JSON) still matches live Core KV
## (`lattice bootstrap verify`) before trusting it — a stack whose containers
## were recreated out-of-band (bypassing `make down`) looks process-healthy but
## seeds against a stale primordial ID set, so reads silently return empty
## while writes still succeed. A mismatch forces the fresh re-bootstrap below.
up:
	@PROC_HEALTHY=0; \
	if docker compose ps --status running --services 2>/dev/null | grep -qx nats && \
	    docker compose ps --status running --services 2>/dev/null | grep -qx postgres && \
	    pgrep -x processor >/dev/null 2>&1 && pgrep -x refractor >/dev/null 2>&1; then \
		PROC_HEALTHY=1; \
	fi; \
	FRESH=0; \
	if [ "$$PROC_HEALTHY" = "1" ] && [ -f "$(BOOTSTRAP_JSON)" ]; then \
		go build -o bin/lattice ./cmd/lattice 2>/dev/null; \
		if NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice bootstrap verify >/dev/null 2>&1; then \
			FRESH=1; \
		fi; \
	fi; \
	if [ "$$FRESH" = "1" ]; then \
		echo "==> Kernel already up (NATS + Postgres healthy, processor + refractor running, bootstrap fresh) — reusing. For a clean rebuild, run 'make down' first."; \
	else \
		if [ "$$PROC_HEALTHY" = "1" ]; then \
			echo "==> Kernel processes look up but $(BOOTSTRAP_JSON) is stale/mismatched against Core KV (reads would silently return empty) — forcing a fresh re-bootstrap."; \
			rm -f $(BOOTSTRAP_JSON); \
		fi; \
		set -e; \
		echo "==> Starting NATS + Postgres..."; \
		docker compose up -d --wait; \
		echo "==> Containers healthy."; \
		echo "==> Killing any background refractor processes (avoid warm-up duplicates)..."; \
		pkill -x refractor 2>/dev/null || true; \
		echo "==> Killing any background processor processes (avoid warm-up duplicates)..."; \
		pkill -x processor 2>/dev/null || true; \
		echo "==> Building bootstrap binary..."; \
		go build -o bin/bootstrap ./cmd/bootstrap; \
		echo "==> Building refractor binary (Story 2.1)..."; \
		go build -o bin/refractor ./cmd/refractor; \
		echo "==> Building lattice CLI..."; \
		go build -o bin/lattice ./cmd/lattice; \
		echo "==> Running bootstrap (seed pass — readiness gate deferred until Refractor is up)..."; \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_BOOTSTRAP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bootstrap -skip-ready-wait; \
		$(MAKE) provision-vault-kek; \
		echo "==> Starting refractor in background..."; \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_REFRACTOR) REFRACTOR_PG_DSN="postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable" LATTICE_VAULT_MASTER_KEK_FILE=$(VAULT_KEK_FILE) ./bin/refractor >refractor.log 2>&1 </dev/null & \
		echo "==> Running bootstrap (readiness gate — blocks until admin + Loom + Weaver + Bridge + objmgr + privacy cap.* projections land)..."; \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_BOOTSTRAP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bootstrap; \
		echo "==> Building processor binary..."; \
		go build -o bin/processor ./cmd/processor; \
		$(MAKE) provision-vault-kek; \
		echo "==> Starting processor in background..."; \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_PROCESSOR) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) PROCESSOR_FILTER=ops.default,ops.urgent,ops.system,ops.meta LATTICE_AUTH_MODE=$(LATTICE_PROCESSOR_AUTH_MODE) LATTICE_VAULT_MASTER_KEK_FILE=$(VAULT_KEK_FILE) ./bin/processor >processor.log 2>&1 </dev/null & \
		echo "==> Lattice ready."; \
	fi

## down — Tear down all containers and remove the bootstrap JSON.
## Volumes are ephemeral (not named), so container removal clears NATS + Postgres data.
down:
	@echo "==> Stopping and removing containers..."
	docker compose down --remove-orphans
	@echo "==> Removing bootstrap JSON (if present)..."
	rm -f $(BOOTSTRAP_JSON)
	@echo "==> Killing any background refractor processes..."
	-pkill -f "bin/refractor" 2>/dev/null || true
	@echo "==> Killing any background processor processes..."
	-pkill -f "bin/processor" 2>/dev/null || true
	@echo "==> Killing any background orchestration / Loupe processes..."
	-pkill -f "bin/loom" 2>/dev/null || true
	-pkill -f "bin/weaver" 2>/dev/null || true
	-pkill -f "bin/bridge" 2>/dev/null || true
	-pkill -f "bin/object-store-manager" 2>/dev/null || true
	-pkill -f "bin/chronicler" 2>/dev/null || true
	-pkill -f "bin/loupe" 2>/dev/null || true
	-pkill -f "bin/loftspace-app" 2>/dev/null || true
	-pkill -f "bin/clinic-app" 2>/dev/null || true
	@echo "==> Down complete."

## verify-kernel — Assert post-Story-4.7 kernel keys exist with correct envelopes.
## Expected count ≈ 91 OK lines (30 top-level keys + aspects + streams/buckets).
verify-kernel:
	@echo "==> Running kernel verification..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) go run ./scripts/verify-kernel.go

## verify-package-rbac — Install rbac-domain package and assert its KV state.
verify-package-rbac:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing rbac-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/rbac-domain
	@echo "==> Running rbac-domain package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-rbac.go

## verify-package-identity — Install identity-domain package and assert its KV state.
verify-package-identity:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing identity-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-domain
	@echo "==> Running identity-domain package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-identity.go

## verify-package-identity-hygiene — Install identity-hygiene and assert its KV state.
verify-package-identity-hygiene:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing identity-hygiene..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-hygiene
	@echo "==> Running identity-hygiene package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-identity-hygiene.go

## verify-package-objects-base — Install objects-base and assert its KV state.
verify-package-objects-base:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing objects-base..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/objects-base
	@echo "==> Running objects-base package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-objects-base.go

## verify-package-location-domain — Install location-domain and assert its KV state.
verify-package-location-domain:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing location-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	@echo "==> Running location-domain package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-location-domain.go

## verify-package-loftspace-domain — Install location-domain + loftspace-domain
## (in dependency order) and assert loftspace-domain's KV state.
verify-package-loftspace-domain:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing location-domain (dependency)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	@echo "==> Installing loftspace-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/loftspace-domain
	@echo "==> Running loftspace-domain package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-loftspace-domain.go

## verify-package-clinic-domain — Install clinic-domain (self-contained) and
## assert its KV state.
verify-package-clinic-domain:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing clinic-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-domain
	@echo "==> Running clinic-domain package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-clinic-domain.go

## verify-package-clinic-reminders — Co-install the clinic vertical (orchestration-
## base → clinic-domain → clinic-reminders) and assert clinic-reminders' KV state
## (the 2 DDLs, the appointmentReminders lens, the weaverTarget playbook, the grant).
verify-package-clinic-reminders:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base + clinic-domain + clinic-reminders..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-reminders
	@echo "==> Running clinic-reminders package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-clinic-reminders.go

## verify-package-service-location — Co-install service-location with its
## dependencies (location-domain + service-domain, plus the deps those need:
## rbac-domain for the operator role, identity-domain + orchestration-base for
## service-domain) in dependency order, then assert service-location's KV state.
verify-package-service-location:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing dependency chain (rbac-domain, identity-domain, orchestration-base, location-domain, service-domain)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/rbac-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-domain
	@echo "==> Installing service-location..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-location
	@echo "==> Running service-location package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-service-location.go

## verify-package-augur — Co-install orchestration-base → augur (the opt-in AI
## reasoning tier; NOT primordial — matches its non-primordial dependency) and
## assert augur's KV state (the augurproposal DDL with its 2 ops, the 2 operator
## grants, the package manifest).
verify-package-augur:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base + augur..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/augur
	@echo "==> Running augur package assertions..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-augur.go

## verify-conformance — Run the contract-conformance freeze suite: the frozen
## OperationReply / envelope / contextHint shapes, Core KV key shapes, the DDL
## aspect set, the closed `response` script-return schema, and the in-code
## reply-constraint enforcement proof (a non-primaryKey response key or a
## primaryKey not in the committed mutation set is rejected fail-closed).
## Self-contained: uses embedded NATS, no Docker stack required.
verify-conformance:
	@echo "==> go test ./internal/processor -run TestConformance"
	go test ./internal/processor -run TestConformance -count=1

## build — Compile all binaries under cmd/.
build:
	@echo "==> Building all binaries..."
	go build ./...
	mkdir -p bin
	go build -o bin/bootstrap ./cmd/bootstrap
	go build -o bin/refractor ./cmd/refractor
	go build -o bin/processor ./cmd/processor
	go build -o bin/lattice ./cmd/lattice
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	go build -o bin/loom ./cmd/loom
	go build -o bin/weaver ./cmd/weaver
	go build -o bin/bridge ./cmd/bridge
	go build -o bin/object-store-manager ./cmd/object-store-manager
	go build -o bin/loupe ./cmd/loupe
	go build -o bin/gateway ./cmd/gateway
	go build -o bin/chronicler ./cmd/chronicler

## test-cli — Run the lattice CLI unit + E2E tests.
test-cli:
	go test ./cmd/lattice/... -v -p 1 -count=1

## processor — Build the Processor binary (Story 1.5).
processor:
	@echo "==> Building processor binary..."
	mkdir -p bin
	go build -o bin/processor ./cmd/processor

## run-processor — Run the Processor against the local make-up harness.
## Requires `make up` to have completed (NATS reachable, core-operations stream live).
run-processor: processor provision-vault-kek
	@echo "==> Starting processor (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_PROCESSOR) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) LATTICE_VAULT_MASTER_KEK_FILE=$(VAULT_KEK_FILE) ./bin/processor

## provision-vault-kek — Generate the local Vault master KEK (Contract #3
## §3.10, internal/vault) on first use. Idempotent: a no-op once the file
## exists. NEVER commit this file — it can decrypt every sensitive aspect
## ever written; deploy/vault/ is gitignored.
provision-vault-kek:
	@mkdir -p $(dir $(VAULT_KEK_FILE))
	@if [ ! -f $(VAULT_KEK_FILE) ]; then \
		echo "==> Generating local Vault master KEK at $(VAULT_KEK_FILE)..."; \
		(umask 077; openssl rand -base64 32 > $(VAULT_KEK_FILE)); \
	fi
	@chmod 600 $(VAULT_KEK_FILE)

## up-full — Full local deployment on latest source: kernel (make up) +
## orchestration tier (Loom/Weaver/Bridge/object-store-manager/Chronicler) + core packages
## + Gateway (:8080, dev-mode) + Loupe, all in the background. When it returns,
## open http://127.0.0.1:7777.
## For a clean rebuild from scratch, run `make down` first.
up-full:
	@$(MAKE) up
	@$(MAKE) orchestration
	@$(MAKE) install-packages
	@$(MAKE) provision-readpath
	@$(MAKE) provision-gateway-role
	@echo "==> Building gateway binary..."
	go build -o bin/gateway ./cmd/gateway
	@echo "==> Killing any prior Gateway process..."
	-pkill -f "bin/gateway" 2>/dev/null || true
	@echo "==> Starting Gateway (:8080, dev-mode) in background..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_GATEWAY) GATEWAY_DEV_MODE=true GATEWAY_PG_DSN=$(GATEWAY_PG_DSN) GATEWAY_READ_MODELS_DIR=$(GATEWAY_READ_MODELS_DIR) GATEWAY_CORS_ORIGINS=$(GATEWAY_CORS_ORIGINS) ./bin/gateway >gateway.log 2>&1 </dev/null &
	@$(MAKE) provision-loupe-role
	@$(MAKE) dev-seed-console-operator
	@echo "==> Building loupe binary..."
	go build -o bin/loupe ./cmd/loupe
	@echo "==> Killing any prior Loupe process..."
	-pkill -f "bin/loupe" 2>/dev/null || true
	@echo "==> Starting Loupe in background (operator identity per mechanism B: scoped consoleOperator by default, never root)..."
	@LOUPE_OP_KEY=$$(jq -r '.operatorActorKey // empty' $(LOUPE_OPERATOR_JSON) 2>/dev/null); \
	 NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LOUPE) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) LOUPE_PG_DSN=$(LOUPE_PG_DSN) LOUPE_OPERATOR_ACTOR_KEY="$$LOUPE_OP_KEY" LOUPE_DEV_AUTH=1 ./bin/loupe >loupe.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Full Lattice ready. Loupe http://127.0.0.1:7777 · Gateway :8080 (dev-mode)."
	@echo "==> Logs: loupe.log gateway.log loom.log weaver.log bridge.log objmgr.log chronicler.log refractor.log processor.log"

## up-full-capability — up-full, then the Processor under REAL CapabilityAuthorizer
## auth: the real-actor-write-auth-e2e proving lane (design §4 Phase 1
## item 3). `make up`'s reuse-detection only checks liveness, not auth mode, so a
## stub Processor left running by a prior `make up`/`up-full` would otherwise go
## unnoticed under this target — so this restarts ONLY the Processor, standalone,
## under LATTICE_AUTH_MODE=capability; NATS/Postgres/refractor/orchestration/
## packages/Gateway/Loupe are all reused as-is via up-full. Also dev-seeds ONE
## staff identity holding `operator` (design §3.3) so a real staff actor exists for
## the allow side of the proof — a real consumer instead comes from the Gateway's
## ProvisionConsumerIdentity pre-flight on first authenticated touch (no seed
## needed). up-full already runs the Processor under capability (the only
## operational mode now), so the restart below is a mode-preserving refresh.
up-full-capability:
	@$(MAKE) up-full
	@echo "==> Refreshing the Processor under LATTICE_AUTH_MODE=capability (real-actor-write-auth-e2e proving lane)..."
	-pkill -f "bin/processor" 2>/dev/null || true
	@sleep 1
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_PROCESSOR) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) PROCESSOR_FILTER=ops.default,ops.urgent,ops.system,ops.meta LATTICE_AUTH_MODE=capability LATTICE_VAULT_MASTER_KEK_FILE=$(VAULT_KEK_FILE) ./bin/processor >processor.log 2>&1 </dev/null &
	@sleep 1
	@$(MAKE) dev-seed-staff
	@$(MAKE) provision-gateway-identity-provisioner
	@echo "==> up-full-capability ready: Processor running under the REAL CapabilityAuthorizer. Loupe http://127.0.0.1:7777 · Gateway :8080 (dev-mode)."

## dev-seed-staff — Dev-seed ONE staff identity holding `operator`, for the
## real-actor-write-auth-e2e proving lane (design §3.3). Submits
## CreateUnclaimedIdentity as the bootstrap admin actor (already operator via the
## primordial holdsRole seed), then AssignRole to grant `operator` — the same
## kernel role every internal service actor holds; today every loftspace/clinic op
## is operator-only (design §3.4), so this is the one role that lets a real staff
## actor exercise the allow side of the proof. Reads roleOperator/bootstrapIdentity
## straight out of $(BOOTSTRAP_JSON) (per-deployment, not hard-coded — see
## internal/bootstrap/nanoid.go). Not idempotent across repeat runs (mints a fresh
## identity each time, no dedup key) — fine for the dev proving lane.
dev-seed-staff:
	@go build -o bin/lattice ./cmd/lattice
	@ADMIN_ID=$$(jq -r '.primordialIDs.bootstrapIdentity' $(BOOTSTRAP_JSON)); \
	 ROLE_ID=$$(jq -r '.primordialIDs.roleOperator' $(BOOTSTRAP_JSON)); \
	 ADMIN_KEY="vtx.identity.$$ADMIN_ID"; \
	 ROLE_KEY="vtx.role.$$ROLE_ID"; \
	 echo "==> Creating dev staff identity (actor=$$ADMIN_KEY)..."; \
	 CREATE_OUT=$$(NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) ./bin/lattice identity create-unclaimed \
		--actor "$$ADMIN_KEY" --output json \
		--payload '{"name":"Dev Staff","email":"staff@dev.lattice.local"}'); \
	 echo "$$CREATE_OUT"; \
	 STAFF_KEY=$$(echo "$$CREATE_OUT" | jq -r '.data.primaryKey'); \
	 if [ -z "$$STAFF_KEY" ] || [ "$$STAFF_KEY" = "null" ]; then \
		echo "==> ERROR: could not determine staff identity key from create-unclaimed output"; exit 1; \
	 fi; \
	 STAFF_ID=$${STAFF_KEY#vtx.identity.}; \
	 echo "==> Assigning operator role to $$STAFF_KEY..."; \
	 NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) ./bin/lattice op submit \
		--operation-type AssignRole --actor "$$ADMIN_KEY" --output json \
		--payload "{\"actorKey\":\"$$STAFF_KEY\",\"roleKey\":\"$$ROLE_KEY\"}" \
		--context-hint-reads "$$STAFF_KEY,$$ROLE_KEY"; \
	 echo "==> Dev staff identity ready: $$STAFF_KEY holds operator. Mint a token: ./bin/gateway dev-token -sub $$STAFF_ID"

## dev-seed-console-operator — Provisions THE standing consoleOperator
## identity (packages/console-operator), NOT the kernel-primordial `operator`
## role dev-seed-staff grants — loupe-operator-auth-lift-design.md mechanism
## B: Loupe's real, non-root operator identity, root reserved for the rare,
## explicit pkg-lifecycle deploy action (§4: "used explicitly and rarely, not
## routine console use"). IDEMPOTENT (unlike dev-seed-staff): persists the
## identity's key to LOUPE_OPERATOR_JSON on first run and reuses it on every
## later run/restart, so up-full's Loupe launch (below) can pick it up as the
## actual default rather than a one-off test fixture. Requires
## `make install-packages` (console-operator, version >= 0.2.0 for its
## read-grant lens) already run.
dev-seed-console-operator:
	@if [ -f $(LOUPE_OPERATOR_JSON) ]; then \
		OP_KEY=$$(jq -r '.operatorActorKey' $(LOUPE_OPERATOR_JSON)); \
		echo "==> Console-operator identity already provisioned: $$OP_KEY ($(LOUPE_OPERATOR_JSON) exists)"; \
	 else \
		$(MAKE) --no-print-directory _dev-seed-console-operator-create; \
	 fi

# _dev-seed-console-operator-create is dev-seed-console-operator's creation
# path, split out so the idempotency check above is a single shell's if/else
# (a bare `exit 0` inside one `@`-prefixed recipe line only exits THAT line's
# subshell in Make, not the rest of the recipe — this split is what actually
# makes the skip work, not the exit code).
_dev-seed-console-operator-create:
	@go build -o bin/lattice ./cmd/lattice
	@ADMIN_ID=$$(jq -r '.primordialIDs.bootstrapIdentity' $(BOOTSTRAP_JSON)); \
	 ADMIN_KEY="vtx.identity.$$ADMIN_ID"; \
	 ROLE_KEY=$$(go run ./scripts/print-role-id.go console-operator consoleOperator); \
	 echo "==> Creating dev console-operator identity (actor=$$ADMIN_KEY)..."; \
	 CREATE_OUT=$$(NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) ./bin/lattice identity create-unclaimed \
		--actor "$$ADMIN_KEY" --output json \
		--payload '{"name":"Dev Console Operator","email":"console-operator@dev.lattice.local"}'); \
	 echo "$$CREATE_OUT"; \
	 OP_KEY=$$(echo "$$CREATE_OUT" | jq -r '.data.primaryKey'); \
	 if [ -z "$$OP_KEY" ] || [ "$$OP_KEY" = "null" ]; then \
		echo "==> ERROR: could not determine identity key from create-unclaimed output"; exit 1; \
	 fi; \
	 OP_ID=$${OP_KEY#vtx.identity.}; \
	 echo "==> Assigning consoleOperator ($$ROLE_KEY) to $$OP_KEY..."; \
	 NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) ./bin/lattice op submit \
		--operation-type AssignRole --actor "$$ADMIN_KEY" --output json \
		--payload "{\"actorKey\":\"$$OP_KEY\",\"roleKey\":\"$$ROLE_KEY\"}" \
		--context-hint-reads "$$OP_KEY,$$ROLE_KEY"; \
	 echo "{\"operatorActorKey\":\"$$OP_KEY\"}" > $(LOUPE_OPERATOR_JSON); \
	 echo "==> Dev console-operator identity ready: $$OP_KEY holds consoleOperator (NOT root)."; \
	 echo "==> Persisted to $(LOUPE_OPERATOR_JSON) — up-full will use it automatically from now on."; \
	 echo "==> Mint a login token: ./bin/gateway dev-token -sub $$OP_ID"

## provision-gateway-identity-provisioner — Grant the Gateway's own system
## identity the `identityProvisioner` role (gateway-claim-flow-identity-provisioning-design.md
## §3.3/§4): the one-time, documented ops action ProvisionConsumerIdentity
## needs before a first-touch consumer can auto-provision — before this runs,
## the Gateway's pre-flight submits ProvisionConsumerIdentity under its own
## identity and is denied (`no matching platformPermission`), tolerated as a
## best-effort no-op (gateway.go's provisionActorIfNeeded), so the symptom is
## silent: the consumer identity just never appears. Idempotent (AssignRole
## on an already-held role is a no-op grant link create-or-noop, not an error).
provision-gateway-identity-provisioner:
	@go build -o bin/lattice ./cmd/lattice
	@ADMIN_ID=$$(jq -r '.primordialIDs.bootstrapIdentity' $(BOOTSTRAP_JSON)); \
	 GATEWAY_ID=$$(jq -r '.primordialIDs.gatewayIdentity' $(BOOTSTRAP_JSON)); \
	 ADMIN_KEY="vtx.identity.$$ADMIN_ID"; \
	 GATEWAY_KEY="vtx.identity.$$GATEWAY_ID"; \
	 ROLE_KEY=$$(go run ./scripts/print-role-id.go identity-domain identityProvisioner); \
	 echo "==> Assigning identityProvisioner ($$ROLE_KEY) to the Gateway's system identity ($$GATEWAY_KEY)..."; \
	 NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) ./bin/lattice op submit \
		--operation-type AssignRole --actor "$$ADMIN_KEY" --output json \
		--payload "{\"actorKey\":\"$$GATEWAY_KEY\",\"roleKey\":\"$$ROLE_KEY\"}" \
		--context-hint-reads "$$GATEWAY_KEY,$$ROLE_KEY"; \
	 echo "==> Gateway identity provisioning ready: first-touch ProvisionConsumerIdentity can now succeed."

## test-real-actor-auth — real-actor-write-auth-e2e design §4 Phase 1 item 4,
## the core proof. Requires `make up-full-capability` (Processor under
## LATTICE_AUTH_MODE=capability) and `make install-loftspace` (CreateLocation /
## SetListingStatus / CreateLeaseApplication need the LoftSpace vertical
## installed) already run against the shared stack. Not self-contained (like
## verify-package-*, it targets the shared stack's NATS_URL/Gateway, not an
## embedded fixture).
test-real-actor-auth:
	@echo "==> Running the real-actor-write-auth e2e proof..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-real-actor-write-auth.go

## test-loupe-operator-tier — loupe-operator-auth-lift-design.md §7 item 6 +
## scoped-privileged-lane-grants-design.md §7 item 3, the operator-tier analog
## of test-real-actor-auth. Requires `make up-full-capability` already running
## (real Processor under LATTICE_AUTH_MODE=capability, real Gateway). Proves a
## consoleOperator can RevokeActor (default-lane) and InstallPackage@meta
## (allowlisted pkg-lifecycle trio, mechanism C) but is denied
## InstallPackage@default (LaneUnauthorized) and CreateMetaVertex (ungranted).
test-loupe-operator-tier:
	@echo "==> Running the Loupe operator-tier e2e proof..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-loupe-operator-tier.go

## up-loftspace — Full stack + the LoftSpace vertical + the applicant app on :7788.
## Runs up-full, installs the LoftSpace vertical (orchestration-base → location-domain
## → loftspace-domain → service-domain → lease-signing), and starts loftspace-app in
## the background alongside Loupe (:7777). The applicant app is the demand-side view;
## Loupe is the operator/inspector. Logs: loftspace-app.log (+ the up-full logs).
up-loftspace:
	@$(MAKE) up-full
	@$(MAKE) provision-loftspace-role
	@$(MAKE) install-loftspace
	@$(MAKE) provision-readpath
	@echo "==> Building loftspace-app binary..."
	go build -o bin/loftspace-app ./cmd/loftspace-app
	@echo "==> Killing any prior loftspace-app process..."
	-pkill -f "bin/loftspace-app" 2>/dev/null || true
	@echo "==> Starting loftspace-app in background (D1.3 read boundary: non-superuser SELECT-only role + dev-auth)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LOFTSPACE_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		LOFTSPACE_APP_PG_DSN="$(LOFTSPACE_APP_PG_DSN)" LOFTSPACE_APP_DEV_AUTH=1 \
		./bin/loftspace-app >loftspace-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> LoftSpace ready. Operator/inspector: http://127.0.0.1:7777 (Loupe) · applicant app: http://127.0.0.1:7788"

## provision-loftspace-role — Create the loftspace-app's Postgres read role: a
## NON-superuser, SELECT-only role (D1.3 Fire 3). The app MUST NOT read as the
## `lattice` superuser — superusers (and BYPASSRLS roles) skip RLS entirely, so the
## protected lease-applications model would leak every actor's rows. SELECT-only
## bounds the WRITE blast radius — a compromised app cannot forge a grant row or
## mutate any read model (it can still read the dev DB's lens tables). Default
## privileges FOR the lattice (Refractor) role cover tables created at later lens
## activation; the explicit grant covers tables that already exist. Idempotent.
provision-loftspace-role:
	@echo "==> Provisioning loftspace-app non-superuser SELECT-only Postgres role..."
	docker compose exec -T postgres psql -U lattice -d lattice -v ON_ERROR_STOP=1 -c "\
		DO \$$\$$ BEGIN \
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='loftspace_app') THEN \
		    CREATE ROLE loftspace_app LOGIN PASSWORD 'loftspace_app_dev' NOSUPERUSER NOCREATEDB NOCREATEROLE; \
		  END IF; \
		END \$$\$$;" \
		-c "GRANT USAGE ON SCHEMA public TO loftspace_app;" \
		-c "ALTER DEFAULT PRIVILEGES FOR ROLE lattice IN SCHEMA public GRANT SELECT ON TABLES TO loftspace_app;" \
		-c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO loftspace_app;"

## provision-readpath — Provision the read-path authorization Postgres tables
## OUT-OF-BAND for the dev stack: the shared actor_read_grants grant table +
## every installed protected read-model table (with FORCE ROW LEVEL SECURITY +
## the §6.14 set-membership policy). Refractor no longer issues this DDL at lens
## activation — it VERIFIES the RLS posture and pauses the lens fail-closed
## (Contract #6 §6.14, verify-and-pause) — so the dev stack provisions it here,
## the exact DDL `lattice lens emit-ddl` prints for an operator to run against a
## real read-model DB. The DDL is read from the installed lens specs in Core KV
## (grant table first; protected tables next), so run this AFTER `make up` /
## install-* so the specs exist. Idempotent (CREATE TABLE IF NOT EXISTS /
## DROP-then-CREATE POLICY); a no-op when no protected/grant lens is installed.
provision-readpath:
	@echo "==> Building lattice CLI..."
	@go build -o bin/lattice ./cmd/lattice
	@echo "==> Provisioning read-path authorization tables out-of-band (Contract #6 §6.14)..."
	@set -o pipefail; NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) ./bin/lattice lens emit-ddl | \
		docker compose exec -T postgres psql -U lattice -d lattice -v ON_ERROR_STOP=1 -f -
	@echo "==> Read-path tables provisioned (or none installed)."

## up-clinic — One-command Clinic vertical: up-full → install-clinic → build +
## start clinic-app (:7799) in the background alongside Loupe (:7777). The clinic
## app is the demand-side patient/booking view; Loupe is the operator/inspector.
## Provisions the clinic-app D1.5 read-boundary role (mirrors up-loftspace's D1.3
## wiring) so the shipped protected reads (clinicPatientsRead / clinicAppointmentsRead
## / staff-wildcard) serve instead of 500ing "not configured".
## Logs: clinic-app.log (+ the up-full logs).
up-clinic:
	@$(MAKE) up-full
	@$(MAKE) provision-clinic-role
	@$(MAKE) install-clinic
	@$(MAKE) provision-readpath
	@echo "==> Building clinic-app binary..."
	go build -o bin/clinic-app ./cmd/clinic-app
	@echo "==> Killing any prior clinic-app process..."
	-pkill -f "bin/clinic-app" 2>/dev/null || true
	@echo "==> Starting clinic-app in background (D1.5 read boundary: non-superuser SELECT-only role + dev-auth)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_CLINIC_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		CLINIC_APP_PG_DSN="$(CLINIC_APP_PG_DSN)" CLINIC_APP_DEV_AUTH=1 \
		./bin/clinic-app >clinic-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Clinic ready. Operator/inspector: http://127.0.0.1:7777 (Loupe) · patient app: http://127.0.0.1:7799"

## up-cafe — One-command Café vertical: up-full → install-cafe → build + start
## cafe-app (:7801) in the background alongside Loupe (:7777). No protected
## Postgres read model exists for café (every lens is plain NATS-KV), so no
## provision-*-role step is needed, unlike up-loftspace/up-clinic.
## Logs: cafe-app.log (+ the up-full logs).
up-cafe:
	@$(MAKE) up-full
	@$(MAKE) install-cafe
	@echo "==> Building cafe-app binary..."
	go build -o bin/cafe-app ./cmd/cafe-app
	@echo "==> Killing any prior cafe-app process..."
	-pkill -f "bin/cafe-app" 2>/dev/null || true
	@echo "==> Starting cafe-app in background (dev-auth staff token minter)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_CAFE_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		CAFE_APP_DEV_AUTH=1 \
		./bin/cafe-app >cafe-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Café ready. Operator/inspector: http://127.0.0.1:7777 (Loupe) · café app: http://127.0.0.1:7801"

## provision-clinic-role — Create the clinic-app's Postgres read role: a
## NON-superuser, SELECT-only role (D1.5), mirroring provision-loftspace-role
## (D1.3). The app MUST NOT read as the `lattice` superuser — superusers (and
## BYPASSRLS roles) skip RLS entirely, so the protected patient/appointment read
## models would leak every actor's rows. Idempotent.
provision-clinic-role:
	@echo "==> Provisioning clinic-app non-superuser SELECT-only Postgres role..."
	docker compose exec -T postgres psql -U lattice -d lattice -v ON_ERROR_STOP=1 -c "\
		DO \$$\$$ BEGIN \
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='clinic_app') THEN \
		    CREATE ROLE clinic_app LOGIN PASSWORD 'clinic_app_dev' NOSUPERUSER NOCREATEDB NOCREATEROLE; \
		  END IF; \
		END \$$\$$;" \
		-c "GRANT USAGE ON SCHEMA public TO clinic_app;" \
		-c "ALTER DEFAULT PRIVILEGES FOR ROLE lattice IN SCHEMA public GRANT SELECT ON TABLES TO clinic_app;" \
		-c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO clinic_app;"

## provision-gateway-role — Provision the Gateway's non-superuser SELECT-only
## Postgres role for the read-path front (Fire 3), same posture as
## provision-loftspace-role / provision-clinic-role — a superuser/BYPASSRLS
## role would skip RLS and leak every actor's rows.
provision-gateway-role:
	@echo "==> Provisioning gateway non-superuser SELECT-only Postgres role..."
	docker compose exec -T postgres psql -U lattice -d lattice -v ON_ERROR_STOP=1 -c "\
		DO \$$\$$ BEGIN \
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='gateway') THEN \
		    CREATE ROLE gateway LOGIN PASSWORD 'gateway_dev' NOSUPERUSER NOCREATEDB NOCREATEROLE; \
		  END IF; \
		END \$$\$$;" \
		-c "GRANT USAGE ON SCHEMA public TO gateway;" \
		-c "ALTER DEFAULT PRIVILEGES FOR ROLE lattice IN SCHEMA public GRANT SELECT ON TABLES TO gateway;" \
		-c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO gateway;"

## provision-loupe-role — Provision Loupe's non-superuser, SELECT-only Postgres
## role for the F9 lens-contents read seam. Same posture as provision-loftspace-
## role / provision-clinic-role / provision-gateway-role — deliberately NOT
## BYPASSRLS. Andrew's ratified M5 decision (read-path-authorization-d1-design.md
## §8) is that Loupe's all-access stays INSIDE the RLS boundary via a wildcard
## actor_read_grants row, not an engine-level bypass, so even admin reads pass
## through RLS and remain attributable/loggable (an un-instrumented superuser
## read-actor is one compromise from total exposure). cmd/loupe/pg.go sets
## lattice.actor_id to s.operatorActorKey per query; packages/console-operator's
## consoleOperatorReadGrants lens grants the standing default (scoped operator,
## mechanism B) its wildcard row, and the kernel's capabilityReadWildcardGrants
## lens covers root the same way if LOUPE_OPERATOR_ACTOR_KEY is ever pointed
## back at it. Do not add BYPASSRLS here.
provision-loupe-role:
	@echo "==> Provisioning loupe non-superuser SELECT-only Postgres role..."
	docker compose exec -T postgres psql -U lattice -d lattice -v ON_ERROR_STOP=1 -c "\
		DO \$$\$$ BEGIN \
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='loupe_pg') THEN \
		    CREATE ROLE loupe_pg LOGIN PASSWORD 'loupe_pg_dev' NOSUPERUSER NOCREATEDB NOCREATEROLE; \
		  END IF; \
		END \$$\$$;" \
		-c "GRANT USAGE ON SCHEMA public TO loupe_pg;" \
		-c "ALTER DEFAULT PRIVILEGES FOR ROLE lattice IN SCHEMA public GRANT SELECT ON TABLES TO loupe_pg;" \
		-c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO loupe_pg;"

## orchestration — Build + start the orchestration tier (Loom, Weaver, Bridge,
## object-store-manager, Chronicler) in the background. Requires a running
## deployment (make up). object-store-manager needs no actor key; Chronicler
## submits no ops (P2) but still authenticates via its own NKEY (natsperm
## grants it $KV.orchestration-history.> + health-kv only); the rest load the
## admin actor from the bootstrap JSON. Logs: loom.log weaver.log bridge.log
## objmgr.log chronicler.log. Detects an already-running tier first and
## reuses it rather than killing and restarting it out from under whoever
## else is relying on it.
orchestration:
	@if pgrep -x loom >/dev/null 2>&1 && pgrep -x weaver >/dev/null 2>&1 && \
	    pgrep -x bridge >/dev/null 2>&1 && pgrep "^object-store" >/dev/null 2>&1 && \
	    pgrep -x chronicler >/dev/null 2>&1; then \
		echo "==> Orchestration tier already running (loom/weaver/bridge/objmgr/chronicler all up) — reusing."; \
	else \
		set -e; \
		echo "==> Killing any prior orchestration processes..."; \
		pkill -x loom 2>/dev/null || true; \
		pkill -x weaver 2>/dev/null || true; \
		pkill -x bridge 2>/dev/null || true; \
		pkill "^object-store" 2>/dev/null || true; \
		pkill -x chronicler 2>/dev/null || true; \
		echo "==> Building orchestration binaries..."; \
		go build -o bin/loom ./cmd/loom; \
		go build -o bin/weaver ./cmd/weaver; \
		go build -o bin/bridge ./cmd/bridge; \
		go build -o bin/object-store-manager ./cmd/object-store-manager; \
		go build -o bin/chronicler ./cmd/chronicler; \
		echo "==> Starting Loom / Weaver / Bridge / object-store-manager / Chronicler in background..."; \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LOOM) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/loom >loom.log 2>&1 </dev/null & \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_WEAVER) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/weaver >weaver.log 2>&1 </dev/null & \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_BRIDGE) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bridge >bridge.log 2>&1 </dev/null & \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_OBJMGR) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/object-store-manager >objmgr.log 2>&1 </dev/null & \
		NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_CHRONICLER) ./bin/chronicler >chronicler.log 2>&1 </dev/null & \
		echo "==> Orchestration tier started."; \
	fi

## install-packages — Install the core Capability Packages into a running
## deployment, in dependency order: rbac-domain → control-authz → privacy-base →
## identity-domain → objects-base → console-operator.
## (lattice-pkg only warns on unmet deps; ordering is the caller's responsibility.)
install-packages:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing rbac-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/rbac-domain
	@echo "==> Installing control-authz (ctrl.<component>.<verb> grants; FR30 Fire 1b)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/control-authz
	@echo "==> Installing privacy-base..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/privacy-base
	@echo "==> Installing privacy-operator-grant (operator → ShredIdentityKey; Loupe F12 crypto-shred proof)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/privacy-operator-grant
	@echo "==> Installing identity-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-domain
	@echo "==> Installing objects-base..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/objects-base
	@echo "==> Installing console-operator (scoped consoleOperator role; loupe-operator-auth-lift mechanism B)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/console-operator

## install-loftspace — Install the LoftSpace lease-application vertical onto a
## running full stack (make up-full first), in dependency order:
## orchestration-base → location-domain → loftspace-domain → service-domain →
## lease-signing. up-full ships only the
## core packages; the vertical is an opt-in so demos / the PO loop can drive the
## real lease flow without hand-installing each package.
install-loftspace:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	@echo "==> Installing location-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	@echo "==> Installing loftspace-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/loftspace-domain
	@echo "==> Installing service-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-domain
	@echo "==> Installing lease-signing..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/lease-signing
	@echo "==> Installing loftspace-ledger..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/loftspace-ledger
	@echo "==> Installing bespoke-contracts..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/bespoke-contracts
	@echo "==> LoftSpace vertical installed. Drive it via the lattice CLI or Loupe."

## install-clinic — Install the clinic vertical onto a running up-full stack, in
## dependency order: orchestration-base → clinic-domain → clinic-reminders →
## clinic-ledger. clinic-domain is the bookable domain; clinic-reminders adds
## the @at appointment-reminder orchestration (needs orchestration-base for
## MarkExpired + the Weaver tier up-full runs); clinic-ledger adds the patient
## payment ledger (depends clinic-domain). Drive it via the clinic-app, the
## lattice CLI, or Loupe.
install-clinic:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	@echo "==> Installing clinic-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-domain
	@echo "==> Installing clinic-reminders..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-reminders
	@echo "==> Installing clinic-ledger..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-ledger
	@echo "==> Clinic vertical installed (domain + reminders + ledger). Drive it via the clinic-app, the lattice CLI, or Loupe."

## install-cafe — Install the Café vertical onto a running up-full stack, in
## dependency order: orchestration-base → service-domain → lease-signing →
## cafe-ledger → cafe-domain (identity-domain is already installed by
## install-packages/up-full). Drive it via the cafe-app, the lattice CLI, or
## Loupe.
install-cafe:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	@echo "==> Installing service-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-domain
	@echo "==> Installing lease-signing..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/lease-signing
	@echo "==> Installing cafe-ledger..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/cafe-ledger
	@echo "==> Installing cafe-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/cafe-domain
	@echo "==> Café vertical installed (lease-signing + cafe-ledger + cafe-domain). Drive it via the cafe-app, the lattice CLI, or Loupe."

## install-wellness — Install the Wellness vertical onto a running up-full stack,
## in dependency order: orchestration-base → service-domain → lease-signing →
## wellness-domain (identity-domain is already installed by
## install-packages/up-full; wellness-domain's only cross-package read is
## lease-signing's leaseapp applicationFor link, by known key). Drive it via
## the wellness-app, the lattice CLI, or Loupe.
install-wellness:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	@echo "==> Installing service-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-domain
	@echo "==> Installing lease-signing..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/lease-signing
	@echo "==> Installing wellness-domain..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/wellness-domain
	@echo "==> Wellness vertical installed (lease-signing + wellness-domain). Drive it via the wellness-app, the lattice CLI, or Loupe."

## up-wellness — One-command Wellness vertical: up-full → install-wellness →
## build + start wellness-app (:7802) in the background alongside Loupe
## (:7777). No protected Postgres read model exists for wellness (every lens
## is plain NATS-KV), so no provision-*-role step is needed, unlike
## up-loftspace/up-clinic. Logs: wellness-app.log (+ the up-full logs).
up-wellness:
	@$(MAKE) up-full
	@$(MAKE) install-wellness
	@echo "==> Building wellness-app binary..."
	go build -o bin/wellness-app ./cmd/wellness-app
	@echo "==> Killing any prior wellness-app process..."
	-pkill -f "bin/wellness-app" 2>/dev/null || true
	@echo "==> Starting wellness-app in background (dev-auth staff token minter)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_WELLNESS_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		WELLNESS_APP_DEV_AUTH=1 \
		./bin/wellness-app >wellness-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Wellness ready. Operator/inspector: http://127.0.0.1:7777 (Loupe) · wellness app: http://127.0.0.1:7802"

## install-onebill — Install the Café Inc 3 "one-bill" composition lens (Café
## vertical row, ★★★): re-projects loftspace-ledger's + cafe-ledger's posted
## transactions, tagged by source, into the shared one-bill-history bucket
## keyed by leaseAppKey. Requires BOTH `make install-loftspace` and
## `make install-cafe` to have already run (it matches :transaction/:account
## and :cafetransaction/:cafeaccount vertex classes from each) — installing it
## before either just means that lens side projects zero rows until the
## matching ledger is installed, not an error.
install-onebill:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing one-bill..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/one-bill
	@echo "==> one-bill installed. Combined lease statement: one-bill-history bucket, keyed by leaseAppKey."

## reinstall-package — Dev-loop: diff-apply ONE edited package's DDL/lens onto the
## RUNNING stack in place, no `make down` (F-004 upgrade-aware install). PKG=<dir>,
## e.g. `make reinstall-package PKG=packages/clinic-domain`. A same-version edit
## lands via --force; a bumped version auto-upgrades. The Processor commits the
## create/update/tombstone delta in one atomic batch and the Refractor re-projects
## the changed lenses live — an ADDED lens/role/op hot-activates the same as an
## edited one (Refractor's CDC watch + the DDL cache both react to any commit, not
## just updates). Only a primordial/kernel-seed change needs `make down &&
## up-<vertical>`. See docs/components/_packages.md.
reinstall-package:
	@if [ -z "$(PKG)" ]; then echo "usage: make reinstall-package PKG=packages/<dir>"; exit 2; fi
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying $(PKG) in place (no teardown)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force $(PKG)

## refresh-clinic — Dev-loop refresh of the Clinic vertical onto the RUNNING stack,
## no `make down`: diff-apply the vertical's packages in place (F-004 upgrade-aware
## install) AND rebuild+restart bin/clinic-app, so an edited OR newly-added handler /
## lens / DDL lands live in one command (both hot-activate — no restart needed).
## Requires an already-running up-clinic (or up-full + install-clinic). Mirrors
## up-clinic minus the teardown + up-full.
refresh-clinic:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying clinic packages in place..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/orchestration-base
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/clinic-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/clinic-reminders
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/clinic-ledger
	@$(MAKE) provision-clinic-role
	@echo "==> Rebuilding clinic-app binary..."
	go build -o bin/clinic-app ./cmd/clinic-app
	@echo "==> Restarting clinic-app..."
	-pkill -f "bin/clinic-app" 2>/dev/null || true
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_CLINIC_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		CLINIC_APP_PG_DSN="$(CLINIC_APP_PG_DSN)" CLINIC_APP_DEV_AUTH=1 \
		./bin/clinic-app >clinic-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Clinic refreshed (packages diff-applied + clinic-app restarted). Patient app: http://127.0.0.1:7799"

## refresh-cafe — Dev-loop refresh of the Café vertical onto the RUNNING stack,
## no `make down`: diff-apply the vertical's packages in place (F-004
## upgrade-aware install) AND rebuild+restart bin/cafe-app — an edited OR
## newly-added lens/role/op hot-activates live, no restart needed. Requires an
## already-running up-cafe (or up-full + install-cafe).
refresh-cafe:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying café packages in place..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/orchestration-base
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/service-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/lease-signing
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/cafe-ledger
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/cafe-domain
	@echo "==> Rebuilding cafe-app binary..."
	go build -o bin/cafe-app ./cmd/cafe-app
	@echo "==> Restarting cafe-app..."
	-pkill -f "bin/cafe-app" 2>/dev/null || true
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_CAFE_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		CAFE_APP_DEV_AUTH=1 \
		./bin/cafe-app >cafe-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Café refreshed (packages diff-applied + cafe-app restarted). Café app: http://127.0.0.1:7801"

## refresh-wellness — Dev-loop refresh of the Wellness vertical onto the RUNNING
## stack, no `make down`: diff-apply the vertical's packages in place (F-004
## upgrade-aware install) AND rebuild+restart bin/wellness-app — an edited OR
## newly-added lens/role/op hot-activates live, no restart needed. Requires an
## already-running up-wellness (or up-full + install-wellness).
refresh-wellness:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying wellness packages in place..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/orchestration-base
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/service-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/lease-signing
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/wellness-domain
	@echo "==> Rebuilding wellness-app binary..."
	go build -o bin/wellness-app ./cmd/wellness-app
	@echo "==> Restarting wellness-app..."
	-pkill -f "bin/wellness-app" 2>/dev/null || true
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_WELLNESS_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		WELLNESS_APP_DEV_AUTH=1 \
		./bin/wellness-app >wellness-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Wellness refreshed (packages diff-applied + wellness-app restarted). Wellness app: http://127.0.0.1:7802"

## refresh-loftspace — Dev-loop refresh of the LoftSpace vertical onto the RUNNING
## stack, no `make down`: diff-apply the vertical's packages in place (F-004
## upgrade-aware install) AND rebuild+restart bin/loftspace-app — an edited OR
## newly-added lens/role/op hot-activates live, no restart needed. Requires an
## already-running up-loftspace (or up-full + install-loftspace). Mirrors
## up-loftspace minus the teardown + up-full.
refresh-loftspace:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying loftspace packages in place..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/orchestration-base
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/location-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/loftspace-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/service-domain
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/lease-signing
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/loftspace-ledger
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_PKG) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/bespoke-contracts
	@$(MAKE) provision-loftspace-role
	@echo "==> Rebuilding loftspace-app binary..."
	go build -o bin/loftspace-app ./cmd/loftspace-app
	@echo "==> Restarting loftspace-app..."
	-pkill -f "bin/loftspace-app" 2>/dev/null || true
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LOFTSPACE_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		LOFTSPACE_APP_PG_DSN="$(LOFTSPACE_APP_PG_DSN)" LOFTSPACE_APP_DEV_AUTH=1 \
		./bin/loftspace-app >loftspace-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> LoftSpace refreshed (packages diff-applied + loftspace-app restarted). Applicant app: http://127.0.0.1:7788"

## run-loupe — Build + run Loupe (the view/control web app) in the FOREGROUND.
## Open http://127.0.0.1:7777. Requires a running deployment (make up / up-full).
## Op-submissions (Shred/Revoke/Attach/Detach/pkg install-upgrade-uninstall)
## relay through the Gateway as the logged-in operator's own verified token
## (loupe-operator-auth-lift-design.md §3.2) — `make run-gateway` (:8080,
## LOUPE_GATEWAY_URL's default) must also be running, which `make up` alone
## does not start; `up-full`/`up-full-capability` do.
run-loupe:
	@echo "==> Building loupe binary..."
	go build -o bin/loupe ./cmd/loupe
	@echo "==> Loupe on http://127.0.0.1:7777 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LOUPE) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) LOUPE_PG_DSN=$(LOUPE_PG_DSN) LOUPE_DEV_AUTH=1 ./bin/loupe

## run-gateway — Build + run the Gateway (external write-path translator) in the
## FOREGROUND, DEV MODE (trusts the checked-in dev JWT key — never for prod).
## Listens on :8080. Mint a token: ./bin/gateway dev-token -sub <identityNanoID>.
## Requires a running deployment (make up / up-full).
## run-gateway — Build + run the Gateway in the FOREGROUND. The read-path
## front (Fire 3) serves from GATEWAY_PG_DSN + GATEWAY_READ_MODELS_DIR;
## `make provision-gateway-role` provisions the role once against a live
## Postgres. Unprovisioned/unreachable Postgres is non-fatal — GET
## /v1/<name> 502s "read model unavailable" until it is.
run-gateway:
	@echo "==> Building gateway binary..."
	go build -o bin/gateway ./cmd/gateway
	@echo "==> Gateway on :8080, GATEWAY_DEV_MODE=true (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_GATEWAY) GATEWAY_DEV_MODE=true \
		GATEWAY_PG_DSN=$(GATEWAY_PG_DSN) GATEWAY_READ_MODELS_DIR=$(GATEWAY_READ_MODELS_DIR) \
		GATEWAY_CORS_ORIGINS=$(GATEWAY_CORS_ORIGINS) \
		./bin/gateway

## run-loftspace-app — Build + run the LoftSpace applicant app in the FOREGROUND.
## Open http://127.0.0.1:7788. Requires a running deployment with the LoftSpace
## vertical (make up-full && make install-loftspace).
run-loftspace-app:
	@echo "==> Building loftspace-app binary..."
	go build -o bin/loftspace-app ./cmd/loftspace-app
	@echo "==> LoftSpace applicant app on http://127.0.0.1:7788 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LOFTSPACE_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/loftspace-app

## run-clinic-app — Build + run the Clinic app in the FOREGROUND. Open
## http://127.0.0.1:7799. Requires a running deployment with the clinic vertical
## installed (make up-full + install-clinic, or make up-clinic).
run-clinic-app:
	@echo "==> Building clinic-app binary..."
	go build -o bin/clinic-app ./cmd/clinic-app
	@echo "==> Clinic app on http://127.0.0.1:7799 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_CLINIC_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/clinic-app

## run-cafe-app — Build + run the Café app in the FOREGROUND. Open
## http://127.0.0.1:7801. Requires a running deployment with the Café vertical
## installed (make up-full + install-cafe, or make up-cafe).
run-cafe-app:
	@echo "==> Building cafe-app binary..."
	go build -o bin/cafe-app ./cmd/cafe-app
	@echo "==> Café app on http://127.0.0.1:7801 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_CAFE_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) CAFE_APP_DEV_AUTH=1 ./bin/cafe-app

## run-wellness-app — Build + run the Wellness app in the FOREGROUND. Open
## http://127.0.0.1:7802. Requires a running deployment with the Wellness
## vertical installed (make up-full + install-wellness, or make up-wellness).
run-wellness-app:
	@echo "==> Building wellness-app binary..."
	go build -o bin/wellness-app ./cmd/wellness-app
	@echo "==> Wellness app on http://127.0.0.1:7802 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_WELLNESS_APP) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) WELLNESS_APP_DEV_AUTH=1 ./bin/wellness-app

## test — Run all Go unit + integration tests.
## Test packages run concurrently (-p 4). Every embedded NATS/JetStream
## fixture binds a random port (Port = -1) and owns a private StoreDir
## under t.TempDir(), so concurrent packages share no JetStream file state.
test:
	@echo "==> go test ./... -p 4"
	go test ./... -p 4

## test-hello-lattice — Run the Phase 1 Gate 5 Hello Lattice integration test suite.
## Requires a running Docker stack (make up) with Refractor live.
## Exits 0 when all six milestones pass and the gate5 Health KV marker is written.
.PHONY: test-hello-lattice
POSTGRES_URL ?= postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable

test-hello-lattice:
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
	  POSTGRES_URL=$(POSTGRES_URL) \
	  go test -tags integration ./internal/hellolattice/... -v -p 1 -count=1 -timeout 30m

## test-health-completeness — Run the Health KV completeness integration test.
## Requires a running Docker stack (make up) with Processor + Refractor live.
## Asserts every non-event-driven Health KV key is present within 30s.
.PHONY: test-health-completeness
test-health-completeness:
	NATS_URL=$(NATS_URL) NATS_NKEY=$(NKEY_LATTICE_CLI) go test -tags integration ./internal/healthkv/... -v -timeout 90s

## test-rollback — Run the Phase 1 Gate 4 compensating-op rollback test suite.
## Self-contained: uses embedded NATS, no Docker stack required.
## Exits 0 when the full create → discover → compensate → verify cycle passes
## for both DDL vertex type and lens branches.
.PHONY: test-rollback
test-rollback:
	go test ./internal/aiagent/... -run TestGate4_CompensatingOpRollback -v -p 1 -count=1

## test-lease-convergence — Story 14.5 external-I/O idempotency + convergence gate.
## Self-contained: embedded NATS, no Docker stack. Boots Processor + Refractor +
## Loom + Weaver + the live bridge in-process, installs the real package chain,
## drives a lease application to steady-state convergence through the live bridge
## (Loom externalTask + bridge + temporal freshness + tasks), proves the external
## effect is at-most-once (FR58 end-to-end), asserts D5 (outcome in aspect, root
## data minimal), and exercises the eager bgcheck-freshness @at lapse. Compiled
## with -tags leaseshortwindow so the freshness window is short enough to watch a
## lapse in bounded wall-clock (the production window stays 5m).
.PHONY: test-lease-convergence
test-lease-convergence:
	go test -tags leaseshortwindow ./internal/leaseconvergence/... -run TestLeaseConvergence -v -p 1 -count=1 -timeout 10m

## test-object-gc — v1b object-GC Loop A+B convergence gate. Self-contained:
## embedded NATS, boots Processor + outbox + Refractor + Weaver + the
## object-store-manager in-process, installs rbac → identity → objects-base,
## attaches + detaches an object, and proves the full chain reclaims the bytes
## (the objectLiveness lens → Weaver directOp(TombstoneObject) → object.tombstoned
## → manager byte delete). Compiled with -tags objectgc.
.PHONY: test-object-gc
test-object-gc:
	go test -tags objectgc ./internal/objectgc/... -run TestObjectGC -v -p 1 -count=1 -timeout 3m

## test-crypto-shred — Vault crypto-shredding Fire 4a end-to-end gate.
## Self-contained: embedded NATS, installs rbac → privacy-base → identity →
## hygiene, wires the real Processor commit path (Vault encrypt-on-write /
## decrypt-on-read) plus a Refractor lens + the KeyShredded nullification
## listener, and drives CreateUnclaimedIdentity -> ShredIdentityKey, proving
## the async privacy-worker + Refractor keyshredded listener both handle the
## event (design vault-crypto-shredding-design.md §6). Compiled with
## -tags cryptoshred.
.PHONY: test-crypto-shred
test-crypto-shred:
	go test -tags cryptoshred ./internal/cryptoshred/... -run TestCryptoShred -v -p 1 -count=1 -timeout 3m

## test-system-actor-capability — system-actor-package-op-grants Fire 2 gate.
## Self-contained: embedded NATS, boots the REAL Processor under
## LATTICE_AUTH_MODE=capability (stub OFF, the Fire 1 union read) + the real
## Refractor projecting both the core `capability` anchor and rbac-domain's
## `capabilityRoles` lens, installs rbac -> identity -> orchestration-base ->
## objects-base -> privacy-base, and submits the four system-actor-submitted
## engine ops (Weaver MarkExpired, Loom CreateTask, object-store-manager
## DetachObject, the privacy actor's RecordShredFinalization) as the real
## kernel-seeded actors, proving each authorizes (design
## system-actor-package-op-grants-design.md §8 Fire 2). Compiled with
## -tags systemactorcapability.
.PHONY: test-system-actor-capability
test-system-actor-capability:
	go test -tags systemactorcapability ./internal/systemactorcapability/... -run TestSystemActorCapability -v -p 1 -count=1 -timeout 3m

## test-control-plane-authz — FR30 Fire 1b Gate-3 control-plane bypass gate.
## Self-contained: embedded NATS, boots the REAL Processor under
## LATTICE_AUTH_MODE=capability + the real Refractor projecting the core
## `capability` anchor and rbac-domain's `capabilityRoles` lens, installs
## rbac -> identity -> control-authz, seeds an operator identity (granted
## control-operator via the real AssignRole op) and an intruder identity (no
## grant), then drives a real weaver control.Service wired with
## internal/controlauth.CapabilityKVChecker over a real NATS round-trip:
## operator disable succeeds, intruder disable denied, anonymous (no
## Lattice-Actor header) disable denied (design
## control-plane-capability-authz-design.md §5). Compiled with
## -tags controlplaneauthz.
.PHONY: test-control-plane-authz
test-control-plane-authz:
	go test -tags controlplaneauthz ./internal/controlplaneauthz/... -run TestControlPlaneAuthz -v -p 1 -count=1 -timeout 3m

## test-augur-convergence — the Augur (Weaver L3 reasoning tier) escalation gate.
## Self-contained: embedded NATS, boots Processor + outbox + Weaver + the live
## bridge (with the deterministic FakeAugur reasoning adapter — no real model
## call), installs rbac → identity → orchestration-base → augur, and drives an
## UNPLANNABLE convergence gap through the full Option-F loop: Weaver escalates →
## directOp(CreateAugurReasoningClaim) → external.augur → bridge FakeAugur →
## RecordProposal. Proves a benign in-scope proposal lands `pending` (billed at
## most once) AND a crafted scope-escaping proposal is caught by the §5 validator
## and stored `invalid` (never dispatchable — the AI-surface DEFENDED assertion).
## Also drives Fire 2b's last hop: an approved directOp proposal dispatches
## through Weaver's real two-op fire (the materialised remediation, then
## RecordProposalDispatch) — the gap actually closes (the remediation commits
## through the real Processor) and the proposal reaches `dispatched`.
## Compiled with -tags augurconvergence.
.PHONY: test-augur-convergence
test-augur-convergence:
	go test -tags augurconvergence ./internal/augurconvergence/... -run TestAugurConvergence -v -p 1 -count=1 -timeout 5m

## test-unrouted-convergence — FR28/FR29's unroutedTasks Weaver target gate.
## Self-contained: embedded NATS, boots Processor + Weaver, installs rbac →
## identity → orchestration-base via the real InstallPackage op path
## (registering the real unroutedTasks meta.weaverTarget + its missing_claim
## gap materialised to the new §10.8 `surface` action), hand-writes the row the
## real unroutedTasks lens projects (proven correct at the cypher level by
## orchestration-base's lens_cypher_test.go — this harness runs no live
## Refractor), and proves the `surface` action round-trips through the real
## install path end to end: a violating row raises a named Health-KV issue
## (Contract #5 §5.5 issues[]) at the declared severity with NO remediation
## ever dispatched, and the issue clears once the row closes. Compiled with
## -tags unroutedconvergence.
.PHONY: test-unrouted-convergence
test-unrouted-convergence:
	go test -tags unroutedconvergence ./internal/unroutedconvergence/... -run TestUnroutedConvergence -v -p 1 -count=1 -timeout 3m

## vet — Run go vet on all packages except vendored ANTLR-generated parsers
## (which contain expected unreachable-code patterns from the generator).
##
## -unreachable=false: ANTLR-generated source uses an unreachable
## `goto errorExit` trick after `return` statements. Since 3.1b-i wires
## the `full` rule engine to actually import the cypher package, the
## unreachable-code analyzer reports on those generated files even when
## the cypher package itself is excluded from the package list (vet's
## unreachable analyzer scans files of imported packages). Disabling
## the unreachable analyzer is the targeted fix — every other vet
## analyzer remains enabled.
vet:
	@echo "==> go vet ./... (excluding vendored ANTLR parsers)"
	go vet -unreachable=false $$(go list ./... | grep -v 'internal/refractor/ruleengine/full/cypher')

## lint-conventions — Static check for CLAUDE.md code conventions (history/changelog
## comments, asp.* key prefixes). Advisory by default; STRICT=1 exits non-zero.
lint-conventions:
	@echo "==> Linting code conventions..."
	go run ./scripts/lint-conventions.go

## lint-board — Backlog-board discipline (index-not-journal): row/section/Done-log
## budgets + journal-pattern + dependency-consistency. Advisory; STRICT=1 exits non-zero.
lint-board:
	@echo "==> Linting backlog board..."
	go run ./scripts/lint-board.go

## install-skills — Install the canonical agentic-ops role-skills from agents/
## into the (gitignored) .claude/skills/ where the harness discovers them.
install-skills:
	@mkdir -p .claude/skills
	@for d in agents/*/; do \
		name=$$(basename $$d); \
		rm -rf ".claude/skills/$$name"; \
		cp -R "$$d" ".claude/skills/$$name"; \
		echo "installed skill: $$name"; \
	done

## clean — Remove compiled binaries.
clean:
	rm -rf bin/

## logs — Show container logs.
logs:
	docker compose logs -f

## ps — Show running containers.
ps:
	docker compose ps
