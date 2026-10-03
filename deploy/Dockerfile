# Glowworm — multi-stage release build (doc 13 §5)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags 2>/dev/null || echo release)" -o /out/glowworm ./cmd/glowworm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/glowworm /glowworm
ENV GLOWWORM_ADDR=0.0.0.0:8080 \
    GLOWWORM_DATA_DIR=/data \
    GLOWWORM_LOG_FORMAT=json
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/glowworm"]
