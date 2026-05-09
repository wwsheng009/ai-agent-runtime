# ai-agent-runtime 迁移文档

> 从 `ai-gateway` 提取 multi-agent runtime 子系统为独立项目。  
> 本文记录当前已完成迁移、仍保留的设计约束，以及原始迁移计划在 2026-03-31 时点上的实际状态。

详细的前端迁移现状、能力边界和路线图，见 [docs/roadmap.md](E:/projects/ai/ai-agent-runtime/docs/roadmap.md)。

---

## 1. 当前状态概览

截至 2026-03-31，本项目已经不再处于“准备搭骨架”的阶段，而是进入了“后端 runtime 已独立、前端工作台已接线、剩余工作以工程收敛为主”的状态。

当前可以明确判断为：

- 后端 extraction 已完成第一轮主干迁移，独立 `backend/` 已可构建，并具备独立的 runtime server、aicli 和相关 demo/server 入口。
- 前端不再是预留目录，而是一个已运行的 `Vite + React + TypeScript` 项目。
- 前端已经接上本项目后端的核心运行时接口，包括聊天 SSE、session runtime 和 team orchestration 控制面。
- 原始迁移文档中的部分“Phase”已经完成，部分已被更现实的当前实现替代，因此本文会同时标注“原计划目标”和“当前实际状态”。

一句话总结：

`ai-agent-runtime` 已经完成“从网关中抽出 runtime 主体”的第一阶段，当前工作的重点不是继续搭空目录，而是把现有 runtime 控制面、类型边界和测试体系收稳。

---

## 2. 当前项目目录结构

下面的结构以当前仓库实际内容为准，不再沿用早期的理想化占位目录。

```text
ai-agent-runtime/
├── backend/                              # Go 后端（独立 Go module）
│   ├── cmd/
│   │   ├── aicli/
│   │   ├── echo-mcp-server/
│   │   ├── runtime-server/               # 当前主要 runtime HTTP 服务入口
│   │   ├── server/                       # 历史/兼容服务入口
│   │   ├── skillsapi-demo/
│   │   └── toolkit-mcp-server/
│   ├── internal/
│   │   ├── agent/
│   │   ├── agentconfig/
│   │   ├── api/
│   │   │   └── skills/                   # 当前 runtime / skills API handler
│   │   ├── artifact/
│   │   ├── background/
│   │   ├── bootstrap/
│   │   ├── capability/
│   │   ├── chat/
│   │   ├── chatcore/
│   │   ├── checkpoint/
│   │   ├── config/
│   │   ├── contextmgr/
│   │   ├── contextpack/
│   │   ├── embedding/
│   │   ├── errors/
│   │   ├── events/
│   │   ├── executor/
│   │   ├── hooks/
│   │   ├── llm/
│   │   ├── mcp/
│   │   ├── memory/
│   │   ├── migrate/
│   │   ├── model/
│   │   ├── observability/
│   │   ├── output/
│   │   ├── pkg/
│   │   ├── policy/
│   │   ├── profile/
│   │   ├── profileinput/
│   │   ├── prompt/
│   │   ├── runtimeserver/
│   │   ├── skill/
│   │   ├── team/
│   │   ├── toolbroker/
│   │   ├── toolkit/
│   │   ├── tools/
│   │   ├── types/
│   │   ├── usageledger/
│   │   └── workspace/
│   └── pkg/
│
├── frontend/                             # 前端项目（Vite + React + TypeScript）
│   ├── package.json
│   ├── vite.config.ts
│   ├── tsconfig.json
│   ├── public/
│   └── src/
│       ├── App.tsx
│       ├── main.tsx
│       ├── api/
│       │   └── runtime/                  # 按 agent-chat / sessions / teams / sse 模块化
│       ├── components/
│       │   └── workspace/
│       ├── data/
│       ├── hooks/
│       ├── lib/                          # 当前仅保留少量兼容/辅助层
│       ├── pages/
│       ├── styles/
│       └── types/
│
├── configs/
├── docs/
│   ├── multi-agents/
│   ├── skill_runtime/
│   ├── working/
│   ├── README.md
│   └── roadmap.md
├── scripts/
├── .github/
├── Makefile
├── README.md
└── MIGRATION.md
```

