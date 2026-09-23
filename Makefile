VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo "")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
BIN     := bin/bambu
MODULE  := github.com/voska/bambu

TOOLS         := $(CURDIR)/.tools
GOFUMPT       := $(TOOLS)/gofumpt
GOIMPORTS     := $(TOOLS)/goimports
GOLANGCI_LINT := $(TOOLS)/golangci-lint

.PHONY: build install fmt fmt-check lint vet test cover ci tools live-test clean

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/bambu

install: build
	install -m 0755 $(BIN) $(shell go env GOPATH)/bin/bambu

fmt: tools
	@$(GOIMPORTS) -local $(MODULE) -w $$(git ls-files '*.go')
	@$(GOFUMPT) -w $$(git ls-files '*.go')

fmt-check: tools
	@$(GOIMPORTS) -local $(MODULE) -l $$(git ls-files '*.go') | tee /dev/stderr | (! read)
	@$(GOFUMPT) -l $$(git ls-files '*.go') | tee /dev/stderr | (! read)

lint: tools
	@$(GOLANGCI_LINT) run ./...

vet:
	go vet ./...

test:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

ci: fmt-check lint vet test build

tools: $(GOFUMPT) $(GOIMPORTS) $(GOLANGCI_LINT)

$(GOFUMPT):
	@GOBIN=$(TOOLS) go install mvdan.cc/gofumpt@v0.9.1
$(GOIMPORTS):
	@GOBIN=$(TOOLS) go install golang.org/x/tools/cmd/goimports@v0.38.0
$(GOLANGCI_LINT):
	@GOBIN=$(TOOLS) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2

# Read-only checks against a real printer (see scripts/live-test.sh).
live-test: build
	./scripts/live-test.sh

clean:
	rm -rf bin/ dist/ coverage.out
