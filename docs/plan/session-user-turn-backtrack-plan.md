# 会话用户轮次回退（Codex-style Backtrack）实施计划

日期：2026-07-26  
状态：**Phase 1–6 done**（runtime core + code restore + HTTP API + aicli `/backtrack` + frontend restore + stable message_id/turn_id + tombstone/审计 + TUI Esc 选择器）；**Phase 6 residual UI done**（frontend Restore 面板 Backtrack audit + dialog `edit_prompt` + transcript 内联导航 + bubble 内联编辑，2026-07-26）  
关联能力：

- Codex 参考：Esc 选中历史用户消息 → 编辑 → 确认后截断 transcript，并可选恢复工作区文件
- 本仓库已有：`checkpoint.Manager`、`SessionActor.Rewind`、checkpoint HTTP API、frontend checkpoint 预览面
- 相关文档：
  - `docs/multi-agents/teams/plan/team4-implementation-analysis.md`
  - `docs/plan/runtime-server-aicli-shared-session-mechanism-plan.md`
  - `docs/plan/aicli-ui-refactor-codex-inspired-plan.md`

## 1. 目标

在本项目中落地与 Codex backtrack 同级的**用户轮次（user-turn）回退**能力，让用户可以：

1. 选中某条历史 **user message** 作为锚点；
2. 可选编辑该条提示词；
3. 确认后把会话历史截断到该锚点**之前**（或“包含锚点并替换其文本”的编辑语义）；
4. 可选联动工作区文件恢复（code restore）；
5. 通过 CLI / runtime API / frontend 统一触发；
6. 发出可订阅事件，让 UI 与 runtime 状态同步收敛。

最终产品语义：

> “回到我刚才那句话，重说/改说，并丢掉之后的 assistant/tool 结果；如果需要，同时撤销之后改过的文件。”

## 2. 非目标

本计划**不**做：

1. 完整 git 工作区级 time-travel（仍以现有 per-file checkpoint 为主）。
2. 自动 fork 出新 session 保留“平行宇宙历史”（Codex 主路径是同 thread 原地截断）。
3. Team / multi-agent 全树级联 rewind 的一次性完美语义（先做单 session，再扩展 child/team）。
4. 重写现有 mutation checkpoint 体系。
5. 立刻把 aicli TUI 改成完整 transcript 可导航 UI（可先 slash + 索引，再增强交互）。

## 3. Codex 能力对照

| 维度 | Codex backtrack | 本项目现状 | 差距 |
|---|---|---|---|
| 锚点 | 用户消息 / turn | mutation `checkpoint_id` | 缺 user-turn 锚点 |
| 触发 | Esc 选择历史 user message | aicli 空 composer Esc + `/backtrack select`；frontend message action + 空 composer Esc 内联导航 | 已对齐主路径 |
| 历史处理 | 截断到锚点前，可编辑后重发 | `ReplaceHistory` / `HeadOffset` / exact conversation snapshot | 有截断原语，无 turn API |
| 代码恢复 | 可选 file snapshot restore | `RestoreCode` / `RestoreBoth` 已存在 | 可复用，但绑定 checkpoint 而非 turn |
| 事件 | `ThreadRolledBack` | `rewind_started` / `rewind_finished` / `rewind_completed` | 事件存在，语义偏 checkpoint |
| 失败 prompt 回滚 | 有 | `rollbackLastUserPrompt` 仅失败路径 drop last user | 不是通用 backtrack |
| Message 标识 | item/turn id | `metadata.message_id` / `turn_id`（Phase 6）+ index fallback + tombstone 审计 + TUI Esc 选择 + frontend Restore audit 面板 + transcript 内联高亮 | 已对齐主路径 |
| 纯对话 turn | 任意 user turn 可回 | checkpoint 主要在 write/shell mutation 时创建 | 无 mutation 时无法 checkpoint restore |
| UI | transcript 内直接编辑 | frontend message Backtrack dialog（含 edit_prompt）+ Esc 内联选轮 + bubble 内联 Edit + checkpoint restore + Restore 面板 audit；aicli Esc picker | 已对齐主路径 |

结论：

> **现有 checkpoint rewind 是“工具变更点恢复”；Codex backtrack 是“用户对话轮次重开”。两者相关，但不能直接等同。**

正确策略是：

