# Nuphus、DeepSeek Harness 与 ai-memory 记忆能力对比及借鉴建议

> 状态：外部方案对比（Docs Only）  
> 审查对象：
> - `E:\projects\ai\nuphus`，`main@76f4ca9`
> - `E:\projects\ai\deepseek-harness`，`master@d743267388`
> - `E:\projects\ai\ai-memory`，`main@1c86aebc`
> 对比基线：本项目 `docs/agent-memory/` Agent Memory v2 方案  
> 结论：**Nuphus 更适合借鉴“分层长期记忆 + 工作记忆日志 + 多路检索”；ai-memory 更适合借鉴“memory product 生命周期、零 LLM 老化、分层 brief、handoff 与审计”；DeepSeek Harness 更适合借鉴“会话检索、跨会话引用、压缩预算、spill 与可观测性”。**

## 1. 总体判断

| 项目 | 是否有内置长期记忆 | 主要强项 | 对本项目价值 |
| --- | --- | --- | --- |
| Nuphus | **有** | SQLite + FTS5 + 可选向量、Kind 分层、项目隔离、工作记忆 MD、内存检索工具、Tenet、记忆 UI | 高，适合借鉴 L2/L3 记忆模型与读取闭环 |
| ai-memory | **有完整长期记忆产品** | Git-backed Markdown wiki + SQLite 派生索引、生命周期 hooks、handoff、零 LLM aging、分层 brief、retrieval fusion、审计 | 很高，适合借鉴记忆生命周期、分层注入、检索与治理 |
| DeepSeek Harness | **没有完整的原生语义记忆** | Session 事件日志、Session Query/FTS、Session Reference、Compaction、Spill、Token Meter、AGENTS 指令预算 | 高，适合借鉴检索、授权、预算、引用快照与压缩 |
| AAR Agent Memory v2 | 设计完成，代码未实现 | 两阶段抽取、summary、citation、suppression、pinned、recall trace | 需要吸收这些方案的优势，但保持项目本地优先与安全边界 |

---

## 2. Nuphus：内置分层记忆

### 2.1 存储架构

Nuphus 的记忆不是单一文件，而是两层：

```text
工作记忆（L1，文件）
  ~/.nuphus/memory/{project-tag}.md
  - append-only journal
  - 单文件上限 32 KiB
  - 新会话只注入最新尾部 ≤ 2000 字符

长期记忆（L2，SQLite）
  ~/.nuphus/.../memory.db
  - memory_entries 主表
  - memory_fts_v4 FTS5 索引
  - memory_embeddings 可选向量
  - 会话/项目/自定义 agent 元数据
```

代码锚点：

- `src/memory/entry.rs`
- `src/memory/mod.rs`
- `src/store/memory.rs`
- `src/agent/mod.rs` `load_cross_session_context`
- `src/tools/definitions/memory.rs`
- `frontend/src/main-window/memories/MemoriesPage.tsx`

### 2.2 记忆类型与生命周期

`MemoryKind` 分为：

| Kind | 内容 | 当前策略（从代码可见） |
| --- | --- | --- |
| `conversation` | 用户/assistant 对话对 | 永久保留 |
| `task_trace` | 紧凑执行轨迹 | 受总条目上限裁剪；注释意图为 90 天衰减 |
| `distill` | LLM 会话提炼摘要 | 永久保留 |
| `pattern` | 用户评分/验证后的可复用模式 | 永久保留 |
| `snapshot` | 历史工作快照 | 历史兼容保留 |

关键设计：

- 自动持久化对话与轨迹；
- 通过 `distill` 把会话压缩为可检索摘要；
- 通过用户评分将高质量执行沉淀为 `pattern`；
- `is_marked` 可豁免裁剪；
- `prune_entries()` 在总条目超过 10,000 时只删除最老的未标记 `task_trace`；
- `conversation` / `distill` / `pattern` 不参与自动裁剪。

### 2.3 工作记忆注入

`ReactAgent::load_cross_session_context()` 构造一个独立的 L1 注入块：

1. 项目记忆日志尾部（≤ 2000 字符）；
2. 最近 5 条 `distill` 标题 + ID，提示模型用 `memory_search` 自查；
3. 当前 session id、日志路径、其它项目记忆索引。

注入只在 Leader 模式且非 Workflow 模式生效，避免把 Leader/CI 记忆混入 Workflow 设计上下文。

这是很值得借鉴的结构：

```text
最近工作状态（短）
  + 可检索的历史标题（导航）
  + 明确 session id / 文件路径（定位）
```

它比“注入一大段长期记忆”更符合 token 和 prompt-cache 约束。

### 2.4 检索工具面

Nuphus 提供：

- `memory_search`：FTS5/BM25、可选语义检索、标签查询、session/goal/kind 过滤；
- `memory_recent`：最近条目，默认排除 `task_trace`；
- `memory_stats`：总数、成功率、agent/kind 分布；
- `memory_session_context`：按 session/turn 查看因果链和紧凑步骤；
- `leader::memory_update` / `workflow_memory_update`：追加工作日志。

