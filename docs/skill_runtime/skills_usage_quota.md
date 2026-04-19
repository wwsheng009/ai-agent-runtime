# Skills Usage And Quota

## 目标

为 `skill_runtime` 提供一层最小可用的 usage / quota 闭环，先解决：

- 谁在调用
- 调了多少次
- 大致用了多少 token
- 何时应该拒绝继续调用

当前实现是：

- 进程内
- 内存型
- 默认按 `tenant_id / project_id / user_id` 三层作用域聚合
- 可选接入 DB-backed usage ledger

它的目标是先形成平台治理闭环，不是最终版多租户计费系统。

## 配置

```yaml
skills_runtime:
  usage_tracking_enabled: true
  usage_ledger_enabled: false
  quota_enabled: false
  default_max_requests: 0
  default_max_tokens: 0
  scope_resolver_enabled: true
  tenant_headers: ["X-Skills-Tenant", "X-Skills-Auth-Tenant", "X-Tenant-ID", "X-Authenticated-Tenant"]
  project_headers: ["X-Skills-Project", "X-Skills-Auth-Project", "X-Project-ID", "X-Authenticated-Project"]
  user_headers: ["X-Skills-User", "X-Skills-Auth-User", "X-User-ID", "X-Authenticated-User"]
  role_headers: ["X-Skills-Role", "X-Skills-Auth-Role", "X-Role", "X-Authenticated-Role"]
  jwt_claims_enabled: true
  tenant_claims: ["tenant_id", "tenant", "tid"]
  project_claims: ["project_id", "project", "pid"]
  user_claims: ["user_id", "user", "uid", "sub"]
  role_claims: ["role", "roles"]
  admin_roles: ["skills-admin"]
  api_key_scopes:
    secret-scope-key:
      tenant_id: team-a
      project_id: ops
      user_id: alice
  quota_policies:
    tenants:
      team-a:
        max_requests: 1000
    projects:
      team-a/ops:
        max_requests: 300
    users:
      team-a/ops/alice:
        max_requests: 50
```

说明：

- `usage_tracking_enabled`
  - 是否记录 `execute / agent_chat` 的使用量
- `quota_enabled`
  - 是否启用默认 quota 拒绝逻辑
- `usage_ledger_enabled`
  - 是否把 usage 记录写入持久化 ledger（需要数据库）
- `scope_resolver_enabled`
  - 是否启用 header / API key 到 scope 的自动绑定
- `jwt_claims_enabled`
  - 是否从 Bearer JWT 中解析 scope
  - 当前仅支持使用全局 `auth.jwt_secret` 校验的 HMAC JWT
- `tenant_headers / project_headers / user_headers`
  - 依次尝试这些 header，补齐缺失的 scope 字段
- `tenant_claims / project_claims / user_claims`
  - Bearer JWT 中用于提取 scope 的 claim 名列表，按顺序尝试
- `role_headers / role_claims / admin_roles`
  - 管理类接口可通过角色授权，不必只依赖静态 `admin_token`
- `api_key_scopes`
  - 当请求没有显式 scope，但携带 Bearer token 或 `x-api-key` 时，按映射补 scope
- `default_max_requests`
  - 单个 scope 的默认请求上限
  - `0` 表示不限制
- `default_max_tokens`
  - 单个 scope 的默认 token 上限
  - `0` 表示不限制
- `quota_policies.tenants`
  - key: `tenant_id`
- `quota_policies.projects`
  - key: `tenant_id/project_id`
  - 兼容只写 `project_id`
- `quota_policies.users`
  - key: `tenant_id/project_id/user_id`
  - 兼容只写 `user_id`

规则：

- 只要 `quota_enabled=true`，usage tracking 会自动启用
- 当前 quota 是进程内计数，不跨实例共享

## 当前统计维度

按 `tenant_id / project_id / user_id` 组合 scope 记录：

- `tenant_id`
- `project_id`
- `user_id`
- `scope_key`

- `request_count`
- `execute_count`
- `agent_chat_count`
- `success_count`
- `failure_count`
- `prompt_tokens`
- `completion_tokens`
- `total_tokens`
- `last_request_at`
- `last_entrypoint`
- `last_skill`
- `entrypoint_counts`
- `skill_counts`

## Quota 判定

当前 quota key：

- `tenant_id / project_id / user_id`

默认值：

- `tenant_id = default`
- `project_id = default`
- `user_id = anonymous`

这意味着：

