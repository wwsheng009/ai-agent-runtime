# 侧栏「工作目录 / 会话」分区合并方案（已落地）

- 日期：2026-09-15
- 状态：**已落地**（Phase 0/1/2 与 Phase 4 清理完成，Phase 3 保留为后续项；落地证据见 §10）
- 触发：`http://localhost:5193/workspace/sessions/session_20260913104748_vyUxmF7F` 左栏「工作目录」与「会话」内容重复
- 范围：**纯前端**（侧栏信息架构与渲染层）；后端接口、分组口径、归档语义零改动
- 上游背景：`docs/plan/workspace-directory-management-implementation-plan.md`（2026-09-10，引入「工作目录」分区与目录注册表）
- 相关台账：`frontend-deepseek-harness-optimization-plan.md`（P2-6 组内折叠 / 分组视图 / 跨组移动）、`frontend-session-branch-fork-plan.md`（谱系行）

---

## 0. 结论摘要

1. **两段同源**：`directories-section.tsx` 与 `sessions-section.tsx` 都从 `useSessionGroupView(...)` 的同一份输出取数，因此同一个运行时会话会在侧栏出现两行。这是重复的唯一根因，不是数据问题。
2. **能力互补**：目录段独有「目录注册表管理」（添加 / 重命名 / 移除 / 目录内新建会话 / 0 会话目录常驻 / 宿主缺失告警）；会话段独有「会话浏览能力」（用户切换 / 统计 / 排序双模式 / 分组切换 / 归档开关 / 组内折叠 Show N more / 行内菜单 / 相对时间 / 谱系 / 拖拽重排与跨组移动）。二者合并后能力应**零丢失**。
3. **目标形态**：合并为**一个以「工作目录」为骨架的会话浏览器**——目录是分组的载体（管理动作挂在组头），会话是叶子（唯一一行、完整行能力），会话浏览工具条整体迁入该段。
4. **硬约束**：仓库有「单文件非空行 ≤ 500」的 lint 门禁。两段现存 334 + 498 行，**直接拼接必然超标**，所以方案的第一阶段必须是抽件重构（会话行 / 组头 / 工具条），而不是把两份 JSX 合到一个文件。
5. **可分段交付**：Phase 0/1 无行为变化（可独立提交、可独立回滚），Phase 2 才是可见的合并；Phase 3 为可选能力补齐。

---

## 1. 现状分析

### 1.1 侧栏结构与数据流

分区顺序（`workspace-sidebar.tsx`）：`本地聊天(chats)` → `工作目录(directories)` → `会话(sessions)` → `运行时概览(runtime)`；`SidebarSectionId = "directories" | "chats" | "sessions" | "runtime"`（`workspace-sidebar/types.ts`）。

```
useSessionGroupView(directories, sessionVisibility.visible, runtimeSessions)
   ├─ mergedDirectoryGroups … 注册目录（含 0 会话）+ 派生目录 + 未归属(Unscoped)
   └─ sessionGroups        … mergedDirectoryGroups 过滤出「有会话」的组 → 排序 → 空白提升 → 谱系稳定
                │
     ┌──────────┴───────────┐
     ▼                      ▼
directories-section    sessions-section
（组头 + 目录管理 + 简化会话行） （工具条 + 组头 + 完整会话行）
```

两段已共享的状态与行为（合并的现成基础）：`openSections`、`openSessionDirectories`、`sessionThreadById`、`selectedThreadId`、`sessionVisibility`（归档过滤）、`use-sidebar-effects.ts`（自动展开首组/选中会话所在组、清理失效 key）、`session-row-view-model.ts`（行渲染口径）、`session-item.tsx`（行 UI 与菜单）。

### 1.2 两段能力清单（实测）

| 能力 | 工作目录（directories-section.tsx, 334 行） | 会话（sessions-section.tsx, 498 行） |
| --- | --- | --- |
| 数据口径 | `mergedDirectoryGroups`（含 0 会话的注册目录、派生目录、Unscoped） | `sessionGroups`（仅有会话的组，按排序规则重排） |
| 计数徽标 | 注册目录数 | 会话线程数 |
| 添加工作目录 | ✅ 段头 `+` → `WorkspaceDirectoryAddDialog`（路径 + 别名） | ✗ |
| 目录内新建会话 | ✅ 仅注册目录组头（hover） | ✗ |
| 重命名目录别名 | ✅ 仅注册目录组头（内联输入） | ✗ |
| 移除目录 | ✅ 仅注册目录组头（确认弹窗，含会话数提示） | ✗ |
| 宿主目录缺失告警 | ✅ 组头 `existsWarning` | ✗ |
| 0 会话目录常驻 | ✅ | ✗（只有空态） |
| 未登记派生目录展示 | ✅ 只读 | ✅ 只读（仅分组标题） |
| Unscoped 分组 | ✅ | ✅ |
| 组展开/折叠 | ✅（按 `openSessionDirectories`） | ✅（同源状态，两段各自渲染） |
| 会话用户选择 | ✗ | ✅ |
| 会话统计摘要 + 刷新 | ✗ | ✅ |
| 排序（最近更新 / 手动） | ✗ | ✅ |
| 组内拖拽重排（手动模式） | ✗ | ✅ |
| 跨组拖拽移动 + 乐观写回/回滚 + 错误条 | ✗ | ✅（落点 `data-testid="sidebar-session-group-drop"`） |
| 分组模式切换（按目录 / 平铺） | ✗ | ✅ |
| 显示已归档开关（+ 隐藏计数） | ✗ | ✅ |
| 组内折叠「展开其余 N 个」（上限 5，选中行自动展开） | ✗ | ✅ |
| 会话行：标题 / 选中态 / 状态图标 | ✅ | ✅ |
| 会话行：相对时间 / 归档徽标 / 谱系徽标与缩进 | ✗ | ✅ |
| 会话行：内联重命名 | ✅（`origin="directories"`） | ✅（`origin="sessions"`） |
| 会话行：操作菜单（重命名/Fork/归档/恢复/删除） | ✗ | ✅（`session.menu`） |
| 会话行：拖拽句柄 | ✗ | ✅ |
| 空态 | ✅（`directories.empty` + 组内 `emptySessions.default`） | ✅（默认 / 搜索 / 全归档三态） |
| 无障碍播报 | ✗ | ✅（`aria-live` + `sidebar-session-order-announcement`） |

