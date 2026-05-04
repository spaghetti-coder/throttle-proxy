# throttle-proxy

A lightweight Go reverse proxy that protects upstream services from request floods by serializing and throttling requests.

It sits between clients and upstream servers, ensuring that at most one request is in-flight at any given time per upstream. Requests are queued and dispatched using Earliest Deadline First (EDF) scheduling with configurable, randomized delays. Under sustained high-frequency load, delays automatically escalate to avoid triggering upstream rate limits.

## Table of Contents

- [When to use](#when-to-use)
- [Features](#features)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
  - [Environment variables](#environment-variables)
  - [MCP tool timeouts for AI agents](#mcp-tool-timeouts-for-ai-agents)
  - [Example: SearxNG with conservative delays](#example-searxng-with-conservative-delays)
- [How it works](#how-it-works)
- [Installation](#installation)
  - [Docker](#docker)
  - [Go](#go)
- [Testing](#testing)

## When to use

- **AI agent integrations**: Your agent can issue dozens parallel `curl` requests to a meta-search engine (e.g., [SearxNG](https://docs.searxng.org/)). The backend proxies them to Google, Bing, DuckDuckGo, which detect simultaneous requests and ban your IP. `throttle-proxy` serializes these requests, adds human-like jitter between them, and gradually backs off if the load pattern persists.
- **Scraping protection**: You need to enforce a strict "one request at a time" policy to a fragile or rate-limited API, with automatic delay escalation when the upstream signals it is unhappy.
- **General rate-limiting**: Any scenario where you want to queue and throttle requests to specific endpoints while letting others pass through unaffected.

## Features

- **Request serialization**: At most one in-flight request per upstream at any time
- **EDF scheduling**: Earliest Deadline First dispatch across multiple upstreams
- **Automatic delay escalation**: Gradually increases delays under sustained high-frequency load
- **Round-robin passthrough**: Non-throttled endpoints bypass the queue entirely
- **Upstream health tracking**: Automatic failover and exponential backoff on 5xx/timeout
- **Thread-safe queue**: Configurable max size with wait-time limits
- **Zero dependencies**: Pure Go 1.24, ~single binary
- **Docker-ready**: Multi-stage build, multi-platform (`linux/amd64`, `linux/arm64`)

## Quick Start

```bash
docker run -p 8080:8080 \
  -e UPSTREAM=http://localhost:9000 \
  ghcr.io/spaghetti-coder/throttle-proxy:latest
```

For persistent configuration, copy [`compose.yaml`](compose.yaml) to your project, edit the environment variables, and run:

```bash
docker compose up -d
```

## Configuration

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `UPSTREAM` | *(required)* | Comma-separated upstream URLs (`http://host:port`) |
| `PORT` | `8080` | HTTP server port |
| `UPSTREAM_TIMEOUT` | `5` | Upstream fetch timeout (seconds) |
| `DELAY` | `0` | Delay between requests (seconds). Supports `min:max` range: `DELAY=0.5:2`. If max < min, max = min |
| `MAX_WAIT` | `0` | Max time a request can wait in queue — returns 503 if exceeded (`0` = unlimited) |
| `ESCALATE_AFTER` | `0` | Trigger delay escalation after this many requests. Must be `0` (disabled) or `>= 2` |
| `ESCALATE_MAX_COUNT` | `3` | Maximum delay escalation steps (`0` = unlimited) |
| `ESCALATE_FACTOR` | `1.5:2.0` | Delay multiplier on each escalation step. Supports constant (`1.5`) or range (`1.5:2.0`) |
| `ENDPOINTS` | `/` | Comma-separated endpoint prefixes to throttle. Prefix-matched: `/search` matches `/search` and `/search/foo`, but not `/searches` |
| `QUEUE_SIZE` | `100` | Max queue size. Minimum: `1`; values below `1` are raised to `1`. Special: `0` = `100` |

### MCP tool timeouts for AI agents

Default MCP tool call timeouts in AI agents (typically 30–120 s) may be too short when the proxy queue backs up. Depending on `QUEUE_SIZE`, `DELAY`, and escalation settings, requests can wait well beyond the agent's default. Increase the timeout in your agent's configuration file to prevent disconnects:

| Agent | Config key | File |
|-------|------------|------|
| **OpenCode** | `experimental.mcp_timeout` | `opencode.jsonc` |
| **Hermes** | `mcp_servers.timeout` | `config.yaml` |
| **Claude Code** | `env.MCP_TOOL_TIMEOUT` | `.claude.json` |

### Example: SearxNG with conservative delays

```bash
docker run -p 8080:8080 \
  -e UPSTREAM=http://searxng:8080 \
  -e DELAY=5:9 \
  -e ESCALATE_AFTER=3 \
  -e ESCALATE_MAX_COUNT=6 \
  -e ENDPOINTS=/search,/images \
  ghcr.io/spaghetti-coder/throttle-proxy:latest
```

In this setup:
- Each upstream waits 5–9 seconds between requests.
- If 3 requests arrive within a tight window, delays escalate by `ESCALATE_FACTOR`.
- The escalation can repeat up to 6 times before capping.
- Only `/search` and `/images` are throttled; everything else passes straight through.

## How it works

1. **Queue**: Incoming requests matching `ENDPOINTS` are placed in a thread-safe queue.
2. **EDF dispatch**: For multiple upstreams, the proxy picks the one with the earliest available deadline, waits if necessary, then forwards the request.
3. **Per-upstream state**: Each upstream tracks its own `next_min_ts` and escalation window independently.
4. **Failover**: If an upstream times out or returns 5xx, the request is retried on the next available upstream.
5. **Escalation**: When sustained load is detected (configured via `ESCALATE_AFTER`), delays increase multiplicatively (by `ESCALATE_FACTOR`) to stay under upstream radar.
6. **Passthrough**: Requests that do not match any `ENDPOINTS` prefix skip the queue and are forwarded immediately via round-robin.

For the full algorithmic specification — including exact escalation formulas, sliding window semantics, and multi-upstream retry logic — see [`spec.md`](spec.md).

## Installation

### Docker

Pull the latest image:

```bash
docker pull ghcr.io/spaghetti-coder/throttle-proxy:latest
```

Or build manually:

```bash
docker build -t throttle-proxy:latest .
```

Multi-platform build with Docker Buildx:

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t throttle-proxy:latest .
```

### Go

Requires Go 1.24+.

```bash
git clone https://github.com/spaghetti-coder/throttle-proxy.git
cd throttle-proxy
go build -o throttle-proxy ./cmd/throttle-proxy
./throttle-proxy
```

## Testing

```bash
go test -count 1 ./...
```

Integration tests cover sequential processing, upstream failover, endpoint matching, and round-robin passthrough:

```bash
go test -count 1 ./integration/...
```
