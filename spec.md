# Throttle proxy

## Issue

SearxNG is a metasearch engine using google, duckduckgo, etc. search engines for aggregation search results.

The problem is underlying search engines detect and ban simultaneous requests, which is a problem for using SearxNG with AI agents, because they often run multiple requests in parallel.

## Solution

Develop throttle proxy that will stay between AI agents and SearxNG server. At most one request is in-flight at any given time per upstream.

The throttle proxy can also be used for other scenarios, not related with SearxNG.

## Technologies stack

docker, docker compose, golang

## Requests queue

The requests queue stores requests and ensures their sequential processing with configured randomized delay. The queue size is configurable via the QUEUE_SIZE environment variable:
- Default: 100
- Minimum: 1 (values below 1 are raised to 1)
- Special case: QUEUE_SIZE=0 is treated as 100
Incoming requests are accepted until the queue reaches its configured limit.

`MAX_WAIT` (optional, seconds, default 0 = disabled) — if a request waits longer than this before firing, return 503 immediately.

Requests in the queue are kept with their original content (including body) and a timestamp of when they entered the queue to support timeout calculations.

## Multi-upstream

With a single upstream the behavior is straightforward. Multi-upstream needs some clarification.

Each upstream maintains its own `next_min_ts` — the earliest timestamp at which the next request to that upstream is allowed to fire. It is updated after each request completes: `next_min_ts = request_done_ts + rand(DELAY)`. While `DELAY` is in seconds, `next_min_ts` is in milliseconds to simulate human behavior. `DELAY` supports constants (`DELAY=5`) or ranges (`DELAY=5:9`). If max < min, max = min.

The dispatcher uses Earliest Deadline First (EDF): pick the upstream with the lowest `next_min_ts`, wait until that time, then fire.

If upstream fails (timeout or server failure):
  1. Try the next earliest upstream until the request succeeds or all upstreams are exhausted. Each retry still waits for the chosen upstream's `next_min_ts`.
  2. If all upstreams exhausted, fail.

A failed request still updates the upstream's `next_min_ts` and counts toward its escalation sliding window.

On success, the upstream response (status code, headers, body, etc) is forwarded to the client as-is. The original client IP is preserved in the X-Forwarded-For header (taking into account existing X-Forwarded-For and X-Real-IP headers) for upstream logging and access control.

## Delay escalation

Gradually increase delays under sustained high-frequency requests to avoid search engine bans while allowing long research sessions.

Each upstream maintains its own escalation state (current `DELAY_MIN`, `DELAY_MAX`, and sliding window), since each upstream maps to a different search engine with independent ban risk. All escalation calculations use the upstream's current `DELAY_MIN`/`DELAY_MAX`, not the global defaults.

The sliding window stores metadata for each request, including its timestamp and the `escalationCount` active at the time of the request.

Escalation is triggered when the following conditions are met:
1. The window contains at least `ESCALATE_AFTER - 1` requests.
2. The oldest request in the window has the same `escalationCount` as the current state (ensuring escalation happens per generation).
3. The span between the oldest and the newest request in the window is less than or equal to `DELAY_MAX * ESCALATE_AFTER`.

When triggered, delays are escalated:

```
factor = rand(ESCALATE_FACTOR)
DELAY_MIN = DELAY_MIN * factor
DELAY_MAX = DELAY_MAX * factor
```

`ESCALATE_FACTOR` is configurable via environment variable, supporting constant (`ESCALATE_FACTOR=1.5`) or range (`ESCALATE_FACTOR=1.5:2.0`) syntax. Default: `1.5:2.0`.

`ESCALATE_MAX_COUNT` caps the number of escalation steps (default: 3, 0 = unlimited).

**Reset**: when the span between the oldest and newest request in the window exceeds `DELAY_MAX * ESCALATE_AFTER`, delays reset to their initial values and the sliding window is cleared.

## Endpoints

`ENDPOINTS` uses prefix matching — `/search` matches `/search` and `/search/foo/bar` but not `/searches`. Default is `/`, meaning all requests are throttled.

Requests not matched by any `ENDPOINTS` entry are passed through directly to the upstream with no queuing or delay, using round-robin upstream selection.

## Upstreams

- Both http and https upstreams must be supported
- Any docker network mode must be supported
