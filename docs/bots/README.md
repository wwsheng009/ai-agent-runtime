# Bots 专题：IM 通道接入（飞书 / QQ / 微信 / Telegram）

> 状态：**方案设计（待评审）** ｜ 日期：2026-10-13 ｜ 参考实现：`E:\projects\ai\codex-server`（`backend/internal/bots`）

本目录把「把外部 IM 接到 agent 上」这件事拆成需求、方案、宿主接入与移植路线四份文档，供
`gateway` / `aicli` / `runtime-server` 三个宿主共同落地。

| 文档 | 内容 |
|------|------|
| [01-requirements.md](./01-requirements.md) | 需求分析：角色/场景、功能与非功能需求、约束与验收 |
| [02-solution-design.md](./02-solution-design.md) | 解决方案：分层架构、核心抽象、数据模型、安全与可观测性 |
| [03-host-integration.md](./03-host-integration.md) | 三宿主接入点与「前端配置不复杂」的 schema 驱动设计（含 API 契约） |
| [04-migration-roadmap.md](./04-migration-roadmap.md) | 从 codex-server 的移植映射、分阶段计划、测试策略与风险 |

## 结论摘要（TL;DR）

1. **可复用**：codex-server 已经把「通道适配（`Provider`）」与「AI 后端（`AIBackend`）」解耦
   （`backend/internal/bots/types.go:39-147`），并提供流式/typing/长轮询等**能力接口**——这正是我们要的通用底座。
2. **需要重写的只有两处边界**：codex-server 的 `*store.MemoryStore` 与 `threads/turns` 模型
   （`service.go:55-85`、`workspace_thread_backend.go`）。本项目对应替换为
   **宿主 Backend 适配**（runtime-server 用 `POST /api/agent/chat`、aicli 用 `localActorRegistry.SubmitPrompt`、
   gateway 用隧道转发到 node/session）。
3. **落点**：共享核心放 `backend/internal/connectors/`，四家适配器放 `connectors/providers/*`；
   gateway/aicli/runtime-server 各有一个 bootstrap 装配文件（镜像 `gateway_bootstrap.go` 的写法）。
4. **配置体验**：不复制 codex-server 的 workspace/bot/binding/trigger 多层模型，收敛为
   **Connector（通道实例）+ Binding（IM 会话 → 本机会话）** 两层；前端统一由核心导出的
   **JSON Schema 渲染表单**，三个前端（gateway console / aicli micro web / runtime-server React）共用同一份字段定义，
   新增通道不改前端。
5. **首期建议**：P0 只做 **Telegram（webhook + 长轮询双模式）+ runtime-server** 端到端打通，
   再按 飞书 → QQ → 微信 扩展，并在 P1 把 gateway 变成「常驻 IM 前置」。

## 与现有通知链路的关系

现有 `Web Push + ntfy` 解决的是「**出站提醒**」（`docs/gateway/08-notifications.md`）；
本专题补的是「**双向会话**」：IM 来信 → 注入 agent turn → 回复回 IM，同时把审批/提问/完成等
注意力事件推给 IM（作为第三条出站通道）。两者共用同一批事件与注意力语义，不重复造轮子。
