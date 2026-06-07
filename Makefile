.PHONY: build test test-race test-cov lint fmt tidy clean run ci ci-migration-gates migrate-new

BINARY := workingbad
PKG := ./...

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

ci: lint test-race build ci-migration-gates

migrate-new:
	@test -n "$(NAME)" || (echo "usage: make migrate-new NAME=<name>"; exit 1)
	@./scripts/new-migration.sh $(NAME)

ci-migration-gates:
	@./scripts/check-migration-gates.sh
