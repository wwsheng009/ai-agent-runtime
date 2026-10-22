# Codex Memory 机制分析与可借鉴边界

> 状态：分析文档  
> 参考：`E:\projects\ai\codex`，`main@cc7ba33601`  
> 目的：提炼 Codex memory 的架构与安全边界，为 AAR Agent Memory v2 提供可复用输入。

## 1. Codex memory 是什么

Codex 的 memory 不是模型参数更新，也不是向量数据库，而是一套“文件 + SQLite + LLM 抽取/整合”的跨会话长期记忆系统：

```text
历史 rollout
  → Phase 1 单会话抽取
  → SQLite stage1 outputs
  → Phase 2 全局去重/整合
  → memory_summary.md / MEMORY.md / summaries / skills
  → 新会话 developer context 注入
  → 按需检索
  → 引用解析与 usage 回写
```

它把“记忆生成”和“记忆读取”拆开，主对话路径不承担提取成本。

## 2. Codex 的关键模块

| 层次 | Codex 模块 | 作用 |
| --- | --- | --- |
| 读取 | `codex-rs/memories/read` | 引用解析、读取使用量统计、记忆根路径 |
| 写入 | `codex-rs/memories/write` | Phase 1/2、Prompt、artifact 同步、Git baseline、workspace diff |
| 运行时扩展 | `codex-rs/ext/memories` | 读取摘要注入、专用 `memories.*` 工具 |
| 状态 | `codex-rs/state` | `stage1_outputs`、jobs、lease、watermark、usage |
| 触发 | `app-server` | 根会话启动后异步启动记忆任务 |
| UI | TUI | Use memories / Generate memories / Reset |
| 配置 | `features` + config | feature flag、版本、dual write、抽取/整合模型、保留策略 |

## 3. Codex 两阶段管线

### 3.1 Phase 1：会话抽取

输入：最近且满足条件的 root session rollout。

筛选条件包括：

- 来源是允许的交互式会话；
- 会话开启 memory generation；
- 不处于活跃/新鲜状态；
- 年龄在窗口内；
- 未被其他任务租约占用；
- 每次启动只处理有限数量。

处理流程：

1. 从 SQLite 领取任务，写入 lease/worker；
2. 过滤 rollout 中的 memory-relevant response items；
3. 使用低 reasoning 模型抽取：
   - `raw_memory`
   - `rollout_summary`
   - 可选 slug；
4. 对结果脱敏；
5. 写入 `stage1_outputs`；
6. 失败任务记录错误并按退避重试。

并发限制、租约和 watermark 避免多个进程重复抽取。

### 3.2 Phase 2：全局 Consolidation

输入：按使用频率、最近使用、生成时间排序后的 stage-1 记忆。

处理流程：

1. 获取全局 Consolidation 锁；
2. 选择有限数量、未过期的记忆；
3. 同步记忆 artifacts：
   - `raw_memories.md`
   - `rollout_summaries/`
   - `MEMORY.md`
   - `memory_summary.md`
   - `skills/`
4. 生成 Git-style workspace diff；
5. 若工作区有变化，启动受限的内部 consolidation agent；
6. 完成后重置 Git baseline，并记录完成 watermark；
7. 若工作区无变化则直接成功退出。

### 3.3 读取注入

新会话读取 `memory_summary.md`，转成 developer instructions：

- 摘要限制在约 2,500 tokens；
- 提示 Agent 优先查 `MEMORY.md`；
- 只在需要时读取 `rollout_summaries/` 和 skills；
- 引用使用记忆时追加 citation block；
- 用户明确要求记住/忘记时，写 ad-hoc note，不直接修改生成文件。

### 3.4 反馈闭环

引用解析器从回答末尾提取：

- 文件路径和行号；
- 引用说明；
- rollout/thread ID。

随后更新对应记忆的 `usage_count` 与 `last_usage`，影响后续 Consolidation 选择。

## 4. Codex 的可借鉴设计

### 4.1 明确分离读写

