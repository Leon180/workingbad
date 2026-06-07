.PHONY: build test test-race test-cov lint fmt tidy clean run ci ci-migration-gates ci-sqlc migrate-new sqlc sqlc-diff

BINARY := workingbad
PKG := ./...
SQLC_VERSION := v1.21.0

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
