# Kube Workspaces desktop client — developer targets.
#
# Everything here is plain `go`; there is no code generation step and no
# container image (this repo ships desktop binaries, not a service).

BINARY  ?= kube-workspaces
CMD     ?= ./cmd/kube-workspaces
BIN_DIR ?= bin
DIST_DIR?= dist

# Passed through to `make run ARGS="..."`.
ARGS ?=

.PHONY: help build run test vet lint fmt tidy cover clean build-all

help: ## Show this help message
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the kube-workspaces binary into bin/
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) $(CMD)

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

# NOTE: these are pure-Go cross builds and they only work while the tree stays
# cgo-free. The session viewer (SDL3) and, later, H.264 decode (libavcodec) are
# cgo; those targets cannot be cross-compiled from one host and are built by CI
# on per-OS runners instead (see .github/workflows/ci.yml, the `cross` job).
# Keep this target as a fast smoke test of the portable packages, not as the
# release mechanism.
build-all: ## Cross-build the 6 targets into dist/ (pure-Go packages only)
	@mkdir -p $(DIST_DIR)
	@set -e; for target in \
		linux/amd64 linux/arm64 \
		darwin/amd64 darwin/arm64 \
		windows/amd64 windows/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -o $(DIST_DIR)/$(BINARY)-$$os-$$arch$$ext $(CMD); \
	done
