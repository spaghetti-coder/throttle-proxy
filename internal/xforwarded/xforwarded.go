// Package xforwarded handles X-Forwarded-* header management for reverse proxy scenarios.
//
// When using throttle-proxy behind a reverse proxy, the original client IP is
// lost because the proxy becomes the immediate peer. This package preserves
// the original client IP chain using standard X-Forwarded-* headers.
//
// Fallback chain:
//  1. X-Forwarded-For: Appended with current RemoteAddr
//  2. X-Real-IP: Used if X-Forwarded-For is absent
//  3. RemoteAddr: Ultimate fallback
//
// Security note: The X-Forwarded-For header is easily spoofed. Do not trust
// these headers unless your reverse proxy is the only party that can set them
// (e.g., proxy strips incoming X-Forwarded headers and replaces them).
package xforwarded

import "net/http"

// SetXForwardedFor sets the X-Forwarded-For header on outReq based on inReq.
//
// The function follows RFC 7239-style comma-separated list building to preserve
// the entire proxy chain. Each hop appends the previous hop's IP, creating a
// traceable path: "client, proxy1, proxy2, ..."
//
// Header precedence (most to least trusted):
//   - X-Forwarded-For: Extended with current RemoteAddr (comma-separated)
//   - X-Real-IP: Single IP from trusted reverse proxy
//   - RemoteAddr: Direct connection address
//
// Use this function when you need to forward the original client information
// to upstream servers. Direct header manipulation is discouraged; use this
// helper to ensure consistent behavior across the codebase.
//
// Security: This function does not validate IP addresses. The caller is
// responsible for ensuring inReq comes from a trusted source (e.g., after
// verifying the connection is from your reverse proxy).
func SetXForwardedFor(outReq, inReq *http.Request) {
	clientIP := inReq.RemoteAddr
	if xri := inReq.Header.Get("X-Real-IP"); xri != "" {
		clientIP = xri
	}

	if xff := inReq.Header.Get("X-Forwarded-For"); xff != "" {
		// Append to existing comma-separated list per RFC 7239
		outReq.Header.Set("X-Forwarded-For", xff+", "+clientIP)
	} else {
		outReq.Header.Set("X-Forwarded-For", clientIP)
	}
}