i18n 命名空间同理分裂：目录段用 `sidebar.directories.*`、`sidebar.sessionDirectoryUnscoped`；会话段用 `sidebar.sections.sessions`、`sidebar.session.*`、`sidebar.sessionGrouping.*`、`sidebar.sessionOrder.*`、`sidebar.sessionMove.*`、`sidebar.emptySessions.*`、`sidebar.sessionUser*`、`sidebar.sessionUsersLoading`、`sidebar.runtimeStats.syncing`。

### 1.3 重复的代价（为什么必须合并）

1. **同一会话两行**：`frontend/e2e/sidebar-session-actions.spec.ts:24-39` 的注释即是证据——同一会话同时出现在「Directories → Unscoped sessions」与「Sessions」，操作菜单只在 Sessions 分区渲染，测试被迫按分区收窄定位。
2. **重命名双挂载抢焦点**：为规避两个内联编辑器同帧卸载，`workspace-sidebar.tsx:33` 引入 `SessionRenameTarget.origin`，并在 `:322-325` 注释「只把本分区发起的标下发给该分区」。合并后该机制整体可删。
3. **折叠态隐式联动**：`openSessionDirectories` 是共享状态，在一段折叠会在另一段同时折叠，用户难以理解。
4. **显示条件分叉**：`showSessionsSection` 与 `showDirectoriesSection` 两套判定（`workspace-sidebar.tsx:199-212`），空态/加载态行为不一致。
5. **测试与维护成本**：e2e 需要 `sectionByTitle(page, "Directories" | "Sessions")` 之类的分区定位（`sidebar-session-actions.spec.ts:42-46`），后续每个会话行用例都要写两遍或显式排除另一段。
6. **无障碍重复**：同一会话被朗读两次，树语义只挂在会话段。

---

## 2. 合并目标与不变量

**目标**

- G1 每个运行时会话在侧栏**只渲染一行**，且这一行具备两段现有能力的并集。
- G2 目录注册表管理（增/删/改名/目录内新建会话）在**所有分组模式下**都可触达。
- G3 会话浏览能力（用户/统计/排序/分组/归档/折叠/菜单/拖拽/谱系）全部保留。
- G4 分区从 4 个降为 3 个（本地聊天 / 工作目录 / 运行时概览），语义不再重叠。

**不变量（不得改变）**

- I1 后端零改动：分组键仍是 `metadata.context.workspace_path`，跨组移动仍走既有 PATCH 写回与乐观回滚链路。
- I2 排序账目（`use-session-order`）、分组账目（`use-session-grouping`）、归档可见性（`use-session-group-visibility`）语义不变。
- I3 折叠默认行为不变：首组 + 选中会话所在组自动展开（`use-sidebar-effects.ts`），失效 key 清理不变。
- I4 组内渐进呈现不变：`SESSION_GROUP_VISIBLE_LIMIT = 5` 与「选中行被隐藏时自动展开」。
- I5 既有 DOM 契约尽量保留：跨组落点 `data-testid="sidebar-session-group-drop"`、播报节点 `data-testid="sidebar-session-order-announcement"`、`role="tree/treeitem"` 语义。
- I6 「本地聊天」段与「运行时概览」段不在本次范围内。

---

## 3. 目标设计

### 3.1 信息架构

- 段 id：沿用 `directories`；标题沿用 `sidebar.sections.directories`（**工作目录**）；`sessions` 段整体删除。
- 段头（收起时唯一可见部分）：目录图标 + 「工作目录」+ 会话数徽标 + `+ 添加目录` + 折叠箭头。
- 段内工具条（展开时，来自现会话段）：用户选择 → 统计摘要 + 刷新 → 排序 / 分组 / 归档开关。
- 树体（`role="tree"`）：组头（目录）+ 会话行（叶子）。

### 3.2 线框（分组模式 = 按目录）

```
┌ 工作目录 (12)                          [+ 添加目录] ┐  ← 段头
│ 用户  [默认用户 ▾]                                  │  ← 工具条 R1（多用户；单用户折叠为一行）
│ 共 12 · 活跃 9 · 已归档 3                 [刷新]     │  ← 工具条 R2
│ [最近更新 ▾] [按目录 ▾] [已归档]                     │  ← 工具条 R3（排序 / 分组 / 归档）
│ ───────────────────────────────────────────────────  │
│ ▾ 📁 ai-agent-runtime   ⚠宿主缺失   3   ⟨新会话 重命名 移除⟩ │  ← 注册目录组头（hover 动作）
│      ● 会话 A                            10:24       │
│      ○ 会话 B                             昨天       │
│      ○ 会话 C                             3 天前      │
│      ⋯ 展开其余 2 个会话                             │  ← Show N more（>5）
│ ▸ 📁 docs-site                          1            │  ← 派生目录（未登记，只读；hover 可选「登记」）
│ ▸ 📁 未归属会话                          1            │  ← Unscoped（不可作为跨组落点）
└──────────────────────────────────────────────────────┘
```

分组模式 = 平铺时：隐藏组头，直接输出扁平会话列表；目录管理改由段头 `管理目录` 按钮承载（见 3.5-D）。

### 3.3 能力落位（合并后每个能力的归属）

| 能力 | 落位 | 说明 |
| --- | --- | --- |
| 添加目录 | 段头 `+` | 原样迁移（`WorkspaceDirectoryAddDialog`） |
| 目录内新建会话 | 注册目录组头 hover | 原样迁移 |
| 目录重命名 / 移除 | 注册目录组头 hover | 原样迁移 |
| 宿主缺失告警 | 组头徽标 | 原样迁移 |
| 0 会话目录常驻 | 组内空提示 | 复用 `emptySessions.default` |
| 未登记目录「登记为工作目录」 | 派生组头 hover | **新增（Phase 3，可裁剪）** |
| 用户选择 / 统计 / 排序 / 分组 / 归档 | 段内工具条 | 整体迁入 |
| 组展开折叠 / Show N more | 组头 / 组内 | 复用共享状态与上限 |
| 跨组拖拽移动 | 组头落点 | 复用 `sidebar-session-group-drop` |
| 会话行（时间/状态/谱系/归档徽标/重命名/菜单/拖拽） | 会话行 | 统一为**同一个**行组件 |
| 空态 | 树体 | 默认 / 搜索 / 全归档 + 「已登记但无会话」提示 |
| 播报 | 树体外 `aria-live` | 原样迁移 |

