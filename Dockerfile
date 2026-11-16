# syntax=docker/dockerfile:1

# Build stage: the whole module is compiled into one static binary. Migrations
# and demo assets are embedded with //go:embed, so the runtime image does not
# need the source tree.
FROM golang:1.25.1-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Runtime stage: alpine keeps ca-certificates for HTTPS model calls. Running as
# a non-root user is the minimum for a service that reads request bodies.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app \
    && adduser -S -G app -u 10001 app
COPY --from=build /out/server /usr/local/bin/server
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
