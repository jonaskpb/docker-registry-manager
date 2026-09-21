BINARY   := drm
PKG      := ./cmd/drm
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

# CGO is off everywhere so every build is a single static binary with no
# runtime dependencies.
export CGO_ENABLED := 0

# Platforms built by `make release`.
PLATFORMS := linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build install test race vet fmt check clean release

all: check build

build: ## Build the binary for the host platform
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

install: ## Install the binary into GOBIN
	go install -trimpath -ldflags "$(LDFLAGS)" $(PKG)

test: ## Run the test suite
	go test ./...

race: ## Run the test suite under the race detector
	CGO_ENABLED=1 go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the source
	gofmt -l -w .

check: vet test ## Vet and test

clean:
	rm -rf $(BINARY) dist/

release: ## Cross-compile a static binary for every supported platform
	@rm -rf dist && mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=dist/$(BINARY)-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out $(PKG) || exit 1; \
	done
	@cd dist && sha256sum * > SHA256SUMS
	@ls -lh dist/
