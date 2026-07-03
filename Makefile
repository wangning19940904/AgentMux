# AgentNexus build orchestration.
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

BINARY      := anx
ALIAS       := agent-nexus
CMD         := ./cmd/anx
DIST        := dist
VERSION     ?= 0.1.0
LDFLAGS     := -s -w -X main.version=$(VERSION)
PLATFORMS   := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.PHONY: all build web release cross desktop menubar test vet clean tidy

all: release

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@ln -sf $(BINARY) $(ALIAS)

web:
	cd web && npm install && npm run build

# Release build with the WebUI embedded (requires `make web` first).
release: web
	go build -tags embedweb -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@ln -sf $(BINARY) $(ALIAS)

# Cross-compile the CLI with embedded WebUI for every target platform.
cross: web
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)-$(VERSION)-$$os-$$arch$$ext"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -tags embedweb -ldflags "$(LDFLAGS)" -o "$$out" $(CMD) || exit 1; \
	done
	@echo "artifacts in $(DIST)/"

# Wails desktop app. Requires: go install github.com/wailsapp/wails/v2/cmd/wails@latest
# and a frontend symlink: ln -s ../web desktop/frontend
desktop: web
	cd desktop && wails build -tags desktop

# macOS menu bar app (SwiftUI). macOS only.
menubar:
	cd macos-menubar && swiftc -O -o AgentNexusMenuBar main.swift AppDelegate.swift \
		-framework AppKit -framework SwiftUI

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(DIST) $(BINARY) $(ALIAS) web/dist macos-menubar/AgentNexusMenuBar

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
