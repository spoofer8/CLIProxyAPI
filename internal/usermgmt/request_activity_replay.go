package usermgmt

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	"github.com/tidwall/gjson"
)

func (w *requestActivityWriter) replay(ctx context.Context, path string) error {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return errors.New("activity recovery open failed")
	}
	defer func() { _ = file.Close() }()
	var item *store.RequestActivity
	requestFormat, responseFormat := "raw", "json"
	hasDecoded, complete := false, false
	for {
		record, errRead := readActivityJournalRecord(file, w.aead, filepath.Base(path))
		if errors.Is(errRead, io.EOF) {
			break
		}
		if errors.Is(errRead, io.ErrUnexpectedEOF) {
			complete = false
			break
		}
		if errRead != nil {
			return errRead
		}
		switch record.Kind {
		case "begin":
			if item != nil || record.Item == nil {
				return errors.New("invalid activity recovery admission")
			}
			item = record.Item
		case "finish":
			if item == nil || record.Item == nil || record.Item.ID != item.ID {
				return errors.New("invalid activity recovery completion")
			}
			item = record.Item
			complete = true
		case "content":
			if item == nil {
				return errors.New("invalid activity recovery ordering")
			}
			switch record.Direction {
			case "request":
				hasDecoded = true
			case "request_raw":
				requestFormat = record.Format
			case "response":
				responseFormat = record.Format
			default:
				return errors.New("invalid activity recovery direction")
			}
		default:
			return errors.New("invalid activity recovery record")
		}
	}
	if item == nil || item.ID+".journal" != filepath.Base(path) {
		return errors.New("invalid activity recovery identity")
	}
	if _, err := w.db.GetRequestActivity(ctx, item.ID, time.Time{}); err != nil {
		return err
	}
	direction := "request_raw"
	if hasDecoded {
		direction = "request"
		requestFormat = "json"
	}
	requestReader, err := newActivityDirectionReader(path, w.aead, direction)
	if err != nil {
		return err
	}
	defer func() { _ = requestReader.Close() }()
	var input io.Reader = requestReader
	var closeDecoder func()
	switch requestFormat {
	case "zstd":
		decoder, err := zstd.NewReader(input)
		if err != nil {
			item.BodyOmittedReason = "content_encoding"
			input = strings.NewReader("")
		} else {
			input = decoder
			closeDecoder = decoder.Close
		}
	case "gzip":
		decoder, err := gzip.NewReader(input)
		if err != nil {
			item.BodyOmittedReason = "content_encoding"
			input = strings.NewReader("")
		} else {
			input = decoder
			closeDecoder = func() { _ = decoder.Close() }
		}
	}
	if closeDecoder != nil {
		defer closeDecoder()
	}
	requestBody, errRead := io.ReadAll(input)
	if errRead != nil {
		item.BodyOmittedReason = "content_encoding"
		requestBody = nil
	}
	sanitized, omitted := sanitizeCompleteActivityJSON(requestBody)
	if len(requestBody) > 0 {
		if omitted != "" {
			item.BodyOmittedReason = omitted
		} else {
			item.BodyOmittedReason = ""
		}
		if model := gjson.GetBytes(requestBody, "model").String(); model != "" {
			item.Model = activityText(model, 256)
		}
		if item.SessionID == "" {
			item.SessionID, item.SessionSource = reliableBodySession(requestBody)
		}
		if item.PreviousResponseID == "" {
			item.PreviousResponseID = activityText(gjson.GetBytes(requestBody, "previous_response_id").String(), 256)
		}
	}
	requestBody = nil
	if item.SessionID == "" && item.PreviousResponseID != "" {
		sessionID, _, err := w.db.FindResponseSession(ctx, item.UserID, item.PreviousResponseID)
		if err == nil {
			item.SessionID, item.SessionSource = sessionID, "previous_response_id"
		}
	}
	if len(sanitized) > 0 {
		item.BodyPreview, item.BodyTruncated, _ = requestBodyPreviewUnbounded(sanitized)
		item.PromptPreview = promptPreview(sanitized)
		count, err := w.saveContent(ctx, item.ID, "request", "json", sanitized, 0)
		if err != nil {
			return err
		}
		item.RequestContentBytes = count
	}
	sanitized = nil
	responseReader, err := newActivityDirectionReader(path, w.aead, "response")
	if err != nil {
		return err
	}
	defer func() { _ = responseReader.Close() }()
	if err := w.saveResponseContent(ctx, item, responseReader, responseFormat); err != nil {
		return err
	}
	if item.SessionID == "" && item.ResponseID != "" {
		item.SessionID = "response:" + item.ResponseID
		item.SessionSource = "response_id"
	}
	state := "interrupted"
	if complete && item.StatusCode != 499 {
		state = "complete"
	}
	if err := w.db.CompleteRequestActivityContent(ctx, *item, state); err != nil {
		return err
	}
	if err := w.db.ReconcileRequestSessions(ctx, item.ID); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return errors.New("activity recovery close failed")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("activity recovery cleanup failed")
	}
	return syncActivityDirectory(w.directory)
}

