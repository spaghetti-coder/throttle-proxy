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
