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

# install puts the Linux binaries in INSTALL_DIR and, when WIN_INSTALL_DIR
# is set (for example /mnt/c/Users/<you>/bin under WSL), the Windows exes
# there.
INSTALL_DIR     ?= $(HOME)/bin
WIN_INSTALL_DIR ?=

# The dev container (.devcontainer): Go, Node and the tools in an image.
DOCKER     ?= docker
DEV_IMAGE  ?= picmover-dev
DEV_WORK   := /workspaces/picmover
# -t only when stdin is a terminal, so the dev-* targets also run from
# scripts and CI.
DEV_TTY    := $(shell [ -t 0 ] && echo -t)
DEV_RUN     = $(DOCKER) run --rm -i $(DEV_TTY) \
	-v "$(CURDIR):$(DEV_WORK)" -w $(DEV_WORK) \
	-v picmover-gomod:/home/dev/go/pkg/mod \
	-v picmover-gocache:/home/dev/.cache/go-build \
	-v picmover-npm:/home/dev/.npm

.PHONY: build test vet check web web-check build-all build-linux build-windows build-darwin build-app app-binaries build-app-windows build-app-linux build-app-darwin winres icons clean install dev-image dev-shell dev-install dev-check

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

app-binaries: build-app-windows build-app-linux build-app-darwin

build-app-windows:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build $(APP_WIN) -o $(DIST)/picmover-app.exe ./cmd/picmover-app

build-app-linux:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(APP_FLAGS) -o $(DIST)/picmover-app ./cmd/picmover-app

build-app-darwin:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build $(APP_FLAGS) -o $(DIST)/picmover-app-darwin-arm64 ./cmd/picmover-app

# winres regenerates the Windows resources of picmover-app (icon, manifest,
# version info) as cmd/picmover-app/rsrc_windows_amd64.syso, which is
# committed so that builds need only Go. The version strings come from
# VERSION at the time it is run. It is a release step: run it on a clean
# tree and commit the .syso; the file then names the commit it was made
# on, so the exe's file version is one commit behind by construction
# (unless it is run on a tag, which then names the tag). icons redraws
# the icon PNGs (Windows resources, tray, Linux desktop entry, web favicon
# and manifest icons).
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

# install builds the Linux and Windows binaries and desktop apps, installs
# the Linux ones as $(INSTALL_DIR)/picmover and $(INSTALL_DIR)/picmover-app
# and, if WIN_INSTALL_DIR is set, copies the exes there as
# picmover-win64.exe and picmover-app.exe. On Linux, picmover-app writes
# its own launcher entry and icons when it starts.
install: web
	$(MAKE) build-linux build-windows build-app-linux build-app-windows
	install -D -m 0755 $(DIST)/picmover-linux-amd64 "$(INSTALL_DIR)/picmover"
	install -D -m 0755 $(DIST)/picmover-app "$(INSTALL_DIR)/picmover-app"
ifneq ($(strip $(WIN_INSTALL_DIR)),)
	mkdir -p "$(WIN_INSTALL_DIR)"
	cp $(DIST)/picmover-windows-amd64.exe "$(WIN_INSTALL_DIR)/picmover-win64.exe"
	cp $(DIST)/picmover-app.exe "$(WIN_INSTALL_DIR)/picmover-app.exe"
else
	@echo "Windows builds: $(DIST)/picmover-windows-amd64.exe and $(DIST)/picmover-app.exe (set WIN_INSTALL_DIR to copy them)"
endif

# dev-image builds the dev container image with your uid/gid, so files it
# writes to the repo are owned by you.
dev-image:
	$(DOCKER) build -t $(DEV_IMAGE) \
		--build-arg USER_UID=$(shell id -u) --build-arg USER_GID=$(shell id -g) \
		.devcontainer

# dev-shell opens a shell in the dev container with the repo mounted. Go
# and npm caches live in named volumes. Your git name and email, if set,
# are passed in as GIT_AUTHOR_*/GIT_COMMITTER_* variables.
dev-shell:
	n="$$(git config user.name)"; e="$$(git config user.email)"; \
	export GIT_AUTHOR_NAME="$$n" GIT_COMMITTER_NAME="$$n" \
		GIT_AUTHOR_EMAIL="$$e" GIT_COMMITTER_EMAIL="$$e"; \
	$(DEV_RUN) \
		$${n:+-e GIT_AUTHOR_NAME -e GIT_COMMITTER_NAME} \
		$${e:+-e GIT_AUTHOR_EMAIL -e GIT_COMMITTER_EMAIL} \
		$(DEV_IMAGE) bash -l

# dev-install runs 'make install' in the dev container; INSTALL_DIR (and
# WIN_INSTALL_DIR, if set) are mounted so the binaries land on the host.
dev-install:
	mkdir -p "$(INSTALL_DIR)" $(if $(strip $(WIN_INSTALL_DIR)),"$(WIN_INSTALL_DIR)")
	$(DEV_RUN) -v "$$(cd "$(INSTALL_DIR)" && pwd):/install" \
		$(if $(strip $(WIN_INSTALL_DIR)),-v "$$(cd "$(WIN_INSTALL_DIR)" && pwd):/wininstall") \
		$(DEV_IMAGE) make install INSTALL_DIR=/install \
		$(if $(strip $(WIN_INSTALL_DIR)),WIN_INSTALL_DIR=/wininstall)

# dev-check runs 'make check' in the dev container.
dev-check:
	$(DEV_RUN) $(DEV_IMAGE) make check
