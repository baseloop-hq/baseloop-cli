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
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	agent_bin="$$tmp/agent-bin"; \
	home="$$tmp/home"; \
	mkdir -p "$$agent_bin" "$$home"; \
	printf '#!/bin/sh\nexit 0\n' > "$$agent_bin/claude"; \
	chmod +x "$$agent_bin/claude"; \
	printf '#!/bin/sh\nexit 0\n' > "$$agent_bin/codex"; \
	chmod +x "$$agent_bin/codex"; \
	export HOME="$$home" \
		CODEX_HOME="$$home/.codex" \
		BASELOOP_CONFIG="$$tmp/config.json" \
		BASELOOP_STATE="$$tmp/state" \
		BASELOOP_NO_UPDATE_CHECK=1 \
		PATH="$$agent_bin:/usr/bin:/bin"; \
	./bin/$(BINARY) --version; \
	./bin/$(BINARY) commands --json; \
	./bin/$(BINARY) auth status --json; \
	./bin/$(BINARY) setup skills --json

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
		ZDOTDIR=$(DEV_HOME) \
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
