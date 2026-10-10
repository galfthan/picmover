# Makefile for picmover: test/vet and cross-platform builds

GO        ?= go
BINARY    := picmover
DIST      := dist
# nodynamic: the gen2brain image decoders (HEIC, AVIF, WebP encoder) use
# their embedded WebAssembly builds only, never a system shared library.
TAGS        := -tags nodynamic
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_FLAGS := $(TAGS) -trimpath -ldflags '-s -w -X picmover/cmd.Version=$(VERSION)'

# Node for the web frontend: the dev box installs it under ~/.local/node.
NODE_BIN := $(HOME)/.local/node/bin
ifneq ($(wildcard $(NODE_BIN)),)
export PATH := $(NODE_BIN):$(PATH)
endif

.PHONY: build test vet check web web-check build-all build-linux build-windows build-darwin clean install

# build and build-all embed the frontend, so they build it first (needs
# Node). A plain 'go build' works without Node; the binary then says the
# frontend is not built.
build: web
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(BINARY) .

test:
	$(GO) test $(TAGS) ./...

vet:
	$(GO) vet $(TAGS) ./...

# web builds the Svelte app into web/dist, which the binary embeds.
web:
	cd web && npm ci && npm run build

web-check:
	cd web && npm ci && npm run check && npm test

check: vet test web-check

build-all: web
	$(MAKE) build-linux build-windows build-darwin

build-linux:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(DIST)/picmover-linux-amd64 .

build-windows:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(DIST)/picmover-windows-amd64.exe .

build-darwin:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -o $(DIST)/picmover-darwin-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(DIST)/picmover-darwin-amd64 .

clean:
	rm -rf dist picmover

# Author's personal convenience target: copies the Linux and Windows builds
# into the author's bin directories.
install: web
	$(MAKE) build-linux build-windows
	cp $(DIST)/picmover-linux-amd64 /home/alfthan/bin/picmover
	cp $(DIST)/picmover-windows-amd64.exe /mnt/c/Users/galft/bin/picmover-win64.exe