### 3.4 状态与数据归属（不变的部分尽量不动）

- 数据：`useSessionGroupView` 输出两路，合并后使用 `mergedDirectoryGroups`（含 0 会话组）+ 既有的排序结果 `sessionGroups`。**过滤与排序仍在 hook 内完成**，组件只做渲染，避免把排序逻辑搬进 JSX。
- 本地状态：`openSessionDirectories`（组折叠）、`openSections`（分区折叠，去掉 `sessions` 键）、`SessionRenameTarget`（**去掉 `origin`，退化为 `string | null`**）、`sidebarActionError`（合并为一份错误条）。
- 显示条件：`showSessionsSection` / `showDirectoriesSection` 合并为单一 `showWorkspaceSection`（两者条件的并集）。
- 拖拽/排序/分组/归档的 hooks 全部复用，不新增状态源。

### 3.5 交互细则（合并后必须明确的 6 条）

- **A. 展开与懒加载**：组默认折叠/展开规则完全沿用 `use-sidebar-effects.ts`；组内超过 5 条只渲染 5 条 + 「展开其余 N 个会话」；被选中的会话若被上限隐藏则自动展开该组。合并不会改变懒加载语义。
- **B. 排序与拖拽**：手动排序模式下，组内拖拽重排与跨组拖拽移动保持现状链路（乐观覆盖 → 写回 → 失败回滚 + 错误条）。分组模式为「平铺」时不渲染组头，因此不支持跨组拖拽（与现状一致）；如需在平铺模式移动，用 Phase 3 的行菜单「移动到目录」。
- **C. 归档**：默认隐藏 + 工具条开关，`hiddenArchivedCount` 提示保留；归档会话仍参与目录组计数口径（沿用现状）并在组内以徽标区分。
- **D. 平铺模式下的目录管理（必须补）**：现状之所以在平铺模式仍能管理目录，是因为目录段独立于会话段始终渲染目录行；合并后平铺模式没有组头，管理入口会丢失。因此 Phase 2 必须同时交付段头 `管理目录` 按钮 → `WorkspaceDirectoryManageDialog`（列出注册目录，逐项支持新会话 / 重命名 / 移除，复用现有回调与弹窗组件）。**这是合并的完整性前提，不是可选优化。**
- **E. 重命名**：合并后同一会话只有一处内联编辑器，删除 `origin` 机制与 `workspace-sidebar.tsx:322-325` 的分区下发逻辑。
- **F. 空态与引导**：无注册目录且无会话 → 「还没有工作目录」+ `+ 添加目录` CTA；有注册目录但无会话 → 目录树 + 组内空提示；搜索无命中/全部归档 → 沿用 `emptySessions.search` / `allArchived`。

### 3.6 组件拆分与行数预算（满足 500 行门禁）

合并后单文件必然超标，按职责拆分为 4 个新件 + 1 个改造件：

| 文件 | 职责 | 预估非空行 |
| --- | --- | --- |
| `workspace-sidebar/directories-section.tsx`（改造：合并后的段主体） | 段外壳 + 工具条接线 + 树装配 + 空态 | ~380–450 |
| `workspace-sidebar/session-browser-toolbar.tsx`（新） | 用户选择 + 统计摘要 + 排序/分组/归档控件 + 管理目录按钮 | ~260–320 |
| `workspace-sidebar/session-row.tsx`（新） | 行视图模型 → `SidebarSessionItem`，统一菜单/拖拽/重命名/谱系接线 | ~120–160 |
| `workspace-sidebar/directory-group-header.tsx`（新） | 组头：标签 / 计数 / 告警 / 落点 / 动作槽 | ~110–160 |
| `workspace-directory-manage-dialog.tsx`（新，Phase 2） | 平铺模式的目录管理弹层 | ~150–200 |
| `workspace-sidebar/sessions-section.tsx` | **删除**（内容拆入上述文件） | — |

---

## 4. 决策点（需拍板，均附推荐）

| # | 决策 | 选项 | 推荐 | 理由 |
| --- | --- | --- | --- | --- |
| D1 | 合并后段名 | 工作目录 / 会话 | **工作目录** | 骨架是目录；「会话」语义太泛且与本地聊天易混 |
| D2 | 平铺模式 | 保留 + 补目录管理弹层 / 直接删掉平铺 | **保留 + 弹层**（Phase 2 内交付） | 平铺是已交付且有 e2e 的能力，删除属于功能倒退 |
| D3 | 未登记目录 | 保持只读 / 增加「登记为工作目录」 | **增加**（Phase 3，可裁剪） | 派生目录是「有会话但未注册」，登记是自然下一步 |
| D4 | 用户选择器 | 留在段内工具条 / 移到侧栏 header | **留在段内** | 改动最小，且与会话列表上下文一致 |
| D5 | 段头徽标 | 会话数 / 目录数 | **会话数** | 与原「会话」段一致，用户以会话为计数锚点；目录数在统计行体现（如 `3 个目录 · 12 个会话`） |
| D6 | 纯目录管理能力是否加「无会话也展开」 | 是 / 否 | **否** | 保持现状：0 会话目录显示为一行 + 可新建会话，不做自动展开 |

---

## 5. 分阶段实施计划

### Phase 0 · 基线与准备（0.5 天，无行为变化）

- 冻结现状：确认工作树中侧栏相关在飞改动已落地（见 §8 协调项），记录基线测试结果。
- 收敛 e2e 定位工具：把 `sectionByTitle` / `sessionsSection` 抽到 `frontend/e2e/support`（若已存在同名 helper 则复用），为 Phase 2 的一处替换铺路。
- 门禁：`npm test` + 侧栏相关 e2e 全绿；`npm run lint` 0 违规。
- 提交信息建议：`test(sidebar): 冻结目录/会话分区基线与定位 helper`。

### Phase 1 · 抽件重构（1 天，双段并存，DOM 不变）

