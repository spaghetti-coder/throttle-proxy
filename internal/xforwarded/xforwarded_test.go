package xforwarded

import (
	"net/http/httptest"
	"testing"
)

// TestSetXForwardedFor_AppendsExisting verifies X-Forwarded-For is extended
func TestSetXForwardedFor_AppendsExisting(t *testing.T) {
	src := httptest.NewRequest("GET", "/test", nil)
	src.Header.Set("X-Forwarded-For", "1.2.3.4")
	src.RemoteAddr = "5.6.7.8:1234"

	out := httptest.NewRequest("GET", "/test", nil)
	SetXForwardedFor(out, src)

	if got := out.Header.Get("X-Forwarded-For"); got != "1.2.3.4, 5.6.7.8:1234" {
		t.Fatalf("expected XFF to append, got %q", got)
	}
}

// TestSetXForwardedFor_UsesRealIPWhenXFFMissing verifies fallback to X-Real-IP
func TestSetXForwardedFor_UsesRealIPWhenXFFMissing(t *testing.T) {
	src := httptest.NewRequest("GET", "/test", nil)
	src.Header.Set("X-Real-IP", "1.2.3.4")
	src.RemoteAddr = "5.6.7.8:1234"

	out := httptest.NewRequest("GET", "/test", nil)
	SetXForwardedFor(out, src)

	if got := out.Header.Get("X-Forwarded-For"); got != "1.2.3.4" {
		t.Fatalf("expected XFF to use X-Real-IP, got %q", got)
	}
}

// TestSetXForwardedFor_UsesRemoteAddrFallback verifies fallback to RemoteAddr
func TestSetXForwardedFor_UsesRemoteAddrFallback(t *testing.T) {
	src := httptest.NewRequest("GET", "/test", nil)
	src.RemoteAddr = "5.6.7.8:1234"

	out := httptest.NewRequest("GET", "/test", nil)
	SetXForwardedFor(out, src)

	if got := out.Header.Get("X-Forwarded-For"); got != "5.6.7.8:1234" {
		t.Fatalf("expected XFF to fall back to RemoteAddr, got %q", got)
	}
}

// TestSetXForwardedFor_CombinesXFFAndXRealIP verifies X-Real-IP overrides RemoteAddr
// when X-Forwarded-For is present, preserving the chain with the real client IP
func TestSetXForwardedFor_CombinesXFFAndXRealIP(t *testing.T) {
	src := httptest.NewRequest("GET", "/test", nil)
	src.Header.Set("X-Forwarded-For", "1.2.3.4, 2.3.4.5")
	src.Header.Set("X-Real-IP", "9.8.7.6")
	src.RemoteAddr = "5.6.7.8:1234"

	out := httptest.NewRequest("GET", "/test", nil)
	SetXForwardedFor(out, src)

	// X-Real-IP overrides RemoteAddr, but chain from XFF is preserved
	// Current behavior: X-Real-IP is used as the clientIP to append
	if got := out.Header.Get("X-Forwarded-For"); got != "1.2.3.4, 2.3.4.5, 9.8.7.6" {
		t.Fatalf("expected XFF chain with X-Real-IP appended, got %q", got)
	}
}

// TestSetXForwardedFor_ChainBuilding verifies RFC 7239 chain building behavior
func TestSetXForwardedFor_ChainBuilding(t *testing.T) {
	// Simulate a 3-hop proxy chain
	src := httptest.NewRequest("GET", "/test", nil)
	src.Header.Set("X-Forwarded-For", "client.ip, proxy1.ip")
	src.RemoteAddr = "proxy2.ip:8080"

	out := httptest.NewRequest("GET", "/test", nil)
	SetXForwardedFor(out, src)

	// Should append current hop to chain
	if got := out.Header.Get("X-Forwarded-For"); got != "client.ip, proxy1.ip, proxy2.ip:8080" {
		t.Fatalf("expected proper chain building, got %q", got)
	}
}