1. 以 **user-turn backtrack** 作为一等产品能力；
2. 把现有 **checkpoint code/conversation restore** 作为 backtrack 的可选实现底座与增强项；
3. 在 API / actor / CLI / UI 上统一暴露 turn 语义，而不是强迫用户理解 `checkpoint_id`。

## 4. 现状盘点（可复用资产）

### 4.1 会话历史模型

- `backend/internal/chat/session.go`
  - `History []types.Message`
  - `HeadOffset`：可见历史截断
  - `ReplaceHistory` / `GetMessages` / `GetRecentMessages`
  - `CanonicalMessageCount`
- `backend/internal/types/message.go`
  - `Message{Role, Content, ContentParts, ToolCalls, ToolCallID, Metadata}`
  - **没有稳定 message id / turn id 字段**

### 4.2 Actor 与失败回滚

- `backend/internal/chat/actor.go`
  - `Rewind(ctx, checkpointID, mode)`
  - `handleRewindTo` → `checkpoint.Manager.Restore`
  - `applyConversationSnapshot` / `applyConversationPrefix`
  - `rollbackLastUserPrompt`：执行失败且已 append user prompt 时，删除最后一条匹配 user message
- 事件：
  - `EventRewindStarted`
  - `EventRewindFinished`
  - hook `EventRewindCompleted`

### 4.3 Checkpoint

- `backend/internal/checkpoint`
  - mutation 前后自动捕获文件
  - 可保存 `MessageCount` + exact `Conversation` snapshot
  - restore mode：`code` / `conversation` / `both`
  - code restore 语义：撤销**目标 checkpoint 之后**的变更（later checkpoints reverse）
- 捕获点：
  - `backend/internal/agent/approved_tool.go` 在 write/shell/mutation hint 工具执行前设置
    `pending.MessageCount` 与 `pending.Conversation`

### 4.4 API / Client / Frontend

已有：

- `GET /api/runtime/sessions/{id}/checkpoints`
- `GET /api/runtime/sessions/{id}/checkpoints/{checkpoint_id}/files`
- `POST .../preview`
- `POST .../restore`
- `skillsapi.Client` 对应方法
- frontend `use-runtime-checkpoints` + artifact checkpoint surface（列表/预览）

缺失：

- turn 列表 / backtrack API
- frontend restore 动作按钮
- aicli `/backtrack` 或 `/rewind` slash
- 统一 “user turn index → history prefix + optional code restore” 编排

### 4.5 已有相近但不同的能力

| 能力 | 用途 | 为什么不够 |
|---|---|---|
| `fork_turns` / `fork_context` | spawn child 复制父历史 | 创建新 session，不是原地回退 |
| `rollbackLastUserPrompt` | 失败时去掉刚写入的 user | 仅 last message + 失败路径 |
| compact | 压缩历史 | 改写语义，不是用户指定回退 |
| checkpoint restore | 恢复 mutation 点 | 依赖 checkpoint，不覆盖纯对话 turn |

## 5. 目标产品语义

### 5.1 核心术语

- **User Turn**：一次用户提交开启的对话轮次。起点是一条 `role=user` 消息，终点是下一条 user 消息之前的全部 assistant/tool/reasoning 消息。
- **Backtrack Anchor**：被选中的那条 user message。
- **History Prefix**：锚点之前的可见历史（默认不含锚点本身；若是“编辑并重发”，则用新文本替换锚点）。
- **Code Restore Scope**：从锚点之后发生的文件变更集合。
- **In-place Backtrack**：同一 `session_id` 上截断并继续，不新建 session。

### 5.2 用户可见行为

#### A. 对话-only 回退

1. 用户选择第 N 条 user message；
2. 系统丢弃该条及之后全部消息；
3. composer 预填原文本（可改）；
4. 用户提交后开启新 turn。

#### B. 编辑并重发

1. 选择第 N 条 user message；
2. 历史截断到 N-1；
3. 用编辑后的文本作为下一条 user prompt 立即或稍后发送。

#### C. 对话 + 代码联合回退

1. 同 A/B；
2. 额外把锚点之后捕获到的文件变更尽量恢复到锚点前状态；
3. 向用户展示恢复结果：成功路径、跳过路径、错误。

#### D. 仅预览

不改历史、不改文件，只返回：

