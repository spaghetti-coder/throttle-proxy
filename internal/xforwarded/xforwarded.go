package xforwarded

import "net/http"

// SetXForwardedFor sets the X-Forwarded-For header on outReq based on inReq.
// It falls back to X-Real-IP if X-Forwarded-For is absent, and ultimately to RemoteAddr.
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
