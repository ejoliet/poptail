# poptail — build, test, lint, cross-compile (README Build Order phases 0+4)

BINARY    := poptail
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64
DIST      := dist
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
# README acceptance criterion: binaries < 15 MB
MAX_BYTES := 15728640

.PHONY: build test lint cross checksums clean

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...
	golangci-lint run

# Release artifacts: dist/poptail_<os>_<arch>[.exe], stripped, version stamped,
# each checked against the 15 MB acceptance ceiling.
cross:
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)_$${os}_$${arch}$${ext}"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o "$$out" . || exit 1; \
		size=$$(wc -c < "$$out" | tr -d ' '); \
		if [ "$$size" -gt "$(MAX_BYTES)" ]; then \
			echo "FAIL $$out: $$size bytes > 15 MB ceiling"; exit 1; \
		fi; \
		echo "ok $$p ($$size bytes)"; \
	done

checksums: cross
	@cd $(DIST) && shasum -a 256 $(BINARY)_* > SHA256SUMS && cat SHA256SUMS

clean:
	rm -rf $(DIST)
