# Go toolchain is pinned: Go 1.25+ requires macOS 12 and will not run on the
# competition MacBook (mid-2014, Big Sur 11.7). See docs/architecture.md.
export GOTOOLCHAIN := go1.24.13

# Ubuntu 24.04 ships webkit2gtk 4.1 (not 4.0), hence the build tag.
TAGS := webkit2_41

.PHONY: dev build test race check fmt vet lint vuln frontend

dev:
	wails dev -tags $(TAGS)

build:
	wails build -tags $(TAGS)

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
