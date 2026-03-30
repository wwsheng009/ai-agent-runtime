# Team4 设计文档实现状态分析报告

> 基于 `docs/multi-agents/teams/design/team4.md`，按当前源码重新核对 Team4 的控制流闭环、四个关键骨架文件，以及仍未完全收口的实现缺口。

**分析日期**: 2026-03-15  
**更新说明**: 本文档已按当前源码状态修订，替换了先前已经过期的缺口判断。  
**设计文档**: `docs/multi-agents/teams/design/team4.md`

---

## 总体结论

Team4 设计强调的主控制链已经落地，并且比早期分析结论更完整：

`SessionActor -> Agent Loop -> Permission/Hook -> Checkpoint -> Team Orchestrator`

当前实现可以概括为：

- `repo.go` 最终落成为 `store.go + sqlite_store.go`
- `checkpoint.Manager` 已支持 `code / conversation / both`
- `actor.go` 已支持 ask/approval/rewind/run meta，且挂起状态可持久化恢复
- team task claim 与 path claim 已改为原子事务路径

**综合完成度：90%+**

当前最主要的残余问题不再是“骨架缺失”，而是：

1. shell 类工具的 checkpoint 边界仍然带有启发式与预算限制  
2. 恢复语义还缺少更强的“恰好一次”保障与 crash-recovery 端到端测试  
3. 仓库里仍有一个与 Team4 无直接关系的 `internal/runtime/background` 构建问题，阻碍更大范围联跑

---

## 1. `internal/team/repo.go`

**设计目标**

- 统一 Team repo 接口
- 支持 claim / lease / mailbox / path claim
- 提供足够强的一致性语义

**当前实现**

- 最终形态是 `internal/team/store.go` + `internal/team/sqlite_store.go`
- Team / Task / Dependency / Mailbox / PathClaim / Event 均已落地
- `BlockTask` 已存在：`internal/team/store.go`, `internal/team/sqlite_store.go`
- mailbox receipts 已落地，不再只是 `acked_at` 单表：`internal/team/sqlite_store.go`
- `ClaimTaskWithPathClaims` 已存在，并将 task claim + path claim + teammate state 放入同一事务：`internal/team/store.go`, `internal/team/sqlite_store.go`
- SQLite DSN 已统一注入 `_txlock=immediate`，用于加强写事务竞争行为：`internal/team/sqlite_store.go`

**与旧分析相比的修正**

- `WithImmediateTx` 已存在
- `BlockTask` 已存在
- mailbox 已经不是单纯 `acked_at` 模型，已有 receipts 表
- claim task 与 path claim 已不再是单纯的“两步补偿式” happy path

**剩余问题**

- 当前原子 claim 主要覆盖 orchestrator 主路径；其它 task 生命周期操作仍有进一步统一事务化的空间
- path claim 冲突检查虽然已进事务，但仍依赖路径层级重叠启发式，而非更细粒度的内容级冲突检测

## 2. `internal/team/orchestrator.go`

**当前实现**

- orchestrator 主结构存在：`internal/team/orchestrator.go`
- ready task claim 存在
- lease reclaim 存在
- complete / fail / block 分支均存在
- runner 与 lead planner 已接通：`internal/team/teammate_runner.go`, `internal/team/lead_planner.go`
- mailbox 读写与 task context 已有 broker tool 支撑，已不再只是“服务层能力”

**与设计稿的符合度**

- task 分发、回收、阻塞、重规划、汇总主路径都已落地
- `BlockTask` 已是一等状态机分支，不再缺失
- writer 限制、path claim、mailbox digest 已接入 dispatch / teammate runner 主流程

**剩余问题**

- dispatch 策略仍偏实用主义，尚未完全收敛成设计稿里更细的 profile/capability 打分模型
- replan 逻辑已经存在，但仍偏“最小可用”，不是复杂 DAG 场景下的完整重规划器

## 3. `internal/runtime/checkpoint/manager.go`

**当前实现**

- tool mutation 前后自动捕获已存在
- restore / preview 已支持 `code`
- `conversation` / `both` 已支持
- code rewind 已从“恢复目标 checkpoint 自身”升级为“撤销目标点之后的 checkpoints”
- actor 已会根据 `ConversationHead` 应用 conversation rewind

**与旧分析相比的修正**

- `conversation` 不再是未实现
- `both` 不再是未实现
- rewind 已不再只是 “code restore + actor 侧 head-offset 补偿” 的半成品

**剩余问题**

- shell 类工具虽然已支持 `mutated_paths / changed_paths / patch / diff`，也新增了基于 `cwd/workdir` 的受限目录快照 fallback，但仍不是完整工作区级变更捕获
- 当 shell 修改范围过大、目录超出预算、或工具没有返回足够元数据时，checkpoint 仍可能退化为部分捕获

## 4. `internal/runtime/chat/actor.go`

**当前实现**

- actor command 模型、pause/resume、审批、问题、rewind、event subscribe 已存在
- `RunMeta` 已从 submit 传到 tool context
- `CurrentRunMeta` 已进入 SQLite runtime state
- `PendingApproval` / `PendingQuestion` / `PendingTool` 已持久化
- `ask_user_question` 在 actor 重启后可以继续
- approval 在 actor 重启后也可以基于持久化 `PendingTool` 恢复执行
- `ContinueWithSession` 已接入，恢复时无需再伪造一轮新的 user prompt

**与旧分析相比的修正**

- `CurrentRunMeta` 已经进入持久层，不再是缺口
- 挂起恢复不再只是“状态落库”，而是真正具备继续执行能力

**剩余问题**

- 当前恢复是“持久化 pending tool + 继续 loop”，但还没有更强的“恰好一次”副作用保障
- 如果进程在外部副作用已经发生、但 tool result 尚未写回 transcript 时再次崩溃，仍可能需要额外幂等保护

---

## 关键偏差

与 Team4 设计稿相比，现在最需要关注的已经不是四个骨架文件“有没有”，而是以下三点实现质量问题：

1. shell mutation 的 checkpoint 边界仍不够强，尚未达到完整工作区级 rewind 语义  
2. 挂起恢复链已打通，但副作用恢复仍缺更强的幂等与 crash-safety 保证  
3. 更大范围仓库验证仍受 `internal/runtime/background` 的独立构建问题影响

---

## 下一步建议

1. 继续增强 shell 工具的变更发现能力，尽量减少对 `mutated_paths` hint 的依赖  
2. 为 approval / question / resumed tool execution 增补 crash-recovery 端到端测试  
3. 修复 `internal/runtime/background` 的现存构建问题，恢复更大范围联跑能力

---

*报告更新时间: 2026-03-15*
