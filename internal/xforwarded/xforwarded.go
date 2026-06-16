// Package xforwarded preserves the original client IP chain for proxied requests.
//
// Note: X-Forwarded-* headers are trivially spoofed. Trust them only when
// the reverse proxy is the only party that can set them.
package xforwarded

import "net/http"

// SetXForwardedFor sets X-Forwarded-For on outReq using the client info from inReq.
// It prefers X-Real-IP over RemoteAddr when X-Forwarded-For is absent.
func SetXForwardedFor(outReq, inReq *http.Request) {
	client := inReq.Header.Get("X-Real-IP")
	if client == "" {
		client = inReq.RemoteAddr
	}
	if xff := inReq.Header.Get("X-Forwarded-For"); xff != "" {
		client = xff + ", " + client
	}
	outReq.Header.Set("X-Forwarded-For", client)
}
