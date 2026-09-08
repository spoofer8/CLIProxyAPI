package auth

import (
	"context"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// executeStreamWithUsageLease keeps the authenticated store alive for the raw
// upstream producer, even when a retry or client cancellation discards its stream.
func executeStreamWithUsageLease(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx = withExecutionPolicy(ctx, executor, auth, req)
	if errPolicy := authorizeExecutionRequest(ctx, executor, auth, req); errPolicy != nil {
		return nil, errPolicy
	}
	if hooks, ok := sdkaccess.RequestHooksFromContext(ctx); !ok || hooks.Begin == nil {
		response, err := executor.ExecuteStream(ctx, auth, req, opts)
		return response, sdkaccess.NormalizePolicyError(err)
	}
	release, errLease := sdkaccess.BeginRequestLease(ctx)
	if errLease != nil {
		return nil, errLease
	}
	stream, errStream := executor.ExecuteStream(ctx, auth, req, opts)
	errStream = sdkaccess.NormalizePolicyError(errStream)
	if errStream != nil || stream == nil || stream.Chunks == nil {
		release()
		return stream, errStream
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer release()
		defer close(out)
		forward := true
		for chunk := range stream.Chunks {
			if !forward {
				continue
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				forward = false
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: stream.Headers, Chunks: out}, nil
}
