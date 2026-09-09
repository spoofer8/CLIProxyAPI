import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import axios from 'axios';
import { apiClient } from '../src/services/api/client';
import { modelsApi } from '../src/services/api/models';
import { normalizeManagedUser } from '../src/services/api/users';
import {
  normalizePermissions,
  userPermissionsApi,
  type UserPermission,
} from '../src/services/api/userPermissions';
import { replaceModelAllows } from '../src/features/users/modelPermissions';

const restrictions: UserPermission[] = [
  { scope: 'provider', value: 'azure', effect: 'allow' },
  { scope: 'model', value: 'restricted-*', effect: 'deny' },
];
let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
});

describe('user model selection', () => {
  test('all models removes only model allows, preserving other restrictions', () => {
    expect(
      replaceModelAllows(
        [...restrictions, { scope: 'model', value: 'old', effect: 'allow' }],
        'all',
        []
      )
    ).toEqual(restrictions);
  });
  test('selected IDs preserve case, provider restrictions and model deny rules', () => {
    const result = replaceModelAllows(restrictions, 'selected', ['ModelA', 'modela', 'ModelA']);
    expect(result).toEqual([
      ...restrictions,
      { scope: 'model', value: 'ModelA', effect: 'allow' },
      { scope: 'model', value: 'modela', effect: 'allow' },
    ]);
  });
  test('empty explicit selection and new wildcard grants cannot become unrestricted access', () => {
    expect(() => replaceModelAllows(restrictions, 'selected', [])).toThrow('empty_selection');
    expect(() => replaceModelAllows(restrictions, 'selected', ['*'], ['?'])).toThrow(
      'empty_selection'
    );
    const rules: UserPermission[] = [
      ...restrictions,
      { scope: 'model', value: 'existing-*', effect: 'allow' },
    ];
    expect(replaceModelAllows(rules, 'selected', [], ['existing-*'])).toEqual(rules);
    expect(replaceModelAllows(rules, 'selected', ['now-exact'], [])).toEqual([
      ...restrictions,
      { scope: 'model', value: 'now-exact', effect: 'allow' },
    ]);
  });
  test('exact deny cannot be overwritten and total rule limit includes preserved rules', () => {
    expect(() =>
      replaceModelAllows([{ scope: 'model', value: 'private', effect: 'deny' }], 'selected', [
        'private',
      ])
    ).toThrow('denied_model');
    expect(() =>
      replaceModelAllows(
        restrictions,
        'selected',
        Array.from({ length: 127 }, (_, i) => 'model-' + i)
      )
    ).toThrow('too_many_rules');
  });
  test('malformed permission responses fail instead of appearing unrestricted', () => {
    expect(() => normalizePermissions(undefined)).toThrow();
    expect(() =>
      normalizePermissions([{ scope: 'model', value: 'x', effect: 'unknown' }])
    ).toThrow();
  });
  test('permission writes use explicit arrays, encoded IDs and shared cancellation', async () => {
    const put = spyOn(apiClient, 'put').mockResolvedValue({});
    restore = () => put.mockRestore();
    const signal = new AbortController().signal;
    await userPermissionsApi.replace('user/id', [], signal);
    expect(put).toHaveBeenCalledWith(
      '/users/user%2Fid/permissions',
      { permissions: [] },
      { signal }
    );
  });
  test('System Admin comes from explicit server flag, never role alone', () => {
    expect(normalizeManagedUser({ role: 'admin' }).systemAdmin).toBe(false);
    expect(normalizeManagedUser({ system_admin: 'true' }).systemAdmin).toBe(false);
    expect(normalizeManagedUser({ system_admin: true }).systemAdmin).toBe(true);
  });
  test('model catalog can retain case-sensitive IDs and cancellation without changing older callers', async () => {
    const get = spyOn(axios, 'get').mockResolvedValue({
      data: { data: [{ id: 'ModelA' }, { id: 'modela' }] },
    });
    restore = () => get.mockRestore();
    const signal = new AbortController().signal;
    expect(
      (
        await modelsApi.fetchModels(
          'http://localhost:8317',
          'fixture-key',
          {},
          { caseSensitive: true, signal }
        )
      ).map((model) => model.name)
    ).toEqual(['ModelA', 'modela']);
    expect(get.mock.calls[0][1]?.signal).toBe(signal);
    expect(
      (await modelsApi.fetchModels('http://localhost:8317', 'fixture-key')).map(
        (model) => model.name
      )
    ).toEqual(['ModelA']);
  });
});
