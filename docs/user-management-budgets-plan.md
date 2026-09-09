# Native user cost budgets and complete activity

Status: backend and native UI implementations are saved; final integration and
browser verification are in progress. No deployment of these budget/activity
changes has occurred. Frontend verification passed 478 tests, lint, TypeScript,
and the single-file production build using Bun 1.3.14.
The source of product decisions is [the requirements](user-management-budgets-spec.md).
Questions 29 and 30 are resolved: Europe/London midnight for daily limits, and
manual cost resolution before further ordinary-user requests after missing usage,
including users with no spending caps. The configured Admin remains exempt.
This scope supersedes the old parent
`plan.md` restrictions against cost accounting and frontend source changes.

## Phase 0 — Documentation discovery

Discovery completed against current backend and vendored frontend source.
Revalidate exact line numbers after edits; the paths and symbols are canonical.

Existing APIs and patterns:

- `internal/usermgmt/store/store.go`: `Open`, `Store.Transaction`; nested domain
  mutations reuse an enclosing transaction.
- `internal/usermgmt/store/schema.go`: `EnsureSchema` executes bootstrap under
  PostgreSQL advisory transaction lock `1129333077`. Existing CREATE statements
  do not migrate an already deployed table.
- `internal/usermgmt/store/users.go`: `CreateUser`, `GetUser`, `ListUsers`,
  `UpdateUser`, `DeleteUser`; explicit-set flags distinguish omitted fields from
  null. Copy transaction and safe DTO patterns, not old token-quota semantics.
- `internal/usermgmt/store/store_test.go`: isolated real PostgreSQL schemas,
  concurrent bootstrap, transactional schema failure and cascade tests.
- `internal/usermgmt/audit.go`: `Runtime.mutate` couples mutation and audit;
  audit fields are allowlisted. Runtime invalidates live-scope caches after
  successful administrative changes.
- `internal/access/config_access/provider.go`: config `api-keys` authentication.
  `internal/usermgmt/access.go`: managed-key identity. Panel authentication is
  separately enforced in `internal/api/handlers/management/session_auth.go`.
- `sdk/access/request_hooks.go`: existing hooks are `Check`, `Begin`,
  `Authorize`, `FilterProviders`, `CopyContext`, and `Capture`. `Check` receives
  context only; `Capture` currently cannot return a logging failure.
- `internal/usermgmt/request_check.go`, `quota.go`, `accounting.go`: existing
  admission and usage context. `HandleUsage` currently discards billing detail
  and persists asynchronously; it is not a durable cost ledger.
- `sdk/cliproxy/usage/manager.go` and `accounting.go`: `usage.Record`,
  `EnsureTokenBreakdownForProvider`, `TokenBreakdown.Valid`. Canonical v2 input
  buckets are uncached/cache-read/cache-write; output buckets separate reasoning
  and nonreasoning. Preserve accounting quality and unclassified tokens.
- `internal/runtime/executor/helps/usage_helpers.go`: once-only publication and
  additional-model usage. One accepted request may generate multiple records.
- `internal/usermgmt/permissions_handlers.go`: GET/PUT user permissions already
  exists; `store.ReplacePermissions` replaces the complete policy atomically.
  Model/provider scopes, allow/deny precedence and aliases already work.
- `internal/api/server_usermgmt_capture.go`: HTTP incoming-body tee.
  `sdk/api/handlers/request_body.go`: compressed-body decoding occurs later.
- `sdk/api/handlers/stream_forwarder.go`: shared SSE forwarding, but first
  chunks, plain JSON, errors and WebSocket writes also have separate paths.
- `sdk/api/handlers/openai/openai_responses_websocket*.go`: capture begins on
  original client frames, before reconstructed history; response writes and
  terminal errors must both be observed.
- `sdk/cliproxy/session/info.go`: some explicit session identities are useful,
  but user IDs, cache keys and request IDs are not reliable session grouping.
- `management-ui/src/features/users/`: native page, shared Sheet/Modal inspector,
  API-key dialogs and session-scoped async guards.
- `management-ui/src/services/api/users.ts`: normalization and serialization
  through the existing authenticated API client.
- `management-ui/AGENTS.md` and `UPSTREAM.md`: React/SCSS conventions, four
  locales, Bun 1.3.14 verification and single-file HTML deployment.

Remaining discovery before implementation:

- Apply the confirmed daily-reset and missing-usage decisions above.
- Select and verify a public pricing source with provider/model provenance and
  published input/output/cache rates. Document source, units, update time and
  missing coverage. Do not invent rates or confuse alias names with billable
  model identity. Provider-specific pricing that cannot be represented must be
  classified as unpriced until an administrator supplies an override.
- Confirm the existing supported transport boundaries. Any extension to routes
  currently forbidden to managed keys needs the same identity, permission,
  durable capture and accounting tests before enabling those routes.

## Phase 1 — Durable schema and visible configuration Admin

Implementation:

- Copy the transactional bootstrap and isolated-schema test patterns to add
  explicit idempotent upgrades for existing installations.
- Add a stable system-Admin identity associated with the configured main proxy
  key. Synchronize key changes without duplicating the user or storing raw
  credentials in user DTOs, audit, browser state or new database fields.
- Preserve the separate management authentication path. Only the system Admin
  identity receives the requested automatic exemption; do not infer exemption
  merely from an arbitrary user's `role=admin`.
- Add durable tables for price versions/overrides, user cost limits, usage/cost
  events and activity content. Model user limits as independent optional USD
  amounts; use exact decimal storage/serialization and checked arithmetic.
- Retain historical token records as historical data. Do not fabricate dollar
  balances or silently convert old token limits into dollar caps.
- Define admission, in-progress, completed and unresolved-accounting states and
  stable event IDs so restart/replay cannot double-charge usage.

Verification:

- Fresh and upgrade bootstrap; repeated/concurrent migration; rollback on late
  DDL failure; existing data preserved.
- One Admin row across restarts and config-key changes; key works for proxy
  traffic and cannot authenticate management routes.
- Legacy main-key requests attribute usage/activity to Admin, including live
  configuration reload and detached completion contexts.

Guards: no raw-key principal in persisted content, no automatic Admin password,
no new dollar limits, no production migration until tested against a snapshot.

## Phase 2 — Versioned prices and durable cost events

Implementation:

- Add a validated default-price catalog with atomic automatic refresh, manual
  overrides and last-known-good retention on refresh failure. Missing price
  remains explicit; failed refresh must not replace valid rates with zeros.
- Resolve billable model/provider identity separately from displayed/requested
  aliases. Snapshot applicable rate/version with each usage event.
- Copy canonical usage fixtures to price uncached input, cache read/write,
  ordinary output and reasoning without double counting. Preserve unknown or
  inconsistent accounting rather than labeling it free usage.
- Use exact decimal calculations, immutable event identities and transactional
  persistence. Preserve legitimate separately reported models/attempts.
- Start cost totals from activation; provide management reporting and audited
  price overrides through established handler/DTO conventions.
- Mark missing reliable usage unresolved and block further budgeted requests
  until an administrator resolves the cost, as confirmed in question 30.

Verification:

- Known provider token breakdowns, reasoning/cache overlap, aliases, manual
  prices, stale catalog refresh, price changes, duplicate event replay,
  additional-model records and incomplete usage.
- Existing events keep their original prices after catalog changes.
- Interrupted persistence is recoverable and never silently loses or duplicates
  charged usage; accounting unavailability blocks new budgeted requests.

Guards: do not use floating-point values as financial truth; do not rely on the
old best-effort usage queue for enforcement; do not charge based on the public
alias when it resolves to a different billed model.

## Phase 3 — Cost admission and rolling availability

Implementation:

- Replace active token quota enforcement with cost-limit enforcement; retain
  tokens as the underlying measured usage and explanatory UI detail.
- Enforce lifetime, daily, rolling seven-day and rolling thirty-day amounts
  together. Reset daily limits at Europe/London midnight, as confirmed in
  question 29; seven-day and thirty-day limits remain rolling windows.
- Copy injected-clock quota tests for precise boundaries. Derive rolling
  availability from expenditure timestamps, considering all blocking limits;
  lifetime exhaustion has no automatic recovery timestamp.
- Check each new logical HTTP request or WebSocket turn. Let accepted requests
  finish even if completion causes overspend; do not add cost reservations.
- Block budgeted requests for unpriced models until manually priced. Keep Admin
  unrestricted while displaying unknown or unresolved costs honestly.
- Add structured failure details and reset/retry information through the actual
  SDK error path; explicitly extend its header contract if Retry-After requires
  it. Do not invent a currently nonexistent error-header interface.
