FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod .
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /throttle-proxy ./cmd/throttle-proxy

FROM scratch
COPY --from=builder /throttle-proxy /throttle-proxy
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENTRYPOINT ["/throttle-proxy"]
