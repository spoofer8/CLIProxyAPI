# User management operations

User management is an optional PostgreSQL-backed feature with USD budgets,
model restrictions, and durable request/response history. The first configured
`api-keys` credential is attributed to the stable **System Admin** account while
the feature is enabled. Its proxy access does not grant management access.
Other configured credentials retain their existing behavior.

## Users & activity UI

Open `/management.html#/users`, or choose **Users & activity** in the management
panel's sidebar. The page shares the panel's layout, theme, language and login.
Named administrator sessions and the panel's management-key login are supported.
Existing `/users` bookmarks redirect to this native route.

- Select a user and choose **Edit budgets** to set any combination of lifetime,
  daily, rolling 7-day, and rolling 30-day USD limits. All configured limits
  apply. Blank means unlimited; zero blocks new spending. Daily limits reset
  at midnight in `Europe/London`, including daylight-saving changes.
- **Allowed models** chooses all configured models or selected case-sensitive
  model IDs. Existing provider restrictions and model deny rules remain active.
  Existing wildcard allow rules remain visible until deliberately removed.
- **Model pricing** shows automatically refreshed models.dev defaults and
  persistent manual overrides. Costs retain the price snapshot used for each
  execution. Unmatched models require manual prices for budgeted users.
- **Inspect request** shows the submitted conversation and model response,
  including tools, system instructions, code, and sanitized raw content. Search,
  model, status, time and explicit-session filters apply to stored history.
  Content pages load incrementally without a storage text-size cap.
- **New user** and **API keys** provision managed client credentials. The
  configured main credential is never displayed in this account view; additional
  managed keys are listed separately. System Admin usage, costs and activity
  are recorded, but spending limits and model restrictions do not apply to it.

Capture applies to new requests after deployment; historical prompts cannot be
recovered when request logging was off. Ordinary HTTP requests and individual
Responses WebSocket turns retain their submitted content and returned output.
Older preview-only entries remain labelled as such. Financial accounting starts
at activation; historical token counters are not converted into invented costs.

Durable activity is mandatory for managed requests and retained indefinitely.
The old `request-activity.enabled` and `retention-days` fields remain accepted
for configuration compatibility but do not disable capture or expire full text.
Authentication headers/query credentials are excluded; credential-shaped JSON
fields, nested JSON tool credentials and inline file data are redacted/omitted.
Text is displayed without executing HTML or loading remote attachments.
Attachments retain metadata/references without binary contents. Summary previews
and transport pages are bounded; the complete retained text is not. In-progress
or interrupted requests clearly show when their content or usage is incomplete.

Only management administrators may list or read captured content. New admission
requires a durable activity record and encrypted, fsynced recovery journal; a
failure rejects admission with 503. Output is journaled before forwarding so
accepted content can be recovered after a database outage or restart. Preserve
the configured `spool-directory` and its private key alongside PostgreSQL.
Already accepted requests may finish above a spending limit; later requests
are blocked. Missing reliable usage requires manual **Resolve cost** with an
audited total and explanation before further budgeted requests are allowed.

The frontend source is vendored in `management-ui/` from upstream release
`v1.22.15`. Build and deploy its single HTML bundle as `management.html` and keep
`remote-management.disable-auto-update-panel: true` to preserve the integration.
See [frontend provenance and build instructions](../management-ui/UPSTREAM.md).

## Delivery status

| Phase | Capability | Status |
| --- | --- | --- |
| 0 | PostgreSQL schema, configuration, startup and reload lifecycle | Implemented |
| 1 | Users, hashed API keys, management API, usage identity | Implemented |
| 2 | Monthly token reporting (legacy token limits superseded by USD budgets) | Implemented |
| 3 | Model/provider permissions and filtered model listings | Implemented |
| 4 | Named administrator login, sessions and audit events | Implemented |
| Extension | System Admin attribution, USD budgets, pricing, durable full activity | Implemented; deployment tracked separately |

Current builds initialize the complete schema when the feature is enabled.

## PostgreSQL and configuration

