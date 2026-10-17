# DSH 借鉴方案真实终端验证报告（runbook 验证 E / 验证 F）

> 执行日期：2026-10-17
> 执行方式：`aicli chat --headless --script`（headless 多轮脚本，同一交互槽串行提交 4 轮），隔离工作区 `output/real-test/dsh-e2e-ws/`（项目层 `.aicli/runtime.yaml` 仅开启 `agents.forkContextEnabled` / `agents.iterateGoalEnabled` + `schedule.enabled` 可观测标记），provider `workbuddy` / model `cn:glm-5.3-flash` / reasoning `max` / `--yolo`。
> 运行目录：`output/real-test/dsh-e2e-run-20261017-082152/`（transcript / render / sessions / logs 全量留存）
> 结论：**验证 E 与验证 F 全部通过**；过程中发现并修复两个接线缺陷（`f33301b2`），首次运行（`dsh-e2e-run-20261017-073232`）为修复前对照。

## 0. 首次运行暴露的两个接线缺陷（已修复）

首次运行中 fork 子级跑通但审计键全空、`iterate_goal` 被模型报告"工具清单中不存在"：

1. **spawn schema 能力位静态化**：`loop.go` / `tool_list.go` 两处 `spawnSubagentsToolDefinition(..., DefaultCapabilities())` 用静态默认值构建模型侧 schema——部署经 `agents.forkContextEnabled` 授予能力后 `context` 字段仍永远不可见（P1-1' 三层一致的第三层被短路）。修复：schema 改读调度器装配的部署能力位（`SubagentScheduler.Capabilities()` + `Agent.spawnSchemaCapabilities()`）。
2. **合成 allowlist 漏 admit `iterate_goal`**：`buildLocalChatToolPolicy` 只补 `spawn_subagents`；`5fe4ff9e`（移除 runtime-essential bypass）后 `iterate_goal` 被追加进目录、又被 `filterPolicyBlockedToolDefinitions` 以 `surface_filtered_tools: [iterate_goal]` 剔除——工具"在目录里、模型永远看不到"的静默失效。修复：合成 allowlist 仅在 `agents.iterateGoalEnabled=true` 时补入 `iterate_goal`；显式 profile/permissions 名单不扩权（守卫测试锁定）。

修复回归：隔离 worktree（HEAD `d4815ef6` + 我方 12 文件）`internal/agent` 与 `cmd/aicli/commands` 全量测试全绿；附带把两个旧 bypass 语义的红断言（`chat_setup_test.go` 显式名单测、`chat_tool_surface_parity_test.go` update_goal 测）对齐现行"allowlist 不再豁免"语义。

## 1. 验证 E：fork 上下文复述（P2-1'）— 通过

前置生效证据：
- 修复后首次请求的 `effective_tool_surface` 中 `spawn_subagents` schema 含 `context` 字段（enum `fresh|fork`）——能力授予可达模型面。
- 子级任务 `restater`（显式 `"context": "fork"`）以 background batch 派发，走 fork 快照路径。

审计键证据（`sessions/runtime/session_runtime.sqlite` `cache_requests.record_json`，子会话 `subagent_restater_d2cb4d…` 首请求）：

| 字段 | 值 | 判读 |
|---|---|---|
| `fork_prefix_preserved` | `1` | 继承快照在最终装配前缀中连续完整 |
| `fork_snapshot_messages` | `2` | 父级已完成轮次（1 条 user + 1 条 assistant），system 剥离、进行中轮次不在内 |
| `fork_snapshot_tokens` | `44` | 快照规模 |
| `fork_prefix_kept_messages` | `2` | kept == snapshot → 零缩水 |
| `usage.cache_read_tokens` | `0`（`cache_read_reported=1`） | **provider 正常上报，0 = 该请求零命中**。机制见 §3 修正说明：隐式精确前缀缓存下，fork 子级首请求是"写缓存"而非"读命中"；同批父会话与 iterate 子级首请求同样为 0、第二条请求起命中 31k–36k（数据佐证 provider 上报链路完好）。早前"本 provider 不上报"为误读，已更正 |

