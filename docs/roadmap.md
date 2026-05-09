# 前端迁移进度与路线图

更新时间：2026-05-09

本文用于记录 `ai-agent-runtime` 当前前端相对 `E:\projects\ai\deer-flow\frontend` 的迁移现状、能力边界、工程风险和后续路线。本文的目标不是推动“原样照搬 Deer Flow 前端”，而是明确：

- 哪些内容已经稳定迁入
- 哪些内容是围绕本项目 runtime API 新长出来的
- 哪些内容仍应延后
- 当前最值得投入的工程收口点是什么

2026-05-09 同步补充：

- 当前前端已经不再只有 `/` 和 `/workspace` 两个入口；`App.tsx` 已注册 `/logs`、`/runtime/config`、`/workspace/chats/:threadId`、`/workspace/sessions/:sessionId`、`/workspace/restore/:sessionId` 等路由。
- `src/api/runtime/*` 已覆盖 agent chat、SSE、sessions/checkpoints、teams、logs、models、runtime config/service control。
- `RuntimeTeams` 已拆出 `frontend/src/components/workspace/runtime-teams/*` 子模块，最大热点从单一 `runtime-teams.tsx` 转移到 settings/config editor 与 team dispatch/detail 子域。
- 前端测试已经落地，不再是“没有测试文件”的状态；当前 `frontend/src` 下有 52 个 `*.test.ts(x)` 文件。

## 1. 结论摘要

当前前端迁移的性质，不是全量复制，而是一条以 `Vite + React + TypeScript` 为目标架构的裁剪式重构路线。到 2026-03-31 为止，这条路线已经明显超过“可运行 UI 壳”阶段，进入了“真实 runtime 控制台 + team orchestration 控制面”阶段。

建议用两套口径看待当前进度：

| 评估口径 | 当前估算进度 | 说明 |
| --- | --- | --- |
| 相对 Deer Flow 全量前端功能 | 约 35% | 已吸收部分视觉体系、工作台布局、聊天和编排控制面，但仍未覆盖 Deer Flow 的完整页面体系、`core` 域层、认证、设置和大量高级浏览器能力 |
| 相对本项目既定迁移策略 | 约 78% | `Vite` 工程、Landing、Workspace、聊天 SSE、session runtime、Runtime Teams、多 team fan-out 和 dispatch monitor 已落地，当前主要缺工程收敛、结果聚合和部分工作台能力 |

一句话总结：

当前仓库中的前端已经完成第一阶段迁移，第二阶段中的 runtime team orchestration 能力也已经提前落地；现在最缺的不是“继续多搬 Deer Flow 页面”，而是“把现有 runtime 控制面拆稳、测稳，并补齐结果聚合能力”。

## 2. 本次更新范围与方法

本次文档更新基于以下核查动作：

1. 对比 `ai-agent-runtime/frontend` 与 `deer-flow/frontend` 的目录规模、源码体量和依赖规模。
2. 复核当前前端是否真实对接本项目后端，而不是停留在静态 mock。
3. 核对 `RuntimeTeams`、`workspace-page` 和 runtime API 层的实际实现范围。
4. 重新执行前端构建与静态检查，确认当前质量状态。

本次同步的是“代码现状”，不是旧规划的重复整理。

## 3. 量化对比

以下统计用于说明迁移覆盖范围，不直接代表质量高低：

| 指标 | Deer Flow 来源前端 | 当前前端 | 说明 |
| --- | --- | --- | --- |
| `src` 源码文件数 | 232 | 215 | 当前前端体量已经接近来源项目，但结构和协议仍以本项目 runtime 为准 |
| `src` TS/TSX 文件数 | - | 213 | 当前 `frontend/src` 主要由 TypeScript / React 文件组成 |
| `src` TS/TSX 代码总行数 | 22201 | 41727 | 当前规模已超过来源前端统计值，主要增长来自 runtime config/settings、team orchestration、tests 和 i18n |
| 前端测试文件数 | - | 52 | 当前已经具备多处单元/组件测试，不再是无测试状态 |
| 非生成文件总数 | 312 | 215+ | 已排除 `node_modules`、`dist`、`.next`、`coverage` 等生成目录；当前统计按 `frontend/src` 口径 |
| 相同相对路径文件数 | 13 | 13 | 说明直接路径复用非常有限 |
| 相同相对路径代码文件数 | 10 | 10 | 存在部分同名文件，但多数已经重写或改造成 Vite 结构 |
| 运行时依赖数 | 70 | 13 | 当前前端显著裁剪，没有引入 Deer Flow 大量运行时依赖；新增依赖主要服务 i18n、routing、markdown、icons 和样式合并 |
| 开发依赖数 | 15 | 17 | 开发工具链接近，但目标平台不同 |

