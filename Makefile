# poptail — build, test, lint, cross-compile (README Build Order phase 0)

BINARY    := poptail
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64
DIST      := dist

.PHONY: build test lint cross clean

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...
	golangci-lint run

# AIDEV-NOTE: until the v1 root package exists (phase 1+), cross is a per-platform
# compile check of all packages; phase 4 adds -o $(DIST)/$(BINARY)_$(GOOS)_$(GOARCH).
cross:
	@for p in $(PLATFORMS); do \
		GOOS=$${p%/*} GOARCH=$${p#*/} go build ./... || exit 1; \
		echo "ok $$p"; \
	done

clean:
	rm -rf $(DIST)
