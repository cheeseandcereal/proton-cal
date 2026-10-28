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

# Account-wide mutations and the cleanup sweep need exclusive access.
INTEGRATION_SERIAL := ^(TestE2EUpdateCalendar|TestE2ECachedDefaultRecovery|TestCreateCalendarLifecycle|TestConcurrentSessionRefresh|TestZSweep)$$

# Run independent packages in parallel, then isolated tests with the sweep last.
integration:
	@status=0; \
	go test -tags integration -count=1 -v -skip '$(INTEGRATION_SERIAL)' \
		./pkg/calsvc/... \
		./pkg/mcpserver/... \
		./pkg/cli/... \
		./pkg/integration/... || status=1; \
	go test -p 1 -tags integration -count=1 -v -run '$(INTEGRATION_SERIAL)' \
		./pkg/calsvc/... \
		./pkg/integration/... || status=1; \
	exit $$status

clean:
	rm -f proton-cal coverage.out coverage.html
