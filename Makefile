.PHONY: build lint fmt test cover integration clean

# Stamp the binary with the short commit id, suffixed -dirty when the working
# tree has uncommitted changes (tracked, staged or untracked); "dev" outside a
# git checkout. Append the UTC build date so dev builds are self-identifying.
GIT_REV := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
DIRTY := $(shell test -n "$$(git status --porcelain 2>/dev/null)" && echo -dirty)
BUILD_DATE := $(shell date -u +%Y-%m-%d)
VERSION := $(GIT_REV)$(DIRTY) ($(BUILD_DATE))
LDFLAGS := -X 'github.com/cheeseandcereal/proton-cal/pkg/cli.version=$(VERSION)'

build:
	go build -ldflags "$(LDFLAGS)" -o proton-cal ./cmd/proton-cal

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:" && gofmt -l . && exit 1)
	go vet ./...
	@command -v golangci-lint >/dev/null 2>&1 || (echo "golangci-lint not installed; see https://golangci-lint.run/welcome/install/" && exit 1)
	golangci-lint run

fmt:
	gofmt -w .

test:
	go test ./...

# Unit-test coverage report (excludes the opt-in live integration tests).
cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@echo "HTML: go tool cover -html=coverage.out"

# Integration tests hit the real Proton API; opt-in. See pkg/integration.
# The live e2e tests live alongside the packages they exercise (the calsvc
# service layer, the MCP tool handlers, the CLI commands in-process) plus the
# original domain-layer suite under pkg/integration.
integration:
	go test -tags integration -count=1 -v \
		./pkg/integration/... \
		./pkg/calsvc/... \
		./pkg/mcpserver/... \
		./pkg/cli/...

clean:
	rm -f proton-cal coverage.out coverage.html
