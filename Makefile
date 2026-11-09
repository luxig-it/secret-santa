# Local development is driven entirely through this file so that the devcontainer,
# Codespaces and CI all run the same commands.

# Load .env if present and hand every entry to the child processes.
-include .env
export

BINARY := secret-santa
IMAGE  ?= ghcr.io/luxig-it/secret-santa:dev

.PHONY: help generate run watch test vet build image clean

help:
	@grep -E '^[a-z-]+:.*?##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/' | expand -t24

generate: ## Compile .templ files to Go
	go tool templ generate

run: generate ## Start the server once
	go run ./cmd/$(BINARY)

watch: ## Regenerate templates and reload the browser on every save
	SECRET_SANTA_ADDR=:8081 go tool templ generate -watch \
		-proxy="http://localhost:8081" -proxyport=8080 -open-browser=false \
		-cmd="go run ./cmd/$(BINARY)"

test: generate ## Run the test suite
	go test ./...

vet: generate ## Static checks
	go vet ./...

build: generate ## Build a static binary into ./dist
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/$(BINARY) ./cmd/$(BINARY)

image: ## Build the production container image
	docker build -t $(IMAGE) .

clean:
	rm -rf dist