写入是异步后台任务；读取是本地文件查找和 prompt 注入。主模型请求不因抽取任务阻塞。

### 4.2 分层记忆而不是单一“上下文”

- summary：每轮少量注入；
- index：可搜索 registry；
- evidence：按需读取的会话摘要/技能；
- usage：反馈排序。

这种渐进式披露避免一次注入过多历史。

### 4.3 任务状态与租约

Phase 1/Phase 2 都有 job 状态、worker、lease、retry、watermark。对多进程、多入口（CLI、runtime-server、Web）尤其重要。

### 4.4 写入工作区可审计

记忆 artifacts 放在专用根目录，并有 Git baseline/diff；用户能看到本轮 Consolidation 改了什么。

### 4.5 记忆安全规则

- 记忆内容不是指令；
- 不允许记忆直接授予权限；
- 明确“用户要求记住/忘记”才修改；
- 引用帮助追踪哪些记忆真正被使用；
- 记录 secrets 脱敏；
- 内部 Consolidation agent 网络和工具受限。

## 5. 不应照搬的部分

| Codex 设计 | AAR 的差异 | 结论 |
| --- | --- | --- |
| `~/.codex/memories` 全局文件根 | AAR 默认是项目工作区隔离，且 `.aicli/` 已 gitignore | 默认项目级 SQLite，Markdown 只做可读导出 |
| 文件是记忆事实源 | AAR 已有 SQLite、artifact、factledger、knowledge | SQLite 作为事实源，Markdown 作为编译产物 |
| 单 agent 的 MEMORY.md 编辑 | AAR 有工具权限、MCP、多 agent、前端 | 统一走 memory API/tool，不让模型直接编辑任意记忆文件 |
| codex 内部 item 形状 | AAR 使用 `types.Message` 和 canonical transcript | 使用 AAR 的消息/事件模型 |
| 只在根 session startup 触发 | AAR 有 CLI、server、ACP、subagent、idle session | 支持 session-end、idle、定时任务多触发方式 |
| 全局单一 scope | AAR 有多 workspace、profile、项目权限 | 显式 project/user/profile scope 和隔离键 |
| 依赖 Git baseline 判断变化 | AAR 项目 `.aicli/` 默认不入 Git | 使用 revision + source watermark + content hash，不依赖 Git |

## 6. 对 AAR 的映射

| Codex | AAR 对应落点 |
| --- | --- |
| `codex-rs/memories/read` | 扩展现有 `internal/memorystore` 的查询/注入层 |
| `codex-rs/memories/write` | 新增 `internal/memoryruntime` 下的 extract/consolidate/jobs |
| `codex-rs/ext/memories` | 扩展 `contextmgr` 注入与工具注册，不另建 Agent extension 系统 |
| `stage1_outputs` | 新 SQLite `memory_entries` + `memory_sources` |
| `jobs` | 新 `memory_jobs`，复用 `sqliteutil` 的单写者/WAL 基线 |
| `memory_summary.md` | `<workspace>/.aicli/memory/memory_summary.md`（materialized） |
| `MEMORY.md` | `<workspace>/.aicli/memory/MEMORY.md`（materialized） |
| citation → usage | `memory_usage` + answer citation parser |
| `MemoryTool` feature flag | `memory.mode=off/shadow/on` + 子开关 |
| TUI memories settings | 现有 `/memory` 命令 + 前端 Harness Memory 面板扩展 |

## 7. 结论

Codex 的实现证明了几件事：

1. 跨会话记忆最适合两阶段异步管线；
2. 记忆需要“摘要 + 可检索证据”的渐进式披露；
3. 必须把记忆内容当作不可信数据；
4. 记忆写入需要任务、租约、重试和审计，不应只是追加文本；
5. 只有引用/使用反馈，才能让记忆排序长期有效。

AAR 应借鉴这些结构与边界，但用本项目已有的 SQLite、canonical transcript、fact ledger、contextmgr 和 Web/TUI 控制面落地。
