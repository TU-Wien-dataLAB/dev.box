# Official ContainerSSH agent v0.10.0 (the published image is amd64-only).
FROM containerssh/agent@sha256:a4cc5356ccd25df99b5ccf73a567dcb024601d3448b989154f9cf152cf4fbdbb AS agent

FROM --platform=$BUILDPLATFORM golang:1.26.7@sha256:e30143be198ab04cf7ba25fba83ab3a692ca584c994aad0bf131fa0eb32dd8c1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN test "$TARGETOS/$TARGETARCH" = "linux/amd64" || \
    (echo 'Official agent is amd64-only; build with --platform linux/amd64' >&2; exit 1)
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -mod=readonly -trimpath -ldflags='-s -w' -o /out/dev-box-settings .

FROM scratch
COPY --from=agent /usr/bin/containerssh-agent /usr/bin/containerssh-agent
COPY --from=build /out/dev-box-settings /usr/local/bin/dev-box-settings
COPY passwd /etc/passwd
COPY group /etc/group
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/dev-box-settings"]
