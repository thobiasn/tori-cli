FROM golang:1.26-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tori ./cmd/tori

FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11
RUN apk add --no-cache ca-certificates
COPY --from=build /tori /usr/local/bin/tori
COPY deploy/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
CMD ["tori", "agent", "--config", "/etc/tori/config.toml"]