- 新增 `session-browser-toolbar.tsx`、`session-row.tsx`、`directory-group-header.tsx`。
- `sessions-section.tsx` 改为使用新件（渲染结果与 DOM 结构保持不变，工具条与行接线只是搬家）。
- 目的：把「能抽出可复用件」与「改变行为」拆成两个可独立回滚的提交；同时把 498 行的文件降到 300 行以内。
- 门禁：`npm test` 全绿；`session-collapse / session-grouping / session-order / sidebar-session-actions` 四个 e2e 全绿（**零断言改动**即通过）。
- 提交信息建议：`refactor(sidebar): 抽出会话工具条/会话行/目录组头（无行为变化）`。

### Phase 2 · 合并为单段（1–1.5 天，可见变更）

- `directories-section.tsx` 装配为合并段：工具条 + 完整会话行 + Show N more + 行菜单 + 拖拽 + 播报节点。
- 新增 `workspace-directory-manage-dialog.tsx` 并在段头挂 `管理目录`（D2 的完整性前提）。
- `workspace-sidebar.tsx`：删除 `sessions-section` 渲染；合并 `showSessionsSection` / `showDirectoriesSection` 为 `showWorkspaceSection`；`SessionRenameTarget` 去掉 `origin`；`SidebarSectionId` 去掉 `"sessions"`；`openSections` 初始化同步。
- 删除 `sessions-section.tsx`；i18n 增删键（zh-CN 与 en-US 同步，`sidebar.sections.sessions` 若无其它引用则删除；新增 `sidebar.directories.manage*`）。
- e2e：所有按分区标题定位的用例改为单段定位；**新增回归断言「同一会话在侧栏只渲染一次」**（对目录树内的行计数）。
- 门禁：`npm run lint`（500 行 + i18n 对齐）、`npm test`、`npm run build`、侧栏全部 e2e（collapse/grouping/order/stats/sidebar-session-actions/branch）。
- 提交信息建议：`feat(sidebar): 合并「工作目录/会话」分区为单一目录会话树`。

### Phase 3 · 能力补齐（0.5–1 天，可选）

- 派生目录组头新增「登记为工作目录」：复用 `WorkspaceDirectoryAddDialog` 并预填路径；登记成功后组头切换为可管理态。
- 平铺模式行菜单新增「移动到目录」子菜单（组头不可用时的替代路径）；写回链路复用 `sessionMove`。
- 可选：为派生目录开放「在目录中新建会话」。**前置确认**：后端 `CreateSession` 是否允许只传 `workspace_path` 而不传 `directory_id`（上游方案里两者是并列可选参数，未验证仅路径时的行为）——未确认则不做。
- 门禁：新增用例覆盖登记与移动；既有 e2e 不回退。

### Phase 4 · 清理与文档（0.5 天）

- 删除死代码/死 props/死 i18n 键（`greps` 确认无引用后删除，双语同步）。
- 更新 `workspace-directory-management-implementation-plan.md` 与优化台账：标注「分区一分为二造成重复」的问题及本合并方案。
- 把本方案状态从「草案」改为「已落地」，并回填各阶段提交号。

---

## 6. 文件级改动地图

| 文件 | Phase | 动作 |
| --- | --- | --- |
| `frontend/src/components/workspace/workspace-sidebar.tsx` | 2 | 单段装配；删 origin；合并显示条件 |
| `.../workspace-sidebar/directories-section.tsx` | 1–2 | 抽件后改造为合并段主体 |
| `.../workspace-sidebar/sessions-section.tsx` | 1–2 | 先被抽件、后删除 |
| `.../workspace-sidebar/session-browser-toolbar.tsx` | 1 | 新增 |
| `.../workspace-sidebar/session-row.tsx` | 1 | 新增 |
| `.../workspace-sidebar/directory-group-header.tsx` | 1 | 新增 |
| `.../workspace-directory-manage-dialog.tsx` | 2 | 新增（平铺模式目录管理） |
| `.../workspace-sidebar/types.ts` | 2 | `SidebarSectionId` 去掉 `"sessions"`；props 收敛 |
| `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/base.ts` | 2–4 | 键增删，双语逐键对齐 |
| `frontend/e2e/sidebar-session-actions.spec.ts` | 0/2 | helper 收敛、注释与定位更新、加唯一性断言 |
| `frontend/e2e/session-{collapse,grouping,order,stats,search}.spec.ts` | 2 | 按需改用单段定位 |
| `frontend/src/components/workspace/workspace-sidebar*.test.tsx` 等单测 | 1–3 | 抽件后断言目标调整；新增唯一性与平铺管理用例 |
| `docs/plan/workspace-directory-management-implementation-plan.md` | 4 | 回填合并说明 |

---

## 7. 测试与验收

**分层验收**

| 层 | 目标 | 命令/文件 |
| --- | --- | --- |
| 单测 | 合并段渲染唯一性、组头动作按注册态显隐、平铺模式可管理目录、折叠/上限/空态 | `npx vitest run src/components/workspace/workspace-sidebar*.test.tsx src/components/workspace/workspace-sidebar/**` |
| e2e | 行菜单、拖拽重排/跨组移动、分组切换、归档开关、统计、目录 CRUD | `session-collapse / session-grouping / session-order / sidebar-session-actions`（跑前需先 build） |
| 门禁 | 单文件 ≤500 非空行、i18n zh/en 逐键对齐、无备份文件 | `npm run lint` |
| 构建 | 类型与打包 | `npm run build` |

**必须新增的回归断言**

1. 同一会话在侧栏中只出现一行（按会话标题统计出现次数 = 1）。
2. 分组模式切到「平铺」后，段头 `管理目录` 可完成「重命名 / 移除 / 在目录中新建会话」三项操作。
3. 段头 `+ 添加目录` 与组头「目录内新建会话」在合并后仍可完成创建并跳转到规范路由。
4. 组内超过 5 条时仍只渲染 5 条，选中被隐藏会话自动展开。
5. 关闭「本地聊天」段不影响合并段；「运行时概览」段不受影响。

---

## 8. 风险、回滚与并行协调

