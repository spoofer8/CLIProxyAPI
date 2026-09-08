package usermgmt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestActivityPreviewRedactsCredentialsAndFileData(t *testing.T) {
	body := []byte(`{"model":"example","api_key":"top-secret","messages":[{"role":"user","content":"keep this text"}],"tools":[{"arguments":"{\"password\":\"tool-secret\",\"query\":\"safe argument\"}"}],"image_url":{"url":"https://example.invalid/image?key=url-secret&size=small"},"source":{"type":"base64","media_type":"image/png","data":"raw-image-data"},"file_data":"raw-file-data","cookie":"cookie-secret"}`)
	preview, _, omitted := requestBodyPreview(body, "")
	if omitted != "" || !json.Valid([]byte(preview)) {
		t.Fatal("valid input did not produce readable JSON")
	}
	for _, secret := range []string{"top-secret", "tool-secret", "url-secret", "raw-image-data", "raw-file-data", "cookie-secret"} {
		if strings.Contains(preview, secret) {
			t.Fatal("sensitive field or inline file retained")
		}
	}
	if !strings.Contains(preview, "keep this text") || !strings.Contains(preview, "safe argument") || !strings.Contains(preview, "size=small") {
		t.Fatal("non-sensitive content lost")
	}
}

func TestRequestActivityPreviewKeepsLatestQuestionWithinBudget(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"model": "example", "messages": []any{map[string]any{"role": "system", "content": strings.Repeat("history ", 60000)}, map[string]any{"role": "user", "content": "latest question must remain readable"}}})
	preview, truncated, omitted := requestBodyPreview(body, "")
	if !truncated || omitted != "" || len(preview) > MaxRequestCapturePreviewBytes || !json.Valid([]byte(preview)) || !strings.Contains(preview, "latest question must remain readable") {
		t.Fatal("oversized history lost latest question or exceeded budget")
	}
}

func TestRequestActivityPreviewDoesNotRetainInvalidFragments(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"password":"secret",`), []byte("{} {}"), []byte(strings.Repeat("x", MaxRequestCaptureInspectBytes+1)), {0xff}} {
		preview, _, omitted := requestBodyPreview(body, "")
		if preview != "" || omitted == "" {
			t.Fatal("invalid/oversized input retained a raw fragment")
		}
	}
}
