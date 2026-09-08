package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type policyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f policyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func outboundTestContext(check func(sdkaccess.PolicyTarget) error) context.Context {
	ctx := sdkaccess.WithPolicyTarget(context.Background(), sdkaccess.PolicyTarget{
		RequestedModel: "visible-alias", ResolvedModel: "visible-alias", ExecutionModel: "upstream-original",
		Provider: "openai-compatible-test", DenyModels: []string{"intermediate-alias"},
	})
	return sdkaccess.WithRequestHooks(ctx, sdkaccess.RequestHooks{Authorize: func(_ context.Context, target sdkaccess.PolicyTarget) error {
		return check(target)
	}})
}

func TestOutboundFinalModelPreservesVisibleIdentityAndURLDenies(t *testing.T) {
	var checked sdkaccess.PolicyTarget
	ctx := outboundTestContext(func(target sdkaccess.PolicyTarget) error { checked = target; return nil })
	if err := AuthorizeOutboundRequest(ctx, []byte(`{"model":"rewritten","input":[{"model":"user-content"}]}`), "https://upstream.invalid/openai/deployments/physical-target/responses?api-key=private-value"); err != nil {
		t.Fatal(err)
	}
	if checked.RequestedModel != "visible-alias" || checked.ResolvedModel != "visible-alias" || checked.ExecutionModel != "upstream-original" || checked.Provider != "openai-compatible-test" || checked.PayloadModel != "rewritten" {
		t.Fatalf("outbound policy lost immutable target fields: %+v", checked)
	}
	if strings.Join(checked.DenyModels, ",") != "intermediate-alias,physical-target" {
		t.Fatalf("URL deployment was not included in deny-only targets: %v", checked.DenyModels)
	}
	original, _ := sdkaccess.PolicyTargetFromContext(ctx)
	if len(original.DenyModels) != 1 {
		t.Fatal("outbound check mutated the parent target")
	}
}

func TestOutboundRejectsDuplicateRootModelOnly(t *testing.T) {
	ctx := outboundTestContext(func(sdkaccess.PolicyTarget) error { return nil })
	for _, payload := range []string{
		`{"model":"allowed","model":"denied"}`,
		`{"model":"denied","\u006dodel":"allowed"}`,
		`{"model":null,"model":"allowed"}`,
		`{"model":"allowed","Model":"denied"}`,
		`{"MODEL":"allowed"}`,
	} {
		if err := AuthorizeOutboundRequest(ctx, []byte(payload), ""); !sdkaccess.IsPolicyError(err) {
			t.Fatal("ambiguous root model selectors were accepted")
		}
	}
	for _, payload := range []string{
		`{"model":"allowed","input":[{"model":"content","model":"other-content"}]}`,
	} {
		if err := AuthorizeOutboundRequest(ctx, []byte(payload), ""); err != nil {
			t.Fatal("unrelated content was treated as a routing selector")
		}
	}
}

func TestOutboundURLPreservesNamespacedModelSelectors(t *testing.T) {
	for _, test := range []struct{ requestURL, model string }{
		{"https://upstream.invalid/models/org/model:generateContent", "org/model"},
		{"https://upstream.invalid/models/org%2Fmodel:countTokens", "org/model"},
		{"https://upstream.invalid/openai/deployments/org%2Fmodel/responses", "org/model"},
		{"https://upstream.invalid/v1beta/models/org/models/private:generateContent", "org/models/private"},
		{"https://upstream.invalid/v1/projects/models/locations/us/publishers/google/models/org/models/private:generateContent", "org/models/private"},
	} {
		ctx := outboundTestContext(func(target sdkaccess.PolicyTarget) error {
			for _, model := range target.DenyModels {
				if model == test.model {
					return outboundInspectionFailure{}
				}
			}
			return nil
		})
		if err := AuthorizeOutboundRequest(ctx, []byte(`{}`), test.requestURL); !sdkaccess.IsPolicyError(err) {
			t.Fatal("namespaced URL model bypassed its deny rule")
		}
	}
}

type oversizedPolicyReader struct{ read int }

func (r *oversizedPolicyReader) Read(data []byte) (int, error) {
	clear(data)
	r.read += len(data)
	return len(data), nil
}
func (*oversizedPolicyReader) Close() error { return nil }

