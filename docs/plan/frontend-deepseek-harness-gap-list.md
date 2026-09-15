# frontend 计划缺口清单（2026-09-13 复核）

> 对象计划：`docs/plan/frontend-deepseek-harness-optimization-plan.md`
> 复核基线：HEAD `41471f24`（复核时前端工作区无未提交改动）
> 复核方法：计划文本逐条对拍 + 12 个声称 commit 的 `git show --stat` 校验 + 静态代码取证（grep/view）+ 四道门禁本地重跑
> 关联章节：计划 §9.4「2026-09-13 复核与缺口登记」

## 0. 门禁实测（复核当天）

| 门禁 | 结果 |
|---|---|
| `npm run build` | exit 0（无 INEFFECTIVE_DYNAMIC_IMPORT 警告） |
| `npm run test` | exit 0，121 文件 / 758 用例通过 |
| `npm run test:e2e` | exit 0，35 通过（7 个 spec，约 1.4 min） |
| `npm run lint:i18n` | scanned=540 / violations=0（§9.3 旧记录 534） |
| `npm run verify:clean`（本次新增） | 扫描 751 文件，0 处 `.bak` / `.backups` 残留 |

## 1. 缺口总表（20 项）

| ID | 缺口 | 类别 | 严重度 | 处置状态 |
|---|---|---|---|---|
| A1 | P0-1 验收回退：`src` 下 `.bak`/`.backups` 复活 | 验收回退 | 高 | **已处置**（清理 + 门禁，批次 1） |
| A2 | P0-2 全局指标失效：> 500 行文件曾为 3 个 | 验收回退 | 高 | **已处置（批次 2 + 批次 8）**：批次 2 修复至 0；2026-09-13 P2-1A 交付后复检回退 2 个（`quota.tsx`=559、`workspace-page.tsx`=512），批次 8 二次拆分归零并新增 `scripts/verify-max-lines.mjs` 门禁（见 §3 复检记录） |
| A3 | P0-4 数值口径偏差（89 vs 87、22 vs 20） | 验收漂移 | 低 | **已登记**（复检备注） |
| B1 | P1-5 队列与 Steering 零实施 | 零交付 | 高 | 已登记（§6.2 状态行），**待排期** |
| B2 | P1-8 连接状态统一零新增交付 | 零交付 | 高 | **已修复**（批次 2 第二项，2026-09-13 合入主树，见 §3） |
| B3 | P1-9 会话列表状态与整理零实施 | 零交付 | 高 | **已修复**（批次 3，2026-09-13 合入主树，见 §3） |
| B4 | P1-10 全局错误边界与加载失败面零实施 | 零交付 | 高 | **已修复**（批次 2 首项，2026-09-13 合入主树，见 §3） |
| B5 | P2 未启动 9 项（P2-2/3/4/5/6/8/9/10/11） | 零交付（P2-1A 已有 8 项子能力落地） | 中 | 已登记（§6.3 说明行），**待排期** |
| C1 | P1-4 `@` 引用仅 file 组（session/subagent 延后） | 部分交付 | 低 | 计划已披露，保持 |
| C2 | P2-1 后端能力接线：9 行未接、8 行已闭环（Jobs / 审批闭环 / usage / 会话搜索 / fs/read-file / skills 市场与热重载 / 子代理控制面 / 会话统计） | 部分交付 | 中 | 已登记，Jobs / 审批闭环 / usage / 会话搜索 / fs/read-file / skills 市场与热重载 / 子代理控制面 / 会话统计已关闭，**待排期**（余 9 行） |
| C3 | P2-7 命令系统无内置执行器 | 已交付（四个子片全落地，批次 12–14） | 中 | 已登记；**内置命令清单与执行器（`/export` / `/rename`）已交付并回填（批次 12）；`/model` 目录弹窗与 `/feedback` log-only 入口已交付并回填（批次 13）**，命令专属 popupSelect 候选面（弹层自持焦点 / 本地检索 / 虚拟高亮）已交付并回填（批次 14）——P2-7 四个子片全部落地 |
| D1 | §9.3 缺 P1-5/8/9/10 状态行 | 台账 | 中 | **已处置**（§6.2 增补，批次 1） |
| D2 | §6.3 无 P2 状态登记机制 | 台账 | 中 | **部分处置**（加说明行；P2 启动时建登记） |
| D3 | P0-1/P0-2/P0-4 回退与漂移未记录 | 台账 | 中 | **已处置**（复检备注 + §9.4，批次 1） |
| D4 | §5.5 A2 行号引用在 logs 页拆分后失效 | 文档 | 低 | **已处置**（引用更新，批次 1） |
| D5 | 跨计划「P1-5」编号冲突（grep 误导） | 文档 | 中 | **已处置**（状态行加注 + §9.4，批次 1） |
| D6 | 头部仍标「草案（待评审）」、i18n 计数 534 失真 | 文档 | 低 | **已处置**（头部更新 + §9.4，批次 1） |
| E1 | `.bak`/`.backups` 无门禁看护 | 工程 | 高 | **已处置**（`verify-no-backups` 并入 lint，批次 1） |
| E2 | i18n namespace 动态导入「未分块」告警 | 工程 | 低 | **已澄清关闭**（按设计：首屏同步注入，动态入口用于切换/审计，不追求分块；见 §2-E） |
| E3 | 无视觉回归 / 性能基准设施 | 工程 | 中 | 待排期（随 P2-4/P2-5） |

## 2. 缺口明细与证据

### A. 验收回退类（"已完成"但当前不成立）

| ID | 缺口与证据 | 处置 |
|---|---|---|
| A1 | 计划行 359/361 验收「`frontend/src` 下 `*.bak`=0」。复核实测：`src` 下 52 个 `.bak` + 17 个 `.backups/`，`frontend/` 全域 72 个 `.bak` + 19 个 `.backups/`（全部 2026-09-13 生成，含 `frontend/.backups`、`frontend/.tmp/.backups`）。`.gitignore` 的 `*.bak`/`.backups/` 规则使其对 `git status` 完全隐形，导致回退无法被发现。修复来源：P0-2/P1-x 批量重构持续用 `.backups` 做安全副本。 | **批次 1 已清理**（72 + 19 → 0 / 0）并新增门禁（见 E1）；计划 P0-1 状态行已加复检备注 |
| A2 | 计划行 379/381 验收「> 500 行文件数降至 0」。复核实测 3 个：`styles/globals.css`=1043、`components/workspace/message-markdown-streaming.ts`=525（P1-2 `c12e0cf4` 引入）、`components/workspace/trajectory/subagent-session-dialog.test.tsx`=516（`8c55411d`）。按计划 ts/tsx 口径为 2 个；M4 阈值（≤ 10）仍满足。另 §9.3 有陈旧行数漂移：event-readers 311（记 230）、primitives 224（记 287）、sessions.ts 186（记 141）、history-artifacts 306（记 296）、apply 333（记 330）——仍全部 < 500。 | **批次 2 已修复（2026-09-13）**：① `message-markdown-streaming.ts`(525) → 目录 barrel `message-markdown-streaming/{types 59, blocks 102, fence 93, tails 276, index 19}`（14 个导出逐个对拍，原 .ts 删除）；② `subagent-session-dialog.test.tsx`(516) → 301 行主文件（12 个 it 标题 1:1）+ `subagent-session-dialog.test-helpers.ts`(133) + `subagent-session-target.test.ts`(51) + `subagent-session-dialog-trajectory.test.tsx`(102)；③ `styles/globals.css`(1043) → 入口 17 + `styles/globals/{theme 110, tokens 335, themes 150, base 435}`（切割脚本内置字节级重组校验，保序）。复测：`frontend/src` 内 > 500 行文件 **0 个**；构建产物 CSS 与拆分前同尺寸同 SHA256。**2026-09-13 复检（P2-1A 第八项交付后）回退**：> 500 行文件 2 个——`pages/usage-analytics/quota.tsx`=559、`pages/workspace-page.tsx`=512（随 P2-1A 接线增量引入，非拆分回退）；该口径无门禁看护（`scripts/` 仅 i18n 与 no-backups 两个脚本），所以未被拦截；处置已定（批次 8，2026-09-13）：`quota.tsx` 559 → 429 + `quota-shared.ts` 74 + `quota-atoms.tsx` 73；`workspace-page.tsx` 512 → 437 + `hooks/workspace/use-workspace-session-actions.ts` 133（重命名 / 归档 / 归档恢复 / Fork / 删除 / 目录内新建六个动作收口，仅搬迁不改语义）；新增 `scripts/verify-max-lines.mjs` 门禁（非空行 ≤ 500、i18n 词典整树豁免、501 行探针负路径 exit 1）并入 `npm run lint` 与 `npm run verify:lines`，该口径自此有门禁看护；复测 `frontend/src` 内 > 500 非空行文件 **0 个**（最大 `workspace-sidebar.tsx`=483），门禁四件套 lint 0 error / 3 基线 warning（i18n scanned=605 / violations=0、备份门禁 874 文件 0 残留）、test 156 文件 / 1026 用例、build exit 0、test:e2e 57 passed。 |
| A3 | `@theme` 映射实测 89 条（计划记 87）；残留 `[var(--…)]` 任意值实测 22 行/14 文件（记 20）；`landing.css` 变量声明 0 成立。 | 已登记（P0-4 复检备注） |

### B. 零交付类（有任务定义、无代码）

| ID | 计划位置 | 复核证据 | 处置 |
|---|---|---|---|
| B1 | §6.2 P1-5（行 466-470） | `resolveSubmitMode` / `busyEnter` / `updateQueue` / `steeringAvailable` 在 `frontend/src` 全零命中；"queue/队列" 命中均为无关的 provider 请求队列设置。依赖后端队列 API（未就绪）。 | §6.2 已补「未开始」状态行；待排期 |
| B2 | §6.2 P1-8（行 486-493） | 统一连接呈现件 / 顶栏与消息流尾状态条 / 直连 `/api/agent/chat` 恢复 / 手动重试与 last seq 拉齐——四项新交付零落地；仅有 §5.5 A1/A2 预登记既有资产（`use-session-runtime-stream.ts` 重连、logs 连接标签）。 | **已修复**（2026-09-13）：新增 `lib/connection-status.ts`（统一词汇/配色/文案 + logs 状态映射 + `withTransportDegradation`）、`components/ui/connection-status-badge.tsx`（pill/header 双变体、非在线态手动重试）、`hooks/workspace/use-connection-status-labels.ts`；会话流暴露 `connectionStatus/retryConnection`（复用既有常驻重连，状态按会话键存储，effect 同步段零 setState）；顶栏 + 消息流尾接入；`workspace-page.tsx` 收口直连 chat 断线；日志页头改挂共享呈现件（`variant="header"`，视觉不变） |
| B3 | §6.2 P1-9（行 491-496） | 侧栏行状态指示 / 归档与非破坏删除 / 归档恢复 / Fork / 行内时间浮层 / 空态区分均无；后端 `archive`/`activate`/`close`、`/sessions/stats` 已就绪但前端零消费。 | **已修复**（2026-09-13）：行状态指示（等待类优先级：等待审批 > 计划待审 > 等待回答 > 运行中 > 子代理 > 归档/关闭 > 空闲）、归档 + 归档恢复 + 非破坏删除（仅移除会话引用，删除当前会话时回落工作台首页）、Fork（后端无克隆 API，以「同标题后缀 + 继承工作目录」新开独立会话，不伪造分支历史）、操作菜单（Esc / ↑↓ / Home/End + `aria-haspopup`/`aria-expanded`）、行内相对时间 + 「创建于」浮层、空态三态（无会话 / 无匹配 / 全部归档）齐备；详见 §3 批次 3 |
| B4 | §6.2 P1-10（行 498-502） | `ErrorBoundary`/`componentDidCatch`/chunk 重试零命中；`main.tsx:37` 仍 `document.getElementById("root")!`，`bootstrapDocumentSettings()`（`:17-35`）在挂载前无捕获。 | **已修复**（2026-09-13）：新增 `components/errors/*`（全局/路由/面板三层边界 + 统一错误面 + chunk 可重试路由壳）、`lib/lazy-retry.ts`（尝试上限 + 400/1200ms 递增退避 + `ChunkLoadError`）、`core/bootstrap/*`（`#root` 解析 / 启动失败可见面 / 启动完整性检查）、`core/logger.ts`；`main.tsx` 全启动路径转可见错误面，`App.tsx` 路由改 `RetryableLazyRoute`，右栏两面板挂 `PanelErrorBoundary`；双语 `errors.*` 10 键对齐 |
| B5 | §6.3（行 504-611） | P2 共 11 项：0 项整包交付，P2-1A 已有八项子能力落地（后台任务 Jobs、运行时状态快照、用量 / 配额面板、会话元数据搜索、运行时文件读取与预览、技能市场与热重载、子代理控制面（AgentControl 身份图）、侧栏会话统计摘要（`/sessions/stats`），2026-09-13）；P2-1、P2-7 部分（见 C2/C3）；**P2-6 已完成**（排序双模式 / 手动顺序账目 / 组内拖拽重排（批次 15，`10627d34`）；会话分组视图（按目录 / 平铺）+ 跨组移动与 Host 写回（批次 16，`8520de33`）；组内展开 / 折叠（Show N more）+ 空白会话提升（批次 17，`f2c9cf98`）；空态三态由批次 3 交付，`c25a72e0`）、**P2-9 部分**（子片 1-4：子片 1-3 已由批次 9-11 交付，子片 4「composer 上方浮动『当前任务』Todo 面板」已由批次 18 交付）；P2-2/3/4/5/8/10/11 零实施（依据：a11y/axe 零命中、无视觉 golden、无虚拟化与基准、侧栏排序 / 组内拖拽已由批次 15 交付、分组视图与跨组移动已由批次 16 交付（归档 / 归档恢复已由批次 3 交付）、无 Files changed 行、goal 指示已由批次 11 交付（任务状态条未实施；子代理 lineage 与后代目录已由 P2-1A 控制面交付）、无 `fs/*` 写入消费（`fs/read-file` 已接入）、skills 市场 / 热重载已接入、右栏硬编码两面板）。 | §6.3 已启用「P2-1A 逐项」状态块（Jobs、运行时状态快照、用量 / 配额、会话元数据搜索、运行时文件读取与预览、技能市场与热重载、子代理控制面、会话统计已完成）；待 M3/M4 排期 |

