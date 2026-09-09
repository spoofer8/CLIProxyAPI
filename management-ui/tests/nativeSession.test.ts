import { afterEach, beforeEach, describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { AxiosError, type AxiosInstance, type AxiosResponse } from 'axios';
import { apiClient } from '../src/services/api/client';
import { useAuthStore } from '../src/stores/useAuthStore';
import en from '../src/i18n/locales/en.json';
import zhCN from '../src/i18n/locales/zh-CN.json';
import zhTW from '../src/i18n/locales/zh-TW.json';
import ru from '../src/i18n/locales/ru.json';

const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
const instance = (apiClient as unknown as { instance: AxiosInstance }).instance;
const originalAdapter = instance.defaults.adapter;
let storage: Map<string, string>;
let browser: EventTarget;
let redirects: string[];
let postSpy: ReturnType<typeof spyOn<typeof apiClient, 'post'>> | undefined;

function connect(key = 'cpa-session', named = true) {
  useAuthStore.setState({
    apiBase: 'http://native-session.test',
    managementKey: key,
    isAuthenticated: true,
    connectionStatus: 'connected',
    rememberPassword: true,
  });
  apiClient.setConfig({ apiBase: 'http://native-session.test', managementKey: key });
  storage.set('isLoggedIn', 'true');
  storage.set('managementKey', JSON.stringify(key));
  storage.set('apiBase', JSON.stringify('http://native-session.test'));
  if (named) storage.set('cpa-session-mode', 'true');
}

beforeEach(() => {
  storage = new Map();
  redirects = [];
  browser = Object.assign(new EventTarget(), {
    location: {
      host: 'native-session.test',
      replace: (url: string) => redirects.push(url),
    },
  });
  Object.defineProperty(globalThis, 'window', { configurable: true, value: browser });
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    },
  });
  connect();
});

afterEach(() => {
  postSpy?.mockRestore();
  postSpy = undefined;
  instance.defaults.adapter = originalAdapter;
  storage.delete('cpa-session-mode');
  void useAuthStore.getState().logout();
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow);
  else Reflect.deleteProperty(globalThis, 'window');
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage);
  else Reflect.deleteProperty(globalThis, 'localStorage');
});

describe('native named-session logout', () => {
  test('waits for server revocation, deduplicates clicks and removes bootstrap credentials', async () => {
    let finish!: (value: unknown) => void;
    postSpy = spyOn(apiClient, 'post').mockImplementation(
      () => new Promise((resolve) => (finish = resolve))
    );
    const first = useAuthStore.getState().logout();
    expect(useAuthStore.getState().logout()).toBe(first);
    expect(postSpy).toHaveBeenCalledTimes(1);
    expect(postSpy).toHaveBeenCalledWith('/logout', {});
    expect(useAuthStore.getState().isAuthenticated).toBe(true);
    expect(storage.get('cpa-session-mode')).toBe('true');

    finish({});
    await first;
    expect(useAuthStore.getState().isAuthenticated).toBe(false);
    for (const key of ['isLoggedIn', 'managementKey', 'apiBase', 'cpa-session-mode']) {
      expect(storage.has(key)).toBe(false);
    }
    expect(redirects).toEqual(['/login']);
    await useAuthStore.getState().logout();
    expect(postSpy).toHaveBeenCalledTimes(1);
  });

  test('preserves the session on failure and allows retry', async () => {
    postSpy = spyOn(apiClient, 'post').mockRejectedValueOnce(new Error('unavailable'));
    await expect(useAuthStore.getState().logout()).rejects.toThrow('unavailable');
    expect(useAuthStore.getState().isAuthenticated).toBe(true);
    expect(storage.get('cpa-session-mode')).toBe('true');
    expect(redirects).toEqual([]);
    postSpy.mockResolvedValue({});
    await useAuthStore.getState().logout();
    expect(postSpy).toHaveBeenCalledTimes(2);
    expect(redirects).toEqual(['/login']);
  });

  test('management-key logout remains local even with an unrelated mode flag', async () => {
    connect('legacy-test-key');
    postSpy = spyOn(apiClient, 'post');
    await useAuthStore.getState().logout();
    expect(postSpy).not.toHaveBeenCalled();
    expect(useAuthStore.getState().isAuthenticated).toBe(false);
    expect(redirects).toEqual([]);
  });

  test('the marker alone does not revoke a cookie session without named mode', async () => {
    storage.delete('cpa-session-mode');
    postSpy = spyOn(apiClient, 'post');
    await useAuthStore.getState().logout();
    expect(postSpy).not.toHaveBeenCalled();
    expect(redirects).toEqual([]);
  });

  test('a completed old logout cannot clear a newly selected connection', async () => {
    let finish!: (value: unknown) => void;
    postSpy = spyOn(apiClient, 'post').mockImplementation(
      () => new Promise((resolve) => (finish = resolve))
    );
    const logout = useAuthStore.getState().logout();
    connect('new-connection-test-key', false);
    finish({});
    await logout;
    expect(useAuthStore.getState().isAuthenticated).toBe(true);
    expect(useAuthStore.getState().managementKey).toBe('new-connection-test-key');
    expect(redirects).toEqual([]);
  });
});

