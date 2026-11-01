# throttle-proxy

A lightweight Go reverse proxy that protects upstream services from request floods by serializing and throttling requests.

It sits between clients and upstream servers: at most one request is in-flight per upstream. Requests are queued and dispatched by earliest availability with randomized delays, which escalate under sustained load to stay clear of upstream rate limits.

## Table of Contents

- [When to use](#when-to-use)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
  - [Environment variables](#environment-variables)
  - [MCP tool timeouts for AI agents](#mcp-tool-timeouts-for-ai-agents)
  - [Example: SearxNG with conservative delays](#example-searxng-with-conservative-delays)
- [How it works](#how-it-works)
- [Installation](#installation)
  - [Docker](#docker)
  - [Go](#go)

## When to use

- **AI agents**: an agent firing dozens of parallel `curl` calls at a meta-search engine (e.g. [SearxNG](https://docs.searxng.org/)) gets its IP banned, because the engine sees simultaneous requests. Serialization plus randomized delays fix that.
- **Scraping**: enforce "one request at a time" against a fragile or rate-limited API.
- **Selective throttling**: throttle the endpoints you choose, pass everything else straight through.

## Quick Start

```bash
docker run -p 8080:8080 \
  -e UPSTREAM=http://host.docker.internal:9000 \
  ghcr.io/spaghetti-coder/throttle-proxy:latest
```

`localhost` inside the container is the container itself; use `host.docker.internal` (or `--network host` on Linux) to reach an upstream running on the host.

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

When the queue backs up, a throttled call can outlive the agent's default MCP tool timeout. Raise it in the agent config:

| Agent           | Config key                 | Suggested | File              |
|-----------------|----------------------------|-----------|-------------------|
| **OpenCode**    | `experimental.mcp_timeout` | `310000`  | `opencode.jsonc`  |
| **Hermes**      | `mcp_servers.timeout`      | `310000`  | `config.yaml`     |
| **Claude Code** | `env.MCP_TOOL_TIMEOUT`     | `310000`  | `.claude.json`    |

`310000` ms = 5 min plus margin: that is when most MCP servers give up on a tool call.

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
- Each upstream gets a 5–9 second gap between requests.
- 3 requests in a short window multiply the delays by `ESCALATE_FACTOR`, up to 6 times.
- Only `/search` and `/images` are throttled; everything else bypasses the queue.

## How it works

1. **Queue**: Incoming requests matching `ENDPOINTS` are placed in a thread-safe queue.
2. **EDF dispatch**: Earliest Deadline First — with several upstreams the proxy picks the one that becomes available soonest, waits if necessary, then forwards.
3. **Per-upstream state**: each upstream keeps its own schedule and escalation state, so one banned engine does not slow the others.
4. **Failover**: If an upstream times out or returns 5xx, the request is retried on the next available upstream.
5. **Escalation**: When sustained load is detected (configured via `ESCALATE_AFTER`), delays increase multiplicatively (by `ESCALATE_FACTOR`).
6. **Passthrough**: Requests that do not match any `ENDPOINTS` prefix skip the queue and are forwarded immediately via round-robin.

Full algorithm details: [`spec.md`](spec.md).

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

Requires Go 1.24+. No external dependencies.

```bash
git clone https://github.com/spaghetti-coder/throttle-proxy.git
cd throttle-proxy
go build -o throttle-proxy ./cmd/throttle-proxy
./throttle-proxy
```