这组数据说明两个关键事实：

1. 当前迁移路线仍然不是“全量复制 Deer Flow”。
2. 当前前端虽然体量小很多，但已经承载了真实的聊天、session runtime 和 team orchestration 逻辑，复杂度并不低。

## 4. 当前已经完成的部分

### 4.1 Vite 工程与开发代理已经稳定

当前前端已经建立为独立的 `Vite + React` 项目，并通过开发代理直接对接本项目后端接口。已完成内容包括：

- 基础工程结构和 `package.json`
- `Vite` 开发配置
- `/api` 和 `/healthz` 的开发代理
- Tailwind 与 React 插件接入
- 生产构建链路

这部分已经稳定，不需要再回头走 `Next.js` 路线。

### 4.2 页面入口和 Deer Flow 风格外壳已经落地

当前前端已从早期两个入口扩展为多条本地路由：

- `/`
- `/logs`
- `/runtime/config`
- `/workspace/chats/new`
- `/workspace/chats/:threadId`
- `/workspace/sessions/:sessionId`
- `/workspace/restore/:sessionId`

并已经完成以下高复用部分的迁移或重写：

- Landing 页面整体视觉方向
- Hero 区块和特性卡片
- 基础按钮、徽章、代码块等 UI 组件
- Workspace 的侧栏、消息区、制品栏三栏结构
- 全局样式基底

需要强调的是，这些内容不是简单复制。当前实现已经根据 `Vite` 路由、本地状态模型和本项目 runtime API 做了重写。

### 4.3 工作台聊天和 session runtime 已真实接入后端

当前工作台已经完成了核心聊天与会话运行时接线，主要包括：

- `POST /api/agent/chat` 的 SSE 消费
- `GET /api/runtime/sessions/{id}/history`
- `GET /api/runtime/sessions/{id}/runtime/stream`
- SSE 事件转本地消息模型
- 将 planning、orchestration、route、observation、subagent、tool events 沉淀为制品
- 发送消息后进行 authoritative history 同步

这意味着当前前端已经不再是“UI 壳 + mock”，而是围绕本项目后端协议组织真实交互。

### 4.4 Runtime API 层已经完成第一轮模块化

早期的大型 `runtime-api.ts` 已经被拆分为按领域划分的 API 层：

- `src/api/runtime/agent-chat.ts`
- `src/api/runtime/config.ts`
- `src/api/runtime/logs.ts`
- `src/api/runtime/models.ts`
- `src/api/runtime/sessions.ts`
- `src/api/runtime/teams.ts`
- `src/api/runtime/sse.ts`
- `src/api/runtime/shared.ts`

当前 `src/lib/runtime-api.ts` 只保留为兼容导出层，不再承担主要实现细节。这说明 API 模块化这条线已经从“规划项”转为“已完成项”。

### 4.5 Runtime Teams 已形成项目特有的 orchestration 控制面

`RuntimeTeams` 不是 Deer Flow 中原封不动搬来的模块，而是围绕 `ai-agent-runtime` 后端能力扩写出的工作台控制面。当前已经接入：

- 团队列表
- 团队摘要和 final summary
- 队友列表
- 任务列表和任务图
- 团队事件流
- 团队邮箱列表
- 路径占用列表
- 详情加载的部分失败容错

同时，已经具备多项轻交互能力：

- 发送 team mailbox message
- ack mailbox message
- path claims conflict check
- 创建 session
- 创建 team
- 绑定 teammate
- 创建 task

这部分已经明显超出了“展示 UI 壳”的范围，属于本项目前端对 runtime orchestration 的主动封装。

