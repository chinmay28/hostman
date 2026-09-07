SHELL := /bin/bash
GO ?= go
BIN := bin/deployer
WEB_DIST := server/internal/web/dist

## The version's patch number is the repository's commit count, which a compiled
## binary can't ask git for — scripts/version.mjs works it out and the linker
## stamps it in. Empty (no node, no git, a shallow clone) leaves the Go package's
## default of 0, which reads as "unstamped build" rather than as a release.
VERSION_PKG := github.com/chinmay28/deployer/server/internal/version
PATCH := $(shell node scripts/version.mjs --patch 2>/dev/null)
LDFLAGS := -s -w $(if $(PATCH),-X $(VERSION_PKG).Patch=$(PATCH))

.PHONY: build server web test test-web test-installer test-provision test-torrent vet run clean version bump-version

## build: PWA into the embed directory, then the single binary
build: web server

server: | $(WEB_DIST)/index.html
	cd server && $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN) ./cmd/deployer

## version: print the version this tree would build as
version:
	@node scripts/version.mjs

## bump-version: move the version line to this month (UTC) — the first commit
## on a branch does this, so a release says when its work began
bump-version:
	@node scripts/version.mjs --bump

## The Go binary embeds $(WEB_DIST), so it needs to exist even when only the
## server is being built. This placeholder is replaced by the real PWA.
$(WEB_DIST)/index.html:
	@mkdir -p $(WEB_DIST)
	@printf '<!doctype html>\n<meta charset="utf-8">\n<title>HostMan</title>\n<p>Web UI not built. Run <code>make build</code>.</p>\n' > $@

## web: build the PWA straight into the server's embed directory
web:
	@if [ -d apps/web ]; then \
		cd apps/web && npm ci && npm run build; \
	else \
		echo "apps/web not present yet — keeping the placeholder in $(WEB_DIST)"; \
	fi

test:
	cd server && $(GO) test ./...

## test-web: the PWA's unit tests (needs node; installs apps/web's dependencies)
test-web:
	cd apps/web && npm ci && npm test

## test-installer: install, upgrade, rollback and uninstall in a sandbox (needs root)
test-installer:
	./scripts/test-quickstart.sh

## test-provision: set a host up over SSH for real (needs root and sshd; makes a
## throwaway user and writes /etc/sudoers.d/deployer, then removes both)
test-provision:
	cd server && DEPLOYER_E2E=1 $(GO) test ./internal/hosts/ -run ProvisionEndToEnd -v

## test-torrent: drive a real deluge (needs deluged and deluge-console; starts a
## daemon on 58946 that talks to the network for as long as the test runs)
test-torrent:
	cd server && DEPLOYER_DELUGE=1 $(GO) test ./internal/hostops/ -run RealDeluge -v

vet:
	cd server && $(GO) vet ./...

## run: build and start the server with verbose logging
run: server
	./$(BIN) -v

clean:
	rm -rf bin
