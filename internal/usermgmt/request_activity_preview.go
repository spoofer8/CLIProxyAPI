package usermgmt

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxRequestCaptureInspectBytes = 4 << 20
const MaxRequestCapturePreviewBytes = 256 << 10

// Credential-shaped fields are removed at every depth, including nested tool
// arguments. This is a request preview, not a copy of the original wire bytes.
func credentialField(key string) bool {
	key = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	for _, suffix := range []string{"apikey", "authorization", "password", "passwd", "secret", "token", "cookie", "cookies", "privatekey", "credential", "credentials", "sessionid"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func redactRequestValue(value any, depth int) (any, bool) {
	if depth > 64 {
		return "[omitted: nesting limit]", true
	}
	truncated := false
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if credentialField(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			if text, ok := child.(string); ok {
				lower := strings.ToLower(key)
				if lower == "file_data" || (lower == "data" && (typed["mimeType"] != nil || typed["mime_type"] != nil || typed["media_type"] != nil || typed["type"] == "base64")) {
					typed[key] = "[omitted: inline file data]"
					truncated = true
					continue
				}
				if lower == "arguments" && json.Valid([]byte(text)) {
					decoder := json.NewDecoder(strings.NewReader(text))
					decoder.UseNumber()
					var arguments any
					if decoder.Decode(&arguments) == nil {
						arguments, cut := redactRequestValue(arguments, depth+1)
						if encoded, err := json.Marshal(arguments); err == nil {
							typed[key], truncated = string(encoded), truncated || cut
							continue
						}
					}
				}
			}
			sanitized, cut := redactRequestValue(child, depth+1)
			typed[key], truncated = sanitized, truncated || cut
		}
	case []any:
		for index, child := range typed {
			sanitized, cut := redactRequestValue(child, depth+1)
			typed[index], truncated = sanitized, truncated || cut
		}
	case string:
		if strings.HasPrefix(typed, "data:") {
			return "[omitted: inline file data]", true
		}
		if strings.HasPrefix(typed, "http://") || strings.HasPrefix(typed, "https://") {
			if parsed, err := url.Parse(typed); err == nil {
				query, changed := parsed.Query(), parsed.User != nil
				parsed.User = nil
				for key := range query {
					if credentialField(key) || strings.EqualFold(key, "key") {
						query.Set(key, "[REDACTED]")
						changed = true
					}
				}
				if changed {
					parsed.RawQuery = query.Encode()
					return parsed.String(), false
				}
			}
		}
	}
	return value, truncated
}

func requestBodyPreview(body []byte, omitted string) (string, bool, string) {
	if omitted != "" {
		return "", false, safeBodyOmissionReason(omitted)
	}
	if len(body) == 0 {
		return "", false, "empty_body"
	}
	if len(body) > MaxRequestCaptureInspectBytes {
		return "", false, "inspection_limit"
	}
	if !utf8.Valid(body) {
		return "", false, "invalid_json"
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return "", false, "invalid_json"
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); errTrailing != io.EOF {
		return "", false, "invalid_json"
	}
	value, truncated := redactRequestValue(value, 0)
	encoded, errEncode := json.Marshal(value)
	if errEncode != nil {
		return "", false, "invalid_json"
	}
	if len(encoded) <= MaxRequestCapturePreviewBytes {
		return string(encoded), truncated, ""
	}
	// Long conversations commonly exhaust a prefix-only preview before reaching
	// the current question. Select the latest user turn before shrinking values.
	selected := value
	if object, ok := value.(map[string]any); ok {
		selected = map[string]any{"model": object["model"], "latest_user_content": latestUserContent(object)}
	}
	budget := 180 << 10
	encoded, errEncode = json.Marshal(map[string]any{"_preview": "Earlier or oversized content omitted", "content": compactRequestValue(selected, &budget, 0)})
	if errEncode != nil || len(encoded) > MaxRequestCapturePreviewBytes {
		return "", true, "preview_limit"
	}
	return string(encoded), true, ""
}

func safeBodyOmissionReason(reason string) string {
	switch reason {
	case "empty_body", "inspection_limit", "invalid_json", "unsupported_content_type", "body_read_error", "body_not_read", "content_encoding", "websocket":
		return reason
	default:
		return "body_unavailable"
	}
}

func latestUserContent(object map[string]any) any {
	for _, key := range []string{"messages", "input", "contents"} {
		switch content := object[key].(type) {
		case string:
			return content
		case []any:
			for index := len(content) - 1; index >= 0; index-- {
				if message, ok := content[index].(map[string]any); ok && message["role"] == "user" {
					return message
				}
			}
			if len(content) > 0 {
				return content[len(content)-1]
			}
		}
	}
	return object
}

func compactRequestValue(value any, budget *int, depth int) any {
	if *budget <= 0 || depth > 16 {
		return "[omitted]"
	}
	*budget -= 64
	switch typed := value.(type) {
	case string:
		limit := min(48<<10, max(*budget, 0))
		if len(typed) > limit {
			// Preserve the end, where the latest question often appears.
			typed = typed[len(typed)-limit:]
			for len(typed) > 0 && !utf8.RuneStart(typed[0]) {
				typed = typed[1:]
			}
			typed = "[earlier text omitted] " + typed
		}
		*budget -= len(typed)
		return typed
	case []any:
		start := max(0, len(typed)-8)
		items := make([]any, 0, len(typed)-start)
		for _, child := range typed[start:] {
			items = append(items, compactRequestValue(child, budget, depth+1))
		}
		return items
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		// Content fields have first claim on the remaining byte budget.
		priority := map[string]int{"latest_user_content": -7, "content": -6, "text": -5, "parts": -4, "input": -3, "role": -2, "model": -1}
		sort.SliceStable(keys, func(i, j int) bool { return priority[keys[i]] < priority[keys[j]] })
		result := make(map[string]any)
		for index, key := range keys {
			if index >= 32 || *budget <= 0 {
				break
			}
			if len(key) > 256 {
				continue
			}
			*budget -= len(key)
			result[key] = compactRequestValue(typed[key], budget, depth+1)
		}
		return result
	default:
		return value
	}
}