### C. 部分交付类

| ID | 已交付 | 缺口 | 处置 |
|---|---|---|---|
| C1 | P1-4 草稿/附件/命令机制/引用菜单/combobox 语义齐备 | `@` 候选仅 file 组；session/subagent 组待数据源（计划行 464 已披露） | 保持（依赖数据源） |
| C2 | P2-1 A 层：approval `expired`（经 P1-7）、SSE replay（经 P1-8）已可用；**用量 / 配额面板已交付**（`/usage/stats|ledger|policy` 三端点 + token 口径，403 / 503 / 未配置上限均如实降级不伪造，2026-09-13）；**后台任务 Jobs 全链路已交付**（纯前端接线：五端点 API 归一化 + 面板 + runtime 事件联动 + 单测/e2e，2026-09-13）；**运行时状态快照已交付**（`GET /sessions/{id}/runtime` → `types/runtime/session-runtime.ts` + `api/runtime/session-runtime.ts` + `lib/pending-interaction/snapshot.ts` + `hooks/workspace/use-session-runtime-state.ts` + `use-pending-interactions` 水合 + `workspace-page` 接线，关闭「重载/重连后未决审批与提问丢失」，2026-09-13）；**会话元数据搜索已交付**（`POST /sessions/search` 服务端 user/tags AND/state 过滤 + 弹层（计数、触顶提示、空态与不可用降级）+ 竞态收口 hook，2026-09-13）；**运行时文件读取与预览已交付**（`POST /api/runtime/fs/read-file`：base64 解码分型（text / empty / binary / tooLarge）+ 预览弹层 + 工具行文件链接入口，空 / 二进制 / 超限 / 读失败均如实呈现、不落本地兜底，2026-09-13）；**技能市场与热重载已交付**（`GET /skills`、`/skills/{name}`、`/skills/search`、`/skills/stats`、`/skills/hot-reload/stats` 与 `POST /skills/hot-reload/{start,stop,reload}` 八端点 + 目录 / 检索 / 详情 / 统计 / 热重载五区页：目录结构异常不伪装空市场、坏条目只丢单行且保留后端 `count`、semantic 请求被降级时如实标注 `resolved_mode` / `used_embedding`、403 / 503 / policy 禁用均如实降级且不渲染伪 watching，2026-09-13）；**子代理控制面已交付**（`GET /agent-control/agents` + `POST /sessions/{id}/agents/{agent_id}/close|resume`：两段式身份图加载 + 会话头 lineage 面包屑 + 后代目录 + 独立 Stop / Resume、未知状态不提供动作、控制面未启用如实降级，2026-09-13）；**侧栏会话统计摘要已交付**（`GET /sessions/stats`：按 `user_id` 聚合、与侧栏用户筛选同一口径，计数只显非零 chip，不可用 / 失败如实降级且不伪造 0 计数，2026-09-13） | 其余 9 行未接：`fs/write-file\|append-file`（写入侧；读侧 `fs/read-file` 已接）、deliverables 字段、plugin `config_schema`、`top_p`、goal 快照、`Last-Event-ID`、queue/steering、upload、MCP 目录、`/artifacts`、currency cost | Jobs、「审批闭环」、usage / 配额、会话搜索、fs/read-file、skills 市场 / 热重载、子代理控制面与会话统计行已关闭，余 9 行待排期 |
| C3 | P2-7 机制：registry/四分类/键盘 UI/dispatch/`no-executor` 提示 | **内置命令清单与执行器已交付（2026-09-13，批次 12）**：`lib/composer-builtin-commands.ts`（`export`=`action` / `rename`=`execute` 清单 + 参数解析：`--redact` 白名单、未知 flag 与多余位置参数前置失败、空标题前置失败）、`hooks/workspace/composer/use-composer-command-executor.ts`（认领 / 未认领布尔契约 + 逐命令错误隔离 + 通知只持 i18n key）、`main-section.tsx` 注入 `commands` 与执行器；`/export` 复用轨迹页同一 `exportSessionTrajectoryJsonl`（JSONL 下载），`/rename` 复用侧栏同一 `onRenameSession`（不另建处理器）；结果以 `role="status"` / `role="alert"` 通知条回执并可关闭。**`/model` 目录弹窗与 `/feedback` log-only 入口已交付（2026-09-13，批次 13）**：`lib/composer-model-options.ts`（目录 → provider 分组（保序、同名跨 provider 不合并）、按名 / 按座位解析当前项，未就绪 / 空目录如实返回空集与 not-ready）、`components/workspace/composer-model-dialog.tsx`（分组列表 + 当前项徽标 + loading / error / empty 三态 + Esc 关闭，选中走常驻座位同一 `onModelChange`）、`hooks/workspace/composer/use-composer-command-surface.ts`（清单 / `/model` 候选与弹窗 / 执行器 / 回执文案收口为单一命令面）；`/model` 缺参 / 目录未就绪前置失败、未知模型名如实报错并列出目录，`/feedback` 只写本地日志且回执明说本版本未接入上报渠道（**不伪造模型名、不假装已上报**）。**命令专属 popupSelect 候选面已交付（2026-09-13，批次 14）**：`lib/composer-model-options.ts` 增 `filterComposerModelGroups`（大小写不敏感子串检索，模型名或所属 provider 名任一命中即保留，落空分组不留空壳）、`components/workspace/composer-model-dialog.tsx` 弹层自持焦点（打开即检索，不必先点输入框）+ ↑/↓ 虚拟高亮 + Enter 应用 + 高亮行滚入视口；「检索无匹配」与「目录为空」两种状态各自如实呈现（**不补占位模型、不伪造默认选中**）。**P2-7 至此收口：四个子片全部落地** | 已关闭（批次 14），P2-7 收口 |

### D. 台账 / 文档类

| ID | 证据 | 处置 |
|---|---|---|
| D1 | §9.3 最后一条为 P1-7（行 805）；P1-5/8/9/10 无任何状态记录 | **批次 1 已在 §6.2 补 4 条状态行**（未开始，含原因） |
| D2 | §6.3 无 `> **状态**` 行，11 项任务不可追踪 | 已加小节说明行；完整登记机制随 P2 启动建立 |
| D3 | P0-1 清理回退、P0-2 全局指标失效、P0-4 数值漂移均未入台账 | **批次 1 已加 P0 复检备注 + §9.4 汇总** |
| D4 | §5.5 A2 引 `logs-page.tsx:132-166,301-303` 已失效（P0-2 `76c2f5be` 拆分） | **批次 1 已改为 `pages/logs-page/connection.tsx` / `logs-header.tsx`** |
| D5 | `frontend/src` 注释「P1-5 方案 1/2/4」实指 `multi-agent-execution-optimization-plan.md` | **批次 1 已在 P1-5 状态行与 §9.4 加注** |
| D6 | 计划头「草案（待评审）」；i18n 扫描数 534 vs 实测 540 | **批次 1 已更新头部状态与 §9.4 计数** |

### E. 工程 / 流程类

| ID | 证据 | 处置 |
|---|---|---|
| E1 | `.gitignore` 屏蔽使回退隐形；无任何检查脚本看护 P0-1 约束 | **批次 1 新增 `frontend/scripts/verify-no-backups.mjs`**（扫描 751 文件；排除 node_modules/dist/.artifacts/.tmp/test-results/playwright-report），已并入 `npm run lint`，并提供 `npm run verify:clean` |
| E2 | 构建警告 INEFFECTIVE_DYNAMIC_IMPORT；复核 `i18n/loaders.ts:7-10` 注释：首屏由 `initI18n` 同步注入全部 namespace（避免闪烁/缺键），动态入口用于语言切换与回退审计 | **关闭**：不追求 chunk 拆分；已登记为"设计口径"而非缺口 |
| E3 | 无 `toHaveScreenshot`/golden、无 2000 事件基准、无 markdown 缓存指标 | 待排期（随 P2-4/P2-5） |

## 3. 处置记录与排期

### 批次 1（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| A1 | 清理 `frontend/` 全域本地备份：**72 个 `.bak` + 19 个 `.backups/`**（删除前确认 19 个目录内除 `.bak` 外无其它文件；唯一无对应源文件的 `frontend/.tmp/en-US-common.probe.bak` 为 i18n 探针产物，一并删除） | 清理后 `Get-ChildItem -Recurse -Force` 实测 0 / 0 |
| E1 | 新增 `frontend/scripts/verify-no-backups.mjs`；`package.json` 增加 `verify:clean` 并接入 `lint` 链 | `npm run lint` → 0 error / 3 warning（3 条为基线既有）+ `i18n lint OK（scanned=540, violations=0）` + `[verify-no-backups] OK（扫描 751 个文件，0 处残留）` |
| D1 | §6.2 为 P1-5 / P1-8 / P1-9 / P1-10 增补「未开始」状态行（含依赖与依据） | 计划文件 §6.2 |
| D3 | P0-1 / P0-2 / P0-4 状态行追加「2026-09-13 复检」备注；新增 §9.4 复核与缺口登记 | 计划文件 §6.1 / §9.4 |
| D4 | §5.5 A2 与 P1-8 背景中的 `logs-page.tsx:132-166,301-303` 引用更新为拆分后的实际路径 | 计划文件行 299 / 488 |
| D5 | 在 P1-5 状态行与 §9.4 标注跨计划编号冲突 | 计划文件 §6.2 / §9.4 |
| D6 | 头部状态由「草案（待评审）」改为「执行中」并链接本清单；i18n 计数漂移入 §9.4 | 计划文件行 3 |
| B4 | P1-10 实现（worktree 隔离子任务，20 个文件）。子任务 79 步后被 `execution_context` 取消（`session_end.status=stopped`，非代码失败），产出经本会话逐文件复核后收编主树：三层错误边界、chunk 上限 + 递增退避重试、`#root`/启动失败可见面、启动完整性检查、统一 logger、zh/en 10 键、5 个测试文件 | 隔离树：lint 0 error / 3 基线 warning、i18n 550/0、**vitest 126 文件 / 778 用例**、build exit 0；主树复跑同结果 |

### 批次 2（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| A2 | **三类超长文件拆分**（行为中性：公共 API 不变、既有断言零改动）：① `components/workspace/message-markdown-streaming.ts`(525) → 目录 barrel `message-markdown-streaming/{types 59, blocks 102, fence 93, tails 276, index 19}`；② `trajectory/subagent-session-dialog.test.tsx`(516) → 301 行主文件（12 个 it 标题 1:1）+ `subagent-session-dialog.test-helpers.ts`(133) + `subagent-session-target.test.ts`(51) + `subagent-session-dialog-trajectory.test.tsx`(102)；③ `styles/globals.css`(1043) → 入口 17 + `styles/globals/{theme 110, tokens 335, themes 150, base 435}`（切割脚本先做字节级重组校验再落盘，保序） | `frontend/src` 内 > 500 行文件 **3 → 0**（`Get-ChildItem -Recurse` 实测）；构建产物 `index-B1wjI4k5.css`(129938 B) / `landing-page-D_9RRpVY.css`(1407 B) 与拆分前**同尺寸同 SHA256**（逐字节一致）；主树复跑：`pnpm lint` 0 error / 3 基线 warning（i18n scanned=555 / violations=0、备份门禁 777 文件 0 残留）、`vitest` **128 文件 / 778 用例全绿**（73.3s）、`test:e2e` **35 passed**（1.5m）、build exit 0 |
| B2 | **P1-8 连接状态统一与断线恢复收口**（复用既有重连，未新增第二套退避）：新增 `lib/connection-status.ts`（统一状态词汇/配色/文案 + `connectionStatusFromLogsState` + `withTransportDegradation`）、`components/ui/connection-status-badge.tsx`（pill/header 双变体，非在线态挂手动重试）、`hooks/workspace/use-connection-status-labels.ts`；`use-session-runtime-stream.ts` 暴露 `connectionStatus/retryConnection`（状态按「会话键」存储：无会话派生 idle、重试回落 connecting；effect 同步段零 setState + 守卫式状态更新避免逐事件重渲染）；顶栏与消息流尾接入状态条；`workspace-page.tsx` 收口直连 `/api/agent/chat` 断线；日志页头改挂共享呈现件（`variant="header"`，视觉不变）；新增 `e2e/connection-recovery.spec.ts` | `pnpm lint` 0 error / 3 基线 warning（i18n scanned=558 / violations=0、备份门禁 784 文件 0 残留）；**vitest 131 文件 / 793 用例**（65.8s）；`build` exit 0；`test:e2e` **36 passed**（1.5m，含新增用例：断线状态可见 + 手动重试不重发 chat、重放 delta 幂等） |

### 批次 2 建议（按优先级）

1. ~~**B4 验收与合入**~~ ✅ 已完成（2026-09-13）：`main.tsx` 启动路径逐行复核（`bootstrapDocumentSettings` / `createRoot` / `render` 全部 try/catch，失败落可见错误面且不覆盖已有 `role="alert"`）；`App.tsx` 的 loader 提升为模块级稳定引用，手动重试经 generation 重建而非无界重载。下一项转 A2。
2. ~~**A2 拆分**~~ ✅ 已完成（2026-09-13）：三个目标全部落地（5 + 4 + 3 个文件）；`globals.css` 保序拆分以「切割脚本字节级重组校验 + 构建产物 CSS SHA256 对拍」双重验证，拆分后 `pnpm test:e2e` **35 passed** 复跑。下一项转 B2。
3. ~~**B2**~~ ✅ 已完成（2026-09-13）：统一连接呈现件（pill/header 双变体，日志页头与会话流共用）→ 顶栏/消息流尾状态条 → 直连 chat 断线收口 → 手动重试复用会话流入口（幂等：重试不重发 chat、重放 delta 不重复渲染）。下一项转 **B1 → B3**：队列与 Steering（依赖后端 API，需先确认接口就绪度）→ 会话列表状态与整理（后端已就绪，纯前端接线）。
4. **D2/E3**：P2 启动时建立 §6.3 状态登记；视觉回归/性能基准随 P2-4/P2-5。
5. ~~**B3**~~ ✅ 已完成（2026-09-13，批次 3）：会话行状态指示（等待类优先级合并）→ 操作菜单 Fork/归档/恢复/非破坏删除（键盘 + aria）→ 行内相对时间与「创建于」浮层 → 空态三态；Fork 以「同标题后缀 + 继承工作目录」新开独立会话（后端无克隆 API，不伪造分支历史）。

