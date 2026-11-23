# Build reproductible : même commit + même chaîne Go (voir go.mod) = même SHA-256.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS_BUILD = -trimpath -mod=readonly -buildvcs=true \
	-ldflags "-s -w -buildid= -X github.com/2guntan/diguee/internal/version.version=$(VERSION)"

export CGO_ENABLED = 0

.PHONY: build test vet verifier-build lancer
build:
	go build $(GOFLAGS_BUILD) -o bin/diguee ./cmd/diguee
	sha256sum bin/diguee

vet:
	go vet ./...

test:
	go test ./...

# Construit deux fois, chacune avec un cache de compilation vide, et compare
# les SHA-256.
verifier-build:
	rm -rf bin/repro && mkdir -p bin/repro/cache-a bin/repro/cache-b
	GOCACHE=$(CURDIR)/bin/repro/cache-a go build $(GOFLAGS_BUILD) -o bin/repro/a ./cmd/diguee
	GOCACHE=$(CURDIR)/bin/repro/cache-b go build $(GOFLAGS_BUILD) -o bin/repro/b ./cmd/diguee
	sha256sum bin/repro/a bin/repro/b
	cmp bin/repro/a bin/repro/b && echo "build reproductible : SHA-256 identiques"

lancer: build
	./bin/diguee
