# Management frontend provenance and build

This directory vendors the installed management frontend release, with Users &
activity integrated into its existing application.

- Upstream: https://github.com/router-for-me/Cli-Proxy-API-Management-Center
- Release: `v1.22.15`
- Commit: `ed5f1c48e11ba7335f1e8f676f228c280196af85`
- License: MIT; see the preserved [LICENSE](LICENSE).
- The upstream [README.md](README.md) and [README_CN.md](README_CN.md) are
  preserved. This file documents the local integration and release process.

The native integration adds `src/features/users/`, its Management API module,
the `/users` hash route and a **Users & activity** entry in the existing sidebar.
It uses the existing page layout, UI components, themes, language store and
authentication. English, Simplified Chinese, Traditional Chinese and Russian
translations are included. The normal Logout action revokes named server
sessions before clearing local state; failures use the existing notification
system. Legacy management-key login remains available.

The HTML includes `<meta name="cpa-native-management" content="1">`. The
backend uses this marker to initialize a named session before the app starts
without adding floating links, another logout control or storage overrides.
The public `cpa-session` marker requires a valid administrator session cookie;
it is not a management credential. Old `/users` bookmarks redirect to
`/management.html#/users`.

Only source, tests, assets, configuration and upstream documentation are
vendored. Git metadata, upstream GitHub workflows, dependencies, build output,
local `CLAUDE.md` and caches are excluded.

## Build and verify

Use Bun `1.3.14`, as pinned in `package.json`; upstream CI uses Node.js 24.
From the backend repository root:

```sh
cd management-ui
bun install --frozen-lockfile
bun run test
bun run lint
VERSION=v1.22.15+users bun run build
```

The build runs TypeScript checking and produces `dist/index.html` with its
JavaScript, styles and assets inlined. Set `VERSION` explicitly: without it,
Vite's upstream fallback can pick a backend repository tag rather than the
frontend version. Do not edit generated HTML or commit dependencies/build
output. Native feature regressions are covered by `tests/usersApi.test.ts`,
`tests/usersConversation.test.ts` and `tests/nativeSession.test.ts`; browser
acceptance should exercise the real backend route with the resulting bundle.

## Deploy the single HTML file

Preserve the current `management.html` for rollback, then deploy
`management-ui/dist/index.html` as `management.html` in the backend's configured
management static directory. No other frontend files or separate frontend
service are required. The directory normally is `static` beside the backend
configuration; `MANAGEMENT_STATIC_PATH` or the backend's writable storage path
can override it.

Merge these fields into the existing backend configuration:

```yaml
remote-management:
  disable-control-panel: false
  disable-auto-update-panel: true
```

Keep automatic panel updates disabled while using this local build, so an
upstream asset does not replace the native Users integration. The setting
still permits an upstream download when `management.html` is missing, so
install the custom bundle before opening the management page. Preserve the
existing management secret and remote-access settings.

Open `/management.html#/users` or choose **Users & activity** in the sidebar.
Operational details are in [user-management.md](../docs/user-management.md).
To roll back the frontend, restore the saved `management.html`; do not modify
user accounts, quotas or stored request activity.