- 将删除的 turn/message 摘要
- 将恢复的文件列表
- 是否存在 exact conversation snapshot / partial code coverage

### 5.3 与 Codex 对齐的关键选择

| 决策点 | 建议 | 原因 |
|---|---|---|
| 是否 fork 新 session | **默认 in-place** | 对齐 Codex；本项目 session 文件/runtime state 更简单 |
| 是否保留被截断历史 | **默认物理截断 + 可选 tombstone/审计事件** | 现有 `ReplaceHistory` 已是物理替换；完整平行历史成本高 |
| 锚点键 | **优先 stable message id；过渡期支持 user_turn_index** | 当前无 message id，需分阶段 |
| 代码恢复 | **可选，默认 off 或 ask** | 文件恢复有风险，产品上应显式确认 |
| 与 checkpoint 关系 | **backtrack 编排层调用 checkpoint，不取代它** | 复用现有 restore 算法 |
| 运行中是否允许 | **默认拒绝 busy session；可先 interrupt 再 backtrack** | 避免与 in-flight tool 冲突 |

## 6. 架构设计

### 6.1 分层

```text
UI / CLI / API
  └─ Backtrack Service  (turn 语义编排)
       ├─ Turn Indexer           # user turn 识别与定位
       ├─ History Truncator      # session history 截断/替换
       ├─ Code Restore Planner   # 把 turn 锚点映射到 checkpoint 集合
       ├─ Checkpoint Manager     # 现有 code restore
       └─ Event / Hook Publisher # backtrack_* / 复用 rewind_* 扩展字段
```

建议落点：

1. **核心编排**：`backend/internal/chat/backtrack.go`（或 `internal/backtrack`）
2. **Actor 命令**：`SessionActor.Backtrack(...)` + `handleBacktrack`
3. **HTTP**：
   - `GET /api/runtime/sessions/{id}/turns`
   - `GET /api/runtime/sessions/{id}/backtrack/audit`
   - `POST /api/runtime/sessions/{id}/backtrack/preview`
   - `POST /api/runtime/sessions/{id}/backtrack`
4. **CLI**：`/backtrack` slash（可别名 `/rewind`）
5. **Frontend**：message list 上的 “回退到此处” + checkpoint panel restore 按钮

### 6.2 数据模型建议

#### 6.2.1 过渡期：不强制改 Message schema 也能上线

```go
type UserTurn struct {
    Index          int    `json:"index"`            // 0-based among user messages in visible history
    MessageIndex   int    `json:"message_index"`    // index in visible history
    Preview        string `json:"preview"`
    CreatedHint    string `json:"created_hint,omitempty"`
    HasLaterMutation bool `json:"has_later_mutation"`
    CheckpointIDs  []string `json:"checkpoint_ids,omitempty"` // later checkpoints after this turn
}

type BacktrackRequest struct {
    SessionID        string `json:"-"`
    UserTurnIndex    *int   `json:"user_turn_index,omitempty"`
    MessageIndex     *int   `json:"message_index,omitempty"`
    MessageID        string `json:"message_id,omitempty"` // phase 2
    Mode             string `json:"mode"` // conversation | both | code(optional niche)
    EditPrompt       string `json:"edit_prompt,omitempty"`
    AutoSubmit       bool   `json:"auto_submit,omitempty"`
    PreviewOnly      bool   `json:"preview_only,omitempty"`
    IncludeAnchor    bool   `json:"include_anchor,omitempty"` // true: keep anchor text as last user msg without resubmit
}

type BacktrackResult struct {
    SessionID              string   `json:"session_id"`
    Mode                   string   `json:"mode"`
    TruncatedToMessageCount int     `json:"truncated_to_message_count"`
    RemovedMessageCount    int      `json:"removed_message_count"`
    RemovedUserTurns       int      `json:"removed_user_turns"`
    AnchorPreview          string   `json:"anchor_preview,omitempty"`
    EditedPrompt           string   `json:"edited_prompt,omitempty"`
    CodeRestore            *checkpoint.RestoreResult `json:"code_restore,omitempty"`
    Warnings               []string `json:"warnings,omitempty"`
    EventsEmitted          []string `json:"events_emitted,omitempty"`
}
```

#### 6.2.2 Phase 2：给 Message 增加稳定 ID

建议在 `types.Message.Metadata` 或一等字段加入：

