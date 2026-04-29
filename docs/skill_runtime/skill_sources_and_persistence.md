# Skill Sources And Persistence

## 目标

`skill_runtime` 现在区分三类 skill 来源：

- `system`
- `external`
- `runtime`

这样可以把平台内置能力、业务域扩展能力、运行时动态创建能力明确分层。

## 来源层级

### `system`

系统级 skills。

来源：

- `skills_runtime.skill_dir`

用途：

- 平台通用能力
- 通用 shell / file / fetch / runtime smoke

约束：

- 不应放业务域 skill
- 不应放 ABAP / ERP / coding pack 这类垂直能力

### `external`

外部扩展 skills。

来源：

- `skills_runtime.extra_skill_dirs`
- `aicli chat --skills-dir <dir>`

用途：

- 项目私有 skill pack
- 业务域 skill pack
- 团队扩展包

### `runtime`

运行时动态创建的 skills。

来源：

- `POST /api/skills`
- `PUT /api/runtime/skills/{name}`
- `POST /api/runtime/skills/batch`

默认情况下它们只存在于当前 registry 中；如需落盘，必须显式持久化。

当 skill 带有 prompt 配置时，当前运行时支持 companion `prompt.md`：

- 读取时：如果 skill 目录下存在 `prompt.md`，会自动装载
- 持久化时：如果 skill 带有 prompt，运行时会同时维护 `skill.yaml` 与 `prompt.md`
- `prompt.md` 使用 `System / User` 分段时，会映射回 `systemPrompt / userPrompt`

## 加载优先级

多目录加载的默认规则：

1. 先加载系统目录
2. 再按声明顺序加载外部目录
3. 如果出现同名 skill，采用“先加载目录优先”

含义：

- 系统级 skill 默认优先于外部 skill
- 外部目录之间也按声明顺序决定覆盖权

当前不会自动覆盖已有高优先级同名 skill。

## 目录配置

配置文件：

```yaml
skills_runtime:
  enabled: true
  skill_dir: ./.agents/skills
  extra_skill_dirs:
    - ./custom-skills
    - ./domain-skills
  read_only: false
  disable_import: false
  disable_persist: false
  disable_reload_ops: false
  disable_hot_reload_ops: false
```

CLI 追加目录：

```bash
aicli chat --skills-dir ./team-skills --skills-dir ./project-skills
```

## 来源可见性

每个 skill 现在都有运行时来源元数据：

```json
{
  "source": {
    "path": ".../skill.yaml",
    "dir": ".../skills/my-pack/my-skill",
    "layer": "external",
    "prompt_path": ".../skills/my-pack/my-skill/prompt.md"
  }
}
```

`GetStats` 还会额外返回：

- `skill_dirs`
- `source_summary`
- `mutation_policy`

## API 过滤

以下接口支持来源过滤：

- `GET /api/skills`
- `GET /api/runtime/skills/search`
- `GET /api/runtime/skills/stats`
- `GET /api/runtime/skills/export`

支持的 query 参数：

- `source_layer=system|external|runtime`
- `source_dir=<path>`

示例：

```bash
curl "http://127.0.0.1:8081/api/skills?source_layer=external"
curl "http://127.0.0.1:8081/api/runtime/skills/search?q=shell&source_dir=C:/team-skills"
curl "http://127.0.0.1:8081/api/runtime/skills/stats?source_layer=system"
curl "http://127.0.0.1:8081/api/runtime/skills/export?source_layer=external"
```

## 管理保护

以下变更类接口现在要求：

- loopback 请求
- 或有效 `admin_token`
- 或匹配 `skills_runtime.admin_roles` 的管理员角色

受保护的接口包括：

- `POST /api/skills`
- `PUT /api/runtime/skills/{name}`
- `DELETE /api/runtime/skills/{name}`
- `POST /api/runtime/skills/batch`
- `POST /api/runtime/skills/import`
- `POST /api/runtime/skills/reload`
- `POST /api/runtime/skills/hot-reload/start`
- `POST /api/runtime/skills/hot-reload/stop`
- `POST /api/runtime/skills/hot-reload/reload`

此外还支持轻量治理策略：

