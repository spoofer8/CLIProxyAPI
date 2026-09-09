package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type requestHookFailure struct{ error }

// IsRequestHookError identifies local admission failures that clients must see,
// even on transports that hide retryable upstream failures.
func IsRequestHookError(err error) bool {
	var failure *requestHookFailure
	return errors.As(err, &failure) || sdkaccess.IsPolicyError(err)
}

func beginRequestExecution(ctx context.Context) (func(), *interfaces.ErrorMessage) {
	release, err := sdkaccess.BeginRequestLease(ctx)
	if err != nil {
		return nil, requestHookError(err)
	}
	if hooks, ok := sdkaccess.RequestHooksFromContext(ctx); ok && hooks.Check != nil {
		if errCheck := hooks.Check(ctx); errCheck != nil {
			release()
			return nil, requestHookError(errCheck)
		}
	}
	return release, nil
}

func requestHookError(err error) *interfaces.ErrorMessage {
	var response interface {
		StatusCode() int
		ResponseBody() []byte
	}
	if errors.As(err, &response) {
		headers := http.Header{"Content-Type": []string{"application/json"}}
		var responseHeaders interface{ ResponseHeaders() http.Header }
		if errors.As(err, &responseHeaders) {
			for key, values := range responseHeaders.ResponseHeaders() {
				headers[key] = append([]string(nil), values...)
			}
		}
		return &interfaces.ErrorMessage{
			StatusCode: response.StatusCode(), Error: &requestHookFailure{err}, DirectResponse: true,
			Body:    append([]byte(nil), response.ResponseBody()...),
			Headers: headers,
		}
	}
	return executionErrorMessage(err)
}