- `message_id`
- `turn_id`（user message 生成，后续 assistant/tool 继承）

兼容策略：

1. 新消息写入时生成；
2. 旧会话按“可见历史中的 user 序号”定位；
3. API 同时接受 `message_id` 与 `user_turn_index`。

### 6.3 Turn 识别算法

对 `session.GetMessages()`（可见历史）：

```text
userTurns = []
for i, msg := range messages:
  if msg.Role == "user":
    userTurns.append({Index: len(userTurns), MessageIndex: i, Preview: truncate(msg.Content)})
```

Backtrack 到 `userTurns[k]`：

- 默认 prefix = `messages[: userTurns[k].MessageIndex]`
- 若 `IncludeAnchor=true` 且无 edit：prefix 含锚点
- 若提供 `EditPrompt`：
  - prefix 到锚点前
  - 可选立即 `Submit(EditPrompt)` 或只把文本返回给 UI composer

注意：

- tool/assistant 中间消息从属于前一个 user turn；
- system / 本地 injection 消息需明确策略：默认随 history 一起按 index 处理，不单独暴露为可回退锚点。

### 6.4 Code restore 映射算法

目标：从 user turn 锚点推断“之后发生过哪些文件变更”。

推荐算法（务实可落地）：

1. 读取 session checkpoints，按 `created_at` 排序；
2. 每个 checkpoint 有 `MessageCount`（mutation 前历史长度）与可选 exact conversation；
3. 设锚点前缀长度为 `prefixLen`；
4. 选 **最后一个满足 `MessageCount <= prefixLen` 的 checkpoint** 作为 code base；
5. 若存在更早 exact conversation 可辅助校验；
6. 调用现有 `Restore(mode=code|both)`：
   - 现有实现会 reverse **目标之后** later checkpoints，这正适合“回到锚点附近的某次 mutation 前”；
7. 若锚点之后没有任何 checkpoint：
   - conversation 仍可 backtrack；
   - `warnings` 提示 “no file checkpoints after anchor”。

边界：

- 纯对话 turn 之后若无 mutation，code restore 为空成功；
- shell 大目录/超预算 skipped 文件，必须在 result.Warnings/Errors 暴露；
- 不承诺 git clean 级别完整还原。

### 6.5 Actor 编排伪代码

```go
func (a *SessionActor) handleBacktrack(cmd Backtrack) {
    // 1. reject if running unless interrupt requested
    // 2. load session visible history
    // 3. resolve anchor (message_id | message_index | user_turn_index)
    // 4. build prefix history
    // 5. if mode includes code: plan + restore via checkpoint manager
    // 6. ReplaceHistory(prefix); SetHeadOffset(0); persist
    // 7. clear stale runtime state:
    //      CurrentTurnID, pending approval/question/tool, frozen tools, token high-water if needed
    // 8. publish backtrack_started/finished (and optionally reuse rewind_* with reason=user_turn)
    // 9. optional auto-submit edited prompt
}
```

必须清理的运行态：

- `CurrentTurnID`
- `PendingApproval` / `PendingQuestion` / `PendingTool`
- `FrozenTurnTools*`
- 过期 tool receipts 的“当前 turn”关联
- 可选：context token count 重估

不建议在 backtrack 时静默继续旧 run。

### 6.6 事件模型

新增（推荐）或扩展现有 rewind 事件 payload：

```json
{
  "reason": "user_turn_backtrack",
  "session_id": "...",
  "mode": "both",
  "user_turn_index": 2,
  "message_index": 7,
  "truncated_to_message_count": 7,
  "removed_message_count": 12,
  "edited": true,
  "code_restore": {
    "checkpoint_id": "chk_...",
    "applied_paths": ["a.go"],
    "errors": []
  }
}
```

事件名建议：

- `backtrack_started`
- `backtrack_finished`
- hook：`backtrack_completed`

短期为减少改动，也可复用：

- `rewind_started` / `rewind_finished`
- 但必须带 `reason=user_turn_backtrack`，避免 UI 把它当成纯 checkpoint restore。

### 6.7 API 设计

#### `GET /api/runtime/sessions/{id}/turns`

返回 user turns 列表，供 UI/CLI 选择。

#### `GET /api/runtime/sessions/{id}/backtrack/audit`

