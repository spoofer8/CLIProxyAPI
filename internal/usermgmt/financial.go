package usermgmt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

type financialAdmission struct {
	billingProviders  map[string]string
	admitted          bool
	identityValidated bool
	permissions       *compiledPermissions
	approvedPrices    map[financialPriceTarget]bool
}

type financialPriceTarget struct{ provider, model string }

func financialTarget(target sdkaccess.PolicyTarget) financialPriceTarget {
	// ExecutionModel is the post-authentication alias resolution passed to the
	// executor and its usage reporter. PayloadModel may still be the incoming
	// public alias; it remains separately checked by permission deny rules.
	model := target.ExecutionModel
	if model == "" {
		model = target.PayloadModel
	}
	if model == "" {
		model = target.ResolvedModel
	}
	return financialPriceTarget{strings.ToLower(strings.TrimSpace(target.Provider)), billableModel(model)}
}

func capturedFinancialKey(ctx context.Context, scope *usageScope) (string, bool) {
	correlation, ok := ctx.Value(requestCaptureContextKey{}).(requestCaptureIdentity)
	identity, identified := sdkaccess.ResultFromContext(ctx)
	if !ok || !identified || correlation.scopeID != scope.id || correlation.userID != identity.Principal {
		return "", false
	}
	return financialKey(scope, correlation.id), true
}

func (r *Runtime) capturedAdmission(ctx context.Context, scope *usageScope) (financialAdmission, bool) {
	key, ok := capturedFinancialKey(ctx, scope)
	if !ok {
		return financialAdmission{}, false
	}
	r.financialMu.Lock()
	defer r.financialMu.Unlock()
	admission, exists := r.financialActive[key]
	return admission, exists
}

func (r *Runtime) snapshotCapturedPolicy(ctx context.Context, scope *usageScope, permissions *compiledPermissions) {
	key, ok := capturedFinancialKey(ctx, scope)
	if !ok {
		return
	}
	r.financialMu.Lock()
	defer r.financialMu.Unlock()
	if admission, exists := r.financialActive[key]; exists && !admission.identityValidated {
		admission.identityValidated = true
		admission.permissions = permissions
		r.financialActive[key] = admission
	}
}

func (r *Runtime) capturedPriceApproved(ctx context.Context, scope *usageScope, target financialPriceTarget) bool {
	key, ok := capturedFinancialKey(ctx, scope)
	if !ok {
		return false
	}
	r.financialMu.Lock()
	defer r.financialMu.Unlock()
	return r.financialActive[key].approvedPrices[target]
}

func (r *Runtime) approveCapturedPrice(ctx context.Context, scope *usageScope, target financialPriceTarget) {
	key, ok := capturedFinancialKey(ctx, scope)
	if !ok {
		return
	}
	r.financialMu.Lock()
	defer r.financialMu.Unlock()
	if admission, exists := r.financialActive[key]; exists {
		if admission.approvedPrices == nil {
			admission.approvedPrices = make(map[financialPriceTarget]bool)
		}
		admission.approvedPrices[target] = true
		r.financialActive[key] = admission
	}
}

func (r *Runtime) capturedBudgetAdmitted(ctx context.Context, scope *usageScope) bool {
	admission, ok := r.capturedAdmission(ctx, scope)
	return ok && admission.admitted
}

func (r *Runtime) admitCapturedBudget(ctx context.Context, scope *usageScope) {
	key, ok := capturedFinancialKey(ctx, scope)
	if !ok {
		return
	}
	r.financialMu.Lock()
	defer r.financialMu.Unlock()
	if admission, exists := r.financialActive[key]; exists {
		admission.admitted = true
		r.financialActive[key] = admission
	}
}

