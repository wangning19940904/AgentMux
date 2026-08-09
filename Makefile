# AgentMux build orchestration.
#
# Common targets:
#   make build           # build CLI for the host (no embedded WebUI)
#   make web             # build the React WebUI into web/dist
#   make release         # build CLI with embedded WebUI for the host
#   make cross           # cross-compile CLI (+embedded WebUI) for all platforms
#   make desktop         # build the Wails desktop app (requires wails toolchain)
#   make menubar         # build the macOS SwiftUI menu bar app (macOS only)
#   make test            # run Go tests
#   make sign-macos      # codesign + notarize the macOS artifacts (needs creds)

BINARY      := amux
ALIAS       := agentmux
CMD         := ./cmd/amux
HOOK_BINARY := agentmux-hook
HOOK_CMD    := ./cmd/agentmux-hook
DIST        := dist
REMOTE_ASSETS := desktop/build/remote-assets
VERSION     ?= 0.1.0
LDFLAGS     := -s -w -X main.version=$(VERSION)
HOOK_LDFLAGS := -s -w
PLATFORMS   := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64
WAILS       ?= $(HOME)/go/bin/wails

.PHONY: all build web release cross remote-assets desktop menubar test vet clean tidy

all: release

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	go build -ldflags "$(HOOK_LDFLAGS)" -o $(HOOK_BINARY) $(HOOK_CMD)
	@ln -sf $(BINARY) $(ALIAS)

web:
	cd web && npm install && npm run build

# Release build with the WebUI embedded (requires `make web` first).
release: web
	go build -tags embedweb -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	go build -ldflags "$(HOOK_LDFLAGS)" -o $(HOOK_BINARY) $(HOOK_CMD)
	@ln -sf $(BINARY) $(ALIAS)

# Cross-compile the CLI with embedded WebUI for every target platform.
cross: web
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)-$(VERSION)-$$os-$$arch$$ext"; \
		hook_out="$(DIST)/$(HOOK_BINARY)-$(VERSION)-$$os-$$arch$$ext"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -tags embedweb -ldflags "$(LDFLAGS)" -o "$$out" $(CMD) || exit 1; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -ldflags "$(HOOK_LDFLAGS)" -o "$$hook_out" $(HOOK_CMD) || exit 1; \
	done
	@echo "artifacts in $(DIST)/"

# CLI payloads used by the desktop Console to bootstrap SSH machines without
# requiring a pre-existing AgentMux installation on the target.
remote-assets: web
	@mkdir -p $(REMOTE_ASSETS)
	@for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${p%/*}; arch=$${p#*/}; out="$(REMOTE_ASSETS)/amux-$$os-$$arch"; \
		echo "building remote installer $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -tags embedweb -ldflags "$(LDFLAGS)" -o "$$out" $(CMD) || exit 1; \
	done

# Wails desktop app. Requires: go install github.com/wailsapp/wails/v2/cmd/wails@latest
# and a frontend symlink: ln -s ../web desktop/frontend
desktop: web remote-assets
	go build -ldflags "$(HOOK_LDFLAGS)" -o $(HOOK_BINARY) $(HOOK_CMD)
	@if [ "$$(uname -s)" = "Darwin" ]; then $(MAKE) menubar; fi
	@mkdir -p desktop/build
	@cp assets/branding/agentmux-logo.png desktop/build/appicon.png
	cd desktop && $(WAILS) build -tags "desktop embedweb" -skipbindings -ldflags "$(LDFLAGS)"
	@for app_dir in desktop/build/bin/*.app; do \
		[ -d "$$app_dir" ] || continue; \
		mkdir -p "$$app_dir/Contents/Resources/agentmux-remote"; \
		cp $(REMOTE_ASSETS)/amux-* "$$app_dir/Contents/Resources/agentmux-remote/"; \
	done
	@if [ "$$(uname -s)" = "Darwin" ]; then \
		for app_dir in desktop/build/bin/*.app; do \
			[ -d "$$app_dir" ] || continue; \
			macos_dir="$$app_dir/Contents/MacOS"; \
			cp macos-menubar/AgentMuxMenuBar "$$macos_dir/AgentMuxMenuBar"; \
			cp $(HOOK_BINARY) "$$macos_dir/$(HOOK_BINARY)"; \
			codesign --force --sign - "$$macos_dir/AgentMuxMenuBar"; \
			codesign --force --sign - "$$macos_dir/$(HOOK_BINARY)"; \
			codesign --force --deep --sign - "$$app_dir"; \
		done; \
	fi

# macOS menu bar app (SwiftUI). macOS only.
menubar:
	cd macos-menubar && swiftc -O -o AgentMuxMenuBar main.swift AppDelegate.swift \
		-framework AppKit -framework SwiftUI

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(DIST) $(REMOTE_ASSETS) $(BINARY) $(ALIAS) $(HOOK_BINARY) web/dist macos-menubar/AgentMuxMenuBar

# Codesign + notarize macOS artifacts. Set these env vars first:
#   MACOS_SIGN_IDENTITY  e.g. "Developer ID Application: Your Name (TEAMID)"
#   AC_PROFILE           notarytool keychain profile name
sign-macos:
	@test -n "$(MACOS_SIGN_IDENTITY)" || (echo "set MACOS_SIGN_IDENTITY" && exit 1)
	codesign --force --options runtime --timestamp \
		--sign "$(MACOS_SIGN_IDENTITY)" $(DIST)/$(BINARY)-$(VERSION)-darwin-arm64
	codesign --force --options runtime --timestamp \
		--sign "$(MACOS_SIGN_IDENTITY)" $(DIST)/$(BINARY)-$(VERSION)-darwin-amd64
	@echo "To notarize: xcrun notarytool submit <zip> --keychain-profile $(AC_PROFILE) --wait"
