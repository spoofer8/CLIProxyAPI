package logging

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestRequestAndResponseCookieLogsAreRedacted(t *testing.T) {
	request := map[string][]string{"Cookie": {"cpa_session=REQUEST-SECRET"}, "Authorization": {"Bearer ordinary-token"}}
	response := map[string][]string{"Set-Cookie": {"cpa_session=RESPONSE-SECRET; HttpOnly; Path=/"}}
	logger := &FileRequestLogger{}
	formatted := logger.formatLogContent("/test", http.MethodPost, request, []byte(`{}`), nil, nil, nil, nil, []byte(`{}`), http.StatusOK, response, nil)
	if strings.Contains(formatted, "REQUEST-SECRET") || strings.Contains(formatted, "RESPONSE-SECRET") || !strings.Contains(formatted, "[REDACTED]") {
		t.Fatal("request log exposed cookie credentials")
	}
	var streamed bytes.Buffer
	if err := writeResponseSection(&streamed, http.StatusOK, true, response, strings.NewReader(`{}`), nil, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(streamed.String(), "RESPONSE-SECRET") {
		t.Fatal("streamed response log exposed Set-Cookie")
	}
	cloned := cloneHeaders(request)
	if cloned["Cookie"][0] != "[REDACTED]" || cloned["Authorization"][0] != request["Authorization"][0] || request["Cookie"][0] != "cpa_session=REQUEST-SECRET" {
		t.Fatal("structured log redaction changed live or unrelated headers")
	}
}
