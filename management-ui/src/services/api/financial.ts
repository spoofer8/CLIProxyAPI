import { apiClient } from './client';

export const budgetPeriods = ['lifetime', 'daily', 'weekly', 'monthly'] as const;
export type BudgetPeriod = (typeof budgetPeriods)[number];
export type BudgetLimits = Record<BudgetPeriod, string | null>;
export interface BudgetStatus {
  budget: BudgetLimits;
  spend: Record<BudgetPeriod, string>;
  blocked: boolean;
  adminExempt: boolean;
  requiresAdminAction: boolean;
  availableAt: string | null;
  dailyResetsAt: string;
  activatedAt: string;
  unresolvedRequests: number;
  unresolvedRequestIds: string[];
  unpricedRequests: number;
  blockingLimits: string[];
}
export interface PriceRates {
  input: string;
  output: string;
  cacheRead: string | null;
  cacheWrite: string | null;
  reasoning: string | null;
}
export interface ModelPrice extends PriceRates {
  id: string;
  provider: string;
  model: string;
  source: string;
  sourceUrl: string;
  manual: boolean;
  updatedAt: string;
  contextTiers: (PriceRates & { aboveTokens: number })[];
}
export interface CostEvent {
  id: string;
  requestId: string;
  at: string;
  provider: string;
  model: string;
  alias: string;
  costUsd: string | null;
  status: string;
  reason: string;
  tokenBreakdown: unknown;
  priceSnapshot: unknown;
}
const object = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
const text = (value: unknown) => (typeof value === 'string' ? value : '');
const list = (value: unknown): unknown[] => (Array.isArray(value) ? value : []);
const integer = (value: unknown) =>
  typeof value === 'number' && Number.isFinite(value) ? value : 0;

/** Monetary values stay decimal strings through editing, requests and display. */
export function normalizeUsd(value: string, precision = 18): string | null {
  if (!new RegExp('^\\d{1,20}(?:\\.\\d{1,' + precision + '})?$').test(value)) return null;
  const [whole, fraction = ''] = value.split('.');
  const normalizedWhole = whole.replace(/^0+(?=\d)/, '');
  const normalizedFraction = fraction.replace(/0+$/, '');
  return normalizedWhole + (normalizedFraction ? '.' + normalizedFraction : '');
}
export function formatUsd(value: string): string {
  const normalized = normalizeUsd(value);
  if (normalized === null) return '—';
  const [whole, fraction] = normalized.split('.');
  return '$' + whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',') + (fraction ? '.' + fraction : '');
}
const money = (value: unknown, fallback = '') =>
  typeof value === 'string' ? (normalizeUsd(value) ?? fallback) : fallback;
const nullableMoney = (value: unknown) =>
  value == null ? null : typeof value === 'string' ? normalizeUsd(value) : null;
