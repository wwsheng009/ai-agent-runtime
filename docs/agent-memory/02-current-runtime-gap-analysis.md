# 当前 Runtime 记忆能力与差距分析

> 状态：分析文档  
> 基线：`backend` 当前实现与 `main@9abf08fb`  
> 目的：明确哪些能力已经存在、哪些应复用、哪些必须新增。

## 1. 结论先行

AAR 已经有一套“项目级手动记忆”的 MVP，但还不是 Codex 式跨会话记忆系统：

| 能力 | 当前状态 |
| --- | --- |
| 运行期 observation memory | 已有，进程内、有界 |
| 项目级 durable notes | 已有，`notes.jsonl`，手写为主 |
| 关键词检索 | 已有，简单 token overlap |
| 上下文自动注入 | 已有，受 token 预算控制 |
| `/memory` 命令 | 已有 status/add/list/search |
| Web 管理 | 已有 Settings → Harness → Memory |
| HTTP API | 已有 `/api/runtime/harness/memory` |
| 会话历史 | 已有 SQLite canonical transcript 与 compact |
| 事实证据 | 已有 artifact / fact ledger |
| 代码探索记忆 | 已有 knowledge layer |
| 自动会话抽取 | **缺失** |
| Consolidation / 摘要编译 | **缺失** |
| 记忆去重/冲突/更新/遗忘 | **缺失** |
| 记忆新鲜度/过时检测/回源验证 | **缺失** |
| 引用与 usage 反馈 | **缺失** |
| 后台任务/租约/重试 | **缺失** |
| 项目级 SQLite 记忆库 | **缺失** |
| 跨项目/用户级 scope | **缺失** |
| 记忆专用 Agent 工具 | **缺失** |
| 脱敏与记忆审计 | **不完整** |

所以本项目的正确方向不是“重写记忆”，而是**在现有 memorystore 上增加写入管线、SQLite 存储、摘要编译、反馈和治理**。

## 2. 当前已有能力

### 2.1 `internal/memory`：运行期 observation memory

文件：`backend/internal/memory/memory.go`

特点：

- 进程内 `[]types.Observation`；
- 按 `maxSize` 截断最旧 observation；
- 支持 recent/search/filter/stats；
- session map 仅进程内；
- 不跨进程、不跨会话持久化。

定位：**工作记忆/观察缓存**，不是长期记忆。`agent.enableMemory` 和 `agent.maxMemorySize` 应继续表示这一层，不能与新 memory v2 混用。

### 2.2 `internal/memorystore`：项目级 durable notes

文件：`backend/internal/memorystore/store.go`

现状：

- 存储路径默认 `<project>/.aicli/memory/notes.jsonl`；
- 追加 `Note`，字段为 id/text/tags/source/session_id/created_at；
- 搜索是关键词 token overlap + 时间排序；
- `SelectForInject` 按 query 和 token budget 选择；
- `FormatNotes` 生成 prompt block；
- 没有 update/delete/forget、去重、引用、来源证据、后台任务；
- `notes.jsonl` 是唯一事实源，损坏行会被跳过，但没有修复/校验接口；
- 只有手写入口，没有自动生成入口。

定位：**项目级人工笔记 MVP**，是 memory v2 的兼容入口与迁移对象。

### 2.3 `contextmgr`：上下文注入与预算

文件：`backend/internal/contextmgr/manager.go`、`facts.go`、`compact.go`

现状：

- `ProjectMemoryStore` 只依赖 `SelectForInject`；
- `Manager.Build` 会把 project memory 追加到 raw history 之后的动态尾部；
- 如果本回合已有 `project_memory` stage 或存在 active-turn replay，会抑制重复注入；
- `Budget.MaxProjectMemory` / `ProjectMemoryTokens` 已有默认值：
  - balanced：5 条 / 600 tokens；
  - compact：3 条 / 400 tokens；
  - extended：8 条 / 900 tokens；
- 注入会写 metadata 和 `context.project_memory.*` 事件；
- prompt 布局明确要求动态层不能插入 raw history 之前。