### 4.6 多 Team 执行控制面已经形成闭环

当前前端已经不只是“查看 team 状态”，而是支持真正的多 team fan-out 执行链路。已落地能力包括：

- 向多个 existing executable teams 分发同一个 next task
- 在分发前做 executability 预检查，避免把任务发给不可运行 team
- 批量 provision runnable teams 并立刻 dispatch task
- 两种 fan-out 模式：
  - `Review / Implement / Verify`
  - `Mirror Same Task`
- `Dispatch monitor` 自动轮询与状态汇总
- 在监控区展示 task 状态、assignee、最新 team event、mailbox 预览和 summary

这一层能力已经不是 Deer Flow UI 的简单参考，而是本项目 runtime team orchestration 的前端控制面雏形。

## 5. 当前没有迁入，或仍明显缺失的部分

### 5.1 Deer Flow 的页面体系仍未迁入

当前项目已经实现 landing、workspace、logs、runtime config 等 Vite 页面入口，但 Deer Flow 前端中仍有大量页面没有进入当前仓库，例如：

- 聊天线程页面
- agent 页面和新建 agent 页面
- 设置相关页面
- 更细粒度的 workspace 子路由

这意味着当前项目还没有覆盖 Deer Flow 的完整页面导航结构。

### 5.2 来源项目的大量 `core` 能力仍未迁入

Deer Flow 前端中的以下领域层能力没有按来源项目原样迁入：

- `threads`
- `memory`
- `uploads`
- 来源项目自己的 `core` 状态模型
- `Next.js` 认证与服务端路由

同时，本项目已经有围绕 runtime API 的 `models`、settings/runtime config editor 和 i18n 框架；这些不是 Deer Flow `core` 的直接迁移。当前差距主要在 Deer Flow 式 thread/core 状态模型、uploads、memory、认证和服务端路由。

### 5.3 当前工作台仍缺若干关键能力

虽然 runtime 控制面已经很深，但当前前端仍有明显缺口：

- 已有 `/workspace/chats/*`、`/workspace/sessions/*`、`/workspace/restore/*` 基础 deep link，但恢复索引、切换语义和错误状态还需要继续收敛
- checkpoint preview / file diff 已有，Deer Flow 式线程文件系统 artifact、通用文件拉取和错误处理仍未完整实现
- 更成熟的消息分组、inline tool steps、workflow 卡片
- 多 team 结果聚合、outcome 汇总和 final summary 对比
- 更系统的空状态、错误状态和回放能力

换句话说，当前已经有“运行中的控制台”，但距离“可长期使用的完整工作台”还差一轮产品层补齐。

### 5.4 目录结构已收敛一部分，但仍处于过渡态

当前实际前端结构已经比早期更接近长期形态：

- `src/api`
- `src/components`
- `src/data`
- `src/lib`
- `src/pages`
- `src/hooks`
- `src/styles`
- `src/types`

这比早期“所有逻辑集中在少数页面和 `lib`”的状态前进了一步，但仍然缺少：

- `src/store`
- 更清晰的页面状态边界

因此，前端已经不是快速搭建初期，但也还没有完成最终结构收敛。

## 6. 对当前迁移策略的判断

当前代码体现出来的迁移策略依然是正确的：

- UI 外观和布局结构尽量复用或参考 Deer Flow
- 页面入口、状态组织和 API 协议以本项目为准
- 后端契约不去伪装成 Deer Flow 的原始模型
- `Next.js`、认证、LangGraph 等强耦合能力不直接引入

这个策略正确的原因在于：

1. 它避免把 Deer Flow 的平台耦合一并复制进来。
2. 它让当前前端从第一天起就围绕本项目后端接口建模。
3. 它允许 UI 和协议分别演进，而不是让后端倒过来兼容来源项目模型。

当前真正的问题，不是迁移方向错误，而是这条路线已经进入第二阶段，工程收尾和结果聚合还没完全跟上。

## 7. 当前质量状态

### 7.1 构建与静态检查

当前前端已通过以下检查：

- `pnpm lint` 通过
- `pnpm build` 通过

这说明：

