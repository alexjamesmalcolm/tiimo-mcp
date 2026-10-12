FROM golang:1.27.1-alpine AS build
WORKDIR /app
RUN apk add --no-cache ca-certificates build-base
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# SQLite is embedded into the binary; GCC is needed only in this build stage.
RUN CGO_ENABLED=1 go build -tags sqlite_omit_load_extension -buildvcs=false -trimpath -ldflags="-s -w" -o /tiimo-mcp ./cmd/tiimo-mcp

FROM alpine:3.23
RUN apk add --no-cache ca-certificates su-exec
COPY --from=build /tiimo-mcp /tiimo-mcp
COPY --chmod=0755 docker/entrypoint.sh /usr/local/bin/tiimo-entrypoint
# Root initializes the mounted directory, then entrypoint execs Go as UID/GID 65532.
USER 0:0
ENV AUTH_DB_PATH=/data/auth/auth.db
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tiimo-entrypoint"]
