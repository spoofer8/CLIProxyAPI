package api

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/tidwall/gjson"
)

// The tee writes durable encrypted frames and never imposes a text length cap.
type requestCaptureReader struct {
	io.ReadCloser
	capture func([]byte) error
}

func (r *requestCaptureReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.capture != nil {
		if errCapture := r.capture(p[:n]); errCapture != nil {
			return n, errCapture
		}
	}
	return n, err
}

type activityResponseWriter struct {
	gin.ResponseWriter
	capture func([]byte, string) error
	failed  bool
}

func (w *activityResponseWriter) Write(body []byte) (int, error) {
	format := "json"
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		format = "sse"
	}
	if err := w.capture(body, format); err != nil {
		w.failed = true
		return 0, err
	}
	n, err := w.ResponseWriter.Write(body)
	if err != nil {
		w.failed = true
	}
	return n, err
}
func (w *activityResponseWriter) WriteString(text string) (int, error) { return w.Write([]byte(text)) }

func capturedWebsocketRequest(request *http.Request) bool {
	return request.Method == http.MethodGet && websocket.IsWebSocketUpgrade(request) && (request.URL.Path == "/v1/responses" || request.URL.Path == "/backend-api/codex/responses")
}
func requestCaptureModel(body []byte, path string) string {
	if model := gjson.GetBytes(body, "model").String(); model != "" {
		return model
	}
	if strings.HasPrefix(path, "/v1beta/models/") {
		model, _, _ := strings.Cut(strings.TrimPrefix(path, "/v1beta/models/"), ":")
		return model
	}
	return ""
}
func reliableActivitySession(headers http.Header) (string, string) {
	if metadata := headers.Get("X-Codex-Turn-Metadata"); metadata != "" {
		value := gjson.Get(metadata, "session_id")
		if value.Type == gjson.String && value.String() != "" && len(value.String()) <= 512 && !strings.ContainsAny(value.String(), "\r\n\x00") {
			return value.String(), "header:x-codex-turn-metadata.session_id"
		}
	}
	// Deliberately excludes request IDs, cache keys, user IDs, and affinity hints.
	for _, key := range []string{"X-Claude-Code-Session-Id", "Session-Id", "Session_id", "X-Http-Session-Id", "X-Session-Id", "X-Conversation-Id", "X-Thread-Id"} {
		value := headers.Get(key)
		if value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\r\n\x00") {
			return value, "header:" + strings.ToLower(key)
		}
	}
	return "", ""
}
func (s *Server) attachUserRequestCapture(c *gin.Context, hooks *sdkaccess.RequestHooks) {
	hooks.CopyContext = usermgmt.CopyRequestCaptureContext
	hooks.CaptureContent = s.userManagement.RecordCapturedContent
	hooks.CompleteNonGenerating = s.userManagement.CompleteNonGeneratingRequest
	path := c.Request.URL.Path
	sessionID, sessionSource := reliableActivitySession(c.Request.Header)
	hooks.CaptureDurable = func(ctx context.Context, model string, body []byte) (context.Context, func(int), error) {
		captured, finish, err := s.userManagement.BeginDurableRequestCapture(ctx, usermgmt.RequestCaptureMetadata{Method: "WS", Path: path, Model: model, Body: body, SessionID: sessionID, SessionSource: sessionSource})
		return captured, func(status int) { finish(usermgmt.RequestCaptureResult{StatusCode: status}) }, err
	}
}
func (s *Server) beginUserHTTPRequestCapture(c *gin.Context) (func(), error) {
	if capturedWebsocketRequest(c.Request) {
		return func() {}, nil
	}
	path := c.Request.URL.Path
	sessionID, sessionSource := reliableActivitySession(c.Request.Header)
	ctx, finish, err := s.userManagement.BeginDurableRequestCapture(c.Request.Context(), usermgmt.RequestCaptureMetadata{Method: c.Request.Method, Path: path, Model: requestCaptureModel(nil, path), SessionID: sessionID, SessionSource: sessionSource})
	if err != nil {
		return func() {}, err
	}
	c.Request = c.Request.WithContext(ctx)
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		format := "raw"
		switch strings.ToLower(strings.TrimSpace(c.Request.Header.Get("Content-Encoding"))) {
		case "zstd":
			format = "zstd"
		case "gzip":
			format = "gzip"
		}
		c.Request.Body = &requestCaptureReader{ReadCloser: c.Request.Body, capture: func(body []byte) error {
			return s.userManagement.RecordCapturedContent(ctx, "request_raw", format, body)
		}}
	}
	writer := &activityResponseWriter{ResponseWriter: c.Writer, capture: func(body []byte, format string) error {
		return s.userManagement.RecordCapturedContent(ctx, "response", format, body)
	}}
	c.Writer = writer
	return func() {
		status := c.Writer.Status()
		if writer.failed || c.Request.Context().Err() != nil {
			if status < 400 {
				status = 499
			}
		}
		finish(usermgmt.RequestCaptureResult{StatusCode: status})
	}, nil
}
