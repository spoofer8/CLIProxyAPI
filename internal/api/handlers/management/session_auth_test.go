package management

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestManagementOriginDistinguishesEmptyTLSState(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test:8317/v0/management/login", nil)
	req.Header.Set("Origin", "http://proxy.test:8317")
	req.TLS = &tls.ConnectionState{}
	if !SameOriginRequest(req) {
		t.Fatal("cleartext buffered connection was mistaken for HTTPS")
	}
	req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true}
	if SameOriginRequest(req) {
		t.Fatal("HTTP origin was accepted on a negotiated TLS connection")
	}
	req.Header.Set("Origin", "https://proxy.test:8317")
	if !SameOriginRequest(req) {
		t.Fatal("matching HTTPS origin was rejected")
	}
}

func TestManagementLoginAndLegacyShareBanState(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, envSecret: "legacy-secret"}
	for range 3 {
		h.RecordAuthenticationFailure("127.0.0.1")
	}
	for range 2 {
		h.AuthenticateManagementKey("127.0.0.1", true, "wrong")
	}
	if allowed, status, _ := h.CheckManagementAccess("127.0.0.1", true); allowed || status != http.StatusForbidden {
		t.Fatal("login and legacy did not share ban state")
	}
	h.ResetAuthenticationFailures("127.0.0.1")
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "legacy-secret"); !allowed {
		t.Fatal("shared reset did not restore legacy auth")
	}
	if allowed, status, _ := h.CheckManagementAccess("192.0.2.3", false); allowed || status != http.StatusForbidden {
		t.Fatal("named login guard bypassed allow-remote")
	}
}

func TestManagementSameOriginBrowserAndCLI(t *testing.T) {
	for _, test := range []struct {
		origin, referer, site string
		allowed               bool
	}{
		{"http://proxy.test:8317", "", "same-origin", true},
		{"http://evil.test", "", "cross-site", false},
		{"null", "", "", false},
		{"", "http://proxy.test:8317/management.html", "same-origin", true},
		{"", "http://evil.test/page", "", false},
		{"", "", "", true},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://proxy.test:8317/v0/management/config", nil)
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Referer", test.referer)
		req.Header.Set("Sec-Fetch-Site", test.site)
		if got := SameOriginRequest(req); got != test.allowed {
			t.Fatalf("origin=%q referer=%q allowed=%v,want%v", test.origin, test.referer, got, test.allowed)
		}
	}
}
