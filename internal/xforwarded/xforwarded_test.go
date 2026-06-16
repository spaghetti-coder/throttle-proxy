package xforwarded

import (
	"net/http/httptest"
	"testing"
)

func TestSetXForwardedFor(t *testing.T) {
	cases := []struct {
		name       string
		xff        string
		xRealIP    string
		remoteAddr string
		want       string
	}{
		{name: "append to existing XFF", xff: "1.2.3.4", remoteAddr: "5.6.7.8:1234", want: "1.2.3.4, 5.6.7.8:1234"},
		{name: "use X-Real-IP when XFF missing", xRealIP: "1.2.3.4", remoteAddr: "5.6.7.8:1234", want: "1.2.3.4"},
		{name: "fall back to RemoteAddr", remoteAddr: "5.6.7.8:1234", want: "5.6.7.8:1234"},
		{name: "combine XFF chain and X-Real-IP", xff: "1.2.3.4, 2.3.4.5", xRealIP: "9.8.7.6", remoteAddr: "5.6.7.8:1234",
			want: "1.2.3.4, 2.3.4.5, 9.8.7.6"},
		{name: "build chain from XFF and RemoteAddr", xff: "client.ip, proxy1.ip", remoteAddr: "proxy2.ip:8080",
			want: "client.ip, proxy1.ip, proxy2.ip:8080"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := httptest.NewRequest("GET", "/test", nil)
			src.Header.Set("X-Forwarded-For", tc.xff)
			src.Header.Set("X-Real-IP", tc.xRealIP)
			src.RemoteAddr = tc.remoteAddr

			out := httptest.NewRequest("GET", "/test", nil)
			SetXForwardedFor(out, src)

			if got := out.Header.Get("X-Forwarded-For"); got != tc.want {
				t.Fatalf("X-Forwarded-For = %q, want %q", got, tc.want)
			}
		})
	}
}
