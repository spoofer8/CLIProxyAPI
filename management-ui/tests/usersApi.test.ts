import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import {
  normalizeManagedUser,
  normalizeUserRequest,
  normalizeUserSettings,
  requestQuery,
  usersApi,
} from '../src/services/api/users';
import { parseTokenLimit, quotaValue } from '../src/features/users/logic';

afterEach(() => {
  mockRestore?.();
  mockRestore = undefined;
});
let mockRestore: (() => void) | undefined;

describe('native users API contracts', () => {
  test('preserves inherit, unlimited, and explicit quota values', () => {
    expect(normalizeManagedUser({ monthly_token_limit: null }).monthlyTokenLimit).toBeNull();
    expect(normalizeManagedUser({ monthly_token_limit: 0 }).monthlyTokenLimit).toBe(0);
    expect(normalizeManagedUser({ monthly_token_limit: 12345 }).monthlyTokenLimit).toBe(12345);
    expect(quotaValue('inherit', '500')).toBeNull();
    expect(quotaValue('unlimited', '500')).toBe(0);
    expect(quotaValue('custom', '500')).toBe(500);
    for (const invalid of ['', '0', '-1', '1.2', 'Infinity', '9007199254740992']) {
      expect(quotaValue('custom', invalid)).toBeUndefined();
    }
    expect(parseTokenLimit('0', 0)).toBe(0);
  });

  test('normalizes hyphenated quota settings and monthly usage', () => {
    expect(
      normalizeUserSettings({
        quota: { 'default-monthly-tokens': 9876, enforce: true },
        request_activity: { enabled: true, retention_days: 30 },
      })
    ).toEqual({
      defaultMonthlyTokens: 9876,
      enforce: true,
      captureEnabled: true,
      retentionDays: 30,
    });
    expect(
      normalizeManagedUser({
        usage: {
          period: '2026-09',
          input_tokens: 3,
          output_tokens: 7,
          total_tokens: 10,
          request_count: 2,
        },
      }).usage
    ).toEqual({
      period: '2026-09',
      inputTokens: 3,
      outputTokens: 7,
      totalTokens: 10,
      requestCount: 2,
    });
  });

  test('unknown request usage stays unavailable while recorded zero remains zero', () => {
    const result = normalizeUserRequest({
      input_tokens: null,
      total_tokens: 0,
      duration_ms: 0,
      body_preview: '{"messages":[]}',
      body_truncated: true,
      body_omitted_reason: 'preview_limit',
    });
    expect(result.inputTokens).toBeNull();
    expect(result.totalTokens).toBe(0);
    expect(result.durationMs).toBe(0);
    expect(result.bodyPreview).toBe('{"messages":[]}');
    expect(result.bodyTruncated).toBe(true);
    expect(result.bodyOmittedReason).toBe('preview_limit');
  });

  test('cursor filtering uses backend parameter names and UTC timestamps', () => {
    expect(
      requestQuery({ model: '  gpt  ', status: 'error', since: '2026-09-01T13:30:00Z' }, 42)
    ).toEqual({
      limit: 50,
      before: 42,
      model: 'gpt',
      status: 'error',
      since: '2026-09-01T13:30:00.000Z',
    });
    expect(requestQuery({ model: '', status: '', since: '' })).toEqual({ limit: 50 });
  });

  test('quota writes send null explicitly and use the shared client with cancellation', async () => {
    const patch = spyOn(apiClient, 'patch').mockResolvedValue({
      id: 'user',
      monthly_token_limit: null,
    });
    mockRestore = () => patch.mockRestore();
    const controller = new AbortController();
    await usersApi.setQuota('user/name', null, controller.signal);
    expect(patch).toHaveBeenCalledWith(
      '/users/user%2Fname',
      { monthly_token_limit: null },
      { signal: controller.signal }
    );
  });

  test('request detail unwraps the backend request envelope', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({
      request: { id: 'r1', model: 'example', body_preview: 'retained' },
    });
    mockRestore = () => get.mockRestore();
    const result = await usersApi.request('request/id');
    expect(get).toHaveBeenCalledWith('/requests/request%2Fid', { signal: undefined });
    expect(result.id).toBe('r1');
    expect(result.bodyPreview).toBe('retained');
  });
});
