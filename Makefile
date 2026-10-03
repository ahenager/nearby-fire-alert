BINARY := nearby-fire-alert
PKG := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILDTIME)

.PHONY: all build test lint fmt vet run clean

all: build

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)/cmd/nearby-fire-alert

test:
	go test ./...

lint: vet
	go vet ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run:
	go run ./cmd/nearby-fire-alert

clean:
	rm -f $(BINARY)
