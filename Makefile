SHELL := /bin/bash
GO ?= go
BIN := bin/hostman
WEB_DIST := server/internal/web/dist

## The version is the commit's month plus the repository's commit count, which
## a compiled binary can't ask git for — scripts/version.mjs works both out and
## the linker stamps them in. Empty (no node) leaves the Go package's defaults:
## patch 0 under the build's month, which reads as "unstamped build" rather
## than as a release. A shallow clone gets patch 0 from the script itself.
VERSION_LDFLAGS := $(shell node scripts/version.mjs --ldflags 2>/dev/null)
LDFLAGS := -s -w $(VERSION_LDFLAGS)

.PHONY: build server web icons test test-web test-icongen test-installer test-provision test-torrent vet run clean version icons-check test-android android

## build: PWA into the embed directory, then the single binary
build: web server

server: | $(WEB_DIST)/index.html
	cd server && $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN) ./cmd/hostman

## version: print the version this tree would build as
version:
	@node scripts/version.mjs

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

## icons: cut the PWA's and the Android app's icons from art/hostman-logo.png
## (run after the logo changes); icons-check fails if a committed one drifted
ICONGEN := cd tools/icongen && $(GO) run . -in ../../art/hostman-logo.png \
	-out ../../apps/web/public -android ../../apps/android/app/src/main/res
icons:
	$(ICONGEN)

icons-check:
	$(ICONGEN) -check

test:
	cd server && $(GO) test ./...

## test-icongen: the icon generator's own tests (it is a separate Go module)
test-icongen:
	cd tools/icongen && $(GO) test ./...

## test-web: the PWA's unit tests (needs node; installs apps/web's dependencies)
test-web:
	cd apps/web && npm ci && npm test

## test-installer: install, upgrade, rollback and uninstall in a sandbox (needs root)
test-installer:
	./scripts/test-quickstart.sh

## test-provision: set a host up over SSH for real (needs root and sshd; makes a
## throwaway user and writes /etc/sudoers.d/hostman, then removes both)
test-provision:
	cd server && HOSTMAN_E2E=1 $(GO) test ./internal/hosts/ -run ProvisionEndToEnd -v

## test-torrent: drive a real deluge (needs deluged and deluge-console; starts a
## daemon on 58946 that talks to the network for as long as the test runs)
test-torrent:
	cd server && HOSTMAN_DELUGE=1 $(GO) test ./internal/hostops/ -run RealDeluge -v

## android: the Android app (apps/android) with the server built in for its
## full-stack mode, stamped with this tree's version; needs a JDK 17+ and the
## Android SDK (ANDROID_HOME). The APK lands in apps/android/app/build/outputs.
GRADLE := cd apps/android && ./gradlew --no-daemon --max-workers=2
APP_VERSION := -PappVersionName=$(shell node scripts/version.mjs 2>/dev/null) \
	-PappVersionCode=$(shell node scripts/version.mjs --patch 2>/dev/null) \
	-PappVersionDate=$(shell node scripts/version.mjs --date 2>/dev/null)
android:
	$(GRADLE) :app:assembleRelease $(APP_VERSION)

## test-android: the app's decisions as plain JVM tests (no Android SDK needed),
## and the Go packages the phone's server depends on
test-android:
	cd server && $(GO) test ./internal/hostenv/ ./internal/discovery/
	$(GRADLE) -PcoreOnly :core:test

vet:
	cd server && $(GO) vet ./...

## run: build and start the server with verbose logging
run: server
	./$(BIN) -v

clean:
	rm -rf bin
