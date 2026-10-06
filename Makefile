GO ?= go

.PHONY: all
all: fmt vet test

.PHONY: fmt
fmt: ## Format with gofumpt (the commit hook also runs the configured formatters).
	$(GO) tool gofumpt -w .

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: test
test: ## Unit tests. Nothing external needed.
	$(GO) test ./... -race -coverprofile=cover.out

.PHONY: build
build:
	$(GO) build ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: clean
clean:
	rm -f cover.out

.PHONY: check
check: vet test ## What CI runs.

.PHONY: help
help:
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