| # | 风险 | 影响 | 缓解 |
| --- | --- | --- | --- |
| R1 | 合并段单文件超 500 行门禁 | lint 失败 | 先抽件（Phase 1），行数预算见 §3.6 |
| R2 | 平铺模式丢失目录管理入口 | 功能倒退 | Phase 2 内交付 `管理目录` 弹层（§3.5-D） |
| R3 | e2e 普遍依赖分区标题定位 | 大面积失败 | Phase 0 收敛 helper；Phase 2 一次性替换为单段定位 |
| R4 | 折叠态/自动展开行为回归 | 用户体验下降 | 不触碰 `use-sidebar-effects.ts` 与 `openSessionDirectories` 语义；e2e 覆盖 |
| R5 | 重命名/焦点回归 | 点铅笔无响应 | 删除 origin 机制后，用 e2e `sidebar-session-actions` 的 Rename 用例回归；保留「同一时刻最多一个编辑器」的不变量 |
| R6 | 分区消失引发用户困惑 | 使用习惯变更 | 段名沿用「工作目录」+ 会话数徽标 + 空态 CTA；如需可加一次性引导文案 |
| R7 | 跨组移动落点被新组头破坏 | 写回失败 | 组头保留 `data-testid="sidebar-session-group-drop"`；Unscoped 仍不可作为落点 |
| R8 | i18n 键删除遗漏引用 | 运行时报键缺失/lint 失败 | 删键前 `grep` 全仓引用；zh-CN / en-US 同批修改 |

**回滚**：本次改动是纯前端渲染层重组，无数据迁移、无接口变更。Phase 0/1 为无行为变化提交，Phase 2 为单次提交，任一阶段出问题直接 `git revert` 对应提交即可回到双段形态。

**并行协调（重要）**：当前工作树中以下与本任务直接相关的文件处于未提交改动状态，Phase 1 抽件会大面积触碰：
`workspace-sidebar.tsx`、`workspace-sidebar/directories-section.tsx`、`workspace-sidebar/sessions-section.tsx`、`workspace-sidebar/session-item.tsx`、`e2e/sidebar-session-actions.spec.ts`。
开工前需先确认这些改动已提交或与并行工作流约定互斥窗口，否则抽件补丁必然冲突。

---

## 9. 附录：关键代码索引

| 关注点 | 位置 |
| --- | --- |
| 分区装配、重命名 origin、显示条件 | `frontend/src/components/workspace/workspace-sidebar.tsx`（:33、:199-212、:310-326、:395-399、:451-455） |
| 目录段实现 | `frontend/src/components/workspace/workspace-sidebar/directories-section.tsx`（:103/:115-116/:126/:150/:189/:213-214/:232-242/:261/:313/:324/:335） |
| 会话段实现 | `frontend/src/components/workspace/workspace-sidebar/sessions-section.tsx`（:144/:162/:167/:183/:192/:241-242/:274/:325/:341/:398/:477-504） |
| 会话行 UI 与菜单 | `frontend/src/components/workspace/workspace-sidebar/session-item.tsx` |
| 行渲染口径 | `frontend/src/components/workspace/workspace-sidebar/session-row-view-model.ts` |
| 折叠/自动展开 | `frontend/src/components/workspace/workspace-sidebar/use-sidebar-effects.ts` |
| 分组视图 hook | `frontend/src/hooks/workspace/use-session-group-view.ts` |
| 分组/排序/归档 hooks | `frontend/src/hooks/workspace/use-session-grouping.ts`、`use-session-order.ts`、`hooks/workspace/workspace-sidebar/use-session-group-visibility.ts`、`use-session-drag-reorder.ts` |
| 组共享口径 | `frontend/src/lib/workspace/session-grouping.ts`、`frontend/src/components/workspace/workspace-sidebar-shared.ts` |
| 目录注册表弹窗 | `frontend/src/components/workspace/workspace-directory-add-dialog.tsx` |
| i18n | `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/base.ts`（`sidebar.sections.*`、`sidebar.directories.*`、`sidebar.session*`） |
| 现有重复的测试证据 | `frontend/e2e/sidebar-session-actions.spec.ts:24-39`（两段重复的说明）、`:42-46`（分区定位 helper） |

---

## 10. 实施记录（2026-09-15）

**状态**：Phase 0 / 1 / 2 与 Phase 4 清理已完成（改动仍在工作树、**未提交**，回滚方式见 §8）；Phase 3 为可选能力补齐，保留为后续项（§5）。**后端零改动**。

### 10.1 落地内容（文件级）

| 文件 | 状态 | 说明 |
| --- | --- | --- |
| `workspace-sidebar/directories-section.tsx` | 改（474 非空行） | 合并段主体：以「工作目录」为骨架承载分组，会话作为叶子行；会话浏览工具条整体迁入该段 |
| `workspace-sidebar/sessions-section.tsx` | **删** | 会话段并入后删除，重复渲染的两行同源问题消失 |
| `workspace-sidebar/session-browser-toolbar.tsx` | 新增（203 行） | 工具条抽件：用户切换 / 统计 / 排序双模式 / 分组切换 / 归档开关 |
| `workspace-sidebar/session-row.tsx` | 新增（97 行） | 会话行渲染（行 UI 与菜单的分层保持既有口径） |
| `workspace-sidebar/directory-group-header.tsx` | 新增（115 行） | 目录组头（注册态可管理 / 派生态只读），保留 `data-testid="sidebar-session-group-drop"` 落点 |
| `workspace-sidebar/directory-group-actions.tsx` | 新增（63 行） | 组头动作（目录内新建会话、重命名、移除） |
| `workspace-directory-manage-dialog.tsx` | 新增 | §3.5-D 平铺模式下的目录管理入口 |
| `workspace-sidebar.tsx` | 改 | 单段装配；删除重命名 origin 机制；合并段显示条件取原两段**并集**（§3.4） |
| `workspace-sidebar/types.ts` | 改 | `SidebarSectionId = "directories" \| "chats" \| "runtime"`（`"sessions"` 移除） |
| `workspace-sidebar/use-sidebar-effects.ts` | 改 | 检索时展开 `chats` + `directories`（随段合并同步） |
| `workspace-sidebar/session-item.tsx`、`workspace-sidebar-shared.ts` | 改 | 行渲染与共享口径随合并收敛 |
| `i18n/resources/{zh-CN,en-US}/workspace/base.ts` | 改 | `sidebar.sections.sessions` 无引用后删除；双语逐键对齐 |
| `e2e/session-{collapse,grouping,order}.spec.ts`、`e2e/sidebar-session-actions.spec.ts` | 改 | 分区定位 helper 收敛到单段口径（R3 缓解） |
| `workspace-sidebar-cross-group-move.test.tsx`、`workspace-sidebar-shared.test.ts` | 改 | 单测断言对齐合并段语义 |

