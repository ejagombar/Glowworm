BINARY := glowworm
VERSION ?= $(shell git describe --tags 2>/dev/null || echo dev)

.PHONY: build test vet lint fmt run release image clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/glowworm

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -l -w cmd internal web

run: build
	./$(BINARY) serve

# The binaries the release workflow publishes, with their checksums, in dist/.
release:
	mkdir -p dist
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
			-o dist/$(BINARY)-$$os-$$arch$$ext ./cmd/glowworm || exit 1; \
	done
	cd dist && sha256sum $(BINARY)-* > checksums.txt

image:
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t glowworm:$(VERSION) .

clean:
	rm -f $(BINARY) dist/$(BINARY)-* dist/checksums.txt