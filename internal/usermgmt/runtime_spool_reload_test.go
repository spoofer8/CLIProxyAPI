package usermgmt

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeSpoolReloadPreservesAcceptedCapture(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	_, _, key := createTestIdentity(t, engine)
	oldCtx, oldFinish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "GET", Path: "/fixture", Body: []byte(`{"input":"accepted before reload"}`)})
	if err != nil {
		t.Fatal(err)
	}
	oldState := captureState(oldCtx)
	_, cfg := runtime.Snapshot()
	cfg.RequestActivity.SpoolDirectory = t.TempDir()
	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	newCtx, newFinish, err := runtime.BeginDurableRequestCapture(usageContext(t, runtime, key), RequestCaptureMetadata{Method: "GET", Path: "/fixture", Body: []byte(`{"input":"accepted after reload"}`)})
	if err != nil {
		t.Fatal(err)
	}
	newState := captureState(newCtx)
	if oldState.scope == newState.scope || oldState.scope.store == newState.scope.store {
		t.Fatal("spool path change reused old capture scope")
	}
	if !strings.HasPrefix(newState.journal.path, cfg.RequestActivity.SpoolDirectory+string(filepath.Separator)) {
		t.Fatal("new admissions did not use new spool directory")
	}
	if err := runtime.RecordCapturedContent(oldCtx, "response", "json", []byte(`{"text":"old accepted request still finishes"}`)); err != nil {
		t.Fatal(err)
	}
	newFinish(RequestCaptureResult{StatusCode: 200})
	oldFinish(RequestCaptureResult{StatusCode: 200})
	<-oldState.scope.done
	if err := runtime.FlushRequestActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, _ := runtime.Snapshot()
	if !strings.Contains(allActivityText(t, db, oldState.item.ID, "response"), "old accepted request still finishes") {
		t.Fatal("reload lost accepted request output")
	}
	if !strings.Contains(allActivityText(t, db, newState.item.ID, "request"), "accepted after reload") {
		t.Fatal("reload lost new request input")
	}
}
