# Kube Workspaces desktop client — developer targets.
#
# Everything here is plain `go`; there is no code generation step and no
# container image (this repo ships desktop binaries, not a service).

BINARY  ?= kube-workspaces
CMD     ?= ./cmd/kube-workspaces
BIN_DIR ?= bin
DIST_DIR?= dist

# VERSION is stamped into the binary. A tagged build gets the tag; anything
# else gets `<last-tag>-<n>-g<sha>[-dirty]`, or the bare sha in a shallow
# checkout with no tags.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -s -w -X main.version=$(VERSION)

# VERSION without the upstream v prefix, for places that want a plain number
# (the macOS CFBundleShortVersionString).
SHORTVER := $(VERSION:v%=%)

# Windows only: link as a GUI subsystem executable (PE subsystem 2) instead of
# a console one (subsystem 3). Without this the loader allocates a console for
# the process, so launching the client from Explorer opens a black console
# window next to the shell — and that console owns the process, so closing it
# kills the application. No other platform has a subsystem flag; leave their
# link lines alone.
#
# The cost is that a GUI subsystem process inherits no standard streams from
# cmd.exe or PowerShell, which would mute `kube-workspaces list` and every other
# subcommand. cmd/kube-workspaces/console_windows.go reattaches them at startup;
# the two changes only work as a pair, so do not apply one without the other.
WINDOWS_LDFLAGS ?= $(LDFLAGS) -H=windowsgui

# Cross-compilers for the cgo-enabled Windows build (Track B webview). Only
# build-windows-cgo uses them; the tree stays CGO_ENABLED=0 everywhere else.
# webview_go compiles a C++ shim, and Go injects -mthreads into every Windows
# cgo compile, so both CC and CXX must be the Mingw-w64 pair (Debian:
# gcc-mingw-w64-x86-64 + g++-mingw-w64-x86-64).
CGO_CC ?= x86_64-w64-mingw32-gcc
CGO_CXX ?= $(CGO_CC:%-gcc=%-g++)

# Windows PE resource objects. go-winres is a pinned build-time tool invoked
# through `go run @version` so it never appears in go.mod. It writes the
# .syso files into the package directory, where the Go linker automatically
# picks up the one that matches the target GOOS/GOARCH from the filename
# suffix (rsrc_windows_amd64.syso vs rsrc_windows_arm64.syso).
#
# The winres.json defines the .exe icon (from assets/icon.ico) and the
# version-info block; the numeric versions come from `git describe` via
# go-winres' git-tag mode. No manifest is emitted, which is deliberate: the
# process has no declared DPI awareness, so SDL keeps sole control of it and
# this build does not change how the window is presented on high-DPI displays.
WINRES ?= go run github.com/tc-hib/go-winres@v0.3.3

# Passed through to `make run ARGS="..."`.
ARGS ?=

.PHONY: help build build-windows build-windows-cgo run test vet lint fmt tidy cover icons winres clean build-all

help: ## Show this help message
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the kube-workspaces binary into bin/
	@mkdir -p $(BIN_DIR)
	@# GOOS-aware so that a developer on Windows builds the same kind of binary
	@# that ships, rather than a console one that behaves differently from the
	@# release. Everywhere else this is exactly the old command.
	@goos=$$(go env GOOS); ldflags="$(LDFLAGS)"; ext=""; \
	if [ "$$goos" = "windows" ]; then ldflags="$(WINDOWS_LDFLAGS)"; ext=".exe"; $(WINRES) make --in winres.json --out cmd/kube-workspaces/rsrc --arch=amd64,arm64 --product-version=git-tag --file-version=git-tag; fi; \
	echo "go build -trimpath -ldflags \"$$ldflags\" -o $(BIN_DIR)/$(BINARY)$$ext $(CMD)"; \
	go build -trimpath -ldflags "$$ldflags" -o $(BIN_DIR)/$(BINARY)$$ext $(CMD)

