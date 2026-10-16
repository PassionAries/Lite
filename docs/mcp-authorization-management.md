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

This replaces the server scope when `all_nodes` or `target_uuids` is sent. Omit
both to keep the current scope. Sending `target_uuids` without `all_nodes` is an
explicit allowlist; an empty list is rejected and does not become all servers.
`all_nodes: true` is required for dynamic scope. `all_nodes: false` without a
replacement list keeps the stored allowlist, or is rejected if the grant was
all servers.

Omit `note` to keep the current note; an explicit empty string clears it. Omit
**both** `duration_minutes` and `long_term` to preserve the current expiry
policy. If only `duration_minutes` is sent, the current long-term flag is kept
(a long-term grant stays long-term). Switching to a temporary grant requires
`long_term: false` and a valid duration. Temporary duration starts at the time
of saving. Long-term mode uses the existing long-term expiry policy.

The endpoint requires a same-origin administrator browser session belonging to
the grant's owner and fresh password/TOTP or passkey verification, just like
initial approval. API keys cannot edit grants. Ended, expired, or revoked grants
cannot be revived through this endpoint.

The grant ID, token family, OAuth client, and creation time are preserved.
Unconsumed, unexpired refresh tokens follow the edited expiry atomically; expired
or consumed tokens are not revived. Access tokens remain short lived and each
use still checks the live grant. The client does not need to restart OAuth.

Changing server scope or expiry interrupts running operations, closes interactive
terminals, and drops undelivered MCP exec/file events for this grant. Note-only
edits do not interrupt. Agent delivery and editing share the existing
remote-delivery gate. Queued work is allowed if the node is still in scope, the work carries a
non-zero expiry, and that expiry is not later than the live grant (an extension
must not drop in-flight work). Terminal ready/write/read/resize re-check the
live grant.
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
