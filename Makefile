all: lint build test ## Run lint, build, and tests

.PHONY: help
help: ## Show this help message
	@awk 'BEGIN { FS = ":.*##[[:space:]]*"; printf "Usage: make <target>\n\nTargets:\n" } /^[a-zA-Z0-9_-]+:.*##/ { sub(/^[[:space:]]+/, "", $$2); printf "  %-12s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build all Go packages
	go build ./...

install: ## Download modules and install analysis tools
	bash scripts/install.sh

lint: fmt vet staticcheck misspell ## Run formatting, vet, staticcheck, and misspell

fmt: ## Format Go source files
	bash scripts/gofmt.sh

vet: ## Run go vet
	go vet ./check ./cmd/... ./download ./handlers ./tools/...
	go vet ./main.go

staticcheck: ## Run staticcheck
	go tool staticcheck ./...

test: ## Run tests and enforce coverage threshold
	bash scripts/test.sh

start: ## Start the web application
	 go run main.go

misspell: ## Check Go source spelling
	find . -name '*.go' -not -path './_repos/*' -not -path './download/test_downloads/*' -not -path './check/testdata/*' | xargs go tool misspell -error
