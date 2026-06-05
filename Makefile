# Lattice Phase 1 — Story 1.3 Dev Harness
# Requires: Docker, Docker Compose, Go 1.26.1+
#
# Quick reference:
#   make up              — start everything (cold or warm)
#   make down            — tear down everything cleanly
#   make verify-bootstrap — assert primordial state; exit 0 on success
#   make build           — compile all binaries
#   make vet             — run go vet ./...

SHELL := /bin/bash
NATS_URL ?= nats://localhost:4222
BOOTSTRAP_JSON ?= $(abspath ./lattice.bootstrap.json)

# Load .env if it exists (ignored by git).
-include .env

.PHONY: up down verify-kernel verify-package-rbac verify-package-identity verify-package-identity-hygiene verify-conformance build vet test test-bypass test-capability-adversarial test-rollback test-cli test-hello-lattice test-health-completeness processor run-processor clean logs ps

## up — Bring up NATS + Postgres, run bootstrap binary, block until readiness gate.
up:
	@echo "==> Starting NATS + Postgres..."
	docker compose up -d --wait
	@echo "==> Containers healthy."
	@echo "==> Killing any background refractor processes (avoid warm-up duplicates)..."
	-pkill -f "bin/refractor" 2>/dev/null || true
	@echo "==> Killing any background processor processes (avoid warm-up duplicates)..."
	-pkill -f "bin/processor" 2>/dev/null || true
	@echo "==> Building bootstrap binary..."
	go build -o bin/bootstrap ./cmd/bootstrap
	@echo "==> Building refractor binary (Story 2.1)..."
	go build -o bin/refractor ./cmd/refractor
	@echo "==> Building lattice CLI..."
	go build -o bin/lattice ./cmd/lattice
	@echo "==> Running bootstrap (seed pass — readiness gate deferred until Refractor is up)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bootstrap -skip-ready-wait
	@echo "==> Starting refractor in background..."
	NATS_URL=$(NATS_URL) REFRACTOR_PG_DSN="postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable" ./bin/refractor >refractor.log 2>&1 </dev/null &
	@echo "==> Running bootstrap (readiness gate — blocks until admin + Loom + Weaver cap.* projections land)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bootstrap
	@echo "==> Building processor binary..."
	go build -o bin/processor ./cmd/processor
	@echo "==> Starting processor in background..."
	NATS_URL=$(NATS_URL) PROCESSOR_FILTER=ops.default,ops.urgent,ops.system,ops.meta LATTICE_AUTH_MODE=stub ./bin/processor >processor.log 2>&1 </dev/null &
	@echo "==> Lattice ready."

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
	@echo "==> Down complete."

## verify-kernel — Assert post-Story-4.7 kernel keys exist with correct envelopes.
## Expected count ≈ 89 OK lines (28 top-level keys + aspects + streams/buckets).
verify-kernel:
	@echo "==> Running kernel verification..."
	NATS_URL=$(NATS_URL) go run ./scripts/verify-kernel.go

## verify-package-rbac — Install rbac-domain package and assert its KV state.
verify-package-rbac:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing rbac-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/rbac-domain
	@echo "==> Running rbac-domain package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-rbac.go

## verify-package-identity — Install identity-domain package and assert its KV state.
verify-package-identity:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing identity-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-domain
	@echo "==> Running identity-domain package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-identity.go

## verify-package-identity-hygiene — Install identity-hygiene and assert its KV state.
verify-package-identity-hygiene:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing identity-hygiene..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-hygiene
	@echo "==> Running identity-hygiene package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-identity-hygiene.go

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
run-processor: processor
	@echo "==> Starting processor (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) ./bin/processor

## test — Run all Go unit + integration tests.
## NOTE (Story 2.1b Gap 4): -p 1 serializes test-package execution.
## Embedded NATS / JetStream fixtures across many packages each spin up
## an in-process server; with default GOMAXPROCS parallelism many
## servers run concurrently and exhaust file-descriptor/memory budgets
## on the runner, manifesting as KV-put "context deadline exceeded"
## timeouts in the bypass + substrate suites. Fixtures already use
## Port = -1 (random) so port collisions are not the underlying cause;
## -p 1 is the targeted fix until Phase 2 reduces fixture cost.
test:
	@echo "==> go test ./... -p 1"
	go test ./... -p 1

## test-bypass — Run the Phase 1 Gate 2 adversarial bypass test suite.
## Requires a running Docker stack (make up). Exits 0 only when all 4 bypass
## categories are BLOCKED. Writes gate2-report.txt and the Health KV marker.
.PHONY: test-bypass
test-bypass:
	@$(MAKE) down
	@$(MAKE) up
	@$(MAKE) verify-kernel
	go test ./internal/bypass/... -v -count=1

## test-capability-adversarial — Run the Phase 1 Gate 3 Capability Lens
## adversarial test suite. Requires a running Docker stack (make up). Exits 0
## only when all 4 attack vectors are DEFENDED. Writes gate3-report.txt and the
## Health KV marker at health.gates.phase1.gate3.
##
## Per-vector tests (TestCapAdv_*) use embedded NATS and are self-contained.
## The TestGate3_Report roll-up connects to the live stack for the Health KV marker.
.PHONY: test-capability-adversarial
test-capability-adversarial:
	@$(MAKE) down
	@$(MAKE) up
	@$(MAKE) verify-kernel
	go test ./internal/bypass/... -v -run TestCapAdv -count=1
	go test ./internal/bypass/... -v -run TestGate3_Report -count=1

## test-hello-lattice — Run the Phase 1 Gate 5 Hello Lattice integration test suite.
## Requires a running Docker stack (make up) with Refractor live.
## Exits 0 when all six milestones pass and the gate5 Health KV marker is written.
.PHONY: test-hello-lattice
POSTGRES_URL ?= postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable

test-hello-lattice:
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
	  POSTGRES_URL=$(POSTGRES_URL) \
	  go test -tags integration ./internal/hellolattice/... -v -p 1 -count=1 -timeout 30m

## test-health-completeness — Run the Health KV completeness integration test.
## Requires a running Docker stack (make up) with Processor + Refractor live.
## Asserts every non-event-driven Health KV key is present within 30s.
.PHONY: test-health-completeness
test-health-completeness:
	NATS_URL=$(NATS_URL) go test -tags integration ./internal/healthkv/... -v -timeout 90s

## test-rollback — Run the Phase 1 Gate 4 compensating-op rollback test suite.
## Self-contained: uses embedded NATS, no Docker stack required.
## Exits 0 when the full create → discover → compensate → verify cycle passes
## for both DDL vertex type and lens branches.
.PHONY: test-rollback
test-rollback:
	go test ./internal/aiagent/... -run TestGate4_CompensatingOpRollback -v -p 1 -count=1

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

## clean — Remove compiled binaries.
clean:
	rm -rf bin/

## logs — Show container logs.
logs:
	docker compose logs -f

## ps — Show running containers.
ps:
	docker compose ps