- 当前 TypeScript 编译可过
- 当前 ESLint 已经从早期失败状态收口到通过
- 当前 Vite 打包链路可用

### 7.2 自动化测试情况

当前前端已经补上测试基线，`frontend/src` 下已有 52 个 `*.test.ts(x)` 文件，覆盖范围包括：

- runtime API shared / SSE helper
- workspace shell/sidebar shared 逻辑
- logs page shared/detail panel
- settings domain editor 工具函数
- landing page 与部分 workspace 富内容组件

仍需继续补强的高风险区域：

- 多 team dispatch monitor 的长轮询/状态收敛
- runtime config editor 的端到端保存/预览/写回流程
- workspace 多 session 恢复与 artifact preview 的组合流程

### 7.3 大文件与模块边界风险

当前最大的几个文件仍然明显偏大，但热点已经从单一 workspace 文件转移到 settings/config 与 team 子域：

- `src/components/workspace/settings/backend-config-settings-page.tsx`：约 3559 行
- `src/components/workspace/settings/runtime-provider-groups-domain-editor.tsx`：约 1138 行
- `src/components/workspace/runtime-teams/runtime-team-dispatch-panel.tsx`：约 816 行
- `src/components/workspace/runtime-teams/runtime-team-details-panel.tsx`：约 784 行
- `src/components/workspace/runtime-teams/use-runtime-team-dispatch.ts`：约 686 行
- `src/components/workspace/runtime-teams.tsx`：约 649 行
- `src/types/runtime.ts`：约 651 行

相比旧状态，`runtime-api.ts` 和 `runtime-teams.tsx` 已经完成拆分，不再是唯一热点；但 config editor 与 team dispatch/detail 子域仍然承担了较多状态机、格式化和 UI 组装逻辑，后续应继续按 domain editor / hook / presentational component 拆分。

## 8. 已知偏差与文档不一致点

截至本次更新，`roadmap.md` 已经同步到最新实现状态，但仓库内仍可能存在以下偏差：

1. 部分历史文档和页面文案仍会低估 `RuntimeTeams` 的接入深度。
2. `MIGRATION.md` 中的目标目录结构与当前实际结构仍有差距。
3. 当前前端已经具备 team orchestration 控制面，但其他文档可能仍把它描述成“工作台雏形”或“UI 壳”。

这些偏差不会阻塞开发，但会影响团队对当前状态的判断。

## 9. 建议的路线图

后续工作建议按四个阶段推进，而不是回到“先搬更多 Deer Flow 页面”的节奏。

### Phase 1：基础迁移与第一轮接线

当前状态：已完成

已完成内容：

- `Vite + React + TypeScript` 工程搭建
- Landing 与 Workspace 外壳
- `/api/agent/chat` SSE
- session history/runtime stream
- Runtime API 第一轮模块化
- Runtime Teams 基础控制面
- lint/build 收口

### Phase 2：工程收敛与控制面拆分

当前状态：进行中

目标：

- 把已经落地的 runtime 控制面变成可持续维护的结构

建议动作：

1. 继续拆分 `runtime-teams.tsx` 及 `runtime-teams/*` 子域，重点收敛 dispatch/detail/monitor 的状态边界。
2. 继续保持 `workspace-page.tsx` 的装配层定位；当前页面约 138 行，后续应把剩余 chat submit 和 artifact 聚合逻辑下沉到 hooks/domain helpers。
3. 继续收紧 `src/types/runtime.ts`，把 API 返回类型、UI 展示类型和本地工作台模型边界拆清。
4. 为 SSE 解析、消息合并、多 team dispatch monitor 补最小测试。

验收标准：

- 大文件继续收缩，而不是继续膨胀
- 核心 runtime 行为有最小自动化测试
- 页面层主要负责装配，不再承载大段状态机逻辑

### Phase 3：补齐工作台 MVP 缺口

当前状态：部分已开始

目标：

- 从“可运行控制台”进入“可持续使用的前端工作台”

建议动作：

