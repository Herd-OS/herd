VERSION ?= $(shell git describe --tags --always --dirty)
LDFLAGS = -ldflags="-X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o bin/herd ./cmd/herd

release:
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o bin/herd-linux-amd64 ./cmd/herd
	GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o bin/herd-linux-arm64 ./cmd/herd
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o bin/herd-darwin-amd64 ./cmd/herd
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o bin/herd-darwin-arm64 ./cmd/herd
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/herd-windows-amd64.exe ./cmd/herd

test:
	go test ./...

lint:
	golangci-lint run

.PHONY: build release test lint
