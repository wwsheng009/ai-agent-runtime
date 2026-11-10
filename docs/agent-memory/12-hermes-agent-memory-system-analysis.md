# Hermes Agent 记忆系统架构与实现分析（长期 / 短期记忆）

> 状态：外部方案分析（Docs Only，不含代码改动）
> 分析对象：`NousResearch/hermes-agent`（https://github.com/NousResearch/hermes-agent），`main@9dcf7714df7a0d49c335925d12d4b017274ee355`（2026-11-09）
> 对照基线：本项目 `docs/agent-memory/` 的 Agent Memory v2 方案
> 方法：官方文档 + 源码静态审计 + 测试锚点交叉验证。行号基于上述 commit，后续版本可能漂移；引用时优先使用“文件 + 符号”定位。
> 配套落地设计：`docs/agent-memory/13-memory-injection-and-learning-update-plan.md`（读路径注入 + 写路径学习更新）。
> 结论先行：**Hermes 不是“把所有历史塞进向量库”的记忆产品，而是一套围绕上下文窗口成本构建的分层记忆栈：小型常驻精选记忆（MEMORY.md / USER.md）+ 有损但可恢复的上下文压缩（handoff summary + 受保护尾部）+ 无损可检索的会话档案（SQLite/FTS5）+ 可插拔的外部语义记忆 Provider + 程序性记忆（Skills）。**

---

## 0. 结论摘要

### 0.1 六层记忆模型

| 层 | 名称 | 主要载体 | 进入模型上下文的方式 | 生命周期 |
|---|---|---|---|---|
| L0 | 当前请求工作集 | 当前 user message、临时插件/召回上下文 | 直接进入本次请求 | 单次请求 |
| L1 | 会话上下文 / 工作记忆 | `state.db` 中的 active transcript、工具结果、压缩摘要、受保护尾部 | system prompt + messages + handoff summary | 会话内，跨 turn |
| L2 | 跨会话精选记忆 | `$HERMES_HOME/memories/MEMORY.md`、`USER.md` | 会话开始/压缩边界渲染进 system prompt 的 volatile 区 | 跨会话，长期直到被删除/重置 |
| L3 | 外部长期记忆 | RetainDB、ByteRover、Honcho、OpenViking、Mem0、Hindsight 等 | 静态 provider block + 每轮 prefetch 注入 + provider 工具 | 由 provider 管理 |
| L4 | 程序性记忆 | Skills（`SKILL.md` 及 supporting files） | 按需加载 / skills index | 跨会话，按任务复用 |
| L5 | 情景档案 / 可检索历史 | `state.db` 全量 messages、`messages_fts`、`messages_fts_trigram` | `session_search` 按需检索 | 长期保留，可归档/清理 |

> 这里的 L5 不是独立数据库，而是 L1 的“冷存储”视图：被压缩的 active 行会软归档为 `active=0, compacted=1`，仍然保留在同一个 `state.db` 中并可被 FTS5 检索。

### 0.2 六个关键设计判断

1. **常驻记忆必须小，历史必须可检索。** `MEMORY.md` 约 2,200 字符、`USER.md` 约 1,375 字符；真正的长历史放在 `state.db`，通过 `session_search` 按需取回。
2. **压缩是有损的，归档是无损的。** 压缩只改写“当前活跃上下文”，旧消息软归档而非删除；摘要里带 `session_search` 恢复指针。
3. **写入异步、读取有界。** 外部 provider 的 `sync_turn`、`queue_prefetch` 在单 worker 后台线程执行；每轮 recall 有超时和 stuck gate，不让一个挂死的后端拖住对话。
4. **缓存稳定优先于“每次都是最新”。** 内置记忆在 prompt 构建时拍成 frozen snapshot；会话中途写入落盘但不改当前 prompt，直到下一次 prompt 重建（压缩边界/新会话）。外部 recall 则作为 user message 的 `api_content` sidecar 注入，不重排 system prefix。
5. **记忆是数据，不是指令。** 记忆块、外部召回块、压缩摘要都被明确标注为参考数据；写入前做注入/外泄扫描，外部 recall 用 `<memory-context>` 围栏并从输出流中剥离。
6. **机制与后端分离。** 核心只提供文件记忆、压缩、会话检索和 Provider 契约；语义抽取、知识图谱、用户建模、向量检索交给单一外部 provider，避免核心无限膨胀。

### 0.3 一句话评价

Hermes 的记忆系统在“**上下文工程 + 运行可靠性 + 缓存友好**”上明显强于多数同类实现；在“**语义检索、事实版本化、冲突消解、来源审计**”上则刻意保持简单，把复杂度外移给 provider。它更像一套“**有记忆的上下文操作系统**”，而不是一个完整的记忆产品。

---

## 1. 研究范围与方法

### 1.1 范围

本文重点分析：

- 内置长期记忆：`MEMORY.md` / `USER.md` 的文件格式、读写协议、容量治理、安全扫描、审批与后台学习闭环；
- 短期/工作记忆：system prompt 分层、活跃 transcript、上下文压缩、摘要回注、会话持久化；
- 长历史检索：SQLite / FTS5 / `session_search` 的召回路径；
- 外部长期记忆：`MemoryProvider` ABC、生命周期 hook、prefetch/sync、pre-compress checkpoint、profile 隔离；
- 理论映射：工作记忆、情景记忆、语义记忆、程序性记忆、巩固/检索/遗忘的工程对应；
- 对本项目 Agent Memory v2 的可借鉴边界与验证方案。

不把以下内容误称为“记忆系统”：

- `SOUL.md`：人格/身份文件；
- `AGENTS.md`、`CLAUDE.md` 等 context files：项目指令；
- Skills：程序性记忆，虽然参与学习闭环，但不是事实型长期记忆；
- 普通日志、usage analytics、tool receipts：运行遥测，不是模型可召回的记忆。

### 1.2 证据等级

| 等级 | 证据 | 用途 |
|---|---|---|
| A | 可执行源码 | 行为事实 |
| B | 测试文件 | 边界与回归契约 |
| C | 官方文档 | 设计意图、配置语义、用户操作 |
| D | README / 发布说明 | 概述性口径，可能与代码有代差 |

本文以 A/B 为主；官方文档用于解释“为什么这样设计”；遇到文档与代码不一致时以代码为准并标注。

本文以静态审计为主，未对所有外部 provider 做联网/付费实测；涉及运行时时序、并发和失败行为的结论以代码路径与测试锚点为准。

### 1.3 关键路径

| 主题 | 入口 |
|---|---|
| 内置记忆存储 | `tools/memory_tool_store.py`、`tools/memory_tool.py` |
| 内置记忆注入 | `agent/system_prompt.py::_memory_parts`、`agent/agent_init.py::_init_memory` |
| 短期压缩 | `agent/context_compressor.py`、`agent/context_compressor_window.py`、`agent/context_compressor_tail.py` |
| 压缩提交 | `agent/conversation_compression.py`、`hermes_state_messages.py::archive_and_compact` |
| 会话检索 | `tools/session_search_tool.py`、`hermes_state_search.py` |
| 外部 provider | `agent/memory_provider.py`、`agent/memory_manager.py`、`plugins/memory/` |
| 后台学习 | `agent/background_review.py`、`agent/turn_finalizer.py`、`agent/turn_context.py` |
| 上下文引擎插件 | `agent/context_engine.py`、`website/docs/developer-guide/context-engine-plugin.md` |

---

## 2. 理论模型：Hermes 的记忆分层

### 2.1 认知科学映射（工程类比，不是官方理论）

Hermes 没有单独发布“记忆理论白皮书”。下面的映射是从实现中归纳出的工程模型，用于解释其取舍：

| 认知概念 | Hermes 对应 | 说明 |
|---|---|---|
| 工作记忆（working memory） | L0 + L1 的活跃 transcript / 受保护尾部 | 容量由模型 context window 和压缩阈值决定；正在使用的信息保持高保真 |
| 情景记忆（episodic memory） | L5 `state.db` 历史消息、FTS5 | “某次会话里发生过什么”；按时间/关键词检索 |
| 语义记忆（semantic memory） | L2 `MEMORY.md` / `USER.md`；L3 外部 provider | 去情境化的稳定事实、偏好、环境约定 |
| 程序性记忆（procedural memory） | L4 Skills | “怎么做某类任务”的步骤、工具、坑 |
| 巩固（consolidation） | 上下文压缩摘要、后台 review、外部 provider 的 session-end 抽取 | 把易失的过程材料转成更稳定、更短的表征 |
| 检索（retrieval） | 常驻注入、provider prefetch、`session_search`、skills 按需加载 | 不同记忆层用不同线索触发 |
| 遗忘（forgetting） | 容量淘汰、显式 remove、软归档、provider 侧衰减 | 内置层没有自动 TTL；遗忘主要是“容量驱动的合并/删除”或“从活跃上下文移出” |
| 元记忆（metamemory） | 记忆用量头、recall indicator、`/journey`、压缩 telemetry | 让 agent/用户知道“用了哪层记忆、还剩多少、是否压缩过” |

### 2.2 核心理论命题

可以把 Hermes 的设计概括为五个命题：

#### 命题 1：上下文是缓存，记忆是后备存储

模型 context window 是昂贵、易失的“缓存”；`state.db` 是不可变/半不可变的后备存储。
Hermes 的选择不是“扩大缓存”，而是：

- 用压缩把工作集维持在阈值以下；
- 用软归档保留被移出的原始材料；
- 用 `session_search` 在需要时把冷数据重新取回。

它与 OS 分页的相似点是“分层 + 换入换出”；不同点是 **没有自动 page fault**：是否检索、检索什么，由模型调用工具或 provider prefetch 决定。

#### 命题 2：有损压缩 + 无损档案

压缩摘要会丢失细节，但 Hermes 通过三种机制降低“记忆损失”：

1. **保留受保护头部与尾部**：system prompt、最初交流、最近消息、最近真实用户请求、最近 assistant 回复、工具调用组不被拆散；
2. **摘要中包含恢复指针**：lean 模式明确告诉模型可以用 `session_search(query=..., session_id=...)` 取回被压缩内容；
3. **原始行软归档**：`archive_and_compact` 将旧行标记为 `active=0, compacted=1`，不删除；FTS5 仍可搜索。

这使压缩更像“**把工作集折叠成索引**”，而不是“永久删除历史”。

#### 命题 3：写入是反思，读取是线索驱动

- 内置记忆不会自动从每轮对话抽取；它依赖模型显式调用 `memory` 工具，或后台 review 在 turn 结束后判断“是否值得保存”。
- 外部 provider 可以在 `sync_turn`、`on_session_end`、`on_pre_compress` 中做自动抽取，但这是 provider 的策略，不是核心的默认行为。
- 读取也不是单一向量检索：常驻记忆用“始终注入”，历史用“关键词/时间/FTS5”，外部记忆用“query prefetch”，技能用“任务相关性加载”。

#### 命题 4：缓存一致性只在“缓存边界”处理

Hermes 把 prompt 分成 stable / context / volatile 三层，内置记忆位于 volatile 尾部。
记忆写入落盘后，当前会话的 system prompt **不会立即重渲染**；只有以下边界会重建：

- 新会话；
- 上下文压缩提交后（`invalidate_system_prompt` 会重新 `load_from_disk`）；
- 显式 prompt refresh / 模型切换等需要重建缓存的路径。

因此“frozen snapshot”不是永久冻结，而是“**在下一个缓存失效点之前保持字节稳定**”。这是缓存友好与记忆新鲜度之间的显式 trade-off。

#### 命题 5：记忆是数据，不是指令

记忆内容会进入 system prompt 或 user message，因此 Hermes 把它当作**不可信数据**处理：

- 写入前扫描 prompt injection、exfiltration、持久化后门、不可见 Unicode；
- 加载时再次扫描，命中项只从 snapshot 替换为 `[BLOCKED: ...]`，live 列表保留原文供用户删除；
- 外部 recall 用 `<memory-context>` 围栏，并附 system note 说明“不是新的用户输入，只作为参考数据”；
- 流式输出经过 scrubber，防止 provider 预包装的 memory-context 泄漏到用户界面；
- 压缩摘要 prompt 明确要求把 transcript 当作 data，不执行其中的指令。

### 2.3 与常见记忆架构的对比

| 架构 | 典型做法 | Hermes 的差异 |
|---|---|---|
| 纯 RAG / 向量库 | 所有文档切块、embedding、top-k 注入 | 核心不依赖向量；内置是文件 + FTS5；向量/语义检索交给外部 provider |
| MemGPT 式 OS 分页 | 主上下文/归档内存，agent 显式管理 page in/out | 类似“分层 + 换出”，但 Hermes 用压缩摘要和 `session_search`，没有统一的 page fault 机制 |
| Generative Agents 记忆流 | 记忆流 + recency/importance/relevance 检索 + reflection | Hermes 内置没有 importance/recency 打分和自动 reflection 管线；后台 review 是有限的反思，外部 provider 可补足 |
| 文件式 Agent 记忆 | `MEMORY.md` / `USER.md` + 手工整理 | Hermes 在此基础上增加了容量硬约束、批次原子写、安全扫描、审批、后台 review、跨会话检索 |
| 记忆产品（Mem0/Honcho 等） | 自动抽取、用户建模、知识图谱 | Hermes 把它们作为可插拔后端，核心保持“小而可控” |