返回 durable tombstone 审计摘要（oldest-first；不包含被截断正文）。

响应：

```json
{
  "session_id": "...",
  "count": 1,
  "entries": [
    {
      "id": "bt_...",
      "created_at": "2026-07-26T00:00:00Z",
      "mode": "conversation",
      "user_turn_index": 1,
      "message_index": 2,
      "anchor_preview": "second",
      "truncated_to_message_count": 2,
      "removed_message_count": 2,
      "removed_user_turns": 1
    }
  ]
}
```

#### `POST /api/runtime/sessions/{id}/backtrack/preview`

Body：

```json
{
  "user_turn_index": 2,
  "mode": "both",
  "edit_prompt": "改成这样"
}
```

#### `POST /api/runtime/sessions/{id}/backtrack`

Body 同上，另加：

```json
{
  "auto_submit": true
}
```

响应统一 `BacktrackResult`。

兼容：

- 现有 `/checkpoints/{id}/restore` **保留**，作为底层/高级入口；
- backtrack API 是产品主入口。

### 6.8 aicli 交互

#### Phase 1（可马上做）

```text
/backtrack                 # 交互选择（Esc 空输入等价；非交互则 list）
/backtrack list            # 列出 user turns
/backtrack select          # 强制打开选择器
/backtrack audit           # 列出 durable tombstone 审计摘要
/backtrack 3               # preview conversation-only
/backtrack 3 --apply       # 截断到 turn 3 前，预填原文本
/backtrack 3 --both --apply
/backtrack 3 --edit "新提示" --submit
```

别名：`/rewind` → 若参数像 checkpoint id 则走旧 checkpoint restore；若是数字则走 user-turn backtrack。

#### Phase 2（TUI 增强）

- 在 transcript/message 渲染中为 user 行提供可选择标记；
- 或 Esc 进入 “选择历史 user turn” 模式（对齐 Codex，依赖 fixed-bottom/composer 成熟度）。

### 6.9 Frontend

最小闭环：

1. `message-list` 每条 user message 增加 “回退到此”；
2. 打开确认对话框：
   - 将删除 N 条消息 / M 个 turns
   - 是否同时恢复文件
   - 是否把原文本填入 composer
3. 调用 backtrack API；
4. 刷新 history + checkpoints + runtime events。

同时补齐 checkpoint panel 的显式 Restore 按钮（高级用户入口），但文案区分：

- **回退到用户消息**（推荐）
- **恢复检查点**（高级/工具变更点）

## 7. 分阶段实施

### Phase 0：契约与定位（0.5–1 天）

交付：

1. 本文档评审确认；
2. 冻结术语：`user_turn_index` / `mode` / in-place 语义；
3. 明确 busy session 策略：拒绝 or interrupt-then-backtrack。

验收：

- 产品/实现双方对 “锚点是否包含原 user message” 达成一致。  
  **建议默认：编辑重发时不保留旧锚点；纯回退预填时也不保留，由 composer 持有文本。**

### Phase 1：Runtime 核心 backtrack（2–4 天） — **done**

交付：

1. `TurnIndexer` + `BacktrackRequest/Result`（`internal/chat/backtrack.go`）
2. `SessionActor.Backtrack` / `PreviewBacktrack` / `ListTurns`（`backtrack_actor.go`）
3. history 截断 + runtime state 清理
4. 单元测试：中间/首/末 turn、edit、auto_submit、busy 拒绝

验收：

- `go test ./internal/chat/ -run Backtrack` 绿。

### Phase 2：Code restore 联动（1–3 天） — **done（映射 + both/code）**

交付：

1. turn → checkpoint 映射（`BaseCheckpointID` / later ids）
2. `mode=both|code` 调用现有 `checkpoint.Manager.Restore(code)`
3. warnings：无 checkpoint / partial failure 保留 conversation 截断

验收：

- conversation 优先；code restore 失败不回滚历史。

### Phase 3：HTTP API + skillsapi client（1–2 天） — **done**

交付：

1. `GET .../turns`、`GET .../backtrack/audit`、`POST .../backtrack/preview`、`POST .../backtrack`
2. `pkg/skillsapi`：`ListSessionTurns` / `ListSessionBacktrackAudit` / `PreviewSessionBacktrack` / `ApplySessionBacktrack`
3. handler tests（含 busy → 409；audit empty→after-apply）