行为证据（transcript）：
- 子级复述："父级之前只完成了第 1 轮工作（快照里仅有"父级第 1 轮已完成"这一条记录）；它本轮收到的任务是复述继承到的上下文……"——复述内容**恰为父级已完成轮次**，未包含本次 fork 请求与验证 prompt（进行中轮次不在快照内），与"配平前缀"设计一致。
- 审计键仅出现在子级首个 LLM 请求（`forkAuditSpecForTask` 仅首尝试），重试与后续请求不带。

## 2. 验证 F：iterate_goal 多轮交接与进展门控（P2-2'）— 通过

成功路径（objective：round 1 读 `evidence.txt` 首行 → continue，round 2 ≤20 字总结 → complete，max_rounds=3）：
- transcript：`iterate_goal 最终 status=complete，rounds_started=2`。
- 每轮 fresh 子级（`subagent_iterate_1_*` / `subagent_iterate_2_*` 独立会话行），轮间经 `submit_report` 结构化 handoff 交接；无 fork 审计键（fresh 对照成立）。

stalled 变体（objective：每轮 `submit_report` status=continue、next_steps 固定 `['no-op']`，max_rounds=6）：
- transcript：`iterate_goal 最终 status=stalled，rounds_started=4`。
- 与 runbook 预测的确定性推导逐字吻合：round 2/3/4 连续 3 次 no-progress strike（next_steps 逐字节相同）→ G6 进展门控在第 4 轮收口，不烧满 6 轮预算。

fresh 对照（cache_requests）：`iterate_1..4` 各子会话所有请求 `fork_prefix_preserved` 全为 NULL（未观测）——fork 审计只覆盖 fork 子级，fresh 子级零误标。

## 3. 观察到的运行时事实（不判失败，供后续改进）

- **缓存命中数据修正（2026-10-17 复核）**：全部 26 条请求 `cache_read_reported=1`——provider 上报链路完好。父会话与 iterate 子级呈"首请求 0 → 后续命中 31k–36k"的标准隐式前缀缓存曲线；fork 子级首（且唯一）请求 `cached=0` 的根因是：其请求前缀（子级 system + 快照 + 子级工具面）与父级已缓存前缀在第一条消息即分叉（子级有意拥有独立角色与收窄工具面），对精确前缀匹配的 provider 是全新前缀 → 首请求写缓存不读命中，且单轮任务没有第二次请求命中自身前缀。
- **设计口径修正**：fork 的 KV 复用收益 = `fork_prefix_preserved`（必要条件：前缀未被装配改写）+ 子级自身会话内的后续请求命中；跨会话直接复用父级 KV 需要"子级信封（system+tools）与父级逐字节一致"，与"子级独立角色/工具面"的设计互斥，不作为验收目标。runbook 验证 E 的缓存证据口径已同步改写。
- `session_runtime.sqlite` 的 `cache_requests.prompt_fingerprint` 全部为空（`usage` 上报为空字符串时未写指纹）；shared-session 场景下如需按指纹聚合需另行补齐。
- 父会话多轮续跑出现一次 `context.tool_schema.frozen` 后的 schema 增量（`iterate_goal` 进入工具面），属 turn 边界重建的预期行为。

## 4. 产物索引

- 运行产物：`output/real-test/dsh-e2e-run-20261017-082152/`（transcript.txt / render.log / sessions/runtime/*.sqlite / logs/**/http/*_request_provider_wrapper.json）
- 修复前对照：`output/real-test/dsh-e2e-run-20261017-073232/`（模型明确报告 iterate_goal 不存在 + fork 审计键全空的现场）
- 夹具：`output/real-test/dsh-e2e-ws/`（rounds.txt / .aicli/runtime.yaml / evidence.txt）、`output/real-test/run-dsh-e2e.ps1`
- 修复提交：`f33301b2`；runbook 场景定义：`docs/plan/multi-agent-real-terminal-validation-runbook.md` §7/§8