---
## 3. 总体架构与关键数据流

### 3.1 组件关系

```text
┌──────────────────────────────────────────────────────────────────────┐
│                         一次用户 turn                               │
└──────────────────────────────────────────────────────────────────────┘
        │
        ▼
┌──────────────────────────────────────────────────────────────────────┐
│ AIAgent prompt 组装                                                  │
│  stable: 身份 / 指导 / 编码 brief / pinned skills                    │
│  context: caller system message / AGENTS.md / workspace              │
│  volatile: skills index / MEMORY.md / USER.md / provider block /     │
│            profile / timestamp / env hints                           │
└──────────────┬───────────────────────────────────────────────────────┘
               │
               ▼
┌──────────────────────────────┐
│ turn-start / preflight 压缩  │  ← 可能重写 active transcript
└──────────────┬───────────────┘
               │
               ▼
┌──────────────────────────────┐
│ MemoryManager.prefetch_all() │  ← 外部 provider 的 query recall
│  · 外部 provider 在后台线程  │
│  · 超时 / stuck gate / spill │
└──────────────┬───────────────┘
               │ 返回 recall 文本
               ▼
┌──────────────────────────────────────────────────────────────────────┐
│ 当前 user message + <memory-context> → api_content sidecar           │
└──────────────┬───────────────────────────────────────────────────────┘
               │
               ▼
┌──────────────────────────────────────────────────────────────────────┐
│ conversation loop                                                    │
│  · ContextCompressor.should_compress() / compress()                  │
│  · 工具调用、工具结果、assistant 回复                                │
└──────────────┬───────────────────────────────────────────────────────┘
               │
               ├── 压缩时：prune → boundary → summary → assemble
               │        └── archive_and_compact → state.db（软归档）
               │
               ▼
┌──────────────────────────────────────────────────────────────────────┐
│ turn 收尾                                                            │
│  · MemoryManager.sync_all() → provider.sync_turn()（后台单 worker）  │
│  · MemoryManager.queue_prefetch_all() → 为下一轮预热                 │
│  · 可选 background review → memory / skills 写入                     │
└──────────────┬───────────────────────────────────────────────────────┘
               │
               ▼
┌──────────────────────────────────────────────────────────────────────┐
│ memory boundary（压缩提交、/new、/reset、gateway/CLI exit）          │
│  · provider.on_session_end() → 抽取/落盘                             │
│  · provider.on_session_switch() → 重新绑定 session                    │
│  · shutdown / drain                                                 │
└──────────────────────────────────────────────────────────────────────┘
```

### 3.2 一次 turn 的记忆读写时序

1. **构造 system prompt**：`build_system_prompt_parts()` 把内置 `MEMORY.md` / `USER.md` 的 frozen snapshot 放入 volatile 区；外部 provider 的静态 `system_prompt_block()` 也放在这里。
2. **记录上一轮用户消息**：`_previous_user_message_text()` 从历史中提取“上一条真实用户输入”，供 provider 在 `on_turn_start` 中恢复上下文（尤其进程重启后）。
3. **通知 provider turn 开始**：`MemoryManager.on_turn_start()` 传递 turn number、当前 query、model、作者身份、上一条消息。
4. **turn-start 压缩之后，召回外部记忆**：`prefetch_all()` 对非 trivial prompt 调用 provider `prefetch()`；外部 provider 在受控线程中执行，超时则本轮不注入。
5. **把 recall 变成 ephemeral context**：`compose_user_api_content()` 将 recall 包装为 `<memory-context>`，追加到当前 user message 的 `api_content` sidecar；不改 system prompt，不重排前缀。
6. **模型调用与工具循环**：pre-API、post-tool 等位置仍可能触发压缩。
7. **turn 结束**：`sync_all()` 把 user/assistant 文本（以及可选 messages/author）交给 provider 的 `sync_turn()`；`queue_prefetch_all()` 为下一轮预热。
8. **后台 review**：如果到了 memory nudge 或 skill nudge 阈值，fork 一个 review agent，判断是否写 memory / skills；写入发生在后台，不阻塞用户回复。

关键锚点：

- `agent/turn_context.py::_memory_turn_start_and_prefetch`（约 1059）
- `agent/turn_context.py::compose_user_api_content`（约 125）
- `agent/turn_finalizer.py::_mirror_turn_to_memory`（约 591）
- `agent/memory_manager.py::prefetch_all`、`sync_all`、`queue_prefetch_all`

### 3.3 一次压缩的读写时序

```text
prompt_tokens >= threshold
        │
        ▼
should_compress_info()
  ├─ 检查 cooldown / anti-thrash / 结构 no-op
  └─ 允许则进入 compress()
        │
        ▼
Phase 1: prune old tool results（无 LLM）
  ├─ 去重完全相同的 tool result
  ├─ 旧 tool result → 一行摘要 / 恢复 stub
  ├─ 旧图片 → 占位符
  └─ 保护未读的 pending tool round
        │
        ▼
Phase 2: 确定 head / middle / tail
  ├─ head: system + protect_first_n（首次压缩后衰减）
  ├─ tail: token budget + protect_last_n 下限 + 最近用户/assistant 锚点
  └─ tool_call/tool_result 组不拆散
        │
        ▼
Phase 3: 结构化摘要（LLM / deterministic fallback）
  ├─ 首次：Goal / Constraints / Progress / Decisions / Files / Next Steps ...
  ├─ 后续：update previous summary，而不是从零重写
  └─ lean 模式追加：session log、anchor index、verbatim user messages、session_search footer
        │
        ▼
Phase 4: 组装
  ├─ system prompt 追加 compaction note
  ├─ summary 以合适 role 插入（避免同角色连续）
  ├─ summary 末尾加 END MARKER，防止模型把摘要当新输入
  ├─ tail 原样保留（可能被 lean 模式 demote 旧 tool result）
  └─ sanitize 孤立 tool pairs / 历史图片 / stale reasoning
        │
        ▼
commit
  ├─ archive_and_compact：旧 active 行 → active=0, compacted=1
  ├─ 插入 summary + compacted tail 作为新 active 行
  ├─ 并发追加的 tail 行按 watermark 重新编号/克隆
  └─ 重建 system prompt（重新加载 memory），刷新工具定义
```

关键锚点：

- `agent/context_compressor.py::compress`（约 5422）
- `agent/context_compressor_window.py::_plan_compaction_window`（约 39）
- `agent/context_compressor.py::_generate_summary`（约 3872）
- `agent/context_compressor.py::_assemble_compressed`（约 5539）
- `agent/conversation_compression.py::compress_context`（约 3959）
- `agent/conversation_compression.py::_commit_compaction`（约 3547）
- `hermes_state_messages.py::archive_and_compact`（约 908）

### 3.4 一次会话边界的时序

1. `on_session_end()` 在真实 teardown（CLI exit、gateway shutdown）和显式 memory session boundary（压缩提交、`/new`/`/reset`/rotation）触发，**不是每轮**；压缩提交时它拿到的是压缩前的 immutable snapshot，用于先抽取再改写。
2. `commit_session_boundary_async()` 把 `on_session_end` 和 `on_session_switch` 串成一个后台任务，避免“旧会话的抽取”与“新会话绑定”乱序；`commit_memory_session()` 则在压缩提交前只做旧 transcript 的抽取。
3. `on_session_switch()` 让 provider 重新绑定 session id；`/branch`、`/resume`、压缩轮转、`/new` 都可能走这条路径。
4. `shutdown_all()` 先有界 drain 单 worker 队列，再按 provider 逆序关闭；超时会明确记录 abandoned writes / prefetches。

这套顺序很重要：如果 `on_session_end` 和 `on_session_switch` 并发，provider 可能把旧 transcript 写进新 session，或重复 ingest。

---

## 4. 短期记忆：会话内上下文系统

### 4.1 短期记忆是什么

Hermes 没有单独的“short-term memory store”。短期记忆由四部分组成：

1. **system prompt**（稳定身份、上下文文件、记忆块、工具指导）；
2. **活跃 message list**（当前会话的 user/assistant/tool 消息）；
3. **压缩摘要**（被折叠的中间历史）；
4. **受保护尾部**（最近消息、最近用户请求、最近 assistant 回复、未读工具轮）。

这四部分共同构成模型每次请求看到的“工作集”。`ContextCompressor` 只改写这个工作集；`state.db` 保存持久副本。

### 4.2 Prompt 的三层缓存结构

`agent/system_prompt.py::build_system_prompt_parts()` 明确把 prompt 分成三层：

| 层 | 内容 | 变化频率 | 设计目的 |
|---|---|---|---|
| `stable` | 身份（`SOUL.md` 或默认）、通用/工具指导、编码 brief、pinned skills | 极低 | 最长公共前缀，最大化 provider prefix cache |
| `context` | caller `system_message`、`AGENTS.md`/context files、workspace snapshot | 会话/工作区级 | 项目上下文，可随 workspace 变化 |
| `volatile` | skills index、`MEMORY.md`、`USER.md`、外部 provider block、profile、timestamp、env hints | 会话/压缩边界 | 易变内容放最后，减少缓存失效范围 |

关键点：

- 内置记忆在 `volatile` 区，而不是 stable 区；这样不同 session 的记忆变化不会污染 stable 前缀。
- `build_system_prompt()` 结果缓存在 `agent._cached_system_prompt`；普通 turn 不重建。
- `invalidate_system_prompt()` 在压缩提交后调用，并执行 `_memory_store.load_from_disk()`，所以压缩边界也是记忆快照的刷新点。
- 这解释了“为什么文档说 frozen snapshot，但长时间 gateway 会话中记忆仍可能更新”：**frozen 的粒度是 prompt 缓存代际，不是整个 session 生命周期**。

### 4.3 活跃 transcript 与持久化

`state.db` 的 `messages` 表是短期工作集的持久副本，关键列包括：

| 列 | 作用 |
|---|---|
| `active` | 当前 active transcript 的一部分；压缩后旧行置 0 |
| `compacted` | 被压缩摘要取代的历史行；`active=0, compacted=1`，仍可被 FTS5 检索 |
| `api_content` | 实际发送给 API 的字节；当它与 `content` 不同（例如注入了 memory-context）时保存，保证下一轮 replay 字节一致 |
| `message_uid` | 跨 copy 的稳定逻辑消息 id；压缩克隆、轮转、分支会复制它 |
| `absorbed_message_uids` | 被合并进当前行的消息 uid 列表（角色交替修复、micro-compaction 合并等） |
| `tool_call_uids` / `tool_call_uid` | 工具调用/结果的稳定 per-occurrence id，避免 provider tool id 重复导致配对错误 |

设计意图：

- **content 是“人类可读/可展示”的规范内容；api_content 是“模型实际看到的字节”**。两者分离，既保留显示语义，又保证 prompt-cache 的 replay 一致性。
- 压缩不是 `DELETE`，而是 `active=0, compacted=1`；`session_search` 可以检索 archived rows。
- `message_uid` 让插件 context engine 可以跨物理 row id 变化跟踪同一条逻辑消息。

### 4.4 触发与预算

#### 4.4.1 主压缩触发

`ContextCompressor.should_compress_info()`：

```text
tokens < threshold_tokens                    → 不压缩
automatic gate blocked (cooldown/anti-thrash) → 不压缩，返回 block reason
otherwise                                    → 压缩
```

默认值（`cli-config.yaml.example` / 代码）：

| 参数 | 默认 | 说明 |
|---|---:|---|
| `compression.enabled` | `true` | 是否启用自动压缩 |
| `compression.threshold` | `0.50` | prompt tokens 达到模型窗口的 50% 时触发 |
| 小窗口下限 | `0.75` | context < 512K 时阈值提高到 75%，避免半窗就压缩 |
| `compression.target_ratio` | `0.20` | legacy 模式的尾部预算比例 |
| `compression.tail_mode` | `lean` | 默认 lean：小尾部 + 详细摘要 + 用户消息原文 |
| `compression.protect_last_n` | `20` | 最近消息的保底数量；token budget 优先，但有 count floor |
| `compression.min_tail_user_messages` | `1` | 至少保留最近 N 条真实用户消息；可以突破 token budget |
| `compression.protect_first_n` | `3` | system prompt 之外额外保护的头部消息；首次压缩后衰减 |
| `compression.max_attempts` | `3` | 单轮压缩重试上限 |
| `compression.in_place` | `true` | 默认在原 session id 上软归档，不轮转 |

补充：

- **gateway hygiene**：在 agent 处理消息前，gateway 还有一道 85% 的安全网，使用实际 usage / anchor / rough estimate；仅在 history ≥ 4 且 compression enabled 时触发。它比 agent compressor 的 50% 高，是为了避免长会话每轮都被提前压缩。
- **绝对上限**：`threshold_tokens` 可以给 ratio 触发加一个绝对 token 上限，取两者较低值。
- **per-model thresholds**：按 model 子串匹配，最长匹配优先，可绑定 provider；小窗口下限仍会 raise-only 生效。
- **真实 usage 优先**：正常情况下用 provider 返回的 `prompt_tokens`；没有 anchor 的首次请求如果 rough estimate 已超阈值，会先等一次真实响应（`should_defer_preflight_to_real_usage`），避免仅凭粗估就压缩。真正超窗或 provider 报 context-length 时，走 bounded 的 reactive recovery。