- 同一个 `user_id` 在不同 tenant 下不会共享 quota
- 同一个 `user_id` 在同一 tenant 下但不同 project 也不会共享 quota

scope 来源优先级：

- 请求体
- query 参数
- header / gin context 透传头
- JWT claims
- `api_key_scopes`
- 默认值

gin / 网关上下文说明：

- 如果上游 gin middleware 已经把 `user_id` / `tenant_id` / `project_id` / `role` 写入 gin context，网关会自动透传给 `skills`
- 如果 middleware 只写入 claims map，网关也会尝试从以下 context key 中提取：
  - `claims`
  - `jwt_claims`
  - `user_claims`
  - `auth_claims`

JWT claims 说明：

- `Authorization: Bearer <jwt>` 且 `jwt_claims_enabled=true` 时，runtime 会尝试解析 claims
- claim 默认名：
  - tenant: `tenant_id`, `tenant`, `tid`
  - project: `project_id`, `project`, `pid`
  - user: `user_id`, `user`, `uid`, `sub`
- 当前只做 scope 提取，不负责认证决策；JWT 无法校验时会直接忽略 claims 回退到后续来源

优先级：

- `user`
- `project`
- `tenant`
- `default`

也就是：

- `user > project > tenant > default`

### 请求配额

在执行前判断：

- `request_count >= default_max_requests`

超过后返回 `429`。

### Token 配额

在执行前按当前输入做估算：

- `existing_total_tokens + estimated_prompt_tokens > default_max_tokens`

超过后返回 `429`。

## Token 统计说明

当前实现采用两种来源：

- 如果响应里有真实 `usage`
  - 直接使用真实值
- 如果没有真实 `usage`
  - 使用输入 token 估算值
  - 再加输出文本的估算 token

因此它适合：

- 平台级治理
- 风险控制
- 粗粒度用量观察

它还不是：

- 精确计费
- 跨实例一致 quota
- 长周期财务对账

## API

### `GET /api/runtime/usage/stats`

可选参数：

- `tenant_id`
- `project_id`
- `user_id`

行为：

- 指定任一 scope 参数时，返回该 scope 的 usage 与 quota 剩余额度
- 不指定时，返回全局聚合摘要和已出现的 scope 列表

示例：

```bash
curl -H "X-Skills-Admin-Token: <token>" \
  "http://127.0.0.1:8081/api/runtime/usage/stats?tenant_id=team-a&project_id=ops&user_id=demo-user"
```

### `POST /api/runtime/usage/reset`

可选 body：

```json
{
  "tenant_id": "team-a",
  "project_id": "ops",
  "user_id": "demo-user"
}
```

行为：

- 指定任一 scope 参数：仅重置该 scope usage
- 不指定：清空所有 usage 统计

### `GET /api/runtime/usage/ledger`

读取持久化 usage ledger。

支持过滤：

- `tenant_id`
- `project_id`
- `user_id`
- `entrypoint`
- `skill`
- `success`
- `since`
- `limit`

说明：

- 只有在 `usage_ledger_enabled=true` 且数据库可用时，这个接口才可用
- 当前 ledger 使用现有 `token_usage_history` 表，额外 scope 信息放在 `metadata`

### `GET /api/runtime/usage/policy`

返回当前 runtime 内生效的 usage / quota policy 明细：

- `tracking_enabled`
- `quota_enabled`
- `default_max_requests`
- `default_max_tokens`
- `tenants`
- `projects`
- `users`

### `GET /api/runtime/auth/policy`

返回当前 runtime 内生效的 scope/auth resolver 策略摘要：

- `enabled`
- `jwt_claims_enabled`
- `jwt_secret_configured`
- `tenant_headers / project_headers / user_headers / role_headers`
- `tenant_claims / project_claims / user_claims / role_claims`
- `admin_roles`
- `api_key_scope_count`

### `PUT /api/runtime/auth/policy`

用于在运行期更新 scope/auth resolver 策略。

当前支持：

- `enabled`
- `jwt_claims_enabled`
- `tenant_headers / project_headers / user_headers / role_headers`
- `tenant_claims / project_claims / user_claims / role_claims`
- `admin_roles`
- `api_key_scopes`
- `replace`

说明：