1. 收敛现有 `/workspace/chats/*`、`/workspace/sessions/*`、`/workspace/restore/*` 的 session 创建、恢复、切换流程。
2. 在已有 checkpoint preview / file diff 基础上，为 artifact 面板补 Deer Flow 式线程文件系统 artifact、通用文件拉取和错误处理。
3. 为多 team fan-out 增加结果聚合、outcome 汇总和 summary 对比视图。
4. 提升消息区的 workflow 表达，补 inline tool steps、消息分组和更明确的运行轨迹展示。

验收标准：

- 多 session、多 team 工作流可以稳定复用
- checkpoint preview / file diff 可用，线程文件系统 artifact 不再只依赖前端拼装字符串
- 多 team 执行结果可以在前端完成收敛和比对

### Phase 4：选择性吸收 Deer Flow 的高级前端能力

当前状态：待评估

目标：

- 只迁真正高价值、低平台耦合的部分

建议动作：

1. 评估命令面板、消息富渲染、agent 卡片、设置页等模块是否值得迁入。
2. 对每个候选模块做“浏览器复用价值”和“后端耦合风险”评估。
3. 不直接迁入 `Next.js auth`、server routes、来源项目专属 `core` 状态模型。
4. 对确实要复用的模块，继续采用“先 UI，后协议”的迁法。

验收标准：

- 新增模块符合本项目后端协议，而不是牵引后端去兼容 Deer Flow
- 不引入 Next 平台依赖
- 视觉复用与协议适配仍保持解耦

## 10. 不建议执行的路线

以下路线仍然不建议采用：

1. 直接把 Deer Flow 的 `src/app`、`src/core`、`src/server` 整体复制进当前仓库。
2. 为了复用来源前端，强行让本项目后端去模拟 Deer Flow 的前端协议。
3. 在 settings/config editor、runtime-teams dispatch/detail 等当前热点文件上继续串行堆功能，而不先拆结构。
4. 在缺少最小测试的情况下继续快速扩展 team orchestration 交互。

这些做法都会让迁移从“有边界的裁剪重构”退化为“高耦合搬运”。

## 11. 推荐的下一步执行顺序

建议按下面顺序推进：

1. 先继续拆 settings/config editor 与 runtime-teams dispatch/detail 子域，同时保持 `workspace-page.tsx` 的装配层定位。
2. 再补多 team 结果聚合、outcome 汇总和真实 artifact 拉取。
3. 然后继续收敛现有 workspace deep link 与更完整的 session 恢复流程。
4. 最后再选择性吸收 Deer Flow 中成熟但低耦合的高级组件。

如果必须在“继续扩功能”和“先收工程质量”之间二选一，当前阶段应优先收工程质量和结果聚合，因为现有控制面已经足够深，再继续堆功能而不拆边界，后续维护成本会明显上升。

## 12. 当前状态判定

截至 2026-03-31，可以将当前前端状态定义为：

`已完成第一阶段迁移，第二阶段的 runtime team orchestration 能力已经提前落地，当前进入工程收敛与结果聚合阶段。`

更具体一点：

- 它已经不是 demo 壳。
- 它已经接上了本项目后端的关键 runtime 接口。
- 它已经形成了聊天、session runtime、team orchestration 并存的 Vite 工作台。
- 它还没有覆盖 Deer Flow 的完整前端能力。
- 它当前最缺的不是“继续多搬页面”，而是“拆稳现有控制面，并把多 team 执行结果收回来”。

## 13. 当前执行拆分建议

为了让后续工作可并行推进，建议按稳定边界拆成多个 workstream，而不是继续在少数大文件上串行堆功能。

页面与 workspace 的结构分析、team 成员分工和迁移边界，见 [docs/workspace_migration_team_plan.md](E:/projects/ai/ai-agent-runtime/docs/workspace_migration_team_plan.md)。

