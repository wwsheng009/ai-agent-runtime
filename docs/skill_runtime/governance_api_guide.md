# Skills Governance API Guide

> 迁移说明（2026-03-30）：
> - 本文描述的治理 API 现由 `E:\projects\ai\ai-agent-runtime\backend\cmd\runtime-server` 承载
> - `ai-gateway` 已不再暴露 `/monitor/skills-governance`、`/monitor/runtime-health`
> - 运行态治理与观测请改看 `/api/skills/governance/policy`、`/api/skills/runtime/*`

## Scope

This guide covers the unified governance surfaces for `skill_runtime`:

- mutation policy
- usage / quota policy
- auth / scope policy
- governance metrics summary

## Skills API

### `GET /api/skills/capabilities`

Returns unified capability descriptors for `skill / tool / workflow / agent`.

### `GET /api/skills/governance/policy`

Returns a unified governance view with:

- `mutation_policy`
- `usage_policy`
- `auth_policy`
- `persistence`
- `search_admin`

Use this when you want a single snapshot of the active governance state.

### `GET /api/skills/runtime/status`

Returns runtime connectivity and provider/MCP status.

For each MCP entry, the payload now includes:

- `trust_level`
  - `local`
  - `trusted_remote`
  - `untrusted_remote`
- `execution_mode`
  - `local_mcp`
  - `remote_mcp`

This is the main runtime surface for distinguishing:

- local process-backed MCP execution
- remote MCP execution that should be governed, not described as runtime sandboxing

### `GET /api/skills/mutation/policy`

Returns:

- `read_only`
- `disable_import`
- `disable_persist`
- `disable_reload_ops`
- `disable_hot_reload`

### `PUT /api/skills/mutation/policy`

Updates runtime mutation policy.

If `runtime-server` is backed by `configManager` and a config file, the following fields are also written back into `skills_runtime`:

- `read_only`
- `disable_import`
- `disable_persist`
- `disable_reload_ops`
- `disable_hot_reload_ops` (mapped from API field `disable_hot_reload`)

### `GET /api/skills/usage/policy`

Returns detailed runtime usage / quota policy.

### `PUT /api/skills/usage/policy`

Updates runtime usage / quota policy and, when config persistence is available, writes back:

- `usage_tracking_enabled`
- `quota_enabled`
- `default_max_requests`
- `default_max_tokens`
- `quota_policies`

### `GET /api/skills/auth/policy`

Returns detailed runtime auth / scope resolver policy.

### `PUT /api/skills/auth/policy`

Updates runtime auth / scope resolver policy and, when config persistence is available, writes back:

- `scope_resolver_enabled`
- `jwt_claims_enabled`
- `tenant_headers`
- `project_headers`
- `user_headers`
- `role_headers`
- `tenant_claims`
- `project_claims`
- `user_claims`
- `role_claims`
- `admin_roles`
- `api_key_scopes`

## Runtime Observability API

### `GET /api/skills/runtime/status`

Returns runtime connectivity and provider/MCP status.

Use this when you need a current runtime snapshot, including provider health and MCP connectivity.

### `GET /api/skills/runtime/health`

Returns a consolidated runtime health summary derived from the current runtime snapshot.

### `GET /api/skills/runtime/traces/stats`

Returns aggregated runtime trace statistics, including governance and patch-related summaries.

Use this when you want summarized recent runtime telemetry instead of raw traces.

### `GET /api/skills/runtime/traces/governance`

Returns a governance-focused trace view for recent runtime activity.

Typical uses:

- inspect denied events
- inspect patch decisions and provenance
- audit recent governance-sensitive traces

### `GET /api/skills/search/stats`

Returns search / indexing telemetry for the skill search subsystem.

### `GET /api/skills/usage/stats`

Returns scoped usage/quota counters.

## Go Client

The Go client in `backend/pkg/skillsapi/client.go` supports:

- `GetGovernancePolicy()`
- `GetMutationPolicy()`
- `UpdateMutationPolicy()`
- `GetUsagePolicy()`
- `UpdateUsagePolicy()`
- `GetAuthPolicy()`
- `UpdateAuthPolicy()`
- `GetRuntimeStatus()`
- `GetRuntimeHealth()`
- `GetSearchStats()`
- `GetUsageStats()`
- `GetUsageLedger()`

## Notes

- All governance update APIs retain rollback protection if persistence fails.
- File persistence is field-scoped; it does not rewrite unrelated `skills_runtime` settings intentionally.
- `jwt_secret` remains owned by global `auth.jwt_secret` and is not updated through skills governance APIs.
- Runtime trace governance/statistics endpoints are currently HTTP surfaces first; typed client helpers still focus on the core governance/policy APIs.
