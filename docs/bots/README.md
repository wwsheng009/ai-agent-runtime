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
| [05-review-and-hardening.md](./05-review-and-hardening.md) | **方案审查与加固**：三个硬缺口（事件无隔离/通知无中台/多前端无契约护栏）、消息分发可靠性与性能、多通道治理、Bot 交互设计、现有架构优化项与决策清单 |
| [06-target-architecture.md](./06-target-architecture.md) | **目标架构（定稿）与核实**：D1–D10 决策按最佳实践定稿、目标架构图（部署/组件/入站/通知/配置/数据）、组件现状映射、逐条代码级核实与待建清单 |
| [07-config-ux.md](./07-config-ux.md) | **配置页面与交互设计**：四终端（gateway console / React / 微前端 / TUI）页面落点与线框、扫码与 webhook 流程、凭据+操作逻辑两层配置项、三处存储与密钥语义、便捷性与验收 |

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
6. **审查后新增前置阶段 P-1（基础加固，2–3 人日）**：给事件总线加异步订阅原语（有界队列 + panic 隔离）、
   抽出 `internal/notify` 通知中台（把 gateway 的优先级/静默/合并/presence 策略变成共享内核）、
   建立 `internal/connectors/api` 契约包与三宿主契约测试矩阵。详见 [05-review-and-hardening.md](./05-review-and-hardening.md)。
7. **决策已定稿**（[06 §1](./06-target-architecture.md)）：notify 中台引入（绞杀者迁移）、总线新增
   `SubscribeAsync`（不改现有语义）、webhook 仅经公网宿主、IM↔会话默认 1:1、偏好三级 + 高优先多通道去重扇出；
   目标架构图与 10 条代码级核实见该文档 §2/§4。
8. **部署约束（与多实例网关的关系）**：连接器首期只在**单个 gateway 实例（active）**运行；主备切换下
   webhook 由 LB 粘性路由到 active，轮询类通道靠单实例约束避免双消费者。
   多实例（双活）所需的防双写/共享协调/归属路由分级方案见
   [docs/gateway/15-multi-instance-architecture.md](../gateway/15-multi-instance-architecture.md)——
   **不需要自研实例间协议**。
9. **配置体验**（[07-config-ux.md](./07-config-ux.md)）：gateway console 新增一级「机器人」专用配置域，
   React/微前端各加同 Schema 页面，TUI 只做 `/bot` 最小操作；微信/QQ 扫码走 `actions`（自动刷新/回填/引导测试），
   Telegram 一步粘贴 token；密钥三处本地 0600 存储、GET 永不回显。

## 与现有通知链路的关系

现有 `Web Push + ntfy` 解决的是「**出站提醒**」（`docs/gateway/08-notifications.md`）；
本专题补的是「**双向会话**」：IM 来信 → 注入 agent turn → 回复回 IM，同时把审批/提问/完成等
注意力事件推给 IM（作为第三条出站通道）。两者共用同一批事件与注意力语义，不重复造轮子。