func TestOutboundInspectionHasABoundedRead(t *testing.T) {
	ctx := outboundTestContext(func(sdkaccess.PolicyTarget) error { return nil })
	reader := &oversizedPolicyReader{}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream.invalid/responses", reader)
	if err := authorizeOutboundHTTP(req); !sdkaccess.IsPolicyError(err) || reader.read != maxOutboundPolicyBodyBytes+1 {
		t.Fatalf("unbounded body inspection: read=%d error=%v", reader.read, err)
	}
	if err := AuthorizeOutboundRequest(ctx, make([]byte, maxOutboundPolicyBodyBytes+1), ""); !sdkaccess.IsPolicyError(err) {
		t.Fatal("oversized direct websocket payload was accepted")
	}
}

func TestOutboundHTTPDenialNeverSendsAndDoesNotExposeURL(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := outboundTestContext(func(target sdkaccess.PolicyTarget) error {
		if target.PayloadModel == "denied" {
			return outboundInspectionFailure{}
		}
		return nil
	})
	ctx = context.WithValue(ctx, "gin", ginCtx)
	ctx = cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
	calls := 0
	client := (&UsageReporter{}).TrackHTTPClient(&http.Client{Transport: policyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected transport call")
	})})
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.invalid/responses?key=DO-NOT-EXPOSE-THIS", strings.NewReader(`{"model":"denied"}`))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	_, errRequest = client.Do(req)
	if !sdkaccess.IsPolicyError(errRequest) || calls != 0 || cliproxyexecutor.UpstreamAttempted(ctx) {
		t.Fatal("denied final payload reached the upstream transport")
	}
	cfg := &config.Config{}
	cfg.RequestLog = true
	RecordAPIResponseError(ctx, cfg, errRequest)
	logged, _ := ginCtx.Get(apiResponseKey)
	if raw, ok := logged.([]byte); !ok || strings.Contains(string(raw), "DO-NOT-EXPOSE-THIS") || strings.Contains(string(raw), "upstream.invalid") {
		t.Fatal("policy error logging retained a URL-bearing transport wrapper")
	}
	reporter := &UsageReporter{}
	reporter.TrackFailure(ctx, &errRequest)
	if strings.Contains(errRequest.Error(), "DO-NOT-EXPOSE-THIS") || strings.Contains(errRequest.Error(), "upstream.invalid") {
		t.Fatal("executor policy error retained a URL-bearing transport wrapper")
	}
	if fail := failFromErrors(errRequest); fail.StatusCode != http.StatusForbidden || strings.Contains(fail.Body, "DO-NOT-EXPOSE-THIS") {
		t.Fatal("usage failure lost the safe local permission response")
	}
}

func TestOutboundFactoriesCoverCountsAndPreserveAllowedBody(t *testing.T) {
	for _, factory := range []struct {
		name string
		make func(context.Context) *http.Client
	}{
		{"proxy", func(ctx context.Context) *http.Client { return NewProxyAwareHTTPClient(ctx, nil, nil, 0) }},
		{"utls", func(ctx context.Context) *http.Client { return NewUtlsHTTPClient(ctx, nil, nil, 0) }},
	} {
		t.Run(factory.name, func(t *testing.T) {
			const body = `{"contents":[{"text":"unchanged"}],"model":"permitted"}`
			calls := 0
			transport := policyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				defer func() { _ = req.Body.Close() }()
				got, errRead := io.ReadAll(req.Body)
				if errRead != nil || string(got) != body {
					t.Error("policy inspection changed the non-replayable body")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
			})
			deny := false
			ctx := outboundTestContext(func(sdkaccess.PolicyTarget) error {
				if deny {
					return outboundInspectionFailure{}
				}
				return nil
			})
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport))
			client := factory.make(ctx)
			for _, rejected := range []bool{false, true} {
				deny = rejected
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream.invalid/models/permitted:countTokens", io.NopCloser(strings.NewReader(body)))
				response, err := client.Do(req)
				if rejected {
					if !sdkaccess.IsPolicyError(err) {
						t.Fatal("token-count client bypassed final authorization")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					_ = response.Body.Close()
				}
			}
			if calls != 1 {
				t.Fatalf("transport calls=%d, want one allowed request", calls)
			}
		})
	}
}

func TestOutboundLegacyHasNoInspectionOrClientChanges(t *testing.T) {
	client := &http.Client{}
	if GuardHTTPClient(context.Background(), client) != client || GuardHTTPClient(nil, client) != client {
		t.Fatal("legacy HTTP client was wrapped")
	}
	if err := AuthorizeOutboundRequest(context.Background(), []byte(`{"model":"x","model":"y"}`), "invalid%URL"); err != nil {
		t.Fatal("legacy request gained new payload validation")
	}
}