export function normalizeBudget(value: unknown): BudgetStatus {
  const raw = object(value),
    limits = object(raw.budget),
    spend = object(raw.spend);
  return {
    budget: Object.fromEntries(
      budgetPeriods.map((period) => [period, nullableMoney(limits[period + '_usd'])])
    ) as BudgetLimits,
    spend: Object.fromEntries(
      budgetPeriods.map((period) => [period, money(spend[period + '_usd'])])
    ) as Record<BudgetPeriod, string>,
    blocked: raw.blocked === true,
    adminExempt: raw.admin_exempt === true,
    requiresAdminAction: raw.requires_admin_action === true,
    availableAt: typeof raw.available_at === 'string' ? raw.available_at : null,
    dailyResetsAt: text(raw.daily_resets_at),
    activatedAt: text(raw.activated_at),
    unresolvedRequests: integer(raw.unresolved_requests),
    unresolvedRequestIds: list(raw.unresolved_request_ids).filter(
      (value): value is string => typeof value === 'string'
    ),
    unpricedRequests: integer(raw.unpriced_requests),
    blockingLimits: list(raw.blocking_limits).filter(
      (value): value is string => typeof value === 'string'
    ),
  };
}
export function serializeBudget(limits: BudgetLimits) {
  return Object.fromEntries(budgetPeriods.map((period) => [period + '_usd', limits[period]]));
}
function normalizeRates(raw: Record<string, unknown>): PriceRates {
  return {
    input: money(raw.input_usd_per_million, ''),
    output: money(raw.output_usd_per_million, ''),
    cacheRead: nullableMoney(raw.cache_read_usd_per_million),
    cacheWrite: nullableMoney(raw.cache_write_usd_per_million),
    reasoning: nullableMoney(raw.reasoning_usd_per_million),
  };
}
export function normalizePrice(value: unknown): ModelPrice {
  const raw = object(value);
  return {
    ...normalizeRates(raw),
    id: text(raw.id),
    provider: text(raw.provider),
    model: text(raw.model),
    source: text(raw.source),
    sourceUrl: text(raw.source_url),
    manual: raw.manual === true,
    updatedAt: text(raw.updated_at),
    contextTiers: list(raw.context_tiers).map((value) => {
      const tier = object(value);
      return { ...normalizeRates(tier), aboveTokens: integer(tier.above_tokens) };
    }),
  };
}
export function serializePrice(provider: string, model: string, rates: PriceRates) {
  return {
    provider,
    model,
    input_usd_per_million: rates.input,
    output_usd_per_million: rates.output,
    cache_read_usd_per_million: rates.cacheRead,
    cache_write_usd_per_million: rates.cacheWrite,
    reasoning_usd_per_million: rates.reasoning,
  };
}
const userPath = (id: string) => '/users/' + encodeURIComponent(id);
export const financialApi = {
  async budget(id: string, signal?: AbortSignal) {
    return normalizeBudget(await apiClient.get(userPath(id) + '/budget', { signal }));
  },
  async setBudget(id: string, limits: BudgetLimits, signal?: AbortSignal) {
    return normalizeBudget(
      await apiClient.put(userPath(id) + '/budget', serializeBudget(limits), { signal })
    );
  },
  async resolve(requestId: string, costUsd: string, note: string, signal?: AbortSignal) {
    await apiClient.post(
      '/requests/' + encodeURIComponent(requestId) + '/cost-resolution',
      { cost_usd: costUsd, note: note.trim() },
      { signal }
    );
  },
  async events(id: string, offset = 0, signal?: AbortSignal) {
    const raw = object(
      await apiClient.get(userPath(id) + '/cost-events', { params: { limit: 25, offset }, signal })
    );
    return {
      total: integer(raw.total),
      events: list(raw.events).map((value): CostEvent => {
        const event = object(value);
        return {
          id: text(event.id),
          requestId: text(event.request_id),
          at: text(event.at),
          provider: text(event.provider),
          model: text(event.model),
          alias: text(event.alias),
          costUsd: nullableMoney(event.cost_usd),
          status: text(event.status),
          reason: text(event.reason),
          tokenBreakdown: event.token_breakdown,
          priceSnapshot: event.price_snapshot,
        };
      }),
    };
  },
  async prices(signal?: AbortSignal) {
    const raw = object(await apiClient.get('/pricing', { signal }));
    return list(raw.prices).map(normalizePrice);
  },
  async setPrice(provider: string, model: string, rates: PriceRates, signal?: AbortSignal) {
    const raw = object(
      await apiClient.put('/pricing/override', serializePrice(provider, model, rates), { signal })
    );
    return normalizePrice(raw.price);
  },
  async resetPrice(provider: string, model: string, signal?: AbortSignal) {
    await apiClient.delete('/pricing/override', { params: { provider, model }, signal });
  },
  async refreshPrices(signal?: AbortSignal) {
    await apiClient.post('/pricing/refresh', {}, { signal });
  },
};