重点可借鉴项：

1. **默认排除高噪声类型**：`memory_recent` 默认排除 `task_trace`；
2. **严格 AND + 零结果诊断**：返回逐词命中数，建议减少关键词或使用 semantic；
3. **标签式查询走 LIKE**，普通查询走 FTS5；
4. **语义检索失败可回退关键词**；
5. **session_id 精查不受项目过滤限制**，但跨项目默认不可见；
6. **工具结果只返回摘要 + score + id**，细节由 session context 读取。

### 2.5 项目与 agent 隔离

Nuphus 使用两层隔离：

- `session_meta.project_tag`：默认只查当前项目；
- `custom_agent_id`：
  - Custom 会话可访问“本卡片私有 + 项目公共”；
  - 非 Custom 会话排除所有 Custom 私有记忆；
  - 公共记忆不污染 Custom 私有层。

还支持 `all_projects=true` 显式跨项目查询，且先通过其它项目记忆索引获取线索。

### 2.6 Tenets：不可变用户原则

Nuphus 还有一层不同于普通记忆的 `TenetStore`：

- 最多 20 条；
- 用户确认后 `immutable=true`；
- 有 `priority` 和 `enforce_level`（Suggestion / Warning / Block）；
- 会注入 prompt；
- 工具调用前会做关键词匹配检查，Block 可阻断执行；
- 不参与普通记忆清理/演化。

重要边界：Tenet 属于**用户策略/规则层**，不是“记忆内容”。AAR 应借鉴“不可变/pinned”概念，但不应让普通 memory 直接影响工具权限。

### 2.7 可观测性与 UI

Nuphus 的记忆页面分为：

- Overview：总数、成功率、DB 大小、embedding 覆盖率、时间跨度、distill/pattern 数；
- Sessions：会话列表、日期分组、搜索、详情；
- Experience：snapshot / distill / review；
- 从经验跳回来源会话；
- 删除、评分、标签和 kind 展示。

这比只提供 `/memory` 文本命令更完整，能形成“生成 → 查看 → 追溯 → 评分 → 清理”的产品闭环。

### 2.8 Nuphus 可借鉴点

| 借鉴点 | 对 AAR 的价值 |
| --- | --- |
| 工作记忆 MD tail + 长期 SQLite 两层 | 补强 AAR 的 L1/L2 边界 |
| Kind + retention/prune 策略 | 补强 AAR 的运行期增长控制 |
| 高噪声 kind 默认排除 | 降低 task trace 污染 |
| 零结果诊断 | 提升召回可解释性 |
| session context 因果链 | 补强 AAR 的 evidence 查看 |
| 项目 + custom agent 双层隔离 | 补强 workspace/agent scope |
| 可选 embedding + HNSW | 可作为 v2 后续语义检索方向 |
| 记忆 UI 三分页和评分 | 补强 AAR 的可观测性/运营面 |
| Tenet 不可变层 | 借鉴 pinned/immutable，但要与权限层分开 |

### 2.9 不应直接照搬

- 自动保存全部对话可能带来隐私与成本问题；AAR 仍应 candidate + 脱敏 + opt-in；
- `tenet` 直接阻断工具属于策略层，不应混入普通 memory；
- `prune_entries` 当前主要按总条数裁剪，不能替代 AAR 的 retention/审计/suppression；
- 直接文件写入和 SQLite 双写需要幂等与并发保护；
- 语义检索会引入 embedding 模型/成本，AAR v1 不应默认开启。

---

## 3. DeepSeek Harness：会话检索与上下文工程

### 3.1 原生记忆定位

DeepSeek Harness **没有像 Nuphus/Codex 那样的内置长期语义记忆模块**。它的长期记忆通过 MCP memory server 外接：

- Memorix；
- MCP Reference Memory（本地知识图谱 JSONL）；
- Engram。

官方文档明确：

- 这三者是默认关闭的参考配置；
- DSH 负责 MCP 生命周期、工具发现和错误重连；
- 不负责记忆数据库、embedding、自动摘要、冲突消解或遗忘策略；
- 第三方 memory server 不提供统一摘要/遗忘/引用闭环。

这说明 DSH 把“记忆实现”交给了 provider，而不是在核心中固化。

### 3.2 Session 事件日志

DSH 的核心优势是会话事件模型：

- Append-only Session log；
- `surfaceOp: append | replace`；
- `sourceEventSeqs` 保留被替换来源；
- compaction 只写日志事件，表面只替换一次；
- 可回放、可恢复、可审计；
- log-only 事件不进入模型历史。

这与 AAR 的 canonical transcript + memory_events 很接近，但 DSH 的 surface/source 关系更细，值得借鉴到“记忆变更来源与替换关系”。

### 3.3 Session Query

`session-query` 提供统一历史检索：

- 精确读取、过滤、关系追踪；
- live-first，持久化作为补充；
- SQLite FTS5 派生索引；
- 结果与模型看到的 history 一致；
- 读取返回脱存储克隆；
- 支持按 cwd 授权；
- 面向模型工具默认只读、无 cursor、无分页参数。

