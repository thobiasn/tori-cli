FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tori ./cmd/tori

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /tori /usr/local/bin/tori
COPY deploy/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
CMD ["tori", "agent", "--config", "/etc/tori/config.toml"]
