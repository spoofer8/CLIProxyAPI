package api

import (
	"bytes"
	"testing"
)

func TestManagementSessionBridgeRunsBeforeUpstreamBundle(t *testing.T) {
	for _, head := range []string{"<head>", `<HEAD data-panel="upstream">`} {
		original := []byte(`<!doctype html><html>` + head + `<script type="module">window.upstreamStarted=true</script></head><body>Panel</body></html>`)
		snapshot := append([]byte(nil), original...)
		result := injectManagementSessionBridge(original, true)
		bridge := bytes.Index(result, []byte(`id="cpa-session-bridge"`))
		bundle := bytes.Index(result, []byte(`type="module"`))
		if bridge < 0 || bundle <= bridge {
			t.Fatal("session bootstrap must execute before the upstream bundle")
		}
		if !bytes.Contains(result, []byte("const authenticated = true;")) {
			t.Fatal("validated session state was not passed into bootstrap")
		}
		if !bytes.Equal(snapshot, original) {
			t.Fatal("injecting response HTML changed the downloaded asset")
		}
		if again := injectManagementSessionBridge(result, true); !bytes.Equal(again, result) {
			t.Fatal("repeated response adaptation duplicated the logout bridge")
		}
	}
}

func TestManagementSessionBridgeHandlesMissingSessionAndHead(t *testing.T) {
	original := []byte(`<html><script>window.upstreamStarted=true</script></html>`)
	result := injectManagementSessionBridge(original, false)
	if !bytes.HasPrefix(result, []byte(`<script id="cpa-session-bridge">`)) || !bytes.Contains(result, []byte("const authenticated = false;")) {
		t.Fatal("missing-head panel did not receive an unauthenticated bootstrap before its scripts")
	}
	if !bytes.HasSuffix(result, original) {
		t.Fatal("fallback injection modified upstream content")
	}
}