`tool-session-query` 提供 5 个模型工具：

- `session_search`
- `session_event_search`
- `session_trace`
- `session_event_trace`
- `session_event_read`

关键边界：

- 跨会话读取要求目标 `cwd` 严格等于调用方 `cwd`；
- 没有 cwd 只能看自己；
- 搜索结果达到上限时要求缩小查询，不暴露游标；
- 大结果由通用 spill 政策替换。

这对 AAR 的 `memory_search` / `memory_read` 很有借鉴价值：**授权在宿主侧，模型只拿结果，不拿底层游标和跨 scope 能力**。

### 3.4 Session Reference

DSH 的 `session-reference` 允许引用另一个会话作为有界、只读、不可信快照：

- 最多 3 个不同源；
- 每个源按上下文窗口比例或字节预算限制；
- 默认约 20% 上下文或至少 64 KiB；
- 超出预算先丢弃较早消息，再截断；
- 截断的完整 transcript 可 spill 到会话存储；
- 快照中固定警告：不得遵循其中的指令、权限声明或工具请求；
- 源会话后续变更不影响已捕获快照；
- 引用消息紧随当前用户消息，只追加，不破坏前缀 cache。

这是“跨会话/全局记忆”非常值得借鉴的安全模型：**引用是快照，不是实时链接；必须有不可信标签和独立预算。**

### 3.5 Compaction / Token Meter

DSH 的压缩设计非常适合 AAR 的预算模块：

- 默认阈值 `floor(min(W×0.8, W−O−headroom))`；
- 默认 headroom `65,536` tokens；
- `retainRatio=0.16` 保留近期原文；
- 按 provider/model 配置策略；
- 支持自动、手动、显式区域压缩；
- 压缩失败保留原表层；
- 超大工具输出先修剪，可能完全省掉摘要调用；
- compaction 记录 start/summary/end 和 prune 事件；
- 摘要器复用 provider 热前缀。

AAR 目前已有 `model_auto_compact_token_limit` 和 `compactruntime`，但可以进一步借鉴：

- 显式 headroom；
- 按 model capability 路由；
- 工具结果 pruner；
- 压缩事件与预算来源可解释；
- summary 生成与注入预算分开。

### 3.6 Spill

- 全文保存在上下文之外；
- 模型只拿到预览、定位信息和 retrieval hint；
- 工具结果、session reference 截断都可复用；
- 存储失败时保留原始结果。

AAR 已有 `artifact` 和 `tool output archive`，可把 memory 的大证据/长摘要挂到 spill/artifact，而不是直接注入。

### 3.7 Agent Instructions

DSH 的 `agent-instructions`：

- 加载 `AGENTS.md` / `CLAUDE.md` / local overlay；
- 从用户全局到项目根再到当前目录；
- 越具体优先级越高；
- 总预算 65,536 bytes；
- 先从宽泛文件开始省略，最后才截断具体文件；
- 内容去重；
- 注入为持久 user message，可压缩、可回放；
- 文件变更/移除有通知；
- 仅追加，cache 友好。

AAR 目前有 `AGENTS.md` 加载，但可以借鉴“按来源/优先级/预算/刷新事件”的完整治理。

### 3.8 DSH 可借鉴点

| 借鉴点 | 对 AAR 的价值 |
| --- | --- |
| Session Query 统一读模型 | 替换/补强 `memory_session_context` 类能力 |
| 跨会话严格 workspace 授权 | 强化 project/cwd 隔离 |
| Session Reference 不可信快照 | 用户全局记忆/跨 project 引用的安全模型 |
| Spill + preview | 大记忆证据不直接进 prompt |
| Compaction token meter + headroom | 补强 AAR 总预算与模型策略 |
| Tool result pruner | 降低抽取/摘要成本和噪声 |
| AGENTS 指令分层预算 | 补强 L4/profile/项目指令层 |
| 事件 surfaceOp/sourceEventSeqs | 补强 revision/source 可追溯 |
| MCP memory provider 参考 | 可作为未来外部记忆后端 seam |
| 写入 A → 新会话 B → 召回 → 使用 | 最直接的长期记忆 E2E 验收 |

### 3.9 DSH 不应直接照搬

- 不能把长期记忆完全外包给 MCP；AAR 需要内置 L2/L3 与统一摘要/遗忘；
- `cwd` 字符串严格相等过严，AAR 已设计 canonical `project_key`；
- Session reference 引用的是会话快照，不是语义记忆；AAR 仍需 memory records；
- DSH 的 compaction 是会话内，不是跨会话长期记忆；
- 第三方 MCP server 不应直接获得 AAR 的 grant/permission 能力。

---

## 4. ai-memory：完整长期记忆产品

### 4.1 定位与总体架构

`ai-memory` 是一个独立 Rust 二进制/服务，面向 20+ coding harness，提供跨 agent、跨会话、跨机器、可选的团队共享长期记忆。