build-windows: ## Cross-build a Windows amd64 binary into ./kw.exe for testing on a Windows host
	@echo "go build -trimpath -ldflags \"$(WINDOWS_LDFLAGS)\" -o kw.exe $(CMD)"
	$(WINRES) make --in winres.json --out cmd/kube-workspaces/rsrc --arch=amd64,arm64 --product-version=git-tag --file-version=git-tag
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags "$(WINDOWS_LDFLAGS)" -o kw.exe $(CMD)

build-windows-cgo: ## Cross-build a Windows amd64 binary WITH cgo into ./kw-cgo.exe (Track B webview/WebView2)
	@# Needs the Mingw-w64 cross toolchain (Debian/Ubuntu:
	@#     apt install gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64
	@# ). CGO_ENABLED=1 compiles internal/web's webview_go backend (C++ shim
	@# via $(CGO_CXX); Go adds -mthreads, which the native g++ knows nothing
	@# about), so the `web` subcommand can open container workspaces in the
	@# embedded webview on the Windows host (real-display check 29).
	@# WebView2.h pulls in EventToken.h, which MinGW does not ship: the
	@# ABI-equivalent shim lives at internal/web/mswebview2/EventToken.h and is
	@# injected via CGO_CXXFLAGS below.
	@# build-windows above stays CGO_ENABLED=0 to match the shipped six-target
	@# artifacts.
	@command -v $(CGO_CC) >/dev/null 2>&1 || { echo "error: $(CGO_CC) not found (apt: gcc-mingw-w64-x86-64)" >&2; exit 1; }
	@command -v $(CGO_CXX) >/dev/null 2>&1 || { echo "error: $(CGO_CXX) not found (apt: g++-mingw-w64-x86-64)" >&2; exit 1; }
	$(WINRES) make --in winres.json --out cmd/kube-workspaces/rsrc --arch=amd64,arm64 --product-version=git-tag --file-version=git-tag
	CC=$(CGO_CC) CXX=$(CGO_CXX) CGO_CXXFLAGS="-I$(abspath internal/web/mswebview2)" CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags "$(WINDOWS_LDFLAGS)" -o kw-cgo.exe $(CMD)

winres: ## Regenerate the Windows .syso resources (icon + version info) for cmd/kube-workspaces
	$(WINRES) make --in winres.json --out cmd/kube-workspaces/rsrc --arch=amd64,arm64 --product-version=git-tag --file-version=git-tag

run: ## Run the client from source (make run ARGS="--help")
	go run $(CMD) $(ARGS)

test: ## Run all tests with the race detector
	go test -race ./...

vet: ## Run go vet over every package
	go vet ./...

