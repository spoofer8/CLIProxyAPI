# User management operations

User management is an optional PostgreSQL-backed feature. Existing `api-keys`
remain usable; enabling the feature does not replace client credentials or the
management secret.

## Delivery status

| Phase | Capability | Status |
| --- | --- | --- |
| 0 | PostgreSQL schema, configuration, startup and reload lifecycle | Implemented |
| 1 | Users, hashed API keys, management API, usage identity | Implemented |
| 2 | Monthly token accounting and quota rejection | Planned |
| 3 | Model/provider permissions and filtered model listings | Planned |
| 4 | Named administrator login, sessions and audit events | Planned |

Phase 0 creates the tables needed by later phases. Their presence does not mean
that quota enforcement, permissions, login, or audit recording are active.

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
    default-monthly-tokens: 2000000
    enforce: false
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

Bootstrap creates these six tables and their indexes in one transaction:

- `cpa_users`
- `cpa_user_api_keys`
- `cpa_user_permissions`
- `cpa_usage_monthly`
- `cpa_sessions`
- `cpa_audit_events`

Repeated startup is safe. Concurrent bootstrap attempts share an advisory lock.
The application role needs permission to create tables and indexes in its
schema. Bootstrap does not modify unrelated tables.

Enabled/DSN changes are applied on configuration reload. A replacement database
must connect and initialize successfully before it replaces the current store.
A failed replacement leaves the previous user-management settings active and
logs the failure; unrelated valid configuration changes can still apply.
Changing settings while retaining the same resolved DSN reuses the pool.

Set `user-management.enabled: false` to disable the feature and close its
connection pool. Disabled mode does not validate its DSN or duration settings,
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
failure rejects a user key with HTTP 401; legacy-key authentication can still
fall through to the existing provider.

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
`monthly_token_limit`, `created_at`, and `updated_at`. Key objects contain `id`,
`user_id`, `key_prefix`, `label`, `status`, `last_used_at`, `created_at`, and
`revoked_at`. Password hashes and key hashes are never returned. Phase 1 does
not yet include monthly usage in user responses.

For `PATCH /users/:id`, omitting `monthly_token_limit` preserves its current
value; JSON `null` clears the override so the configured default applies.
Explicit zero or a negative integer means unlimited. This field is stored in
Phase 1; enforcement begins in Phase 2.

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
  --data '{"email":"example@example.invalid","display_name":"Example user","role":"user","monthly_token_limit":2000000}'

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

User-authenticated requests retain the existing client protocols and model
names. The access principal is the stable user ID. Consequently,
`usage.Record.APIKey` contains a user ID for user-key traffic, while legacy-key
traffic keeps the existing principal representation. Consumers must not assume
that this field always contains a credential. Multiple keys belonging to one
user share the same usage identity.

Phase 1 acceptance combines a real upstream completion using a newly issued
key with integration tests that capture `usage.Record` and verify its user-ID
principal. The existing `/v0/management/usage-queue` consumes queued records;
do not poll it as a read-only attribution check. The existing `api-key-usage`
endpoint reports upstream credential activity, not per-user accounting.

## Monthly quotas — Phase 2, planned

The default shown above is two million tokens per user per UTC calendar month.
An explicit user limit overrides the default; a nonpositive effective limit is
unlimited. Monthly periods use `YYYY-MM`, so no reset job is required.

Keep `quota.enforce: false` for the first week after accounting is implemented.
Compare attributed token totals with upstream usage before enabling rejection.
In Phase 0 these quota fields are configuration only: no counters are written
and no request is rejected because of a quota.

The planned limit check occurs before execution and rejects an exhausted quota
with HTTP 429 and error code `quota_exceeded`. It does not reserve estimated
tokens. Concurrent requests can all pass before earlier requests finish or
their accounting becomes visible, so overshoot can exceed one request's token
usage. This is not a strict reservation system.

Accounting is intended to run asynchronously. Failed writes during a database
outage may undercount usage without failing an already executing request.
Legacy configured proxy keys remain exempt from user quotas and permissions.

## Permissions, login and audit — later phases

Phase 3 will add model/provider permission rules and filter model listings for
the authenticated user. The planned default is unrestricted access when there
are no rules, with deny rules taking precedence. Permission settings are not
enforced by Phase 0.

Phase 4 will add administrator email/password login, revocable sessions, and
audit events. The upstream management panel currently has its own shared-key
login; accepting a cookie in middleware alone cannot replace that client flow.
The integration must bridge its login state and revoke the server session on
logout while preserving the existing management secret as a recovery path.
Until that phase is implemented, the existing management authentication remains
the only supported panel login.