核心数据流：

```text
lifecycle hooks
  → sanitize / observation
  → sanitize boundary / idempotency / spool
  → session-end summary + handoff
  → optional LLM consolidation / auto-improve
  → git-backed Markdown wiki (source of truth)
  → SQLite derived index (FTS/entity/link/vector/session/handoff/audit)
  → SessionStart brief / retrieval / MCP tools
```

它最值得注意的不是“又一个记忆库”，而是把记忆当作一个有生命周期、可审计、可回滚、可评测的产品系统。

### 4.2 两层事实源：Markdown wiki + 派生 SQLite

- Markdown wiki 是**权威事实源**；
- SQLite 是可重建的派生索引；
- 页面在 git 中版本化；
- 删除是逻辑删除 + git 版本 + tombstone；
- `restore-page` 可从 git revision 恢复；
- 索引可 reindex，不要求从二进制库导出；
- 页面、会话、观察、handoff、audit 都在同一数据边界内。

AAR 当前选择 SQLite 作为事实源、Markdown 作为 materialized view，方向相反。**不建议在 v1 反转事实源**，但应借鉴：

- 可重建索引；
- 版本链/恢复；
- 页级审计；
- 导出与 reindex 契约；
- “删除”的真实语义必须公开。

### 4.3 生命周期捕获与幂等

- 生命周期 hooks 采集 prompt、tool、session boundaries；
- 网络写入在 hot path 上 fire-and-forget，短超时，429 而不是无限排队；
- 原生 hook 使用稳定 idempotency key；
- 失败 spool，session-end 由 detached drain 处理；
- 写入完成标记分阶段：wiki / handoff 效果可重放；
- 同一 project/key 有串行门，避免重试重叠；
- hook payload 先 sanitize，再进入存储；
- 捕获失败不会阻塞 agent 正常任务。

这对 AAR 的启示：

- 记忆抽取任务必须有 idempotency key；
- 触发与执行分离；
- 失败可重试但不热循环；
- 不能用“对话主路径同步写记忆”；
- source sanitize 必须在落库前，而不是展示前。

### 4.4 SessionEnd summary 与 claim-once handoff

ai-memory 在 session end 时：

1. 生成规则式 `sessions/<id>.md` 摘要（无 LLM）；
2. 创建一个 handoff 给下一个 agent；
3. 一个事务内插入 handoff、标记 session ended、记录 observation 覆盖数；
4. handoff 有 owner，默认 claim-once；
5. 下一个 SessionStart 注入 handoff；
6. 支持 manual handoff、offer-not-claim、过期和重发恢复；
7. 没有可靠 SessionEnd 的 harness 需要 `finalize-session`；
8. 长期未结束的 session 可由 idle maintenance 收口。

这对 AAR 的启示很强：

- “长期记忆”不只是可搜索页面，还应有**跨会话续作包**；
- 续作包应包含：进展、失败、未决问题、下一步、source refs；
- 续作包应有 owner 和一次性交付语义，避免多个 agent 重复消费；
- 规则式 fallback 必须在无 LLM 时可用。

### 4.5 Tiered SessionStart brief：非常值得借鉴的 token 控制

ai-memory 的 SessionStart brief 不是“注入所有高优先级页面”，而是 L0/L1/L2 渐进式：

- **L0**：frontmatter `abstract` → `summary` → leading prose，256 UTF-8 bytes；
- **L1**：`summary` → `abstract` → leading prose，500 UTF-8 bytes；
- **L2**：完整 page body；
- 默认预算 `max_chars=4000`，clamp 1500-20000；
- 先保证安全 preamble、untrusted fence、footer；
- pinned / `_rules/` / `_slots/` 先获得深度；
- 其他页面先 L0，再按预算升级 L1/L2；
- 放不下的页面以 path 指针列出，供 `memory_read_page` 深读；
- 同类页面可渲染为目录索引；
- 全流程零 LLM，固定决定，cache 友好。

这比 AAR 当前“summary + top-k records”更成熟，尤其值得借鉴：

- 同一预算下覆盖更多 standing rules；
- 摘要/正文分层，不直接截断一个长页面；
- 必需安全框架不可被内容挤掉；
- 省略项变成可检索指针，而不是静默丢失。

### 4.6 零 LLM memory aging

ai-memory 的 retention 公式：

```text
retention = salience · exp(−λ · age_days)
          + σ · ln(1 + access_count)
            · exp(−μ · days_since_access)
            · breadth
```

关键点：

- 默认 λ=0.02，约 35 天半衰期；
- access_count / last_accessed_at 是使用反馈；
- breadth 按 distinct actors 加权，避免单一用户/单次热度垄断；
- salience 可由 user feedback 调整；
- tiers：working / episodic / semantic / procedural；
- semantic/procedural 永不自动衰减，pinned 永不过期；
- 删除先经过 tombstone，180 天后硬删除；
- retention 主要用于 sweep，不一定直接参与 ranking；
- 默认零 LLM，access-weighted retention 默认开启；
- ranking 相关改动必须通过 recall eval。