### 批次 3（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| B3 | **P1-9 会话列表状态与整理能力**：新增 `workspace-sidebar/session-row-actions.ts`（`buildForkSessionTitle` / `buildForkSessionRequest` / `resolveSelectionAfterSessionDelete`）+ 单测 4 例；`session-row-status.ts` / `state-icon-utils.ts` / `labels.ts` 行状态指示（等待审批 > 计划待审 > 等待回答 > 运行中 > 子代理 > 归档/关闭 > 空闲）；`session-item.tsx` 菜单补齐 Fork / 归档 / 恢复 / 删除（Esc / ↑↓ / Home/End / Tab + `aria-haspopup`/`aria-expanded`）；`sessions-section.tsx` 空态三态与菜单接线；`workspace-page.tsx` 三个处理器（Fork 新会话、非破坏删除 + 当前会话回落 `/workspace/chats/new`、归档/恢复沿用）+ `workspace-shell{s,}` / `workspace-sidebar` 全链路传参；双语 `sidebar.session.{fork,forkSuffix,delete}` 对齐；`e2e/mock-server.mjs` 补 POST 建会话确定性 id（`e2e-fork-N`）与单会话 DELETE；新增 `e2e/sidebar-session-actions.spec.ts` 2 用例 | `pnpm lint` **0 error / 3 基线 warning**（i18n scanned=560 / violations=0、备份门禁 789 文件 0 残留；清理本次 mock 编辑产生的 `e2e/.backups/` 后 `verify:clean` 复跑 OK）；`pnpm test` **133 文件 / 803 用例全绿**（73.1s，B3 新增 1 文件 / 4 用例）；`pnpm build` exit 0；`pnpm test:e2e` **38 passed**（1.6m，含新增两用例） |

### 批次 4（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-1A-Jobs | **后台任务（Jobs）面板**（缺口 C2 首项，P2 起点；后端零改动，纯前端接线）：`types/runtime/jobs.ts` + `api/runtime/jobs.ts`（五端点归一化，兼容 `background.Job` 的 PascalCase 序列化键）；`hooks/workspace/use-background-jobs.ts`（打开拉取、`job_*` runtime 事件 800ms 合并刷新、live 任务 5s 轮询、取消后即时刷新）+ `use-elapsed-tick.ts`（仅 live 存在时计时）；`components/workspace/jobs-panel.tsx` + `jobs-panel-shared.ts`（会话头顶栏入口、live/settled 分区、耗时与退出码、输出分页展开、Esc/遮罩关闭 + 焦点恢复）；双语 `panels.jobs.*`；`e2e/mock-server.mjs` 新增 jobs 五端点与 `POST /api/_test/jobs` 注入（`/api/_test/reset` 清空） | `pnpm lint` **0 error / 3 基线 warning**（i18n scanned=567 / violations=0、备份门禁 802 文件 0 残留）；`pnpm test` **136 文件 / 837 用例全绿**（80.1s，新增 3 文件 / 34 例：`api/runtime/jobs.test.ts` 11、`components/workspace/jobs-panel-shared.test.ts` 15、`components/workspace/jobs-panel.test.tsx` 8）；`pnpm build` exit 0；`pnpm test:e2e` **40 passed**（1.6m，含新增 `e2e/jobs-panel.spec.ts` 2 例） |
| P2-1A-Runtime 快照 | **运行时状态快照 → 待交互重建**（关闭 P1-7 遗留的「重载 / 重连后未决审批与提问丢失」，A 表「审批闭环」行整行闭环；后端零改动）：`types/runtime/session-runtime.ts`（+ barrel）；`api/runtime/session-runtime.ts`（`getSessionRuntimeState`：404 按空态、结构不满足抛错不伪造、snake_case/camelCase 双兼容、id 编码）；`lib/pending-interaction/snapshot.ts`（幂等重建 + 不回退 `resolving`/终态 + 缺席结算 `snapshot_absent` + `expires_at` 超时兜底）；`hooks/workspace/use-session-runtime-state.ts`（按会话键存、切换/卸载 abort、同步段零 setState、`refresh`）；`hooks/workspace/use-pending-interactions.ts` 水合 + `workspace-page.tsx` 接线；`e2e/mock-server.mjs` 新增由事件存储推导未决项的 `GET /sessions/{id}/runtime` | `pnpm lint` **0 error / 3 基线 warning**（i18n scanned=571 / violations=0、备份门禁 809 文件 0 残留）；`pnpm test` **139 文件 / 862 用例全绿**（78.1s，新增 3 文件 / 25 例：`api/runtime/session-runtime.test.ts` 9、`lib/pending-interaction/snapshot.test.ts` 10、`hooks/workspace/use-session-runtime-state.test.tsx` 6）；`pnpm build` exit 0；`pnpm test:e2e` **41 passed**（1.6m，含新增 `e2e/pending-interaction.spec.ts` P1-7d；反向对照：临时摘除 mock 端点后该用例失败，证明卡片由快照水合而非事件流） |
| P2-1A-Usage | **用量 / 配额面板**（缺口 C2「usage / 配额」行；P2 第二项；后端零改动，纯前端接线，**严格 token 口径、不推算货币成本**）：`types/runtime/usage.ts` + `api/runtime/usage.ts`（`/usage/stats|ledger|policy` 三端点归一化；缺 `usage`/`policy`/`records` 判结构失败抛错不伪造空态；`scope` 缺席即全局、`quota` 缺席显式降级；403/503 保留 `status`）；`hooks/use-usage-quota.ts`（三段独立请求 / 独立错误、按作用域重取、筛选后重取账本、已知作用域保留供回选）；`pages/usage-analytics/quota.tsx`（`UsageQuotaPanel`：策略摘要、余量与生效来源、账本筛选与匹配计数、403 补 token 提示、503 账本未配置、未配置上限显示「无配额快照」）；`pages/usage-analytics/overview.tsx` 挂载 + 双语 `usageAnalytics.quota.*`；`e2e/mock-server.mjs` 新增 usage 三端点夹具（全局带 `scopes`、作用域带 `scope + quota`、`tenant-a` 无上限返回 `quota: null`、账本支持 entrypoint / skill / success / limit 过滤）、新增 `e2e/usage-quota.spec.ts` 2 例 | `pnpm lint` **0 error / 3 基线 warning**（i18n scanned=575 / violations=0、备份门禁 817 文件 0 残留）；`pnpm test` **142 文件 / 891 用例全绿**（84.7s，新增 3 文件 / 29 例：`api/runtime/usage.test.ts`、`hooks/use-usage-quota.test.tsx`、`pages/usage-analytics/quota.test.tsx`）；`pnpm build` exit 0；`pnpm test:e2e` **43 passed**（1.7m，含新增 `e2e/usage-quota.spec.ts` 2 例） |
| P2-1A-SessionSearch | **会话元数据搜索**（缺口 C2「sessions/search」行；后端零改动，纯前端接线）：`types/runtime/sessions.ts` 增补 `RuntimeSessionRecord` / `RuntimeSessionSearchFilters`（入参 camelCase）/ `RuntimeSessionSearchEcho`（后端 filters 回显 camelCase，与请求体 snake_case 不同名）/ `RuntimeSessionSearchResponse`；`api/runtime/session-search.ts`（请求体 snake_case、空 user/tags/state 省略、`limit` 默认 50、offset 收口；`sessions` 非数组抛错不伪装空结果、单条缺 `id` 只丢该条；`isSessionSearchUnavailable` 归类 404/405/501/503 与 `STORE_UNAVAILABLE`）；`hooks/workspace/use-session-search.ts`（idle → loading → ready/error：序号丢弃过期响应、新检索与卸载 abort 在途请求、主动取消不落错误态）；`components/workspace/session-search-dialog.tsx` + `session-search-shared.ts`（用户 / 状态 / 标签 AND 三组筛选、结果行与相对时间、`N session(s) matched` 计数与触顶提示、空态与不可用降级分列、Esc/遮罩关闭 + 焦点回位）；侧栏入口接线（打开即重挂载）；双语 `panels.sessionSearch.*`；`e2e/mock-server.mjs` 新增 `POST /api/runtime/sessions/search` 与由 seed 派生的 `/sessions/users`、新增 `e2e/session-search.spec.ts` 3 例 | `pnpm lint` **0 error / 3 基线 warning**（i18n scanned=579 / violations=0、备份门禁 826 文件 0 残留）；`pnpm test` **144 文件 / 908 用例全绿**（98.4s，新增 2 文件 / 17 例：`api/runtime/session-search.test.ts` 12、`components/workspace/session-search-dialog.test.tsx` 5）；`pnpm build` exit 0；`pnpm test:e2e` **46 passed**（1.8m，含新增 `e2e/session-search.spec.ts` 3 例） |
| P2-1A-FilePreview | **运行时文件读取与预览**（缺口 C2「fs/read-file」行；后端零改动，纯前端接线，**内容唯一来源为运行时进程返回的字节，不做本地兜底缓存或占位文本**）：`lib/file-preview/decode.ts`（base64 解码 → text / empty / binary（NUL 或 UTF-8 失败）、行数统计、`formatByteSize`）；`types/runtime/files.ts`（+ barrel）与 `api/runtime/files.ts`（`FILE_READ_PATH`、`FILE_PREVIEW_MAX_BYTES = 1_000_000`、`normalizeFileReadPayload`、`readRuntimeFile`、`isFileReadUnavailable`）；`hooks/workspace/use-file-preview.ts`（closed → loading → ready/error：序号丢弃过期响应、open / retry / close abort 在途请求、超限 `tooLarge` 且 `body=null`）+ `hooks/workspace/use-focus-restore.ts`；`components/workspace/file-preview-dialog.tsx`（`role="dialog"` + `aria-modal` + `tabIndex={-1}` + 打开即聚焦、`data-testid` 家族、错误分「端点不可用（带状态码）」与「真实失败（后端 error 文本）」）；工具行文件链接入口（`data-tool-row-file-link="true"`）；双语 `panels.filePreview.*`；`e2e/mock-server.mjs` 新增 `POST /api/runtime/fs/read-file`（未登记路径 **500 + `no such file`**，与后端口径一致、不伪装 404）+ `e2e/support.ts` 新增 `seedRuntimeFiles`、新增 `e2e/file-preview.spec.ts` 3 例 | `pnpm lint` **0 error / 3 基线 warning**（i18n scanned=584 / violations=0、备份门禁 837 文件 0 残留）；`pnpm test` **147 文件 / 934 用例全绿**（82.05s，新增 3 文件 / 26 例：`lib/file-preview/decode.test.ts` 8、`api/runtime/files.test.ts` 9、`components/workspace/file-preview-dialog.test.tsx` 9）；`pnpm build` exit 0；`pnpm test:e2e` **49 passed**（1.9m，含新增 `e2e/file-preview.spec.ts` 3 例） |

### 批次 5（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-1A-Skills（台账回填） | **技能市场与热重载**（缺口 C2「skills 市场 / 热重载」行；后端零改动、纯前端接线）：`types/runtime/skills.ts`（+ barrel）；`api/runtime/skills.ts`（八端点归一化：非数组抛错不伪装空市场、坏条目只丢单行且保留后端 `count`、`mutation_policy` / `embedding` 缺席置 `null` 显示未知、检索 `limit` 默认 20 / 上限 200、`isSkillsUnavailable` 与 `isSkillsForbidden` 分类不混用、写操作 `X-Skills-Admin-Token` 空串不发头）；`hooks/use-skills-market.ts`（三段独立请求 / 独立错误、检索一份在途 + 序号丢弃过期响应、写操作成功以响应 stats 覆盖本地快照）；`pages/skills-page.tsx` + `pages/skills/{catalog,detail,stats,hot-reload,shared}.tsx`（目录 / 检索：`resolved_mode` 与 `used_embedding` 原样展示；详情失败不回落本地缓存；policy 三态；热重载 503 不渲染伪 watching / 403 提示补令牌）；`App.tsx` 路由 + runtime-config 与侧栏入口；双语 `skills.*`；`e2e/mock-server.mjs` skills 全族夹具（含缺 `name` 坏条目且 `count` 上报 3）+ `e2e/skills-market.spec.ts` 4 例 | `pnpm lint` **0 error / 3 基线 warning**（i18n scanned=593 / violations=0、备份门禁 850 文件 0 残留）；`pnpm test` **148 文件 / 948 用例全绿**（101.94s，新增 1 文件 / 14 例：`api/runtime/skills.test.ts` 14）；`pnpm build` exit 0；`pnpm test:e2e` **53 passed**（2.2m，含新增 `e2e/skills-market.spec.ts` 4 例） |

### 批次 6（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-1A-AgentControl | **子代理控制面（AgentControl 身份图）**（缺口 C2「subagent 控制面」行；后端零改动、纯前端接线；动作面只做「看 + 停 + 恢复」）：`types/runtime/agents.ts`（`active` / `stale` / `closed` / `unknown` + `RuntimeAgentRecord` / `RuntimeAgentCatalog` / `RuntimeAgentMutation`）；`api/runtime/agents.ts`（`listRuntimeAgents` 三类过滤 + `include_closed`、前端收口 `limit` 默认 200 / 上限 500；`closeRuntimeAgent` / `resumeRuntimeAgent`；`normalizeRuntimeAgentStatus` 未知值一律 `unknown`；`isAgentControlUnavailable` 404/405/501/503）；`hooks/use-session-agents.ts`（两段式加载 + `pickCurrentAgent` / `buildAgentLineage` / `listAgentDescendants` / `deriveSessionAgentTree`；AbortController + 序号；身份行缺失不等于错误；`truncated` 按后端 `count` 比较；close / resume 只以响应身份行覆盖本地行）；`components/workspace/session-agents-panel.tsx` + `session-agents-panel-shared.ts`（portal `role=dialog` 弹层：lineage 链 + 后代目录（运行 / 已结算分区、元数据、触顶提示）、`active` / `stale` 可停止、`closed` 可恢复、`unknown` 不给动作）；`workspace-shell-topbar.tsx` lineage 面包屑 + 入口（含后代计数）、`main-section.tsx` 装载；双语 `panels.agents.*` + `topbar.agents` / `agentsBreadcrumb` | `pnpm exec vitest run src/components/workspace/session-agents-panel.test.tsx src/components/workspace/session-agents-panel-shared.test.ts` **20 passed**；`pnpm lint` **0 error / 3 基线 warning**（i18n scanned=598 / violations=0、备份门禁 861 文件 0 残留）；`pnpm test` **152 文件 / 997 用例全绿**（103.40s，新增 4 文件 / 49 例：`api/runtime/agents.test.ts` 17、`hooks/use-session-agents.test.tsx` 12、`session-agents-panel-shared.test.ts` 9、`session-agents-panel.test.tsx` 11）；`pnpm build` exit 0；`pnpm test:e2e` **53 passed**（2.2m，本批未新增 e2e 用例） |