### 2.1 前端结构与原计划的差异

原计划中前端目录曾被写成：

- `src/api`
- `src/components`
- `src/pages`
- `src/hooks`
- `src/store`
- `src/types`
- `src/utils`

当前实际已经演进为：

- `src/api`
- `src/components`
- `src/data`
- `src/hooks`
- `src/lib`
- `src/pages`
- `src/styles`
- `src/types`

结论是：

- `src/api`、`src/hooks`、`src/types` 已经落地。
- `src/store`、`src/utils` 目前并不是主要组织方式。
- `src/data`、`src/styles`、`src/lib` 是当前实际结构的一部分，文档必须按这个现实来描述。

---

## 3. 来源映射与后端解耦原则

后端迁移的核心思路没有变化：从 `ai-gateway` 中提取 runtime 相关能力，同时避免把网关持久层、基础设施层和平台耦合一并复制进来。

### 3.1 主要来源映射

| 目标路径（backend/internal/） | 来源路径（ai-gateway/） | 当前判断 |
| --- | --- | --- |
| `types/` | `internal/runtime/types` | 已迁移，低耦合 |
| `errors/` | `internal/runtime/errors` | 已迁移，低耦合 |
| `output/` | `internal/runtime/output` | 已迁移，低耦合 |
| `events/` | `internal/runtime/events` | 已迁移，低耦合 |
| `capability/` | `internal/runtime/capability` | 已迁移，低耦合 |
| `policy/` | `internal/runtime/policy` | 已迁移，低耦合 |
| `memory/` | `internal/runtime/memory` | 已迁移，低耦合 |
| `artifact/` | `internal/runtime/artifact` | 已迁移，低耦合 |
| `checkpoint/` | `internal/runtime/checkpoint` | 已迁移，低耦合 |
| `executor/` | `internal/runtime/executor` | 已迁移，低耦合 |
| `hooks/` | `internal/runtime/hooks` | 已迁移，低耦合 |
| `contextmgr/` | `internal/runtime/contextmgr` | 已迁移，低耦合 |
| `prompt/` | `internal/runtime/prompt` | 已迁移，低耦合 |
| `background/` | `internal/runtime/background` | 已迁移，低耦合 |
| `observability/` | `internal/runtime/observability` | 已迁移，低耦合 |
| `embedding/` | `internal/runtime/embedding` | 已迁移，低耦合 |
| `migrate/` | `internal/runtime/migrate` | 已迁移，低耦合 |
| `contextpack/` | `internal/runtime/contextpack` | 已迁移，低耦合 |
| `profileinput/` | `internal/runtime/profileinput` | 已迁移，低耦合 |
| `toolbroker/` | `internal/runtime/toolbroker` | 已迁移，低耦合 |
| `tools/` | `internal/runtime/tools` | 已迁移，低耦合 |
| `mcp/` | `internal/mcp` | 已迁移，自洽 |
| `pkg/logger/` | `internal/pkg/logger` | 已迁移，低耦合 |
| `workspace/` | `internal/runtime/workspace` | 已迁移，仍需关注与 agent 的边界 |
| `chatcore/` | `internal/runtime/chatcore` | 已迁移 |
| `llm/` | `internal/runtime/llm` | 已完成第一轮解耦，仍需注意 balancer 相关抽象 |
| `skill/` | `internal/runtime/skill` | 已迁移 |
| `agent/` | `internal/runtime/agent` | 已迁移 |
| `team/` | `internal/team` | 已迁移 |
| `toolkit/` | `internal/toolkit` | 已迁移，但仍是后端复杂度热点 |
| `chat/` | `internal/runtime/chat` | 已迁移 |
| `agentconfig/` | 从 `internal/config` 裁剪 | 已落地，承担解耦后的配置子集 |

### 3.2 保留在 ai-gateway 的内容