AAR 当前已有 retention_class 和 maxUnusedDays，但可进一步借鉴：

- access-weighted retention；
- 按 tier/kind 的半衰期；
- distinct actor breadth；
- tombstone → hard delete 的双阶段；
- semantic/procedural 与 episodic 的不同策略。

### 4.7 冷记忆压缩、去重、矛盾检测

ai-memory 的零 LLM 老化路径包括：

- **extractive tier-down**：冷 episodic 页面保留 abstract、summary、文件路径/错误码/代码引用等 keep-token，删除低信号 prose，原版本仍可由 git/supersession 恢复；
- **DBSCAN 冷簇去重**：只对 `cold_episodic` 候选集做聚类，一个簇保留一个 survivor，其余 supersede；
- **entropy/boilerplate prefilter**：低信息观察在 consolidation 前过滤；
- **contradiction edges**：0.4–0.75 相似度带内记录 `contradicts` 边，lint 提示，不自动删除；
- **opt-in LLM dream pass**：只在 idle 时重写冷簇，源页面永不删除，必须过 eval gate，且取消于用户活动。

AAR 可借鉴的顺序：

1. 先做确定性 dedupe / extractive compression；
2. 再做 contradiction flag；
3. LLM rewrite 放到 v1.1/后续，并保留 source；
4. 任何 lossy/ranking 改动都必须有 eval gate。

### 4.8 多路检索与 explain

ai-memory 的 `memory_query` 融合：

- FTS5；
- lexical entity match；
- link-neighbor graph；
- optional vector cosine；
- raw observation fallback；
- 事务维护的 CJK unigram/bigram FTS；
- RRF 融合；
- bounded authority multiplier（kind/tier/pinned/正负标签）；
- optional LLM rerank（有界、失败保留本地顺序）；
- access reinforcement（查询、读取、related-walk 都计使用）；
- `explain` 输出 streams_active、ranks、RRF 贡献。

这比 AAR 的 FTS/LIKE + 单分数更成熟。AAR 应优先借鉴：

- 多路 RRF；
- authority/kind/tier 调整；
- raw observation fallback；
- explain 输出；
- 检索与读取都产生 usage。

### 4.9 时间有效性与 typed edges

- 页面版本有 ingestion-time `valid_from` / `valid_to`；
- `as_of` 可查询“某时间点我们当时知道什么”；
- 历史查询融合 entity timeline + version-filtered FTS；
- 明确不把 ingestion-time 伪装成 world-time；
- typed edges 为封闭集合：`causes` / `fixes` / `contradicts`；
- `contradicts` 直接供 lint；
- edge 类型不接受自由文本，避免不可查询的 folksonomy；
- 旧版本搜索排序受当前索引统计影响，文档明确说明边界。

AAR 已有 revision/supersede，但没有 `as_of`。可借鉴：

- 给 memory record / summary 增加 ingestion-time window；
- 为审计/复盘提供 `as_of` 查询；
- 采用封闭 relation vocabulary，而不是自由 edge type。

### 4.10 Auto-improvement 与 eval gate

ai-memory 的自动改进：

- 只处理新完成 session；
- 有 watermark 和 per-session claim；
- 生成小提案（gotcha/decision/concept/procedure/rule）；
- 结构化 schema、confidence、size cap、evidence quote；
- 先 staging，再审批/自动批准；
- 可通过 executable eval gate；
- 记录 pending-writes audit；
- 默认 auto-approve 但每条有 provenance；多人服务器建议 require_approval；
- 负向过滤：无活动 session、单命令 smoke、release marker、临时缺依赖、宽泛“工具坏了”、一次性任务叙事、已解决瞬时故障；
- 不改写当前活动 prompt；
- 不在 hook 延迟内运行；
- 失败 proposal 有 rejection buffer，避免无限重试。

AAR 的 Phase 2/自动记忆可借鉴：

- proposal/staging 模式；
- evidence + confidence + eval；
- negative filters；
- 失败候选保留与去重；
- 审批和调度分离；
- 自动提案 actor 单独归因。

### 4.11 Cross-project profile：全局记忆的可借鉴模型

ai-memory 的 profile 是“跨项目用户偏好”的完整实现：

- 单用户默认开启；多用户默认关闭，opt-in 私有；
- 只从用户自己写的 prompt 和 curated pages 中 harvest；
- 不读 tool output；
- `min_projects=2` 才自动晋升；
- 用户明确“在所有项目中”只需一次；
- 最新陈述覆盖旧陈述；旧版本保留；
- `profile forget` 后同一证据不会自动复活；
- 用户编辑过的页面不会被自动改写；
- digest 稳定排序、stack 过滤、字节预算；
- 优先级：当前指令 > 仓库规则 > 项目 memory > profile；
- 有 evidence、confidence、generated_by、contributor；
- 多用户时一人不能把偏好种进所有人的 profile。

这对 AAR 的 L3 user scope 非常关键。AAR 目前只有“user scope 可选”的方向，缺乏：

