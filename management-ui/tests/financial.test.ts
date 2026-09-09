import { describe, expect, it } from 'bun:test';
import {
  normalizeUsd,
  formatUsd,
  normalizeBudget,
  serializeBudget,
  normalizePrice,
  serializePrice,
} from '../src/services/api/financial';

describe('exact USD editing and display', () => {
  it('preserves sub-cent charges and amounts beyond JS integer precision', () => {
    expect(normalizeUsd('00001.000000000000000001')).toBe('1.000000000000000001');
    expect(formatUsd('0.000000000000000001')).toBe('$0.000000000000000001');
    expect(formatUsd('99999999999999999999.123456789012345678')).toBe(
      '$99,999,999,999,999,999,999.123456789012345678'
    );
    expect(formatUsd('1.200')).toBe('$1.2');
    expect(formatUsd('bad')).toBe('—');
  });
  it('rejects negative, exponential and inexact pricing inputs', () => {
    for (const value of [
      '-1',
      '1e3',
      '.5',
      '1.',
      'Infinity',
      '1,000',
      ' 1',
      '100000000000000000000',
      '0.0000000000000000001',
    ]) {
      expect(normalizeUsd(value)).toBeNull();
    }
    expect(normalizeUsd('0.000000000001', 12)).toBe('0.000000000001');
    expect(normalizeUsd('0.0000000000001', 12)).toBeNull();
  });
});
describe('financial API normalization', () => {
  it('distinguishes zero cap from unlimited and preserves recovery details', () => {
    const normalized = normalizeBudget({
      budget: { daily_usd: '0', weekly_usd: null, monthly_usd: '50.5000', lifetime_usd: '100' },
      spend: {
        daily_usd: '0.000000000000000001',
        weekly_usd: '1',
        monthly_usd: '2',
        lifetime_usd: '3',
      },
      blocked: true,
      requires_admin_action: true,
      available_at: null,
      unresolved_requests: 1,
      unresolved_request_ids: ['request-id'],
      daily_resets_at: '2026-03-29T23:00:00Z',
      activated_at: '2026-01-01T00:00:00Z',
    });
    expect(normalized.budget.daily).toBe('0');
    expect(normalized.budget.weekly).toBeNull();
    expect(normalized.spend.daily).toBe('0.000000000000000001');
    expect(normalized.availableAt).toBeNull();
    expect(normalized.unresolvedRequestIds).toEqual(['request-id']);
    expect(serializeBudget(normalized.budget)).toEqual({
      lifetime_usd: '100',
      daily_usd: '0',
      weekly_usd: null,
      monthly_usd: '50.5',
    });
    expect(formatUsd(normalizeBudget({}).spend.daily)).toBe('—');
  });
  it('keeps unknown cache buckets distinct from explicitly free and preserves context tiers', () => {
    const price = normalizePrice({
      provider: 'openai',
      model: 'model',
      input_usd_per_million: '0.4',
      output_usd_per_million: '1.6',
      cache_read_usd_per_million: '0',
      cache_write_usd_per_million: null,
      reasoning_usd_per_million: '1.6',
      manual: false,
      context_tiers: [
        { above_tokens: 200000, input_usd_per_million: '0.8', output_usd_per_million: '2.4' },
      ],
    });
    expect(price.cacheRead).toBe('0');
    expect(price.cacheWrite).toBeNull();
    expect(price.contextTiers[0].input).toBe('0.8');
    expect(price.contextTiers[0].aboveTokens).toBe(200000);
    expect(serializePrice(price.provider, price.model, price)).toEqual({
      provider: 'openai',
      model: 'model',
      input_usd_per_million: '0.4',
      output_usd_per_million: '1.6',
      cache_read_usd_per_million: '0',
      cache_write_usd_per_million: null,
      reasoning_usd_per_million: '1.6',
    });
  });
});
