# bash so recipes can rely on pipefail (set per-recipe; .SHELLFLAGS needs
# GNU make >= 3.82, but macOS ships 3.81).
SHELL := /bin/bash

BINARY := baseloop
VERSION ?= dev
DEV_VERSION ?= 0.5.0-local
DEV_HOME ?= /tmp/baseloop-dev-home
DEV_BIN_DIR ?= /tmp/baseloop-dev-bin
DEV_SKIP_SETUP ?= 0
DEV_SKIP_AUTH ?= 0

.PHONY: build test fmt smoke release-check dev-release dev-install dev-doctor dev-uninstall clean

build:
	go build -ldflags "-X github.com/baseloop-hq/baseloop-cli/internal/version.Version=$(VERSION)" -o bin/$(BINARY) ./cmd/baseloop

test:
	go test ./...

fmt:
	gofmt -w cmd internal

smoke: build
	./bin/$(BINARY) --version
	./bin/$(BINARY) commands --json
	./bin/$(BINARY) auth status --json
	tmp=$$(mktemp -d); \
	printf '#!/bin/sh\nexit 0\n' > "$$tmp/claude"; \
	chmod +x "$$tmp/claude"; \
	printf '#!/bin/sh\nexit 0\n' > "$$tmp/codex"; \
	chmod +x "$$tmp/codex"; \
	home=$$(mktemp -d); \
	HOME="$$home" CODEX_HOME="$$home/.codex" PATH="$$tmp:$$PATH" ./bin/$(BINARY) setup skills --json

release-check: fmt test smoke
	scripts/check-cli-surface.sh

dev-release:
	scripts/build-release.sh $(DEV_VERSION)
	scripts/install-dev.sh file://$(CURDIR)/dist

dev-install: dev-release
	rm -rf $(DEV_HOME) $(DEV_BIN_DIR)
	mkdir -p $(DEV_BIN_DIR)
	set -o pipefail; curl -fsSL file://$(CURDIR)/dist/install-cli | \
		HOME=$(DEV_HOME) \
		BASELOOP_AGENT_HOME=$(HOME) \
		BASELOOP_BIN_DIR=$(DEV_BIN_DIR) \
		BASELOOP_VERSION=$(DEV_VERSION) \
		BASELOOP_API_URL=$(BASELOOP_API_URL) \
		BASELOOP_WEB_URL=$(BASELOOP_WEB_URL) \
		BASELOOP_SKIP_SETUP=$(DEV_SKIP_SETUP) \
		BASELOOP_SKIP_AUTH=$(DEV_SKIP_AUTH) \
		bash

dev-doctor:
	HOME=$(DEV_HOME) $(DEV_BIN_DIR)/$(BINARY) doctor --json

dev-uninstall:
	rm -rf $(DEV_HOME) $(DEV_BIN_DIR)

clean:
	rm -rf bin dist