> 复用既有已提交件（非本轮新增）：`workspace-sidebar/section-shell.tsx`、`session-group-toggle.tsx`、`use-session-group-visibility.ts`、`session-user-menu.ts`、`session-descriptor.ts`、`labels.ts`。
> 说明：当前工作树同时承载其他并行工作流的在飞改动，上表只列本方案直接触碰的侧栏相关文件。

### 10.2 决策回执（对照 §3）

| 决策 | 落地位置 / 证据 |
| --- | --- |
| D1 段名沿用「工作目录」 | `directories-section.tsx:282` `title={t("sidebar.sections.directories")}`；en-US 为 `Directories`（e2e 可达名断言 `^工作目录 \d+$` / `Directories 1` 同步更新） |
| D5 徽标 = 会话数 | `directories-section.tsx:283` `count={<Badge>{sessionThreads.length}</Badge>}` |
| D6 0 会话目录不自动展开 | 组头常驻、组内容默认折叠；e2e 跨组移动用例断言行数 0 且组头计数 `alpha 0` 常驻 |
| §3.4 显示条件取两段并集 | `workspace-sidebar.tsx`（单段装配），`use-sidebar-effects.ts:43-48` 检索时展开 `chats` + `directories` |
| §3.5-D 平铺模式保留目录管理入口 | `directories-section.tsx:184-188` `renderPlainSessionList = sessionGroupingMode === "flat" && sessionDirectoryGroups.length > 0`；`workspace-directory-manage-dialog.tsx` 承载重命名 / 移除 / 目录内新建会话 |
| §3.5-E 重命名状态退化为 `string \| null` | 删除原「重命名 origin」单编辑源机制（同一时刻最多一个编辑器的不变量保留） |
| §3.3 注册目录零会话常驻 | `appendEmptyRegisteredGroups`（`workspace-sidebar-shared.ts`）在合并段继续生效 |
| R7 跨组落点不被新组头破坏 | 组头保留 `data-testid="sidebar-session-group-drop"`；组容器 `data-testid="sidebar-session-group"` |

### 10.3 本轮修复（合并引发的 e2e 定位回归）

| # | 症状 | 根因 | 修复 |
| --- | --- | --- | --- |
| 1 | 折叠 / 分组用例找不到分区段 | 段标题正则仍为 `^会话 \d+$`，合并后段名是「工作目录 N」 | `session-collapse.spec.ts:104-110`、`session-grouping.spec.ts:173-180` 改为 `/^工作目录 \d+$/` |
| 2 | 组内展开用例连锁失败（`element(s) not found`） | 组头抽件后 `groupHeader().locator("xpath=..")` 只到组头包裹层，`expandGroup` 误点已展开的组 | `session-grouping.spec.ts:205-216` 改用 `[data-testid="sidebar-session-group"]:has([data-testid="sidebar-session-group-drop"][title="<key>"])` |
| 3 | 跨组移动断言「源组整组消失」失败 | 合并段按 §3.3 让 0 会话**注册**目录常驻（只有派生空组才消失） | `session-grouping.spec.ts:282-286, 295-298` 改为断言行数 0 + 组头计数 `alpha 0`（刷新后同断言） |

### 10.4 验收证据（§7 分层验收）

| 层 | 命令 | 结果 |
| --- | --- | --- |
| 单测 | `npm test`（vitest） | **195 文件 / 1478 用例全绿** |
| e2e（本方案直接相关） | `npx playwright test e2e/session-collapse.spec.ts e2e/session-grouping.spec.ts` | **5 passed**（24.1s：组内折叠 2 例 + 分组 / 跨组移动 3 例） |
| 门禁 | `npm run lint` | **0 error / 3 warning**（i18n scanned=661 / violations=0；备份门禁 978 文件 0 残留；行数门禁 926 个 `.ts/.tsx` 中 0 个 > 500，最大 `src/lib/trajectory/timeline-window.ts`=494；字面色值门禁 232 文件 0 处） |
| 构建 | `npm run build`（`tsc -b && vite build`） | exit 0（`✓ built in 1.31s`） |

合并段文件本身的行数预算（§3.6）：`directories-section.tsx` **474** 非空行、`session-browser-toolbar.tsx` 203、`directory-group-header.tsx` 115、`session-row.tsx` 97、`directory-group-actions.tsx` 63——均在 500 行门禁内。

### 10.5 全量 e2e 结果与归因

`npx playwright test --reporter=line` 全量 **82 用例：80 passed / 2 failed**，两个红项均**不在本方案的改动面**，归因如下：

| 失败用例 | 归因 | 证据 |
| --- | --- | --- |
| `e2e/connection-recovery.spec.ts:17`（直连 chat 流失败可见断线状态） | 并行工作流在飞改动：该 spec 的断线判据在同批工作树中**新增**了「重试后顶栏收敛回在线」断言，而 `src/api/runtime/shared.ts` 的实现改动（08:32）晚于本次 e2e 所用 `dist` 构建（08:29），即断言新于包 | 重建 `dist`（`npm run build` exit 0）后单跑该 spec：**1 passed** |
| `e2e/live-delta.spec.ts:25`（打字机流式渲染） | **既有红项**（与本次合并无关）：在**不含任何工作树改动**的 HEAD `be1754c4` 上同样失败 | `git worktree add --detach <tmp> HEAD` + `frontend/node_modules` junction + `npm run build` + 定向 e2e，结果 **1 failed**；本方案未触碰 `message-list*` / `mock-server.mjs` / 流式链路，`workspace-sidebar-shared.ts` 的引用方仅侧栏组件与 `hooks/workspace/use-session-group-view.ts` |

定向 e2e（本方案直接相关 5 例）与单测、门禁、构建均为绿，见 §10.4。

### 10.6 遗留与后续