func financialKey(scope *usageScope, id string) string { return scope.id + ":" + id }
func (r *Runtime) beginFinancialRequest(ctx context.Context, scope *usageScope, id, userID, model string, at time.Time) error {
	if err := scope.store.BeginFinancialRequest(ctx, id, userID, model, at); err != nil {
		return quotaError(http.StatusServiceUnavailable, "Cost accounting is unavailable; request was not accepted", "server_error", "accounting_unavailable")
	}
	r.mu.RLock()
	providers := make(map[string]string, len(r.billingProviders))
	for key, value := range r.billingProviders {
		providers[key] = value
	}
	r.mu.RUnlock()
	r.financialMu.Lock()
	if r.financialActive == nil {
		r.financialActive = map[string]financialAdmission{}
	}
	r.financialActive[financialKey(scope, id)] = financialAdmission{billingProviders: providers}
	r.financialMu.Unlock()
	return nil
}
func (r *Runtime) finishFinancialRequest(ctx context.Context, scope *usageScope, id string, status int) error {
	r.financialMu.Lock()
	_, active := r.financialActive[financialKey(scope, id)]
	_, failed := r.financialFailed[financialKey(scope, id)]
	r.financialMu.Unlock()
	if !active {
		return nil
	}
	defer func() {
		r.financialMu.Lock()
		delete(r.financialActive, financialKey(scope, id))
		delete(r.financialFailed, financialKey(scope, id))
		r.financialMu.Unlock()
	}()
	if failed {
		if err := scope.store.MarkFinancialUnresolved(ctx, id, "accounting_write_failed"); err != nil {
			return err
		}
	}
	return scope.store.FinishFinancialRequest(ctx, id, status)
}
func (r *Runtime) financialWriteFailed(scope *usageScope, id string) {
	r.financialMu.Lock()
	if r.financialFailed == nil {
		r.financialFailed = map[string]struct{}{}
	}
	r.financialFailed[financialKey(scope, id)] = struct{}{}
	r.financialMu.Unlock()
}