#### 4.4.2 触发点

| 触发点 | 说明 |
|---|---|
| turn-start / preflight | 新 turn 开始时，基于 rough estimate 或 anchor 判断 |
| pre-API | 真正发出请求前，基于最新 usage / 估算判断 |
| post-tool | 工具结果可能突然撑大上下文，循环内检查 |
| gateway hygiene | agent 之前的 85% 安全网 |
| manual `/compress` | 用户显式触发，可带 focus topic |
| idle compaction | 会话空闲后恢复时，可选地先压缩 |
| micro-compaction | 可选，每轮吸收一个旧 exchange |
| prepared compaction | 可选，接近阈值时后台预生成摘要 |
| native / server-side | provider 自己压缩（OpenAI Responses / Codex app-server） |

### 4.5 四阶段压缩算法

#### Phase 1：无 LLM 的 tool-result prune

在调用摘要模型之前，先做便宜的确定性清理：

- 完全相同的 tool result 只保留最新一份，旧副本替换为 back-reference；
- 超过阈值的旧 tool result 替换为一行摘要 / recovery stub；
- 旧图片 payload 替换为占位符，避免 base64 撑爆 body；
- **未读的 pending tool round 不 prune**：模型刚请求、还没读到的工具结果必须保持原文，否则会重复执行有副作用的工具或盲答；
- `skill_view` 刚加载/尾部引用的 skill 内容不 prune，避免“模型以为 skill 还在上下文里”的 ghost-skill 问题；
- 如果尾部本身超过 soft ceiling，会做 pressure demotion，但保留最近几条和 pending round。

这一步通常能回收大量 token，且不需要任何模型调用。

#### Phase 2：确定 head / middle / tail 边界

```text
messages:
[0 .. head)        受保护头部
[head .. cut)      middle → 摘要
[cut .. end)       tail → 原样保留

head = system prompt（若有）+ effective protect_first_n
tail = 从后往前累计 token，直到 tail budget
```

关键保护规则：

- `protect_first_n` 在第一次压缩后**衰减**，避免早期对话永久化石化为“不可压缩头部”；
- `protect_last_n` 是下限，不是绝对；尾部 token 预算优先；
- `min_tail_user_messages` 可以突破 token budget，保证最近 N 条真实用户请求原文保留；
- 最近 assistant 回复、最近 user 请求、未读工具轮、atomic tool group 都尽量保留；
- `_align_boundary_backward()` 会把 cut 拉到 tool group 的父 assistant 之前，避免拆散 call/result；
- 如果单个进行中的 turn 过大，允许一个 bounded exception：只保留 tool-group 对齐的 mid-turn cut，把该 turn 的开头请求放进 handoff（避免整个 turn 撑爆 context）。

#### Phase 3：结构化摘要

摘要不是自由文本，而是一个固定结构（fresh / iterative update 两种 prompt）：

```text
## Historical Task Snapshot
## Goal
## Constraints & Preferences
## Completed Actions
## Active State
## Blocked
## Key Decisions
## Errors & Fixes
## Resolved Questions
## Relevant Files
## Critical Context
## Pruned Skills
```

关键设计：

- **iterative update**：后续压缩把 `_previous_summary` 传给 LLM，要求“保留仍相关信息、追加新完成项、把 In Progress 移到 Completed、移除明确过时项”，而不是每次从零重写；
- **summary budget**：`content_tokens × 0.20`，最小 2,000 tokens，最大 `min(context_length × 0.05, 10,000)`（代码常量；官方文档有一处写 12K，以当前代码为准）；
- **lean 模式**：尾部只保留约 2.5% 窗口（10K floor / 25K cap，且不超过 trigger 的 50%），把连续性转移到摘要中的 session log、anchor index、逐字用户消息、`session_search` 恢复指针；这样比保留巨大 verbatim tail 更省 token，同时提高长会话 recall；
- **redaction**：摘要输入和输出都做 secret redaction；transcript 中的 `<think>` 块会被剥离；工具参数在截断前先 redact，避免 PEM 等跨截断泄漏；
- **skill safety**：被压缩掉的 `skill_view` 内容会留下 `[SKILL_PRUNED: ...]` 标记，摘要必须逐字保留，避免模型继续使用已不在上下文的技能；
- **deterministic fallback**：摘要模型不可用/超时/输出截断时，用本地可提取的 anchors（user asks、actions、files、errors、last dropped turns）生成结构化 fallback；如果配置 `abort_on_summary_failure: true`，则保持 transcript 不变并中止压缩。

#### Phase 4：组装

- 在 system prompt 追加 `_COMPRESSION_NOTE`：说明早期 turn 已被折叠，当前文件/状态可能仍反映之前工作，记忆块仍然权威；
- summary 的 role 动态选择（user/assistant），避免与 head/tail 形成连续同角色，满足 Anthropic/Bedrock/OpenAI/Mistral 等不同模板约束；
- 如果角色冲突无法避免，summary 会 merge 进 tail 的承载行（通常是首行），并用 `[END OF PRIOR CONTEXT — COMPACTION SUMMARY BELOW]` / `_SUMMARY_END_MARKER` 分隔；
- tail 行打上 `_compaction_tail` 标记，`archive_and_compact` 用这个标记识别“已被携带的原始行”，避免同一消息在压缩后出现两份；
- `_sanitize_tool_pairs()` 清理孤立的 tool_call/tool_result；历史图片、stale reasoning、重复的 replay sidecar 也会被清理；
- 最后重建 system prompt，刷新工具定义（压缩边界是 forever-session 中少数能安全刷新配置/工具的时机）。

### 4.6 压缩的持久化：软归档而不是删除

`hermes_state_messages.py::archive_and_compact()` 的核心契约：

1. 在一个 session id 下，把当前 active 行统一标记为 `active=0, compacted=1`；
2. 插入压缩后的 message list（summary + tail）作为新的 active 行；
3. 用 `watermark` 处理“摘要生成很慢，期间又有新消息到达”的并发追加：这些行不会被摘要掉，而是在压缩集之后按新 id 重新排序/克隆；
4. 用 `message_uid` / `absorbed_message_uids` / `tool_call_uids` 保持逻辑身份；
5. 整个过程在一个事务里完成；压缩 lease/fence 防止两个 compaction 同时提交；
6. 失败时保留未压缩 transcript；不会出现“摘要没写成但旧行已删除”的半状态。

关键结果：

- **默认 in-place**：同一个 session id 不变，旧行软归档，search 仍可命中；没有 parent/child session 链。
- **可选 rotation**：`compression.in_place=false` 时创建 child session，旧 session 以 `end_reason=compression` 结束；两种模式都保持 admission owner / input id 不变。
- **可恢复性**：`session_search` 能检索 `compacted=1` 的 archived rows；当前 live context 的过滤逻辑会排除“仍在上下文里”的当前 lineage，但允许已离开 live context 的压缩历史。

### 4.7 其他短期机制

#### 4.7.1 Micro-compaction（默认关闭）

- 每个完成的 turn 吸收**一个最老的未摘要 exchange**（assistant + tool results + 后续 assistant iterations，直到下一个 user message）；
- **用户消息永不压缩**：exchange 从 assistant 开始，user prompt 原文始终保留；
- 维护一个 rolling summary；超过 `micro_compact_defrag_threshold_tokens`（默认 2000）时对 summary 自身做 defrag；
- 每次 pass 调 `archive_and_compact` 同步数据库；
- 同一 exchange 连续失败 3 次则 cursor 前进，避免永远卡住；
- 代价：每轮改写已发送历史，破坏 prompt cache；因此默认关闭，提供 `micro_compact_every_n_turns` 频率旋钮。

#### 4.7.2 Prepared compaction（默认关闭）

- 当 usage 距离 trigger 不到 12% 时，后台在 copy 上预生成下一份摘要；
- 真正的自动压缩到来时，如果 session、head boundary、message 结构（role/tool ids/names）仍然匹配，就 splice 预生成摘要；
- 一份 prepared summary 只服务一次压缩；manual / focus / overflow / provider-context 路径不会使用它；
- 不适用于 plugin context engine、Codex app-server、native Responses compaction、micro-compaction。

#### 4.7.3 Native / server-side compaction

- OpenAI Responses API 可在服务端把旧上下文压成 opaque encrypted `compaction` item；
- Hermes 捕获该 item 到 assistant message 的 replay sidecar，后续请求回放；
- `store:false`、不使用 `previous_response_id`，对 ZDR 友好；
- Codex app-server 的 thread compaction 由 Codex 自己拥有，Hermes 不重写本地 transcript，只记录 compaction boundary。

#### 4.7.4 Context Engine 插件

`agent/context_engine.py` 定义 `ContextEngine` ABC，默认实现是 `ContextCompressor`；`context.engine` 可显式选择插件。插件可以：

- `should_compress()` / `compress()`：替换压缩策略；
- `select_context()`：为**单次请求**替换上下文（检索、topic routing、branch switching），不改持久历史；
- `on_turn_complete()`：观察完成的 turn，更新自己的索引/摘要；
- `get_tool_schemas()` / `handle_tool_call()`：提供自己的检索工具；
- 使用 `message_uid` 作为稳定消息身份。

设计上，`select_context()` 与 `compress()` 正交：前者是“本次请求选哪些上下文”，后者是“上下文太长时如何缩短”。两者都默认 no-op / fail-open，插件异常不会让请求变差。

### 4.8 短期记忆的设计评价

**强项：**

- 明确的 head/tail 保护、tool group 对齐、pending round 保护，减少“压缩后模型重复执行/答非所问”；
- lean 模式把“最近性”与“连续性”分开：小尾部保证便宜，详细摘要 + 用户原文 + anchor index 保证 recall；
- 软归档 + FTS5 让“压缩”不等于“丢失”，并提供恢复指针；
- 真实 usage anchor、cooldown、anti-thrash、stale attempt fence、并发 watermark 等工程细节非常完整；
- 缓存感知：system prompt 分层、summary 只在缓存失效边界重建、prepared compaction 避免高峰停顿。

**代价/局限：**

- 摘要仍然是 lossy；摘要模型上下文太小、超时、输出截断都会降低质量；
- `lean` 模式依赖摘要中的 session log/anchor index，摘要质量差时恢复能力下降；
- 压缩会破坏 prompt cache（尽管只在边界发生）；micro-compaction 每轮破坏缓存，故默认关闭；
- 长 gateway 会话如果不 `/new`，压缩会反复运行，学习闭环（memory/session_search）不容易触发；
- 压缩逻辑复杂，参数多，调优需要按模型/负载实测。

---
## 5. 长期记忆 A：内置精选记忆（MEMORY.md / USER.md）

### 5.1 存储布局与格式

默认路径（profile 隔离）：

```text
$HERMES_HOME/memories/MEMORY.md
$HERMES_HOME/memories/USER.md
```

默认 profile 下通常对应 `~/.hermes/memories/`；命名 profile 对应 `~/.hermes/profiles/<name>/memories/`。`get_memory_dir()` 每次调用都从当前 `HERMES_HOME` 解析，避免 import 时绑定错误 profile。

文件格式：

```text
══════════════════════════════════════════════
MEMORY (your personal notes) [67% — 1,474/2,200 chars]
══════════════════════════════════════════════
User's project is a Rust web service at ~/code/myapi using Axum + SQLx
§
This machine runs Ubuntu 22.04, has Docker and Podman installed
§
User prefers concise responses, dislikes verbose explanations
```

- 条目之间用 `ENTRY_DELIMITER = "\n§\n"` 分隔；
- 条目可以多行；
- 头部显示 store 名称、使用率、当前/上限字符数；
- `USER.md` 的标题是 `USER PROFILE (who the user is)`；
- 空 store 不渲染 block。

### 5.2 读取与注入：frozen snapshot

`MemoryStore.load_from_disk()`：

1. 读取文件（`utf-8-sig`，容忍 Notepad BOM；严格解码，避免有损读写）；
2. `dict.fromkeys` 去重，保留首次出现顺序；
3. 若加载后的总字符超过 store 上限（包括单条超大 entry），加载时只警告，不截断（可能是外部手写/旧上限遗留；截断会丢用户记忆）；
4. 对每条做 strict threat scan；命中项在 **snapshot** 中替换为 `[BLOCKED: ...]`，但 live list 保留原文，让用户能看到并删除；
5. 调用 `_render_block()` 生成 frozen snapshot，保存在 `_system_prompt_snapshot`。

`format_for_system_prompt()` 返回的是 snapshot，不是 live state。于是在同一 prompt 代际内：

- 模型看到的是稳定的记忆块，有利于 prefix cache；
- 中途 `memory` 工具写入会立即落盘，工具响应显示 live usage；
- 当前 system prompt 不变；下一次会话或压缩边界重建 prompt 时才刷新。

