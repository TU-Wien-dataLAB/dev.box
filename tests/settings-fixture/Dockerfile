# Match the pinned Go build image used by settings-tui/Dockerfile.
FROM --platform=$BUILDPLATFORM golang:1.26.7@sha256:e30143be198ab04cf7ba25fba83ab3a692ca584c994aad0bf131fa0eb32dd8c1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod main.go main_test.go ./
# BuildKit's build stage is disposable; never run these process tests on a host.
RUN SETTINGS_FIXTURE_DISPOSABLE_LINUX=1 go test -race -count=1 ./... && go vet ./...
RUN test "$TARGETOS/$TARGETARCH" = "linux/amd64" || \
    (echo 'Settings fixture is amd64-only; use --platform linux/amd64' >&2; exit 1)
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -mod=readonly -trimpath -ldflags='-s -w' -o /out/settings-acceptance-helper .

# Inherit the original nonroot USER and TUI ENTRYPOINT unchanged.
FROM ghcr.io/tu-wien-datalab/dev.box/settings-tui@sha256:addad96cb660725389df3cf602d5b183db6864c15e60ae1766f6d768cf614252
COPY --from=build /out/settings-acceptance-helper /usr/local/bin/settings-acceptance-helper