Use a dedicated database and login role. This store is independent of the
optional PostgreSQL configuration/auth-file store and its `PGSTORE_*` settings.
On Debian, a minimal setup is:

```sh
sudo apt-get update
sudo apt-get install -y postgresql
sudo systemctl enable --now postgresql
sudo -u postgres createuser --login --no-superuser --no-createdb --no-createrole cliproxy
sudo -u postgres createdb --owner=cliproxy cliproxy
sudo -u postgres psql -c '\password cliproxy'
```

The last command prompts for a password without putting it in shell history.
Reuse an existing application database and role rather than recreating them or
changing credentials used by another deployment.

Keep PostgreSQL listening only on loopback. Verify the server's
`listen_addresses` and listening sockets; Debian's default `localhost` normally
binds `127.0.0.1` and `::1`:

```sh
sudo -u postgres psql -Atc 'SHOW listen_addresses;'
ss -lnt 'sport = :5432'
```

Store a connection URL in a root-readable environment file, for example
`/etc/cliproxyapi/usermgmt.env`. Use directory permissions `0700` and file
permissions `0600`; generate a strong password and URL-encode any reserved
characters in it. This example contains a placeholder, not a usable password:

```text
CLIPROXY_USERMGMT_DSN=postgres://cliproxy:<password>@127.0.0.1:5432/cliproxy?sslmode=disable
```

Load it through a dedicated systemd drop-in for `cliproxyapi.service`:

```ini
[Service]
EnvironmentFile=/etc/cliproxyapi/usermgmt.env
```

Add this block to the existing proxy configuration:

```yaml
user-management:
  enabled: true
  dsn: "${CLIPROXY_USERMGMT_DSN}"
  quota:
    default-monthly-tokens: 0 # Legacy compatibility; configure USD budgets in the panel.
    enforce: false
  request-activity:
    retention-days: 0
    spool-directory: "/var/lib/cliproxyapi/activity-spool"
  session:
    ttl: "12h"
  cache:
    ttl: "30s"
```

`sslmode=disable` is appropriate here because the database connection remains
on loopback. The proxy's existing HTTP/TLS deployment remains unchanged.

Run `systemctl daemon-reload` and restart the service when adding or changing
its environment file. Editing the file does not change a running process's
environment. `${NAME}` references are expanded once when opening the store and
remain references when configuration is saved. A missing or empty referenced
variable rejects an enabled configuration. Literal `$` characters otherwise
remain unchanged.

The DSN is omitted from the management configuration JSON. The existing
administrator-only raw YAML download still exposes whatever is actually in the
configuration file; using an environment reference keeps the password out of
that file. Do not publish environment files, raw configuration, or database
credentials in logs and diagnostic reports.

## Startup, reload and rollback

When enabled, startup requires a reachable PostgreSQL database and a successful
schema bootstrap. A failure prevents startup. Database errors omit connection
details so that passwords are not included in normal error messages.

Bootstrap creates account tables plus financial/activity migrations and indexes:

- `cpa_users`
- `cpa_user_api_keys`
- `cpa_user_permissions`
- `cpa_usage_monthly`
- `cpa_sessions`
- `cpa_audit_events`

Additional tables hold budgets, price snapshots, cost events, durable requests,
and full content chunks. Use schema inspection to list the complete current set.

Repeated startup is safe. Concurrent bootstrap attempts share an advisory lock.
The application role needs permission to create tables and indexes in its
schema. Bootstrap does not modify unrelated tables.

Enabled/DSN changes are applied on configuration reload. A replacement database
must connect and initialize successfully before it replaces the current store.
A failed replacement leaves the previous user-management settings active and
logs the failure; unrelated valid configuration changes can still apply.
Changing settings while retaining the same resolved DSN reuses the pool.

An admitted request keeps its original database identity across a
reload. The old store remains open until its usage producers finish and queued
records drain, so late streaming usage is not charged to the replacement
database. Financial/activity persistence is independent of optional analytics
queues. Shutdown has a bounded drain, with durable journals retained for replay.

