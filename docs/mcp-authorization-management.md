# MCP authorization editing and dynamic server scope

The companion Lite-web UI adds **Edit authorization** to active grant details and
**All servers (including future additions)** to approval and editing dialogs.
The frontend and backend changes must be released together.

## Server scope

- `all_nodes` is an explicit boolean, defaulting to `false` for existing grants
  and approval requests that omit it.
- `all_nodes: false` requires a non-empty `target_uuids` list of eligible nodes,
  preserving the existing allowlist behavior.
- `all_nodes: true` stores an empty target list and dynamically includes current
  and future eligible nodes. It is not a snapshot or the result of a search.
  It can be chosen even before any eligible servers exist.
- Dynamic scope does not bypass Agent remote-control switches, remote protocol
  requirements, MCP capability/version checks, or site-wide switches.
- Grant list/detail responses and the MCP `get_authorization` tool expose
  `all_nodes`, so an empty target list cannot be mistaken for an implicit wildcard.
- Explicit revocation also reaches nodes used by previous operations, including
  file-transfer sessions opened under dynamic scope.

## Editing an active grant

`POST /api/admin/mcp/leases/:id` accepts:

```json
{
  "all_nodes": true,
  "target_uuids": [],
  "note": "Infrastructure maintenance",
  "duration_minutes": 120,
  "long_term": false,
  "password": "<administrator password>"
}
```

This replaces the server scope and note. Omit **both** `duration_minutes` and
`long_term` to preserve the current expiry policy. Otherwise, supply a valid
duration and an explicit long-term choice using the same limits as approval.
Temporary duration starts at the time of saving. Long-term mode uses the existing
long-term expiry policy.

The endpoint requires a same-origin administrator browser session belonging to
the grant's owner and fresh password/TOTP or passkey verification, just like
initial approval. API keys cannot edit grants. Ended, expired, or revoked grants
cannot be revived through this endpoint.

The grant ID, token family, OAuth client, and creation time are preserved.
Unconsumed, unexpired refresh tokens follow the edited expiry atomically; expired
or consumed tokens are not revived. Access tokens remain short lived and each
use still checks the live grant. The client does not need to restart OAuth.

Saving interrupts running operations and interactive terminals under this grant.
Agent delivery and editing share the existing remote-delivery gate, and queued
requests are checked against the current server scope and expiry before delivery.
Edits cancel individual operations rather than permanently revoking the grant ID
on the Agent. Finished work is not rolled back.

Changes are recorded as `audit.mcp_update`, including scope and expiry. The UI
keeps the current expiry by default and only sends new duration fields when the
administrator selects **Change expiry**.

## Regression checks

```sh
go test ./web/mcp ./web/api/remote ./database/auditlog
```

The companion UI includes `tests/mcpAuthorization.test.ts` for request policy,
explicit opt-in, expiry preservation, scope narrowing, and locale coverage.
