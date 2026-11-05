# tmx: one binary for the daemon, the CLI, the MCP server and the desktop app.
# `make` builds it for this machine; `make help` lists everything.
#
# Builds run at low priority with few parallel jobs so the laptop stays usable:
# override with `make JOBS=8 NICE=`.

# Windows (GitHub's runners, Git Bash + mingw make): /bin/bash is not a Windows path, so take bash from PATH.
# $(OS) is still the environment's here (Windows_NT); it is redefined as GOOS below.
ifeq ($(OS),Windows_NT)
SHELL       := bash.exe
else
SHELL       := /bin/bash
endif
.SHELLFLAGS := -eo pipefail -c
.DEFAULT_GOAL := build

GO     ?= go
PNPM   ?= pnpm
JOBS   ?= 2
NICE   ?= $(if $(shell command -v nice 2>/dev/null),nice -n 10,)
WAILS_VERSION := v2.17.0
WAILS  ?= $(or $(shell command -v wails 2>/dev/null),$(shell $(GO) env GOPATH)/bin/wails)

MODULE  := github.com/blackcode-ch/tmx-go
VERSION ?= $(shell git describe --tags --match 'fleet-v[0-9]*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/config.Version=$(VERSION)
export GOFLAGS := -p=$(JOBS)

OS     := $(shell $(GO) env GOOS)
ARCH   := $(shell $(GO) env GOARCH)
EXE    := $(if $(filter windows,$(OS)),.exe,)
APP    := build/bin/tmxg.app
BIN    := bin/tmxg$(EXE)

# macOS: who signs tmx.app. "-" = ad hoc, a new identity in every build, so macOS forgets the
# microphone and screen-recording permissions on every update. CI signs with the certificate from
# `make signing-cert` (SIGN_IDENTITY = its SHA-1, SIGN_KEYCHAIN = scripts/signing-keychain.sh's keychain).
SIGN_IDENTITY ?= -
SIGN_KEYCHAIN ?=
MIC_USAGE := tmx uses the microphone only while you record or dictate.
CAM_USAGE := tmx uses the camera only while you record a camera clip.

UI_SRC := $(shell find ui/src ui/public -type f 2>/dev/null) ui/index.html ui/vite.config.ts \
          ui/tsconfig.json ui/tsconfig.app.json ui/tsconfig.node.json ui/package.json ui/pnpm-lock.yaml
UI_OUT := ui/dist/index.html
UI_DEPS := ui/node_modules/.modules.yaml

# Cross-compiled headless binaries (no window; servers and CI). CGO is off: pure Go everywhere.
DIST_TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: release-key signing-cert build app app-windows headless dist ui icon deps tools test race vet lint typecheck check run up down status open clean distclean help

## build: the one binary for this machine (macOS/Windows: with the desktop window; Linux: headless)
ifeq ($(OS),linux)
build: headless
else
build: app
endif

## app: desktop build (bin/tmxg is the CLI; on macOS it points into build/bin/tmxg.app)
app: $(UI_OUT) | $(WAILS)
	$(NICE) $(WAILS) build -m -s -skipbindings -tags desktop -trimpath -ldflags "$(LDFLAGS)"
	@mkdir -p bin
ifeq ($(OS),darwin)
	@# The bundle's version (Finder, About): wails.json has none, so it said 1.0.0. v1.2.3[-n-gsha] → 1.2.3.
	@v=$$(echo "$(VERSION)" | sed -E 's/^v//; s/-.*//'); case "$$v" in [0-9]*.[0-9]*) \
	  /usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $$v" -c "Set :CFBundleVersion $$v" $(APP)/Contents/Info.plist ;; esac
	@# Without these keys macOS ends the app the moment the page asks for the microphone or the camera.
	@/usr/libexec/PlistBuddy -c "Set :NSMicrophoneUsageDescription '$(MIC_USAGE)'" $(APP)/Contents/Info.plist 2>/dev/null || \
	  /usr/libexec/PlistBuddy -c "Add :NSMicrophoneUsageDescription string '$(MIC_USAGE)'" $(APP)/Contents/Info.plist
	@/usr/libexec/PlistBuddy -c "Set :NSCameraUsageDescription '$(CAM_USAGE)'" $(APP)/Contents/Info.plist 2>/dev/null || \
	  /usr/libexec/PlistBuddy -c "Add :NSCameraUsageDescription string '$(CAM_USAGE)'" $(APP)/Contents/Info.plist
	@# Info.plist changed, so sign again (always: an unsigned arm64 app does not start).
	codesign --force --sign "$(SIGN_IDENTITY)" $(if $(SIGN_KEYCHAIN),--keychain "$(SIGN_KEYCHAIN)") $(APP)
	@codesign --verify --deep --strict $(APP)
	@ln -sfn ../$(APP)/Contents/MacOS/tmxg $(BIN)
	@echo "built $(APP) (open it for the window); CLI: $(BIN)"