验收：

- runtime-server 与 actor 同一路径。

### Phase 4：aicli slash 命令（1–2 天） — **done**

交付：

1. `/backtrack` 列表、preview、apply、`select`、`audit`；`/rewind <N>` 数字参数别名
2. `--both` / `--edit` / `--submit`；apply 后 composer 预填
3. catalog + argument completion（含 `select` / `audit`）
4. 空 composer Esc → 交互选择器（Phase 6）

验收：

- `go test ./cmd/aicli/commands/ -run 'Backtrack|ChatSlashCommandCatalog'` 绿。

### Phase 5：Frontend 闭环（2–4 天） — **done**

交付：

1. message 级 backtrack action（user bubble `Backtrack`）
2. preview + `window.confirm` 确认
3. apply 后 history 刷新 + composer 预填
4. checkpoint panel restore 按钮（conversation / code / both）
5. `backtrack_finished` / `rewind_finished` 触发 checkpoint 列表刷新

验收：

- `pnpm test` 覆盖 backtrack helpers + message list action。

### Phase 6：稳定 ID 与体验打磨（可选，2–5 天） — **done（2026-07-26）**

交付：

1. message_id / turn_id — **done**：`types.EnsureMessageIdentity` / `EnsureHistoryMessageIdentities`；`Session.AddMessage` / `ReplaceHistory` / `loadSession` + `GetHistoryPage` 懒回填；backtrack `message_id` 解析 + 单测；frontend history 映射优先 `metadata.message_id`
2. 旧会话 fallback — **done**：index 仍可用；unknown/synthetic `message_id` 在同时带 `user_turn_index`/`message_index` 时回退 index（兼容 pre-backfill UI）
3. TUI Esc 选择模式 — **done**：
   - 空 composer 上 bare Esc → `ErrInteractiveInputBacktrackRequested` → `handleInteractiveBacktrackSelect`
   - fullscreen list（可搜索）优先；plain 序号输入 fallback
   - `/backtrack`（交互空参）与 `/backtrack select|pick|ui` 等价入口
   - apply conversation（或有 later mutation 时可选 both）并 prefill composer
4. 审计日志 / tombstone — **done**：
   - 物理截断前写轻量 `BacktrackTombstone`（不保留全文，仅摘要 + capped message_id/turn_id）
   - 持久化于 `session.Metadata.Context["backtrack_audit_log"]`（ring buffer，最多 20 条）
   - apply 成功后暴露于 `BacktrackResult.tombstone`、`backtrack_finished`/`rewind_finished` payload
   - `SessionActor.ListBacktrackAudit` 可读历史 tombstone；preview / code-only 不写 tombstone
   - `GET /api/runtime/sessions/{id}/backtrack/audit` + skillsapi/frontend client + aicli `/backtrack audit|tombstones|history`
   - slash catalog/completion 含 `select` / `audit`
   - **frontend audit 面板 residual（2026-07-26）**：`useRuntimeCheckpoints` 已加载的 tombstone 列表接到 Artifact **Restore** 表面（Backtrack audit 列表 + tombstone detail）；Restore tab 计数徽标；`artifact-panel-shared` format helpers + 单测

### Phase 6 residual：Frontend audit 面板 — **done（2026-07-26）**

交付：

1. Restore 表面新增 **Backtrack audit** 区（与 Restore points 并列）
2. 展示 durable tombstone：anchor preview / turn·message index / removed counts / mode / edited / base+later checkpoints / removed ids（capped）
3. `backtrack_finished` / `rewind_finished` 事件仍驱动 hook 重载（既有 `shouldReloadBacktrackAudit`）
4. 文案明确：tombstone 仅审计摘要，**不**恢复被截断正文

验收：

- `pnpm test`：`artifact-panel-shared` backtrack audit helpers 绿

### Phase 6 residual：Dialog edit_prompt — **done（2026-07-26）**

交付：

1. Backtrack 确认对话框可编辑原 user prompt（textarea 预填 `fullText`）
2. 仅当文本相对原文变化时下发 `edit_prompt`（`resolveBacktrackEditPrompt`）
3. apply 后 composer 优先预填 `composer_prompt` / `edited_prompt` / 编辑框文本
4. workspace-page / WorkspaceShell 接线 `setBacktrackEditPrompt`

