SHELL := /usr/bin/env bash
COMPOSE ?= docker compose
GO ?= go
MODEL ?= qwen2.5:3b

.PHONY: help init-env build run fmt tidy test test-race test-integration bench \
        lint vuln secrets openapi-lint compose-up compose-down demo demo-ollama llm \
        load demo-gif clean down

help:
	@echo "gogate targets"
	@echo "  make init-env          create .env with random local secrets"
	@echo "  make build             build ./bin/gogate"
	@echo "  make run               run the gateway locally"
	@echo "  make test              run unit tests"
	@echo "  make test-race         run unit tests with the race detector"
	@echo "  make test-integration  run testcontainers integration tests"
	@echo "  make lint              gofmt, go vet and golangci-lint"
	@echo "  make vuln              govulncheck"
	@echo "  make secrets           gitleaks scan"
	@echo "  make demo              start mock-only demo and run curl examples"
	@echo "  make demo-ollama       start Ollama, pull $(MODEL) and show an example"
	@echo "  make llm               start Ollama and pull $(MODEL) only"
	@echo "  make load              run k6 load test and write build/k6-summary.json"
	@echo "  make demo-gif          record docs/demo.gif with VHS (optional tooling)"
	@echo "  make compose-up        start the full observability stack"
	@echo "  make down              stop all services"
	@echo "  make clean             remove build artefacts and local runtime data"

init-env:
	./scripts/init-env.sh

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X github.com/yugrinovD/gogate/internal/version.Version=dev -X github.com/yugrinovD/gogate/internal/version.Commit=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X github.com/yugrinovD/gogate/internal/version.Date=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/gogate ./cmd/gogate
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o bin/gogate-mockupstream ./cmd/gogate-mockupstream

run: init-env build
	$(GO) run ./cmd/gogate

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	$(GO) test -race -tags=integration ./...

bench:
	$(GO) test -run '^$$' -bench . -benchmem ./internal/...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt required:"; gofmt -l .; exit 1)
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed; skipping"; fi

vuln:
	@if command -v govulncheck >/dev/null 2>&1; then govulncheck ./...; else $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...; fi

secrets:
	@if command -v gitleaks >/dev/null 2>&1; then gitleaks detect --source . --no-git --config .gitleaks.toml; elif command -v docker >/dev/null 2>&1; then docker run --rm -v "$(CURDIR):/repo" -w /repo zricethezav/gitleaks:latest detect --source . --no-git --config .gitleaks.toml; else echo "gitleaks is not installed"; exit 1; fi

openapi-lint:
	@if command -v npx >/dev/null 2>&1; then npx --yes @redocly/cli@latest lint api/openapi.public.yaml api/openapi.admin.yaml; else echo "npx is required for OpenAPI linting"; exit 1; fi

compose-up: init-env
	$(COMPOSE) up -d --build postgres redis gogate prometheus grafana jaeger

demo: init-env
	./scripts/demo.sh

demo-ollama: init-env
	./scripts/demo.sh --ollama

llm: init-env
	$(COMPOSE) --profile llm up -d ollama
	@echo "Waiting for Ollama..."
	@for i in $$(seq 1 60); do $(COMPOSE) exec -T ollama ollama list >/dev/null 2>&1 && break; sleep 2; done
	$(COMPOSE) exec -T ollama ollama pull $(MODEL)

load: init-env
	./scripts/load.sh

demo-gif:
	./scripts/demo-gif.sh

down:
	$(COMPOSE) --profile llm --profile load down --remove-orphans

compose-down: down

clean:
	$(COMPOSE) --profile llm --profile load down -v --remove-orphans
	rm -rf bin build coverage.out coverage.html data
