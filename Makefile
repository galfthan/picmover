# Makefile for picmover: test/vet and cross-platform builds

GO        ?= go
BINARY    := picmover
DIST      := dist
# nodynamic: the gen2brain image decoders (HEIC, AVIF, WebP encoder) use
# their embedded WebAssembly builds only, never a system shared library.
TAGS        := -tags nodynamic
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_FLAGS := $(TAGS) -trimpath -ldflags '-s -w -X picmover/cmd.Version=$(VERSION)'

# The desktop app (cmd/picmover-app): a GUI program on Windows (no console).
APP_LDFLAGS := -s -w -X main.version=$(VERSION)
APP_FLAGS   := $(TAGS) -trimpath -ldflags '$(APP_LDFLAGS)'
APP_WIN     := $(TAGS) -trimpath -ldflags '-H windowsgui $(APP_LDFLAGS)'
# go-winres runs as a tool, outside the module graph.
WINRES      := $(GO) run github.com/tc-hib/go-winres@v0.3.3

# Node for the web frontend: the dev box installs it under ~/.local/node.
NODE_BIN := $(HOME)/.local/node/bin
ifneq ($(wildcard $(NODE_BIN)),)
export PATH := $(NODE_BIN):$(PATH)
endif

.PHONY: build test vet check web web-check build-all build-linux build-windows build-darwin build-app app-binaries winres icons clean install

# build and build-all embed the frontend, so they build it first (needs
# Node). A plain 'go build' works without Node; the binary then says the
# frontend is not built.
build: web
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(BINARY) .

test:
	$(GO) test $(TAGS) ./...

# vet covers the three target systems (the shell has per-OS files).
vet:
	for os in linux windows darwin; do CGO_ENABLED=0 GOOS=$$os $(GO) vet $(TAGS) ./... || exit 1; done

# web builds the Svelte app into web/dist, which the binary embeds.
web:
	cd web && npm ci && npm run build

web-check:
	cd web && npm ci && npm run check && npm test

check: vet test web-check

build-all: web
	$(MAKE) build-linux build-windows build-darwin app-binaries

# build-app builds picmover-app for Windows (GUI), Linux and macOS (arm64).
build-app: web
	$(MAKE) app-binaries

app-binaries:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build $(APP_WIN) -o $(DIST)/picmover-app.exe ./cmd/picmover-app
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(APP_FLAGS) -o $(DIST)/picmover-app ./cmd/picmover-app
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build $(APP_FLAGS) -o $(DIST)/picmover-app-darwin-arm64 ./cmd/picmover-app

# winres regenerates the Windows resources of picmover-app (icon, manifest,
# version info) as cmd/picmover-app/rsrc_windows_amd64.syso, which is
# committed so that builds need only Go. The version strings come from
# VERSION at the time it is run. It is a release step: run it on a clean
# tree and commit the .syso; the file then names the commit it was made
# on, so the exe's file version is one commit behind by construction
# (unless it is run on a tag, which then names the tag). icons redraws
# the icon PNGs.
winres:
	$(WINRES) make --in winres/winres.json --out cmd/picmover-app/rsrc --arch amd64 \
		--product-version=$(VERSION) --file-version=$(VERSION)

icons:
	$(GO) run ./winres/gen

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
	rm -rf dist picmover picmover-app

# Author's personal convenience target: copies the Linux and Windows builds
# into the author's bin directories.
install: web
	$(MAKE) build-linux build-windows
	cp $(DIST)/picmover-linux-amd64 /home/alfthan/bin/picmover
	cp $(DIST)/picmover-windows-amd64.exe /mnt/c/Users/galft/bin/picmover-win64.exe
