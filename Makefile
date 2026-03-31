VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
BINARY := keni-agent

.PHONY: build build-linux-amd64 build-linux-arm64 build-all mock-dashboard test lint clean

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/keni-agent

mock-dashboard:
	go build -o bin/mock-dashboard ./cmd/mock-dashboard

build-linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(BINARY)-linux-amd64 ./cmd/keni-agent

build-linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(BINARY)-linux-arm64 ./cmd/keni-agent

build-all: build-linux-amd64 build-linux-arm64

test:
	go test -v -race ./...

lint:
	go vet ./...

clean:
	rm -rf bin/
