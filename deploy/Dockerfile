# Glowworm's container image: ghcr.io/ejagombar/glowworm, built for amd64
# and arm64 by the release workflow. To build it yourself, from the
# repository root:
#   docker build -f deploy/Dockerfile --build-arg VERSION=$(git describe --tags) -t glowworm .
# The Go build runs on the builder's own platform and cross-compiles, so a
# multi-arch build needs no emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" -o /out/glowworm ./cmd/glowworm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/glowworm /glowworm
# Text logs, so `docker logs` shows the setup link and its QR code on the
# first start. Set GLOWWORM_LOG_FORMAT=json for a log collector.
ENV GLOWWORM_ADDR=0.0.0.0:8080 \
    GLOWWORM_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot
HEALTHCHECK --interval=30s --timeout=5s CMD ["/glowworm", "healthcheck"]
ENTRYPOINT ["/glowworm"]
CMD ["serve"]