### 批次 7（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-1A-SessionStats | **侧栏会话统计摘要**（缺口 C2「`/sessions/stats`」行；后端零改动、纯前端接线；口径硬约束：只呈现后端上报计数，不推算 / 不补零）：`types/runtime/sessions.ts` 增补 `RuntimeSessionStats` / `RuntimeSessionStatsResponse`；`api/runtime/session-stats.ts`（`fetchRuntimeSessionStats`：`user_id` 空串不发查询参数；`normalizeSessionStats`：`stats` 非对象即抛错、计数只收有限非负数取整、`tags` 只保留字符串键 → 有限数且不构造占位标签；`isSessionStatsUnavailable`：404/405/501/503 判不可用，与真实失败分列）；`hooks/workspace/use-session-stats.ts`（idle → loading → ready/error + `refresh()`；AbortController + 请求序号：userId 变化 / 重跑 / 卸载中止在途、过期响应丢弃；主动 abort 不落错误态；失败保留上次成功数据）；`components/workspace/workspace-sidebar/session-stats-summary.tsx` + `session-stats-summary-shared.ts`（total 恒显、其余计数非零才显 chip（`data-testid` 家族 `session-stats-summary` / `session-stats-chip-*` / `session-stats-loading` / `session-stats-unavailable` / `session-stats-error` / `session-stats-retry`）；加载中不渲染骨架数值；不可用 / 失败各有独立呈现且可重试，不回落成 0 计数）；接线：`workspace-sidebar.tsx` 与侧栏用户筛选用同一 `userId`、`sessions-section.tsx` 渲染并透传 `refresh`；双语 `workspace.base.sessionStats.*`；`e2e/mock-server.mjs` 新增 `GET /api/runtime/sessions/stats` 夹具（按 `user_id` 聚合、camelCase `totalMessages`）+ `e2e/support.ts` 的 `seedSession({ userId, state })` 维度 | `pnpm exec vitest run`（本批 4 文件）**29 passed**（`api/runtime/session-stats.test.ts`、`hooks/workspace/use-session-stats.test.tsx`、`session-stats-summary-shared.test.ts`、`session-stats-summary.test.tsx`）；全量门禁：`pnpm lint` **0 error / 3 基线 warning**（i18n scanned=602 / violations=0、备份门禁 870 文件 0 残留）；`pnpm test` **156 文件 / 1026 用例全绿**（107.02s，新增 4 文件 / 29 例）；`pnpm build` exit 0；`pnpm test:e2e` **57 passed**（2.2m，含新增 `e2e/session-stats.spec.ts` 4 例：默认用户聚合与 seed 一致、切侧栏用户重取、503 如实降级且不渲染任何 chip 并在恢复后重试可见真实计数、500 按真实失败呈现） |

### 批次 8（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| A2-二次修复（P0-2 行数门禁） | **两个「随 P2-1A 接线增量引入」的超长文件拆分 + 行数门禁**（行为中性：公共 API 不变、既有断言零改动）：① `pages/usage-analytics/quota.tsx`(559 非空行) → `quota.tsx` 429 + `quota-shared.ts` 74（常量 / 负载形状辅助 / 纯格式函数）+ `quota-atoms.tsx` 73（`PolicyBadge` / `StatRow` / `QuotaBar`；组件与纯函数分文件以过 `react-refresh/only-export-components`）；② `pages/workspace-page.tsx`(512) → 437 + `hooks/workspace/use-workspace-session-actions.ts` 133（重命名 / 归档 / 归档恢复 / Fork / 删除 / 目录内新建六个动作收口为 hook，页面只保留组合与转发，页面内 `navigate` / `t` 随之下沉）；③ 新增 `scripts/verify-max-lines.mjs`：`frontend/src` 下 `.ts/.tsx` 非空行 > 500 即失败、`src/i18n/resources/**` 整树豁免，并入 `npm run lint` 并新增 `npm run verify:lines`（501 行探针负路径验证 exit 1 后清理） | `npm run verify:lines` **exit 0**（扫描 831 个 `.ts/.tsx`、61 个词典豁免、0 个 > 500，最大 `components/workspace/workspace-sidebar.tsx`=483）；`npx tsc -b --force` exit 0；`npx eslint`（5 个改动文件）exit 0；`npm run lint` **0 error / 3 基线 warning**（i18n scanned=605 / violations=0、备份门禁 874 文件 0 残留）；`npm test` **156 文件 / 1026 用例全绿**（94.7s）；`npm run build` exit 0；`npm run test:e2e` **57 passed**（2.2m，含 `usage-quota.spec.ts`） |

### 批次 9（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-9-子片1（后台任务常驻状态条） | **P2-9 首个子片：顶栏常驻状态条 + 数据源单一事实源重构**（后端零改动、纯前端）：① `hooks/workspace/use-background-jobs.ts` 新增 `liveCount` 派生（与弹层 live 分区同一 `isLiveJobStatus` 判据）并导出 `BackgroundJobsController`；② `components/workspace/jobs-panel.tsx` 从「自带 hook + `sessionId` / `lastRuntimeEventType` / `runtimeEventCount`」改为纯渲染 + `controller` 注入；③ `workspace-shell/main-section.tsx` 成为 shell owner（hook 只装载一次，顶栏与弹层共用一份数据）；④ `workspace-shell-topbar.tsx` 新增 `liveJobsCount` 与 `data-testid="topbar-jobs-running"` 常驻状态条（仅 live > 0 渲染、`hidden sm:flex`、点击打开弹层）；⑤ 双语 `topbar.jobsRunning`；**通知位按 §10 风险表约束只做计数与入口，完成 / 失败一次性提示仍归 toast / 桌面通知** | `npx tsc -b --force` exit 0；`npx eslint`（9 个改动文件）exit 0；定向单测 **16 passed**（`jobs-panel.test.tsx` 8、`jobs-status-bar.test.tsx` 2、`use-background-jobs.test.tsx` 6）+ 工作区回归 3 文件 / 9 用例；`npm run lint` **0 error / 3 基线 warning**（i18n scanned=605 / violations=0、备份门禁 876 文件 0 残留（含清理本轮 3 个 `.backups/` 目录）、行数门禁 0 个 > 500）；`npm test` **158 文件 / 1034 用例全绿**（121.6s）；`npm run build` exit 0；`npm run test:e2e` **57 passed**（2.0m，`e2e/jobs-panel.spec.ts` 首个用例改为先断言并点击状态条）。**口径修正**：e2e 只跑 `dist/` 产物，改源码后必须先 `build`（本轮首跑未 build，命中旧产物导致状态条断言假失败） |

### 批次 10（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-9-子片2（子代理树：可折叠到末级 + 只读原因 + 记录跨度） | **P2-9 第二个子片：后代目录升级为可折叠层级树，并补齐只读解释与记录跨度格式化**（后端零改动、纯前端；层级 / 折叠只影响渲染，不改数据）：① `components/workspace/session-agents-panel-shared.ts` 新增纯派生——`buildAgentForest`（`parentAgentId`（集合内）→ `agent_path` 最长前缀祖先回退；两者都解析不到的孤儿行**保留为顶层不丢弃**；环数据断开闭环边且不递归爆栈）、`flattenAgentTree`（折叠感知展开为行序列，附带 `depth` / `childCount` / `hiddenDescendantCount`）、`agentDurationSpan`（`createdAt` → `closedAt ?? updatedAt`，端点缺失 / 非法 / 终点早于起点一律 `null`）、`formatAgentDuration` + `formatAgentDurationExact`（判别联合：秒→分→时→天→天时→月→月天→年→年月；精确档不足一天与紧凑档一致、≥ 一天补零到秒）、`agentReadOnlyReason`（只回答数据能证实的「已关闭记录」/「父代理不在线」，其余 `null`）；② 新增 `components/workspace/session-agents-tree.tsx`（`SessionAgentsTree` + 行组件）：`<ul aria-label>` + `role="treeitem"` + `aria-level` + 分支行 `aria-expanded`、折叠按钮 `data-testid="agent-branch-toggle"`（文案带 `{{name}}` 与 `{{count}}` 隐藏后代数）、记录跨度 chip `data-testid="agent-duration"`（title 明说「记录跨度：from → to（创建 → 关闭/最后更新，**非活跃耗时**）」，不借用目标实现的耗时说辞）、只读原因行 `data-testid="agent-readonly"`；③ `session-agents-panel.tsx` 的行渲染整体下沉到树组件（面板只保留 lineage / 计数 / 截断 / 空态），后代目录由「运行 / 已结算两段平铺」改为**单棵层级树 + 运行 / 已结算计数摘要**（`agents-count-running` / `agents-count-settled`；状态仍在每行徽标上，运行 / 非运行语义不丢）；④ 双语 `panels.agents.*` 新增 `treeAria` / `branchExpand` / `branchCollapse` / `durationLabel` / `durationExactTitle` / `duration.*`（9 档）/ `readonly.*`（2 条），zh-CN 与 en-US 逐键对齐。**如实降级**：层级派生失败不臆造父子；端点缺失不补零、无依据的只读原因不渲染；`unknown` 不给动作。**遗留（后端字段缺口）**：`RuntimeAgentRecord` 无 token 字段，子代理 token 口径暂不渲染（不得用会话级 usage 均摊推算），待后端上报后接入 | `npx tsc -b --force` exit 0；`npx vitest run src/components/workspace/session-agents-tree.test.tsx` **4 passed**（乱序输入建树到末级 + 折叠只隐藏该节点后代 + 只读原因 / 无依据不渲染 + 跨度 chip 与缺端点不渲染）；`npm run lint` **0 error / 3 基线 warning**（i18n scanned=606 / violations=0、备份门禁 878 文件 0 残留（含清理本轮 1 个 `.backups/` 目录 / 2 个文件）、行数门禁 835 文件 0 个 > 500，最大 `workspace-sidebar.tsx`=483）；`npm test` **159 文件 / 1052 用例全绿**（98.7s；本批新增 1 文件 / 18 例：`session-agents-tree.test.tsx` 4 + `session-agents-panel-shared.test.ts` +14）；`npm run build` exit 0；`npm run test:e2e` **57 passed**（2.0m，未新增 e2e 用例——树交互 / 只读原因 / 跨度由组件与纯函数单测覆盖） |

### 批次 11（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-9-子片3（会话目标 goal 四相指示 MVP） | **P2-9 第三个子片：会话目标只读投影**（后端零改动、纯前端；暂停 / 恢复 / 完成入口留给后端快照）。**数据面事实（先复核后实现）**：goal 只在 chat SSE `tool_end.payload.tool.content` 完整到达；线程层 `getToolResultSummary` 截断到 240 字符、轨迹层 `toolResultSummaryOf` 不读 `tool.content`，直接解析二者会拿到半截 JSON——故唯一写入口放在 SSE 边界：① 新增 `src/lib/session-goal/derive.ts`（纯函数）——`parseGoalToolResult`（接受 `{"goal":{...}}` / `{"updated":true,"goal":{...}}` / `{"goal":null}` / `goal_missing` / 裸 goal 对象；截断 / 非法 JSON / 无 goal 字段一律 `unknown`）、`parseSessionGoal`（四相 `active` / `paused` / `budget_limited` / `complete` 映射；未知 status **不映射**、`statusRaw` 保真透出）、`deriveSessionGoal`（`unknown` 不覆盖既有结论、`absent` 明确清空）、`sessionGoalFromTrajectoryItems`（降级路径：摘要不完整即 `null`，宁可无指示不显示半截目标）、`formatGoalTokens`（999 / 1.2k / 20k / 123k / 1.5M；非法值返回空串）；② 新增 `src/lib/session-goal/store.ts`——按 `session_id` 的进程内只读投影：`recordGoalToolEnd`（非 goal 工具与无法判定的结果**不写入、不覆盖**）、`getSessionGoal` / `useSessionGoal`（`useSyncExternalStore`，投影引用稳定）、`resetSessionGoalStore`（测试用）；③ 捕获接线：`agent-chat-turn/stream-handlers.ts` 的 `onToolEnd` 调用 `recordGoalToolEnd(sessionId, payload)`（deps 新增可选 `sessionId`），`use-workspace-agent-chat-turn.ts` 透传 `threadSnapshot.sessionId`；④ 新增 `components/workspace/session-goal-indicator.tsx`（`data-testid="topbar-session-goal"` + `data-phase` + 目标文本 / 用量子节点）；⑤ 顶栏接线：`workspace-shell-topbar.tsx` 以 `selectedThread.sessionId` 订阅（未触碰并行工作流在改文件）；⑥ 双语 `topbar.goal.*`（phase 五键 + `objective` / `statusRaw` / `usage` / `usageUsed` / `completedBy` / `completionSummary` / `updatedAt` / `derivedNote`）。**如实降级**：无捕获数据不渲染；token 两侧都有才显示 `used / budget`（缺字段不补零）；未知状态显示「状态未知」并在 title 透出后端原始 status；`goal_missing` 是合法空操作（清空投影，不当作失败）；刷新后 store 清空即不显示（不回落猜测值），权威源待 §6.3 P2-1B | `npx tsc -b --force` exit 0；定向 `npx vitest run src/lib/session-goal/session-goal.test.ts src/components/workspace/session-goal-indicator.test.tsx` **21 passed**（14 + 7：四相映射 / 未知状态保真 / 截断与非法 JSON 不猜 / `goal_missing` 清空 / token 不补零 / 订阅通知与引用稳定 / 指示条渲染与消失 / 非 goal 工具不渲染）；`npm run lint` **0 error / 3 基线 warning**（i18n scanned=612 / violations=0、备份门禁 887 文件 0 残留、行数门禁 844 个 `.ts/.tsx` 中 0 个 > 500，最大 `workspace-sidebar.tsx`=483）；`npm test` **162 文件 / 1077 用例全绿**（123.8s；本批新增 2 文件 / 21 例，另含并行工作流在库测试文件）；`npm run build` exit 0；`npm run test:e2e` **57 passed**（2.1m，未新增 e2e 用例——四相 / 降级 / 清空由纯函数与组件测试覆盖，e2e 按「先 build 再跑」口径执行） |

