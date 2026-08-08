.DEFAULT_GOAL := help
SHELL := /bin/bash

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: secrets
secrets: ## Generate TENANT_KEK, VISIT_PEPPER and database passwords into .env
	@test -f .env || cp .env.example .env
	@if grep -q '^TENANT_KEK=.\+' .env; then \
		echo "TENANT_KEK already set in .env; refusing to overwrite."; \
		echo "Regenerating it would make every encrypted field unreadable."; \
	else \
		sed -i.bak "s|^TENANT_KEK=.*|TENANT_KEK=$$(openssl rand -base64 32)|" .env && \
		sed -i.bak "s|^VISIT_PEPPER=.*|VISIT_PEPPER=$$(openssl rand -base64 32)|" .env && \
		sed -i.bak "s|^DB_PASSWORD=.*|DB_PASSWORD=$$(openssl rand -hex 24)|" .env && \
		sed -i.bak "s|^APP_DB_PASSWORD=.*|APP_DB_PASSWORD=$$(openssl rand -hex 24)|" .env && \
		rm -f .env.bak && \
		echo "Secrets written to .env."; \
		echo; \
		echo "  Back up TENANT_KEK somewhere OTHER than your database backups."; \
		echo "  Lose it and every sensitive field and encrypted file is gone for good."; \
	fi

.PHONY: build
build: ## Build both binaries
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-o bin/collectr ./cmd/collectr
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
		-o bin/collectr-worker ./cmd/collectr-worker

.PHONY: test
test: ## Run the test suite
	$(GO) test -race ./...

.PHONY: cover
cover: ## Run tests with a coverage report
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: lint
lint: lint-arch ## Run all linters
	$(GO) vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run || \
		echo "golangci-lint not installed; skipping (go vet still ran)"
	@test -z "$$(gofmt -l ./cmd ./internal)" || \
		{ echo "not gofmt-clean:"; gofmt -l ./cmd ./internal; exit 1; }

.PHONY: lint-arch
lint-arch: ## Verify module import boundaries
	$(GO) test ./internal/arch/...

.PHONY: vuln
vuln: ## Scan dependencies for known vulnerabilities
	@command -v govulncheck >/dev/null || $(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

.PHONY: dev
dev: ## Start postgres and redis for local development
	docker compose up -d postgres redis
	@echo "Postgres and Redis are up. Run: make run"

.PHONY: run
run: ## Run the API server against the local dev dependencies
	set -a && source .env && set +a && \
	ENV=dev \
	DATABASE_URL="postgres://collectr:$$DB_PASSWORD@localhost:5432/collectr?sslmode=disable" \
	REDIS_URL="redis://localhost:6379/0" \
	STORAGE_LOCAL_PATH="./.data/files" \
	$(GO) run ./cmd/collectr

.PHONY: run-worker
run-worker: ## Run the worker against the local dev dependencies
	set -a && source .env && set +a && \
	ENV=dev \
	DATABASE_URL="postgres://collectr:$$DB_PASSWORD@localhost:5432/collectr?sslmode=disable" \
	REDIS_URL="redis://localhost:6379/0" \
	STORAGE_LOCAL_PATH="./.data/files" \
	$(GO) run ./cmd/collectr-worker

.PHONY: up
up: ## Start the full stack
	docker compose up -d --build

.PHONY: down
down: ## Stop the stack (volumes are kept)
	docker compose down

.PHONY: logs
logs: ## Tail application logs
	docker compose logs -f collectr worker

.PHONY: psql
psql: ## Open a psql shell as the owner role
	docker compose exec postgres psql -U collectr -d collectr

.PHONY: load-seed
load-seed: ## Seed links, purposes and the form the k6 scripts expect
	@EMAIL=$(EMAIL) PASSWORD=$(PASSWORD) ./load/seed.sh

.PHONY: load
load: ## Run the load tests (needs the stack up, and make load-seed first)
	docker compose --profile load run --rm k6 run /scripts/redirect.js
	docker compose --profile load run --rm k6 run /scripts/render.js
	docker compose --profile load run --rm k6 run /scripts/submit.js

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf bin coverage.out .data
