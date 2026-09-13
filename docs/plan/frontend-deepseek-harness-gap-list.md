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
| A2 | P0-2 全局指标失效：> 500 行文件现为 3 个 | 验收回退 | 高 | 已登记，**待排期**（拆分） |
| A3 | P0-4 数值口径偏差（89 vs 87、22 vs 20） | 验收漂移 | 低 | **已登记**（复检备注） |
| B1 | P1-5 队列与 Steering 零实施 | 零交付 | 高 | 已登记（§6.2 状态行），**待排期** |
| B2 | P1-8 连接状态统一零新增交付 | 零交付 | 高 | 已登记，**待排期** |
| B3 | P1-9 会话列表状态与整理零实施 | 零交付 | 高 | 已登记，**待排期** |
| B4 | P1-10 全局错误边界与加载失败面零实施 | 零交付 | 高 | **处置中**（批次 1 启动实现） |
| B5 | P2 未启动 9 项（P2-2/3/4/5/6/8/9/10/11） | 零交付 | 中 | 已登记（§6.3 说明行），**待排期** |
| C1 | P1-4 `@` 引用仅 file 组（session/subagent 延后） | 部分交付 | 低 | 计划已披露，保持 |
| C2 | P2-1 后端能力接线：16 行未接、1 行部分 | 部分交付 | 中 | 已登记，**待排期** |
| C3 | P2-7 命令系统无内置执行器 | 部分交付 | 中 | 已登记，**待排期** |
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
| A2 | 计划行 379/381 验收「> 500 行文件数降至 0」。复核实测 3 个：`styles/globals.css`=1043、`components/workspace/message-markdown-streaming.ts`=525（P1-2 `c12e0cf4` 引入）、`components/workspace/trajectory/subagent-session-dialog.test.tsx`=516（`8c55411d`）。按计划 ts/tsx 口径为 2 个；M4 阈值（≤ 10）仍满足。另 §9.3 有陈旧行数漂移：event-readers 311（记 230）、primitives 224（记 287）、sessions.ts 186（记 141）、history-artifacts 306（记 296）、apply 333（记 330）——仍全部 < 500。 | 已登记；拆分待排期（候选：先拆 `message-markdown-streaming.ts` 与测试文件，`globals.css` 需按 token/基础/组件层拆分并保序验证） |
| A3 | `@theme` 映射实测 89 条（计划记 87）；残留 `[var(--…)]` 任意值实测 22 行/14 文件（记 20）；`landing.css` 变量声明 0 成立。 | 已登记（P0-4 复检备注） |

### B. 零交付类（有任务定义、无代码）

| ID | 计划位置 | 复核证据 | 处置 |
|---|---|---|---|
| B1 | §6.2 P1-5（行 466-470） | `resolveSubmitMode` / `busyEnter` / `updateQueue` / `steeringAvailable` 在 `frontend/src` 全零命中；"queue/队列" 命中均为无关的 provider 请求队列设置。依赖后端队列 API（未就绪）。 | §6.2 已补「未开始」状态行；待排期 |
| B2 | §6.2 P1-8（行 484-489） | 统一连接呈现件 / 顶栏与消息流尾状态条 / 直连 `/api/agent/chat` 恢复 / 手动重试与 last seq 拉齐——四项新交付零落地；仅有 §5.5 A1/A2 预登记既有资产（`use-session-runtime-stream.ts` 重连、logs 连接标签）。 | 同上；待排期 |
| B3 | §6.2 P1-9（行 491-496） | 侧栏行状态指示 / 归档与非破坏删除 / 归档恢复 / Fork / 行内时间浮层 / 空态区分均无；后端 `archive`/`activate`/`close`、`/sessions/stats` 已就绪但前端零消费。 | 同上；待排期 |
| B4 | §6.2 P1-10（行 498-502） | `ErrorBoundary`/`componentDidCatch`/chunk 重试零命中；`main.tsx:37` 仍 `document.getElementById("root")!`，`bootstrapDocumentSettings()`（`:17-35`）在挂载前无捕获。 | **批次 1 启动实现**（worktree 子任务，验收含单测与全门禁） |
| B5 | §6.3（行 504-611） | P2 共 11 项：0 项作为 P2 交付；P2-1、P2-7 部分（见 C2/C3）；P2-2/3/4/5/6/8/9/10/11 零实施（依据：a11y/axe 零命中、无视觉 golden、无虚拟化与基准、侧栏无归档/拖拽、无 Files changed 行、无 jobs/goal/子代理树面板、无 `fs/*` 消费、右栏硬编码两面板）。 | §6.3 已加「未开始」说明行；待 M3/M4 排期 |