lint: ## Run golangci-lint if it is installed, otherwise skip
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found; skipping (install: https://golangci-lint.run/welcome/install/)"; \
	fi

fmt: ## Format all Go source with gofmt
	gofmt -w -s .

tidy: ## Tidy and verify go.mod / go.sum
	go mod tidy

# The platform icon artefacts are derived from one master: assets/icon.svg is
# the Kube Workspaces cube; everything else is regenerated from it. Inkscape
# rasterises the vector (ImageMagick's SVG rendering is too low-density to
# keep a thin stroke crisp at 1024×1024), ImageMagick embeds a proper
# multi-size .ico with 32-bit DIB entries, and cmd/mkicon packs the .icns
# container and the 256px copy that the viewer embeds in the binary.
#
# The outputs are committed so builds and CI never need the tools; rerun this
# target when the logo changes.
ICON_SRC  ?= assets/icon.svg
ICON_PNG  ?= assets/icon.png
icons: ## Regenerate all icon artefacts from assets/icon.svg
	@inkscape $(ICON_SRC) -w 1024 -h 1024 -o $(ICON_PNG)
	@convert $(ICON_PNG) -define icon:auto-resize=256,128,64,48,32,16 assets/icon.ico
	go run ./cmd/mkicon -src $(ICON_PNG)

cover: ## Run tests with coverage and print a per-function summary
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

clean: ## Remove build output and coverage artefacts
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out coverage.html

# The whole binary, viewer included, cross-builds from any one host: the SDL3
# binding is purego and bundles the library, so there is no cgo anywhere in the
# tree. If that ever stops being true (H.264 decode via libavcodec is the
# likely cause), this target breaks loudly and release builds move to per-OS
# runners.
build-all: ## Cross-build and package all 6 targets into dist/
	@# Start from an empty dist/: a leftover archive from an earlier build
	@# would otherwise be picked up by the checksum glob and published.
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@# The Windows targets link the .syso resource objects, so generate them
	@# once up front; the non-Windows targets ignore them.
	@$(WINRES) make --in winres.json --out cmd/kube-workspaces/rsrc --arch=amd64,arm64 --product-version=git-tag --file-version=git-tag
	@set -e; for target in \
		linux/amd64 linux/arm64 \
		darwin/amd64 darwin/arm64 \
		windows/amd64 windows/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		ext=""; ldflags="$(LDFLAGS)"; \
		if [ "$$os" = "windows" ]; then ext=".exe"; ldflags="$(WINDOWS_LDFLAGS)"; fi; \
		stage="$(DIST_DIR)/$(BINARY)-$$os-$$arch"; \
		echo "building $$os/$$arch"; \
		rm -rf "$$stage"; mkdir -p "$$stage"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$$ldflags" -o "$$stage/$(BINARY)$$ext" $(CMD); \
		if [ "$$os" = "darwin" ]; then \
			app="$$stage/Kube Workspaces.app"; \
			mkdir -p "$$app/Contents/MacOS" "$$app/Contents/Resources"; \
			mv "$$stage/$(BINARY)" "$$app/Contents/MacOS/$(BINARY)"; \
			cp assets/icon.icns "$$app/Contents/Resources/icon.icns"; \
			printf '%s\n' \
				'<?xml version="1.0" encoding="UTF-8"?>' \
				'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"' \
				' "https://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
				'<plist version="1.0">' \
				'<dict>' \
				'	<key>CFBundleDevelopmentRegion</key><string>en</string>' \
				'	<key>CFBundleDisplayName</key><string>Kube Workspaces</string>' \
				'	<key>CFBundleExecutable</key><string>$(BINARY)</string>' \
				'	<key>CFBundleIconFile</key><string>icon.icns</string>' \
				'	<key>CFBundleIdentifier</key><string>io.kube-workspaces.desktop</string>' \
				'	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>' \
				'	<key>CFBundleName</key><string>kube-workspaces</string>' \
				'	<key>CFBundlePackageType</key><string>APPL</string>' \
				'	<key>CFBundleShortVersionString</key><string>$(SHORTVER)</string>' \
				'	<key>CFBundleVersion</key><string>1</string>' \
				'	<key>LSMinimumSystemVersion</key><string>10.13</string>' \
				'	<key>NSHighResolutionCapable</key><true/>' \
				'</dict>' \
				'</plist>' > "$$app/Contents/Info.plist"; \
			chmod +x "$$app/Contents/MacOS/$(BINARY)"; \
		fi; \
		cp README.md LICENSE "$$stage/"; \
		if [ "$$os" = "windows" ]; then \
			(cd $(DIST_DIR) && zip -qr "$(BINARY)-$(VERSION)-$$os-$$arch.zip" "$(BINARY)-$$os-$$arch"); \
		else \
			tar -czf "$(DIST_DIR)/$(BINARY)-$(VERSION)-$$os-$$arch.tar.gz" \
				-C $(DIST_DIR) "$(BINARY)-$$os-$$arch"; \
		fi; \
		rm -rf "$$stage"; \
	done
	@cd $(DIST_DIR) && sha256sum *.tar.gz *.zip > SHA256SUMS 2>/dev/null || true
	@echo; echo "$(DIST_DIR)/ ($(VERSION)):"; ls -1 $(DIST_DIR)
