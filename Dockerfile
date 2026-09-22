# syntax=docker/dockerfile:1

# Multi-stage build for pii-service.
#
# Stage 1 builds a static binary. The Go toolchain version is pinned to the
# version declared in go.mod (1.27.1) and the image digest is fixed so the
# build is reproducible.
FROM golang:1.27.1@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder

WORKDIR /src

# Copy module files first to leverage the layer cache for dependencies.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build a static binary. CGO is disabled so
# the binary has no dynamic library dependencies and runs on a minimal image.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pii-service ./cmd/pii-service

# Stage 2 is the minimal runtime image. It contains only the executable and
# the static data the service needs. The image runs as the distroless non-root
# user (UID 65532) by default; no shell or package manager is present.
#
# Configuration and secrets are mounted at runtime and never baked into the
# image. The service performs no file writes, so the root filesystem is mounted
# read-only at runtime; a writable /tmp is provided for the Go runtime.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=builder /out/pii-service /pii-service

# The service listens on the port configured by PII_LISTEN_ADDR. The default
# is 8080; the port is exposed for documentation and host mapping.
EXPOSE 8080

USER 65532:65532

ENTRYPOINT ["/pii-service"]