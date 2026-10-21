# syntax=docker/dockerfile:1

# Cross-compile on the build machine's platform instead of emulating the target.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
# modernc.org/sqlite is pure Go, so the binary is fully static.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/ledger-snap ./cmd/ledger-snap
# The runtime image has no shell; create the data directory here.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/ledger-snap /app/ledger-snap
COPY --from=build --chown=nonroot:nonroot /out/data /app/data
COPY config.toml /app/config.toml
EXPOSE 8080
VOLUME ["/app/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/ledger-snap", "healthcheck"]
ENTRYPOINT ["/app/ledger-snap"]
CMD ["serve", "--config", "/app/config.toml"]
