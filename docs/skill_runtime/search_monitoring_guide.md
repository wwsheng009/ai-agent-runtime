# Search Monitoring Guide

> 迁移说明（2026-03-30）：
> - `ai-gateway` 已下线 `/monitor/search-admin`、`/monitor/search`、`/monitor/metrics` 这类 runtime 专属监控桥接
> - 当前应直接面向独立 `runtime-server` 的 `/api/skills/search/*`、`/api/skills/runtime/*` 与服务日志
> - `search_admin_actions_total`、`search_reindex_runs_total` 等指标常量仍存在于 runtime 内部；若要做 Prometheus 抓取，需要额外接入指标导出链路

## Scope

This guide covers runtime monitoring and operations for search-related skill platform capabilities:

- local semantic search
- search admin audit actions
- reindex operations
- runtime admin API access
- optional external metrics export

The related implementation lives in:

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\observability\metrics.go`

## Runtime/Admin Endpoints

### 1. Search Admin Stats

`GET /api/skills/search/stats`

Returns the runtime-side search admin summary and current search telemetry snapshot.

Main fields:

- `search`
- `search_admin`
- `embedding`
- `runtime`
- `validation`

Use this when you want the current runtime search state without depending on the old gateway `/monitor/*` bridge.

Example:

```bash
curl \
  -H "Authorization: Bearer $SKILLS_RUNTIME_ADMIN_TOKEN" \
  http://127.0.0.1:8081/api/skills/search/stats
```

### 2. Manual Reindex

`POST /api/skills/search/reindex`

Behavior:

- loopback requests are allowed by default
- remote requests require `skills_runtime.admin_token`
- default cooldown is `30s`
- returns `429` with `Retry-After` during cooldown
- `force=true` skips cooldown check

Accepted auth headers:

- `Authorization: Bearer <token>`
- `X-Skills-Admin-Token: <token>`

Examples:

```bash
curl -X POST \
  -H "X-Skills-Admin-Token: $SKILLS_RUNTIME_ADMIN_TOKEN" \
  http://127.0.0.1:8081/api/skills/search/reindex
```

```bash
curl -X POST \
  -H "X-Skills-Admin-Token: $SKILLS_RUNTIME_ADMIN_TOKEN" \
  "http://127.0.0.1:8081/api/skills/search/reindex?force=true"
```

### 3. Runtime Status

`GET /api/skills/runtime/status`

Use this to inspect current runtime wiring, runtime validation state, provider/MCP status, and config-derived runtime metadata.

### 4. Runtime Health

`GET /api/skills/runtime/health`

Use this to inspect a summarized health view derived from runtime status.

### 5. Audit Logs

Search admin actions are also written into runtime-side logs. This is now the primary source for per-request operational triage when the old gateway monitor bridge is absent.

## Metrics

### `search_admin_actions_total`

Counts search admin actions.

Labels:

- `action`
- `outcome`
- `access_mode`

Typical values:

- `action=search_stats`
- `action=search_reindex`
- `outcome=success|forbidden|rate_limited|failed`
- `access_mode=loopback|token|denied`

### `search_reindex_runs_total`

Counts `search_reindex` executions by result.

Labels:

- `action`
- `outcome`
- `access_mode`

This is narrower than `search_admin_actions_total` and is useful for alerting directly on reindex behavior.

## PromQL Examples

These queries are still valid if your standalone runtime deployment exports the runtime metrics registry into Prometheus or another metrics backend.

### Total Search Admin Actions

```promql
sum(search_admin_actions_total)
```

### Search Admin Actions by Outcome

```promql
sum by (outcome) (search_admin_actions_total)
```

### Search Admin Actions by Access Mode

```promql
sum by (access_mode) (search_admin_actions_total)
```

### Reindex Success Rate

```promql
sum(search_reindex_runs_total{outcome="success"})
/
sum(search_reindex_runs_total)
```

### Reindex Rate-Limit Count

```promql
sum(search_reindex_runs_total{outcome="rate_limited"})
```

### Forbidden Remote Search Admin Access

```promql
sum(search_admin_actions_total{outcome="forbidden"})
```

## Operational Checks

### Healthy State

Typical healthy expectations:

- `search_stats` requests are mostly `success`
- `search_reindex` has low frequency
- `rate_limited` reindex events are rare
- `forbidden` events are either zero or clearly expected

### Suspicious State

Investigate when you see:

- sustained growth in `search_admin_actions_total{outcome="forbidden"}`
- repeated `search_reindex_runs_total{outcome="rate_limited"}`
- repeated forced reindex operations
- unexpected non-loopback admin access

## Config

Relevant config lives in `skills_runtime`:

```yaml
skills_runtime:
  admin_token: "${SKILLS_RUNTIME_ADMIN_TOKEN:-}"
  reindex_cooldown: "${SKILLS_RUNTIME_REINDEX_COOLDOWN:-30s}"
```

See:

- `E:\projects\ai\ai-agent-runtime\backend\configs\config.yaml`
- `E:\projects\ai\ai-agent-runtime\backend\internal\agentconfig\config.go`

## Suggested Dashboard Blocks

Recommended panels:

- total search admin actions
- search admin actions by outcome
- search admin actions by access mode
- total reindex runs
- reindex outcomes
- forbidden search admin attempts over time
- rate-limited reindex attempts over time

## Related Docs

- `docs/skill_runtime/platform_runtime_roadmap.md`
- `docs/skill_runtime/skills_api_result_contract.md`
- `docs/skill_runtime/skills_api_stream_contract.md`