- 晋升门槛；
- 用户写入来源识别；
- 防复活；
- 多用户隔离；
- 稳定 digest；
- 优先级规则。

### 4.12 安全、生命周期与运维

ai-memory 的治理值得直接作为清单：

- sanitize 是唯一 ingress；
- scope resolution 集中在 `ScopeResolver`，显式 scope fail closed；
- `per_actor` / `per_session` 隔离；
- workspace/project UUID 目录，稳定名称与路径解耦；
- 多用户 auth ladder；
- 写入前 admission webhooks，可 reject/mutate；
- dry-run + confirm + `--compact`；
- purge 明确定义“逻辑删除 vs byte erasure”；
- git 版本恢复；
- backup/restore/reindex；
- audit log；
- WAL/checkpoint/vacuum 的运维边界；
- adversarial security tests（移除 guard 必须测试失败）；
- evals / recall eval gate；
- 量化写入上限、延迟和 query p95。

AAR 已有一部分（suppression、audit、recall trace、canonical project_key），但可以借鉴：

- 删除语义文档化；
- reindex / restore 契约；
- dry-run/confirm；
- scope resolver 单点；
- admission/reject 策略；
- adversarial tests；
- eval gate。

### 4.13 ai-memory 可借鉴点与不应照搬

| 借鉴点 | 对 AAR 的价值 |
| --- | --- |
| 两层事实源 + 可重建索引 | 补强 reindex/export/recovery |
| 生命周期 hooks + idempotency + spool | 补强记忆 job 入口与可靠性 |
| claim-once handoff | 补强跨会话续作闭环 |
| L0/L1/L2 tiered brief | 直接优化 token 与覆盖 |
| access-weighted retention | 补强按使用老化 |
| extractive compaction + dedup + contradiction | 零 LLM 质量维护 |
| FTS+entity+graph+vector RRF | 补强召回质量 |
| `as_of` ingestion-time | 补强审计/复盘 |
| typed edges | 补强矛盾/修复链 |
| auto-improvement proposal + eval | 补强 Consolidation 治理 |
| cross-project profile | 补强 L3 准入与防复活 |
| dry-run/purge/reindex/admission | 补强运维与安全 |

不应直接照搬：

- 独立多用户服务器/团队共享作为 AAR v1 范围；
- Markdown wiki 作为唯一事实源（AAR 当前 SQLite canonical，不应反转）；
- 20+ harness 的 hooks 适配；
- 复杂 dream/DBSCAN/embedding 作为 v1 默认；
- AGENTS.md 自动改写；ai-memory 本身也明确“只推荐、不自动写”；
- 把 Tenet/规则直接当普通 memory 权限；
- ai-memory 的 native Windows 仍标为 experimental，不能把其平台实现直接当作 AAR Windows 兼容基线。

---

## 5. 四方对比

| 能力 | Nuphus | ai-memory | DeepSeek Harness | AAR Agent Memory v2 | 建议 |
| --- | --- | --- | --- | --- | --- |
| 原生长期记忆 | 有 | 有完整产品 | 无（MCP 外接） | 设计有，未实现 | AAR 保持内置 |
| 事实源 | SQLite + MD | Markdown wiki + 派生 SQLite | 外部 MCP | SQLite + materialized MD | 采用 AAR 现有方向，借可重建索引 |
| 记忆生命周期 | kind/prune | tier/decay/compaction/dream | 无内置 | candidate/active/superseded/deleted | 借 ai-memory 的 aging |
| 跨会话续作 | 工作日志 | claim-once handoff | Session Reference | 设计无 handoff | 增加 handoff/续作包 |
| 注入层级 | MD tail + 摘要 | L0/L1/L2 brief | context/spill | summary/hybrid | 借 tiered brief |
| 检索 | FTS+vector | FTS+entity+graph+vector RRF | Session Query | FTS/LIKE+rank | 借多路融合/explain |
| 时间查询 | 无 | `as_of` | 无 | revision/supersede | 借 ingestion-time |
| 全局/跨项目 | project/custom | profile 多用户 | session ref | user scope 可选 | 借 profile 准入规则 |
| 自动改进 | distill/pattern | proposal+eval+audit | 无 | Phase 2 | 借 staging/eval/negative filters |
| 删除/审计 | delete/prune | tombstone/git/purge | 无 | suppression/events | 借 dry-run/reindex |
| 安全 | project/custom | scope resolver/admission | workspace auth | project_key | 借 fail-closed + adversarial tests |

---

## 6. 建议优先级与采纳阶段

> P0/P1/P2 表示价值优先级，不等于发布范围。版本归属以 `README §3.4` 和 `05 §1.1` 为准。