| Workstream | 当前状态 | 目标 | 主要写入范围 |
| --- | --- | --- | --- |
| WS-A 基础质量收口 | 已完成首轮 | 保持 `lint/build` 持续通过，避免基础组件回退 | `src/components/ui/*`、`src/components/landing/*`、局部 workspace 组件 |
| WS-B Runtime API 模块化 | 已完成首轮 | 继续收紧类型边界和兼容出口 | `src/api/runtime/*`、`src/lib/runtime-api.ts`、`src/types/*` |
| WS-C Workspace 页状态拆分 | 进行中 | 保持 `workspace-page.tsx` 装配层定位，继续下沉 chat/artifact 逻辑 | `src/pages/workspace-page.tsx`、`src/hooks/workspace/*` |
| WS-D Runtime Teams 拆分 | 进行中 | 拆 `runtime-teams.tsx` 的概览、邮箱、路径占用、派发、监控等子域 | `src/components/workspace/runtime-teams.tsx`、后续 `src/components/workspace/runtime-teams/*` |
| WS-E 最小测试补齐 | 已有基线 | 继续为 SSE、消息合并、dispatch monitor 补高风险自动化测试 | `src/**/*.test.*` |
| WS-F 结果聚合与 artifact 补齐 | 待开始 | 收敛多 team 执行结果，并补真实 artifact 拉取/预览 | `src/components/workspace/*`、`src/pages/*`、`src/api/runtime/*` |

建议的边界约束：

1. WS-C 优先处理页面状态机，不主动改 `RuntimeTeams` 内部实现。
2. WS-D 优先处理团队控制面拆分，不主动改主页面状态机。
3. WS-B 继续保留兼容导出，避免影响其他工作流。
4. WS-E 只覆盖高风险逻辑，不做大而全测试。
5. 所有 workstream 合并前都至少验证 `pnpm lint` 和 `pnpm build`。

### 13.1 本轮执行状态

截至本轮执行完成，当前状态可记为：

| Workstream | 当前状态 | 本轮结果 |
| --- | --- | --- |
| WS-A 基础质量收口 | 已完成 | 已修复现有 lint 问题，基础组件行为保持稳定 |
| WS-B Runtime API 模块化 | 已完成 | 已落地 `src/api/runtime/*` 和 `src/types/runtime.ts`，旧入口保留兼容 |
| WS-C Workspace 页状态拆分 | 第一轮完成 | 已拆出 runtime teams 加载、session history 同步、runtime stream 订阅 3 个 hooks，聊天提交流和 artifact 逻辑仍待继续下沉 |
| WS-D Runtime Teams 拆分 | 第一轮完成 | 已抽出共享类型、排序、格式化和 dispatch 模板逻辑到 `runtime-teams/shared.ts`，UI 细粒度组件拆分仍待继续 |
| WS-E 最小测试补齐 | 已有基线 | 当前已有 52 个 `*.test.ts(x)`，仍需补高风险流程测试 |
| WS-F 结果聚合与 artifact 补齐 | 未开始 | 当前仍缺多 team 执行结果聚合和真实 artifact 拉取 |

本轮统一验证结果：

1. `frontend/pnpm lint` 通过
2. `frontend/pnpm build` 通过

这意味着当前可以继续进入下一轮拆分，而不是先回头做救火式修复。

## 14. 页面与 Workspace 结构分析

本节聚焦两个问题：

1. Deer Flow 原前端的 `page / workspace` 结构实际上是如何组织的。
2. 当前 `ai-agent-runtime` 前端已经复用了什么，还缺什么。

### 14.1 来源项目的页面结构

Deer Flow 的 landing 入口非常直接：

- [src/app/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/page.tsx)
  - `Header`
  - `Hero`
  - `CaseStudySection`
  - `SkillsSection`
  - `SandboxSection`
  - `WhatsNewSection`
  - `CommunitySection`
  - `Footer`

这说明来源 landing 的结构不是“一个大 Hero + 简单介绍”，而是明显的营销站式多 section 页面。它的布局骨架是：

- 固定头部
- 全屏 Hero
- 多段内容 section 顺序展开
- 页脚收束

### 14.2 来源项目的 workspace 结构

Deer Flow 的 workspace 不是单页组件，而是一个分层清晰的路由布局系统：

- [src/app/workspace/layout.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/layout.tsx)
  - `QueryClientProvider`
  - `SidebarProvider`
  - `WorkspaceSidebar`
  - `SidebarInset`
  - `CommandPalette`
  - `Toaster`

其工作方式是：

1. `workspace/layout.tsx` 提供全局 shell。
2. 具体页面通过 `SidebarInset` 注入内容。
3. 内容页再使用 `WorkspaceContainer / WorkspaceHeader / WorkspaceBody` 形成统一页面骨架。

