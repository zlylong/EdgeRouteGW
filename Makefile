BIN        := bin/edgeroutegw
PKG        := github.com/zlylong/EdgeRouteGW
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT) -X $(PKG)/internal/version.BuildTime=$(BUILD_TIME)
GOFLAGS    := -trimpath
export CGO_ENABLED := 0

.PHONY: build build-arm64 test vet run clean deb

build:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/edgeroutegw

build-arm64:
	GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)-arm64 ./cmd/edgeroutegw

vet:
	go vet ./...

test: vet
	CGO_ENABLED=1 go test -race -count=1 ./...

run: build
	$(BIN) serve -listen 127.0.0.1:18080 -data-dir ./.dev-data -log-level debug

deb: build
	nfpm package -f packaging/nfpm.yaml -p deb -t dist/

clean:
	rm -rf bin dist .dev-data
