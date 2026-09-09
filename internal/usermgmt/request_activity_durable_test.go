package usermgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func allActivityText(t *testing.T, db *store.Store, id, direction string) string {
	t.Helper()
	var full strings.Builder
	after := int64(0)
	for {
		chunks, err := db.ListRequestContent(context.Background(), id, direction, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range chunks {
			full.WriteString(chunk.Text)
			after = chunk.Sequence
		}
		if len(chunks) < 2 {
			break
		}
	}
	return full.String()
}

func TestRequestActivityFullTextRecoveryAndPagination(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	user, _, key := createTestIdentity(t, engine)
	body, _ := json.Marshal(map[string]any{"model": "test", "session_id": "explicit-session", "messages": []any{
		map[string]any{"role": "system", "content": "original system instructions"},
		map[string]any{"role": "user", "content": strings.Repeat("full text retained ", 300000) + "final question"},
		map[string]any{"role": "tool", "content": "full tool result", "tool_call_id": "call_1"},
	}, "image_url": "data:image/png;base64,SECRET_BINARY", "api_key": "SECRET_API_KEY"})
	// GET makes this an activity-only fixture, independent of financial usage.
	ctx, finish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "GET", Path: "/fixture?key=SECRET_QUERY", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	id := captureState(ctx).item.ID
	copied := CopyRequestCaptureContext(context.Background(), ctx)
	response := []byte("data: {\"type\":\"delta\",\"text\":\"complete streamed reply\",\"api_key\":\"SECRET_RESPONSE\"}\n\ndata: [DONE]\n\n")
	for _, part := range [][]byte{response[:17], response[17:41], response[41:]} {
		if err := runtime.RecordCapturedContent(copied, "response", "sse", part); err != nil {
			t.Fatal(err)
		}
	}
	finish(RequestCaptureResult{StatusCode: 200})
	if err := runtime.FlushRequestActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	request := allActivityText(t, db, id, "request")
	reply := allActivityText(t, db, id, "response")
	if !json.Valid([]byte(request)) || !strings.Contains(request, "original system instructions") || !strings.Contains(request, "final question") || !strings.Contains(request, "full tool result") || len(request) < 4<<20 {
		t.Fatal("complete conversation was truncated")
	}
	for _, secret := range []string{"SECRET_BINARY", "SECRET_API_KEY", "SECRET_RESPONSE", "SECRET_QUERY"} {
		if strings.Contains(request+reply, secret) {
			t.Fatal("credential or attachment retained")
		}
	}
	if !strings.Contains(reply, "complete streamed reply") {
		t.Fatal("split SSE event was lost")
	}
	item, err := db.GetRequestActivity(context.Background(), id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if item.CaptureState != "complete" || item.SessionID != "explicit-session" || item.RequestContentBytes != int64(len(request)) || len(item.BodyPreview) > MaxRequestCapturePreviewBytes {
		t.Fatalf("wrong full content metadata: %+v", item.RequestActivitySummary)
	}
	page := requestJSON(t, engine, http.MethodGet, "/requests/"+id+"/content?direction=request&limit=1", "", 200)
	if !strings.Contains(page.Body.String(), `"next_after":`) || strings.Contains(page.Body.String(), `"next_after":0`) {
		t.Fatal("large content was not incrementally paginated")
	}
	filtered := requestJSON(t, engine, http.MethodGet, "/users/"+user.ID+"/requests?session_id=explicit-session", "", 200)
	if !strings.Contains(filtered.Body.String(), id) {
		t.Fatal("explicit session filter lost request")
	}
	if _, err := db.DB().Exec(`UPDATE cpa_request_activity SET at=now()-interval '20 years' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, engine, http.MethodGet, "/requests/"+id, "", 200)
}

func TestRequestActivityDatabaseOutageAndRestartReplay(t *testing.T) {
	runtime, engine, dsn := testRuntime(t)
	_, _, key := createTestIdentity(t, engine)
	ctx, finish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "GET", Path: "/fixture", Body: []byte(`{"messages":[{"role":"user","content":"recover request"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	state := captureState(ctx)
	id := state.item.ID
	writer := state.scope.activity
	db, cfg := runtime.Snapshot()
	// Stop automatic recovery; the test controls the persistence failure and restart.
	writer.cancel()
	<-writer.done
	if _, err := db.DB().Exec(`ALTER TABLE cpa_request_content RENAME TO cpa_request_content_offline`); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RecordCapturedContent(ctx, "response", "json", []byte(`{"id":"resp_recovered","text":"recover reply"}`)); err != nil {
		t.Fatal("accepted output depended on unavailable database", err)
	}
	finish(RequestCaptureResult{StatusCode: 200})
	if err := writer.flush(context.Background()); err == nil {
		t.Fatal("database outage was silently ignored")
	}
	journalBytes, err := os.ReadFile(state.journal.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journalBytes, []byte("recover request")) || bytes.Contains(journalBytes, []byte("recover reply")) {
		t.Fatal("journal contains plaintext conversation")
	}
	info, err := os.Stat(state.journal.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("journal permissions are not private")
	}
	if _, err := db.DB().Exec(`ALTER TABLE cpa_request_content_offline RENAME TO cpa_request_content`); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.Open(context.Background(), store.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	recovered := newRequestActivityWriter(replacement, 0, cfg.RequestActivity.SpoolDirectory)
	defer func() {
		if err := recovered.close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err := recovered.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(allActivityText(t, db, id, "response"), "recover reply") {
		t.Fatal("restart lost durable output")
	}
	if _, err := os.Stat(state.journal.path); !os.IsNotExist(err) {
		t.Fatal("successfully replayed journal retained")
	}
	if err := recovered.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	chunks, err := db.ListRequestContent(context.Background(), id, "response", 0, 100)
	if err != nil || len(chunks) != 1 {
		t.Fatal("replay duplicated response chunks")
	}
}

func TestRequestActivityRejectsUnavailableAdmissionAndSpool(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, key := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, key)
	db, _ := runtime.Snapshot()
	if _, err := db.DB().Exec(`ALTER TABLE cpa_request_activity RENAME TO cpa_request_activity_offline`); err != nil {
		t.Fatal(err)
	}
	_, finish, err := runtime.BeginDurableRequestCapture(ctx, RequestCaptureMetadata{Method: "GET", Path: "/fixture"})
	if err == nil {
		t.Fatal("unrecorded request admitted")
	}
	finish(RequestCaptureResult{})
	if _, err := db.DB().Exec(`ALTER TABLE cpa_request_activity_offline RENAME TO cpa_request_activity`); err != nil {
		t.Fatal(err)
	}
	writer := runtime.current.activity
	writer.cancel()
	<-writer.done
	_, _, err = runtime.BeginDurableRequestCapture(ctx, RequestCaptureMetadata{Method: "GET", Path: "/fixture"})
	if err == nil {
		t.Fatal("unavailable durable spool admitted request")
	}
}

func TestRequestActivityRejectsTamperedRecoveryJournal(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, key := createTestIdentity(t, engine)
	ctx, finish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "GET", Path: "/fixture", Body: []byte(`{"input":"keep secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	state := captureState(ctx)
	writer := state.scope.activity
	writer.cancel()
	<-writer.done
	finish(RequestCaptureResult{StatusCode: 200})
	data, err := os.ReadFile(state.journal.path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	if err := os.WriteFile(state.journal.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writer.flush(context.Background()); err == nil {
		t.Fatal("unauthenticated journal replayed")
	}
	// Remove only this deliberately corrupted test artifact so cleanup can succeed.
	if err := os.Remove(state.journal.path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(writer.directory, "journal.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("journal key is not private")
	}
}

func TestRequestActivityUnlimitedDefaults(t *testing.T) {
	cfg := config.UserManagementConfig{}.WithDefaults()
	if cfg.RequestActivity.RetentionDays != 0 {
		t.Fatal("full content defaults to finite retention")
	}
}

func TestRequestActivityReconcilesLateParentAndReliableSessions(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, key := createTestIdentity(t, engine)
	parentCtx, parentFinish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "GET", Path: "/fixture", Body: []byte(`{"metadata":{"user_id":"user_example_account_account_session_8ade2881-5b01-4b03-9755-cd829e75cdf0"},"input":"parent"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.RecordCapturedContent(parentCtx, "response", "json", []byte(`{"id":"resp_parent","output":[]}`)); err != nil {
		t.Fatal(err)
	}
	releaseProducer := beginCapturedProducer(parentCtx)
	parentFinish(RequestCaptureResult{StatusCode: 200})
	childCtx, childFinish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "GET", Path: "/fixture", Body: []byte(`{"previous_response_id":"resp_parent","input":"child"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.RecordCapturedContent(childCtx, "response", "json", []byte(`{"id":"resp_child","output":[]}`)); err != nil {
		t.Fatal(err)
	}
	childFinish(RequestCaptureResult{StatusCode: 200})
	if err := runtime.FlushRequestActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	childID := captureState(childCtx).item.ID
	child, err := db.GetRequestActivity(context.Background(), childID, time.Time{})
	if err != nil || child.SessionID != "response:resp_child" {
		t.Fatal("child did not remain independent while parent was active", err)
	}
	releaseProducer()
	if err := runtime.FlushRequestActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	child, err = db.GetRequestActivity(context.Background(), childID, time.Time{})
	if err != nil || child.SessionID != "8ade2881-5b01-4b03-9755-cd829e75cdf0" || child.SessionSource != "previous_response_id" {
		t.Fatalf("late parent session edge not reconciled: %+v %v", child.RequestActivitySummary, err)
	}
	if id, _ := reliableBodySession([]byte(`{"metadata":{"user_id":"ordinary-user"},"prompt_cache_key":"same-key"}`)); id != "" {
		t.Fatal("generic user/cache identifiers grouped unrelated conversations")
	}
}