这个设计与 `agent/system_prompt.py::invalidate_system_prompt()` 配合：压缩提交后会 `load_from_disk()`，所以“frozen”只在缓存代际内成立。

### 5.3 `memory` 工具：写入协议

工具 schema（`tools/memory_tool.py::MEMORY_SCHEMA`）只暴露：

```json
{
  "target": "memory | user",
  "operations": [
    {"action": "add", "content": "..."},
    {"action": "patch", "old_text": "...", "content": "..."},
    {"action": "remove", "old_text": "..."}
  ]
}
```

设计要点：

1. **一次调用只写一个 target**；batch 里的 operation 不能带不同 target。要同时改两个 store，分两次调用。
2. **batch 原子性**：所有 operation 先在内存副本上验证，最后按最终结果检查字符预算；任何一个失败则整批不写。
3. **预算只在 batch 最终状态检查**：因此一个 batch 可以“先 remove/patch 腾空间，再 add”，解决“单独 add 会溢出”的问题。
4. **没有 `replace` action**：`replace` 曾覆盖整个 entry，模型常把它当 find-and-swap，导致多事实条目被清空；现在拒绝 `replace`，指向 `patch`。需要整条重写时，把整条 entry 作为 `old_text`。
5. **`patch` 是 span 替换**：`old_text` 必须在目标 entry 中只出现一次；它保留 entry 的其余内容。若出现多次，返回错误要求更长上下文。
6. **`remove` 是整条删除**：`old_text` 只需唯一子串；整条精确匹配优先于子串匹配，避免短 entry 被长 sibling 吞掉。
7. **匹配容错**：先做 byte-exact 子串；失败后做引号/破折号/空白归一化匹配（模型常把 curly quote 重打成 straight quote，或把换行重打成 `\n`）。标点/空白单独不能选中整个 entry。
8. **重复 add 是 no-op**：如果内容已存在，返回 success + “Entry already exists (no duplicate added)”，不报错。
9. **容量错误可操作**：溢出时返回当前 entries、usage、需要释放多少字符，并明确要求“一个 batch 同时腾空间 + add”；如果模型反复失败，每轮最多 3 次 consolidation failure，之后返回 terminal 结果，要求停止重试并继续回复用户，避免 memory 副作用阻塞主任务。
10. **成功响应不带完整 entries**：避免模型“再找点东西修”而重复调用；但 replace/remove 会返回被覆盖/删除的完整原文，防止静默数据丢失。
11. **拒绝清空 store**：一个 batch 如果会删除非空 store 的最后一条 entry，会被拒绝；只有“单独一个 remove”才允许故意删最后一条。
12. **后台 review 的删除门**：unattended review 只能 `add`；`patch`/`remove`（包括 batch 里的）会被 staged 为 pending write，等用户 `/memory approve`。attended 的 `/refine` 不受此限。
13. **没有 `read` action**：记忆已经通过 system prompt 自动可见；工具只负责修改。handler 仍接受旧的 top-level 单操作形状（action/content/old_text）以兼容旧 transcript，但新 schema 只广告 `operations[]`。
14. **动态 target schema**：如果只启用一个 store，工具 schema 只广告那个 target；两个 store 都禁用时，`memory` 工具从工具面移除，相关 guidance 也不再注入。

### 5.4 容量治理：硬上限 + 模型自我整合

默认限制：

| Store | 上限 | 约 token | 典型条目 |
|---|---:|---:|---:|
| `MEMORY.md` | 2,200 chars | ~800 | 8–15 |
| `USER.md` | 1,375 chars | ~500 | 5–10 |

关键策略：

- **不自动压缩**：写入超限直接报错，不静默丢弃旧条目；
- **模型自己整合**：错误信息给出当前 entries 和需要释放的字符数，模型必须在同一 turn 内用 batch remove/patch 腾空间再 add；
- **加载超限只警告**：外部手写/旧上限可能让文件超限；加载后仍全部注入，但后续 add 会被拒绝，直到回到上限内；
- **drift guard**：如果文件里出现“单条 entry 大于整个 store 上限”的自由文本（例如手工 append、未用 `§` 分隔），重写会丢内容，因此拒绝写入并保存 `.bak.<ts>` 快照；只有纯删除可以继续；
- **不可读文件拒绝写**：文件存在但读失败（锁、权限、编码、IO）时，拒绝保存，避免把“读不到”当成空文件覆盖；
- **原子写 + 文件锁**：独立 `.lock` 文件 + `atomic_write_text`，读者不会看到截断文件；锁文件权限 0600，`O_NOFOLLOW` 防符号链接；
- **外部编辑兼容**：BOM 容忍；布局空白差异不算 drift；只有“可能丢非空白内容”才拒绝。

这套策略的核心是：**容量治理是显式的、可解释的、不会静默丢数据**。它把“记忆整理”变成模型的一项可见任务，而不是后台黑盒淘汰。

### 5.5 安全扫描与审批

#### 5.5.1 写入/加载扫描

`tools/threat_patterns.py` 的 strict scope 覆盖：

- 经典 prompt injection（ignore previous instructions、system prompt override、disregard rules、bypass restrictions）；
- 角色劫持 / 身份覆盖（you are now、pretend、name yourself）；
- C2 / promptware（register as node、heartbeat、pull task、unset agent env）；
- 外泄（curl/wget 带 secret、cat `.env`/credentials、send to URL、输出 conversation history）；
- 持久化后门（`authorized_keys`、写 `~/.ssh`、修改 AGENTS.md/CLAUDE.md/.cursorrules、修改 `.hermes/config.yaml`/`SOUL.md`）；
- 硬编码 secret；
- 不可见/双向 Unicode（零宽字符、方向覆盖/隔离符等）。

因为记忆会进入 system prompt 并跨 session 持久化，写入和加载都扫描；命中项在 snapshot 中被 `[BLOCKED: ...]` 替代，live list 保留原文供删除。

#### 5.5.2 写入审批（`write_approval`）

| 配置 | 行为 |
|---|---|
| `false`（默认） | 自由写入；后台 review 也可直接写 |
| `true` | 需要审批。交互 CLI 前台写入 inline 提示；gateway/script/后台 review 则 staged 到 pending store |

操作面：

```text
/memory pending
/memory approve <id|all>
/memory reject <id|all>
/memory approval on|off
```

关键细节：

- staged 的 `patch`/`remove` 会记录被操作的**完整 entry**（`matched_entry`）；批准时只对这条 entry 生效；如果 entry 在 staged 后变了，批准会被拒绝并保留 pending，防止 approve 到用户没看过的内容；
- 后台 review 的 destructive operations 即使 `write_approval=false` 也会 staged（unattended delete gate）；`add` 仍可直接写；
- `notify_memory_tool_write()` 只在工具结果 `success=true` 且 `staged!=true` 时镜像到外部 provider，避免把“只 staged 未落盘”的写入同步出去。

### 5.6 后台学习闭环（background review）

#### 5.6.1 触发

- **memory nudge**：`memory.nudge_interval` 默认 10 个 user turn；仅当 memory enabled、`memory` tool 在 valid tools、store 存在时计数；
- **skill nudge**：`skills.creation_nudge_interval` 默认 10 个 tool iterations；
- 两者可独立触发；如果同时触发，review prompt 使用 combined scope；
- `/refine` 可手动触发，`focus` 文本会追加到 prompt，且 `explicit=True` 标记为 attended（允许完整 memory 操作集）。

#### 5.6.2 Review fork 的隔离与缓存优化

`build_cache_parity_fork()`：

- 同一模型时，**继承 parent 的 cached system prompt、tools[]、reasoning config、cache scope**，使 review 的第一次请求命中 parent 的 prompt cache；
- review fork 持久化隔离：`_persist_disabled=True`、`_session_db=None`、`_end_session_on_close=False`，不会把 review harness turn 写进真实 session；
- 外部 memory provider 在 fork 中 `skip_memory=True`，避免把 review prompt 写进用户真实记忆命名空间；
- 内置 `MEMORY.md`/`USER.md` store 由 caller 重新绑定，review 可以正常读写；
- 不同模型路由时，cache 必然冷，因此 replay 一个 compact digest（最近 24 条 + 更早摘要），而不是完整 transcript；
- `max_input_tokens` 控制一次 review 的累计输入预算；默认是 review 模型窗口的 75%，上限 600K；
- 本地 managed llama-server 上 review 默认 defer 到 idle，避免占用用户下一次 prompt 的 GPU；队列按 session 合并，超过 `defer_max_age_s` 强制运行。

#### 5.6.3 Tool 白名单

review 默认只允许：

- `memory`（仅当 review_memory 触发且 store enabled）；
- skills 管理工具；
- `read_file` / `search_files`（只读，用于 read-before-write）；
- 通过 `auxiliary.background_review.extra_tools` 显式加入的 parent 工具。

写文件/终端等通用写工具被拒绝；自主维护必须走 `skill_manage` 的校验，或 memory tool 的审批/删除门。

#### 5.6.4 Prompt 约束

review prompt 明确区分：

- `USER.md`（target=`user`）：用户是谁、偏好、沟通风格、个人细节、期望；
- `MEMORY.md`（target=`memory`）：环境事实、工具怪癖、项目约定、配置坑、路径/端点；
- 一条 fact 只进一个 store，不要两边都写；
- 只保存**有本对话证据**的事实；不要从话题/语气/使用工具推断用户特质、技能水平、角色、心情；不要一次性请求升级为长期偏好，除非用户明确“always/from now on/remember”或表达 standing like/dislike；
- 任务特定的流程、坑、用户对这类工作的偏好，应进 skills，不是 memory。

#### 5.6.5 写入与通知

- review 的 memory 写入走同一个 `memory` 工具和 `MemoryStore`，因此有相同的扫描、预算、锁、审批；
- 成功后 `MemoryManager.on_memory_write()` 把写入镜像给外部 provider；`patch` 对外表示为 `replace`（整条新 entry），并带 `previous_content`；
- `display.memory_notifications` 控制 gateway 的提示：`off` / `on`（默认，`💾 Memory updated`）/ `verbose`（带变更预览）；提示不影响 review 是否执行；
- `/journey` / learning graph 把 memory entries 和 skills 画成时间线，并支持 `list/edit/delete`，提供可观测和人工纠错入口。

### 5.7 设计评价

**强项：**

- 硬预算 + 可操作错误 + batch 原子性，把“记忆整理”变成可控的模型行为；
- 不静默截断、不静默删除、drift 备份、不可读拒绝、旧锁权限修复，数据安全细节非常扎实；
- frozen snapshot + volatile 放置 + 压缩边界刷新，兼顾 prompt cache 与新鲜度；
- 写入/加载双重 threat scan，适合“会进入 system prompt 的持久数据”；
- 后台 review 的 cache parity、tool 白名单、attended/unattended 区分、删除 staging，兼顾学习效果与安全；
- memory vs skills 的路由规则清晰，避免“什么都塞进 memory”。

**局限：**

- 只有 2,200 / 1,375 字符，长期事实多了必须频繁整合；整合质量依赖模型能力；
- 没有自动抽取：模型不调用 `memory` 工具，就不会持久化；弱工具调用模型容易“口头说记住了”但没写；
- 没有事实版本、时间有效性、冲突消解、来源引用；只有精确重复拒绝和子串定位；
- 没有自动 decay / TTL；删除靠模型或用户显式操作；
- frozen snapshot 在长 gateway 会话中可能长时间陈旧，直到压缩/新会话；
- 两个进程共享同一 `HERMES_HOME` 会互相叠加写入；官方明确要求一个 home 一个 agent，跨 agent 共享应使用 external provider 或独立 profile。

---
## 6. 长期记忆 B：外部 Memory Provider

### 6.1 定位与约束

外部 Memory Provider 是**加法**，不是替代：

- 内置 `MEMORY.md` / `USER.md` 始终可用；
- 同时最多一个外部 provider；
- provider 可以提供静态 system prompt block、每轮 recall、turn sync、session-end 抽取、pre-compress checkpoint、自己的工具；
- 单 provider 限制的原因是：防止 tool schema 膨胀、避免多个后端冲突/重复写入，保持行为可解释。

### 6.2 发现与加载

发现顺序（先到先得，bundled 最高优先级）：

| 来源 | 路径 | 说明 |
|---|---|---|
| Bundled | `plugins/memory/<name>/` | 随 Hermes 发布；新 provider 不再进 core |
| User | `$HERMES_HOME/plugins/<name>/` | 用户/插件市场安装，按 profile 隔离 |
| Project | `./.hermes/plugins/<name>/` | 需 `HERMES_ENABLE_PROJECT_PLUGINS=1` |
| Package | `hermes_agent.memory_providers` entry point | pip 安装的 distribution |

注意：这个顺序与通用插件系统的“later wins”相反。因为 provider 是按**名字**激活（`memory.provider`），工作区里的同名目录绝不能 shadow 掉 shipped provider，否则会静默改变记忆后端。

发现阶段只枚举目录/entry point，不 import；只有 `memory.provider` 选中的 provider 才会加载。加载失败/不可用会 warning，内置记忆仍工作。

