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
