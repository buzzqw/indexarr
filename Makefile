BINARY  := bin/indexarr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt run clean qa def-validate

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/indexarr

test:
	go test ./...

vet:
	go vet ./...

# QA: static checks, unit/integration tests and a real-binary HTTP smoke test.
qa:
	./scripts/qa.sh

# UI route/link smoke test against a running instance (read-only, GET only).
.PHONY: ui-smoke
ui-smoke:
	./scripts/ui-smoke.sh

fmt:
	gofmt -w .

run:
	go run ./cmd/indexarr

clean:
	rm -rf bin

# Validate the example definitions and the bundled catalog. The catalog pass
# rejects derived drafts (meta.derived_from or the cardigann-import author):
# converted Cardigann files are writing aids and must never ship in the catalog.
def-validate:
	go run ./cmd/indexarr-def validate examples/definitions
	go run ./cmd/indexarr-def validate -catalog internal/catalog/definitions

# Resource budget: optimised binary size plus idle RSS on a seeded database.
.PHONY: budget
budget:
	./scripts/budget.sh

# Cross-compile every supported GOOS/GOARCH pair with CGO disabled.
.PHONY: build-matrix
build-matrix:
	./scripts/build-matrix.sh

# Cross-compiled standalone binaries (the SQLite driver is pure Go).
.PHONY: release
release:
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64              go build -trimpath -ldflags "$(LDFLAGS)" -o bin/indexarr-linux-amd64 ./cmd/indexarr
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64              go build -trimpath -ldflags "$(LDFLAGS)" -o bin/indexarr-linux-arm64 ./cmd/indexarr
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64             go build -trimpath -ldflags "$(LDFLAGS)" -o bin/indexarr-windows-amd64.exe ./cmd/indexarr
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64              go build -trimpath -ldflags "$(LDFLAGS)" -o bin/indexarr-darwin-arm64 ./cmd/indexarr
