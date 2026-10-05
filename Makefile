# ClusterForge – ontwikkeltaken. `make help` toont een overzicht.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DEV_DATABASE_URL ?= postgres://clusterforge:clusterforge@localhost:5432/clusterforge?sslmode=disable
TEST_DATABASE_URL ?= postgres://clusterforge:clusterforge@localhost:5432/clusterforge_test?sslmode=disable
SQLC_VERSION := v1.29.0

.PHONY: help
help: ## Toon deze hulp
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

.PHONY: dev-up
dev-up: ## Start PostgreSQL en VictoriaMetrics voor ontwikkeling
	docker compose -f deploy/docker-compose.dev.yml up -d

.PHONY: dev-down
dev-down: ## Stop de ontwikkelcontainers
	docker compose -f deploy/docker-compose.dev.yml down

.PHONY: dev-server
dev-server: ## Start de Go-server op :8080 tegen de ontwikkeldatabase
	CF_DATABASE_URL="$(DEV_DATABASE_URL)" CF_SECURE_COOKIES=false CF_LOG_LEVEL=debug go run ./cmd/clusterforge-server serve

.PHONY: dev-web
dev-web: ## Start de Next.js-devserver op :3000 (stuurt /api door naar :8080)
	cd web && pnpm dev

.PHONY: dev-admin
dev-admin: ## Maak een beheerder aan in de ontwikkeldatabase (USER=naam)
	CF_DATABASE_URL="$(DEV_DATABASE_URL)" go run ./cmd/clusterforge-server admin create -username $(or $(USER),admin)

.PHONY: generate
generate: ## Genereer sqlc-, OpenAPI-server- en TypeScript-code opnieuw
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
	go generate ./...
	cd web && pnpm generate:api

.PHONY: web
web: ## Bouw de webinterface en kopieer hem naar internal/webui/dist
	cd web && pnpm install --frozen-lockfile && pnpm build
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -r web/out/. internal/webui/dist/

.PHONY: build
build: web ## Bouw bin/clusterforge-server met ingebedde webinterface
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/clusterforge-server ./cmd/clusterforge-server

.PHONY: test
test: ## Draai alle Go-tests (integratietests tegen TEST_DATABASE_URL)
	CF_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -p 1 ./...

.PHONY: lint
lint: ## Lint Go en de webinterface
	go vet ./...
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	cd web && pnpm lint && pnpm typecheck

.PHONY: docker
docker: ## Bouw de container-image
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t clusterforge-server:$(VERSION) .
