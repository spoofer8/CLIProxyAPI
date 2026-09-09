package usermgmt

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func (r *Runtime) registerFinancialRoutes(group *gin.RouterGroup) {
	group.GET("/users/:id/budget", r.getBudget)
	group.PUT("/users/:id/budget", r.setBudget)
	group.GET("/users/:id/cost-events", r.listCostEvents)
	group.POST("/requests/:id/cost-resolution", r.resolveCost)
	group.GET("/pricing", r.listPrices)
	group.PUT("/pricing/override", r.setPriceOverride)
	group.DELETE("/pricing/override", r.deletePriceOverride)
	group.POST("/pricing/refresh", r.refreshPrices)
}
func (r *Runtime) getBudget(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var result BudgetStatus
	e := r.withStore(false, func(db *store.Store) error {
		var e error
		result, e = r.budgetStatus(c.Request.Context(), &usageScope{store: db}, id, time.Now())
		return e
	})
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, result)
}
func (r *Runtime) setBudget(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var b store.BudgetLimits
	if !decodeRequest(c, &b) {
		return
	}
	if e := b.Validate(); e != nil {
		badRequest(c, e.Error())
		return
	}
	e := r.mutate(c.Request.Context(), AuditEvent{Action: "budget.update", Target: id, Detail: map[string]any{"fields": []string{"lifetime_usd", "daily_usd", "weekly_usd", "monthly_usd"}}}, func(db *store.Store) error { return db.SetBudget(c.Request.Context(), id, b) })
	if e != nil {
		respondError(c, e)
		return
	}
	r.getBudget(c)
}
func (r *Runtime) listCostEvents(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}
	var events []store.CostEvent
	var total int
	e := r.withStore(false, func(db *store.Store) error {
		var e error
		events, total, e = db.ListCostEvents(c.Request.Context(), id, limit, offset)
		return e
	})
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, gin.H{"events": events, "total": total})
}
func (r *Runtime) resolveCost(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	r.financialMu.Lock()
	active := false
	for key := range r.financialActive {
		if strings.HasSuffix(key, ":"+id) {
			active = true
			break
		}
	}
	r.financialMu.Unlock()
	if active {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "The request is still running. Resolve its cost after it finishes."})
		return
	}

	var input struct {
		CostUSD string `json:"cost_usd"`
		Note    string `json:"note"`
	}
	if !decodeRequest(c, &input) {
		return
	}
	cost, e := store.NormalizeMoney(input.CostUSD)
	if e != nil {
		badRequest(c, e.Error())
		return
	}
	if strings.TrimSpace(input.Note) == "" || !validText(input.Note, 2048) {
		badRequest(c, "A resolution note is required (1..2048 bytes)")
		return
	}
	eventID, e := newID()
	if e != nil {
		respondError(c, e)
		return
	}
	e = r.mutate(c.Request.Context(), AuditEvent{Action: "cost.resolve", Target: id, Detail: map[string]any{"reason": "administrator_resolution"}}, func(db *store.Store) error {
		return db.ResolveRequestCost(c.Request.Context(), id, cost, input.Note, eventID)
	})
	if errors.Is(e, store.ErrResolutionBelowKnown) {
		badRequest(c, e.Error())
		return
	}
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, gin.H{"resolved": true})
}
func (r *Runtime) listPrices(c *gin.Context) {
	var prices []store.ModelPrice
	e := r.withStore(false, func(db *store.Store) error { var e error; prices, e = db.ListPrices(c.Request.Context()); return e })
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, gin.H{"prices": prices, "currency": "USD", "units": "USD per million tokens", "catalog_url": PublishedPricingURL})
}
func (r *Runtime) setPriceOverride(c *gin.Context) {
	var input struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		store.PriceRates
	}
	if !decodeRequest(c, &input) {
		return
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.Model = strings.TrimSpace(input.Model)
	if input.Provider == "" || input.Model == "" || !validText(input.Provider, 128) || !validText(input.Model, 256) {
		badRequest(c, "Provider and model are required")
		return
	}
	if e := input.PriceRates.Validate(); e != nil {
		badRequest(c, e.Error())
		return
	}
	id, e := newID()
	if e != nil {
		respondError(c, e)
		return
	}
	p := store.ModelPrice{ID: id, Provider: input.Provider, Model: input.Model, PriceRates: input.PriceRates, Source: "administrator override", Manual: true, UpdatedAt: time.Now().UTC()}
	e = r.mutate(c.Request.Context(), AuditEvent{Action: "price.override", Target: input.Provider + "/" + input.Model, Detail: map[string]any{"replacement": true}}, func(db *store.Store) error { return db.SavePrice(c.Request.Context(), p) })
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(http.StatusOK, gin.H{"price": p})
}
func (r *Runtime) deletePriceOverride(c *gin.Context) {
	provider, model := c.Query("provider"), c.Query("model")
	if provider == "" || model == "" || !validText(provider, 128) || !validText(model, 256) {
		badRequest(c, "Provider and model are required")
		return
	}
	e := r.mutate(c.Request.Context(), AuditEvent{Action: "price.restore_default", Target: provider + "/" + model, Detail: map[string]any{"cleared": true}}, func(db *store.Store) error { return db.DeletePriceOverride(c.Request.Context(), provider, model) })
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, gin.H{"deleted": true})
}
func (r *Runtime) refreshPrices(c *gin.Context) {
	e := r.withStore(false, func(db *store.Store) error { return refreshPublishedPrices(c.Request.Context(), db) })
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, gin.H{"updated": true})
}
