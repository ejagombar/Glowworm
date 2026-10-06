BINARY := glowworm
VERSION ?= $(shell git describe --tags 2>/dev/null || echo dev)

.PHONY: build test vet fmt run release clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/glowworm

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal web

run: build
	./$(BINARY) serve

release: build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY)-linux-amd64 ./cmd/glowworm
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY)-linux-arm64 ./cmd/glowworm
	docker build -f deploy/Dockerfile -t glowworm:$(VERSION) .

clean:
	rm -f $(BINARY) $(BINARY)-linux-*