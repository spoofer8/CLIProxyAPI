package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// Final model inspection is bounded even for non-replayable request bodies.
// Legacy requests return before this limit or any body inspection is applied.
const maxOutboundPolicyBodyBytes = 64 << 20

// AuthorizeOutboundRequest checks the final serialized payload after translator
// and payload-config rewrites. Visible aliases and the selected provider remain
// the immutable values supplied by the execution boundary.
func AuthorizeOutboundRequest(ctx context.Context, payload []byte, requestURL string) error {
	if hooks, ok := sdkaccess.RequestHooksFromContext(ctx); !ok || hooks.Authorize == nil {
		return nil
	}
	if len(payload) > maxOutboundPolicyBodyBytes {
		return outboundInspectionError()
	}
	target, _ := sdkaccess.PolicyTargetFromContext(ctx)
	target.PayloadModel = ""
	if raw := bytes.TrimSpace(payload); len(raw) > 0 && raw[0] == '{' {
		// Duplicate routing selectors are ambiguous across upstream parsers.
		// Decode only root keys; model strings in user/tool content are unrelated.
		if !json.Valid(raw) {
			return outboundInspectionError()
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		_, _ = decoder.Token()
		seenModel := false
		for decoder.More() {
			name, errName := decoder.Token()
			var value json.RawMessage
			if errName != nil || decoder.Decode(&value) != nil {
				return outboundInspectionError()
			}
			if name != "model" {
				if key, ok := name.(string); ok && strings.EqualFold(key, "model") {
					// Some compatibility servers decode into Go structs, where
					// differently cased keys also select the model.
					return outboundInspectionError()
				}
				continue
			}
			if seenModel {
				return outboundInspectionError()
			}
			seenModel = true
			if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				if errModel := json.Unmarshal(value, &target.PayloadModel); errModel != nil {
					return outboundInspectionError()
				}
			}
		}
	}
	if requestURL != "" {
		parsed, errURL := url.Parse(requestURL)
		if errURL != nil {
			return outboundInspectionError()
		}
		// Gemini/Vertex and Azure can select a deployment in the URL rather
		// than the body. These physical names supplement deny checks only.
		escapedPath := parsed.EscapedPath()
		for _, marker := range []string{"/models/", "/deployments/"} {
			if index := strings.Index(escapedPath, marker); index >= 0 {
				name := escapedPath[index+len(marker):]
				if marker == "/deployments/" {
					name = strings.SplitN(name, "/", 2)[0]
				} else {
					// Vertex resource IDs can themselves equal "models" before
					// the actual publisher model collection. Recognize that
					// collection while preserving all subsequent namespace parts.
					parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
					resource := 0
					if len(parts) > 0 && strings.HasPrefix(parts[0], "v") {
						resource = 1
					}
					if len(parts) > resource+7 && parts[resource] == "projects" && parts[resource+2] == "locations" && parts[resource+4] == "publishers" && parts[resource+6] == "models" {
						name = strings.Join(parts[resource+7:], "/")
					}
					if action := strings.LastIndexByte(name, ':'); action >= 0 {
						name = name[:action]
					}
				}
				name, errName := url.PathUnescape(name)
				if errName != nil {
					return outboundInspectionError()
				}
				if name != "" {
					target.DenyModels = append(target.DenyModels, name)
				}
			}
		}
	}
	return sdkaccess.AuthorizeRequest(ctx, target)
}

type outboundInspectionFailure struct{}

func (outboundInspectionFailure) Error() string   { return "Final request model could not be authorized" }
func (outboundInspectionFailure) StatusCode() int { return http.StatusForbidden }
func (outboundInspectionFailure) ResponseBody() []byte {
	return []byte(`{"error":{"message":"Final request model could not be authorized","type":"permission_error","code":"model_not_permitted"}}`)
}

func outboundInspectionError() error {
	return &sdkaccess.PolicyError{Cause: outboundInspectionFailure{}}
}

type replayPolicyBody struct {
	io.Reader
	io.Closer
}

func authorizeOutboundHTTP(req *http.Request) error {
	if req == nil {
		return nil
	}
	if hooks, ok := sdkaccess.RequestHooksFromContext(req.Context()); !ok || hooks.Authorize == nil {
		return nil
	}
	if req.ContentLength > maxOutboundPolicyBodyBytes {
		return outboundInspectionError()
	}
	var payload []byte
	if req.Body != nil && req.Body != http.NoBody {
		reader := req.Body
		if req.GetBody != nil {
			var errBody error
			reader, errBody = req.GetBody()
			if errBody != nil {
				return outboundInspectionError()
			}
		}
		var errRead error
		payload, errRead = io.ReadAll(io.LimitReader(reader, maxOutboundPolicyBodyBytes+1))
		if req.GetBody != nil {
			_ = reader.Close()
		} else {
			// Preserve ownership/close behavior of a non-replayable source.
			req.Body = &replayPolicyBody{Reader: bytes.NewReader(payload), Closer: reader}
		}
		if errRead != nil || len(payload) > maxOutboundPolicyBodyBytes {
			return outboundInspectionError()
		}
	}
	requestURL := ""
	if req.URL != nil {
		requestURL = req.URL.String()
	}
	return AuthorizeOutboundRequest(req.Context(), payload, requestURL)
}

type outboundPolicyTransport struct{ base http.RoundTripper }

func (t outboundPolicyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if errPolicy := authorizeOutboundHTTP(req); errPolicy != nil {
		if req != nil && req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errPolicy
	}
	return t.base.RoundTrip(req)
}

// GuardHTTPClient also covers token-count clients which do not create usage
// reporters. Reporters replace this wrapper with their own final-send check.
func GuardHTTPClient(ctx context.Context, client *http.Client) *http.Client {
	if client == nil {
		return nil
	}
	if hooks, ok := sdkaccess.RequestHooksFromContext(ctx); !ok || hooks.Authorize == nil {
		return client
	}
	if _, guarded := client.Transport.(outboundPolicyTransport); guarded {
		return client
	}
	guarded := *client
	transport := guarded.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	guarded.Transport = outboundPolicyTransport{base: transport}
	return &guarded
}