- `read_only`
- `disable_import`
- `disable_persist`
- `disable_reload_ops`
- `disable_hot_reload_ops`

这些 mutation policy 现在也支持运行期更新，并在服务端绑定 `configManager` + 配置文件时定点写回 YAML。

含义：

- `read_only=true`：禁止 `create/update/delete/batch/import`
- `disable_import=true`：单独禁止 `import`
- `disable_persist=true`：禁止 `persist=true`、external skill 回写、`delete_file=true`
- `disable_reload_ops=true`：禁止 `POST /api/runtime/skills/reload`
- `disable_hot_reload_ops=true`：禁止 `POST /api/runtime/skills/hot-reload/*`

同时，原有的搜索运维接口也仍然受同一 token 保护：

- `GET /api/runtime/skills/search/stats`
- `POST /api/runtime/skills/search/reindex`

Header 用法：

```http
Authorization: Bearer <token>
```

或：

```http
X-Skills-Admin-Token: <token>
```

如果启用了 scope/auth resolver，也可以通过 header 或 JWT claims 传管理员角色：

```yaml
skills_runtime:
  admin_roles: ["skills-admin", "platform-admin"]
  role_headers: ["X-Skills-Role", "X-Skills-Auth-Role", "X-Role", "X-Authenticated-Role"]
  role_claims: ["role", "roles"]
```

说明：

- 显式角色 header 优先于 JWT claims
- JWT claims 依赖 `jwt_claims_enabled=true` 和全局 `auth.jwt_secret`
- 如果网关认证中间件把角色放在 gin context `role`，或把 claims 放在 `claims / jwt_claims / user_claims / auth_claims`，`skills` 也会自动透传复用

## 变更审计与监控

skills 变更接口现在会同步写入 runtime metrics：

- `skill_mutation_actions_total`

标签：

- `action`
- `outcome`
- `access_mode`

查看入口：

- `GET /api/runtime/skills/stats`
- `GET /api/runtime/mutation/policy`
- runtime admin logs
- 可选的外部 metrics 导出链路（若已接入）

典型动作包括：

- `skill_create`
- `skill_update`
- `skill_delete`
- `skill_batch_create`
- `skill_import`
- `skill_reload`
- `skill_hot_reload_start`
- `skill_hot_reload_stop`
- `skill_hot_reload_reload`

## 持久化 runtime skill

创建或更新 runtime skill 时，可以显式落盘到外部目录。

支持接口：

- `POST /api/skills?persist=true`
- `PUT /api/runtime/skills/{name}?persist=true`
- `POST /api/runtime/skills/batch?persist=true`
- `POST /api/runtime/skills/import?persist=true`

可选参数：

- `target_dir=<path>`

规则：

- 如果提供 `target_dir`，则持久化到该目录
- 如果未提供 `target_dir`，默认落到第一个 `external` 目录
- 如果没有可用外部目录，则请求失败
- 明确禁止把 runtime skill 写入 `system` 目录
- 如果 skill 带有 prompt，prompt 会优先拆到 companion `prompt.md`

示例：

```bash
curl -X POST "http://127.0.0.1:8081/api/skills?persist=true&target_dir=C:/team-skills" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "team_echo",
    "description": "Team-local echo skill",
    "triggers": [{"type":"keyword","values":["team echo"],"weight":1}]
  }'
```

## Reload 行为

### 热加载

`POST /api/runtime/skills/hot-reload/start`

支持：

- `dir`
- `dirs`

### 普通 reload

`POST /api/runtime/skills/reload`

支持：

- request body: `dir` / `dirs`
- query: `dir`
- 默认回退到当前 loader 持有的目录列表

示例：

```bash
curl -X POST http://127.0.0.1:8081/api/runtime/skills/reload \
  -H "Content-Type: application/json" \
  -d '{
    "dirs": ["./.agents/skills", "./custom-skills"]
  }'
```

## 当前建议

- 系统目录只放平台通用 skill
- 业务域能力一律走外部目录
- 动态创建 skill 默认保持 `runtime`，只有在明确 `persist=true` 时才落盘
- 如果要做团队级 skill 管理，优先围绕外部目录和导出接口组织，而不是修改系统目录
