package util

import "testing"

func TestCookieHeaderRedaction(t *testing.T) {
	for _, header := range []string{"Cookie", "cookie", "Set-Cookie", "SET-COOKIE"} {
		if got := MaskSensitiveHeaderValue(header, "cpa_session=secret"); got != "[REDACTED]" {
			t.Fatalf("%s was not fully redacted", header)
		}
	}
	if got := RedactCookieHeaderValue("X-RateLimit-Tokens", "123"); got != "123" {
		t.Fatal("cookie-only log filtering changed another header")
	}
}