### 批次 12（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-7-子片2（内置命令清单与执行器：`/export`、`/rename`） | **P2-7 第二个子片：把 `/` 菜单从「机制在位、无命令可执行」补齐为「真实可执行」**（后端零改动、纯前端；机制层 P1-4 子片 3 已在位，本批不重复定义机制）。① 新增 `lib/composer-builtin-commands.ts`——`COMPOSER_BUILTIN_COMMANDS`（`export`=`action`：`/export [--redact]`；`rename`=`execute`：`/rename <title>`，描述与参数提示走双语 key）+ `parseExportCommandArgs`（`--redact` 白名单：未知 flag 与多余位置参数分别 `unknown-flag` / `unexpected-argument`，**前置失败、不发起导出**）+ `parseRenameCommandArgs`（标题 trim 后为空即失败，**不产生空标题**）+ `createComposerCommandRegistry`（沿用 `lib/composer-commands.ts` 的非法名 / 同名冲突「构造即抛」语义）；② 新增 `hooks/workspace/composer/use-composer-command-executor.ts`——`run` 同步派发并返回 boolean：`true`=已认领（结果异步回填通知条）、`false`=未认领（交回 composer 既有 `no-executor` 提示，**命令行绝不静默降级为 prompt**）；逐命令 `try/catch` **错误隔离**（一条失败不影响后续命令与输入路径）；`/export` 直接复用轨迹页同一实现 `exportSessionTrajectoryJsonl(sessionId, { redact })`（同一分页拉取 → JSONL → 浏览器下载），`/rename` 复用侧栏同一处理器 `onRenameSession`（**单一事实源，不另建重命名通道**）；导出重入保护（进行中再触发如实报错）；通知模型只持 `{ tone, messageKey, values }`，文案在渲染层本地化；③ 新增 `components/workspace/composer-command-result-notice.tsx`——成功 `role="status"` / 失败 `role="alert"` 的可关闭回执条（`data-composer-command-result="success\|error"` + `data-composer-command-result-dismiss`）；④ 新增 `components/workspace/composer-status-row.tsx`（状态条整体抽出，行数门禁：`message-composer.tsx` 一度 507 非空行 → 抽出后达标）；⑤ 接线：`workspace-shell/main-section.tsx` 注入 `commands` 与执行器并映射通知，`message-composer.tsx` 提交后**已认领即清空命令行**、未认领 / 被阻塞保留草稿供修正；⑥ 双语 `composer.builtin.export.*`（9 键）/ `composer.builtin.rename.*`（6 键）；回执条关闭按钮复用既有 `composer.commands.dismiss`。**如实降级**：非法参数、无会话、导出进行中、导出失败、重命名失败各自如实报错，不伪造成功、不补零；无数据源（新会话未登记）不导出。**提交口径（本轮核实）**：菜单打开时纯 Enter 归菜单所有（选择 / 下钻 / 无候选时阻塞并提示），命令行提交走 Ctrl/Cmd+Enter 或发送按钮——e2e 依此口径断言。**未交付（仍开放）**：`/feedback` 入口、`/model` 弹窗与命令专属 popupSelect 候选（后续子片） | `npx tsc -b --force` exit 0；定向单测 `npx vitest run src/lib/composer-builtin-commands.test.ts src/hooks/workspace/composer/use-composer-command-executor.test.tsx` **17 passed**；`npm run lint` **0 error / 3 基线 warning**（i18n scanned=617 / violations=0、备份门禁 895 文件 0 残留、行数门禁 851 个 `.ts/.tsx` 中 0 个 > 500，最大 `workspace-sidebar.tsx`=483）；`npm test` **164 文件 / 1096 用例全绿**（较批次 11 的 162/1077 新增本批 2 例 + 并行工作流在库文件）；`npm run build` exit 0；`npm run test:e2e` **59 passed**（2.0m，含新增 `e2e/composer-commands.spec.ts` 2 例：`/` 菜单列出 `export` / `rename` 且 `/rename` 缺标题给出 alert、清空命令行并可按关；`/export --json` 前置失败且不产生下载 + `/export` 真实下载 JSONL 并以 status 回报条数与文件名）；**提交态独立核验**（临时 worktree = `8833ceb6` + 本批文件）：`npx tsc -b --force` exit 0、`npm test` **163 文件 / 1092 用例全绿** |

### 批次 13（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-7-子片3（`/model` 会话模型切换与目录弹窗 + `/feedback` log-only 入口） | **P2-7 第三个子片：把「命令可执行」推进到「命令有专属候选 / 弹窗，且数据没就绪时如实降级」**（后端零改动、纯前端；机制层 P1-4 子片 3 与 `/export`、`/rename` 执行器已在位，本批不重复定义机制，**不改后端、不新建数据通道**）。① 新增 `lib/composer-model-options.ts`——运行时模型目录（`GET /api/runtime/models`）→ provider 分组（保持目录顺序；同名模型跨 provider **不合并**，避免选错座位）、按名 / 按座位解析当前项；目录未就绪或为空时返回空集与 not-ready，**不伪造模型名、不猜默认值**；② 新增 `components/workspace/composer-model-dialog.tsx`（+ `composer-model-dialog.test.tsx`）——分组列表 + 当前项徽标 + loading / error / empty 三态 + Esc 关闭；选中调用 composer 常驻座位同一个 `onModelChange`（**单一事实源，不另建选择通道**）；③ 新增 `hooks/workspace/composer/use-composer-command-surface.ts`——命令清单、`/model` 候选与弹窗开合、执行器与回执文案收口为一个命令面，`workspace-shell/main-section.tsx` 退化为接线（`commands` / `commandResult` / `onCommand` / `onDismissCommandResult` / `modelGroups` / `modelDialogOpen`）；④ 内置命令扩展 `/model`（缺参 / 目录未就绪前置失败；未知模型名如实报错并列出目录可选值）与 `/feedback`（log-only：只写本地日志，回执明说本版本未接入上报渠道）；⑤ `workspace-shell.tsx` / `workspace-shell/types.ts` / `pages/workspace-page.tsx` 透传 `runtimeModels`（与常驻座位同源，不新建缓存或请求）；⑥ 双语 `workspace.base.modelDialog.*` 与 `composer.builtin.model` / `composer.builtin.feedback` 逐键对齐。**如实降级**：目录 loading / 失败 / 空各自如实呈现；无目录时不弹假候选；`/feedback` 不假装已上报。**未交付（仍开放）**：命令专属 popupSelect 候选（§9.1 收敛表 5-12）。 | `npm run lint` **0 error / 3 基线 warning**（i18n scanned=617 / violations=0、备份门禁 897 文件 0 残留、行数门禁 852 个 `.ts/.tsx` 中 0 个 > 500，最大 `workspace-sidebar.tsx`=483）；`npm test` **165 文件 / 1117 用例全绿**（较批次 12 的 164/1096 新增本批单测 + 并行工作流在库文件）；`npm run build`（`tsc -b && vite build`）exit 0；定向 e2e `npx playwright test e2e/composer-commands.spec.ts` **5 passed**（新增 3 例：`/model` 打开目录弹窗并应用所选且座位断言生效、无目录时 not-ready 且不伪造候选、`/feedback` log-only 回执）；**提交态独立核验**（临时 worktree `ai-agent-runtime-v13` 重置到提交 `28546e71`、`git status` 干净后重跑）：lint **0 error / 3 基线 warning**、`npm test` **165 文件 / 1117 用例全绿**、`npm run build` exit 0、定向 e2e **5 passed** —— 重置后工作树与提交树逐字节一致，即上述门禁即提交态门禁 |

### 批次 14（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-7-子片4（命令专属 popupSelect 候选面） | **P2-7 第四个子片（收口）：把 `/model` 候选面补齐到目标项目 `PopupSelectView` 的键盘 / 检索语义**（后端零改动、纯前端；机制层 P1-4 子片 3、内置执行器、`/model` 弹窗分别已在批次 12 / 13 在位，本批不重复定义机制，**不改后端、不新建数据通道**）。① `components/workspace/composer-model-dialog.tsx`——弹层**自持焦点**（打开即把焦点交给检索框，打字即检索，不必先点输入框）、↑/↓ 虚拟高亮（焦点留在检索框）+ Enter 应用（`preventDefault` 阻止行按钮默认激活，同一次按键不重复派发）、高亮行 `scrollIntoView({ block: "nearest" })` 滚入视口；② `lib/composer-model-options.ts` 新增 `filterComposerModelGroups`——与目标项目 `filterOptions` 同口径的本地检索：空 query 原样返回（不排序、不裁剪）、大小写不敏感**子串**命中模型名或所属 provider 名、全部落空的 provider 分组不留空壳；③ **「检索无匹配」与「目录为空」是两种状态**，各自如实呈现（新增双语 `workspace.base.modelDialog.noMatch` / `search.{aria,placeholder}`）——**不补占位模型、不伪造默认选中**；④ 目录晚到（先 loading、目录后到）时检索框更晚挂载：以「待聚焦」标志把焦点接住，**修复打开瞬间焦点落在遮罩上的真实缺陷**；⑤ 候选面状态随弹层挂载——外壳只持对话框生命周期，检索词与高亮随卸载归零，高亮改为派生值（默认跟住当前座位，用户 ↑/↓ 或悬停后接管），避免在 effect 里同步 `setState` 触发级联渲染（过 `react-hooks/set-state-in-effect` 门禁）。**P2-7 至此收口：四个子片全部落地**（§9.1 收敛表 5-9 / 5-12） | 提交 `82c81149`（9 文件，+563/−13；其中 2 个与并行工作流共享的双语词典按「HEAD + 本批 hunk」入索引，并行工作流的在飞改动未混入）。主树复测：`npm run build` exit 0、定向 vitest **4 文件 / 47 用例全绿**、定向 e2e `e2e/composer-commands.spec.ts` **7 passed**（本批新增 2 例：候选面本地检索 + 无匹配与空目录区分 + 虚拟高亮 / Enter 应用；菜单内下钻派发并应用）。**提交态独立核验**（临时 worktree 重置到 `82c81149`、`pnpm install --offline` 后重跑）：`npm run lint` **0 error / 3 基线 warning**（i18n scanned=617 / violations=0、备份门禁 896 文件 0 残留、行数门禁 852 个 `.ts/.tsx` 中 0 个 > 500，最大 `workspace-sidebar.tsx`=483）、`npm test` **165 文件 / 1130 用例全绿**、`npm run build` exit 0、定向 e2e **7 passed**。（主树同批 lint 仅因并行工作流遗留的 4 个 `.backups/` 被备份门禁阻断，非本批引入；提交态 worktree 复跑 0 残留。） |

### 批次 15（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-6-子片1（会话排序双模式 + 手动顺序账目 + 组内拖拽重排） | **P2-6 第一个子片：把会话列表从「固定排序」推进到「最近更新 / 手动双模式 + 组内拖拽重排」**（后端零改动、纯前端；分组按工作目录为批次 3 / P2-1A 既有能力，本批**不改分组语义、不做跨组移动——不写假落点、不做假回滚**）。① `lib/workspace/session-order.ts`——纯函数面：`SessionOrderMode` = `updated | manual`、`compareSessionsByRecency`（时间缺失 / 非法的会话一律沉底、同刻按 id 升序稳定 tiebreak）、`reconcileSessionOrder`（账目中已消失 / 重复的 id 忽略、未入账新会话按最近更新顺序追加末尾）、`orderSessionsForMode`（`updated` 档恒派生且不读不写账目；`manual` 档无账目时按最近更新呈现）、`moveSessionInOrder`（落点与现状等价时返回**入参同一引用**，供「不写空账目」判定）；② `lib/workspace/session-order-store.ts`——版本化浏览器本地账目 `{ version, accounts }`（只收字符串 id 数组；存储不可用 / 配额失败不抛错，也不影响本会话排序结果）；③ `hooks/workspace/use-session-order.ts`——排序模式走设置域 `workspace.sessionOrder`，账目只在拖拽提交时写入（**渲染期零写存储**）；④ `components/workspace/workspace-sidebar/session-order-control.tsx`——`role="group"` + `aria-pressed` 双档控件，两个选项都是真实行为，不含占位项；⑤ `components/workspace/workspace-sidebar/use-session-drag-reorder.ts`——组内拖拽编排（非手动模式整行不可拖、跨组与自落点不接管（浏览器如实呈现「不可放置」）、落点按 `clientY < rect.top + rect.height / 2` 判 `before` / `after`、行内移动不误清落点、等价落点不写账目也不播报、`dragend` 清态）；⑥ 接线 `workspace-sidebar` / `sessions-section` / `session-item`（分组内重排 + 控件 + `role="status"` 播报 + 行级 `draggable` 与落点指示线，缺省形态与不接线时一致）+ 设置域归一化（未知取值回落默认，不做隐式映射）；⑦ 双语 `workspace.base.sidebar.sessionOrder.*` 7 键（label / groupLabel / updated / manual / updatedHint / manualHint / moved，zh-CN 与 en-US 逐键对齐）。**未交付（子片 2+）**：分组视图（工作区分组 / 平铺）切换、跨组移动与 Host 写回（先本地乐观、失败可见可恢复）、组内展开 / 折叠（Show N more）、空白新会话在获得首条消息后自动提升 | 提交 `10627d34`（18 文件，+1734/−6；其中 2 个与并行工作流（右栏合并 / P2-1B 技能面）共享的双语词典按「HEAD + 本批 hunk」入索引——`git show 10627d34 -- <词典>` 仅含 `sessionOrder` 9 行新增，并行工作流的在飞改动未混入）。主树复测：`npm run build`（`tsc -b && vite build`）exit 0、定向 vitest **4 文件 / 51 用例全绿**、定向 e2e `npx playwright test e2e/session-order.spec.ts` **3 passed**（控件默认最近更新 + 切手动后行可拖且偏好刷新保留 / 手动模式拖拽重排落账目 + 播报 + 刷新保持 / 切回最近更新不读账目但账目保留）、全量 `npm run test:e2e` **67 passed**。**提交态独立核验**（临时 worktree `ai-agent-runtime-v14` 检出 `10627d34`、`git status` 干净、`frontend/node_modules` junction 指回主树后重跑）：`npm run lint` **0 error / 3 基线 warning**（i18n scanned=631 / violations=0、备份门禁 920 文件 0 残留、行数门禁 874 个 `.ts/.tsx` 中 0 个 > 500，最大 `src/lib/trajectory/timeline-window.ts`=494）、`npm test` **173 文件 / 1255 用例全绿**、`npm run build` exit 0、全量 e2e **67 passed** |

