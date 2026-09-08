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

// requestCaptureReader observes only bytes consumed by the existing handler.
// It never drains a rejected request or changes the data seen by the proxy.
type requestCaptureReader struct {
	io.ReadCloser
	preview  []byte
	overflow bool
}

func (r *requestCaptureReader) Read(p []byte) (int, error) {
	n, errRead := r.ReadCloser.Read(p)
	if n > 0 {
		remaining := usermgmt.MaxRequestCaptureInspectBytes - len(r.preview)
		if n > remaining {
			r.overflow = true
		}
		if remaining > 0 {
			r.preview = append(r.preview, p[:min(n, remaining)]...)
		}
	}
	return n, errRead
}

func capturedWebsocketRequest(request *http.Request) bool {
	return request.Method == http.MethodGet && websocket.IsWebSocketUpgrade(request) &&
		(request.URL.Path == "/v1/responses" || request.URL.Path == "/backend-api/codex/responses")
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

func (s *Server) attachUserRequestCapture(c *gin.Context, hooks *sdkaccess.RequestHooks) {
	hooks.CopyContext = usermgmt.CopyRequestCaptureContext
	path := c.Request.URL.Path // Never retain query credentials or HTTP headers.
	hooks.Capture = func(ctx context.Context, model string, body []byte) (context.Context, func(int)) {
		metadata := usermgmt.RequestCaptureMetadata{Method: "WS", Path: path, Model: model}
		if len(body) > usermgmt.MaxRequestCaptureInspectBytes {
			metadata.BodyOmittedReason = "inspection_limit"
		} else {
			metadata.Body = body
		}
		capturedCtx, finish := s.userManagement.BeginRequestCapture(ctx, metadata)
		return capturedCtx, func(status int) {
			finish(usermgmt.RequestCaptureResult{StatusCode: status})
		}
	}
}

func (s *Server) beginUserHTTPRequestCapture(c *gin.Context) func() {
	_, cfg := s.userManagement.Snapshot()
	if !cfg.RequestActivity.CaptureEnabled() {
		return func() {}
	}
	if capturedWebsocketRequest(c.Request) {
		// Each original frame has its own identity and lifecycle below the upgrade.
		return func() {}
	}
	path := c.Request.URL.Path
	ctx, finish := s.userManagement.BeginRequestCapture(c.Request.Context(), usermgmt.RequestCaptureMetadata{
		Method: c.Request.Method, Path: path, Model: requestCaptureModel(nil, path),
	})
	c.Request = c.Request.WithContext(ctx)
	var reader *requestCaptureReader
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		reader = &requestCaptureReader{ReadCloser: c.Request.Body}
		c.Request.Body = reader
	}
	return func() {
		result := usermgmt.RequestCaptureResult{StatusCode: c.Writer.Status(), BodyOmittedReason: "empty_body"}
		if reader != nil {
			switch {
			case reader.overflow:
				result.BodyOmittedReason = "inspection_limit"
			case len(reader.preview) == 0 && c.Request.ContentLength != 0:
				result.BodyOmittedReason = "body_not_read"
			default:
				result.Body = reader.preview
				result.BodyOmittedReason = ""
				result.Model = requestCaptureModel(reader.preview, path)
			}
		}
		finish(result)
	}
}
