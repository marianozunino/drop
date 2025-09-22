FROM golang:1.24.1-bullseye AS builder

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        build-essential pkg-config libsqlite3-dev ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags="-s -w -extldflags '-static'" \
    -tags netgo \
    -installsuffix netgo \
    -o /app/drop ./cmd/drop

RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags="-s -w -extldflags '-static'" \
    -tags netgo \
    -installsuffix netgo \
    -o /app/migrate ./cmd/migrate

RUN chmod +x /app/drop /app/migrate

FROM alpine:3.19
ARG PORT=3000

WORKDIR /app

COPY --from=builder /app/drop /app/drop
COPY --from=builder /app/migrate /app/migrate
COPY --from=builder /app/internal/migration/migrations /app/internal/migration/migrations
COPY config/config.docker.yaml /app/config/config.yaml
COPY scripts/wrapper.sh /app/wrapper.sh

VOLUME ["/app/uploads", "/app/config", "/app/data"]
EXPOSE ${PORT}

CMD ["/app/wrapper.sh"]