func (w *requestActivityWriter) saveContent(ctx context.Context, id, direction, format string, body []byte, part int64) (int64, error) {
	total := int64(len(body))
	for len(body) > 0 {
		size := min(len(body), 64<<10)
		for size < len(body) && !utf8.RuneStart(body[size]) {
			size--
		}
		if err := w.db.SaveRequestContentChunk(ctx, id, direction, format, part, string(body[:size])); err != nil {
			return 0, err
		}
		part++
		body = body[size:]
	}
	return total, nil
}

func sanitizeCompleteActivityJSON(body []byte) ([]byte, string) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, "empty_body"
	}
	if !utf8.Valid(body) {
		return []byte(`{"_omitted":"invalid_utf8"}`), "invalid_json"
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return []byte(`{"_omitted":"invalid_json"}`), "invalid_json"
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return []byte(`{"_omitted":"invalid_json"}`), "invalid_json"
	}
	value, _ = redactRequestValue(value, -10000)
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"_omitted":"invalid_json"}`), "invalid_json"
	}
	return encoded, ""
}

func requestBodyPreviewUnbounded(body []byte) (string, bool, string) {
	if len(body) <= MaxRequestCapturePreviewBytes {
		return string(body), false, ""
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return `{"_preview":"Full content available below"}`, true, ""
	}
	budget := 32 << 10
	encoded, err := json.Marshal(map[string]any{"_preview": "Full content available below", "latest_user_content": compactRequestValue(latestUserContent(value), &budget, 0)})
	if err != nil || len(encoded) > MaxRequestCapturePreviewBytes {
		return `{"_preview":"Full content available below"}`, true, ""
	}
	return string(encoded), true, ""
}

func promptPreview(body []byte) string {
	var object map[string]any
	if json.Unmarshal(body, &object) != nil {
		return ""
	}
	content := latestUserContent(object)
	if message, ok := content.(map[string]any); ok {
		for _, key := range []string{"content", "parts", "text"} {
			if value, exists := message[key]; exists {
				content = value
				break
			}
		}
	}
	if text, ok := content.(string); ok {
		return activityText(text, 2048)
	}
	encoded, _ := json.Marshal(content)
	return activityText(string(encoded), 2048)
}

func reliableBodySession(body []byte) (string, string) {
	if id, _, _ := cliproxysession.ClaudeMetadataIdentities(body); id != "" && validText(id, 512) {
		return id, "body:claude_metadata_session"
	}
	for _, path := range []string{"conversation.id", "conversation_id", "thread_id", "session_id", "sessionId", "metadata.session_id"} {
		value := gjson.GetBytes(body, path)
		if value.Type == gjson.String && validText(value.String(), 512) && value.String() != "" {
			return value.String(), "body:" + path
		}
	}
	return "", ""
}

func sanitizedResponseContent(body []byte, format string) ([]byte, string) {
	if len(body) == 0 {
		return nil, "json"
	}
	if format == "sse" || bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) || bytes.HasPrefix(bytes.TrimSpace(body), []byte("event:")) {
		reader := bufio.NewReader(bytes.NewReader(body))
		var output, event bytes.Buffer
		flush := func() {
			payload := bytes.TrimSpace(event.Bytes())
			if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
				sanitized, _ := sanitizeCompleteActivityJSON(payload)
				output.Write(sanitized)
				output.WriteByte('\n')
			}
			event.Reset()
		}
		for {
			line, err := reader.ReadString('\n')
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				flush()
			} else if strings.HasPrefix(line, "data:") {
				if event.Len() > 0 {
					event.WriteByte('\n')
				}
				event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			if err != nil {
				flush()
				break
			}
		}
		return output.Bytes(), "jsonl"
	}
	if format == "ws" {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var output bytes.Buffer
		for {
			var value any
			if err := decoder.Decode(&value); err != nil {
				if err != io.EOF {
					output.WriteString(`{"_omitted":"incomplete_websocket_frame"}` + "\n")
				}
				break
			}
			value, _ = redactRequestValue(value, -10000)
			encoded, _ := json.Marshal(value)
			output.Write(encoded)
			output.WriteByte('\n')
		}
		return output.Bytes(), "jsonl"
	}
	sanitized, _ := sanitizeCompleteActivityJSON(body)
	return sanitized, "json"
}

func capturedResponseID(body []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var id string
	for {
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			break
		}
		for _, path := range []string{"response.id", "id"} {
			value := gjson.GetBytes(raw, path).String()
			if strings.HasPrefix(value, "resp_") {
				id = activityText(value, 256)
			}
		}
	}
	return id
}
