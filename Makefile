BINARY := grocery
PKG := ./cmd/grocery

.PHONY: build test vet fmt check clean

build:
	go build -o $(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

check: fmt vet test build

clean:
	rm -f $(BINARY)
