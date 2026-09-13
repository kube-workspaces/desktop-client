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

# Passed through to `make run ARGS="..."`.
ARGS ?=

.PHONY: help build run test vet lint fmt tidy cover clean build-all

help: ## Show this help message
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the kube-workspaces binary into bin/
	@mkdir -p $(BIN_DIR)
	@# GOOS-aware so that a developer on Windows builds the same kind of binary
	@# that ships, rather than a console one that behaves differently from the
	@# release. Everywhere else this is exactly the old command.
	@goos=$$(go env GOOS); ldflags="$(LDFLAGS)"; ext=""; \
	if [ "$$goos" = "windows" ]; then ldflags="$(WINDOWS_LDFLAGS)"; ext=".exe"; fi; \
	echo "go build -trimpath -ldflags \"$$ldflags\" -o $(BIN_DIR)/$(BINARY)$$ext $(CMD)"; \
	go build -trimpath -ldflags "$$ldflags" -o $(BIN_DIR)/$(BINARY)$$ext $(CMD)

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
