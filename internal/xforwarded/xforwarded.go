// Package xforwarded builds the X-Forwarded-For chain for proxied requests.
package xforwarded

import "net/http"

// SetXForwardedFor appends the client address to outReq's X-Forwarded-For,
// extending an existing comma-separated chain. The client address is taken from
// X-Real-IP when present, otherwise from inReq.RemoteAddr. Values are copied
// unvalidated: meaningful only when the immediate peer is a trusted proxy.
func SetXForwardedFor(outReq, inReq *http.Request) {
	clientIP := inReq.RemoteAddr
	if xri := inReq.Header.Get("X-Real-IP"); xri != "" {
		clientIP = xri
	}

	if xff := inReq.Header.Get("X-Forwarded-For"); xff != "" {
		outReq.Header.Set("X-Forwarded-For", xff+", "+clientIP)
	} else {
		outReq.Header.Set("X-Forwarded-For", clientIP)
	}
}
