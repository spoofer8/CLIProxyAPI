import { apiClient } from './client';

export interface MonthlyUsage {
  period: string;
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  requestCount: number;
}
export interface ManagedUser {
  id: string;
  email: string;
  displayName: string;
  role: string;
  status: string;
  systemAdmin: boolean;
  monthlyTokenLimit: number | null;
  usage?: MonthlyUsage;
}
export interface UserManagementSettings {
  defaultMonthlyTokens: number;
  enforce: boolean;
  captureEnabled: boolean;
  retentionDays: number;
}
export interface UserRequest {
  id: string;
  userId: string;
  at: string;
  method: string;
  path: string;
  model: string;
  statusCode: number;
  durationMs: number | null;
  provider: string;
  inputTokens: number | null;
  outputTokens: number | null;
  totalTokens: number | null;
}
export interface UserRequestDetail extends UserRequest {
  bodyPreview: string;
  bodyTruncated: boolean;
  bodyOmittedReason: string;
}
export interface UserApiKey {
  id: string;
  prefix: string;
  label: string;
  status: string;
}
export interface RequestFilters {
  model: string;
  status: string;
  since: string;
}

const record = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
const string = (value: unknown) => (typeof value === 'string' ? value : '');
const number = (value: unknown) =>
  typeof value === 'number' && Number.isFinite(value) ? value : 0;
const optionalNumber = (value: unknown) => (value == null ? null : number(value));
const list = (value: unknown) => (Array.isArray(value) ? value : []);

export function normalizeManagedUser(value: unknown): ManagedUser {
  const raw = record(value);
  const usage = record(raw.usage);
  return {
    id: string(raw.id),
    email: string(raw.email),
    displayName: string(raw.display_name),
    role: string(raw.role),
    status: string(raw.status),
    systemAdmin: raw.system_admin === true,
    monthlyTokenLimit: optionalNumber(raw.monthly_token_limit),
    ...(raw.usage
      ? {
          usage: {
            period: string(usage.period),
            inputTokens: number(usage.input_tokens),
            outputTokens: number(usage.output_tokens),
            totalTokens: number(usage.total_tokens),
            requestCount: number(usage.request_count),
          },
        }
      : {}),
  };
}
export function normalizeUserSettings(value: unknown): UserManagementSettings {
  const raw = record(value),
    quota = record(raw.quota),
    capture = record(raw.request_activity);
  return {
    defaultMonthlyTokens: number(quota['default-monthly-tokens']),
    enforce: quota.enforce === true,
    captureEnabled: capture.enabled === true,
    retentionDays: number(capture.retention_days),
  };
}
export function normalizeUserRequest(value: unknown): UserRequestDetail {
  const raw = record(value);
  return {
    id: string(raw.id),
    userId: string(raw.user_id),
    at: string(raw.at),
    method: string(raw.method),
    path: string(raw.path),
    model: string(raw.model),
    statusCode: number(raw.status_code),
    durationMs: optionalNumber(raw.duration_ms),
    provider: string(raw.provider),
    inputTokens: optionalNumber(raw.input_tokens),
    outputTokens: optionalNumber(raw.output_tokens),
    totalTokens: optionalNumber(raw.total_tokens),
    bodyPreview: string(raw.body_preview),
    bodyTruncated: raw.body_truncated === true,
    bodyOmittedReason: string(raw.body_omitted_reason),
  };
}
const normalizeKey = (value: unknown): UserApiKey => {
  const raw = record(value);
  return {
    id: string(raw.id),
    prefix: string(raw.key_prefix),
    label: string(raw.label),
    status: string(raw.status),
  };
};
export function requestQuery(filters: RequestFilters, before = 0) {
  return {
    limit: 50,
    ...(before > 0 ? { before } : {}),
    ...(filters.model.trim() ? { model: filters.model.trim() } : {}),
    ...(filters.status ? { status: filters.status } : {}),
    ...(filters.since ? { since: new Date(filters.since).toISOString() } : {}),
  };
}
const userPath = (id: string) => `/users/${encodeURIComponent(id)}`;

export const usersApi = {
  async list(offset = 0, signal?: AbortSignal) {
    const raw = record(await apiClient.get('/users', { params: { limit: 100, offset }, signal }));
    return { users: list(raw.users).map(normalizeManagedUser), total: number(raw.total) };
  },
  async get(id: string, signal?: AbortSignal) {
    return normalizeManagedUser(await apiClient.get(userPath(id), { signal }));
  },
  async create(displayName: string, email: string, signal?: AbortSignal) {
    return normalizeManagedUser(
      await apiClient.post(
        '/users',
        {
          display_name: displayName.trim(),
          email: email.trim(),
          role: 'user',
        },
        { signal }
      )
    );
  },
  async setQuota(id: string, monthlyTokenLimit: number | null, signal?: AbortSignal) {
    return normalizeManagedUser(
      await apiClient.patch(
        userPath(id),
        {
          monthly_token_limit: monthlyTokenLimit,
        },
        { signal }
      )
    );
  },
  async settings(signal?: AbortSignal) {
    return normalizeUserSettings(await apiClient.get('/user-management/settings', { signal }));
  },
  async saveSettings(defaultMonthlyTokens: number, enforce: boolean, signal?: AbortSignal) {
    await apiClient.put(
      '/user-management/settings',
      {
        default_monthly_tokens: defaultMonthlyTokens,
        enforce,
      },
      { signal }
    );
  },
  async requests(id: string, filters: RequestFilters, before = 0, signal?: AbortSignal) {
    const raw = record(
      await apiClient.get(`${userPath(id)}/requests`, {
        params: requestQuery(filters, before),
        signal,
      })
    );
    return {
      requests: list(raw.requests).map(normalizeUserRequest),
      nextBefore: number(raw.next_before),
    };
  },
  async request(id: string, signal?: AbortSignal) {
    const raw = record(await apiClient.get(`/requests/${encodeURIComponent(id)}`, { signal }));
    return normalizeUserRequest(raw.request);
  },
  async keys(id: string, offset = 0, signal?: AbortSignal) {
    const raw = record(
      await apiClient.get(`${userPath(id)}/keys`, {
        params: { limit: 100, offset },
        signal,
      })
    );
    return { keys: list(raw.keys).map(normalizeKey), total: number(raw.total) };
  },
  async issueKey(id: string, label: string, signal?: AbortSignal) {
    const raw = record(
      await apiClient.post(`${userPath(id)}/keys`, { label: label.trim() }, { signal })
    );
    return { secret: string(raw.key), key: normalizeKey(raw.api_key) };
  },
  async revokeKey(id: string, signal?: AbortSignal) {
    await apiClient.delete(`/keys/${encodeURIComponent(id)}`, { signal });
  },
};
