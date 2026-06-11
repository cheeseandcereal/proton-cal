.PHONY: build lint fmt test integration clean

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

# Integration tests hit the real Proton API; opt-in. See internal/integration.
integration:
	go test -tags integration -count=1 -v ./internal/integration/...

clean:
	rm -f proton-cal coverage.out coverage.html
