# Throttle proxy

A lightweight Go reverse proxy that serializes requests to upstream servers using Earliest Deadline First (EDF) scheduling.

## Description

Throttle proxy is designed to protect upstream services from request floods by queuing and throttling incoming requests. Primary use cases include:

- **SearxNG**: Throttle requests to avoid overwhelming search backends
- **General**: Rate-limit requests to a site or specific endpoints

## Features

- Request throttling with configurable delay ranges
- EDF (Earliest Deadline First) scheduling for fair request distribution
- Round-robin passthrough for non-throttled endpoints
- Upstream health tracking with exponential backoff
- Thread-safe implementation
- Zero external dependencies
- Docker-ready with multi-stage build

## Quick Start

```bash
docker run -p 8080:8080 -e UPSTREAM=http://localhost:9000 ghcr.io/spaghetti-coder/throttle-proxy:latest
```

## Configuration

<details><summary>Env variables</summary>

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP server port |
| `UPSTREAM` | (required) | Comma-separated upstream URLs |
| `UPSTREAM_TIMEOUT` | `5` | Upstream timeout in seconds |
| `DELAY_MIN` | `0` | Minimum delay between requests (seconds) |
| `DELAY_MAX` | `0` | Maximum delay between requests (seconds) |
| `MAX_WAIT` | `0` | Maximum queue wait time (seconds, 0 = unlimited) |
| `ESCALATE_DELAY_AFTER` | `0` | Increase delay after N failures |
| `ESCALATE_DELAY_MAX_COUNT` | `3` | Maximum delay escalation count |
| `ENDPOINTS` | `/` | Comma-separated endpoint prefixes to throttle |
| `QUEUE_SIZE` | `10000` | Maximum queue size (min: 100) |
</details>

## Architecture

Throttle proxy sits between clients and upstream servers, queuing requests and dispatching them based on EDF scheduling. Requests matching configured endpoints are throttled; others pass through via round-robin.

See [spec.md](spec.md) for detailed architecture documentation.

## Installation

### Docker

Clone [`compose.yaml`](compose.yaml), edit and `docker compose up -d`.

<details><summary>Manual docker image build</summary>

```bash
docker build -t throttle-proxy:latest .
```

The image supports multi-platform builds (linux/amd64, linux/arm64) when using Docker Buildx:

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t throttle-proxy:latest .
```
</details>

### Go

```bash
git clone https://github.com/spaghetti-coder/throttle-proxy.git
cd throttle-proxy
go build -o throttle-proxy ./cmd/throttle-proxy
./throttle-proxy
```