以下包仍然属于网关侧或完整基础设施环境，不适合作为本轮 runtime extraction 的直接迁移目标：

| 包 | 保留原因 |
| --- | --- |
| `internal/gateway/loadbalancer` | 网关核心能力，runtime 侧只保留抽象接口 |
| `internal/config` 全量 | 网关全局配置过大，已裁剪出 `agentconfig` 子集 |
| `internal/runtime/e2e` | 依赖完整网关环境 |
| `internal/model` | 网关持久层 ORM 模型未原样迁入；当前 runtime 仅保留轻量 `backend/internal/model/entity` 等运行期所需结构 |
| `internal/repository` | 数据访问层，属于网关持久层 |
| `internal/session` | 网关 DB-backed session 未原样迁入；当前 runtime 使用自己的 `backend/internal/chat` session/runtime 存储 |
| `internal/taskqueue` | 依赖网关基础设施 |

### 3.3 当前仍成立的解耦原则

以下原则在当前代码中仍然有效：

1. `agentconfig` 继续作为从网关配置裁剪出的 runtime 配置子集。
2. LLM、bootstrap、toolkit 等模块继续通过接口化方式隔离网关特定实现。
3. 前端直接面向 `ai-agent-runtime` 自身的 API 契约建模，不再试图兼容 Deer Flow 或 ai-gateway 的历史前端协议。

---

## 4. 前端迁移现状

前端部分已经和本文件早期版本中的“预留一个 `frontend/src` 空目录”完全不同。当前前端是一个已实际运行的工作台。

### 4.1 已落地的前端基础设施

已完成：

- `Vite + React + TypeScript`
- Tailwind 接入
- `/api` 与 `/healthz` 开发代理
- Landing 页
- Workspace 工作台壳
- 生产构建链路

### 4.2 已接入的运行时能力

当前前端已经真实对接以下后端接口族：

- `POST /api/agent/chat` SSE
- `GET /api/runtime/sessions/{id}/history`
- `GET /api/runtime/sessions/{id}/runtime/stream`
- `GET /api/runtime/teams`
- `GET /api/runtime/teams/summary`
- `GET /api/runtime/teams/{id}/summary/final`
- `GET /api/runtime/teams/{id}/teammates`
- `GET /api/runtime/teams/{id}/tasks`
- `GET /api/runtime/teams/{id}/tasks/graph`
- `GET /api/runtime/teams/{id}/events`
- `GET /api/runtime/teams/{id}/mailbox`
- `POST /api/runtime/teams/{id}/mailbox`
- `POST /api/runtime/teams/{id}/mailbox/{message_id}/ack`
- `GET /api/runtime/teams/{id}/path-claims`
- `POST /api/runtime/teams/{id}/path-claims/check`

### 4.3 已形成的前端工作台能力

当前前端已具备：

- 聊天 SSE 消费与消息增量更新
- session history 同步
- runtime event stream 消费
- planning / orchestration / tool / route / observation / subagent 事件沉淀为制品
- Runtime Teams 面板
- mailbox 发送与 ack
- path claims conflict check
- 多 team fan-out 派发
- runnable team provision
- `Review / Implement / Verify` 与 `Mirror Same Task` 两种 fan-out 模式
- dispatch monitor 自动轮询与状态汇总

### 4.4 当前前端仍缺的能力

尚未完成或仍明显偏弱的部分包括：

- 线程级路由与更清晰的 session 恢复流程
- 真实 artifact 文件拉取与预览
- 多 team 结果聚合、outcome 汇总、summary 对比
- 自动化测试
- `runtime-teams.tsx` 与 `workspace-page.tsx` 的进一步拆分

这意味着前端迁移已经越过“空壳阶段”，但仍处于“功能到位、工程收敛未完成”的阶段。

---

## 5. 原始迁移 Phase 与当前状态

本节保留原始迁移计划的阶段概念，但明确标注它们在当前仓库中的真实状态。

