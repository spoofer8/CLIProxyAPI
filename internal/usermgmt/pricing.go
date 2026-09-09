package usermgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	log "github.com/sirupsen/logrus"
)

const PublishedPricingURL = "https://models.dev/api.json"

type catalogCost struct {
	Input      json.Number     `json:"input"`
	Output     json.Number     `json:"output"`
	CacheRead  json.Number     `json:"cache_read"`
	CacheWrite json.Number     `json:"cache_write"`
	Tiers      []catalogTier   `json:"tiers"`
	LegacyTier json.RawMessage `json:"context_over_200k"`
}
type catalogTier struct {
	Input      json.Number `json:"input"`
	Output     json.Number `json:"output"`
	CacheRead  json.Number `json:"cache_read"`
	CacheWrite json.Number `json:"cache_write"`
	Tier       struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
	} `json:"tier"`
}
type catalogModel struct {
	ID         string      `json:"id"`
	Cost       catalogCost `json:"cost"`
	Modalities struct {
		Output []string `json:"output"`
	} `json:"modalities"`
}
type catalogProvider struct {
	Doc    string                  `json:"doc"`
	Models map[string]catalogModel `json:"models"`
}

func catalogNumber(number json.Number) (string, error) {
	r, ok := new(big.Rat).SetString(string(number))
	if !ok || r.Sign() < 0 {
		return "", store.ErrInvalidMoney
	}
	value := r.FloatString(12)
	reparsed, _ := new(big.Rat).SetString(value)
	if reparsed.Cmp(r) != 0 {
		return "", store.ErrInvalidMoney
	}
	return store.NormalizeRate(value)
}
func optionalCatalogNumber(number json.Number) (*string, error) {
	if number == "" {
		return nil, nil
	}
	v, e := catalogNumber(number)
	return &v, e
}
func catalogRates(c catalogCost, provider string) (store.PriceRates, error) {
	var p store.PriceRates
	var e error
	if p.InputUSD, e = catalogNumber(c.Input); e != nil {
		return p, e
	}
	if p.OutputUSD, e = catalogNumber(c.Output); e != nil {
		return p, e
	}
	if p.CacheReadUSD, e = optionalCatalogNumber(c.CacheRead); e != nil {
		return p, e
	}
	if p.CacheWriteUSD, e = optionalCatalogNumber(c.CacheWrite); e != nil {
		return p, e
	}
	// These vendors bill their reasoning output at the published output-token
	// rate; unknown provider semantics deliberately leave that bucket unpriced.
	switch provider {
	case "openai", "anthropic", "google", "xai", "deepseek", "moonshotai", "alibaba", "azure":
		v := p.OutputUSD
		p.ReasoningUSD = &v
	}
	return p, nil
}
func parsePublishedCatalog(reader io.Reader, at time.Time) ([]store.ModelPrice, error) {
	var providers map[string]catalogProvider
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if e := decoder.Decode(&providers); e != nil {
		return nil, e
	}
	if len(providers) < 1 {
		return nil, errors.New("empty published model catalog")
	}
	result := []store.ModelPrice{}
	for provider, p := range providers {
		for id, m := range p.Models {
			text := len(m.Modalities.Output) == 0
			for _, output := range m.Modalities.Output {
				if output == "text" {
					text = true
				}
			}
			if !text {
				continue
			}
			rates, e := catalogRates(m.Cost, provider)
			if e != nil {
				continue
			}
			// Legacy variable pricing without a precise threshold cannot be guessed.
			if len(m.Cost.LegacyTier) > 0 && string(m.Cost.LegacyTier) != "null" && len(m.Cost.Tiers) == 0 {
				continue
			}
			version, e := newID()
			if e != nil {
				return nil, e
			}
			price := store.ModelPrice{ID: version, Provider: provider, Model: id, PriceRates: rates, Source: "models.dev published provider list-price catalog", SourceURL: p.Doc, UpdatedAt: at}
			supported := true
			for _, tier := range m.Cost.Tiers {
				if tier.Tier.Type != "context" || tier.Tier.Size < 1 {
					supported = false
					break
				}
				rates, e := catalogRates(catalogCost{Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite}, provider)
				if e != nil {
					supported = false
					break
				}
				price.ContextTiers = append(price.ContextTiers, store.ContextPriceTier{AboveTokens: tier.Tier.Size, PriceRates: rates})
			}
			if !supported {
				continue
			}
			sort.Slice(price.ContextTiers, func(i, j int) bool { return price.ContextTiers[i].AboveTokens < price.ContextTiers[j].AboveTokens })
			result = append(result, price)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("catalog contains no supported token prices")
	}
	return result, nil
}
func refreshPublishedPrices(ctx context.Context, db *store.Store) error {
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, PublishedPricingURL, nil)
	if e != nil {
		return e
	}
	request.Header.Set("User-Agent", "CLIProxyAPI-Pricing/1.0")
	request.Header.Set("Accept", "application/json")
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		return errors.New("published price catalog could not be downloaded")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("published price catalog returned HTTP %d", response.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if e != nil {
		return errors.New("published price catalog could not be read")
	}
	if len(data) > 16<<20 {
		return errors.New("published price catalog exceeds allowed size")
	}
	prices, e := parsePublishedCatalog(strings.NewReader(string(data)), time.Now().UTC())
	if e != nil {
		return e
	}
	return db.Transaction(ctx, func(tx *store.Store) error {
		for _, price := range prices {
			old, e := tx.GetPriceAt(ctx, price.Provider, price.Model, price.UpdatedAt)
			if e == nil && !old.Manual {
				before, _ := json.Marshal(struct {
					Rates store.PriceRates
					Tiers []store.ContextPriceTier
				}{old.PriceRates, old.ContextTiers})
				after, _ := json.Marshal(struct {
					Rates store.PriceRates
					Tiers []store.ContextPriceTier
				}{price.PriceRates, price.ContextTiers})
				if string(before) == string(after) {
					continue
				}
			}
			if e := tx.SavePrice(ctx, price); e != nil {
				return e
			}
		}
		return nil
	})
}
func (r *Runtime) startPriceRefresh(scope *usageScope) {
	scope.priceRefreshDone = make(chan struct{})
	go func() {
		defer close(scope.priceRefreshDone)
		refresh := func() {
			refreshFunc := r.pricingRefresh
			if refreshFunc == nil {
				refreshFunc = refreshPublishedPrices
			}
			if e := refreshFunc(scope.cleanupCtx, scope.store); e != nil && scope.cleanupCtx.Err() == nil {
				log.WithError(e).Warn("Published model prices could not refresh; existing prices retained")
			}
		}
		refresh()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-scope.cleanupCtx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}