### 批次 16（2026-09-13）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-6-子片2（会话分组视图 + 跨组移动写回） | **P2-6 第二个子片：把会话列表从「组内组织」推进到「分组视图（按目录 / 平铺）切换 + 跨组移动与 Host 写回」**（后端零改动、纯前端；分组口径按工作目录为批次 3 / P2-1A 既有能力，本批**不改分组判定、不新建第二套顺序账目**）。① `lib/workspace/session-grouping.ts`——分组视图纯口径（`SessionGroupingMode` = `directory \| flat`；`flattenSessionDirectoryGroups` 按「分组顺序 → 组内顺序」拼接平铺序列，两视图共用同一份组内账目，切换不让手动顺序分叉）；② `hooks/workspace/use-session-grouping.ts`——视图走设置域 `workspace.sessionGrouping`（渲染期只读、切换即写入；兜 `isSessionGroupingMode` 防脏存储透传）；③ `hooks/workspace/use-session-group-view.ts`——跨组移动编排（先本地乐观覆盖 `sessionId → 目标路径`；Host 快照已反映目标路径 / 会话消失时在渲染期归约回收覆盖，不在 effect 里 setState；失败即撤销覆盖并就地提示）；④ `components/workspace/workspace-sidebar/session-grouping-control.tsx`——`role="group"` + `aria-pressed` 双档控件（与排序控件同形，两个选项都是真实行为、无占位项）；⑤ `use-session-drag-reorder.ts`——跨组落点（目标目录组头）接管：写回 `PATCH /api/runtime/sessions/:id` 的 `metadata.context.workspace_path`（用注册表原始路径；组头 title 仍为规范化路径），失败回滚原组、不写假落点；⑥ 下沉 `session-row-view-model.ts` / `session-user-menu.ts`（各自保持 < 500 行、行为不变）并接线 `workspace-sidebar` / `sessions-section` / `workspace-shell` / `workspace-page`（`onMoveRuntimeSession` 自页面透传；平铺视图不渲染目录组头——跨组移动不可表达），`workspace-sidebar-shared.ts` 把乐观覆盖的套用 / 回收做成纯函数（无覆盖返回入参同一引用）；⑦ 双语 `workspace.base.sidebar.sessionGrouping.*` 6 键 + `sessionMove.*` 2 键逐键对齐 + 设置域归一化（未知取值回落默认，不做隐式映射）。**未交付（子片 3+）**：组内展开 / 折叠（Show N more）、空白新会话在获得首条消息后自动提升 | 提交 `8520de33`（27 文件，+2045/−218；其中 4 个与并行工作流（P2-7 消息渲染 / 轨迹 / 右栏合并）共享的文件按「HEAD + 本批 hunk」入索引，并行工作流的在飞改动未混入）。**提交态独立核验**（临时 worktree `ai-agent-runtime-v16` 检出 `8520de33`、`git status` 干净、`frontend/node_modules` 以 junction 指回主树后重跑）：`npm run lint` **0 error / 3 基线 warning**（i18n scanned=637 / violations=0、备份门禁 931 文件 0 残留、行数门禁 883 个 `.ts/.tsx` 中 0 个 > 500，最大 `src/lib/trajectory/timeline-window.ts`=494）；`npm test` **176 文件 / 1277 用例全绿**（本批新增 3 个单测文件 / 14 用例：`session-grouping.test.ts` 3、`workspace-sidebar-cross-group-move.test.tsx` 2、`workspace-sidebar-shared.test.ts` 9）；`npm run build`（`tsc -b && vite build`）exit 0（`✓ built in 2.06s`）；全量 `npm run test:e2e` **70 passed**（新增 `e2e/session-grouping.spec.ts` 3 用例：拖到目标组头即乐观归属并写回 Host + 刷新仍在目标组 / Host 拒绝写回即回滚原组并就地提示、刷新后不产生假落点 / 切平铺不渲染组头且不丢会话、切回按目录恢复） |

### 批次 17（2026-09-14）

| 项 | 动作 | 验证 |
|---|---|---|
| P2-6-子片3（组内展开 / 折叠 + 空白会话提升；P2-6 收口） | **P2-6 第三个子片：把会话列表从「分组视图」推进到「组内渐进呈现 + 空白新会话自动提升」**（后端零改动、纯前端；不改分组判定、不新建第二套顺序账目、渲染期零写存储）。① `lib/workspace/session-grouping.ts`——新增 `SESSION_GROUP_VISIBLE_LIMIT`（5，组内渐进呈现的默认可见上限）与 `resolveSessionGroupVisibility`（纯判定：不超上限的组整体可见且不给控件；折叠态只渲染前 `limit` 个并给出「展开其余 N 个会话」；`pinnedId`（当前选中会话）落在折叠区时自动展开——不出现「已选中却看不见」的假态；只影响呈现，不改会话顺序，也不读写任何排序账目）；② `lib/workspace/session-order.ts`——新增 `SessionBlankCandidate` / `isBlankSession`（只认权威消息计数 `metadata.totalTurns === 0`；字段缺失 / 非数字 / 其他值一律按非空白处理——宁可不提升，也不猜）/ `promoteBlankSessions`（把尚无任何消息的新会话钉在分组顶部且彼此保持传入顺序，其余会话相对顺序不变；`anchoredIds`（该组手动顺序账目里出现过的 id）不参与提升——显式意图优先于呈现层；无空白会话时返回入参同一引用）；③ 新增 `components/workspace/workspace-sidebar/use-session-group-visibility.ts`（展开态是会话期本地状态，渲染期零写存储，展开键按目录路径记忆）+ `session-group-toggle.tsx`（真实按钮：`aria-expanded` +「展开其余 N 个会话」/「收起」，`data-testid` 供 e2e 定位）；④ `hooks/workspace/use-session-group-view.ts`——组内顺序在排序结果之上套一层空白提升（`anchoredIds` 取该组手动账目）；⑤ `sessions-section.tsx`——组内渲染改走 `groupVisibility.visible`，当前选中会话作为 `pinnedId` 传入；⑥ 双语 `workspace.base.sidebar.sessionGrouping.collapse` / `showMore` 逐键对齐（与并行工作流共享的双语词典按「HEAD + 本批 hunk」入索引，在飞改动未混入）。**P2-6 收口**：3-1 / 3-2 / 3-3（批次 15）、分组视图 + 跨组移动与 Host 写回（批次 16）、3-4 组内展开折叠 + 3-18 空白会话提升（本批）、3-5 空态三态（批次 3）全部落地，P2-6 项下无未交付子项。 | 提交 `f2c9cf98`（11 文件，+589/−4；其中 2 个与并行工作流共享的双语词典按「HEAD + 本批 hunk」入索引，未把在飞改动带入）。主树复测：`npm run lint` **0 error / 3 基线 warning**（i18n scanned=647 / violations=0；备份门禁被并行工作流在飞残留 `frontend/e2e/.backups/support.ts.20260914-174146.bak` 挡住——非本批产物）；`npm test` **179 文件 / 1322 用例全绿**；`npm run build` exit 0（`✓ built in 1.59s`）；定向 e2e `npx playwright test e2e/session-collapse.spec.ts` **2 passed**（10.4s）。**提交态独立核验**（临时 worktree `ai-agent-runtime-v17` 检出到 `f2c9cf98`、`git status` 干净、`frontend/node_modules` 以 junction 指回主树后重跑）：`npm run lint` **0 error / 3 基线 warning**（i18n scanned=639 / violations=0、备份门禁 933 文件 0 残留、行数门禁 885 个 `.ts/.tsx` 中 0 个 > 500，最大 `workspace-sidebar/sessions-section.tsx`=497）；`npm test` **176 文件 / 1289 用例全绿**；`npm run build` exit 0（`✓ built in 1.42s`）；定向 e2e **2 passed**（10.5s） |

### 批次 18（2026-09-15）

| 项 | 动作 | 验证 |
|---|---|---|
| 实时工具行（工具帧实时建行 + live 桥接收敛） | **症状（实测）：ReAct 回合内工具行不逐条出现，全部帧挤在回合末尾。**根因两处：① 后端 live `streamSink`（`backend/internal/api/skills/handler.go`）只转发 text / reasoning / image，`chat.sse.tool_*` 全由回合结束的观测补发段产生（`buildObservedToolEventPayloads`，实测 21 帧集中在最后 ~18ms）；② 前端只认 `assistant_delta` / `assistant.reasoning` / `assistant.image_progress`，运行时 `tool_started` / `tool_finished`（含 `tool.requested` / `tool.completed` 点形态别名）整类丢弃（假定工具生命周期只经 `chat.sse.*`）。**后端**：① 新增 `backend/internal/api/skills/live_tool_stream.go`——`liveToolStreamTracker`（观测键 ↔ provider call id ↔ 行 id 映射）+ `subscribeLiveToolStream`（`RunReActWithSession` 期间订阅运行时事件总线，按 `streamMu` 串行发 `tool_call` / `tool_start` / `tool_end`，`defer` 退订）；② 收尾段改走 `buildObservedToolEventPayloadsWithLive`——已实时建行的工具**只补一条权威 `tool_end`**（`tool_call.id` 重写为实时行 id，arguments / output 就地补全），其余保持 legacy 三帧形态（不新增第二行）；③ 序号加固：优先取事件 `batch_index`（并行调度 `tool_parallel_scheduler.go:187`），缺席按 step 找空位；`rowIDForObservationKey(key, toolName)` 在逻辑工具名与观测工具名不一致时**拒绝复用**（退回 legacy 形态），避免并行乱序把输出并进错误的行。**前端**：④ 拆出 `frontend/src/lib/thread-state/events-live.ts`（打字机增量应用 + 流式消息定位 + `chat.sse.*` 桥接帧；`events.ts` 只留非 live 归约，为过 P0-2 ≤500 非空行门禁，纯机械搬迁）；⑤ `resolveBridgeToolPayload` 把运行时生命周期字段（`tool_call_id` / `logical_tool` / `arg_preview` / `summary`）补成 `tool` + `tool_call` 双载体，实时行与随后到达的 `chat.sse.tool_end` 按**真实 provider call id** 收敛成同一行；无 id 且工具名落到兜底名 `tool` 的残缺帧不落行。 | `pnpm exec tsc -b --force` exit 0；`pnpm test` **216 文件 / 1679 用例全绿**（新增 `chat-sse-bridge.test.ts` 6 例：实时建行并收尾推理段 / 收尾帧按同一 id 就地补全 / 重复帧幂等 / `error` → `status:"error"` / 无 id 无名的坏帧不落行；后端 `live_tool_stream_test.go` 新增并行乱序对齐、工具名不匹配拒绝复用 2 例）；`pnpm run lint` 0 error（1 基线 warning：`artifact-detail-dialog.tsx` 既有 hooks 依赖告警）+ 行数门禁 974 文件 0 超限（最大 `use-session-runtime-stream.ts`=500）+ i18n / token / 备份门禁全绿；后端 `go build ./...`、`go vet ./internal/api/skills/...`、`go test ./internal/api/skills/...`、`go test ./internal/toolprotocol/... ./internal/agent/...` 全绿，`gofmt -l` 对本批两个文件 0 命中。 |
| P2-9-子片4（composer 上方浮动「当前任务」Todo 面板） | **P2-9 第四个子片：把 agent 的 `todos` 快照接到 composer 卡正上方的浮动面板**（方案 `frontend-composer-floating-task-panel-plan.md`，实施记录见其 §11；后端一处 additive 契约放行 + 纯前端投影，**无新依赖 / 无新 SSE 通道 / 无新全局 store**）。**通道 A（实时）**：① `backend/internal/toolprotocol/result.go`——`Result.EventMapWithMetadataKeys(scopedKeys ...string)` 在既有 thin allowlist 之外按「单次结果」放行工具作用域键（trim 判空、缺席 / nil 跳过、**不并入共享白名单**，故同名键不会被其它工具顺带泄漏）；`eventMap(scopedKeys)` 收内部实现、`EventMap()` 委托 `eventMap(nil)`（既有语义零变化）；② `backend/internal/agent/tool_runtime_events.go`——`attachProtocolResultToPayload` 仅在工具名等价 `todos` 时把 `metadata["todos"]` 裁剪为 `protocol_result.metadata.todo_snapshot`（`{items:[{content,status,active_form}], session_id?, goal_id?}`；空 `content` / 未知 `status` **整条丢弃**、全丢则键不出现；**不改写生产侧 metadata**，与 `compact_reconciliation` 读取的 `metadata["todos"]` 命名显式分离）。**前端投影**：③ 新增 `frontend/src/lib/thread-state/todos.ts`——`deriveTodoSnapshotFromRuntimeEvent`（只认该键，无键即 `null`）、`deriveLatestTodosFromRuntimeEvents`（按 `seq` 取最新）、`mergeTodoSnapshot`（runtime 单调、历史不覆盖实时）、`applyTodoSnapshotToThread`（与 todo 无关的事件**原样返回入参引用**，不打断下游 memo）；`lib/thread-state/events.ts` 在 `applyRuntimeEventToThread` 内折叠调用，私有 `readTextDelta` / `readRawTextValue` 机械搬迁到新 `lib/thread-state/text-utils.ts`（语义不变，为过 ≤500 非空行门禁）；④ 新增 `hooks/workspace/use-session-todos.ts`（空列表不渲染 / 全部完成即隐藏、三态计数、当前项、折叠态）；⑤ **通道 B1 历史兜底**：`lib/thread-state/history-artifacts.ts` 从会话历史工具消息 metadata 反扫最近一次快照（刷新 / 会话切换后仍可恢复）。**UI 与挂载**：⑥ 新增 `components/workspace/task-panel/*`（默认折叠单行条；`aria-expanded` / `aria-controls` 对应列表 `id`、`aria-live="polite"` 进度摘要、状态不靠颜色单独表意；展开 `grid-rows-[1fr]↔[0fr]` + `motion-safe:`；`line-clamp-1` + `title` 全文；渲染上限 8 项 + 页脚；宽度取 dock 轴 `--app-chat-content-width-dock`；`data-testid="todo-panel"`）；⑦ `workspace-shell/main-section.tsx` 在 `chatSurfaceVisible` 分支、`PendingInteractionBar` **之前**挂 `<TodoPanel>`（复用既有 `absolute inset-x-0 bottom-0 z-30` 吸底 overlay，不新增定位体系、不动 composer 内部结构）；⑧ 双语 `panels.todos.*` 逐键对齐 + mock 类型补可选 `todoSnapshot`。**未交付（非阻塞）**：T4 `todos` 工具行摘要打磨留待独立排期。 | `npx tsc -b --force` exit 0；`npm run build`（`tsc -b && vite build`）exit 0；`npm run lint` **0 error / 1 基线 warning**（`artifact-detail-dialog.tsx:50` 既有 `react-hooks/exhaustive-deps`；i18n scanned=684 / violations=0、备份门禁 1035 文件 0 残留、行数门禁 973 个 `.ts/.tsx` 中 0 个 > 500（最大 `hooks/workspace/use-session-runtime-stream.ts`=500）、消息 token 门禁 37 文件 0 处）；`npm test` **216 文件 / 1669 用例全绿**（本批新增 30 例：`lib/thread-state/todos.test.ts` 17、`hooks/workspace/use-session-todos.test.tsx` 6、`components/workspace/task-panel/task-panel.test.tsx` 7）；后端 `go build ./...` / `go vet ./...` / `go test ./internal/agent/... ./internal/toolprotocol/... ./internal/toolkit/...` **全绿**（`agent` / `toolprotocol` / `toolkit` / `toolkit/tools` 四包 ok；含新增「`todos` 工具出现全量条目、**非** `todos` 工具不得出现该键、生产侧 metadata 不被改写」与 `EventMapWithMetadataKeys` opt-in 用例）；e2e 按方案 D3 **不新增用例**（面板数据依赖 live SSE 的 `tool.completed`，e2e 只吃静态 `dist/` 产物、无法稳定构造该事件，覆盖由组件测试 + 方案 §8.2 手工清单承担）。**基线既有失败登记（与本批无关）**：① 全量 `npm run test:e2e` 中 `live-delta.spec.ts:85` / `thread-link.spec.ts:52` / `thread-link.spec.ts:116` 三例在干净 HEAD `2d6dd61c` 独立 worktree + 独立 fresh build 上**同样失败**（同断言、同超时）；② `trajectory.spec.ts:71`（P2-1 搜索过滤）同口径在 HEAD 基线复现（`Expected: 0 / Received: 1`）；③ `workspace-chat.spec.ts:205`（P1-3a 阅读位置保持）为**时序敏感 flaky**——`.artifacts/e2e-failures/` 存有 2026-09-14 的多次失败留图（18:21 / 21:11 / 22:36 / 23:38 等，均早于本批），本批复跑 `--repeat-each=3` **3 passed**、干净 HEAD 单跑亦通过。三者均判定为基线既有问题，未纳入本批范围。 |

