GO ?= go
VERSION ?= dev
BUILD_DIR ?= dist
MODULE := github.com/shiqkuangsan/cursor-cli-byok
MAIN_PACKAGE := ./cmd/cursor-cli-byok
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION)
BUILD_FLAGS := -buildvcs=false -trimpath -ldflags "$(LDFLAGS)"
LINUX_AMD64 := $(BUILD_DIR)/cursor-cli-byok-linux-amd64
LINUX_ARM64 := $(BUILD_DIR)/cursor-cli-byok-linux-arm64

.PHONY: all build clean cross-build checksums release fmt fmt-check test race vet shell-test e2e linux-e2e verify

all: build

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o $(BUILD_DIR)/cursor-cli-byok $(MAIN_PACKAGE)

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

test:
	$(GO) test -p 1 ./... -count=1

race:
	$(GO) test -race -p 1 ./... -count=1

vet:
	$(GO) vet ./...

shell-test:
	./scripts/install_test.sh
	bash test/e2e/version_test.sh
	bash -n test/e2e/run.sh test/e2e/version.sh test/e2e/version_test.sh test/linux-smoke/run.sh
	sh -n scripts/install.sh test/linux-smoke/fake_cursor_agent.sh

cross-build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(LINUX_AMD64) $(MAIN_PACKAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -o $(LINUX_ARM64) $(MAIN_PACKAGE)

checksums: cross-build
	@cd $(BUILD_DIR) && \
	if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum cursor-cli-byok-linux-amd64 cursor-cli-byok-linux-arm64 > checksums.txt; \
	else \
		shasum -a 256 cursor-cli-byok-linux-amd64 cursor-cli-byok-linux-arm64 > checksums.txt; \
	fi

release:
	@test "$(VERSION)" != dev || { echo 'VERSION must be a release tag' >&2; exit 1; }
	$(MAKE) clean
	$(MAKE) checksums VERSION=$(VERSION)

e2e:
	./test/e2e/run.sh

linux-e2e:
	E2E_REQUIRE_LINUX=1 ./test/e2e/run.sh

verify: fmt-check test race vet shell-test cross-build

clean:
	@test ! -d $(BUILD_DIR) || rm -r $(BUILD_DIR)