1. **Phase 3 未做**（可选能力补齐）：派生目录组头「登记为工作目录」、平铺模式行菜单「移动到目录」子菜单、派生目录内「新建会话」（前置：需先确认后端 `CreateSession` 仅传 `workspace_path`、不传 `directory_id` 时的行为）。
2. **提交未做**：本轮为纯前端渲染层重组（无数据迁移、无接口变更），改动仍在工作树；回滚口径见 §8。
3. **转交**：`e2e/live-delta.spec.ts` 的既有红项属消息渲染 / 流式链路在飞工作流，需其收口；本方案不修改该链路。
---

## 11. 侧栏样式优化（2026-09-15 追加）

**状态**：已落地（随 §10 同批工作树，**未提交**；纯前端渲染层，**后端零改动**）。

| 诉求（用户原话） | 落地口径 |
| --- | --- |
| 「目录所在的菜单顶到最左侧」 | 删除会话行在目录下的嵌套缩进（`directories-section.tsx` 原 `ml-4`），目录组头与目录内会话共用同一左边界并贴齐段左缘；组头内边距 `px-1.5 → px-1` |
| 「目录、会话标题中的图标操作放到菜单里」 | 目录组头：三个平铺图标（新建会话 / 重命名 / 移除）收敛为**一个** `MoreHorizontalIcon` 菜单入口；会话行：行内铅笔按钮并入行菜单，作为**首项**「重命名会话」 |
| 「在 hover 时再显示」 | 两个入口都在既有 `opacity-0 … group-hover/*:opacity-100 focus-within:opacity-100` 槽位内：默认不可见，hover 或键盘聚焦（含菜单展开态）才显示 |
| 「把空间与显示让给会话/目录标题」 | 会话行右侧预留 `pr-16 → pr-8`（无菜单行 `pr-9 → pr-2`）；目录组头常驻占位从约 3 个图标宽收到 1 个图标宽；会话行缩进让出的 16px 全部归还标题 |

### 11.1 文件级改动

| 文件 | 改动 |
| --- | --- |
| `workspace-sidebar/directory-group-actions.tsx` | 重写为单一入口菜单：`aria-haspopup="menu"` / `aria-expanded`、`ArrowDown/ArrowUp/Home/End/Escape/Tab` 键盘导航、忙碌态（`creating`）禁用新建项并显示旋转图标；对外 props（`creating / onCreate / onRename / onRemove / t`）不变 |
| `workspace-sidebar/directory-group-header.tsx` | 组头内边距 `px-1.5 → px-1`；动作槽仍是 hover 显示（`group-hover/directory-row` + `focus-within`） |
| `workspace-sidebar/session-item.tsx` | `SessionRowMenu` 新增 `onRename` / `renameLabel`，渲染「重命名会话」首项；删除行内铅笔按钮与 `PencilIcon` 引用；`showMenu` 口径纳入 `onStartRename`；右侧预留收窄 |
| `workspace-sidebar/directories-section.tsx` | 删除会话行嵌套缩进（`ml-4`）；目录重命名输入框内衬 `px-1.5 → px-1`；`cn` 引用随之内联清理 |
| `workspace-sidebar/section-shell.tsx` | 分区标题行内边距 `px-1.5 → px-1`（三个分区共用同一 `SidebarSection`，口径统一）：分区图标与每个目录组头的 `FolderIcon` 落在同一条竖线上，整段左侧只剩一条基准线 |
| `workspace-sidebar/use-sidebar-effects.ts` | 两个 `useEffect` 补齐 `setOpenSections` / `setOpenSessionDirectories` 依赖（二者来自父层 `useState`，identity 稳定，语义零变化）：清掉本批引入的 2 条 `react-hooks/exhaustive-deps` warning |
| `i18n/resources/{zh-CN,en-US}/workspace/base.ts` | 新增 `sidebar.directories.actions`（目录操作 / Directory actions），双语逐键对齐 |

### 11.2 不变式（守卫项）

1. **e2e 定位契约不变**：`data-testid="sidebar-session-group"`、`sidebar-session-group-drop`（`title=` 规范化路径）、`sidebar-session-group-toggle`、会话行 `[role="treeitem"]` + `data-depth` 全部保留；跨组拖拽用例未改一行。
2. **键盘可达性不退化**：菜单入口与菜单项都在 `focus-within:opacity-100` 的槽位内，Tab 进入即显形；`Escape` 关闭并把焦点还给入口。
3. **动作语义零变化**：三个目录动作与会话重命名的回调、文案、忙碌禁用语义逐字保留，仅从 `role="button"` 变为 `role="menuitem"`。

### 11.3 验证证据

| 层 | 命令 | 结果 |
| --- | --- | --- |
| 单测（全量） | `npm test -- --run`（vitest） | **196 文件 / 1492 用例全绿**（含本轮新增 6 例） |
| 单测（侧栏定向） | `npx vitest run sidebar` | **10 文件 / 58 用例全绿**（9.7s；依赖数组收敛后复跑） |
| e2e（直接相关） | `npx playwright test e2e/sidebar-session-actions.spec.ts e2e/session-collapse.spec.ts e2e/session-grouping.spec.ts` | **10 passed**（26.8s）；更早一轮四 spec（含 `session-order`）**13 passed** |
| 门禁 | `npm run lint` | **0 error / 1 warning**（本批 2 条 warning 已随 §11.1 依赖补齐清除；余 1 条属 `artifact-detail-dialog.tsx`，非本批文件。i18n scanned=664 / violations=0；备份 983 文件 0 残留；行数 931 个 `.ts/.tsx` 中 0 个 > 500，最大 `src/pages/workspace-page.tsx`=497；字面色值 32 文件 0 处） |
| 构建 | `npm run build`（`tsc -b && vite build`） | exit 0 |

> e2e 前置口径（本轮踩坑留档）：`playwright.config.ts` 只跑 `dist` 构建产物（`vite preview`），**改完源码必须先 `npm run build` 再跑 e2e**；否则会拿旧包跑出「菜单项找不到」这类假红（本轮首次定向 e2e 即 1 failed 超时，重建 `dist` 后同用例 1.9s 通过）。