验收：

- `pnpm test`：`use-session-backtrack` helpers（含 edit_prompt 差分）绿

### Phase 6 residual：Frontend transcript 内联导航 — **done（2026-07-26）**

交付：

1. 空 composer 上 bare Esc 进入 transcript 选轮模式（对齐 Codex / aicli Esc）
2. ↑/↓ 或 j/k 在 user turns 间移动；Enter 打开既有 Backtrack 对话框（含 edit_prompt）
3. Esc 取消选轮；选中 bubble 高亮 + `data-backtrack-selected` + scroll-into-view + 点击选中 / 双击确认 backtrack（导航模式与普通模式均可）
4. 其他 `aria-modal` 对话框打开时不抢 Esc；导航 helpers 单测覆盖
5. 非空 focused textarea/input（含 bubble 内联编辑）时不抢 Esc

验收：

- `pnpm test`：`use-session-backtrack` navigation helpers + `message-list` 选中高亮 / `data-backtrack-selected`

### Phase 6 residual：Frontend transcript 内联编辑 — **done（2026-07-26）**

交付：

1. user bubble 增加 **Edit** 按钮，展开 bubble 内 textarea（预填 full text）
2. **Continue to backtrack** 打开既有确认对话框，并以编辑文本 seed `editPrompt`
3. `useSessionBacktrack.backtrackToMessage(id, mode, { editPrompt })` + `resolveSeededBacktrackEditPrompt` 支持可选预填
4. 导航模式 / 响应中自动关闭内联编辑，避免状态冲突
5. bare Backtrack 仍 seed 原文；仅相对原文变化时 apply 下发 `edit_prompt`

验收：

- `pnpm test`：`message-list` Edit action 渲染；`use-session-backtrack` seed + edit_prompt 差分仍绿

## 8. 关键实现细节

### 8.1 与 `HeadOffset` 的关系

当前 rewind conversation 最终走 `ReplaceHistory` + `SetHeadOffset(0)`，是**物理截断**，不是长期保留隐藏尾部。

Backtrack 应对齐这一事实：

- 默认物理截断；
- 同时写入轻量 tombstone/审计摘要（`backtrack_audit_log`），**不**恢复被截断正文；
- 若未来要 “可前进/可撤销 backtrack”，需另做 history ledger，不在本计划 MVP 范围。

### 8.2 与 compact 的关系

compact 会改写历史并写入 lineage metadata。Backtrack 在 compact 后仍以**当前可见历史**的 user turns 为准，不尝试穿越 compact 前的原始 turns。

UI 需提示：

> 该会话已经 compact，可回退范围仅限当前压缩后的可见历史。

### 8.3 与 multi-agent / team 的关系

MVP 仅保证：

- 当前 session in-place backtrack。

后续扩展：

1. 若 session 是 parent 且存在 children：
   - 默认警告 “child sessions 不会自动回退”；
   - 可选提供 `cascade=false|true`。
2. team task 已产生的外部副作用不自动撤销。

### 8.4 事务与失败策略

推荐：

1. **先 preview**；
2. apply 时：
   - 先持久化 conversation 截断（可回读验证）；
   - 再执行 code restore；
   - code restore 部分失败：conversation 保持截断，result 标记 partial；
3. 若 conversation 持久化失败：整单失败，不做 code restore。

理由：文件 restore 本就可能 partial；对话一致性优先。

### 8.5 权限与安全

- backtrack conversation：需 session 写权限 / 同用户；
- backtrack both：等同可写工作区，遵守 folder trust / permission mode；
- preview 只读。

### 8.6 Token / 模型状态

截断后：

- 重算或清空 observed context token count；
- 不自动继承被删 turns 的 tool surface freeze；
- 模型/provider 偏好保持不变。

## 9. 测试计划

### 9.1 单元测试

- turn 识别：交错 tool/assistant/user
- 锚点解析：index / message_index / 非法值
- history 截断内容精确比对
- runtime state 清理
- checkpoint 映射：
  - 锚点后无 checkpoint
  - 锚点后多个 checkpoint
  - legacy only MessageCount
  - exact conversation 可还原

### 9.2 API 测试

- preview 不改持久化
- apply 改 history
- busy session 409/冲突
- lease conflict 与现有 session lease 一致

### 9.3 CLI 测试

