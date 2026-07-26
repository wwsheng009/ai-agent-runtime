# Harness Settings（R5）

更新时间: 2026-07-25  
状态: **MVP 已交付**

## 目标

在 Web 控制台 Settings 增加 **Harness** 分区，运营项目级 harness 配置，而不引入 marketplace / FTS / 云同步。

## 产品范围

| 面板 | 能力 | 边界 |
| --- | --- | --- |
| Permissions | 读取项目 `.aicli/permissions.yaml` 规则与 allow/deny 列表 | **只读**；编辑请改磁盘文件 |
| Grants | 列出 / remember / revoke 项目级 durable grants | 危险工具不可 remember（后端规则） |
| Memory | project `memorystore` notes 的 list / keyword search / append | **非** FTS / vector / 云同步 |
| Plugins | 本地 catalog 列表 + trust / enable / disable | **无** marketplace |

## 入口

- Frontend：Settings → `harness` section（`HarnessSettingsPage`）
- Hook：`useHarnessControlPlane`（workspace path 来自 `runtimeClient.workspacePath`）
- API client：`frontend/src/api/runtime/harness.ts`

## Control-plane API

| Method | Path | 说明 |
| --- | --- | --- |
| `GET` | `/api/runtime/harness/permissions` | 项目 permissions 文件视图 |
| `GET` | `/api/runtime/harness/grants` | 当前 grants |
| `POST` | `/api/runtime/harness/grants` | `remember` / `revoke` |
| `GET` | `/api/runtime/harness/memory` | list / keyword search |
| `POST` | `/api/runtime/harness/memory` | append note |
| `GET` | `/api/runtime/harness/plugins` | 本地 plugin catalog |
| `POST` | `/api/runtime/harness/plugins/{id}` | trust / enable / disable |

所有请求以 `workspace_path` 绑定项目根。

## 验收证据

- Backend：`backend/internal/api/skills/harness_handlers.go`（+ tests）
- Frontend：
  - `frontend/src/components/workspace/settings/harness-settings-page.tsx`
  - `frontend/src/hooks/workspace/use-harness-control-plane.ts`
  - `frontend/src/api/runtime/harness.ts`
  - i18n：`settings.sections.harness` + `settings.harness.*`（en-US / zh-CN）
- 验证：
  - `npm test -- harness-settings-page.test.tsx` 通过
  - `npx tsc -b` 通过（2026-07-25）

## 非目标（默认不重开）

1. Plugin marketplace / 商店分发  
2. Memory FTS / embedding / session-end 自动摘要  
3. 在 UI 内直接编辑 permissions.yaml  
4. 云端跨设备 sync  

详见实施方案 §15.2 / §15.3。
