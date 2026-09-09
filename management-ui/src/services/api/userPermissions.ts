import { apiClient } from './client';

export interface UserPermission {
  scope: 'model' | 'provider';
  value: string;
  effect: 'allow' | 'deny';
}

export function normalizePermissions(value: unknown): UserPermission[] {
  if (!Array.isArray(value)) throw new Error('Invalid permission response');
  return value.map((entry: unknown) => {
    const rule = entry as Partial<UserPermission> | null;
    if (
      !rule ||
      (rule.scope !== 'model' && rule.scope !== 'provider') ||
      (rule.effect !== 'allow' && rule.effect !== 'deny') ||
      typeof rule.value !== 'string' ||
      !rule.value
    ) {
      throw new Error('Invalid permission rule');
    }
    return { scope: rule.scope, effect: rule.effect, value: rule.value };
  });
}

export const userPermissionsApi = {
  async get(userId: string, signal?: AbortSignal) {
    const response = await apiClient.get<{ permissions: unknown }>(
      `/users/${encodeURIComponent(userId)}/permissions`,
      { signal }
    );
    return normalizePermissions(response.permissions);
  },
  async replace(userId: string, permissions: UserPermission[], signal?: AbortSignal) {
    await apiClient.put(
      `/users/${encodeURIComponent(userId)}/permissions`,
      { permissions },
      { signal }
    );
  },
};
