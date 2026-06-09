.PHONY: build test test-race test-cov lint fmt tidy clean run ci ci-migration-gates ci-sqlc migrate-new sqlc sqlc-diff serve init-config serve-smoke serve-port stop-serve

BINARY := workingbad
PKG := ./...
SQLC_VERSION := v1.21.0

# Dogfood paths — user's local truth source.
HOME_CONFIG_DIR := $(HOME)/.workingbad
HOME_CONFIG := $(HOME_CONFIG_DIR)/config.yaml
HOME_DB := $(HOME_CONFIG_DIR)/db.sqlite

# Smoke paths — throwaway, safe to nuke.
SMOKE_DIR := /tmp/workingbad-smoke
SMOKE_CONFIG := $(SMOKE_DIR)/config.yaml
SMOKE_DB := $(SMOKE_DIR)/db.sqlite

DEFAULT_PORT := 7878
PORT ?= $(DEFAULT_PORT)

build:
	go build -o $(BINARY) ./cmd/workingbad

test:
	go test $(PKG)

test-race:
	go test -race $(PKG)

test-cov:
	go test -coverprofile=coverage.txt -covermode=atomic $(PKG)
	go tool cover -html=coverage.txt -o coverage.html

lint:
	golangci-lint run $(PKG)

fmt:
	goimports -local github.com/Leon180/workingbad -w .
	gofmt -s -w .

tidy:
	go mod tidy

clean:
	rm -f $(BINARY) coverage.txt coverage.html
	go clean -testcache

run: build
	./$(BINARY) $(ARGS)

ci: lint test-race build ci-migration-gates ci-sqlc

migrate-new:
	@test -n "$(NAME)" || (echo "usage: make migrate-new NAME=<name>"; exit 1)
	@./scripts/new-migration.sh $(NAME)

ci-migration-gates:
	@./scripts/check-migration-gates.sh

# sqlc — uses the locally-installed sqlc binary (brew install sqlc).
# `go run sqlc@version` would avoid the install but pulls cgo on macOS and
# conflicts with Xcode's strchrnul header. CI uses the official sqlc-dev
# setup-sqlc action with the same SQLC_VERSION.
SQLC ?= sqlc
sqlc:
	$(SQLC) generate

# ci-sqlc fails CI if the committed generated code drifts from the SQL files.
ci-sqlc:
	$(SQLC) diff

# serve — build + run against the dogfood config at ~/.workingbad/config.yaml.
serve: build
	@if [ ! -f "$(HOME_CONFIG)" ]; then \
		echo "no config at $(HOME_CONFIG) — run: make init-config"; \
		exit 1; \
	fi
	./$(BINARY) --config $(HOME_CONFIG) serve

# write-config — internal helper. Writes $(CONFIG) with $(DB_PATH) if absent.
# Uses printf so YAML indentation survives Make's per-line recipe execution.
define WRITE_CONFIG
	@mkdir -p $(dir $(CONFIG))
	@if [ ! -f "$(CONFIG)" ]; then \
		printf 'db:\n  path: %s\nai:\n  kind: local\n  local:\n    endpoint: http://localhost:11434\n    model: llama3.1\n' "$(DB_PATH)" > $(CONFIG); \
		echo "wrote $(CONFIG)"; \
	fi
endef

# init-config — write ~/.workingbad/config.yaml if absent. Idempotent.
init-config:
	$(call WRITE_CONFIG)
init-config: CONFIG := $(HOME_CONFIG)
init-config: DB_PATH := $(HOME_DB)

# serve-smoke — fresh temp DB + auto-written config. Won't touch dogfood data.
serve-smoke: build
	$(call WRITE_CONFIG)
	./$(BINARY) --config $(SMOKE_CONFIG) serve
serve-smoke: CONFIG := $(SMOKE_CONFIG)
serve-smoke: DB_PATH := $(SMOKE_DB)

# serve-port — run dogfood with custom port (e.g. make serve-port PORT=7890).
serve-port: build
	@if [ ! -f "$(HOME_CONFIG)" ]; then \
		echo "no config at $(HOME_CONFIG) — run: make init-config"; \
		exit 1; \
	fi
	./$(BINARY) --config $(HOME_CONFIG) serve --port $(PORT)

# stop-serve — kill whatever is listening on the default port.
stop-serve:
	@lsof -nP -iTCP:$(DEFAULT_PORT) -sTCP:LISTEN -t | xargs -r kill || true
