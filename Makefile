# Go toolchain is pinned: Go 1.25+ requires macOS 12 and will not run on the
# competition MacBook (mid-2014, Big Sur 11.7). See docs/architecture.md.
export GOTOOLCHAIN := go1.24.13

# Ubuntu 24.04 ships webkit2gtk 4.1 (not 4.0), hence the build tag.
TAGS := webkit2_41

# Build identity, injected via -ldflags. Version is a semver in the VERSION file;
# build is the git commit count (monotonic, works the same locally and in CI).
VPKG       := gitlab.com/fightmaster1/chrono-desk/internal/version
VERSION    := $(shell cat VERSION 2>/dev/null || echo dev)
BUILD      := $(shell git rev-list --count HEAD 2>/dev/null || echo 0)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -X $(VPKG).Semver=$(VERSION) -X $(VPKG).Build=$(BUILD) -X $(VPKG).Commit=$(COMMIT) -X $(VPKG).Date=$(BUILD_DATE)

.PHONY: dev build test race check fmt vet lint vuln frontend

dev:
	wails dev -tags $(TAGS) -ldflags "$(LDFLAGS)"

build:
	wails build -tags $(TAGS) -ldflags "$(LDFLAGS)"

frontend:
	cd frontend && npm install && npm run build

test:
	go test ./...

race:
	go test -race ./...

fmt:
	gofmt -l .

vet:
	go vet ./...

lint:
	staticcheck ./...

vuln:
	govulncheck ./...

# Full quality gate (same as rfid-hub / rfid-sync)
check: fmt vet lint vuln test
