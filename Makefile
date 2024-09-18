# Makefile for cross-platform Go builds

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean

# Build parameters
BINARY_NAME=picmover
BINARY_UNIX=$(BINARY_NAME)
BINARY_WIN=$(BINARY_NAME)-win64.exe

# Cross-compilation parameters
CC_WINDOWS=x86_64-w64-mingw32-gcc

.PHONY: all build clean build-linux build-windows

all: build

build: build-linux build-windows

build-linux:
	CGO_ENABLED=1 $(GOBUILD) -o $(BINARY_UNIX)

build-windows:
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=$(CC_WINDOWS) $(GOBUILD) -o $(BINARY_WIN)

clean:
	$(GOCLEAN)
	rm -f $(BINARY_UNIX)
	rm -f $(BINARY_WIN)
