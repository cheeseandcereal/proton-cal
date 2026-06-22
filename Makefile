.PHONY: build lint fmt test cover integration clean

build:
	go build -o proton-cal ./cmd/proton-cal

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

# Integration tests hit the real Proton API; opt-in. See internal/integration.
# The live e2e tests live alongside the packages they exercise (the calsvc
# service layer, the MCP tool handlers, the CLI commands in-process) plus the
# original domain-layer suite under internal/integration.
integration:
	go test -tags integration -count=1 -v \
		./internal/integration/... \
		./internal/calsvc/... \
		./internal/mcpserver/... \
		./internal/cli/...

clean:
	rm -f proton-cal coverage.out coverage.html
