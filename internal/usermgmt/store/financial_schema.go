package store

var financialSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS cpa_financial_settings (singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton), activated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`INSERT INTO cpa_financial_settings(singleton) VALUES(true) ON CONFLICT DO NOTHING`,
	`CREATE TABLE IF NOT EXISTS cpa_user_budgets (user_id TEXT PRIMARY KEY REFERENCES cpa_users(id) ON DELETE CASCADE, lifetime_usd NUMERIC(38,18) CHECK(lifetime_usd>=0), daily_usd NUMERIC(38,18) CHECK(daily_usd>=0), weekly_usd NUMERIC(38,18) CHECK(weekly_usd>=0), monthly_usd NUMERIC(38,18) CHECK(monthly_usd>=0), updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`CREATE TABLE IF NOT EXISTS cpa_model_prices (id TEXT PRIMARY KEY, provider TEXT NOT NULL, model TEXT NOT NULL, rates JSONB NOT NULL, source TEXT NOT NULL, source_url TEXT NOT NULL DEFAULT '', manual BOOLEAN NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), retired_at TIMESTAMPTZ)`,
	`CREATE TABLE IF NOT EXISTS cpa_current_prices (provider TEXT NOT NULL, model TEXT NOT NULL, manual BOOLEAN NOT NULL, price_id TEXT NOT NULL REFERENCES cpa_model_prices(id), PRIMARY KEY(provider,model,manual))`,
	`CREATE TABLE IF NOT EXISTS cpa_financial_requests (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES cpa_users(id) ON DELETE CASCADE, model TEXT NOT NULL DEFAULT '', at TIMESTAMPTZ NOT NULL, completed_at TIMESTAMPTZ, attempted BOOLEAN NOT NULL DEFAULT false, state TEXT NOT NULL DEFAULT 'in_progress' CHECK(state IN ('in_progress','completed','unresolved','resolved')), reason TEXT NOT NULL DEFAULT '')`,
	`CREATE INDEX IF NOT EXISTS cpa_financial_requests_user_state ON cpa_financial_requests(user_id,state)`,
	`CREATE TABLE IF NOT EXISTS cpa_cost_events (id TEXT PRIMARY KEY, request_id TEXT NOT NULL REFERENCES cpa_financial_requests(id) ON DELETE CASCADE, user_id TEXT NOT NULL REFERENCES cpa_users(id) ON DELETE CASCADE, provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', alias TEXT NOT NULL DEFAULT '', at TIMESTAMPTZ NOT NULL, cost_usd NUMERIC(38,18) CHECK(cost_usd>=0), status TEXT NOT NULL CHECK(status IN ('priced','unresolved','resolved')), reason TEXT NOT NULL DEFAULT '', price_id TEXT, token_breakdown JSONB NOT NULL DEFAULT '{}', price_snapshot JSONB NOT NULL DEFAULT '{}', resolution_note TEXT NOT NULL DEFAULT '')`,
	`CREATE INDEX IF NOT EXISTS cpa_cost_events_user_at ON cpa_cost_events(user_id,at)`,
	`CREATE INDEX IF NOT EXISTS cpa_cost_events_request ON cpa_cost_events(request_id)`,
}
