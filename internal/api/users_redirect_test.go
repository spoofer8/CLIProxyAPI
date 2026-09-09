package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUsersBookmarkRedirectsIntoManagementPanel(t *testing.T) {
	server, _ := newCaptureTestServer(t, nil)
	response := httptest.NewRecorder()
	server.engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users", nil))
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/management.html#/users" {
		t.Fatal("users bookmark did not redirect to the native panel route")
	}
	response = httptest.NewRecorder()
	server.engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/assets/users_console.js", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("standalone UI assets are still served")
	}
}
