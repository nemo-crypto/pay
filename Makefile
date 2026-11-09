.PHONY: run test build
run:
	go run ./cmd/server
test:
	go test -race ./...
	go vet ./...
build:
	go build -trimpath -o bin/pay ./cmd/server