### C. 部分交付类

| ID | 已交付 | 缺口 | 处置 |
|---|---|---|---|
| C1 | P1-4 草稿/附件/命令机制/引用菜单/combobox 语义齐备 | `@` 候选仅 file 组；session/subagent 组待数据源（计划行 464 已披露） | 保持（依赖数据源） |
| C2 | P2-1 A 层：approval `expired`（经 P1-7）、SSE replay（经 P1-8）已可用；usage 面板部分可用 | 其余 16 行未接：jobs、subagent 控制面、skills 市场/热重载、`/sessions/search\|stats`、`fs/read-file\|write-file`、deliverables 字段、plugin `config_schema`、`top_p`、goal 快照、`Last-Event-ID`、queue/steering、upload、MCP 目录、`/artifacts`、currency cost | 待排期 |
| C3 | P2-7 机制：registry/四分类/键盘 UI/dispatch/`no-executor` 提示 | 无内置命令与执行器（export/feedback/rename），宿主未接 `commands`/`onCommand` | 待排期（随后端/宿主接线） |

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
| B4 | 启动 P1-10 实现（worktree 隔离子任务：三层错误边界、chunk 重试退避、`#root`/启动失败可见面、启动完整性检查、错误走 logger、zh/en 双语文案、单测；验收=lint/test/build 全绿） | 子任务运行中，合入前由本会话复核 |

### 批次 2 建议（按优先级）

1. **B4 验收与合入**（错误边界 + 单测 + 门禁复跑；改 `main.tsx` 属高风险入口，需人工复核 diff）。
2. **A2 拆分**：先 `components/workspace/message-markdown-streaming.ts`（525）与 `trajectory/subagent-session-dialog.test.tsx`（516）；`styles/globals.css`（1043）单独排期（按 token/基础/组件层拆分，需 e2e 视觉校验保序）。
3. **B2 → B1 → B3**：连接状态统一（复用既有重连，风险最低）→ 队列与 Steering（依赖后端 API，需先确认接口就绪度）→ 会话列表状态与整理（后端已就绪，纯前端接线）。
4. **D2/E3**：P2 启动时建立 §6.3 状态登记；视觉回归/性能基准随 P2-4/P2-5。

## 4. 复现命令

```powershell
cd frontend
npm run verify:clean   # 新增：P0-1 备份卫生门禁
npm run lint           # eslint + i18n 扫描 + 备份门禁
npm run test           # vitest（121 文件 / 758 用例）
npm run test:e2e       # playwright（35 用例）
npm run build          # tsc -b + vite build
```

## 5. 备注：仓库级备份残留（范围外）

复核时全仓（不含 node_modules/.git/dist）共 **679 个 `.bak`**，本次仅清理了 `frontend/` 的 72 个；剩余 607 个分布在各域（`backend/cmd/aicli/commands/.backups` 162、`docs/plan/.backups` 134、`backend/.../web/.backups` 75、其余后端/文档目录等），均未被 git 跟踪、被根 `.gitignore` 的 `.backups/` 覆盖。建议各域参照 `verify-no-backups.mjs` 自行清理与加门禁（本计划范围不强制）。
