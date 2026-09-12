VERSION ?= $(shell git describe --tags --always --dirty)
LDFLAGS = -ldflags="-X main.version=$(VERSION)"
HERD_SERVICE_IMAGE ?= herd-service:dev
HERD_SERVICE_SKIP_BUILD ?= 0

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

test-e2e:
	go test ./tests/e2e/... -tags=e2e -v -timeout=30m -count=1

lint:
	golangci-lint run

image-service-build:
	docker build -f Dockerfile.herd_service --build-arg VERSION=$(VERSION) -t $(HERD_SERVICE_IMAGE) .

image-service-smoke:
	@if [ "$(HERD_SERVICE_SKIP_BUILD)" != "1" ]; then \
		$(MAKE) image-service-build HERD_SERVICE_IMAGE=$(HERD_SERVICE_IMAGE); \
	fi
	@cid=$$(docker run -d --rm -e HERD_ENV=development -p 127.0.0.1::8080 $(HERD_SERVICE_IMAGE)); \
	trap 'docker rm -f $$cid >/dev/null 2>&1 || true' EXIT; \
	for i in $$(seq 1 30); do \
		port=$$(docker port $$cid 8080/tcp 2>/dev/null | sed 's/.*://'); \
		if [ -n "$$port" ] && curl -fsS "http://127.0.0.1:$$port/healthz"; then \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	docker logs $$cid; \
	exit 1

.PHONY: build release test test-e2e lint image-service-build image-service-smoke