describe('responses remaining after logout or connection change', () => {
  test('a stale 401 does not sign out the new connection', async () => {
    let rejectRequest!: (reason: unknown) => void;
    let response!: AxiosResponse;
    let started!: () => void;
    const requestStarted = new Promise<void>((resolve) => (started = resolve));
    instance.defaults.adapter = (config) => {
      response = { data: {}, status: 401, statusText: 'Unauthorized', headers: {}, config };
      started();
      return new Promise((_, reject) => (rejectRequest = reject));
    };
    const unauthorized: Event[] = [];
    browser.addEventListener('unauthorized', (event) => unauthorized.push(event));
    const request = apiClient.get('/users');
    await requestStarted;
    connect('new-connection-test-key', false);
    rejectRequest(new AxiosError('Unauthorized', 'ERR_BAD_REQUEST', response.config, {}, response));
    await expect(request).rejects.toMatchObject({ status: 401 });
    expect(unauthorized).toEqual([]);
    expect(useAuthStore.getState().isAuthenticated).toBe(true);
  });

  test('current 401 still emits unauthorized exactly once', async () => {
    instance.defaults.adapter = async (config) => {
      const response = { data: {}, status: 401, statusText: 'Unauthorized', headers: {}, config };
      throw new AxiosError('Unauthorized', 'ERR_BAD_REQUEST', config, {}, response);
    };
    const unauthorized: Event[] = [];
    browser.addEventListener('unauthorized', (event) => unauthorized.push(event));
    await expect(apiClient.get('/users')).rejects.toMatchObject({ status: 401 });
    expect(unauthorized).toHaveLength(1);
  });

  test('stale success headers cannot overwrite the new server metadata', async () => {
    let finish!: (response: AxiosResponse) => void;
    let response!: AxiosResponse;
    let started!: () => void;
    const requestStarted = new Promise<void>((resolve) => (started = resolve));
    instance.defaults.adapter = (config) => {
      response = {
        data: {},
        status: 200,
        statusText: 'OK',
        headers: { 'x-cpa-version': 'old-version', 'x-cpa-support-plugin': 'true' },
        config,
      };
      started();
      return new Promise((resolve) => (finish = resolve));
    };
    const metadataEvents: Event[] = [];
    browser.addEventListener('server-version-update', (event) => metadataEvents.push(event));
    browser.addEventListener('server-plugin-support-update', (event) => metadataEvents.push(event));
    const request = apiClient.get('/users');
    await requestStarted;
    connect('new-connection-test-key', false);
    finish(response);
    await request;
    expect(metadataEvents).toEqual([]);
  });
});

test('native shell uses the normal route, navigation, notifications and local unauthorized cleanup', () => {
  const read = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');
  expect(read('index.html')).toContain('<meta name="cpa-native-management" content="1"');
  expect(read('src/router/MainRoutes.tsx')).toContain("{ path: '/users', element: <UsersPage /> }");
  const layout = read('src/components/layout/MainLayout.tsx');
  expect(layout).toContain("path: '/users'");
  expect(layout).toContain("labelKey: 'users.title'");
  expect(layout).toContain("showNotification(t('users.logout_failed'), 'error')");
  const auth = read('src/stores/useAuthStore.ts');
  expect(auth).toMatch(/addEventListener\('unauthorized',[\s\S]*?clearAuthState\(\)/);
  expect(auth).not.toMatch(/addEventListener\('unauthorized',[\s\S]*?getState\(\)\.logout\(\)/);
  for (const locale of [zhCN, zhTW, ru]) {
    expect(Object.keys(locale.users).sort()).toEqual(Object.keys(en.users).sort());
    for (const [key, value] of Object.entries(en.users)) {
      const translated = locale.users[key as keyof typeof locale.users];
      expect(translated.length).toBeGreaterThan(0);
      expect(translated.match(/\{\{\w+\}\}/g)).toEqual(value.match(/\{\{\w+\}\}/g));
    }
  }
});