Set `user-management.enabled: false` to disable the feature and retire its
connection pool after admitted work drains. Disabled mode does not validate its DSN or duration settings,
does not connect to PostgreSQL, and leaves existing database records intact.
If the enabled service cannot start because PostgreSQL is unavailable, edit the
configuration to disable the feature before restarting.

Before replacing the executable, preserve the running binary, configuration,
and any existing service drop-ins. For a binary rollback, restore the saved
binary/configuration, remove only the drop-ins introduced by that deployment,
reload systemd if necessary, and restart. Keep database data in place. Automated
database backup scheduling is outside this implementation's scope.

## Verification

Check service health, a legacy API-key request, and database schema creation.
The application logs a successful connection as:

```text
user management PostgreSQL connection established and schema ready
```

With `logging-to-file: true`, this message goes to the configured application
log file rather than necessarily appearing in journald. The default file log
is under the authentication directory's `logs` directory. Inspect only the
relevant status messages when collecting deployment evidence.

```sh
curl --fail http://127.0.0.1:8317/healthz
sudo -u postgres psql -d cliproxy -c \
  "SELECT table_name FROM information_schema.tables
   WHERE table_schema = 'public' AND table_name LIKE 'cpa_%'
   ORDER BY table_name;"
```

Provider/model registration can finish shortly after HTTP health becomes
available. Let the registry finish loading before comparing `/v1/models` with
the pre-deployment inventory.

Store and lifecycle integration tests use `CLIPROXY_USERMGMT_TEST_DSN`. Point
it at a disposable test database, never a production database. Store tests use
temporary schemas; runtime/service tests bootstrap the supplied database's
normal schema. Run the full Go test suite and build the server before deployment.

## User keys and identity — Phase 1

The API below is available in builds containing Phase 1. A deployment containing
only Phase 0 must be upgraded before these endpoints can be used.

Only an authenticated management administrator provisions users and keys.
Proxy API keys and the management secret are separate credentials: being a
legacy unrestricted proxy caller does not automatically grant management API
access.

Each user can have multiple keys. Keys are generated by the server, stored as
SHA-256 hashes, and returned in plaintext only when issued. Retain that response
securely; list/get operations must not reveal plaintext keys or their hashes.
Revoking a key or disabling its user prevents subsequent authentication.

Successful authentication is cached for the configured TTL. Mutations through
the management API invalidate the cache immediately. Direct database edits can
take up to that TTL to affect a previously cached credential. A database lookup
failure rejects authentication. A main configured credential whose managed
identity cannot be verified receives a terminal 503; it cannot fall back to
untracked configured-key access while user management remains enabled.

Phase 2 also revalidates the user/key before each execution on an established
Responses WebSocket. An administrator's key revocation, account disable, or
account deletion rejects subsequent frames with an authentication error even
regardless of spending limits. Already running upstream work may finish.

### Management API

All routes below require the existing management secret. They return 404 while
user management is disabled. User/key IDs are ULIDs. New keys begin `sk-cpa-`
and contain 32 cryptographically random bytes encoded with base64url.

| Method and path, relative to `/v0/management` | Success response |
| --- | --- |
| `POST /users` | 201; user object |
| `GET /users?limit=50&offset=0` | 200; `{users, total, limit, offset}` |
| `GET /users/:id` | 200; user object |
| `PATCH /users/:id` | 200; updated user object |
| `DELETE /users/:id` | 200; `{"status":"deleted"}` |
| `POST /users/:id/keys` | 201; `{"key":"<one-time-key>","api_key":{...}}` |
| `GET /users/:id/keys?limit=50&offset=0` | 200; `{keys, total, limit, offset}` |
| `DELETE /keys/:keyId` | 200; `{"status":"revoked"}` |

Lists default to 50 entries, allow at most 200, and require an offset from zero
through one billion. Invalid input returns 400, a missing resource 404, a duplicate email
409, and a management database failure 503. Revoking an existing already-revoked
key is idempotent. Deleting a user cascades to their keys, permissions, sessions
and monthly counters; audit history is retained when audit recording is added.

