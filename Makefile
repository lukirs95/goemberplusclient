# Builds and tests the library and emberplus-cli.
#
#   make test               vet and test both modules with the race detector
#   make build              build emberplus-cli for this machine into bin/
#   make dist               build release archives for all PLATFORMS into dist/
#   make dist VERSION=v1.0  the same with an explicit version
#
# Works with GNU Make 3.81 (macOS) and newer.

CLI       := emberplus-cli
CLI_DIR   := cmd/$(CLI)
BIN_DIR   := bin
DIST_DIR  := dist
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

GO        ?= go
LDFLAGS   := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help
.PHONY: help check test fuzz build dist clean

help:
	@echo "Targets:"
	@echo "  check   gofmt and go vet for both modules"
	@echo "  test    check, then run all tests with the race detector"
	@echo "  fuzz    run each fuzz test for FUZZTIME (default 30s)"
	@echo "  build   build $(CLI) for this machine into $(BIN_DIR)/"
	@echo "  dist    build release archives for $(PLATFORMS) into $(DIST_DIR)/"
	@echo "  clean   remove $(BIN_DIR)/ and $(DIST_DIR)/"
	@echo ""
	@echo "Version: $(VERSION)"

check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...
	cd $(CLI_DIR) && $(GO) vet ./...

test: check
	$(GO) test -race ./...
	cd $(CLI_DIR) && $(GO) test -race ./...

FUZZTIME ?= 30s
fuzz:
	$(GO) test -run '^$$' -fuzz '^FuzzReader$$' -fuzztime $(FUZZTIME) ./pkg/ber
	$(GO) test -run '^$$' -fuzz '^FuzzDecode$$' -fuzztime $(FUZZTIME) ./pkg/glow

build:
	cd $(CLI_DIR) && CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../../$(BIN_DIR)/$(CLI)$(shell $(GO) env GOEXE) .

# dist builds one archive per platform: tar.gz for Linux and macOS, zip for
# Windows, each containing the binary, README and LICENSE, plus a file with
# SHA-256 checksums.
dist: clean-dist
	@mkdir -p $(DIST_DIR)
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		name=$(CLI)_$(VERSION)_$${os}_$${arch}; \
		ext=""; if [ "$$os" = windows ]; then ext=.exe; fi; \
		echo "building $$name"; \
		mkdir -p $(DIST_DIR)/$$name; \
		(cd $(CLI_DIR) && CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../../$(DIST_DIR)/$$name/$(CLI)$$ext .); \
		cp README.md LICENSE $(DIST_DIR)/$$name/; \
		if [ "$$os" = windows ]; then \
			(cd $(DIST_DIR) && zip -qr $$name.zip $$name); \
		else \
			tar -C $(DIST_DIR) -czf $(DIST_DIR)/$$name.tar.gz $$name; \
		fi; \
		rm -r $(DIST_DIR)/$$name; \
	done
	@cd $(DIST_DIR) && if command -v sha256sum >/dev/null; then \
		sha256sum *.tar.gz *.zip > checksums.txt; \
	else \
		shasum -a 256 *.tar.gz *.zip > checksums.txt; \
	fi
	@ls -1 $(DIST_DIR)

.PHONY: clean-dist
clean-dist:
	rm -rf $(DIST_DIR)

clean: clean-dist
	rm -rf $(BIN_DIR)