- `jwt_secret` 仍来自全局 `auth.jwt_secret`，这里不允许运行期覆盖
- `replace=true` 会清空已有 headers / claims / admin_roles / api_key_scopes 后再写入
- 如果网关使用 `configManager` 启动，这些更新会同步回当前配置快照并触发 reload callback
- 如果 `configManager` 绑定了配置文件，auth policy 相关字段会同步写回 YAML 文件
- 文件写回只修改 `skills_runtime` 下的 auth/scope policy 字段，尽量避免覆盖其他配置项与 env 模板

### `DELETE /api/runtime/auth/policy`

当前支持删除两类条目：

- `field=api_key_scope`
- `field=admin_role`

### `PUT /api/runtime/usage/policy`

用于在运行期更新 policy。

示例：

```json
{
  "quota_enabled": true,
  "default_max_requests": 100,
  "projects": {
    "team-a/ops": {
      "max_requests": 30
    }
  }
}
```

说明：

- 默认是 merge 更新
- `replace=true` 时，会用传入 maps 替换现有 `tenants / projects / users`
- 如果服务端接了 `configManager` 且绑定了配置文件，usage/quota policy 会同步写回 YAML
- 文件写回只覆盖 `skills_runtime` 下这些字段：
  - `usage_tracking_enabled`
  - `quota_enabled`
  - `default_max_requests`
  - `default_max_tokens`
  - `quota_policies`
- `usage_ledger_enabled` 不在这个接口里修改

### `DELETE /api/runtime/usage/policy`

删除单条 override。

示例：

```json
{
  "level": "user",
  "key": "team-a/ops/alice"
}
```

## 管理保护

这两个接口要求：

- loopback 请求
- 或有效 `admin_token`

和 `search/stats`、`search/reindex`、skills mutation 管理接口保持一致。

### `GET /api/runtime/mutation/policy`

返回当前 runtime 内生效的 mutation policy：

- `read_only`
- `disable_import`
- `disable_persist`
- `disable_reload_ops`
- `disable_hot_reload`

### `GET /api/runtime/governance/policy`

返回统一治理视图，聚合：

- `mutation_policy`
- `usage_policy`
- `auth_policy`
- `persistence`
- `search_admin`

用途：

- 一次请求查看当前平台治理面是否完整接线
- 排查某个环境到底是“只在内存生效”还是“已接入持久化回写”

### `PUT /api/runtime/mutation/policy`

用于在运行期更新 mutation policy。

支持字段：

- `read_only`
- `disable_import`
- `disable_persist`
- `disable_reload_ops`
- `disable_hot_reload`

说明：

- 如果服务端接了 `configManager` 且绑定了配置文件，mutation policy 会同步写回 YAML
- 文件写回只覆盖 `skills_runtime` 下这些字段：
  - `read_only`
  - `disable_import`
  - `disable_persist`
  - `disable_reload_ops`
  - `disable_hot_reload_ops`

示例：

```json
{
  "read_only": true,
  "disable_import": true,
  "disable_persist": true
}
```

## Auth / Header 绑定

当前不是完整 JWT 用户系统，而是最小绑定层。

`skill_runtime` 会按以下顺序补 scope：

1. 请求体中的 `tenant_id / project_id / user_id`
2. query 参数中的 `tenant_id / project_id / user_id`
3. 配置的 header 列表
4. `api_key_scopes`
5. 默认值

此外，网关在转发 `/api/skills/*` 时，如果 gin 上下文里已有：

- `user_id`
- `tenant_id`
- `project_id`

会自动透传成：

- `X-Skills-Auth-User`
- `X-Skills-Auth-Tenant`
- `X-Skills-Auth-Project`

这让后续真正接入 auth middleware 时，不需要改 skills handler 主逻辑。

## 监控

当前 runtime metrics：

- `skill_usage_requests_total`
- `skill_usage_tokens_total`
- `skill_quota_denials_total`

查看入口：

- `GET /api/runtime/usage/stats`
- `GET /api/runtime/usage/policy`
- `GET /api/runtime/usage/ledger`
- runtime admin logs
- 可选的外部 metrics 导出链路（若已接入）

## 当前限制

- 内存实现，进程重启后会清零
- 当前只支持进程内配置型 scope 配额，不支持动态下发或外部存储
- ledger 当前是“可持久化历史记录”，但 quota 主计数仍以内存态为主
- 不支持跨实例共享 quota

## 下一步

如果继续往平台治理推进，下一阶段应做：

- 按 `tenant / project / user` 分层配置不同 quota policy
- 持久化 usage ledger
- quota policy 存储与动态下发
- API key / auth 绑定
- 更严格的真实 token 计量