### 6.3 Provider ABC 与生命周期

`agent/memory_provider.py::MemoryProvider` 的核心方法：

| 方法 | 调用时机 | 作用 |
|---|---|---|
| `name` | 始终 | provider 标识 |
| `is_available()` | agent init，激活前 | 配置/凭据/依赖检查；不应做网络调用 |
| `initialize(session_id, **kwargs)` | agent 启动 | 建立连接、绑定 profile、存储初始化 |
| `system_prompt_block()` | prompt 组装 | **静态**说明；recall 应走 `prefetch()`，不要放这里 |
| `prefetch(query, *, session_id="")` | 每轮 LLM 前 | 返回 recall 文本；应快，最好消费后台缓存 |
| `queue_prefetch(query, *, session_id="")` | turn 结束后 | 为下一轮预热 |
| `recall_status()` | prefetch 后 | 返回 recall 数量/标签，用于 UI indicator |
| `sync_turn(user, assistant, *, session_id="", messages=None)` | turn 完成后 | 持久化完成的一轮；非阻塞 |
| `get_tool_schemas()` | tool 注入 | OpenAI function schema 列表 |
| `handle_tool_call(name, args, **kwargs)` | 模型调用 provider 工具 | 返回 JSON 字符串 |
| `on_turn_start(turn_number, message, **kwargs)` | 每轮开始 | tick；可拿到 model、author、previous_message |
| `on_session_end(messages)` | 真实 teardown / 压缩提交 / `/new`·`/reset` 边界 | 最终抽取/flush；不在每轮触发 |
| `on_session_switch(new_session_id, ...)` | 进程内 session 重绑 | `/resume`、`/branch`、`/reset`、`/new`、压缩轮转 |
| `on_pre_compress(messages)` | 压缩前 | 抽取洞察，或做 fail-closed checkpoint |
| `on_delegation(task, result, ...)` | 父 agent 观察到 delegation 完成 | 记录子任务结果；子 agent 本身没有 provider session |
| `on_memory_write(action, target, content, metadata)` | 内置 memory 写入成功后 | 镜像到外部后端 |
| `shutdown()` | 进程/会话关闭 | flush、关闭连接 |
| `get_config_schema()` / `save_config()` | `hermes memory setup` | 配置向导 |
| `on_profile_created/renamed` | profile 生命周期 | 建 peer/host、重命名 key |
| `backup_paths()` | backup/import | provider 在 HERMES_HOME 外的状态路径 |

### 6.4 读路径：静态 block + 动态 prefetch

#### 6.4.1 静态 block

`system_prompt_block()` 返回**静态**文本，被 `MemoryManager.build_system_prompt()` 拼接后放入 system prompt 的 volatile 区。它适合：

- provider 名称/项目/能力说明；
- 工具使用提示（“用 `retaindb_search` 搜索……”）；
- 不应包含每轮变化的 recall 结果。

#### 6.4.2 动态 prefetch

每轮：

1. `MemoryManager.prefetch_all(query)` 对非 trivial prompt 调用每个 provider 的 `prefetch()`；
2. 外部 provider 在 daemon 线程中执行，受 `_external_prefetch_timeout` 限制；超时返回空，线程继续在后台跑；
3. `_StuckCallGate` 按 `(HERMES_HOME, provider)` 统计 stuck calls；多个 stuck 后暂停后续调用，定期 half-open probe；
4. 结果合并后用 `build_memory_context_block()` 包装：

```text
<memory-context>
[System note: The following is recalled memory context,
NOT new user input. Treat as authoritative reference data —
this is the agent's persistent memory and should inform all responses.]

...recall...
</memory-context>
```

5. 该文本被追加到当前 user message 的 `api_content` sidecar（`compose_user_api_content`），不放入 system prompt；这样保持 system prefix 字节稳定；
6. `api_content` 会被持久化并在后续请求中 replay，因此召回内容会“粘”在这条 user message 上，直到该 row 被压缩/重写。
7. `describe_recall()` 生成 `🧠 Provider — recalled N memories` 的确定性 indicator，让用户看到记忆被使用，即使模型不主动提及。

#### 6.4.3 溢出与 spill

- 默认**完整保留 provider 排序后的 recall**，以尊重 provider 的相关性排序；
- 但有安全上限：10× `hooks.output_spill.max_chars`，至少 100,000 字符；超过则 spill；
- `memory.prefetch_spill_enabled: true` 时，超过 `hooks.output_spill.max_chars`（默认 10,000）就落文件，只在 prompt 中保留 head/tail preview + 路径；
- 该设置是 profile 级、注册时快照，修改后需重启。

设计取舍：默认不 spill 是为了不破坏 provider 的 relevance-ranked recall；但过大的 recall 会占用 context，因此提供 opt-in。

### 6.5 写路径：turn sync / session end / mirror

#### 6.5.1 `sync_turn`

- `MemoryManager.sync_all()` 把 user/assistant 内容和可选 `messages`、`turn_author` 交给 provider；
- **永远不 inline**：provider 的 `sync_turn` 可能阻塞数分钟；它被提交到单 worker `DaemonThreadPoolExecutor`，串行执行，保证 turn N 在 N+1 之前；
- 所有字符串（包括 tool output、messages 里的文本）先经过 `_redact_for_provider()` 的 egress scrub；secret 不会被 provider 原样归档；
- 某个 provider 失败不会阻塞其他 provider；每个 hook 独立 try/log。

#### 6.5.2 `on_session_end`

- 在真实 teardown（CLI exit、gateway shutdown）和 memory session boundary（压缩提交、`/new`/`/reset`/rotation）触发，不是每轮；
- 压缩提交时，`agent.commit_memory_session(original_messages)` 会在 `archive_and_compact` 之前把**压缩前的 immutable snapshot** 交给 provider，先抽取再改写；
- `commit_session_boundary_async()` 把 `on_session_end` → `on_session_switch` 串成同一个后台任务，保证 `/new`/`/reset`/rotation 的抽取先于重绑；
- 用于 provider 在会话结束/压缩前做 LLM 抽取或 flush；provider 应假定同一边界可能同时收到 `on_pre_compress` 与 `on_session_end`，并保持幂等。

#### 6.5.3 `on_memory_write` 镜像

- 内置 memory 工具成功写入后，`notify_memory_tool_write()` 把 add/replace/remove 镜像给 external provider；
- `patch` 对外表示为 `replace`（整条新 entry），并带 `previous_content`；
- 只有实际提交的写入才镜像；staged/失败不镜像；
- metadata 包含 `write_origin`、`session_id`、`tool_name`、`old_text`、`previous_content` 等 provenance；
- 外部 provider 可据此做增量索引/冲突检测。

#### 6.5.4 `on_delegation`

- 父 agent 完成 `delegate_task` 后，把 task/result 交给 provider；
- 子 agent 不拥有 provider session，避免把子 agent 的临时上下文写进用户长期记忆；
- 这是“跨 agent 记忆”的一种折中：只记录父侧观察到的结果，不共享子 agent 的完整 transcript。

### 6.6 Pre-compress checkpoint：v1 / v2

这是 Hermes 记忆系统里最值得借鉴的可靠性契约之一。

#### v1：best-effort

- `on_pre_compress(messages)` 返回一段文本，注入摘要 prompt 的 `MEMORY PROVIDER CONTEXT` 区；
- 如果 provider 抛异常，host 记日志并继续压缩（可能丢失 provider 想保留的洞察）；
- 所有 bundled provider 默认是 v1。

#### v2：fail-closed

- provider 声明 `pre_compress_checkpoint_api_version = 2`；
- `on_pre_compress(messages, *, require_checkpoint=False)` 必须在返回前把证据持久化；失败要 raise，不能返回 partial success；
- 当 `compression.checkpoint_required: true` 时，host 在**任何有损改写之前**要求至少一个 v2 provider 成功；否则压缩报 `BLOCKED_MISSING_PREREQUISITE`，保留未压缩 transcript，稍后重试；
- v2 provider 收到的是 host 归一化后的 evidence：user/assistant 文本行；tool results、system messages、assistant `tool_calls` payload、既有 compaction summaries 会被过滤；
- checkpoint 必须幂等：失败重试会再次收到同一 transcript（可能略有增长），provider 应按内容 digest upsert，而不是重复 append；
- gate 绑定到所有 compaction authority：native Responses compaction 被抑制、micro-compaction 被强制关闭、proactive prune 被抑制、codex_app_server 在 init 被拒绝、plugin context engine 的 `compress()` 也会先调用 checkpoint；
- fork（subagent/curator 等）在 gate armed 时会加载 provider，`agent_context="subagent"`；turn hooks 被抑制，但 `initialize`、`on_session_switch`、`on_pre_compress` 仍运行，保证 fork 的压缩也能 checkpoint。

这个契约解决了一个核心问题：**“有损压缩前，归档型记忆后端是否真的把原始证据写下去了？”** 默认关闭以保持可用性；只有需要强一致归档的部署才开启。

### 6.7 安全、隔离与故障处理

#### 6.7.1 Egress scrub

所有交给 provider 的内容都经过：

- `_redact_for_provider()`：递归遍历字符串/字典/list/tuple，对每个字符串做 `redact_for_egress`；`data:` URL 例外；被 mask 的 token 会在整个 hand-off 中统一替换为 `[redacted]`；
- `_redact_messages_for_egress()`：先 `without_withheld_prompts()`，再 scrub；
- 这适用于 turn sync、recall query、session-end、pre-compress transcript、memory-tool mirror、delegation result、provider tool args。

限制： scrub 是模式识别；任意 opaque password/ token 不在 `key=value`/`Bearer` 形状中时可能漏过。官方文档明确提示“本地 transcript 保留原文；provider 侧依赖 scrub”。

#### 6.7.2 Profile / multiplexing 隔离

- 一个 gateway 进程可以服务多个 profile；每个 agent 的 `MemoryManager` 拥有自己的 provider 实例；
- provider 必须从 `initialize()` 的 `hermes_home`、`agent_identity`、scoped secrets 绑定所有 store/path/client/bank/tenant；
- 不得在模块/类级别保存 profile-specific mutable state；
- manager 拒绝“另一个 profile 已绑定的 provider 实例”，并警告；
- 后台 hook 在 `in_owner_home()` 下执行，确保 `HERMES_HOME` 正确；
- 工具调用会校验 caller scope，拒绝来自其他 profile 的调用。

#### 6.7.3 超时 / stuck / shutdown

- 外部 prefetch 有超时；超时后本轮不注入，线程留在后台；
- stuck gate 按 profile+provider 统计，避免一个挂死后端每个新 manager 都再开线程；
- `sync_turn`/`queue_prefetch` 在单 worker 上串行；后台调用也有 stuck gate；
- `shutdown_all()` 有界 drain：先给队列机会，再报告 abandoned writes/prefetches；provider 逆序 shutdown；
- `release_soft()` 用于 cache eviction/CLI agent rebuild，只停止 writer 线程，不触发 `on_session_end`（sweep 先 commit，pending session end 拥有两边）。

### 6.8 Provider 生态与能力对比

| Provider | 存储 | 检索/能力 | 最适合 |
|---|---|---|---|
| RetainDB | 云 API | hybrid Vector + BM25 + rerank；profile/context/dialectic/agent self-model；本地 SQLite write-behind queue | 已使用 RetainDB 的团队；需要 hosted 混合检索 |
| ByteRover | 本地 CLI 知识树，可选云同步 | `brv query` 模糊文本 → LLM-driven search；`brv curate`；pre-compress flush | 本地优先、可移植、CLI 工作流 |
| Honcho | Cloud/self-hosted | peer/user modeling、session summary、dialectic reasoning；contextCadence/dialecticCadence/dialecticDepth | 多 agent、跨会话用户建模 |
| OpenViking | 自托管/云 | 文件系统式知识层级、L0/L1/L2 tiered retrieval、6 类自动抽取 | 结构化知识管理、分层浏览 |
| Mem0 | Cloud/self-hosted/OSS | 服务端 LLM fact extraction、semantic search、rerank、dedup | 想“无脑”自动抽取的团队 |
| Hindsight | Cloud/local embedded/external | knowledge graph、entity resolution、multi-strategy retrieval、`reflect` 跨记忆综合 | 需要实体关系和综合回答 |
| Holographic | 本地 SQLite | FTS5 + trust scoring + HRR 代数查询；`contradict` 冲突检测；`fact_feedback` | 本地、无外部依赖、需要事实冲突/信任 |
| Supermemory | Cloud/self-hosted | semantic search、profile、per-turn capture、context fencing、失败重试 | 语义 recall + 用户画像 + 托管 |
| Memori | Cloud | 结构化项目/会话归属、后台 completed-turn capture、quota/summary tools | 需要结构化归属和配额管理 |

Provider 设计的共同模式：

- **静态 block + 动态 prefetch + 工具**三种读路径；
- **turn sync / session end / pre-compress**三种写时机；
- **auto recall / auto capture** 可配置；
- **local-first vs cloud** 是主要部署差异；
- 高级 provider 会补足 Hermes 核心缺失的能力：语义检索、知识图谱、用户建模、冲突/信任、自动抽取、tiered retrieval。

