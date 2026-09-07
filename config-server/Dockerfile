# Build stage
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/config-server .

# Runtime stage
FROM alpine:3.21
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/config-server /usr/local/bin/config-server
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/config-server"]