- Surface configured allowed models through the existing policy mechanism;
  preserve provider/deny rules if a user already has a more complex policy.

Verification:

- Simultaneous limits, rolling expiration, equality boundaries, lifetime cap,
  price absence, database failure, in-flight overspend and live policy edits.
- Consistent HTTP and repeated WebSocket behavior; reset details point to the
  time all applicable rolling constraints permit further requests.
- Existing alias, model-list filtering and provider fallback permission tests.

Guards: no calendar-month interpretation of rolling thirty days; no advertised
automatic reset for a still-exhausted lifetime cap; no management privilege
escalation through the linked proxy key.

## Phase 4 — Durable full text requests, responses and sessions

Implementation:

- Replace the capped best-effort preview as the source of full content with
  durable content storage that supports chunking and incremental reads. Keep
  previews as a separate UI optimization.
- Remove request text retention/size cutoffs from storage, cleanup and reads.
  Do not load every request's full content in activity list responses.
- Preserve credential sanitization and attachment references/metadata; omit
  embedded binary media as selected by the user.
- Capture incoming decoded text for compressed clients, full returned JSON and
  SSE events, and original incoming/returned WebSocket frames. Preserve native
  flushing, cancellation, write errors, protocol semantics and response status.
- Make logging admission failure reject the request. Define durable recovery for
  already accepted activity if persistence fails during output, consistent with
  allowing accepted requests to finish. Do not silently discard stream chunks.
- Group by authenticated user plus reliable explicit conversation/session ID.
  Link previous-response references only when the captured relationship is
  known; retain ungrouped requests when identity is absent.

Verification:

- Long conversations beyond both old caps, compressed bodies, multi-role/tool
  content, attachments, invalid/non-JSON input, streamed responses, errors,
  cancellation, reconnect and concurrent users with equal session strings.
- Real DB failure and process-restart recovery; rejected admission does not
  contact the upstream model. List and content pagination preserve all text.

Guards: no raw authentication headers in activity; no whole-response buffering
that delays streaming; no session grouping by timing, API key, cache key or a
generic user ID; no finite retention job left deleting new complete content.

## Phase 5 — Native UI and administration

Implementation:

- Extend existing Users components and API normalization, preserving the current
  shell, authentication, themes, locales and connection-change guards.
- Show the linked Admin account and its tracked spending/activity with clear
  exemption status. Never display the full configuration key.
- Replace token quota controls with USD limits and daily/seven-day/thirty-day/
  lifetime usage, remaining amounts and next availability.
- Add All models versus explicit model selection using existing shared picker
  interaction patterns, preserving backend case sensitivity and policy scopes.
- Add default-price status, automatic-update information and manual price edits.
- Add prompt preview/search, complete message/tool inspection, request/response
  tabs, sanitized JSON and reliable session navigation with incremental loading.
- Update all four locales and operational documentation around final behavior.

Verification:

- Focused API normalization, financial display, time-window and content render
  safety tests. Test stale responses after user/connection changes.
- `bun run verify` with pinned Bun, plus real browser navigation, price/model/
  budget edits, prompt/response/session drilldown, themes and mobile layout.

Guards: no second application shell, iframe or management login; no client-side
enforcement substituted for backend checks; no assumptions that unknown cost is
zero; no use of the deny-picker normalizer for a case-sensitive allowlist.

## Phase 6 — Integration, publication and deployment

- Run meaningful PostgreSQL integration/race tests for changed storage/runtime
  paths, full Linux Go suite and required server build. Account for previously
  documented unrelated macOS/executor issues without broad unrelated repairs.
- Run a real isolated upstream fixture through HTTP, SSE and Responses WebSocket
  for Admin attribution, permissions, priced usage, rolling caps, durable full
  content, outage behavior and restart recovery.
- Build the vendored single-file frontend with explicit VERSION; verify same
  shell and named-session logout behavior in a browser.
- Save source, requirements, plan, tests and operational instructions to the
  existing GitHub fork. Scan newly published paths for accidental credentials.
- Deploy a tested schema/binary/panel sequence with database-aware rollback and
  backups, preserving production keys, provider configuration and existing data.
  Keep automatic replacement of the customized panel disabled.
- Verify live with temporary isolated accounts and remove all temporary access.
  Leave spending amounts/model defaults for the user to configure as requested.

Completion requires all selected behaviors to work in the native UI and through
the actual proxy protocols, not only passing helper-unit tests.
