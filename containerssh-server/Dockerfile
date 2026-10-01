# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# All build/runtime inputs are pinned; module versions and hashes come from the
# upstream go.mod/go.sum. Cross-compile natively even on an arm64 build host.
FROM --platform=$BUILDPLATFORM golang:1.25.3-alpine@sha256:aee43c3ccbf24fdffb7295693b6e33b21e01baec1b2a55acc351fde345e9ec34 AS build
ARG TARGETARCH
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY source/go.mod source/go.sum ./
RUN go mod download && go mod verify
COPY source/ ./
RUN GOOS=linux GOARCH=$TARGETARCH go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags='-s -w -buildid=' -o /out/containerssh ./cmd/containerssh

# Preserve stock v0.6's user, CA certificates, entrypoint, and filesystem layout.
# This stock manifest is linux/amd64; build this image for linux/amd64 only.
FROM containerssh/containerssh:v0.6@sha256:7adf5995d6d43ecc95fe8225ee87e14d0f1996a03a890b3824d07f5c6a572d07
ARG PATCH_SHA256
LABEL org.opencontainers.image.source="https://github.com/TU-Wien-dataLAB/dev.box" \
      org.opencontainers.image.revision="76b43f44fce98315e7ab56c3db9d14248671a226" \
      io.dev-box.containerssh.patch.sha256="$PATCH_SHA256"
COPY --from=build --chmod=755 /out/containerssh /containerssh