else
	@cp build/bin/tmxg$(EXE) $(BIN)
	@echo "built $(BIN)"
endif

## app-windows: the Windows desktop exe, cross-built on any machine (Wails needs no cgo there): build/bin/tmxg.exe
app-windows: $(UI_OUT) | $(WAILS)
	$(NICE) $(WAILS) build -m -s -skipbindings -tags desktop -trimpath -platform windows/amd64 -ldflags "$(LDFLAGS)"
	@echo "built build/bin/tmxg.exe"

## headless: bin/tmxg without the window (daemon, CLI, MCP; no cgo)
headless: $(UI_OUT)
	@mkdir -p bin
	@rm -f $(BIN)
	CGO_ENABLED=0 $(NICE) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .
	@echo "built $(BIN) (headless)"

## dist: headless binaries for every platform in dist/
dist: $(UI_OUT)
	@mkdir -p dist
	@set -e; for t in $(DIST_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  out=dist/tmxg_$(VERSION)_$${os}_$${arch}$$ext; \
	  echo "  $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(NICE) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out .; \
	done
	@cd dist && shasum -a 256 tmxg_$(VERSION)_* > SHA256SUMS && cat SHA256SUMS

## ui: build the React UI into ui/dist (embedded into the binary)
ui: $(UI_OUT)
$(UI_OUT): $(UI_DEPS) $(UI_SRC)
	cd ui && $(NICE) $(PNPM) build
	@touch $@

## icon: re-render the product icon from build/appicon.svg (appicon.png, the UI's favicon and touch icon)
icon: $(UI_DEPS)
	cd ui && $(NICE) node scripts/icon.mjs

## deps: install the UI's packages (from the lockfile)
deps: $(UI_DEPS)
$(UI_DEPS): ui/package.json ui/pnpm-lock.yaml
	cd ui && $(PNPM) install --frozen-lockfile
	@touch $@

## tools: install the Wails CLI (needed for `make app`)
tools: $(WAILS)
$(WAILS):
	$(GO) install github.com/wailsapp/wails/v2/cmd/wails@$(WAILS_VERSION)

## test: every Go test (real claude runs only with TMXG_LIVE=1), then the UI's node tests (ui/test)
test: $(UI_DEPS)
	$(NICE) $(GO) test ./...
	cd ui && $(PNPM) test test/*.test.mjs

## race: Go tests with the race detector (slow; RACE_PKGS=./internal/chat/... to narrow)
RACE_PKGS ?= ./...
race:
	$(NICE) $(GO) test -race $(RACE_PKGS)

## vet: go vet for this OS plus the headless Linux and Windows builds
vet:
	$(NICE) $(GO) vet ./...
	CGO_ENABLED=0 GOOS=linux $(NICE) $(GO) vet ./...
	CGO_ENABLED=0 GOOS=windows $(NICE) $(GO) vet ./...

## lint: gofmt check and the UI's eslint
lint: $(UI_DEPS)
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	cd ui && $(PNPM) lint

## typecheck: TypeScript check of the UI
typecheck: $(UI_DEPS)
	cd ui && $(PNPM) typecheck

## check: everything CI would run (lint, typecheck, vet, test, build)
check: lint typecheck vet test build

## run: build, then start the daemon (tmx up)
run: build
	$(BIN) up

up down status:
	$(BIN) $@

## open: open the desktop window (macOS)
open:
	open $(APP)

## release-key: make the release signing key (secret on GitHub, public key into internal/update/keys.go)
REPO ?= blackcode-ch/tmx-go
release-key:
	scripts/release-key.sh $(REPO)

## signing-cert: make tmx.app's code-signing certificate (GitHub secrets; macOS keeps permissions across updates)
signing-cert:
	scripts/signing-cert.sh $(REPO)

## clean: remove build output (keeps ui/node_modules)
clean:
	rm -rf bin dist build/bin ui/wailsjs
	find ui/dist -mindepth 1 ! -name .keep -delete 2>/dev/null || true

## distclean: clean plus ui/node_modules
distclean: clean
	rm -rf ui/node_modules

## help: this list
help:
	@echo "tmx $(VERSION) ($(OS)/$(ARCH))"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  make /' | sed 's/: /\t/' | expand -t 22
