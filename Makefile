BIN       ?= bin/diffah
PKG       := github.com/leosocy/diffah
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X $(PKG)/cmd.version=$(VERSION)
GOFLAGS   := -trimpath
TAGS      := containers_image_openpgp

.PHONY: build test test-integration lint fmt fixtures snapshot clean

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -tags '$(TAGS)' -ldflags '$(LDFLAGS)' -o $(BIN) .

test:
	go test $(GOFLAGS) -tags '$(TAGS)' -race -cover ./...

test-integration:
	go test $(GOFLAGS) -tags 'integration $(TAGS)' -race -cover ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .
	goimports -w -local $(PKG) .

fixtures:
	go run -tags '$(TAGS)' ./scripts/build_fixtures

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin/ dist/