- `/backtrack` 列表
- `/backtrack N --apply` 后下一条 prompt 基于新历史
- busy 时拒绝

### 9.4 Frontend 测试

- user message action 渲染
- confirm dialog 文案
- apply 后 message list 缩短
- both 模式展示 applied_paths / errors

### 9.5 手工 smoke

1. 纯对话 3 turns，回退到 turn 2，改口重说；
2. turn 2 后改文件，both 回退，确认文件恢复；
3. shell 超大目录变更，确认 warnings；
4. compact 后再 backtrack；
5. runtime-server 与 aicli 对同一 session 分别触发（验证共用机制）。

## 10. 里程碑与建议排期

| 里程碑 | 内容 | 预估 |
|---|---|---|
| M1 | Actor + 单测 conversation backtrack | 3 天 |
| M2 | code 联动 + API | 3 天 |
| M3 | aicli `/backtrack` | 2 天 |
| M4 | frontend 闭环 | 3 天 |
| M5 | message id / 体验打磨 | 按需 |

合计 MVP（M1–M4）：约 **1.5–2.5 周**。

## 11. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| 无稳定 message id | UI 刷新后 index 漂移 | 尽快上 message_id；UI 以 apply 前 snapshot 为准 |
| checkpoint 覆盖不全 | both 模式用户误以为 git reset | 文案明确 partial；preview 展示 skipped |
| busy/tool 进行中回退 | 状态撕裂 | 默认拒绝；提供 interrupt 后 backtrack |
| 与 child agent 历史分叉 | parent 回退 child 仍旧 | 警告；后续 cascade |
| 把 checkpoint restore 当 backtrack 卖 | 产品语义混乱 | API/文案分层：Backtrack vs Restore Checkpoint |
| 物理删除历史不可撤销 | 误操作 | preview + confirm；后续可加 soft ledger |

## 12. 推荐实施顺序（执行清单）

1. **先做 `SessionActor.Backtrack` conversation-only**  
   直接复用 `ReplaceHistory`，不要先改 UI。
2. **加 turns 列表与 preview API**  
   让 CLI/前端有选锚点依据。
3. **映射 checkpoint 做 both 模式**  
   复用 `checkpoint.Manager.Restore`，不要重写 reverse 算法。
4. **aicli `/backtrack`**  
   最快获得开发者可用闭环。
5. **frontend message 操作**  
   对齐 Codex 的用户心智。
6. **再考虑 message_id 与 Esc TUI**  
   体验升级，不堵 MVP。

## 13. 成功标准

MVP 成功，当且仅当：

1. 用户能针对任意历史 user turn 做 in-place 回退；
2. 回退后会话继续，模型看不到被删 turns；
3. 可选文件恢复走现有 checkpoint 体系并暴露 partial 结果；
4. runtime API、aicli、frontend 至少两条路径可用；
5. 与旧 checkpoint restore 兼容共存，不破坏现有测试；
6. 文档明确：这是 **user-turn backtrack**，不是完整 VCS time travel。

## 14. 附录：关键代码锚点

- 会话：`backend/internal/chat/session.go`
- Actor rewind：`backend/internal/chat/actor.go` (`Rewind`, `handleRewindTo`, `rollbackLastUserPrompt`)
- Checkpoint：`backend/internal/checkpoint/manager.go`
- Mutation 捕获：`backend/internal/agent/approved_tool.go`
- HTTP checkpoint：`backend/internal/api/skills/checkpoint_handlers.go`
- 路由注册：`backend/internal/api/skills/handler.go`
- Frontend checkpoint：`frontend/src/hooks/workspace/use-runtime-checkpoints.ts`
- Frontend surface：`frontend/src/components/workspace/artifact-panel-checkpoint-surface.tsx`
- Message list：`frontend/src/components/workspace/message-list.tsx`

## 15. 一句话结论

> 本项目已经具备 checkpoint 级 rewind 底座，但要对齐 Codex 的 Esc backtrack，必须新增 **user-turn 锚点 + 历史截断编排 + 可选 code restore 映射 + CLI/API/UI 入口**；其中历史截断与文件 reverse 大多可复用现有 `ReplaceHistory` 与 `checkpoint.Manager.Restore`，真正缺的是产品语义层与 turn 级 API，而不是从零实现恢复引擎。