| 能力 | 价值优先级 | 建议版本 |
| --- | --- | --- |
| 按 kind retention | P0 | v1 |
| 零结果诊断 / session context | P0 | v1 |
| 宿主侧 workspace 授权 | P0 | v1 |
| L1 working digest | P0 | v1.1 |
| 跨会话只读快照 | P0 | v1.1 |
| Token headroom / per-model | P1 | v1.1 |
| Spill / preview | P1 | v1.1 |
| SessionEnd handoff / 续作包 | P0 | v1.1 |
| L0/L1/L2 tiered brief | P0 | v1.1 |
| access-weighted retention | P1 | v1.1 |
| extractive compaction / dedup / contradiction | P1 | v1.1 |
| 多路 RRF / explain | P1 | v1.1 |
| `as_of` ingestion-time | P2 | 后续 |
| auto-improve proposal + eval gate | P1 | v1.1 |
| cross-project profile 准入/防复活 | P1 | v1.1 |
| dry-run / reindex / admission | P1 | v1 |
| 可选语义检索 | P2 | 后续 |
| MCP memory provider | P2 | 后续 |
| Tenet 类策略 | P2 | 不进入 memory，交 policy 层 |

### P0：补强长期记忆可用性

1. **L1 working digest（v1.1）**
   - 新增一个短期的“工作记忆摘要”层，注入最近会话进展、下一步、关键文件；
   - 不超过 2,000 字符；
   - 与 L2 summary 分离；
   - 只在新 turn 注入，避免 prompt cache 抖动。

2. **按 kind 的 retention（v1）**
   - `conversation` / `distill` / `pattern` / `manual` 不自动删除；
   - `task_trace` 可裁剪；
   - `pinned` 永不自动删除；
   - 记录 retention 原因和审计。

3. **召回诊断（v1）**
   - 零结果返回逐词命中数/过滤原因；
   - 提供 `memory_recent`、`memory_stats`、`memory_session_context` 对应能力；
   - 区分“没命中”和“命中但被预算丢弃”。

4. **跨会话引用快照（v1.1）**
   - 用户级/跨项目引用只能是只读快照；
   - 带不可信提示；
   - 有 token/字节预算；
   - 超限 spill 到 artifact；
   - 源变更不自动更新。

5. **SessionEnd handoff / 续作包（v1.1）**
   - 规则式 session summary；
   - owner + claim-once 投递；
   - 含进展、失败、未决问题、下一步、source refs。

6. **L0/L1/L2 tiered brief（v1.1）**
   - abstract/summary/body 分层；
   - 安全框架与 footer 预留；
   - pinned/rules 优先；
   - 省略项输出路径指针。

### P1：补强安全与预算

7. **Workspace 授权（v1）**
   - 模型不能自己指定 `workspace_path`；
   - 授权来源必须是宿主调用身份；
   - 跨项目需要显式权限；
   - 禁止暴露 cursor/offset。

8. **Token headroom 与按模型策略（v1.1）**
   - 复用 DSH 的 `W−O−headroom` 思路；
   - 将 memory 注入预算作为总 prompt 预算子项；
   - 支持 per-provider/model 覆盖；
   - 工具结果先 pruner，再抽取/summary。

9. **Spill/Preview（v1.1）**
   - 长记忆/证据不直接进入 prompt；
   - 返回 preview + id + retrieval hint；
   - 原文由 artifact/spill 提供。

10. **Session context 工具（v1）**
   - 按 session/turn 返回因果链和工具摘要；
   - 用途：解释某条记忆为何存在；
   - 与 recall trace 合并可解释性。

### P2：增强检索与外部 provider

11. **可选语义检索（后续）**
   - v1 仍以 FTS5/keyword 为主；
   - embedding/HNSW 作为可选开关；
   - 失败降级 FTS；
   - 记录 embedding coverage。

12. **Memory provider seam（后续）**
    - 内置 SQLite 是默认 provider；
    - 未来可接 MCP memory / Memorix / Engram；
    - provider 只负责存储/搜索/写入工具，不接管 AAR 的 scope、permission、retention、citation；
    - 外部内容必须经过 untrusted 包装。

13. **Tenet 类策略层（不进入 memory）**
    - 可以借鉴“不可变 + 优先级 + enforce”思想；
    - 但应放在 policy/permission 层，不放普通 memory；
    - memory 不应 Block 工具调用。

---

## 7. 对现有 AAR 文档的具体修订建议

> 以下条目已按 v1/v1.1 收敛到对应设计文档；`memory_snapshots` 等未落 schema 的项仍为后续候选。

### 7.1 `03-target-architecture.md`

已补充：

- `L1 working digest`；
- `memory_session_context` 或等价因果链工具；
- per-kind retention；
- cross-session snapshot 的 untrusted 包装。
- v1.1 扩展点：SessionEnd handoff、L0/L1/L2 tiered brief、access-weighted retention、RRF/explain、cross-project profile。

### 7.2 `04-data-model-and-api.md`

已增加/仍待：

- `memory_entries.retention_class`；
- `pinned` 与 `retention_class` 已设计，且语义独立；
- `memory_snapshots`（可选）用于跨会话只读快照；
- `memory_recall_traces` 已设计；
- `memory_session_context` API/tool；
- 零结果诊断字段。
- v1.1 预留：`memory_handoffs`、`memory_recall_traces.streams_json` / `fusion_scores_json`；v1 不建表、不写入。