### 批次 19（2026-09-15）：运行时事件「交付通道」审查（分析批次，无代码改动）

**触发**：批次 18 修复实时工具行后，复核同类事件是否同病。结论：工具不是特例，缺陷在**交付通道**而非单个事件类型。

**四个交付决策点（各自独立白名单，互不校验，无一处上报丢弃）**

| # | 决策点 | 位置 | 作用域 | 未命中行为 |
|---|---|---|---|---|
| A | 总线 → 会话事件库落盘 | `backend/internal/api/skills/handler.go:3860-3882`（`shouldPersistRuntimeSessionEvent`） | `tool.requested` / `tool.completed` / `approval_*` / `checkpoint_created` / `context.profile.injected` / `recall.performed` / `agent.reclaimed` / 压缩四件套 / `session_*` / `context_reconciled` / `assistant_delta` / `assistant.reasoning` / `image_progress` | 不落盘 → 既无实时也无回放 |
| B | 总线 → live-only SSE 旁路 | `backend/internal/api/skills/session_runtime_stream.go:349-352`（`sessionLiveOnlyRuntimeEventTypes`） | 仅 `tool.progress` + `subagent.progress` | 不订阅 → 该类型永远无实时帧（且这两类**不落盘**，刷新即丢） |
| C | 总线 → chat SSE 帧桥 | `backend/internal/api/skills/live_tool_stream.go:298-345`（`subscribeLiveToolStream`） | 仅工具生命周期（批次 18 新增） | 无桥 → 只能等回合末尾巴 |
| D | 回合末尾巴帧 | `backend/internal/api/skills/handler.go:7971-7981` + `:1885-1890` | 工具（`buildObservedToolEventPayloadsWithLive`）、`subagent`、`observation`、`planning`、`orchestration`、`route` | — |

**关键结构性事实（本次新证）**：`/api/agent/runtime/stream` 是**事件库中介**——`store.ListEvents(afterSeq)` 长轮询（`session_runtime_stream.go:168`）+ `WatchEvents` 唤醒（`:109-110`），另加 B 作为直连旁路。因此 **A 的白名单实际决定了「前端能否实时看到某类事件」**；C 之所以必须作为独立订阅存在，正是因为工具生命周期事实在 A/B 两处都不完整。

**命名分裂（同批次 18 的 M3）的确切机制**：`mapRuntimeEventToSession`（`handler.go:3884-3893`）只改 `Type` 字段、payload 原样搬运——同一事实在三个词汇表里各有一个名字：总线 `tool.requested` / `tool.completed`；事件库 `tool_started` / `tool_finished`（`backend/internal/chat/events.go:18-19`）；chat SSE `chat.sse.tool_start` / `tool_end`。三处靠手写 switch 对齐，无共享常量。

**逐类裁定**

| 事件类 | 实时帧 | 落盘 / 回放 | 聊天行 | 裁定 |
|---|---|---|---|---|
| tool 生命周期 | ✅ C | ✅ A（`tool_finished`） | ✅ | 批次 18 已修（实时） |
| tool.progress | ✅ B（live-only） | ❌ 不落盘 | 更新既有行 | 刷新即丢（无回放映射） |
| subagent.batch.* | ❌ 无桥 | ❌ 不在 A | 端点补发帧 | 仅回合末尾巴（`handler.go:1894-1895`），实时性 = 0 |
| subagent.progress | ✅ B（live-only） | ❌ 不落盘 | 折叠为单行 `runtime-0` | 多子代理互相覆盖，`live` 恒 `running` |
| planning / orchestration / route / observation | ❌ | ❌ 不在 A | **不建聊天行** | 只 `pushTrajectory` + `attachTurnArtifact`（`frontend/src/hooks/workspace/agent-chat-turn/stream-handlers.ts:266/282/298/314/330`）→ 伤害面是轨迹页与 turn artifact，非消息区 |
| todos 快照 | ❌ 无帧 | ✅ 随 `tool_finished` payload 原样落盘 | 不建行（走面板） | **修正**：实时（通道 A）+ 历史兜底（通道 B1）均成立（`backend/internal/agent/tool_runtime_events.go:695-701`；`frontend/src/lib/thread-state/todos.ts:115-134`），不存在「两条通道都拿不到」 |
| approval / question | ❌ 无帧 | ✅ `approval_*` 在 A | 由快照重建 | 实时性 = 0；快照无 `toolCallId`，`approval_resolved` 缺 `request_id` 时退化为会话级匹配 |

**两处夸大表述的自我修正**
1. 轨迹页 `unknown-<seq>` 告警（`frontend/src/lib/trajectory/apply.ts:241-252`）是**死代码**：`KNOWN_TRAJECTORY_KINDS` 已在 `recovery.ts:71-87 / 198-200` 前置过滤 → 现有「未知事件」兜底不可达；thread-state 侧相反是**半透传**（`events.ts:43-53` 仍入事件数组与产物，只不建 UI 行）。两套子系统策略彼此背离。
2. 「静默丢弃」的主体不在 reducer，而在 A/B/C/D 四处白名单，加上 `frontend/src/api/runtime/sse.ts:316-317` 的 `default: return` 与 `deltas.ts:124-125 / 194-195` 的分类器 `null`。

**风险登记（子审计落地，含对批次 18 的回击点）**

| ID | 风险 | 证据 |
|---|---|---|
| b2 | 工具帧缺 `tool_call_id` → 兜底 id `tool-<seq>` 永不与 `tool:<id>` 合并 → 同一次调用分叉成两行（**直接命中批次 18 的收敛前提**） | `frontend/src/lib/trajectory/apply.ts:300` |
| a3+c4 | 历史工具结果缺 `tool_call_id` → 结果行消失；冻结只由 `done` / `error` 触发 → 该行**永久 running** | `frontend/src/lib/trajectory/session-history.ts:158-163`；`apply.ts:308` / `:38-41` |
| a4 | `tool.progress` 缺 call id → 静默丢弃 | `frontend/src/lib/trajectory/recovery.ts:310-313` |
| b1 | 缺 `_event.sequence` 的实时帧 seq=0 绕过幂等，随后由 `/runtime/events` 以真实 seq 重投 → 文本二次拼接（唯一缓解：delta key 去重） | `frontend/src/api/runtime/api.ts:37-41`；`frontend/src/hooks/workspace/use-trajectory-snapshot.ts:215-236` |
| b4 | `subagent.progress` 全部折叠到 `runtime-0`，多子代理互覆 | `frontend/src/lib/trajectory/recovery.ts:351-382`；`apply.ts:213/216` |
| b3 | 待交互快照无 `toolCallId`；`approval_resolved` 缺 `request_id` → 会话级匹配，可能结算错卡片 | `frontend/src/lib/trajectory/snapshot.ts:42-56`；`frontend/src/lib/thread-state/events.ts:351-360` |
| a5 | 审批卡片从快照重建，缺 id 时不建卡片且无告警 | `frontend/src/lib/trajectory/snapshot.ts:42-56` |
| c1 | live-only 事件无回放映射 → 刷新 / 重连即丢且不可回填 | `frontend/src/lib/trajectory/recovery.ts:436-441` |
| c2/c3 | `user` 行与历史兜底只在恢复链路存在，实时链路不调用 | `frontend/src/lib/trajectory/session-history.ts:186-199`；`frontend/src/hooks/workspace/use-trajectory-recovery.ts:200-211` |

**建议（未实施，待排期）**

| 优先级 | 项 | 内容 |
|---|---|---|
| P0-1 | 收敛前提守卫 | 为 b2 / a3+c4 补测试（id 缺失 ⇒ 分叉 / 悬挂），否则批次 18 的实时收敛在降级数据下会被无声推翻 |
| P0-2 | 白名单可观测 | A/B/C/D 未命中计入指标；或按 `backend/internal/runtimeobserve/known_types.go` 的三分法复用到 `/api/agent/chat`（该分法此前只覆盖 CLI）；同时把轨迹侧 `unknown-*` 兜底移到白名单之前（或删掉死代码） |
| P1-1 | 子代理实时化前置 | 先修 b4（单行折叠 + 恒 `running`），否则实时化只是把「迟到」换成「互相覆盖」 |
| P1-2 | 统一实体身份 | `entity:{kind,id}` 随帧下发；`tool-<seq>` 类兜底显式标 `degraded`，把「无法合并」变成可观测事件而非静默双行 |

### 批次 20（2026-09-15）：批次 19 四条建议落地（P0-1 / P0-2 / P1-1 / P1-2，实施批次）

**触发**：批次 19 交付四条建议（P0-1 收敛前提守卫、P0-2 白名单可观测、P1-1 子代理实时化前置、P1-2 统一实体身份）。本批按原优先级 1:1 实施，不扩范围；行身份类改动一律「先钉住现状为断言，再改行为」。

