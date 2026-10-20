BIN        := bin/edgeroutegw
PKG        := github.com/zlylong/EdgeRouteGW
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT) -X $(PKG)/internal/version.BuildTime=$(BUILD_TIME)
GOFLAGS    := -trimpath
export CGO_ENABLED := 0

BUN        ?= bun
WEB_DIST   := internal/web/dist

.PHONY: build build-arm64 test vet run clean deb web web-install web-check

# Frontend: installs deps once, builds into internal/web/dist (embedded by go build).
web-install:
	cd web && $(BUN) install --frozen-lockfile

web: web-install
	cd web && $(BUN) run build

web-check:
	cd web && $(BUN) run typecheck && $(BUN) run test

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

# Development: API on 18080 without drivers, Vite dev server on 5173 proxying /api.
dev-api: build
	$(BIN) serve -no-reconcile -listen 127.0.0.1:18080 -data-dir ./.dev-data -log-level debug

dev-web:
	cd web && $(BUN) run dev

deb: build
	nfpm package -f packaging/nfpm.yaml -p deb -t dist/

clean:
	rm -rf bin dist .dev-data web/dist
	find $(WEB_DIST) -mindepth 1 ! -name .gitkeep -delete