User objects contain `id`, `email`, `display_name`, `role`, `status`,
`system_admin`, `monthly_token_limit`, `created_at`, and `updated_at`. Key objects contain `id`,
`user_id`, `key_prefix`, `label`, `status`, `last_used_at`, `created_at`, and
`revoked_at`. Password hashes and key hashes are never returned. Phase 1 does
not include monthly usage in user responses; Phase 2 adds the `usage` field
described below.

The `monthly_token_limit` field remains for compatibility and does not enforce
spending in current builds. Use `/users/:id/budget` for USD limits. System Admin
cannot be deleted, disabled or demoted; its identity survives main-key rotation.
No password or managed API-key row is created for the configured credential.

For command-line administration, put the authorization header in a private
curl configuration file rather than command arguments. Create that file with
permissions `0600`; the following line shows its placeholder content:

```text
header = "Authorization: Bearer <management-secret>"
```

Use the file for requests; the example email is fictional:

```sh
MGMT=http://127.0.0.1:8317/v0/management
CURL_AUTH=/path/to/private-management.curl

curl --fail-with-body --config "$CURL_AUTH" "$MGMT/users" \
  -H 'Content-Type: application/json' \
  --data '{"email":"example@example.invalid","display_name":"Example user","role":"user"}'

USER_ID='<id returned by creation>'
curl --fail-with-body --config "$CURL_AUTH" "$MGMT/users/$USER_ID"
curl --fail-with-body --config "$CURL_AUTH" "$MGMT/users?limit=50&offset=0"

# Protect the issuance response: its key is displayed only once.
umask 077
curl --fail-with-body --config "$CURL_AUTH" "$MGMT/users/$USER_ID/keys" \
  -H 'Content-Type: application/json' --data '{"label":"workstation"}' \
  --output issued-key.json

curl --fail-with-body --config "$CURL_AUTH" "$MGMT/users/$USER_ID/keys"

# Disable every key belonging to this user without deleting records.
curl --fail-with-body --config "$CURL_AUTH" -X PATCH "$MGMT/users/$USER_ID" \
  -H 'Content-Type: application/json' --data '{"status":"disabled"}'

# Re-enable the account; individually revoked keys remain revoked.
curl --fail-with-body --config "$CURL_AUTH" -X PATCH "$MGMT/users/$USER_ID" \
  -H 'Content-Type: application/json' --data '{"status":"active"}'

KEY_ID='<api_key.id returned by issuance>'
curl --fail-with-body --config "$CURL_AUTH" -X DELETE "$MGMT/keys/$KEY_ID"
```

Move the issued key into the client's credential storage, then remove the
temporary issuance-response file. Client credentials remain separate from the
private management curl configuration. Obtain available model aliases from
`GET /v1/models`; deployment aliases need not match the names used in an older
plan or another installation.

Match the request protocol to the configured upstream. The deployment used for
acceptance serves `azure-4.1-mini` through native Responses: a request to
`POST /v1/responses` with `{"model":"azure-4.1-mini","input":"Reply with exactly OK.","max_output_tokens":16}`
succeeds. Its chat payload instead returns HTTP 400, `unsupported_parameter`
for `messages`, including with the preexisting legacy key. That is a baseline
upstream protocol mismatch, not a user-key authentication failure. Compare the
same request using a known-working existing credential before changing account
permissions or proxy configuration.

User-authenticated requests retain the existing client protocols and model
names. The access principal is the stable user ID. Consequently,
`usage.Record.APIKey` contains a user ID for user-key traffic, while legacy-key
traffic keeps the existing principal representation. Consumers must not assume
that this field always contains a credential. Multiple keys belonging to one
user share the same usage identity.

Phase 2 limits user keys to routes with integrated execution/accounting:

- OpenAI chat, completions and Responses, including Responses WebSockets and
  `/backend-api/codex/responses` aliases.
- Anthropic messages and token counting.
- Gemini generation, streaming generation, token counting and interactions.
- OpenAI/Gemini model listings and individual Gemini model lookup.

Media/images/video, realtime, realtime client secrets, live calls, alpha search,
and other unsupported authenticated routes return HTTP 403 with
`endpoint_not_supported` for user keys. Existing legacy credentials keep their
existing route access. This restriction prevents unaccounted routes from
bypassing user budgets; it does not indicate that a model permission was denied.