| Phase | 原始目标 | 当前状态 | 说明 |
| --- | --- | --- | --- |
| Phase 1 | 建立目录骨架 | 已完成，且早已超出 | 当前仓库已不是初始骨架，前端和后端均已实装 |
| Phase 2 | 复制零耦合包 | 已完成首轮 | 大部分低耦合 runtime 包已独立到 `backend/internal/` |
| Phase 3 | 批量替换 import 路径 | 已完成首轮 | 当前后端已可独立构建；后续主要是局部清理而非大规模替换 |
| Phase 4 | 创建 `AgentConfig` 解耦 toolkit | 已完成 | 当前以 `agentconfig` 子集承接该职责 |
| Phase 5 | 创建 balancer 接口解耦 llm | 已完成首轮 | 设计目标已落地到接口化解耦，但具体接口名与早期文档表述可能略有调整 |
| Phase 6 | 迁移 skill、agent、team、chat | 已完成首轮 | 当前这些核心 runtime 模块已存在于独立仓库 |
| Phase 7 | 迁移 aicli | 已完成 | 已在 2026-03-30 完成并记录 |
| Phase 8 | 更新 ai-gateway 引用 | 部分完成 | replace / 集成路径具备，但不是本轮前端文档更新重点 |

结论是：

- 原始 Phase 1 到 Phase 7 已不应继续被描述成“待执行”。
- 当前真正进行中的阶段，是“工程收敛、边界清理、测试补齐和控制面拆分”。

---

## 6. 当前风险与剩余工作

### 6.1 前端工程风险

当前前端的主要风险不是“功能没做”，而是：

- `runtime-teams.tsx` 仍然过大
- `workspace-page.tsx` 仍然过大
- 自动化测试仍缺失
- 多 team 结果聚合尚未完成

### 6.2 后端边界风险

后端当前仍需持续关注：

- `workspace ↔ agent` 的共享边界是否继续稳定
- `toolkit`、`llm`、`bootstrap` 是否继续收敛在接口边界内
- 某些历史设计名词与当前实际接口命名之间是否存在文档漂移

### 6.3 文档风险

如果不及时更新文档，最容易出现三类误判：

1. 把前端误判成仍是空骨架。
2. 把原始 Phase 误判成仍待执行。
3. 把当前重点误判成“继续复制更多页面”，而不是“收紧当前控制面与测试边界”。

---

## 7. 当前验收与验证状态

### 7.1 本次已重新核实的项目状态

本次文档更新过程中，已重新核实：

- `frontend` 当前目录结构
- `frontend/src/api/runtime/*` 模块化状态
- `frontend` 对 runtime teams / mailbox / path claims / SSE 的接入状态
- `pnpm lint`
- `pnpm build`

当前前端验证结果：

- `pnpm lint`：通过
- `pnpm build`：通过

### 7.2 历史迁移验收项

以下项属于此前已经记录的后端迁移里程碑，本次没有重新全量复跑，但仍可作为历史完成状态保留：

- `backend/` 下 `go build ./...` 无错误
- `backend/internal/` 已摆脱对原网关若干核心路径的直接依赖
- `backend/cmd/aicli/` 已迁入独立仓库

需要注意：

- 本次未重新执行 `backend/` 的全量 `go test ./...`
- 本次未重新验证 `ai-gateway` 侧的集成构建

因此，本文对后端 Phase 的判断以当前代码结构和历史迁移记录为依据，而不是一次新的全量回归验证。

---

## 8. 下一步建议

当前最合理的后续顺序是：

1. 按 [docs/roadmap.md](E:/projects/ai/ai-agent-runtime/docs/roadmap.md) 继续拆分 `runtime-teams.tsx` 和 `workspace-page.tsx`。
2. 为 SSE、消息合并和 dispatch monitor 增加最小自动化测试。
3. 补齐多 team 结果聚合、outcome 汇总和真实 artifact 拉取。
4. 在上述工作稳定后，再评估是否继续选择性吸收 Deer Flow 的高级浏览器组件。

不建议再回到“先搭空目录、以后再接后端”的思路，因为当前项目已经明显越过这个阶段。