| 优先级 | 项 | 内容 | 验证 |
|---|---|---|---|
| P0-1 | 收敛前提守卫 | 新增 `frontend/src/lib/trajectory/trajectory-convergence.guard.test.ts`（5 例）：① 缺 call id 的工具帧退化为 `tool-<seq>`，与带 id 的权威帧**分叉成两行**且合成行永久 `running`；② 同一工具的两条缺 id 帧各自成行（不收敛）；③ 悬挂行只有回合末 `done` 会收尾为 completed（`freezeOpenItems` 只处理 error）；④ 历史工具结果缺 `tool_call_id` 时整帧丢弃（既无行也无终态）；⑤ 带 id 时产出 `tool_end`（对照组）。作用：批次 18 的实时收敛以真实 provider call id 为唯一合并键，这些断言把「降级数据下的必然分叉」钉成期望值——后续改动若假定「已经收敛」，会先在这里失败而不是在缺 id 的帧上无声失效。 | `npx vitest run src/lib/trajectory` 17 文件 / 170 用例全绿（含本批全部新增用例） |
| P0-2 | 白名单可观测（A/B/C/D 四通道） | **后端**：① 新增 `backend/internal/api/skills/runtime_event_delivery.go`——`DeliveryChannelsFor` 把「类型 → 通道」收敛成一处可测判定（`session_store` / `live_only` / `chat_bridge` / `tail_only`，可多通道），复用 `runtimeobserve` 已知类型目录区分「已知但被通道裁掉」与「完全未知」；② A 通道未命中按 `reason`（`no_session_id` / `type_not_persisted`）×类型×已知性三分计数（`dropped_known` / `dropped_unknown`，类型桶上界 64 + `_other` 溢出桶，输入为外部类型名必须有界），B 通道在**真正把帧写进订阅通道时**记 `live_forwarded`（该通道不落盘，这是「有没有实时送达」的唯一正向证据）；③ 读侧经 `handler.go` 的 `runtimeStatusSnapshot["runtime_event_delivery"]` 暴露（不新增端点、不改事件流）；④ `handler.go` 落盘白名单抽成 `isPersistedRuntimeEventType`，与分类函数共用同一份清单（测试断言两者一致，防两处漂移）；⑤ `runtimeobserve/known_types.go` 补入两类 live-only 总线类型（`tool.progress` / `subagent.progress`）——它们此前不在任何清单里，会让三分法把「已知但被裁掉」误记成「完全未知」。**前端**：⑥ 删掉 `recovery.ts` 的 `KNOWN_TRAJECTORY_KINDS` 前置白名单（未命中即 `return null`，把 `apply.ts` 的 `unknown-<seq>` 兜底变成死代码）——未知 kind 现在进入 reducer 兜底，生成可见的降级 system 行，类型漂移从「没有反应」升级为可观测事件；chat 路径当前发出的名字全部在 reducer 已映射集合内，故本改动不新增任何行。 | `go test ./internal/api/skills/ -count=1` ok（16.7s，含 `runtime_event_delivery_test.go` 5 例：通道集合、与落盘白名单一致性、已知/未知三分计数、live 转发计数、计数有界且可 JSON 序列化）；`go test ./internal/runtimeobserve/...` ok |
| P1-1 | 子代理实时化前置（风险 b4） | ① 新增 `frontend/src/lib/trajectory/entity-identity.ts`（身份契约唯一拼装点）；② `recovery.ts` 的 `subagentProgressEventToTrajectoryPush` 显式下发 `_trajectory_item_id = subagent:<child_session_id>`——修复前同一父流的所有镜像共用 `runtime-0`，多子代理互相覆盖；③ `apply.ts` 的 subagent 行身份改用 `subagentRowIdOf(payload, seq)`（无子会话标识才退回 `subagent-<seq>`）且状态不再恒 `running`：尾巴帧的 `success` 布尔是唯一权威终态（`completed` / `failed`），live 镜像无该键 ⇒ 保持 `running`（终态行会被 `upsertItem` 冻结，镜像无法就地更新）。 | 新增 `trajectory-entity-identity.test.ts`（5 例：两个子代理各占一行互不覆盖 / 同一子代理后续镜像就地更新不新增行 / entity 决定工具行身份 ×3）；`recovery.test.ts` 增两条身份断言；`npx vitest run src/lib/trajectory` 全绿 |
| P1-2 | 统一实体身份 | ① 契约：`entity: {kind, id, degraded?}`，`<kind>:<id>` 唯一拼装点收敛到 `entity-identity.ts`（`entityItemId` / `toolItemId` / `subagentItemId` / `readTrajectoryEntity`）；② 前端读侧：`apply.ts` 的工具行身份优先取 `entity.id`（后端指定的稳定名），退回 `tool_call_id`，再退回 `tool-<seq>`，并在**任一兜底路径**上把 `degraded: true` 写进工具行 head（`TrajectoryHead` 新增 `degraded?: boolean`，`headsEqual` 纳入比较 ⇒ 标记变化触发快照更新）；③ 后端写侧：`live_tool_stream.go` 新增 `attachToolEntity`（有真实 provider call id ⇒ `entity:{kind:"tool",id:<call_id>}`；缺 id 时**不下发**，交给前端标 degraded）与 `attachDegradedToolEntity`（观测合成身份 `observation_<step>` ⇒ `degraded:true`）；`handler.go` 回合末尾巴按分支调用——实时匹配过的工具补一条**权威** `tool_end`，退回三段式的三帧全标 degraded。于是「同一次调用两行」在数据上可见，而不是等用户发现重复行。 | 新增 `entity-identity.test.ts`（7 例：非法输入一律 null 不猜身份 / degraded 原样透传 / 子会话 id 优先级 / 状态非布尔不当终态 …）+ `trajectory-entity-identity.test.ts`（3 例 entity 契约）+ Go 3 例（实时帧带权威 entity / 缺 id 不下发 entity / 尾巴两分支身份对比）；`go test ./internal/api/skills/ -count=1` ok |

**未交付（非阻塞，独立排期）**：轨迹 UI 尚未把工具行的 `degraded` 呈现为可见徽标——当前可观测面 = 投影数据（head 标记）+ 导出 + 后端计数（`runtime_event_delivery`）。UI 提示需要新增 i18n 键与轨迹面板用例，按批次 18「T4 打磨留待独立排期」的同等口径登记。

**门禁（本批）**：`npx tsc -b --force` exit 0；`npm run build`（`tsc -b && vite build`）exit 0；`npm test` **219 文件 / 1700 用例全绿**（本批新增 25 例——前端 17：收敛守卫 5 + 身份契约 7 + entity 帧契约 5；后端 8：交付通道 5 + entity 帧 3；另改写 2 处既有断言：未知 kind 放行、未知 kind 兜底行身份，并在 `recovery.test.ts` 补 2 条身份断言）；`npm run lint` **0 error / 1 基线 warning**（`artifact-detail-dialog.tsx:50` 既有 hooks 依赖告警）+ i18n scanned=686 / violations=0 + 备份门禁 1040 文件 0 残留 + 行数门禁 978 文件 0 超限（最大 `use-session-runtime-stream.ts`=500）+ 消息 token 门禁 37 文件 0 处；后端 `go build ./...` exit 0、`go vet ./internal/api/skills/... ./internal/runtimeobserve/...` 0、`go test ./internal/api/skills/ -count=1` ok、`go test ./internal/runtimeobserve/...` ok、`gofmt -l` 对本批 3 个 Go 文件 0 命中。

### 批次 21（2026-09-15）：子代理「运行中」误报收口（身份状态 × 执行容器运行态正交化）

**症状（用户实测上报）**：`/workspace/sessions/session_20260915163832_x9tdlxUk` 的子代理面板里，两个 `researcher` 在跑完后仍长期显示「运行状态」。

**根因（实测）**：后端身份行（AgentControl registry record）**只有在显式 `close` / `reclaim` 时才进终态**；子代理跑完一轮后其 session 行仍是 open ⇒ 身份行长期 `active`。前端当时只按身份 `status` 分区（`active` 一律算运行中），于是「容器已结束」被渲染成「运行中」。结论：这**不是主代理忘了关**，也不该由前端臆断「已结束」。

**修复：补一条正交证据，而不是改身份语义**（`runtime_state` 为 additive 字段，身份行与既有 close / resume 口径零变化）

| 层 | 内容 |
|---|---|
| 后端取值链 | 新增 `backend/internal/api/skills/agent_control_runtime_state.go`：`runtime_state` = 活体 actor（`getSessionHub()` / session runtime store）→ 持久运行态（`session_runtime_state` 表）→ **null（未知，不下断言）**；`agent_control_agent_handlers.go` 列表 handler 逐行追加该字段 |
| 前端契约 | `types/runtime/agents.ts` 新增 `RuntimeAgentRuntimeState = running \| idle \| stopped \| unknown` 与 `RuntimeAgentDisplayStatus = RuntimeAgentStatus \| "ended"`；`api/runtime/agents.ts` 新增 `normalizeRuntimeAgentRuntimeState`（只认三值，trim + lowercase，缺省 / 异名 → `unknown`，**不得据此推断「已结束」**） |
| 前端派生 | `session-agents-panel-shared.ts` 新增 `agentDisplayStatus`：身份非 `active` → 原样透传（显式 `closed` 比 `ended` 更具体，优先）；root 行不收敛（轮次之间的 idle 不代表主代理结束）；子代理 `idle` / `stopped` → `ended`；`unknown` → 回退身份状态。`splitSessionAgents` 改按**展示状态**分区；`session-agents-tree.tsx` / `session-agents-panel.tsx` 的徽章、`data-status`、离线文案改用展示状态 |
| 动作授权不变 | `canStopAgent` / `canResumeAgent` 仍按**身份状态**（子代理跑完但身份仍 `active` 时仍可显式「停止」；只有 `closed` 可 Resume） |
| i18n | 双语 `panels.agents.status.ended`（已结束 / Ended） |

**真机证据（本轮，非仅单测）**
1. `GET /api/runtime/agent-control/agents?root_session_id=session_20260915163832_x9tdlxUk&include_closed=true` 实测 5 行全部带 `runtime_state:"idle"`；
2. 用该**真实响应**喂真实前端代码的临时实测脚本（`frontend/src/live-agent-display.check.test.ts`，跑完即删）实测：root（active+idle）→ `active`；两个 researcher（active+idle）→ `ended`；verifier（active+idle）→ `ended`；verifier（closed+idle）→ `closed`；`splitSessionAgents` 把后三者归入 `settled`；
3. 存储侧独立佐证（只读查询）：`backend/data/runtime/session_runtime.sqlite` 中相关 5 个会话 `session_runtime_state.status='idle'`、`current_turn_id=NULL`、`session_actor_leases` 无租约、每个会话均有 `session_end` 事件。

**门禁（本批）**：后端 `go build ./...` exit 0、`go test ./internal/api/skills/ -count=1` ok（15.7s，含新增 3 例：`idle` / `running` / 无数据源不下断言）；前端 `npx tsc -b --pretty false` exit 0、`npm run lint` **0 error / 1 基线 warning**（`artifact-detail-dialog.tsx:50` 既有 hooks 依赖告警）+ i18n scanned=686 / violations=0、`npx vitest run` **219 文件 / 1706 用例全绿**（本批新增 9 例：`agentDisplayStatus` 4 + `splitSessionAgents` 1 + 归一化 3 + 列表字段 1；改写 4 个测试夹具补 `runtimeState` 必填字段）。

**未交付（非阻塞，独立排期）**：本批只改**展示与分区**，不写状态、不做自动回收——身份行仍只有显式 close / reclaim 才终态。若要让已结束子代理的身份行也收敛，需要主代理主动 close 或后端增加空闲回收策略；那属于 AgentControl 生命周期语义变更，不在本批范围。

### 复检记录（2026-09-13，P2-1A 第八项交付后）

| 项 | 复检内容 | 结果 |
|---|---|---|
| A1 / E1 | `node scripts/verify-no-backups.mjs` | OK（扫描 870 文件，0 处残留） |
| A2 | `frontend/src` 内 > 500 行文件 | 复检时回退 2 个（`pages/usage-analytics/quota.tsx`=559、`pages/workspace-page.tsx`=512）→ **已处置（批次 8）**：`quota.tsx` 559 → 429（+ `quota-shared.ts` 74 / `quota-atoms.tsx` 73）、`workspace-page.tsx` 512 → 437（+ `hooks/workspace/use-workspace-session-actions.ts` 133）；新增 `scripts/verify-max-lines.mjs` 门禁（非空行 ≤ 500、i18n 词典豁免、负路径已验证 exit 1）并入 `npm run lint`；复测 **0 个**（最大 `workspace-sidebar.tsx`=483） |
| A3 | `[var(--…)]` 任意值 / `@theme` 映射 | 22 行 / 14 文件；映射 89 条（与登记值一致，无漂移） |
| B1 | queue / steering 符号（`resolveSubmitMode` / `busyEnter` / `steeringAvailable` / `updateQueue`） | 全零命中，仍未实施（依赖后端） |
| B5 | P2 零实施依据（axe / golden / 虚拟化 / Files changed / goal 指示 / fs 写入 / 右栏面板数） | 复核成立；**文字漂移**：依据句中「侧栏无归档/拖拽」应为「无分组 / 排序 / 拖拽」（归档 / 归档恢复已由批次 3 交付，已回填） |
| C1 | `@` 引用分组 | 仅 `files` 组（`main-section.tsx:161-167`，注释已披露会话 / 子代理分组待数据源） |
| C2 | 余 9 行仍未接 | 复核成立：`src/api/runtime` 无 upload / artifacts / goal / mcp / deliverables；无 `fs/write-file\|append-file` 消费；`top_p` 仅见于配置编辑器域；`currency` 仅见 `siteaccount` 展示单位（非用量货币成本） |
| C3 | `/` 命令内置执行器 | 仍无（机制与 `no-executor` 提示在位，缺 export / feedback / rename 执行器） |
| E2 | 动态导入告警 | 仍在（设计口径，非缺口） |
| E3 | 视觉 golden / 性能基准 | `toHaveScreenshot` / axe 全零命中，仍未建 |

> 上表除 A2 外的复检项均为「复核成立 / 保持开放」；A2 的复检回退已于当日处置完毕（批次 8），门禁四件套复测：`lint` 0 error / 3 基线 warning、`test` 156 文件 / 1026 用例全绿、`build` exit 0、`test:e2e` 57 passed。

## 4. 复现命令

```powershell
cd frontend
pnpm install --frozen-lockfile   # 包管理器以 pnpm-lock.yaml 为准（见 docs/development-guidelines.md §前端）
pnpm verify:clean      # P0-1 备份卫生门禁
pnpm verify:lines      # 新增：P0-2 行数门禁（frontend/src 内非空行 > 500 即失败；i18n 词典豁免）
pnpm lint              # eslint + i18n 扫描 + 备份门禁 + 行数门禁
pnpm test              # vitest（156 文件 / 1026 用例，2026-09-13 批次 7 / 8 口径实测）
pnpm test:e2e          # playwright（57 用例，含 B2 断线恢复、B3 会话 Fork/删除、P2-1A Jobs / usage / fs 预览 / 搜索 / stats / skills）
pnpm build             # tsc -b + vite build
```

> 2026-09-13 复核注记：本地若出现 `'vitest' 不是内部或外部命令` 或 `Cannot find module '@rolldown/binding-win32-x64-msvc'`，是 `frontend/node_modules` 安装不完整（缺 `.bin` 与平台原生包），
> 执行 `pnpm install --frozen-lockfile` 修复后上述命令即可复跑；不要改用 `npm install`（会改写 lockfile 形态）。
>
> **e2e 顺序约束（2026-09-13 批次 9 实测踩坑）**：`pnpm test:e2e` 只跑 `dist/` 构建产物（`playwright.config.ts` 的 `requireDist()` 仅校验 `dist/index.html` 存在，webServer 起 `vite preview`），因此**改了 `frontend/src` 后必须先 `pnpm build` 再 `pnpm test:e2e`**；否则用例静默跑在旧产物上（本轮 P2-9 状态条断言即因此首跑失败，`pnpm build` 后复跑通过）。

## 5. 备注：仓库级备份残留（范围外）

复核时全仓（不含 node_modules/.git/dist）共 **679 个 `.bak`**，本次仅清理了 `frontend/` 的 72 个；剩余 607 个分布在各域（`backend/cmd/aicli/commands/.backups` 162、`docs/plan/.backups` 134、`backend/.../web/.backups` 75、其余后端/文档目录等），均未被 git 跟踪、被根 `.gitignore` 的 `.backups/` 覆盖。建议各域参照 `verify-no-backups.mjs` 自行清理与加门禁（本计划范围不强制）。
