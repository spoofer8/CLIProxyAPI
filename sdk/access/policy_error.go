package access

import (
	"context"
	"errors"
	"net/http"
)

// PolicyError makes a local authorization rejection terminal across upstream
// fallback and preserves its safe response at HTTP and WebSocket boundaries.
type PolicyError struct{ Cause error }

func (e *PolicyError) Error() string       { return e.Cause.Error() }
func (e *PolicyError) Unwrap() error       { return e.Cause }
func (*PolicyError) IsRequestScoped() bool { return true }
func (*PolicyError) IsRequestStop() bool   { return true }
func (*PolicyError) DirectResponse() bool  { return true }
func (e *PolicyError) StatusCode() int {
	var status interface{ StatusCode() int }
	if errors.As(e.Cause, &status) {
		return status.StatusCode()
	}
	return http.StatusForbidden
}
func (e *PolicyError) ResponseBody() []byte {
	var response interface{ ResponseBody() []byte }
	if errors.As(e.Cause, &response) {
		return append([]byte(nil), response.ResponseBody()...)
	}
	return []byte(`{"error":{"message":"This request is not permitted for this account","type":"permission_error","code":"model_not_permitted"}}`)
}
func IsPolicyError(err error) bool {
	var policy *PolicyError
	return errors.As(err, &policy)
}

// NormalizePolicyError drops transport wrappers that may include upstream URLs
// while preserving the typed, safe local policy response.
func NormalizePolicyError(err error) error {
	var policy *PolicyError
	if errors.As(err, &policy) {
		return policy
	}
	return err
}

func AuthorizeRequest(ctx context.Context, target PolicyTarget) error {
	if hooks, ok := RequestHooksFromContext(ctx); ok && hooks.Authorize != nil {
		if err := hooks.Authorize(ctx, target); err != nil {
			return &PolicyError{Cause: err}
		}
	}
	return nil
}
