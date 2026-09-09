# User management budgets and activity requirements

Status: requirements confirmed; implementation is in progress. This document
records the user's decisions and does not change the deployed service.

## Confirmed behavior

- Keep all controls within the existing native Users & activity page and shared
  management application.
- Associate the main proxy credential under `api-keys` with a visible default
  Admin user. There are no other existing proxy keys to migrate.
- Keep management authentication separate. Linking the proxy key must not grant
  that key management API or management UI access.
- Exempt the default Admin from model restrictions and spending limits while
  recording its usage, costs, and request activity.
- Let each user use all configured models or an explicit selection.
- Replace token quotas with independently configured lifetime, daily, rolling
  seven-day, and rolling thirty-day USD spending limits. All configured limits
  apply together. The daily limit resets at midnight in Europe/London, including
  the local daylight-saving transition.
- Finish already accepted requests and block new requests after a spending
  limit is reached. Do not reserve estimated maximum costs before admission;
  concurrent or long-running accepted requests can exceed the configured cap.
- The lifetime limit does not reset automatically. An administrator must raise
  it to restore access after exhaustion.
- Use published provider API list prices by default, with editable per-model
  overrides. Automatically refresh default prices for future requests while
  retaining manual overrides and the prices used for previously recorded costs.
- Provide manual price entry for models without known prices. Block budgeted
  users from using those models until a price is configured.
- Block new budgeted requests when cost accounting is unavailable. If an
  accepted request completes without reliable provider usage, block subsequent
  budgeted requests until an administrator manually resolves that cost. Do not
  substitute an estimated charge automatically.
- Start cost accounting with new usage from activation. Do not infer historical
  spending from existing token-only history.
- The user will configure budget amounts, allowed-model defaults, and other
  initial values. Do not invent spending limits.
- Report exhausted limits through API errors with reset/retry information and
  through the admin UI. A separate personal usage portal is not requested.
- Explain when access becomes available under rolling limits. Lifetime
  exhaustion requires an administrator action rather than a reset countdown.

## Confirmed activity requirements

- Show a searchable prompt preview with drilldown into the full submitted
  conversation, individual roles, system instructions, tool calls/results, and
  sanitized JSON.
- Capture incoming client requests and model responses, including streams. A
  second copy of the transformed provider request is not requested.
- Retain text conversations indefinitely, with no per-request text size limit.
  UI pagination or incremental loading must not discard stored conversation
  content.
- Support the proxy's supported clients and transports rather than a single
  named client.
- Offer individual request inspection and session grouping when a reliable
  client-provided identifier exists. Do not infer that unrelated requests form
  a conversation merely because they share a user or occur close together.
- Retain attachment references and metadata rather than media/file contents.
- Reject requests when their activity cannot be durably recorded. This replaces
  the current best-effort request activity behavior.

## Final clarification answers

29. Reset the daily cap at midnight in Europe/London.
30. Block subsequent budgeted requests until an administrator manually resolves
    a completed request's missing cost. Do not automatically estimate it.