### 7.3 `05-implementation-plan.md`

已登记：

- M1：retention_class、recall diagnostics、session context；
- M2：tool result pruning；
- M4：session context 工具/UI；
- M5：FTS vs semantic 对照评估。
- v1.1：working digest、handoff、tiered brief、access-weighted retention、RRF/explain。

### 7.4 `06-test-and-acceptance.md`

已加入：

- `TestMemoryWorkingDigestInjectionBound`；
- `TestMemoryKindRetentionAndPruning`；
- `TestMemoryZeroResultDiagnostics`；
- `TestMemorySessionContextTrace`；
- `TestMemoryCrossSessionSnapshotUntrusted`；
- `TestMemoryToolResultPrunedBeforeExtraction`；
- `TestMemoryWriteNewSessionRecallUse`（借鉴 DSH 的 A/B 会话验收）。
- `TestMemoryHandoffClaimOnce`（v1.1）；
- `TestMemoryTieredBriefBudget`（v1.1）；
- `TestMemoryAccessWeightedRetention`（v1.1）；
- `TestMemoryRRFExplain`（v1.1）；
- `TestMemoryProfilePromotionRequiresEvidence` / `TestMemoryProfileForgetPreventsResurrection`（v1.1）。

---

## 8. 建议的端到端验收场景

参考 DeepSeek Harness 的验证方式，AAR 至少要有：

1. 会话 A：明确写入“验证偏好 X”；
2. 新建会话 B，不复制 A 的对话；
3. B 通过 memory 检索/注入召回 X；
4. B 在回答中使用 X；
5. 用户纠正 X；
6. 新建会话 C 不再出现旧 X；
7. 删除 X 后，D 不再召回；
8. 跨项目 E 不可见；
9. 超过 token 预算时，返回 preview 而不是完整正文；
10. `off` 时 B/C/D 均无 v2 记忆注入。

---

## 9. 最终建议

**优先借鉴 ai-memory 的“完整记忆生命周期 + 分层 brief + 零 LLM aging + handoff + 审计”，借鉴 Nuphus 的“工作日志 + Kind 分层 + 检索工具面”，再借鉴 DeepSeek Harness 的“Session Query + Session Reference + Compaction/Token/Spill + 授权边界”。**

不建议把 DSH 的 MCP 外接模式当作 AAR 的默认长期记忆实现；不建议在 v1 反转 ai-memory 的 Markdown-source-of-truth；也不建议把 Nuphus 的 Tenet 直接混入 memory。AAR 应在现有 `memorystore` 上演进，保持：

- 项目级默认隔离；
- 内置 SQLite 事实源；
- 可解释召回；
- 严格 token 预算；
- 不可信记忆包装；
- 可删除、可抑制、可审计；
- 可选外部 provider，而不是外置为主。

## 10. 参考锚点

### Nuphus

- `src/memory/entry.rs`
- `src/memory/mod.rs`
- `src/memory/tenets.rs`
- `src/store/memory.rs`
- `src/agent/distill.rs`
- `src/agent/mod.rs`
- `src/agent/prompt.rs`
- `src/tools/definitions/memory.rs`
- `src-tauri/src/commands/memory.rs`
- `frontend/src/main-window/memories/MemoriesPage.tsx`

### DeepSeek Harness

- `docs/user/guide/mcp-memory.zh.md`
- `packages/context/README.zh.md`
- `packages/context/session-reference/README.zh.md`
- `packages/session-query/README.zh.md`
- `packages/session-query/session-query/README.zh.md`
- `packages/experimental/tool-session-query/README.zh.md`
- `packages/compaction/README.zh.md`
- `packages/compaction/compaction-basic/README.zh.md`
- `packages/compaction/compaction-tool-result-pruner/README.zh.md`
- `packages/spill/README.zh.md`
- `packages/context/agent-instructions/README.zh.md`
- `docs/persistence-catalog.zh.md`

### ai-memory

- `README.md`
- `docs/ARCHITECTURE.md`
- `docs/design-memory-aging.md`
- `docs/design-tiered-session-brief.md`
- `docs/design-verbatim-residue.md`
- `docs/auto-improvement-loop.md`
- `docs/design-rules-promotion.md`
- `docs/cross-project-profile.md`
- `docs/temporal.md`
- `docs/typed-edges.md`
- `docs/vector-backend-policy.md`
- `docs/auto-scope.md`
- `docs/lifecycle-ops.md`
- `docs/security-boundaries.md`
- `crates/ai-memory-store/src/decay.rs`
- `crates/ai-memory-consolidate/src/sweep.rs`
- `crates/ai-memory-consolidate/src/compaction.rs`
- `crates/ai-memory-consolidate/src/cold_cluster.rs`
- `crates/ai-memory-consolidate/src/auto_improve*.rs`
- `crates/ai-memory-consolidate/tests/suite/recall_eval.rs`
