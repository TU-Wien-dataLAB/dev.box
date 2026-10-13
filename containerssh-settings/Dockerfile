# Official v0.6 runtime below is linux/amd64 only; build with --platform=linux/amd64.
FROM --platform=$BUILDPLATFORM golang:1.25.6-bookworm@sha256:f4490d7b261d73af4543c46ac6597d7d101b6e1755bcdd8c5159fda7046b6b3e AS build
ENV GOTOOLCHAIN=local
WORKDIR /src
# v0.6.0 peeled tag, NOT the adjacent fork's HEAD. Verify the exact source archive.
RUN curl -fsSL --retry 3 https://codeload.github.com/ContainerSSH/ContainerSSH/tar.gz/76b43f44fce98315e7ab56c3db9d14248671a226 -o /tmp/upstream.tar.gz \
    && echo 'bb4b83b8f7517581e738a6c1d7a12d9f8656fdd69e985f780b514438e8cdbf96  /tmp/upstream.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/upstream.tar.gz --strip-components=1 -C /src \
    && rm /tmp/upstream.tar.gz
COPY gateway.patch verify.sh /patches/
# Each regression must fail on stock; all four fixes must pass (including race).
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    sh /patches/verify.sh
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    test "$TARGETOS/$TARGETARCH" = linux/amd64 \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -ldflags='-s -w' -o /out/containerssh ./cmd/containerssh

# Preserve upstream packages, user, ports, volumes, entrypoint and CMD. Only replace gateway.
FROM containerssh/containerssh:v0.6@sha256:7adf5995d6d43ecc95fe8225ee87e14d0f1996a03a890b3824d07f5c6a572d07
LABEL io.dev-box.containerssh.upstream-commit="76b43f44fce98315e7ab56c3db9d14248671a226" \
      io.dev-box.containerssh.patch="settings-issue-9"
COPY --from=build --chmod=0755 /out/containerssh /containerssh