> 工作树清理口径（本轮事故留档）：**不要在含 `node_modules` junction 的目录上使用 `git worktree remove` 或 `Remove-Item -Recurse`** —— 两者会顺着 junction 递归删掉**主树**的 `frontend/node_modules`（本轮即删掉 `.bin` 与部分 `.pnpm` 内容）。只读核对用 `git worktree list` + 主树 `node_modules` 快照；必须清理 junction 时用 `cmd /c rmdir "<junction 路径>"`（只摘链接，不递归）。误删后的恢复：`cd frontend; pnpm install --offline --force`（14.2s，440 包全部从本地 store 重链，无需网络）。

### 11.4 测试口径变更

| 文件 | 变更 |
| --- | --- |
| `e2e/sidebar-session-actions.spec.ts` | 用例名 `铅笔重命名…` → `菜单重命名…`；入口由「hover 行 → 点铅笔」改为「hover 行 → 行菜单 → 重命名会话」菜单项；断言口径（PATCH 落库 / 跨帧存活 / Esc 不写回）不变 |
| `workspace-sidebar/directory-group-actions.test.tsx` | 新增 6 例：默认单入口、菜单项顺序、三类回调与关闭、忙碌禁用、`Escape` 归焦、`ArrowDown` 聚焦首个可用项 |

### 11.5 遗留

1. 目录组头菜单目前只有单测覆盖；如需 e2e 级回归，可在 `session-grouping.spec.ts` 补一条「组头菜单 → 重命名目录」用例（本次未加，避免与并行工作流的该 spec 改动重叠）。
2. 菜单项未配图标（与既有会话行菜单视觉保持一致）；如需图标，建议两个菜单一起统一，另开样式批次。

---

## 12. 第二轮样式优化（2026-09-15）：目录组顶到左侧

**诉求（用户原话，第二次提出）**：「左侧栏上的分组还有缩进，目录/工作目录应该作第一元素顶到左侧。」

### 12.1 根因（单点，不在组头自身）

§11 只处理了**组内会话行**的缩进（`ml-4`）与内衬（`px-1.5 → px-1`），漏掉了会话树**外层包裹层**：

```tsx
// session-browser-toolbar.tsx（改动前）
<div className={cn("ml-3 space-y-1", sessionGroupingMode === "directory" && "border-l border-border pl-2")}>
```

该包裹层是分区正文（`directories-section.tsx` 的目录组列表）与组头之间的唯一横向偏移来源。按目录视图下的实测偏移恰为 **21px** = `ml-3`(12) + `pl-2`(8) + `border-l`(1)——与用户「还有缩进」的观感一致；平铺视图仍留 12px。

### 12.2 改动（文件级）

| 文件 | 改动 |
| --- | --- |
| `workspace-sidebar/session-browser-toolbar.tsx` | 取消会话树包裹层的嵌套缩进：`cn("ml-3 space-y-1", … "border-l border-border pl-2")` → `"space-y-1"`。目录组头自此与分区标题行、用户卡片共用同一条左基准线（分区正文左缘），**顶到侧栏内容盒左侧**；组头自身仍保留 `px-1`，因此 `FolderIcon` 与分区标题 `FolderIcon` 仍落在同一竖线上 |
| `e2e/sidebar-directory-indent.spec.ts`（新增） | 几何回归守卫：断言组容器左缘 == 分区左缘；组头按钮左缘 == 分区左缘 + 4px（`px-1`）；组内第一层会话行（`[role="treeitem"][data-depth="0"]`）左缘 == 分区左缘。断言只量 `boundingBox().x`，不读实现类名，对重构免疫；分区用 `section:has([data-testid="sidebar-session-group"])` 反查，不依赖 UI 文案 |

### 12.3 验证证据

| 层 | 命令 | 结果 |
| --- | --- | --- |
| 修复前（同一守卫用例跑旧 `dist`） | `npx playwright test e2e/sidebar-directory-indent.spec.ts` | **1 failed**：`Expected: <= 1 / Received: 21`——用数值坐实了「21px 残留缩进」的根因判定，并证明守卫有区分力（非空跑） |
| 修复后（重建 `dist`） | 同上 | **1 passed**（1.2s） |
| e2e（侧栏相关 7 spec） | `npx playwright test e2e/sidebar-directory-indent.spec.ts e2e/sidebar-session-actions.spec.ts e2e/session-collapse.spec.ts e2e/session-grouping.spec.ts e2e/session-order.spec.ts e2e/session-search.spec.ts e2e/thread-link.spec.ts` | **18 passed / 1 failed**（50.7s）。唯一红项是 `thread-link.spec.ts:32`（断言 `/runtime/events` 请求数 > 0，实得 0）：**与本轮改动无因果关系**——本轮只改侧栏一行 className 与新增一个 spec，无法影响 chat 页的事件请求路径；该 spec 单独复跑同样失败，且工作树里 `use-session-runtime-stream.ts` / `use-trajectory-recovery.ts` / `workspace-shell*` / `mock-server.mjs` 等**并行工作流**改动正落在事件链路，归属它们处理 |
| 单测（全量） | `npm test -- --run` | **198 文件 / 1501 用例全绿** |
| 单测（侧栏定向） | `npx vitest run sidebar` | 10 文件 / 58 用例全绿 |
| 门禁 | `npm run lint` | **0 error / 2 warning**（exit 0）。两条 warning 均非本批文件：`artifact-detail-dialog.tsx:50`、`use-conversation-scroll.ts:408`。i18n scanned=666 / violations=0；备份 987 文件 0 残留；行数 934 个 `.ts/.tsx` 0 个 > 500（最大 `src/pages/workspace-page.tsx`=497）；字面色值 32 文件 0 处 |
| 构建 | `npm run build`（`tsc -b && vite build`） | exit 0 |

### 12.4 遗留 / 口径

1. 本次坐标系：**侧栏内容盒左缘**（`aside` 的 `p-3` / `p-2.5` 内衬之内）是「顶到左侧」的基准——分区标题、用户卡片、目录组头、组内会话行四者左缘现全部对齐到该线；若后续要求连 `aside` 内衬一并取消，需要连同搜索框、分区标题一起改，另开批次。
2. 组内会话行的**谱系缩进**（`session-item.tsx` 按 `lineage.depth` 的 `paddingLeft: depth * 12`）按设计保留：那是「分支子行」的语义层级，与「目录分组」不是同一维度。
