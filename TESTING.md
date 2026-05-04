# Testing Guide for throttle-proxy

Comprehensive testing documentation for the throttle-proxy project. This guide covers all test types, running instructions, and troubleshooting.

---

## Table of Contents

1. [Test Overview](#test-overview)
2. [Running Tests (Quick Reference)](#running-tests-quick-reference)
3. [Unit Tests](#unit-tests)
4. [Integration Tests](#integration-tests)
5. [Property-Based Tests](#property-based-tests)
6. [E2E Tests](#e2e-tests)
7. [CI/CD Pipeline](#cicd-pipeline)
8. [Troubleshooting](#troubleshooting)
9. [Coverage Report](#coverage-report)

---

## Test Overview

The throttle-proxy project employs multiple test layers to ensure correctness, reliability, and performance:

### Test Types

| Test Type | Purpose | Location |
|-----------|---------|----------|
| **Unit Tests** | Verify individual functions and methods | `*_test.go` in each package |
| **Integration Tests** | Verify component interactions | `integration/integration_test.go` |
| **Property-Based Tests** | Verify mathematical invariants | `internal/upstream/upstream_properties_test.go` |
| **E2E Tests** | End-to-end scenarios with HTTP | `internal/proxy/proxy_e2e_test.go` |
| **Benchmark Tests** | Performance regression detection | `*_test.go` (Benchmark functions) |

### Coverage Targets

- **Overall coverage:** >80%
- **Package coverage:** Each package should maintain >80% coverage
- **Critical paths:** 100% coverage for error handling and edge cases

### Test Philosophy

- **Zero external dependencies:** All tests use only the Go standard library
- **Deterministic:** Tests produce consistent results (no flakiness)
- **Fast:** Unit tests complete in <1 second
- **Parallel-safe:** Tests can run with `-race` flag without data races

---

## Running Tests (Quick Reference)

### Run All Tests

```bash
go test -count 1 ./...
```

### Run with Race Detection

```bash
go test -race -count 1 ./...
```

### Run with Coverage

```bash
# Generate coverage profile
go test -coverprofile=coverage.out ./...

# View coverage in terminal
go tool cover -func=coverage.out

# Generate HTML report
go tool cover -html=coverage.out -o coverage.html
```

### Run with Verbose Output

```bash
go test -v -count 1 ./...
```

### Run Specific Package

```bash
# Config package
go test -v ./internal/config/...

# Dispatcher package
go test -v ./internal/dispatcher/...

# Upstream package
go test -v ./internal/upstream/...
```

---

## Unit Tests

Unit tests verify individual functions and methods in isolation. Each package has its own `*_test.go` file.

### Package: `internal/config`

**File:** `internal/config/config_test.go`

Tests configuration parsing and validation:
- Environment variable parsing
- Duration range parsing (e.g., `0.5:2`)
- Integer range parsing
- Validation of required fields
- Error messages for invalid input

```bash
go test -v ./internal/config/...
```

**Key test cases:**
- Valid configuration with all options
- Missing required upstream URLs
- Invalid duration formats
- Edge cases for queue size (minimum 1)

### Package: `internal/dispatcher`

**File:** `internal/dispatcher/dispatcher_test.go`

Tests the Earliest Deadline First (EDF) scheduler:
- Request queuing and dispatch
- Priority ordering by deadline
- Queue size limits
- Wait time enforcement
- Thread safety

```bash
go test -v ./internal/dispatcher/...
```

**Key test cases:**
- Multiple upstreams with different deadlines
- Queue full handling
- Request timeout in queue
- Concurrent enqueue/dequeue

### Package: `internal/proxy`

**File:** `internal/proxy/proxy_test.go`

Tests the HTTP proxy handler:
- Request forwarding
- Endpoint matching
- Round-robin passthrough
- Error handling
- Response writing

```bash
go test -v ./internal/proxy/...
```

**Key test cases:**
- Throttled endpoint handling
- Passthrough endpoint handling
- Upstream error responses
- Header preservation

### Package: `internal/upstream`

**File:** `internal/upstream/upstream_test.go`

Tests upstream management:
- Health tracking
- Failover logic
- Exponential backoff
- Delay escalation
- State transitions

```bash
go test -v ./internal/upstream/...
```

**Key test cases:**
- Healthy upstream selection
- Unhealthy upstream skipping
- Backoff calculation
- Escalation trigger and reset

### Package: `internal/xforwarded`

**File:** `internal/xforwarded/xforwarded_test.go`

Tests X-Forwarded-* header handling:
- Header parsing
- Client IP extraction
- Protocol detection

```bash
go test -v ./internal/xforwarded/...
```

### Package: `cmd/throttle-proxy`

**File:** `cmd/throttle-proxy/main_test.go`

Tests the main entry point:
- Configuration loading
- Server startup
- Graceful shutdown
- Signal handling

```bash
go test -v ./cmd/throttle-proxy/...
```

---

## Integration Tests

Integration tests verify that components work together correctly.

**File:** `integration/integration_test.go`

### What They Verify

1. **Sequential Processing:** Requests are serialized per upstream
2. **Failover:** When one upstream fails, requests route to healthy upstreams
3. **Endpoint Matching:** Only configured endpoints are throttled
4. **Round-Robin Passthrough:** Non-throttled endpoints bypass the queue

### Running Integration Tests

```bash
# Run all integration tests
go test -count 1 ./integration/...

# Run with verbose output
go test -v ./integration/...

# Run with race detection
go test -race ./integration/...
```

### Expected Duration

Integration tests typically complete in **5-15 seconds**, depending on:
- Number of test cases
- Configured delays
- Upstream response times

### Test Structure

Each integration test:
1. Starts test HTTP servers as upstreams
2. Configures the proxy with test settings
3. Sends concurrent requests
4. Verifies ordering, timing, and responses
5. Cleans up resources

---

## Property-Based Tests

Property-based tests verify mathematical invariants and properties using randomized inputs.

**File:** `internal/upstream/upstream_properties_test.go`

### What They Verify

Using Go's `testing/quick` package, these tests verify:
- **Escalation bounds:** Delay never exceeds configured maximum
- **Backoff calculation:** Exponential backoff follows expected curve
- **State consistency:** Health transitions are valid
- **Deadline ordering:** EDF scheduling maintains priority invariants

### Running Property-Based Tests

```bash
# Run property tests explicitly
go test -v ./internal/upstream/... -run Properties

# Run all tests including properties
go test -v ./internal/upstream/...
```

### How They Work

The `testing/quick` package:
1. Generates random inputs (e.g., delay ranges, escalation counts)
2. Runs the function under test
3. Checks that properties hold
4. Repeats with many random inputs (default: 100 iterations)

If a property fails, it reports the minimal failing input.

---

## E2E Tests

End-to-end tests verify complete scenarios using real HTTP requests.

**File:** `internal/proxy/proxy_e2e_test.go`

### What They Verify

1. **Graceful Shutdown:** Server completes in-flight requests before exiting
2. **Failover Chains:** Multiple upstream failures cascade correctly
3. **Streaming:** Large response bodies stream correctly
4. **Backpressure:** Slow upstreams don't overwhelm the proxy

### Running E2E Tests

```bash
# Run E2E tests explicitly
go test -v ./internal/proxy/... -run TestE2E

# Run all proxy tests including E2E
go test -v ./internal/proxy/...
```

### Test Infrastructure

E2E tests use `httptest` to create dummy upstream servers:
- No external network dependencies
- Tests run in isolation
- Clean up automatically

### Example Scenario

```go
// Test graceful shutdown:
// 1. Start proxy server
// 2. Send slow request (5 second response time)
// 3. Trigger shutdown
// 4. Verify request completes before exit
// 5. Verify no new requests accepted
```

---

## CI/CD Pipeline

The GitHub Actions workflow runs tests automatically on every push.

### What Runs Automatically

**Workflow:** `.github/workflows/test.yaml`

| Trigger | Jobs |
|---------|------|
| Push to `master` | Test, Build, Docker |
| Pull Request | Test, Lint |
| Tag `v*` | Release |

### Test Job

```yaml
- go mod verify
- go test -race -count 1 ./...
- go test -coverprofile=coverage.out ./...
```

### Coverage Reports

Coverage is reported but **NOT** used as a gate:
- Coverage artifacts are uploaded for review
- Current target: >80% (informational only)
- No CI failure for coverage below target

### How to Interpret CI Results

**Green checkmark:** All tests pass, no races detected

**Red X:** 
- Check "Test" job logs for failure details
- Common causes: flaky test, race condition, new bug

**Yellow dot:** Still running (tests take ~2-3 minutes)

---

## Troubleshooting

### Common Test Failures

#### "race detected during execution"

**Cause:** Data race in code or test
**Fix:** 
```bash
# Run with race detector to find the race
go test -race -v ./...
```

#### "timeout" in integration tests

**Cause:** Slow machine or resource contention
**Fix:**
```bash
# Increase timeout
go test -timeout 60s ./integration/...
```

#### "bind: address already in use"

**Cause:** Previous test didn't clean up ports
**Fix:**
```bash
# Find and kill process using port
lsof -ti:8080 | xargs kill -9
```

#### "coverage below threshold"

**Cause:** New code without tests
**Fix:** Add tests for uncovered code paths

### Flaky Test Detection

Tests are considered flaky if they:
- Pass/fail inconsistently across runs
- Fail with timing-related errors
- Pass locally but fail in CI

**Detection:**
```bash
# Run tests multiple times
for i in {1..10}; do
  go test -count 1 ./... || echo "FAILED on run $i"
done
```

**Resolution:**
- Add `time.Sleep()` for timing-sensitive tests
- Use channels instead of `Sleep` for synchronization
- Increase timeouts for CI environments

### Debug Mode

Run tests with verbose output to see detailed information:

```bash
go test -v -count 1 ./...
```

Show individual test names and output:
```bash
go test -v -count 1 ./internal/config/... -run TestParseConfig
```

---

## Coverage Report

### Current Coverage by Package

| Package | Coverage | Status |
|---------|----------|--------|
| `cmd/throttle-proxy` | 80.9% | ✓ Good |
| `internal/config` | 95.9% | ✓ Good |
| `internal/dispatcher` | 89.6% | ✓ Good |
| `internal/proxy` | 95.8% | ✓ Good |
| `internal/upstream` | 100.0% | ✓ Good |
| `internal/xforwarded` | 100.0% | ✓ Good |
| **Overall** | **92.5%** | ✓ Target met |

*Generated: 2026-05-02*

### Coverage Breakdown

| Test Type | Coverage | Notes |
|-----------|----------|-------|
| Unit Tests | ~25% | Isolated function testing |
| E2E Tests | 70.8% | Integration scenarios with httptest servers |
| **Combined** | **95.8%** | Full proxy package coverage |

**Note:** E2E tests achieve 70.8% coverage because they test complete
integration scenarios (graceful shutdown, failover chains, body streaming,
queue backpressure) that require the full system. Unit tests cover the
remaining functions in isolation.

### Coverage History

| Date | Overall | Notes |
|------|---------|-------|
| 2026-05-02 | 80.0% | Phase 4 complete, all targets met |
| 2026-05-02 | 75.8% | After Phase 3 refactoring |

### How to Improve Coverage

1. **Identify uncovered code:**
   ```bash
   go tool cover -html=coverage.out -o coverage.html
   # Open coverage.html in browser
   ```

2. **Add tests for uncovered paths:**
   - Error handling branches (`if err != nil`)
   - Edge cases (empty inputs, boundary values)
   - Concurrent code paths

3. **Focus on critical paths:**
   - Configuration parsing
   - Request dispatching
   - Health checking
   - Failover logic

### Excluding Code from Coverage

Use build tags to exclude debug/test code:

```go
//go:build !test
// +build !test

package main

func debugOnly() { ... }
```

---

## Additional Resources

- [Go Testing Documentation](https://golang.org/pkg/testing/)
- [Go Race Detector](https://golang.org/doc/articles/race_detector.html)
- [Test Coverage](https://blog.golang.org/cover)
- [Project README](./README.md)
- [Architecture Spec](./spec.md)

---

*Last updated: 2026-05-02*