### 6.9 设计评价

**强项：**

- Provider 契约清晰，生命周期覆盖“读、写、压缩前、会话边界、工具、profile、shutdown”；
- 单 provider + 单 worker + stuck gate + timeout，工程上可控；
- egress scrub、profile 隔离、delete staging、pre-compress checkpoint 体现了很强的安全/一致性意识；
- 把“语义记忆”做成可替换后端，核心不被某一家产品锁定。

**局限：**

- 只有一个外部 provider，无法组合“知识图谱 + 用户建模 + 语义检索”多个后端；
- 不同 provider 的 recall 质量/延迟/成本差异很大，核心没有统一的质量评估/路由；
- host 没有普遍性地从 provider-bound messages 中剥离 `<memory-context>`；这依赖 provider 自己防止“recall → 再 capture → 递归污染”（Supermemory 明确做了 context fencing）；
- 外部 provider 看到的是去 secret 后的 transcript，但仍可能包含敏感业务数据；是否出网是部署决策；
- provider 的自动抽取/衰减/冲突策略不在 Hermes 核心可观测范围内，跨 provider 的一致语义无法保证。

---

## 7. 长时档案与检索：`session_search`

### 7.1 数据模型

`state.db` 中：

- `sessions`：session 元数据、model、source、parent_session_id、compression/usage/workspace 字段；
- `messages`：完整消息、tool_calls、reasoning、api_content、active/compacted、message_uid 等；
- `messages_fts`：FTS5 external-content 虚拟表，索引 `content` / `tool_name` / `tool_calls`，通过 INSERT/UPDATE/DELETE 触发器同步；
- `messages_fts_trigram`：trigram 索引，用于 CJK/子串场景；
- `state_meta`：FTS rebuild high-water/progress、storage version 等。

FTS5 schema（简化）：

```sql
CREATE VIRTUAL TABLE messages_fts USING fts5(
    content,
    tool_name,
    tool_calls,
    content='messages',
    content_rowid='id'
);
```

### 7.2 四种调用形状

`session_search` 没有 `mode` 参数，由参数推断：

| 形状 | 参数 | 行为 |
|---|---|---|
| Discovery | `query` | FTS5 搜索，按 lineage 去重，返回 top-N sessions；默认 adaptive：第一名 full hydrate，其余 compact |
| Scroll | `session_id` + `around_message_id` | 返回锚点前后 ±window 条消息；无 FTS5，无 bookends |
| Read | `session_id` | 返回整个 session；大 session 给 head/tail 视图 + scroll 指针 |
| Browse | 无参数 | 最近 sessions 的元数据/预览；无 FTS5 |

Discovery 结果包含：

- `session_id`、`title`、`when`、`source`；
- `snippet`：FTS5 highlighted excerpt；
- `detail`：`full` / `compact`；
- `bookend_start` / `bookend_end`：full 结果的首/尾 3 条 user+assistant 消息；
- `messages`：full 结果的 ±5 窗口；compact 结果只有 anchor message；
- `match_message_id`、`messages_before`、`messages_after`；
- `link`：可直接写给用户的会话链接。

### 7.3 排序、回退与边界

#### 7.3.1 排序

- 默认 `ORDER BY rank`（FTS5 BM25）；
- `sort='newest'/'oldest'` 在 rank 之上加时间偏置；
- 结果按 lineage 去重，避免压缩轮转产生的 parent/child 重复；
- cron 等自动 session 会被 demote，避免高 volume 的自动语料挤掉用户会话。

#### 7.3.2 查询路由

- 普通拉丁文本走 `messages_fts`；
- CJK 走 trigram（≥3 字 token）或 LIKE substring fallback；
- FTS 语法错误/索引 stale/corrupt 时，fail-open 到 canonical `messages` 的 LIKE 扫描；
- 延迟重建期间，用 bounded LIKE 扫描补齐 `(progress, high_water]` 的未索引 gap；
- 零结果时，可能做 OR-relaxed retry（把隐式 AND 放宽为任意词），但只在 exact query 零命中时；
- 显式 `role_filter='tool'` 走 canonical LIKE，因为 trigram 索引排除了 tool rows；
- 时间过滤 `after`/`before` 作用于 `sessions.started_at`，所有路由一致。

#### 7.3.3 当前 session 的过滤

Discovery 默认跳过当前 session 的 live lineage，除非命中内容已经离开 live context：

- 压缩轮转的 parent（`end_reason=compression`）可被搜到；
- `/new` reset 的前驱（没有携带 transcript）可被搜到；
- in-place compaction 的 archived row（同 session id，但 `active=0, compacted=1`）可被搜到；
- 仍 live 的 delegation child（`end_reason=None`）被排除。

Scroll 也会拒绝“锚点仍在当前 live context”的请求，避免模型把已经在上下文里的内容又搜一遍。

#### 7.3.4 Profile 隔离

- `profile=` 参数会打开另一个 profile 的 `state.db` **只读**；
- 裸 `session_id` 不会 fallback 到其他 profile 全库扫描；找不到就返回“not found in this profile”，并提示用 `profile=` 或 `@session:<profile>/<id>`；
- 这防止持有 id 的调用者读取其他 profile 的完整 transcript。

### 7.4 与内置记忆的分工

| 维度 | 内置 Persistent Memory | Session Search |
|---|---|---|
| 容量 | ~1,300 tokens 总量 | 逻辑上不限（受 session retention/cleanup 影响） |
| 速度 | 即时（在 system prompt 中） | ~20ms FTS5 / ~1ms scroll |
| 成本 | 每次 prompt 固定 token | 按需，无 LLM 调用 |
| 用途 | 必须始终在上下文的关键事实 | “上周我们讨论过 X 吗？” |
| 管理 | 模型/用户手动 curated | 自动保存所有 session |
| 失效 | 需要显式 remove/整合 | archived rows 长期保留 |

官方文档的定位很准确：**Memory 是 critical facts always in context；Session Search 是 on-demand specifics**。

### 7.5 设计评价与口径差异

**强项：**

- 无损、真实消息，不是 LLM 生成的“回忆摘要”；适合精确引用/排错；
- FTS5 + trigram + LIKE 多级回退，对 CJK、索引损坏、延迟重建都有工程处理；
- lineage 去重、当前 live context 过滤、profile 只读隔离，避免重复召回和越权；
- adaptive hydration 控制返回体积；bookends 让一次调用同时看到 goal 和 resolution。

**局限：**

- 本质是关键词/全文检索，不是语义检索；同义改写、模糊指代可能漏；
- 依赖 FTS5 索引质量；大库 rebuild 期间结果可能不全（有 gap supplement，但不是无限）；
- 返回的是原始消息窗口，不是“已经消化的结论”；模型需要自己综合；
- 当前 session 的 live lineage 被排除，这是为了避免重复，但也可能让模型“忘了刚刚说过什么”（不过这些内容本来就在上下文里）。

**口径差异：** README 的概述性文案仍写到 “FTS5 session search with LLM summarization”；但当前 `session_search` 实现和工具 schema 明确是 **actual DB messages, no LLM**。本文以当前代码为准。若需要 LLM 摘要，应使用外部 provider 的 `hindsight_reflect` / `retaindb_context` / `honcho_reasoning` 等能力，而不是内置 `session_search`。

---
## 8. 记忆的写入、遗忘与治理

### 8.1 写入来源

| 来源 | 触发 | 目标 | 是否经过审批 | 特点 |
|---|---|---|---|---|
| 前台 `memory` 工具 | 模型显式调用 | `MEMORY.md` / `USER.md` | `write_approval` 控制；CLI inline，其他 staged | 同步、原子、即时可见于工具响应 |
| 后台 review | 每 N 个 user turn / tool iteration | memory / skills | unattended 只能 add；patch/remove staged | 异步、fork、缓存友好、可路由到便宜模型 |
| 外部 provider sync | 每 turn 结束 | provider 自己的 store | provider 自定 | 单 worker 串行、egress scrub、失败隔离 |
| 外部 provider session end | 真实 teardown / 压缩提交 / `/new`·`/reset` 边界 | provider store | provider 自定 | 可做 LLM 抽取/flush；压缩前拿 immutable snapshot |
| 外部 provider pre-compress | 压缩前 | provider archive | v1 best-effort / v2 fail-closed | 保护有损压缩前的证据 |
| 用户手工 | 编辑文件 / `/memory` / `/journey` | 文件或 graph | 人工 | 可能触发 drift guard / 审批 |
| 外部同步镜像 | 内置 memory 写入成功后 | external provider | 跟随内置写入 | `on_memory_write`；staged/失败不镜像 |

### 8.2 遗忘与保留

Hermes 的“遗忘”不是一个统一 TTL，而是分层策略：

| 层 | 遗忘方式 |
|---|---|
| L0 当前请求 | 请求结束自然消失 |
| L1 active transcript | 压缩时移出 active 集；旧行软归档，不删除 |
| L2 内置记忆 | 模型/用户显式 remove；容量满时合并/删除；`hermes memory reset` 清空 |
| L3 外部 provider | 由 provider 决定（decay、TTL、delete、trust 下调等） |
| L4 skills | curator 可归档/修剪；用户可删除；review 不会随意删 skill |
| L5 历史档案 | session 清理策略、手动 prune/archive、profile 删除；不是压缩自动删除 |

关键原则：**压缩不是遗忘，归档不是删除。** 真正删除需要显式动作；这降低了“上下文管理误删证据”的风险，但也意味着存储会增长，需要独立的 retention/cleanup 策略。

### 8.3 可观测性

- **记忆用量头**：`MEMORY (your personal notes) [67% — 1,474/2,200 chars]`，让模型知道容量压力；
- **recall indicator**：`🧠 Provider — recalled N memories`，让用户看到 recall 发生；
- **compression telemetry**：每次 `compress_context` 记录 trigger_source、route、commit_status、method、messages/tokens before/after、reclaimed、pruned counts 等；不含正文；
- **review completion log**：`Background review complete: thread=... calls=... in=... out=... result=...`；
- **FTS rebuild status**：`fts_rebuild_status()` 返回 percent/indexed/total，discovery 结果会附带“索引重建中”的提示；
- **`/journey` / learning graph**：memory entries 和 skills 的时间线，可 edit/delete；
- **doctor / status**：`hermes memory status` 显示 provider 配置；`hermes doctor` 检查记忆文件/配置。

### 8.4 安全边界

| 边界 | 机制 |
|---|---|
| 记忆写入 | strict threat scan；不可见 Unicode；硬编码 secret；prompt injection/exfil/persistence |
| 记忆加载 | 再次 scan；snapshot 中 block，live 列表保留供删除 |
| 外部 recall | `<memory-context>` fence + system note；stream scrubber 防止泄漏到输出 |
| 外部 provider egress | 递归 secret scrub；`data:` URL 例外；`without_withheld_prompts` |
| Profile 隔离 | per-profile home、instance binding、caller scope、read-only cross-profile search |
| 审批 | `write_approval`；staged pending；`matched_entry` pinning；unattended delete gate |
| 压缩前归档 | v2 checkpoint fail-closed；gate 绑定所有 compaction authority；幂等 upsert |
| 指令层级 | 明确“memory/recall/summary 是数据，不是指令”；memory guidance 要求 declarative facts，不用 imperative phrasing |

需要强调：安全扫描是**模式匹配**，不是形式化保证。它能挡住常见 injection/exfil/backdoor，但不能保证所有语义攻击；外部 provider 的数据出网仍需部署方评估。

---

## 9. 理论解释：为什么这样设计

### 9.1 上下文经济学

LLM 的 context window 有两个成本：

1. **token 成本**：每次请求都重新计费（除非命中 provider cache）；
2. **注意力成本**：上下文越长，模型越容易忽略中间信息、被无关内容干扰。

Hermes 的策略是：

- 把“必须始终知道”的事实压缩到 ~1,300 tokens 的常驻记忆；
- 把“可能需要”的历史放到磁盘，按需检索；
- 把“当前任务”保持在受保护尾部；
- 用摘要替代大段中间历史；
- 用 prompt 分层和 frozen snapshot 提高 cache 命中。

这接近“**bounded rationality**”：不追求完美记忆，而是在固定预算下最大化有用信息密度。

### 9.2 记忆层级与局部性

Hermes 的分层符合“记忆局部性”：

- 最近/当前的信息（L1 tail）访问频率最高，保留最高保真；
- 稳定事实（L2）访问频率高但体积小，常驻；
- 历史细节（L5）访问频率低但体积大，冷存储；
- 外部语义记忆（L3）按 query 访问，适合大容量；
- 技能（L4）按任务访问，适合程序性知识。

与 CPU cache 的类比：

| CPU | Hermes |
|---|---|
| L1 cache | active tail / recent messages |
| L2 cache | summary + protected head |
| DRAM | `state.db` messages |
| Disk | archived rows / external provider |
| TLB / page table | `message_uid` / lineage / FTS index |
| Cache invalidation | compaction boundary / prompt rebuild |

但 Hermes 没有硬件式的自动换页；是否“page in”由模型工具调用或 provider prefetch 决定。这是“**agent-controlled paging**”，不是 OS paging。