核心 workspace 路由层级包括：

- [src/app/workspace/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/page.tsx)
- [src/app/workspace/chats/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/chats/page.tsx)
- [src/app/workspace/chats/[thread_id]/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/chats/[thread_id]/page.tsx)
- [src/app/workspace/agents/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/agents/page.tsx)
- [src/app/workspace/agents/new/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/agents/new/page.tsx)
- [src/app/workspace/agents/[agent_name]/chats/[thread_id]/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/agents/[agent_name]/chats/[thread_id]/page.tsx)

从布局关系上看，来源项目的 workspace 可以概括为：

- 第一层：全局 shell
  - sidebar
  - inset 内容区
  - command palette
  - toaster
- 第二层：页面容器
  - breadcrumb/header
  - body
- 第三层：具体业务面板
  - chats
  - agents
  - messages
  - artifacts
  - settings

### 14.3 来源项目的聊天与 artifact 布局骨架

来源项目中，聊天工作台并不是简单三栏，而是“sidebar + 内容区内聊天/制品双面板”的结构：

- [src/components/workspace/chats/chat-box.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/chats/chat-box.tsx)
  - `ResizablePanelGroup`
  - `chat panel`
  - `artifact panel`

更具体地说：

- sidebar 是全局导航层
- chat 与 artifacts 的关系是在内容区内部通过 resizable panel 组织
- artifact panel 默认可开合，并不是永远占据右侧固定列

消息区本身也不是纯文本列表，而是富状态渲染：

- [src/components/workspace/messages/message-list.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/messages/message-list.tsx)
  - conversation 容器
  - message group
  - markdown content
  - subtask card
  - artifact file list
  - streaming indicator

artifact 详情区也具备独立的 header、view mode、code/preview 切换和下载/复制等动作：

- [src/components/workspace/artifacts/artifact-file-detail.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/artifacts/artifact-file-detail.tsx)

### 14.4 当前项目的页面与 workspace 结构

当前 `ai-agent-runtime` 前端页面入口仍比 Deer Flow 扁平，但已从单页 workspace 扩展出基础路由：

- [frontend/src/App.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/App.tsx)
  - `/`
  - `/logs`
  - `/runtime/config`
  - `/workspace/chats/new`
  - `/workspace/chats/:threadId`
  - `/workspace/sessions/:sessionId`
  - `/workspace/restore/:sessionId`

landing 页面当前是简化版：

- [frontend/src/pages/landing-page.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/pages/landing-page.tsx)
  - `LandingHeader`
  - `Hero`
  - lazy `LandingDeferredSections`

`LandingDeferredSections` 中再延迟加载 case study、skills、what's new、community、footer 等后续 section。

workspace 当前则是单页控制台：

- [frontend/src/pages/workspace-page.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/pages/workspace-page.tsx)
  - 约 138 行，主要负责 hooks 装配
  - 线程选择、session history、runtime stream、runtime teams 数据加载已下沉到 `src/hooks/workspace/*`
  - 页面层把状态统一传给 `WorkspaceShell`

`WorkspaceShell` 的布局是显式三栏：

- [frontend/src/components/workspace/workspace-shell.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/components/workspace/workspace-shell.tsx)
  - 顶部 sticky header
  - 左栏 `WorkspaceSidebar`
  - 中栏 `MessageList + MessageComposer`
  - 右栏 `ArtifactPanel`

左栏内部除了线程列表，还展示 runtime teams summary，并通过 lazy `RuntimeTeamsDialog` 打开完整 team 控制面：

- [frontend/src/components/workspace/workspace-sidebar.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/components/workspace/workspace-sidebar.tsx)

这意味着当前项目已经避免把完整 `RuntimeTeams` 控制面直接塞进 sidebar；左栏仍承担一部分 runtime summary / launch surface，但完整控制面在 dialog 中呈现。

### 14.5 已复用的结构与视觉模式

当前前端已经明确复用了 Deer Flow 的这些模式：

- landing 页的深色视觉方向、hero 导向、品牌化标题和 section 化说明
- workspace 的“左侧导航 / 中间消息 / 右侧制品”认知模型
- 若干基础 UI 组件与视觉 token 风格
- 面向 agent work 的消息区、制品区、代码展示区的基本交互预期