Phase 1 acceptance combines a real upstream completion using a newly issued
key with integration tests that capture `usage.Record` and verify its user-ID
principal. The existing `/v0/management/usage-queue` consumes queued records;
do not poll it as a read-only attribution check. The existing `api-key-usage`
endpoint reports upstream credential activity, not per-user accounting.

## USD spending and accounting

Budgets use exact decimal USD strings. Lifetime totals begin at financial
activation; daily totals use the current London calendar day; weekly and monthly
windows cover the rolling previous 7 and 30 days. All configured limits apply.
The API reports blocking limits and the earliest available time based on recorded
spending. Exhausted lifetime limits require an administrator to increase/remove
the limit. There are no automatic dollar defaults or historical backfills.

Each accepted execution records provider, actual billable model, token buckets,
price snapshot, and charge. Public models.dev prices refresh for future executions;
manual overrides persist. Unknown cache/reasoning buckets, missing reliable usage,
unsupported service tiers and interrupted accounting remain unresolved rather
than silently free. Budgeted admission fails when a required price is unavailable.
An administrator resolves an ended request with its verified total USD cost and
an audit note; the total cannot be below the already recorded charge.

Concurrent accepted work is allowed to finish and can exceed a cap. This is not
a prepaid reservation system: subsequent admission is blocked when recorded costs
reach any configured limit or when unresolved accounting requires admin action.

All paths below are relative to `/v0/management` and require management authentication:

| Method and path | Behavior |
| --- | --- |
| `GET /users/:id/budget` | Budgets, spending, reset/availability times and unresolved requests |
| `PUT /users/:id/budget` | Complete replacement of `lifetime_usd`, `daily_usd`, `weekly_usd`, `monthly_usd`; null/omitted means unlimited |
| `GET /users/:id/cost-events?limit=50&offset=0` | Executions, known costs, token breakdown and price snapshots |
| `POST /requests/:id/cost-resolution` | Verified total `cost_usd` plus `note`; running requests return 409 |
| `GET /pricing` | Published defaults and manual overrides, USD per million tokens |
| `PUT /pricing/override` | Set explicit provider/model rates, including optional cache/reasoning rates |
| `DELETE /pricing/override?provider=...&model=...` | Restore the current published default |
| `POST /pricing/refresh` | Refresh defaults while retaining manual overrides |
| `GET /users/:id/requests` | History with `q`, `model`, `status`, `since`, `session_id`, `before`, `limit` filters |
| `GET /requests/:id` | Request details and content metadata |
| `GET /requests/:id/content?direction=request&after=0&limit=20` | Sanitized full text chunks; use `direction=response` for returned output |

Content responses include `chunks`, `next_after`, and `complete`. Chunks carry
sequence, direction, format and text; read pages in order to reconstruct the
retained JSON or newline-separated response events. Session grouping uses explicit
reliable identifiers or established response relationships, never inferred timing.

## Legacy monthly token reporting

The earlier monthly-token enforcement feature is superseded by USD budgets.
`quota.default-monthly-tokens`, `quota.enforce`, and `monthly_token_limit` remain
accepted for compatibility and do not reject current requests. Old deployments
may still enforce them; deploy backend and native panel together.

`GET /v0/management/usage?period=2026-09&limit=50&offset=0` and the optional
`user_id` filter continue to report UTC monthly token counters. `GET /users/:id`
also includes current-month `usage`. These analytics counters are separate from
the durable financial ledger and are not a basis for pricing historical work.
`request_count` counts upstream usage records/attempts rather than necessarily
client HTTP requests, so retries can produce multiple records.

## Permissions — Phase 3

Phase 3 adds model/provider rules and filtered model listings for the
authenticated user. An empty rule set is unrestricted; deny rules take
precedence. These settings require a Phase 3 build and are not enforced by
earlier phases.

Model rules refer to model names and aliases. A model allow rule such as
`azure-*` restricts the visible model list and permitted executions. Execution
checks also run after routing so that aliases, provider fallback, or a changed
model in a later Responses WebSocket frame cannot bypass the policy.