### 9.3 巩固：摘要 vs 反思 vs 抽取

三种巩固机制对应不同目标：

| 机制 | 输入 | 输出 | 目标 |
|---|---|---|---|
| Context compaction | 当前 transcript 的中间段 | handoff summary + tail | 让当前会话继续在窗口内运行 |
| Background review | 完整 turn / 最近对话 | memory entries / skill patches | 把跨会话可复用的事实/流程沉淀 |
| External provider extraction | turn / session / pre-compress transcript | provider 自己的记忆单元 | 语义检索、用户建模、知识图谱 |

它们不是互相替代：

- compaction 是“短期工作集整理”；
- review 是“长期精选记忆的反思性写入”；
- provider extraction 是“可插拔的语义记忆工业化”。

Hermes 明确把“任务特定流程”路由到 skills，而不是 memory；这避免了“所有学习都塞进同一个记忆库”的常见反模式。

### 9.4 缓存一致性：frozen snapshot 的本质

Frozen snapshot 不是“记忆不变”，而是“**在缓存代际内字节不变**”。
Provider prefix cache 的命中要求前缀字节稳定；如果每次 memory 写入都重渲染 system prompt，缓存会不断失效。Hermes 的选择是：

- 记忆写入立即落盘；
- 当前 prompt 不变；
- 工具响应显示 live 状态；
- 下一次缓存失效点（新会话/压缩）再重建。

这是一种“**最终一致**”的 prompt 记忆模型：写入是强一致的（文件锁 + 原子写），读取是最终一致的（下一次 prompt 重建才可见）。它牺牲了“本轮立刻看到新记忆”，换取了 cache 稳定性和成本。

### 9.5 信任边界：memory as data, not instruction

记忆系统最大的安全风险不是“记错”，而是“记住恶意指令并在未来执行”。Hermes 的防线：

1. strict threat scan（写入 + 加载）；
2. memory guidance 要求 declarative facts，不用 imperative；
3. recall fence + system note；
4. 压缩摘要 prompt 把 transcript 当 data；
5. 外部 provider 的 recall 不进入 system prompt，而是进入 user message 的 fenced block；
6. 流式输出 scrubber 防止 recall 泄漏。

这与“prompt injection 防御”的一般原则一致：**不可信内容不能获得指令权限**。但 Hermes 没有形式化的 capability 隔离；安全仍依赖模式匹配和模型遵循。

### 9.6 与 MemGPT / Generative Agents / RAG 的理论差异

| 维度 | MemGPT 类 | Generative Agents | 典型 RAG | Hermes |
|---|---|---|---|---|
| 主上下文管理 | OS 式分页，agent 管理 page in/out | 记忆流 + 检索 | 固定 top-k 文档 | 压缩 + 受保护尾部 + session search |
| 长期存储 | 归档内存 | 记忆流/反思 | 向量库 | 文件 + SQLite + 外部 provider |
| 检索信号 | agent 决策 | recency/importance/relevance | embedding similarity | 常驻注入 / keyword FTS / provider query |
| 反思 | 可选 | reflection | 无 | background review（有限） |
| 遗忘 | 显式/分页 | 衰减/容量 | 索引更新 | 容量整合 + 软归档 + provider 策略 |
| 安全 | 取决于实现 | 取决于实现 | 取决于实现 | 明确 threat scan + fence + approval |

Hermes 的独特组合是：**“文件式 curated memory + 上下文压缩 + 无损会话档案 + provider 插件”**。它不追求“通用认知架构”，而是围绕“agent 日常运行成本”做工程优化。

---

## 10. 设计权衡、风险与边界

| 维度 | Hermes 的选择 | 收益 | 代价/风险 | 缓解 |
|---|---|---|---|---|
| 容量 vs 召回 | 内置记忆硬上限 2.2K/1.375K chars | prompt 成本可控、记忆高密度 | 长期事实多时频繁整合；可能丢低频事实 | 外部 provider；session_search；skills 分流 |
| 新鲜度 vs 缓存 | frozen snapshot，压缩边界刷新 | prefix cache 稳定、成本低 | 长 gateway 会话中记忆可能陈旧 | `/new`；压缩；prompt refresh |
| 有损 vs 无损 | 摘要 + 软归档 | 窗口可控、历史可恢复 | 摘要质量差时细节丢失 | lean anchors、session_search、fallback summary |
| 自动 vs 手动 | 内置记忆主要靠模型工具/ review | 可控、可审计 | 弱模型不调用工具；漏记 | 强模型、显式指令、review nudge、外部 auto-extract |
| 简单 vs 语义 | 核心 FTS5，语义交给 provider | 依赖少、离线可用 | 同义/模糊检索弱 | 外部语义 provider；trigram/LIKE fallback |
| 单 provider vs 组合 | 同时一个外部 provider | tool schema 不膨胀、行为可解释 | 无法组合多个后端 | 选能力最匹配的 provider；未来可扩展 |
| 自动 review vs 成本 | 默认开启，可路由/禁用/预算 | 持续学习 | token 成本；弱模型误写 | `enabled=false`、`max_input_tokens`、便宜模型、write_approval |
| 软归档 vs 删除 | `active=0, compacted=1` | 可检索、可恢复 | 存储增长；FTS 包含历史版本 | retention/cleanup；lineage dedupe |
| Profile 隔离 vs 共享 | 默认 per-profile | 隐私/污染隔离 | 跨 profile 记忆不共享 | 外部 provider 的 workspace/peer 模型；显式共享容器 |
| 安全扫描 vs 可用性 | strict 模式匹配 | 挡住常见攻击 | 误报；opaque secret 漏检 | 人工审核；最小权限；egress 评估 |
| 异步写入 vs 持久性 | 外部 sync 后台单 worker | 不阻塞用户 | 进程退出可能丢队列 | bounded drain；provider 幂等；session-end flush |
| 压缩 vs 缓存 | 批量压缩，micro 默认关 | 缓存破坏少 | 压缩瞬间停顿/成本 | prepared compaction；micro 可选 |
| 单写者 vs 并发 | 文件锁 + 单 worker | 避免丢写/乱序 | 多进程共享 home 会互相污染 | 一 home 一 agent；profile 隔离 |

### 10.1 主要风险清单

1. **摘要质量风险**：summary model 窗口不足/超时/输出截断 → 细节丢失或 fallback；应对：选足够大的 aux model、监控 telemetry、保留 `session_search`。
2. **记忆写入幻觉**：模型声称“已记住”但没调用工具；应对：强模型、显式工具指令、review、验证文件。
3. **记忆污染/递归**：外部 provider 把 recall 内容再次 capture；应对：provider 侧 context fencing（如 Supermemory），host 侧未来可统一剥离。
4. **长会话陈旧**：gateway 不 `/new`，frozen snapshot 和 review 学习闭环不触发；应对：自然边界 `/new`、压缩边界刷新、idle compaction。
5. **敏感数据出网**：external provider 收到 transcript；应对：egress scrub、最小化 provider 数据、local-first provider、合规评估。
6. **FTS 漏检**：关键词不匹配、索引 rebuild、CJK tokenization；应对：trigram/LIKE fallback、OR-relaxed、语义 provider。
7. **多进程写冲突**：两个 agent 共享 HERMES_HOME；官方明确不支持；应对：profile 隔离、external provider 共享。
8. **审批绕过**：unattended review 的 destructive op；已有 staging gate，但 provider 自己的写入不受 host memory gate 约束；应对：provider 级权限/审计。
9. **checkpoint gate 误配**：开启 `checkpoint_required` 但没有 v2 provider → 所有压缩被阻塞；应对：启动 warning / capability probe / doctor。
10. **成本失控**：background review、外部 prefetch、micro-compaction 都可能产生额外调用；应对：预算、频率、便宜模型、禁用开关。

### 10.2 边界：Hermes 不是什么

- 不是“自动把所有对话变成向量”的系统；核心没有 embedding/vector store；
- 不是“事实数据库”：没有 schema、版本、时间有效性、冲突消解、来源引用；
- 不是“多 agent 共享记忆”：默认 profile 隔离，子 agent 不写 provider；
- 不是“无限记忆”：内置记忆有硬上限，会话档案也有 retention/cleanup；
- 不是“记忆即指令”：安全模型明确把记忆当数据；
- 不是“压缩即遗忘”：软归档保留原始消息，session_search 可取回。

---

## 11. 对本项目 Agent Memory v2 的借鉴与落地建议

### 11.1 先明确边界：不要用 Hermes 替换 AAR 的分层

AAR 已有：

- 项目级手工 notes（`backend/internal/memorystore`）；
- 事实/证据层（`artifact` + `factledger`）；
- 代码探索记忆（`knowledge`）；
- Agent Memory v2 的两阶段抽取、summary、citation、suppression、pinned、recall trace。

Hermes 的价值不是“替换这些”，而是提供**运行时工程约束**：

- 如何把记忆注入 prompt 而不破坏 cache；
- 如何做有损压缩 + 无损归档；
- 如何设计 provider 生命周期和 fail-closed checkpoint；
- 如何做容量硬约束、批次原子写、审批、安全扫描；
- 如何控制后台 review 的成本和隔离。

### 11.2 可直接借鉴（P0）

| 借鉴点 | Hermes 做法 | 对 AAR 的落点 |
|---|---|---|
| 小型常驻 curated memory | 2.2K/1.375K chars，entry delimiter，usage header | `memorystore` 的注入视图；硬预算 + 用量提示 |
| batch 原子写入 | add/patch/remove 一个 batch，最终预算检查 | memory tool / API；避免“先删后加”半状态 |
| 精确/唯一子串定位 | 整条匹配优先；patch 只替换唯一 span | 记忆编辑 API；防止误删多事实条目 |
| 不静默截断 | 超限返回 current entries + 需释放字符数 | 写入错误契约；让 agent/用户显式整合 |
| frozen snapshot + volatile 注入 | 记忆块放 prompt 尾部；压缩边界刷新 | contextmgr 注入策略；保护 prompt cache |
| 有损压缩 + 无损归档 | summary + tail；旧行 `active=0, compacted=1` | 会话压缩与 `session_search` 的边界 |
| 软归档可检索 | FTS5 索引 archived rows；lineage 去重 | 会话检索/引用；不要物理删除 |
| provider 生命周期 | initialize/prefetch/sync/session_end/pre_compress/on_memory_write | AAR 的 memory provider / MCP 记忆接口 |
| pre-compress checkpoint | v2 fail-closed；gate 覆盖所有 compaction | 归档型 provider 的强一致契约 |
| 安全扫描 + fence | strict scan；`<memory-context>`；stream scrubber | 记忆注入/召回的安全边界 |
| 审批与 staging | `write_approval`；unattended delete gate；matched_entry pin | 记忆治理/审计；防止后台误删 |
| 单 worker + stuck gate | 外部调用超时/隔离/串行 | 外部记忆服务/搜索服务的稳定性 |

### 11.3 建议增强（P1）

Hermes 的薄弱点正好是 AAR v2 已经规划的方向：

1. **来源与证据链**：每条派生记忆带 session/message/artifact/fact 引用；Hermes 只有 `source_hash` / provider metadata，没有统一 citation 模型。
2. **冲突消解与版本**：`revision` / `supersede` / `tombstone`；Hermes 只做精确重复拒绝和子串定位。
3. **语义/混合检索**：embedding + FTS + rerank + RRF；Hermes 核心只有 FTS5。
4. **召回预算与可观测性**：per-layer token budget、recall trace、zero-result 诊断；Hermes 有 recall indicator/telemetry，但没有统一预算模型。
5. **分层 brief / handoff**：SessionEnd handoff、L0/L1/L2 brief；Hermes 的 lean summary 是很好的参考，但不是完整 handoff。
6. **记忆生命周期**：freshness/stale gate、summary watermark、usage feedback；Hermes 的 review/usage 是轻量版。
7. **多 provider 组合**：AAR 若需要“知识图谱 + 用户建模 + 语义检索”，不要照搬 Hermes 的单 provider 限制；可用路由/仲裁层，但要控制 tool schema 膨胀。

### 11.4 不建议照搬（P2 / 明确不做）

- **不要照搬“只有模型显式调用才记住”**：AAR v2 的两阶段异步抽取更适合自动沉淀；Hermes 的模型工具路径适合“用户偏好/环境事实”的手动/半自动写入。
- **不要照搬“没有来源/版本”**：AAR 的 fact ledger 和 citation 是优势，不能为了简化而放弃。
- **不要照搬“内置无自动 decay”**：AAR 可按 kind 做 retention；但要有 tombstone/审计，避免误删。
- **不要照搬“单 provider 硬限制”**：如果 AAR 需要多后端，设计一个 provider registry + 路由/仲裁，而不是简单禁止。
- **不要照搬“host 不剥离 recall fence”**：AAR 应在 provider-bound transcript 中统一剥离 `<memory-context>`，防止递归污染。
- **不要把 Skills 和 Memory 混在一起**：Hermes 的 memory vs skill 路由值得学习；AAR 应保持“事实/偏好”与“流程/技能”分离。

