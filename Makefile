.DEFAULT_GOAL := help

# .env (see .env.example) also applies to the targets that run Go directly.
-include .env
export PROVIDER1_URL PROVIDER2_URL
ADMIN_API_KEY ?= dev-admin-key
APP_PORT ?= 8080

.PHONY: help
help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: up
up: ## Build and start the stack (Postgres, Redis, app), on :8080 unless APP_PORT says otherwise
	@test -n "$(PROVIDER1_URL)" -a -n "$(PROVIDER2_URL)" || \
		{ echo "Set PROVIDER1_URL and PROVIDER2_URL in .env first (see .env.example)."; exit 1; }
	docker compose up --build -d
	@echo "dashboard  http://localhost:$(APP_PORT)"
	@echo "API docs   http://localhost:$(APP_PORT)/docs"

.PHONY: down
down: ## Stop the stack
	docker compose down --remove-orphans

.PHONY: clean
clean: ## Stop the stack and delete its data
	docker compose down -v --remove-orphans

.PHONY: logs
logs: ## Follow the app's logs
	docker compose logs -f app

.PHONY: ingest
ingest: ## Run ingestion now on the running stack
	@curl -fsS -X POST -H "X-API-Key: $(ADMIN_API_KEY)" localhost:$(APP_PORT)/api/v1/ingest; echo

.PHONY: postman
postman: ## Run the Postman walkthrough against the running stack (needs Node)
	npx --yes newman@6.2.3 run postman/feedrank.postman_collection.json --env-var apiKey=$(ADMIN_API_KEY) --env-var baseUrl=http://localhost:$(APP_PORT)

.PHONY: run
run: ## Run the server locally against the Compose Postgres and Redis
	docker compose up -d postgres redis
	REDIS_ADDR=localhost:6379 ADMIN_API_KEY=$(ADMIN_API_KEY) go run ./cmd/server

.PHONY: dashboard
dashboard: ## Build the dashboard so `go run` can serve it
	cd web/dashboard && npm ci --no-audit --no-fund && npm run build && touch dist/.gitkeep

.PHONY: test
test: ## Unit tests
	go test -race ./...

.PHONY: test-integration
test-integration: ## Unit and Postgres integration tests (needs Docker)
	go test -race -tags integration ./...

.PHONY: test-live
test-live: ## Check the real provider endpoints still match the decoders
	go test -tags live -run Live -v ./internal/provider/

.PHONY: lint
lint: ## golangci-lint
	golangci-lint run ./...
