BINARY  := bin/indexarr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt run clean qa def-validate e2e ci docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/indexarr

# Release image with the version baked in. The new rule requires a Docker
# image at every release so requests and workloads can be tested in a
# container, not only on the host binary.
docker:
	docker build --build-arg VERSION=$(VERSION) -t indexarr:$(VERSION) .

test:
	go test ./...

vet:
	go vet ./...

# QA: static checks, unit/integration tests and a real-binary HTTP smoke test.
qa:
	./scripts/qa.sh

# Local CI: the same jobs as .github/workflows/qa.yml plus Windows/macOS
# test compilation. Writes ci-logs/<commit>.log.
ci:
	./scripts/ci-local.sh

# UI route/link smoke test against a running instance (read-only, GET only).
.PHONY: ui-smoke
ui-smoke:
	./scripts/ui-smoke.sh

# Field-by-field browser test: temporary database, temporary port and local
# fixtures for FlareSolverr, qBittorrent and Sonarr. Skips cleanly when no
# Chrome/Chromium is installed; UI_E2E_REQUIRE_CHROME=1 makes that a failure.
e2e:
	./scripts/ui-e2e.py

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

# Regenerate the compact site index read by the "Add indexer" page. The
# catalog test fails when index.json is stale, so run this after every
# definition or manifest change.
.PHONY: catalog-index
catalog-index:
	go generate ./internal/catalog

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
