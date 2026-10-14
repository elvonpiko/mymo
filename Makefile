BINARY := mymo

.PHONY: build test vet clean

build:
	go build -o $(BINARY) ./cmd/mymo

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

VERSION := $(shell sed -n 's/.*Version = "\(.*\)"/\1/p' internal/version/version.go)

.PHONY: release clean-dist

# release builds every supported platform from one tree, packages
# each with its license and readme, and writes the checksums the
# installers verify against
release:
	VERSION=$(VERSION) bash scripts/release.sh

clean-dist:
	rm -rf dist
