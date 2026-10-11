# 自动化任务（Schedule）：前端业务链条完整性分析

> 配套文档：[`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md)、[`completeness-review-and-integration.md`](./completeness-review-and-integration.md)、[`data-storage-design.md`](./data-storage-design.md)
> 问题：前端整个业务链条是否完整？保存、查看、创建等业务场景是否完善？
> 结论：**原方案只覆盖了"查看/删除"两条链路**；"创建/保存/历史/搜索/实时/归档联动"需按下表补齐，四端各有分工。
>
> **修订记录（2026-10-11）**：已按 [`review-of-design-docs.md`](./review-of-design-docs.md) 修订——保存链路的冲突重拉（F5 相关）、面板注册联合类型/`tone`/`widthClass`（F15）、micro web 静态不变量（F13）、UTC/本地展示边界（F17）。

---

## 1. 业务场景清单（14 项）

| # | 场景 | 说明 |
|---|---|---|
| S1 | **创建** | 模型经 `schedule_create` 创建；UI 提供"新建"引导（打开会话，不在 UI 内建表单） |
| S2 | **列表查看（全局）** | 跨会话任务列表（不激活会话） |
| S3 | **列表查看（会话内）** | 当前会话的活动任务 |
| S4 | **详情查看** | 规则 / 状态 / 下次运行 / 关联会话 |
| S5 | **编辑保存** | 名称、指令、运行时间；草稿 + 保存 + 冲突处理 |
| S6 | **删除** | 确认 + 硬删除 + 结果提示 |
| S7 | **运行记录** | 已保存发送记录分页 |
| S8 | **搜索/筛选** | 名称/指令/sessionId 搜索；全部/已开启/未运行筛选 |
| S9 | **会话关联跳转** | 打开原会话（可用性检查） |
| S10 | **实时刷新** | `schedule.changed` + 连接重置 |
| S11 | **状态机** | 加载/空态/错误态/重试 |
| S12 | **归档联动** | 活动任务阻止归档；停止归档删除任务 |
| S13 | **时间展示** | 设备时区下次运行时间 + 相对时长；规则原文时区 |
| S14 | **权限/禁用** | 子 Agent 会话、已归档会话、无会话时的禁用与原因 |

---

## 2. 四端覆盖矩阵（现状 → 目标）

| 场景 | E1 frontend (React) | E2 micro web | E3 aicli TUI | E4 gateway console |
|---|---|---|---|---|
| S1 创建 | ❌ 需"新建"引导（跳转 `/workspace/chats/new`） | ❌ 需引导（聚焦输入框） | ❌ 需 `/schedule new` 提示 | ➖ 可选 |
| S2 全局列表 | ❌ 需新增页 `/runtime/schedule` | ❌ 需新页签 | ❌ 需 `/schedule list` | ➖ 可选 |
| S3 会话内列表 | ❌ 需面板 | ➖ 可选 | ✅ 状态条计数 | ➖ |
| S4 详情 | ❌ 需面板详情 | ❌ 需详情区 | ❌ 需 `/schedule show <id>` | ➖ |
| S5 编辑保存 | ❌ 需表单（含冲突） | ➖ 可选 | ➖ 只读或命令式 | ➖ |
| S6 删除 | ❌ 需确认 + toast | ❌ 需确认 | ❌ 需 `/schedule delete <id>` | ➖ |
| S7 运行记录 | ❌ 需分页页签 | ➖ | ➖ | ➖ |
| S8 搜索/筛选 | ❌ 需列表头 | ➖ | ➖ | ➖ |
| S9 会话跳转 | ❌ 需按钮 | ❌ 需按钮 | ✅ 命令天然 | ➖ |
| S10 实时刷新 | ❌ 需事件订阅 | ❌ 需 SSE 订阅 | ✅ 事件驱动重绘 | ➖ |
| S11 状态机 | ❌ 需完整 | ❌ 需完整 | ⚠️ 简化 | ➖ |
| S12 归档联动 | ❌ 需禁用态 | ❌ 需禁用态 | ✅ 服务层保证 | ➖ |
| S13 时间展示 | ❌ 需格式化 | ❌ 需格式化 | ❌ 需格式化 | ➖ |
| S14 权限/禁用 | ❌ 需禁用原因 | ❌ 需禁用原因 | ⚠️ | ➖ |

> ✅ 已有 / ⚠️ 部分 / ❌ 待建 / ➖ 非目标。
> **判定：前端业务链条当前为空，需从零建立；本方案给出完整清单与分工，避免只做"列表 + 删除"两条链路。**

---

## 3. 推荐分工（按端定位）

| 端 | 定位 | 必须覆盖 | 可选 |
|---|---|---|---|
| **E1 frontend** | 完整管理面（主入口） | S1–S14 | — |
| **E2 micro web** | 会话内轻量面 | S1/S3/S4/S6/S10/S13 | S2/S5/S7/S8 |
| **E3 aicli TUI** | 终端命令面 | S1/S3/S6/S9/S10 | S4/S13 |
| **E4 gateway console** | 运维只读（可选） | S2/S4 | S6 |

**原则**：S1 创建**始终由模型完成**（UI 只引导），避免三端各写一套创建表单并产生校验口径漂移。

---

## 4. 关键链路的业务闭环设计

### 4.1 创建链路（S1）

```
用户点「新建」→ 打开新会话（E1: /workspace/chats/new；E2: 聚焦 #prompt；E3: 提示输入）
  → 用户在会话里说「每天 9 点提醒我…」
  → 模型调 schedule_create(prompt,title,daily{...})
  → 工具返回规范视图（含 id）
  → 会话内渲染创建卡片（E1 turnTail / E2 工具行 / E3 工具行）
  → 列表实时刷新（schedule.changed）
```

**闭环要素**：模型创建成功 → 视图可见 → 列表刷新 → 可跳详情。**不需要** UI 内表单。

### 4.2 查看链路（S2/S3/S4/S7/S13）

- 全局列表：`GET /api/runtime/schedule/catalog`（活动 + 未运行，含 `sessionId`）。
- 会话内列表：`GET .../list?session_id=`（仅活动）。
- 详情：本地从 catalog 取；运行记录按需 `GET .../history?limit=&before=`。
- 时间展示：`scheduledAt`（UTC）→ 设备时区显示 + 相对时长；**规则原文的时区只在频率文本里显示**。

### 4.3 保存链路（S5，最容易做不完整）

本项目与 dsh 一致采用**乐观并发**，因此保存必须：

1. 进入编辑时**捕获完整 `expected: ScheduleRecord`**（不是部分字段）；
2. 表单维护**本地草稿**（名称/指令/运行时间），未点保存不改服务端；
3. 保存时发送 `{session_id, id, expected, change?, title?, prompt?}`；
4. 处理三类结果：
   - 成功 → 刷新 catalog；
   - `schedule_conflict` → 提示"任务已被其他操作更新"，**强制重拉 catalog 并重新捕获该任务的 `expected`**，保留用户的草稿供重试（不要静默覆盖或自动重提）；
   - `schedule_ended` / `schedule_not_found` → 提示并把该行标记为已结束/移除，刷新 catalog；
5. 保存中/失败期间**保留所显示的任务**，不被目录刷新丢弃。

> **并发语义来源**：`update` 是完整记录 compare-and-set（见主方案 §5/§8）。前端必须**在编辑开始时**捕获完整 `expected`，并在 `schedule_conflict` 后重新捕获，否则会反复冲突。

> **原方案缺口**：只写了"更新原地替换"，未定义前端草稿与冲突 UX。此处补齐。

### 4.4 删除链路（S6）

确认对话框 → `POST .../delete {session_id,id}` → 硬删除 → **应用级提示**（生命周期长于发起它的面板）→ 刷新。
删除**不撤回已入队消息**，需在提示/详情文案声明。

### 4.5 实时与状态（S10/S11）

- 订阅 `schedule.changed`（E1 SSE、E2 SSE、E3 事件总线）→ 失效并重拉；
- 连接重置后重拉；
- 列表状态机：`loading`（首次）/ `refreshing`（保留旧数据）/ `error`（保留最近已知 + 重试）/ `empty`（区分"从未有任务"与"筛选无匹配"）。

### 4.6 归档与权限（S12/S14）

- 有活动任务的会话：**阻止归档**，并解释原因（任务列表）。
- 选择"停止任务"归档 → 任务被删除，UI 刷新。
- 子 Agent 会话：不显示创建入口；已有任务仍可查看/删除。
- 已归档/无会话：详情中的"关联会话"入口禁用并给出原因。

---

## 5. 缺口清单与交付顺序

| 缺口 | 端 | 优先级 | 交付物 |
|---|---|---|---|
| G1 全局页 + 路由 | E1 | P3 | `pages/schedule-page.tsx` + `App.tsx` 一行 |
| G2 会话面板 + 注册 | E1 | P3 | `schedule-panel*.tsx` + `panel-registry.ts` 一行 |
| G3 API/hook/types/i18n | E1 | P3 | `api/runtime/schedule.ts`、`hooks/workspace/use-schedule-tasks.ts`、`types/runtime/schedule.ts`、`i18n/.../panels-schedule.ts` |
| G4 编辑草稿 + 冲突 UX | E1 | P3 | `schedule-panel-edit.tsx`（或面板内） |
| G5 运行记录分页 | E1 | P3 | `schedule-panel-history.tsx` |
| G6 状态条（顶栏计数） | E1 | P3 | `schedule-status-bar.tsx` |
| G7 micro web 页签 + 模块 | E2 | P4 | `js/schedule.js` + `index.html` + `ui.js`/`sse.js` + `verify-micro-web-schedule.mjs` |
| G8 TUI 命令 + 状态条 | E3 | P4 | `/schedule` 命令 + `statusbar` 接线 |
| G9 归档联动禁用态 | E1/E2 | P4 | 会话归档状态接入 |
| G10 gateway 只读面 | E4 | P6 | 可选 |

### 5.1 交付附带约束（评审 F13/F15）

- **E1 面板注册（F15）**：`panel-registry.ts` 的 `WorkspacePanelSurfaceId` 是**联合类型**，新增 `schedule` 必须同步该联合成员；`tone` 建议复用 `artifact`（无专属配色），`widthClass` 取 `content`（288px 自适应），`requiresSession: true` 并给 `disabledReasonKey`。首选"自包含面"（注册 `surface` + 模块作用域 `lazy()`），避免面板级 hook 双拉数据。
- **E2 micro web 静态不变量（F13）**：micro web 的验证脚本会对 `style.css` 做静态检查——**所有不带 fallback 的 `var(--token)` 必须已在主题块定义**（未定义会让整条声明在 computed-value 阶段失效）。`js/schedule.js` + 新增样式必须遵守，并参照 [`docs/aicli/web-testing.md`](../aicli/web-testing.md) 的写法新增 `scripts/verify-micro-web-schedule.mjs`（迷你 DOM + 静态契约）。
- **E2 面板位置**：`index.html` 的 `#tab-schedule` 放在既有页签序列中（建议紧邻 `#tab-config`），顶部菜单同步加"自动化任务"项（`data-menu-action`）。
- **时间边界（F17）**：四端一律"后端 UTC、本地展示"，不得在后端按浏览器时区解析。

---

## 6. 完整性判定

| 维度 | 原方案 | 补全后 |
|---|---|---|
| 创建 | ⚠️ 仅提"打开会话" | ✅ S1 链路闭环（引导 + 卡片 + 刷新） |
| 查看（列表/详情） | ⚠️ 仅全局页 | ✅ S2/S3/S4/S7/S13 四端分工 |
| 保存 | ❌ 未定义前端草稿/冲突 | ✅ S5 草稿 + `expected` + 三类结果 UX |
| 删除 | ✅ 有确认 | ✅ S6 确认 + 应用级提示 |
| 搜索/筛选 | ✅ 有 | ✅ S8 |
| 实时 | ⚠️ 提了事件 | ✅ S10 + 连接重置 |
| 状态机 | ❌ | ✅ S11 |
| 归档/权限 | ⚠️ 只提后端 | ✅ S12/S14 前端禁用态 |
| 四端 | ❌ 只提 Web | ✅ E1–E4 分工矩阵 |

**总体结论**：补全后前端业务链条**完整**；建议 **E1 先做全量（主方案 P3）**，E2/E3 做会话内最小闭环（P4），E4 可选（P6）。**创建永远走模型**，是保持三端口径一致的关键约束。
