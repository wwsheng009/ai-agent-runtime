# Search Monitoring Playbook

> 迁移说明（2026-03-30）：
> - `ai-gateway` 已下线 `/monitor/search-admin` 与 `/monitor/search`
> - 独立 `runtime-server` 请改用 `/api/runtime/skills/search/stats`、`/api/runtime/status`、`/api/runtime/health` 和服务日志
> - 如果部署额外接入了指标导出链路，再结合 `search_admin_actions_total`、`search_reindex_runs_total` 做观测

## Scope

This playbook covers four common operational failures in search-related runtime behavior:

- forbidden admin access spikes
- reindex rate-limit spikes
- reindex failures
- semantic search hit-rate degradation

Use this together with:

- `docs/skill_runtime/search_monitoring_guide.md`
- `GET /api/runtime/skills/search/stats`
- `GET /api/runtime/status`
- `GET /api/runtime/health`
- runtime service logs and any external metrics pipeline

## 1. Forbidden Admin Access Spikes

### Symptom

- `search_admin_actions_total{outcome="forbidden"}` grows continuously
- `/api/runtime/skills/search/stats` or service logs show repeated admin authorization failures

### Quick Checks

1. Confirm source IP pattern:

```bash
curl http://127.0.0.1:8081/api/runtime/skills/search/stats
```

2. Check whether access should be local-only:

- `GET /api/runtime/skills/search/stats`
- `POST /api/runtime/skills/search/reindex`

3. Check config:

```yaml
skills_runtime:
  admin_token: "${SKILLS_RUNTIME_ADMIN_TOKEN:-}"
```

### Likely Causes

- remote caller is hitting admin endpoints without token
- wrong bearer token
- automation still assumes admin endpoints are public
- reverse proxy is forwarding requests from non-loopback IPs

### Remediation

- add or fix `SKILLS_RUNTIME_ADMIN_TOKEN`
- update automation to send:
  - `Authorization: Bearer <token>`, or
  - `X-Skills-Admin-Token: <token>`
- restrict admin callers at ingress
- confirm whether loopback assumptions still hold in deployment topology

## 2. Reindex Rate-Limit Spikes

### Symptom

- `search_reindex_runs_total{outcome="rate_limited"}` increases
- `/api/runtime/skills/search/reindex` returns `429`
- response contains `Retry-After`

### Quick Checks

1. Inspect aggregated summary:

```bash
curl http://127.0.0.1:8081/api/runtime/skills/search/stats
```

2. Inspect cooldown config:

```yaml
skills_runtime:
  reindex_cooldown: "${SKILLS_RUNTIME_REINDEX_COOLDOWN:-30s}"
```

3. Check whether reindex is being triggered by:

- human operator repeatedly
- automation loop
- deployment hook
- external incident workflow

### Likely Causes

- repeated manual retries
- automation with no backoff
- too-short incident loop interval
- forced reindex being used as a normal path

### Remediation

- increase cooldown if operator traffic is expected
- add retry backoff in automation
- use `force=true` only for emergency operations
- separate routine reload from full semantic reindex

## 3. Reindex Failures

### Symptom

- `search_reindex_runs_total{outcome="failed"}` increases
- `/api/runtime/skills/search/reindex` returns `500`
- audit log shows `action=search_reindex outcome=failed`

### Quick Checks

1. Check application logs for search admin audit entries.
2. Check whether embedding router is enabled:

```yaml
router:
  enableEmbedding: true

embedding:
  enabled: true
```

3. Confirm skills are loadable and registry is populated.
4. Confirm startup bootstrap succeeded.

### Likely Causes

- embedding router not initialized
- malformed or broken skill registry state
- hot reload introduced invalid skill state
- runtime startup path skipped skills runtime bootstrap

### Remediation

- verify `skills_runtime.enabled=true`
- verify runtime config file path and skill directory
- run a normal `GET /api/skills` and `GET /api/runtime/skills/stats`
- fix invalid skill manifests, then retry reindex
- restart standalone `runtime-server` if bootstrap state is inconsistent

## 4. Semantic Search Hit-Rate Degradation

### Symptom

- users report semantic search misses
- `GET /api/runtime/skills/search?q=...` falls back to lexical or returns no results
- `resolved_mode` no longer becomes `semantic` when expected

### Quick Checks

1. Compare search API behavior:

```bash
curl "http://127.0.0.1:8081/api/runtime/skills/search?q=search%20customer%20orders%20in%20sap"
curl "http://127.0.0.1:8081/api/runtime/skills/search?q=search%20customer%20orders%20in%20sap&mode=semantic"
curl "http://127.0.0.1:8081/api/runtime/skills/search?q=search%20customer%20orders%20in%20sap&mode=lexical"
```

2. Check embedding index stats:

```bash
curl http://127.0.0.1:8081/api/runtime/skills/stats
```

3. Compare current skill inventory:

```bash
curl http://127.0.0.1:8081/api/skills
```

4. If needed, run manual reindex:

```bash
curl -X POST http://127.0.0.1:8081/api/runtime/skills/search/reindex
```

### Likely Causes

- new skills were added but not indexed yet
- skill descriptions/capabilities do not provide enough semantic text
- hot reload sequence changed registry contents
- operators expect lexical triggers to behave like semantic matches

### Remediation

- run manual reindex
- improve `description`, `capabilities`, `tags`, and semantic trigger coverage in skills
- verify hot reload completed successfully
- tune search mode usage:
  - `auto` for normal clients
  - `semantic` for semantic-only validation
  - `lexical` for regression comparison

## Recommended Queries

If your standalone runtime deployment exports observability metrics into Prometheus or another metrics backend, the following queries remain applicable.

### Forbidden access

```promql
sum by (access_mode) (search_admin_actions_total{outcome="forbidden"})
```

### Reindex failures

```promql
sum(search_reindex_runs_total{outcome="failed"})
```

### Reindex rate limiting

```promql
sum(search_reindex_runs_total{outcome="rate_limited"})
```

### Search admin activity by action

```promql
sum by (action) (search_admin_actions_total)
```

## Escalation Rule

Escalate when any of the following is true:

- `forbidden` keeps increasing after credential fix
- `failed` reindex continues after configuration and manifest validation
- semantic misses persist after successful reindex
- repeated `rate_limited` events indicate automation is still misconfigured

## Minimal Triage Order

1. check `GET /api/runtime/skills/search/stats`
2. check `GET /api/runtime/status`
3. check `GET /api/runtime/health`
4. check `GET /api/runtime/skills/stats`
5. check `GET /api/skills`
6. run `POST /api/runtime/skills/search/reindex`
7. inspect runtime audit logs and any external metrics pipeline