这是 memory v2 的重要基础：新的摘要和检索结果应继续复用这个动态尾部位置，而不是新建前置层。

### 2.4 `artifact` + `factledger`：证据型事实

文件：`backend/internal/artifact/store.go`、`backend/internal/contextmgr/facts.go`

已有：

- `MemoryEntry`：session/task/kind/priority/content/source_refs/source_hash；
- `memory_entries` SQLite 表；
- `factledger` 具备 scope、confidence、evidence refs、valid time；
- `buildFactLedgerMessage` 只注入 active facts，并放在 history 之后。

定位：**已验证事实**。Agent Memory v2 应引用 fact ID / artifact ID，而不是把同样的内容复制成新记忆。

### 2.5 compact / checkpoint

文件：`backend/internal/contextmgr/compact.go`、`backend/internal/chat/compact_boundary.go`、`backend/internal/compactruntime/`

已有：

- canonical transcript 追加式保留；
- prompt projection 可压缩；
- summary checkpoint、compaction boundary、segment start/end；
- durable fact ledger 在 compact 后重新注入；
- `ReplaceHistory()` 与 prompt cache epoch。

定位：**会话内上下文压缩**。它可以作为记忆抽取的输入边界，但 compact summary 不是跨会话长期记忆。

### 2.6 `knowledge`：代码探索记忆

文件：`backend/internal/knowledge/`

已有：

- SQLite、FTS5、索引、exploration memory、Planner；
- `memorystore` / `factledger` / exploration memory 的三层分离已经写在 `docs/knowledge_Layer/README.md`；
- 有上下文快照和来源归因。

定位：**代码探索/知识复用**。Agent Memory v2 不应吞并 knowledge layer，只应通过来源引用与其协作。

### 2.7 会话存储与控制面

已有：

- `chat.SessionStorage` 接口和 SQLite canonical transcript；
- session metadata、runtime state、checkpoint；
- `/api/runtime/harness/memory` GET/POST；
- `/memory` slash 命令；
- Settings 中的 memory section；
- session events、artifact search、compact events。

这些为记忆状态、审计和 UI 提供了现成基础。

## 3. 关键差距

### 3.1 缺少“从会话生成记忆”

当前 notes 只来自：

- `/memory add`；
- Web Settings append；
- 可能的测试/手工写入。

没有：

- 会话结束或空闲后的自动抽取；
- 从 canonical transcript 提取偏好、决策、失败教训；
- 对无关会话的筛选；
- 结构化输出校验；
- 生成结果的证据链与脱敏。

### 3.2 缺少“记忆生命周期”

当前 `Note` 是 append-only 且不可变：

- 不能纠正；
- 不能删除/遗忘；
- 不能标记 superseded；
- 不能合并重复项；
- 不能区分 manual/derived/candidate；
- 不能追溯来源。

这会导致长期记忆噪声和冲突累积。

### 3.3 缺少全局 Consolidation

当前没有：

- 项目级或用户级摘要；
- 去重与冲突消解；
- 最近使用/使用次数排序；
- 未使用过期淘汰；
- 多来源合并；
- 摘要与记录的一致性管理。

### 3.4 缺少记忆专用工具

当前 Agent 只能依靠：

- 自动注入的 top-k notes；
- 文件系统/`/memory` 命令。

这带来两个问题：

- 不能按需搜索、读取来源、查看记忆详情；
- 无法精确追踪“Agent 使用了哪条记忆”。

### 3.5 缺少引用反馈

当前注入只写 `project_memory_ids` metadata，没有：

- 最终答案 citation；
- `last_used_at` / `use_count`；
- 按使用反馈排序；
- 记忆命中/未命中审计。

### 3.6 缺少任务与并发治理

没有：

- extraction/consolidation job 表；
- lease、retry、backoff；
- source watermark；
- 多进程互斥；
- 失败可观察性。

AAR 同时有 CLI、runtime-server、Web、ACP 和后台任务，因此这是必需能力，不是可选优化。

