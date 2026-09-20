.PHONY: tidy build run test test-unit test-integration vet fmt

# Resolve deps and generate go.sum.
tidy:
	go mod tidy

vet:
	go vet ./...

fmt:
	gofmt -l -w .

build: tidy
	go build -o bin/server ./cmd/server

run: build
	./bin/server

# Fast, dependency-free unit tests (pure business rules).
test-unit:
	go test ./internal/domain/... -v

# Concurrency / correctness tests against a real Postgres.
# Requires TEST_DATABASE_URL, e.g.:
#   createdb cashback_test
#   TEST_DATABASE_URL='postgres://localhost:5432/cashback_test?sslmode=disable' make test-integration
test-integration:
	go test ./internal/store/... -run Concurrency -v

# Everything.
test: test-unit
	go test ./... -v