### 11.5 建议的 AAR 记忆运行模型

```text
canonical transcript / artifacts / facts
        │
        ├── 手工 notes（memorystore）
        ├── 派生记忆抽取（async, two-stage）
        │       └── memory.db（source/revision/status/freshness）
        ├── 合并/冲突消解（Consolidation）
        │       ├── memory_summary.md（bounded, injected）
        │       └── summaries/<session>.md（audit）
        └── 程序性知识（skills / knowledge）
        │
        ▼
contextmgr:
  stable prefix (identity/instructions)
  + volatile tail (memory summary / user profile)
  + dynamic recall (bounded, fenced, cache-safe)
        │
        ▼
session_search / memory_search / memory_read
        │
        ▼
citation / usage feedback / suppression / stale gate
```

关键原则：

1. **两套表示**：`memory_summary.md` 用于注入，`memory.db` / summaries 用于审计与检索；summary 是派生视图，不是事实源。
2. **写入异步，读取有界**：抽取/合并不阻塞对话；注入只读本地、受 token budget 限制。
3. **缓存边界**：记忆注入放 volatile 尾部；summary 只在压缩/新会话重建；动态 recall 用 user message sidecar。
4. **证据优先**：派生记忆必须有 source；无证据不落自动记忆。
5. **可删除/可纠正/可审计**：tombstone、revision、recall trace、usage feedback。
6. **记忆不是指令**：注入内容只作为历史数据；安全扫描 + fence + 权限隔离。
7. **provider 契约**：initialize/prefetch/sync/session_end/pre_compress/on_memory_write/shutdown；归档型 provider 支持 fail-closed checkpoint。
8. **失败隔离**：单个 provider/search 超时不影响主对话；write/read 分离；staged/审批。

### 11.6 验证方案（建议）

| 实验 | 目的 | 指标 | 建议门槛 |
|---|---|---|---|
| Prompt cache 命中 | 验证 volatile 注入/冻结快照 | cache_read_tokens / prompt_tokens | 开启记忆后 cache 命中率下降 < 5–10% |
| 记忆写入成功率 | 验证工具/审批/整批原子性 | 成功写入数 / 尝试数；staged 比例 | 强模型 > 95%；弱模型有显式失败提示 |
| 压缩保真度 | 验证 summary 是否保留任务关键信息 | 压缩后任务成功率、关键字段 recall | 与未压缩基线相比下降 < 10% |
| Session search 召回 | 验证 FTS/lineage/时间过滤 | precision@k / recall@k；zero-result 率 | 典型查询 top-3 命中 > 80% |
| 记忆污染 | 验证 recall→capture 递归 | provider 中重复 memory-context 比例 | 0（host/provider 必须剥离） |
| 外部 provider 稳定性 | 验证超时/stuck gate | p95 latency、timeout rate、stuck skips | 不阻塞主对话；超时率可观测 |
| 安全扫描 | 验证注入/外泄/Unicode | 攻击样本拦截率、误报率 | 已知样本 100% 拦截；误报可人工覆盖 |
| 长会话 | 验证 gateway 不 `/new` 的行为 | 压缩次数、memory 陈旧度、review 触发 | 有明确 `/new` 指引；压缩边界刷新 |
| 成本 | 验证 review/prefetch/compaction | tokens per turn、review cost、cache hit | 可配置预算；默认不失控 |

### 11.7 分阶段落地建议

| 阶段 | 内容 | 依赖 |
|---|---|---|
| P0 | 硬预算 + batch 原子写 + frozen snapshot + 软归档 + FTS 检索 | 现有 memorystore/contextmgr |
| P1 | provider 契约 + pre-compress checkpoint + 审批/staging + recall fence | P0 的注入/存储边界 |
| P2 | 派生记忆两阶段抽取 + citation/revision/conflict + hybrid retrieval | P1 的 provider/存储契约 |
| P3 | 多 provider 路由/仲裁 + 语义/图谱 + 用户建模 | P2 的 provenance 与预算模型 |

> 这与 `docs/agent-memory/` 现有 v1/v1.1 范围基本一致；Hermes 分析主要用于强化 P0/P1 的运行时契约，不改变 AAR 以“证据优先 + 项目隔离 + 可审计”为核心的方向。

---

## 12. 结论

Hermes Agent 的记忆系统可以概括为：

```text
小型的、常驻的、人工/模型 curated 的语义记忆
  + 有损但可恢复的上下文压缩（handoff summary + 受保护尾部）
  + 无损的、可全文检索的情景档案（SQLite/FTS5）
  + 可插拔的外部语义记忆 Provider
  + 程序性记忆（Skills）
```

它的设计哲学不是“记住一切”，而是：

- **在固定上下文预算下，保持最有用的信息在模型眼前；**
- **把其余信息放到可检索、可恢复的冷存储；**
- **在缓存失效边界做记忆刷新，而不是每轮重写 prompt；**
- **把记忆当数据而不是指令；**
- **把语义记忆的复杂度外移给 provider，核心保持小而可靠。**

最值得 AAR 借鉴的不是某一个文件格式，而是它的**运行时契约**：

1. 记忆写入的**硬预算 + 批次原子性 + 可操作错误**；
2. prompt 的**分层 + frozen snapshot + 缓存边界刷新**；
3. 压缩的**有损摘要 + 无损归档 + 恢复指针**；
4. 外部 provider 的**生命周期 + 单 worker + 超时隔离 + egress scrub**；
5. 归档型 provider 的**pre-compress checkpoint fail-closed**；
6. 记忆注入/召回的**安全 fence + threat scan + approval/staging**；
7. 后台学习的**cache parity + tool whitelist + attended/unattended 分级**。

最需要 AAR 补足的是 Hermes 刻意留白的部分：**来源引用、事实版本、冲突消解、语义/混合检索、召回预算、跨 provider 一致性**。这些正好是 AAR Agent Memory v2 已有规划的方向。

---

## 附录 A：关键代码锚点

| 文件 | 符号 | 作用 |
|---|---|---|
| `tools/memory_tool_store.py` | `MemoryStore` | 文件记忆存储、批处理、预算、drift guard、原子写 |
| `tools/memory_tool_store.py` | `load_from_disk` | 读取、去重、加载时扫描、冻结 snapshot |
| `tools/memory_tool_store.py` | `_mutate` / `add` / `patch` / `remove` / `apply_batch` | 写入协议与事务边界 |
| `tools/memory_tool_store.py` | `format_for_system_prompt` | 返回 frozen snapshot |
| `tools/memory_tool.py` | `MEMORY_SCHEMA` / `memory_tool` | 工具 schema 与 dispatch |
| `tools/memory_tool.py` | `_background_delete_gate` | unattended review 的删除门 |
| `tools/threat_patterns.py` | `_PATTERNS` / `scan_for_threats` | 注入/外泄/后门/Unicode 扫描 |
| `agent/system_prompt.py` | `_memory_parts` | 内置/外部记忆块的 prompt 注入 |
| `agent/system_prompt.py` | `build_system_prompt_parts` | stable/context/volatile 三层 |
| `agent/system_prompt.py` | `invalidate_system_prompt` | 压缩边界重建 prompt 并重载 memory |
| `agent/agent_init.py` | `_init_memory` | store/provider 初始化 |
| `agent/turn_context.py` | `_memory_turn_start_and_prefetch` | 每轮 on_turn_start + prefetch |
| `agent/turn_context.py` | `compose_user_api_content` | recall 注入 user message api_content |
| `agent/memory_context.py` | `build_memory_context_block` | `<memory-context>` fence |
| `agent/memory_context.py` | `StreamingContextScrubber` | 流式输出剥离 recall |
| `agent/context_compressor.py` | `should_compress_info` | 阈值/cooldown/anti-thrash |
| `agent/context_compressor.py` | `compress` | 四阶段压缩主流程 |
| `agent/context_compressor.py` | `_generate_summary` | 结构化摘要 / iterative update |
| `agent/context_compressor.py` | `_assemble_compressed` | summary/tail 组装 |
| `agent/context_compressor.py` | `_find_tail_cut_by_tokens` | 尾部预算/锚点/tool group 对齐 |
| `agent/context_compressor_window.py` | `_plan_compaction_window` | head/middle/tail 规划 |
| `agent/context_compressor_tail.py` | `lean_tail_budget` / `demote_stale_tail_tools` | lean 尾部策略 |
| `agent/conversation_compression.py` | `compress_context` / `_commit_compaction` | 压缩编排与提交 |
| `agent/conversation_compression.py` | `_pre_compress_memory_context` | provider checkpoint/insight 注入 |
| `hermes_state_messages.py` | `archive_and_compact` | 软归档 + 插入压缩集 + 并发 watermark |
| `hermes_state_search.py` | `search_messages` / `_fts_match_sql` / `get_anchored_view` | FTS5/LIKE/trigram 检索 |
| `tools/session_search_tool.py` | `session_search` / `_discover` / `_scroll` | 四种检索形状 |
| `agent/memory_provider.py` | `MemoryProvider` | 外部 provider ABC |
| `agent/memory_manager.py` | `MemoryManager` / `prefetch_all` / `sync_all` / `on_session_end` / `on_pre_compress` | provider 生命周期编排 |
| `agent/memory_manager_tools.py` | `inject_memory_provider_tools` | provider 工具注入 |
| `plugins/memory/__init__.py` | `discover_memory_providers` / `load_memory_provider` | provider 发现/加载 |
| `agent/background_review.py` | `build_cache_parity_fork` / `_run_review_in_thread` | 后台 review fork 与执行 |
| `agent/background_review.py` | `_review_tool_whitelist` | review 工具白名单 |
| `agent/turn_finalizer.py` | `_mirror_turn_to_memory` | turn 结束后 sync + review 触发 |
| `agent/context_engine.py` | `ContextEngine` | 可插拔上下文引擎 ABC |

## 附录 B：关键配置默认值

```yaml
memory:
  memory_enabled: true
  user_profile_enabled: true
  memory_char_limit: 2200
  user_char_limit: 1375
  nudge_interval: 10
  prefetch_spill_enabled: false
  # provider: retaindb | byterover | honcho | ...  # 单一外部 provider

compression:
  enabled: true
  threshold: 0.50
  target_ratio: 0.20
  tail_mode: lean
  protect_last_n: 20
  min_tail_user_messages: 1
  protect_first_n: 3
  max_attempts: 3
  in_place: true
  prepare_ahead: false
  micro_compact: false
  checkpoint_required: false
  codex_responses_native: false
```

> 以 `cli-config.yaml.example` 和当前代码为准；`codex_*`、`micro_compact`、`prepare_ahead` 等为特定 provider/部署的可选项。

## 附录 C：测试锚点

| 测试文件 | 关注点 |
|---|---|
| `tests/tools/test_memory_tool.py` | 扫描、锁、add/patch/remove、batch 原子性、drift、不可读文件、snapshot、删除门、BOM |
| `tests/tools/test_memory_patch_action.py` | patch span 语义、唯一匹配、数据丢失防护 |
| `tests/tools/test_memory_target_contract.py` | target 校验、单 store/双 store 行为 |
| `tests/tools/test_memory_tool_schema.py` | 动态 target schema |
| `tests/agent/test_memory_provider.py` | provider 注册/发现、prefetch 合并、超时/stuck、工具路由、fence |
| `tests/agent/test_pre_compress_checkpoint_contract.py` | v1/v2 checkpoint、fail-closed、fork、native/micro gate |
| `tests/agent/test_background_review*.py` | review 触发、cache parity、toolset 限制、memory scope |
| `tests/agent/test_builtin_memory_disabled_surface.py` | 只启用一个 store / 全禁用时的工具与 prompt 面 |
| `tests/agent/test_micro_compaction.py` | micro-compaction 游标、summary、DB 同步、失败跳过 |
| `tests/gateway/test_session_memory_review.py` | gateway 侧 memory review 行为 |
| `tests/hermes_cli/test_memory_setup.py` | provider setup/status 配置 |

## 附录 D：术语表

| 术语 | 含义 |
|---|---|
| active transcript | 当前 session 中 `active=1` 的消息集合；模型每轮工作的基础 |
| compacted row | 被压缩摘要取代的旧消息；`active=0, compacted=1`，仍可搜索 |
| handoff summary | 压缩生成的交接摘要；插入 active transcript 代替被折叠的中间段 |
| frozen snapshot | prompt 构建时捕获的记忆块；缓存代际内不变 |
| lean tail | 默认尾部策略：小 token 尾部 + 详细摘要 + 用户原文 + anchor index |
| APK / provider block | 外部 provider 的静态 system prompt 说明 |
| prefetch | 每轮 LLM 前由 provider 返回的动态 recall |
| pre-compress checkpoint | 压缩前要求归档型 provider 持久化证据的 fail-closed 契约 |
| egress scrub | 交给 provider 前的 secret 脱敏 |
| `api_content` | 实际发送给 API 的字节；用于精确 replay 与 cache |
| `message_uid` | 跨物理 row id 变化的稳定逻辑消息 id |
| lineage | 压缩/轮转/分支形成的 session 父子关系；用于去重和检索过滤 |
