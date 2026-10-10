# Makefile for picmover: test/vet and cross-platform builds

GO        ?= go
BINARY    := picmover
DIST      := dist
# nodynamic: the gen2brain image decoders (HEIC, AVIF, WebP encoder) use
# their embedded WebAssembly builds only, never a system shared library.
TAGS        := -tags nodynamic
BUILD_FLAGS := $(TAGS) -trimpath -ldflags '-s -w'

.PHONY: build test vet build-all build-linux build-windows build-darwin clean install

build:
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(BINARY) .

test:
	$(GO) test $(TAGS) ./...

vet:
	$(GO) vet $(TAGS) ./...

build-all: build-linux build-windows build-darwin

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
install: build-linux build-windows
	cp $(DIST)/picmover-linux-amd64 /home/alfthan/bin/picmover
	cp $(DIST)/picmover-windows-amd64.exe /mnt/c/Users/galft/bin/picmover-win64.exe