这些复用主要集中在“浏览器层壳子”，也就是：

- 布局心智模型
- 视觉气质
- 通用 UI primitive
- agent/chat/artifact 的展示模式

### 14.6 已明显偏向 ai-agent-runtime 自身需求的部分

当前前端也已经明显偏离 Deer Flow 原实现，并开始围绕 `ai-agent-runtime` 自己建模：

- 页面入口仍比 Deer Flow 少，但已围绕 logs、runtime config、workspace chats/sessions/restore 建立本项目自己的路由集合
- `WorkspaceSidebar` 承载 runtime teams summary 和 dialog launch surface，而不是只做导航
- `WorkspacePage` 直接处理 `/api/agent/chat`、session history、runtime event stream
- artifact 已有 checkpoint preview / file diff 等 runtime-backed 能力，但还不是 Deer Flow 那种完整线程文件系统视角
- 线程模型当前是本地 UI 线程 + runtime session 的混合模型，而不是 Deer Flow 的 `core/threads` 体系

所以当前前端的本质不是“简化版 Deer Flow”，而是“借了 Deer Flow 的浏览器壳，但已经在 runtime 协议上自成一套”。

### 14.7 当前最大的结构缺口

当前最大的结构缺口不在视觉，而在布局分层：

1. 当前缺少 Deer Flow 那种 `workspace shell` 与 `workspace content route` 的两层结构。
2. 当前把“线程导航”和“team orchestration 控制面”揉在了同一个 sidebar 里，左栏职责过重。
3. 当前 artifact panel 是固定第三列，而不是内容区内部可开合的附属面板，灵活性不如来源实现。
4. 当前 message 区仍是简化模型，离来源项目的富消息分组、subtask、artifact 伴随展示还有明显差距。
5. 当前已有基础线程/session/restore deep link，但恢复索引、切换语义和布局层级还需要继续收敛，workspace 仍更像本项目 runtime 控制台，而不是 Deer Flow 那种完整工作区。

可以把这个缺口总结为一句话：

当前已经有了 Deer Flow 的“外形”，但还没有形成 Deer Flow 那种“布局层级”。

### 14.8 按 team 成员并行推进的建议

后续不建议按“大文件分别拆”来分工，而应按布局边界分工。推荐至少分成 5 个成员：

| 成员 | 负责边界 | 目标 |
| --- | --- | --- |
| Layout Shell Owner | workspace 全局壳与路由骨架 | 引入 `workspace shell + content area` 两层结构，为后续线程页、agent 页预留位置 |
| Chat Surface Owner | message list、composer、streaming、thread 切换 | 把聊天主面板从页面状态机里继续拆出，并向更丰富的消息呈现靠拢 |
| Artifact Surface Owner | artifact 列表、详情、预览、开合逻辑 | 把右侧固定列逐步演进成更接近 Deer Flow 的可控 artifact surface |
| Team Console Owner | runtime teams、mailbox、path claims、dispatch、monitor | 保持 team 控制面围绕 runtime 能力演进，但避免继续侵入主 sidebar 边界 |
| Route and Session Owner | 线程级路由、session 恢复、页面入口组织 | 继续收敛 `/workspace/chats/*`、`/workspace/sessions/*`、`/workspace/restore/*` 的恢复与切换语义 |

推荐的执行顺序：

1. 先做 `Layout Shell Owner + Route and Session Owner`
   - 原因：先把布局骨架立起来，其他面板才有稳定挂点。
2. 再并行推进 `Chat Surface Owner + Artifact Surface Owner`
   - 原因：这两块在内容区内部天然成对出现。
3. `Team Console Owner` 独立推进
   - 原因：它和 Deer Flow 的原结构差异最大，更适合围绕本项目需求独立演化。

如果只开 3 个成员，则建议压缩为：

- 成员 A：Layout + Route
- 成员 B：Chat + Artifact
- 成员 C：Team Console

这会比“一个人拆 workspace-page，一个人拆 runtime-teams”更接近真实的布局边界，也更不容易反复打架。