func canonicalBillingProvider(provider, model string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "codex", "openai":
		return "openai"
	case "claude", "anthropic":
		return "anthropic"
	case "gemini", "gemini-cli", "aistudio", "google-ai-studio":
		return "google"
	case "grok", "xai":
		return "xai"
	case "kimi", "moonshot":
		return "moonshotai"
	case "qwen":
		return "alibaba"
	case "antigravity":
		// The router's provider name is not the billing vendor. Only explicit native
		// model families identify published list pricing; arbitrary aliases stay unknown.
		if strings.HasPrefix(model, "claude-") {
			return "anthropic"
		}
		if strings.HasPrefix(model, "gemini-") {
			return "google"
		}
	}
	return p
}
func billableModel(model string) string {
	return strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
}
func lookupModelPrice(ctx context.Context, db *store.Store, provider, model string, at time.Time) (store.ModelPrice, error) {
	model = billableModel(model)
	// An exact provider override has priority over a published-vendor mapping.
	p, e := db.GetPriceAt(ctx, strings.ToLower(strings.TrimSpace(provider)), model, at)
	if e == nil {
		return p, nil
	}
	if !errors.Is(e, store.ErrNotFound) {
		return p, e
	}
	canonical := canonicalBillingProvider(provider, model)
	if canonical != provider {
		return db.GetPriceAt(ctx, canonical, model, at)
	}
	return p, e
}
func priceUsage(record usage.Record, p store.ModelPrice) (string, string) {
	detail := usage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	b := detail.TokenBreakdown
	if !b.Valid() || b.Quality != usage.TokenAccountingQualityComplete || b.UnclassifiedTokens != 0 || b.TotalTokens == 0 {
		return "", "missing_or_unreliable_usage"
	}
	tier := strings.ToLower(strings.TrimSpace(record.ResponseServiceTier))
	if tier == "" {
		tier = strings.ToLower(strings.TrimSpace(record.ServiceTier))
	}
	if !p.Manual && tier != "" && tier != "default" && tier != "auto" && tier != "standard" {
		return "", "unsupported_service_tier"
	}
	rates := p.PriceRates
	for _, tier := range p.ContextTiers {
		if b.Input.TotalTokens > tier.AboveTokens {
			rates = tier.PriceRates
		}
	}
	buckets := []struct {
		tokens int64
		rate   *string
	}{{b.Input.UncachedTokens, &rates.InputUSD}, {b.Input.CacheReadTokens, rates.CacheReadUSD}, {b.Input.CacheWriteTokens, rates.CacheWriteUSD}, {b.Output.NonReasoningTokens, &rates.OutputUSD}, {b.Output.ReasoningTokens, rates.ReasoningUSD}}
	total := new(big.Int)
	for _, bucket := range buckets {
		if bucket.tokens == 0 {
			continue
		}
		if bucket.rate == nil {
			return "", "unpriced_token_bucket"
		}
		rate, e := store.ParseMoney(*bucket.rate)
		if e != nil {
			return "", "invalid_price"
		}
		rate.Mul(rate, big.NewInt(bucket.tokens))
		q, rem := new(big.Int).QuoRem(rate, big.NewInt(1_000_000), new(big.Int))
		if rem.Sign() != 0 {
			return "", "price_precision_exceeded"
		}
		total.Add(total, q)
	}
	value := store.FormatMoney(total)
	if _, e := store.ParseMoney(value); e != nil {
		return "", "cost_overflow"
	}
	return value, ""
}
func (r *Runtime) recordFinancialUsage(ctx context.Context, scope *usageScope, userID string, record usage.Record) {
	correlation, ok := ctx.Value(requestCaptureContextKey{}).(requestCaptureIdentity)
	if !ok || correlation.scopeID != scope.id || correlation.userID != userID {
		return
	}
	r.financialMu.Lock()
	admission, active := r.financialActive[financialKey(scope, correlation.id)]
	r.financialMu.Unlock()
	if !active {
		return
	}
	// Detached producer contexts outlive client cancellation, but forced service
	// cleanup still cancels database work. No best-effort queue owns this write.
	persistCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(scope.cleanupCtx, cancel)
	defer func() { stop(); cancel() }()
	at := record.RequestedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	detail := usage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	breakdown, _ := json.Marshal(detail.TokenBreakdown)
	id := record.EventID
	if id == "" { // Direct callers may replay a record outside the SDK manager.
		raw, _ := json.Marshal(struct {
			Request, Provider, Model, Auth string
			At                             time.Time
			Detail                         usage.Detail
		}{correlation.id, record.Provider, record.Model, record.AuthID, at, record.Detail})
		hash := sha256.Sum256(raw)
		id = hex.EncodeToString(hash[:])
	}
	event := store.CostEvent{ID: id, RequestID: correlation.id, UserID: userID, Provider: record.Provider, Model: billableModel(record.Model), Alias: record.Alias, At: time.Now().UTC(), Status: "unresolved", TokenBreakdown: breakdown}
	price, e := lookupModelPrice(persistCtx, scope.store, record.Provider, record.Model, at)
	if errors.Is(e, store.ErrNotFound) {
		if vendor := admission.billingProviders[strings.ToLower(record.Provider)]; vendor != "" {
			price, e = lookupModelPrice(persistCtx, scope.store, vendor, record.Model, at)
		}
	}
	switch {
	case e == nil:
		event.PriceID = &price.ID
		event.PriceSnapshot, _ = json.Marshal(price)
		cost, reason := priceUsage(record, price)
		event.Reason = reason
		if reason == "" {
			event.CostUSD = &cost
			event.Status = "priced"
		}
	case errors.Is(e, store.ErrNotFound):
		event.Reason = "unpriced_model"
	default:
		event.Reason = "pricing_unavailable"
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete || detail.TokenBreakdown.TotalTokens == 0 {
		event.Status = "unresolved"
		event.CostUSD = nil
		event.Reason = "missing_or_unreliable_usage"
	}
	if e := scope.store.RecordCostEvent(persistCtx, event); e != nil {
		r.financialWriteFailed(scope, correlation.id)
		log.WithFields(log.Fields{"request_id": correlation.id, "user_id": userID}).WithError(e).Error("Financial accounting requires administrator resolution")
	}
}
func (r *Runtime) checkModelPrice(ctx context.Context, target sdkaccess.PolicyTarget) (resultErr error) {
	identity, ok := sdkaccess.ResultFromContext(ctx)
	if !ok || identity.Provider != AccessProviderName {
		return nil
	}
	r.mu.RLock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	r.mu.RUnlock()
	if scope == nil {
		return unavailableScopeError()
	}
	priceTarget := financialTarget(target)
	if r.capturedPriceApproved(ctx, scope, priceTarget) {
		return nil
	}
	defer func() {
		if resultErr == nil && target.Provider != "" && target.Provider != "home" {
			if correlation, ok := ctx.Value(requestCaptureContextKey{}).(requestCaptureIdentity); ok {
				if e := scope.store.MarkFinancialAttempted(ctx, correlation.id); e != nil {
					resultErr = quotaError(503, "Cost accounting is unavailable", "server_error", "accounting_unavailable")
				} else {
					r.approveCapturedPrice(ctx, scope, priceTarget)
				}
			}
		}
	}()

	user, e := scope.store.GetUser(ctx, identity.Principal)
	if e != nil {
		return quotaError(503, "Cost accounting is unavailable", "server_error", "accounting_unavailable")
	}
	if user.SystemAdmin {
		return nil
	}
	limits, e := scope.store.GetBudget(ctx, identity.Principal)
	if e != nil {
		return quotaError(503, "Cost accounting is unavailable", "server_error", "accounting_unavailable")
	}
	if !limits.Limited() {
		return nil
	}
	model := priceTarget.model
	if target.Provider == "" || target.Provider == "home" {
		return nil
	}
	for _, provider := range r.billingProviderCandidates(target.Provider, model) {
		var price store.ModelPrice
		price, e = lookupModelPrice(ctx, scope.store, provider, model, time.Now())
		if e == nil {
			tier := strings.ToLower(usage.ServiceTierFromContext(ctx))
			if !price.Manual && tier != "" && tier != "auto" && tier != "default" && tier != "standard" {
				return quotaError(403, "This service tier needs a manual model price before a budgeted request can be accepted", "insufficient_quota", "model_unpriced")
			}
		}
		if e == nil || !errors.Is(e, store.ErrNotFound) {
			break
		}
	}
	if errors.Is(e, store.ErrNotFound) {
		return quotaError(403, "This model has no configured price. An administrator must enter its USD rates before it can be used with a budget.", "insufficient_quota", "model_unpriced")
	}
	if e != nil {
		return quotaError(503, "Model pricing is unavailable", "server_error", "accounting_unavailable")
	}
	return nil
}

// CompleteNonGeneratingRequest is invoked only by the local WebSocket prewarm
// branch after it has decided not to contact an upstream provider.
func (r *Runtime) CompleteNonGeneratingRequest(ctx context.Context) error {
	identity, ok := sdkaccess.ResultFromContext(ctx)
	if !ok || identity.Provider != AccessProviderName {
		return nil
	}
	r.mu.RLock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	r.mu.RUnlock()
	if scope == nil {
		return unavailableScopeError()
	}
	key, ok := capturedFinancialKey(ctx, scope)
	if !ok {
		return nil
	}
	r.financialMu.Lock()
	_, active := r.financialActive[key]
	r.financialMu.Unlock()
	if !active {
		return nil
	}
	correlation := ctx.Value(requestCaptureContextKey{}).(requestCaptureIdentity)
	if err := scope.store.CompleteNonGeneratingRequest(ctx, correlation.id); err != nil {
		return err
	}
	r.financialMu.Lock()
	delete(r.financialActive, key)
	delete(r.financialFailed, key)
	r.financialMu.Unlock()
	return nil
}