Patterns match the entire value. `*` matches zero or more characters, including
`/`; `?` matches one Unicode character. Other characters are literal. Model
matching is case-sensitive and recognizes the base name of reasoning-suffixed
aliases. Model allowlists match requested/resolved visible aliases, so an
allowed alias can still map to a differently named upstream deployment. Model
deny rules also check the final execution/payload model names.

Final outbound checks include model changes made by payload overrides and
deployment/model selectors in upstream URLs. For user-key traffic, ambiguous
duplicate or differently cased root `model` selectors are rejected, and final
payload inspection is limited to 64 MiB. Nested model strings in user/tool
content are not routing selectors. Legacy-key requests keep their existing
payload handling.

OpenAI-compatible providers have a configured friendly name and an internal
name. For example, `azure-openai` maps to
`openai-compatible-azure-openai`. Use the provider's actual configuration name;
it is separate from the client-visible model alias.

Provider patterns are normalized to lowercase and match both the canonical
name and the friendly name without its `openai-compatible-` prefix. Model and
provider allowlists constrain their scopes independently: when both exist, the
request must satisfy both. A matching deny in either scope wins. A deny-only
scope permits values that do not match its deny rules. A policy database error
returns 503 rather than treating an unavailable rule set as unrestricted.

### Permission management API

```text
GET /v0/management/users/:id/permissions
PUT /v0/management/users/:id/permissions
```

Both return HTTP 200 with `{"permissions":[{"scope":"model","value":"azure-*","effect":"allow"}]}`.
`PUT` atomically replaces the complete set and returns its normalized form.
`scope` must be `model` or `provider`; `effect` must be `allow` or `deny` and
defaults to `allow` when omitted. The set permits at most 128 rules, each with
a value of at most 256 bytes. Duplicate normalized scope/value pairs and
invalid input return 400 without partially replacing the policy.

Using the private curl authorization file from the user-management examples:

```sh
# Allow Azure aliases, with one explicit exception.
curl --fail-with-body --config "$CURL_AUTH" -X PUT "$MGMT/users/$USER_ID/permissions" \
  -H 'Content-Type: application/json' \
  --data '{"permissions":[{"scope":"model","value":"azure-*","effect":"allow"},{"scope":"model","value":"azure-opus-5","effect":"deny"}]}'

curl --fail-with-body --config "$CURL_AUTH" "$MGMT/users/$USER_ID/permissions"

# Replace the model rules with a provider allowlist.
curl --fail-with-body --config "$CURL_AUTH" -X PUT "$MGMT/users/$USER_ID/permissions" \
  -H 'Content-Type: application/json' \
  --data '{"permissions":[{"scope":"provider","value":"azure-openai","effect":"allow"}]}'

# Clear all rules. Omitted or null permissions are rejected; [] is explicit.
curl --fail-with-body --config "$CURL_AUTH" -X PUT "$MGMT/users/$USER_ID/permissions" \
  -H 'Content-Type: application/json' --data '{"permissions":[]}'
```

Permission changes must take effect for the same previously authenticated key;
clients do not need a replacement key after each policy edit. Clearing the rule
set restores unrestricted access within the supported user-key routes. Legacy
configured proxy keys remain exempt.

### Phase 3 verification

The complete Linux test suite, feature-specific PostgreSQL/race tests, final
outbound-policy executor race tests, build, and repository invariant checks
passed. Broader executor race/stress runs also reproduced two preexisting
issues on the exact pre-Phase-3 revision (`250aab2`): a Claude shared-credential
metadata race and an intermittent Antigravity pooled-connection count failure.
Those unrelated baseline issues were not changed by this feature; the passing
checks above do not constitute a clean repository-wide race-test result.

## Named login and audit — Phase 4

Named panel login requires an active user with `role: admin` and a password.
The system Admin has no automatically assigned management password. Create a separate named administrator for panel login. Acceptance uses
temporary accounts and removes them; create the first permanent administrator
with your chosen email through the existing management API:

```text
POST /v0/management/users
Content-Type: application/json

{"email":"your-admin@example.invalid","display_name":"Administrator","role":"admin"}
```

Authenticate that request with the existing management secret, using the
private curl configuration described above. Use the returned user ID to set
the administrator's password:

```text
POST /v0/management/users/:id/password
Content-Type: application/json

{"password":"<new administrator password>"}
```

Passwords must contain 8–72 bytes and are stored with bcrypt. The response is
`{"status":"ok"}`. Send `{"password":null}` or an empty string to clear a
password; an omitted password is invalid. Only administrators may have a
nonempty panel password. Password, role, and account-status changes revoke
existing sessions; clearing a password also disables password login.

Visit `/login` and enter the administrator's email/password. The form redirects
to `/management.html` after successful login. JSON clients can instead use:

```text
POST /v0/management/login
Content-Type: application/json

{"email":"example@example.invalid","password":"<administrator password>"}
```

The JSON response contains `status`, `user` (`user_id`, `email`, `display_name`,
`role`, `expires_at`), and `redirect`. The session is issued in the `cpa_session`
cookie with `HttpOnly`, `SameSite=Strict`, and `Path=/`. Sessions expire after
the configured absolute TTL, normally 12 hours; the database stores the
cookie's hash. The current plain-HTTP deployment does not set `Secure`.

The native bundle is built from the vendored frontend. A server-side response
bridge initializes its login state using the public marker `cpa-session`; that
marker is not a credential and cannot authenticate without a valid administrator
cookie. The bundle's `cpa-native-management` HTML marker keeps the bridge limited
to session initialization. The panel's normal **Logout** action calls
`POST /v0/management/logout` to revoke the server session before clearing login
state and returning to `/login`. If logout fails, the panel displays its normal
error notification and allows another attempt.

Cookie-authenticated writes and login/logout submissions require the same
origin. Remote access still respects the existing remote-management setting.
An explicit valid legacy management key takes precedence over cookies, keeping
the recovery path independent of the session database even if a stale cookie
is present. Expired/revoked cookies with the public marker return 401 without
consuming the bad-password budget. Use `/management.html?legacy=1` to switch
the browser back to the panel's existing shared-key login.

Named sessions also work when user management is enabled and no shared
management secret remains, provided an administrator account was already
provisioned. Keep the existing secret until that recovery choice is deliberate.
Password login and legacy-key failures share the existing per-IP protection:
five failures trigger a 30-minute ban. Test this behavior only against an
isolated instance, not the production administrator's IP.

### Audit API

```text
GET /v0/management/audit?limit=50&before=<event-id>&actor=<actor-id>&action=<action>
```

The response is `{"events":[...],"next_before":...}`; each event contains `id`,
`at`, `actor`, `action`, `target`, `client_ip`, and `detail`. Results are newest
first; the limit defaults to 50 and is capped at 200. Pass `next_before` as
`before` to page backward. The actor is the named administrator's user ID or
`legacy-admin-key` for explicit legacy authentication.

Events cover user create/update/disable/delete, key create/revoke, quota changes,
permission replacement, password changes, login/logout/login failures, and
invalid proxy keys. A nonempty permission replacement records
`permission.grant`; an explicit empty replacement records `permission.revoke`.
A limit-only account PATCH records `quota.update`. Password changes record
`password.update` with a `cleared` flag. Audit details contain fixed metadata
such as counts and display prefixes, never passwords, password/key hashes,
session cookies, or full request bodies. Invalid-key events are rate-limited.

User-management mutations and their audit event commit together. Deleting an
account does not delete its audit history. Request logging excludes sensitive
management bodies, and Cookie/Set-Cookie headers are redacted by the shared
logging helper.

Phase 4 validation passed the full Linux suite, planned PostgreSQL/race and
build checks, plus 30 fresh-browser checks against the unchanged upstream
panel and 32 isolated API/database checks. Those checks covered real form
login, normal panel logout and server revocation, audit actor/IP attribution,
stale-session recovery, named login without a shared secret, and the failure
ban on an isolated instance. Temporary fixtures were removed afterward.