### 3.7 存储形态不适合自动生成

`notes.jsonl` 的问题：

- 没有事务和索引；
- 全量扫描；
- 无 schema 版本；
- 无原子更新；
- 无关联查询；
- 多进程并发追加难以治理；
- 不能承载来源、usage、job、summary。

需要 SQLite，且应使用项目已有 `sqliteutil` 的 WAL/busy_timeout/单写者基线。

### 3.8 scope 与隔离不足

当前默认 project root，但没有：

- project/user/profile 明确 scope；
- workspace 稳定 ID；
- 敏感级别；
- scope 优先级；
- 跨项目泄漏测试；
- 全局记忆 opt-in。

### 3.9 脱敏与安全不完整

当前 memory notes 没有统一的：

- secret 扫描；
- URL/API key/token 脱敏；
- 敏感路径过滤；
- 记忆内容“仅作为数据”包装；
- 防 prompt injection；
- 删除审计。

## 4. 可直接复用的基础

| 基础 | 复用方式 |
| --- | --- |
| `contextmgr.Manager.Build` | 保留 stage `project_memory`，增加 v2 metadata 和 summary/hybrid 选择 |
| `ProjectMemoryStore` 接口 | 先由兼容 facade 实现，不破坏调用方 |
| `memorystore.Store` API | 保留 Append/List/Search/SelectForInject 语义，底层切到 v2 store |
| `sqliteutil` | WAL、busy_timeout、单连接、锁重试 |
| `migrate` | 新 memory DB schema 迁移 |
| `events.Publisher` | 复用 `context.project_memory.*`，新增 memory.* 事件 |
| artifact / factledger | 作为证据引用，不复制事实 |
| chat canonical transcript | 作为 Phase 1 输入 |
| compact checkpoint | 定位摘要覆盖区间，避免重复抽取 |
| runtime API / frontend | 扩展 memory 管理面和状态面 |
| `/memory` 命令 | 保持兼容，增加 show/forget/why/export |
| 现有测试 | 加 v2 回归、迁移、隔离、引用和 prompt cache 用例 |

## 5. 不应做的事

1. 不把 `internal/memory` 直接改造成持久化长期记忆；
2. 不把 `factledger` 当成自由文本记忆库；
3. 不把 `knowledge` 的 exploration memory 复制进 memorystore；
4. 不绕过 `contextmgr` 在 system prompt 前面插入记忆；
5. 不在普通对话同步调用抽取模型；
6. 不依赖 `.aicli/` 被 Git 跟踪来判定记忆变化；
7. 不让模型直接修改任意记忆文件或 SQLite；
8. 不在未启用的情况下静默生成或注入；
9. 不把“记忆命中”当成“用户授权”。

## 6. 差距对应的方案映射

| 差距 | 目标方案 |
| --- | --- |
| 自动抽取 | Phase 1 extractor + job 表 + idle/session-end 触发 |
| 生命周期 | `memory_entries` 状态机 + supersede + tombstone |
| 新鲜度 | `freshness_status` + source hash/revision + TTL + summary watermark |
| Consolidation | Phase 2 项目级摘要与去重 |
| 工具 | `memory_search` / `memory_read` / `memory_add` |
| 引用反馈 | citation parser + `memory_usage` |
| 并发治理 | `memory_jobs` + lease + watermark + 单写者 |
| 存储 | `memory.db` + FTS5 可选 + Markdown 导出 |
| scope | project 默认，profile/user 显式 |
| 安全 | 脱敏、数据包装、权限隔离、审计 |
| 兼容 | memorystore facade + notes.jsonl 幂等迁移 |

## 7. 实施建议

先做 M1 的存储与读取门面，确保：

- `off` 行为不变；
- 现有 `/memory` 和 Web 面板仍可用；
- `notes.jsonl` 可无损迁移；
- contextmgr 注入位置不变。

然后再做 M2 自动抽取和 M3 Consolidation。不要先做“自动写记忆”而没有删除、去重、证据和回滚能力。
