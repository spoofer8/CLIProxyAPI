package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type PriceRates struct {
	InputUSD      string  `json:"input_usd_per_million"`
	OutputUSD     string  `json:"output_usd_per_million"`
	CacheReadUSD  *string `json:"cache_read_usd_per_million"`
	CacheWriteUSD *string `json:"cache_write_usd_per_million"`
	ReasoningUSD  *string `json:"reasoning_usd_per_million"`
}

func (p *PriceRates) Validate() error {
	for _, v := range []*string{&p.InputUSD, &p.OutputUSD} {
		n, e := NormalizeRate(*v)
		if e != nil {
			return e
		}
		*v = n
	}
	for _, v := range []**string{&p.CacheReadUSD, &p.CacheWriteUSD, &p.ReasoningUSD} {
		if *v != nil {
			n, e := NormalizeRate(**v)
			if e != nil {
				return e
			}
			*v = &n
		}
	}
	return nil
}

type ContextPriceTier struct {
	AboveTokens int64 `json:"above_tokens"`
	PriceRates
}

type ModelPrice struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	PriceRates
	ContextTiers []ContextPriceTier `json:"context_tiers,omitempty"`
	Source       string             `json:"source"`
	SourceURL    string             `json:"source_url"`
	Manual       bool               `json:"manual"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

func (s *Store) SavePrice(ctx context.Context, p ModelPrice) error {
	if p.ID == "" || p.Provider == "" || p.Model == "" {
		return errors.New("price requires ID, provider and model")
	}
	if e := p.PriceRates.Validate(); e != nil {
		return e
	}
	data, e := json.Marshal(struct {
		PriceRates
		ContextTiers []ContextPriceTier `json:"context_tiers,omitempty"`
	}{p.PriceRates, p.ContextTiers})
	if e != nil {
		return e
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	return s.Transaction(ctx, func(tx *Store) error {
		if _, e := tx.query().ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1129333078))`, p.Provider+"/"+p.Model+fmt.Sprint(p.Manual)); e != nil {
			return domainError("lock price version", e)
		}
		_, e := tx.query().ExecContext(ctx, `UPDATE cpa_model_prices SET retired_at=$4 WHERE id IN (SELECT price_id FROM cpa_current_prices WHERE provider=$1 AND model=$2 AND manual=$3)`, p.Provider, p.Model, p.Manual, p.UpdatedAt)
		if e != nil {
			return domainError("retire price version", e)
		}
		_, e = tx.query().ExecContext(ctx, `INSERT INTO cpa_model_prices(id,provider,model,rates,source,source_url,manual,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.ID, p.Provider, p.Model, data, p.Source, p.SourceURL, p.Manual, p.UpdatedAt)
		if e != nil {
			return domainError("save price version", e)
		}
		_, e = tx.query().ExecContext(ctx, `INSERT INTO cpa_current_prices(provider,model,manual,price_id) VALUES($1,$2,$3,$4) ON CONFLICT(provider,model,manual) DO UPDATE SET price_id=EXCLUDED.price_id`, p.Provider, p.Model, p.Manual, p.ID)
		return domainError("activate price version", e)
	})
}
func (s *Store) GetPriceAt(ctx context.Context, provider, model string, at time.Time) (ModelPrice, error) {
	var p ModelPrice
	var rates []byte
	e := s.query().QueryRowContext(ctx, `SELECT id,provider,model,rates,source,source_url,manual,updated_at FROM cpa_model_prices WHERE provider=$1 AND model=$2 AND updated_at<=$3 AND (retired_at IS NULL OR retired_at>$3) ORDER BY manual DESC,updated_at DESC LIMIT 1`, provider, model, at).Scan(&p.ID, &p.Provider, &p.Model, &rates, &p.Source, &p.SourceURL, &p.Manual, &p.UpdatedAt)
	if e != nil {
		return p, domainError("get model price", e)
	}
	e = decodeStoredPrice(rates, &p)
	return p, e
}
func (s *Store) ListPrices(ctx context.Context) ([]ModelPrice, error) {
	rows, e := s.query().QueryContext(ctx, `SELECT DISTINCT ON(p.provider,p.model) p.id,p.provider,p.model,p.rates,p.source,p.source_url,p.manual,p.updated_at FROM cpa_current_prices c JOIN cpa_model_prices p ON p.id=c.price_id ORDER BY p.provider,p.model,p.manual DESC`)
	if e != nil {
		return nil, domainError("list prices", e)
	}
	defer rows.Close()
	result := []ModelPrice{}
	for rows.Next() {
		var p ModelPrice
		var rates []byte
		if e = rows.Scan(&p.ID, &p.Provider, &p.Model, &rates, &p.Source, &p.SourceURL, &p.Manual, &p.UpdatedAt); e != nil {
			return nil, domainError("read price", e)
		}
		if e = decodeStoredPrice(rates, &p); e != nil {
			return nil, e
		}
		result = append(result, p)
	}
	return result, domainError("read prices", rows.Err())
}
func (s *Store) DeletePriceOverride(ctx context.Context, provider, model string) error {
	return s.Transaction(ctx, func(tx *Store) error {
		_, e := tx.query().ExecContext(ctx, `UPDATE cpa_model_prices SET retired_at=now() WHERE id IN (SELECT price_id FROM cpa_current_prices WHERE provider=$1 AND model=$2 AND manual=true)`, provider, model)
		if e != nil {
			return domainError("retire price override", e)
		}
		_, e = tx.query().ExecContext(ctx, `DELETE FROM cpa_current_prices WHERE provider=$1 AND model=$2 AND manual=true`, provider, model)
		return domainError("delete price override", e)
	})
}

func decodeStoredPrice(data []byte, p *ModelPrice) error {
	var stored struct {
		PriceRates
		ContextTiers []ContextPriceTier `json:"context_tiers,omitempty"`
	}
	if e := json.Unmarshal(data, &stored); e != nil {
		return e
	}
	p.PriceRates = stored.PriceRates
	p.ContextTiers = stored.ContextTiers
	return nil
}
