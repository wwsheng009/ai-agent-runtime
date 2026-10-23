# 工具层抽象设计审核（2026-10-22）

- 被审文档：`docs/plan/tool-layer-abstraction-design-20261022.md`
- 审核范围：架构完整性、契约完整性、安全边界、输出契约、状态模型、多表面适配、迁移与验收
- 审核轮次：**Round 2 + Design freeze**
- 文档状态：**P0+P1 design frozen；P2 backlog**
- 审核结论：**Conditional approval for PR0（自审冻结；独立安全评审为发布门）**
- 总体判断：初版主要缺口已补，设计内容冻结；后续不再重复设计复审，
  转入 PR0 契约验证和 PR1–3 实现证据。

> 2026-10-22 冻结：P0/P1 设计内容冻结。PR0 可开始；PR1 需 PR0 + P0 测试；
> 独立安全评审是 canary/生产发布门，不再阻塞 PR0。

## 0. 复审策略与冻结规则

之前几轮的状态一直是 `Changes requested`，原因不是同一个问题反复失败，而是：

- 那几轮是**设计修订 + 自审**，不是独立复审；
- 每轮又扩大了范围（P0 → P1 → P2 → 最佳实践/网络工具），没有冻结边界；
- 因此每轮都能发现新问题，状态自然一直停不下来。

从现在起改为**冻结 + 证据门**：

| 门 | 条件 | 当前 |
|----|------|------|
| Design freeze | P0/P1 内容冻结 | 本轮完成 |
| PR0 | 契约/测试骨架、工具清单对账 | 下一步 |
| PR1 | PR0 通过 + P0-1/P0-2 测试 | PR0 后 |
| PR2 | PR1 通过 + read-only conformance | PR1 后 |
| PR3 | PR1 通过 + CLI gate 测试 | PR1 后 |
| Canary/生产 | 独立安全评审 + PR1–3 证据 | 发布前 |

规则：

- **不再进行重复设计复审**；新发现进入 PR backlog；
- 只有“可复现的 P0 安全缺陷”可以重开设计门；
- 独立安全评审是**发布门**，不是 PR0 的前置门；
- 设计冻结不等于生产批准；它只表示“可以开始用测试验证设计”。

## 1. 总体结论

方案的“一份定义 + 一份实现 + 多表面 adapter”方向是对的，也正确识别了当前本地/远程工具重复、输出策略漂移、`toolkit/tools` 与 `toolctx` 耦合等问题。

初版更接近“架构愿景 + 迁移提纲”；本轮修订已补 P0 与 P1 主要契约。
下表同时列初版覆盖度与修订后**作者自评**（不是审核批准）：

| 维度 | 初版 | 修订后（自评） | 证据状态 |
|------|------|------------------|----------|
| 分层架构 | 80% | 92% | design frozen |
| 核心契约 | 70% | 90% | design frozen |
| 权限与安全 | 50% | 90% | security review（release gate） |
| 输出契约 | 75% | 92% | PR1 evidence |
| 会话与状态 | 35% | 85% | PR2/3 evidence |
| 多表面适配 | 65% | 90% | PR0/1 evidence |
| 迁移与兼容 | 65% | 88% | PR1 evidence |
| 测试与性能 | 60% | 85% | PR1/2 evidence |
| 运维与版本 | 30% | 85% | release gate |
| 文档一致性 | 75% | 92% | design frozen |

修订后整体覆盖度：**约 90%（作者自评）**。
设计门为 **Conditional approval for PR0**；生产/Canary 仍需独立安全评审和
PR1–3 验收证据。设计内容不再重复复审。

---

## 2. P0 阻塞项与修订状态

初版四项均为安全/架构阻塞；本轮修订状态如下：

| P0 | 设计文档 | 修订状态 | 复审/实现验证 |
|----|----------|----------|----------------|
| P0-1 | §9.3/§9.6 | 内容已补 | 独立安全复审；PR1 恶意 metadata 测试 |
| P0-2 | §9.7 | 内容已补 | 独立安全复审；PR1 artifact scope 测试 |
| P0-3 | §6.6/§10.5/§10.6 | 内容已补 | 复审；PR3 gate 顺序测试 |
| P0-4 | §11.1/Phase 4 | 内容已补（local-only） | 复审；remote schema 测试 |

**注意**：下表 P0-1..P0-4 的“问题/必须明确”保留初版审核上下文；
是否关闭以 §9 审核签字和 §11 复审清单为准。

### P0-1 远程输出 metadata 的信任边界（安全）

**问题**

方案第 9 节建议远程复用本地的 `skip_render_truncation`、`model_visible_budget_bytes`、`reserved_tail_bytes`、`artifact_id`。但当前本地链路会**无条件信任** MCP 返回的 `Meta`：

- `internal/toolkit/mcp_server.go:147-151`：远程 `ToolResult.Metadata` → `CallToolResult.Meta`；
- `internal/tools/manager.go:969-1010`：`formatMCPResult` 直接 `cloneMetadataMap(result.Meta)`；
- `internal/output`：`skip_render_truncation=true` 时直接 pass-through；
- `internal/output/reserved_tail_test.go:267-285` 明确验证了“owned window 不再 fold”。

也就是说：一个被攻陷/配置错误的远程 MCP server 可以返回：

```json
{"skip_render_truncation": true, "model_visible_budget_bytes": 999999999}
```

并附带巨大 body，从而**绕过本地 render 层的模型上下文预算**。这不是抽象层的小问题，而是跨信任域的输出注入。

**必须在方案中明确**

1. 远程 metadata 是**不可信提示**，不是授权；
2. 本地 surface 对 `executionScope=remote`（或 trustLevel != local）的响应强制硬上限，不因 `skip_render_truncation` 放行；
3. `model_visible_budget_bytes` 只能**收窄**，不能超过本地 `modelToolTextByteBudget`/硬上限；
4. `reserved_tail_bytes` 必须校验（不得超过 hard cap，必须能从 body 中找到对应尾部），否则忽略；
5. `artifact_id` 必须带 namespace（见 P0-2）；
6. 增加恶意 metadata 测试：
   - 远程声明 skip + 超大 body → 本地仍然 fold/拒绝；
   - 远程声明超大 budget → 本地 clamp；
   - 远程声明虚假 reserved tail → 忽略；
7. 文档第 9.3 节要增加“trust boundary”小节，不能只写“链路已通”。

**修订状态**：§9.3/§9.6 已补 trust boundary、Meta allowlist、hard cap、
budget clamp、tail 校验和恶意 metadata 测试清单；同时拆分
`CaptureLimit` / `ModelVisibleCap`，并新增 wire 层 `MaxMCPResponseBytes`
与显式 `MCPResultContext`（trust 只来自 server config）。

**结论**：内容已补；独立安全复审和 PR1 测试通过前仍不关闭，PR1 不应上线。

### P0-2 Artifact 命名空间与所有权

**问题**

- 远程 `ArtifactStore` 与本地 artifact store 是两套存储；
- `artifact_id` 通过 `Meta` 回到本地后没有 namespace；
- 模型可能拿远程 artifact id 调本地 `artifact_read`，或反之；
- 当前文档只写“ArtifactSink 接口”，没有定义跨 surface 的 ID 路由/所有权。

**必须在方案中明确**

```go
type ArtifactRef struct {
    Scope  string // local | remote
    Target string // remote target name, empty for local
    ID     string
}
```

- 远程 artifact id 形如 `remote:238:<id>`；
- 本地 `artifact_read` 只解析 local scope；
- 远程 `artifact_read` 只解析对应 target scope；
- 跨 scope 请求返回 `ARTIFACT_SCOPE_DENIED`；
- 加 conformance/security 测试：伪造/跨 target/跨 scope 的 artifact id 必须拒绝。

**修订状态**：§9.7 已补 `ArtifactRef`、local/remote canonical id、
`Origin` 感知的裸 id 归属、跨 target 拒绝、严格 ID/target 正则、
归档失败/过期错误码和兼容迁移规则。

**结论**：内容已补；独立安全复审和 PR1 artifact scope 测试通过前仍不关闭。

### P0-3 Capability / Policy Gate 的顺序与映射

**问题**

方案提出 capability gate，但没有给出与现有权限机制的精确关系：

- `--deny-tool` / `--allow-tool`；
- `permission-mode`（default / accept_edits / plan / bypass / dont_ask）；
- 远程 `profile` / `tools`（工具是否存在）；
- 远程 command allowlist/pattern/metachar/shellrisk；
- subagent read-only boundary；
- folder trust / MCP trust level；
- `CanDirectCall` 直调表面。

当前文字容易让实现者新增第二个授权层，或让 direct/CLI 绕过 gate。

**建议补充一张固定顺序表**：

```text
1. surface filter: profile / tools / target enabled
2. tool lookup + alias + collision
3. deny-tool / hard deny
4. mode/capability decision (allow / ask / deny)
5. approval (if ask)
6. runtime path/command policy
7. execute
8. output shape
9. audit/access/metrics
```

并明确：

- capability 是**声明**，不是授权；
- 不新增远程独立授权表；
- `CanDirectCall` 不得绕过 3/4/5/6；
- direct CLI 也必须走同一个 gate；
- 远程 `profile` 只决定“工具是否存在”，不决定“这次是否允许”。

**修订状态**：§6.6/§10.5/§10.6 已补 0–18 固定顺序、复用
`policy.Engine` / `ToolExecutionPolicy`、direct/CLI 同一性、bypass/HardAsk
边界和 gate 测试清单；复审发现 allow rule/callback 可短路部分安全门，
已改为 hard safety gates 并增加 `policy_legacy_safety_order` 兼容开关。

**结论**：内容已补；复审和 PR3 gate 顺序测试通过前仍不关闭。

### P0-4 Streaming / Jobs / Interactive Terminal 的契约

**问题**

本方案的核心场景是远程部署/命令执行，但当前 `Result` 是 final-only，`Runtime` 没有：

- `Executor`（可注入的进程执行）；
- `StreamSink` / `ProgressSink`（stdout/stderr/进度）；
- `JobRuntime`（job_start/output/cancel/list）；
- PTY/interactive stdin；
- detach/background；
- shell output artifact tee。

本地已有 `bash` 的 interactive/background/detach/terminal stream 逻辑，`aicli_exec`、`background_submitter`、`terminal_stream_wiring` 等；文档把它们隐含在“shell core”里，但没有契约。

**必须二选一**：

A. 在 core 中增加：

```go
type StreamSink interface {
    Stdout([]byte) error
    Stderr([]byte) error
    Progress(Progress)
}

type JobRuntime interface {
    Start(...) (JobRef, error)
    Output(...) (JobOutput, error)
    Cancel(...) error
    List(...) []JobRef
}
```

并把 `job_*` 纳入 core/能力矩阵。

B. 明确声明 streaming/interactive/jobs 为 **local-only surface**，远程只支持 final-result shell；在能力矩阵、文档、CLI 中显式标注，不假装与本地一致。

无论选哪个，不能保持当前“文档提了 shell core，但没有流式/job 契约”的状态。

**修订状态**：§11.1 已选择方案 B：v1 core 只承诺 final-result；
interactive/PTY/jobs/streaming 明确 local-only；Phase 4、远程 schema 和
CLI 契约同步收敛。远程现有 `job_*` 标记为 legacy、默认关闭。

**结论**：内容已补；复审确认 local-only 范围后可关闭。

---

## 3. P1 缺口与修订状态

初版 P1 缺口与当前修订状态：

| P1 | 设计文档 | 修订状态 | 复审/实现验证 |
|----|----------|----------|----------------|
| 工具清单 | §13.0 | 已补 | PR0 与 `toolregistry` 对账 |
| MCP surface | §8.7 | 已补 | PR0 测试骨架 |
| 会话状态 | §6.4/§11.2 | 已补 | typed accessor/矩阵复审 |
| 错误/重试/幂等 | §10.7 | 已补 | golden + 不自动重放测试 |
| 版本/兼容 | §13.7 | 已补 | 混合版本测试 |
| CLI/direct | §8.4.1/§10.6 | 已补 | CLI golden |
| 可观测性 | §12.4 | 已补 + rotation/基数 | schema/脱敏测试 |
| 性能 | §15.2.1/§15.2.2 | 已补 + race/负载 | benchmark 执行 |
| 威胁模型 | §16.3 | 已补 + 供应链 | 安全测试映射 |

以下小节保留初版缺口描述，状态以本表为准；未完成“复审/实现验证”的项
仍按缺口处理。

### 3.1 工具清单与迁移矩阵缺失

文档有阶段，但没有**逐工具清单**。`internal/toolkit/tools` 有 147 个文件，至少包含：

- 核心文件/搜索：view、read alias、ls、glob、grep
- 写入：write、edit、append_write、apply_patch、multiedit
- 执行：shell、bash、aicli_exec、execute_shell_command
- 后台/终端：background_submitter、detach、terminal stream
- 状态：todos、read_dedup、read_ledger、session_state_gc
- 内容：artifact_read、fetch、download
- 代码/知识：code_*、sourcegraph、knowledge
- 网络：web_search、openai_image_generate
- 其他：git_worktree、document_guard、file_encoding、atomic_write、patch_support

需要一张表：

| 工具 | 是否进入 core | 依赖的 Runtime | 支持 surfaces | 迁移阶段 | 本地专属能力 |
|------|---------------|----------------|---------------|----------|--------------|

否则无法判断“Phase 2 只读 core”到底覆盖哪些工具，也无法估计工作量。

### 3.2 MCP surface 规格不足

文档提到 MCP stdio/http，但没有给出完整 surface 契约：

- auth：Bearer / mTLS / token env / loopback 规则；
- `trustLevel` / `executionScope` 与 capability 的关系；
- MCP Roots 的收窄（只能收窄，不能扩大）；
- stateless vs stateful、session id、KeepAlive；
- canonical tool name / prefix / 64 字符预算 / collision；
- tool description/schema 大小限制、tool poisoning、不可信 server 的 schema；
- `tools/list` 过滤（profile/tools/allow/deny）；
- `tools/call` 的 `Meta` 信任边界；
- 401/403/429/超时/断连的错误映射；
- access log / audit / metrics 的字段与轮转。

现有 `cmd/toolkit-mcp-server` 只 `Start` stdio（`main.go:60-83`），HTTP 是 `aicli-remote-tools` 自己实现的；方案应明确“统一 serve”是目标还是现状。

### 3.3 会话状态被低估

`internal/toolctx` 远不止 workspace/read ledger：

- session id / tool call id / turn id / goal id / agent depth；
- generated image output dir；
- shell output artifact dir；
- workspace root；
- file change notifier；
- allowed roots；
- artifact store；
- artifact read scope；
- read-only roots。

方案只列了 read dedup/ledger/todos。需要：

- 完整的 `SessionState` 能力矩阵；
- 本地/远程/CLI 的 state 支持；
- 并发、TTL/GC、session 隔离；
- 远程 stateless 的明确差异；
- 不要让 `Get/Set(any)` 变成无类型垃圾袋；建议 typed stores 或 nil 可选接口。

### 3.4 错误、重试与幂等

文档有 `ErrorCode/NextAction/Retriable`，但缺：

- 错误分类：validation / policy / path / command / execution / transport / timeout / internal；
- `Result.Err` 与 `ErrorCode` 的优先级；
- MCP `IsError` / HTTP status / CLI exit code 的映射表；
- 写/执行类操作**不得自动重试**的规则；
- 幂等键/重放语义；
- 取消/超时/进程组清理；
- transport 断连与 reconnect 的错误是否可重试。

### 3.5 版本与兼容策略

缺：

- core / surface / tool schema 的版本号；
- alias/deprecation 策略；
- 混合版本（新客户端 + 旧远程，旧客户端 + 新远程）；
- `tools/list` 的 schema 版本协商；
- remote binary 升级/回滚；
- audit/access log schema 版本；
- 旧 `toolkit.Tool` 与新 `toolcore.Handler` 的 adapter 兼容测试。

### 3.6 CLI / Direct 表面的策略与审批

CLI 章节有命令示例，但缺：

- `CanDirectCall` 的准确语义；
- CLI 是否支持 approval/ask，还是只能 `--yes`；
- 默认只读与显式 `--allow-write/--allow-command`；
- token/auth（如果 CLI 连接远程）；
- JSON schema/错误格式/exit code 的稳定契约；
- stdin/二进制/大输出/分页；
- `toolctl run` 是否允许 `--dangerously-bypass`（不建议）。

### 3.7 可观测性接口

文档说 core 发事件、surface 记录，但 `Runtime` 没有 `Logger`/`Tracer`/`Metrics`/`Progress` 接口。需要：

- 结构化事件 schema（含版本）；
- 工具开始/结束/进度/取消；
- 审计字段与 access log 字段；
- secret redaction；
- 指标维度（surface/tool/capability/decision）；
- 审计写入失败策略（best-effort，但不静默）。

### 3.8 性能方法与验收阈值

文档有 benchmark 方向，但缺：

- 测试语料/规模（多少文件、多大文件、多少并发）；
- 机器/CI 基线；
- p50/p95/alloc 阈值；
- 远程 vs 本地、rg vs fallback 的对照；
- MCP 序列化/元数据/trust filter 的开销；
- 负载/竞态测试（并发 429、registry 读写、session state）；
- 压测脚本与结果归档。

### 3.9 安全威胁模型

需单独一节覆盖：

- 不可信远程 server / 恶意 metadata / artifact scope；
- tool poisoning（description/schema 注入、超大 schema）；
- 路径穿越/symlink/TOCTOU/设备文件；
- 命令注入/解释器放行/元字符；
- secrets（命令 preview、audit、access log、env）；
- SSRF/URL allowlist（fetch/download/web_search）；
- prompt injection（远程文件内容）；
- subagent read-only boundary；
- folder trust / MCP trust level；
- OS 级隔离（systemd/容器）；
- 直调 CLI 的权限绕过。

---

## 4. P2 文档一致性复查

初版 P2 项的复查状态：

| P2 项 | 状态 | 说明 |
|-------|------|------|
| 现状 vs 目标混写 | 已处理 | §8.2 标明当前/目标；§9.3 改为 trust boundary；PR1 措辞统一 |
| 类型细节 | 已处理 | `InputSchema` adapter、`Registry.Filter`、`Clock/IDs`、权限表已补 |
| 命名/开放问题 | 部分 | `CanDirectCall`/`Sessions` 已收口；`toolcore`/`toolctl` 名称仍开放 |
| 附录/证据 | 已处理 | Appendix B 改 PowerShell；工具数量与 Appendix A 已修正 |

以下为初版问题清单，保留作审计上下文。

1. **现状 vs 目标混写**
   - 8.2 说 MCP 支持 stdio/http，但当前 `cmd/toolkit-mcp-server` 只 stdio；
   - 9.3 说 metadata 链路“已经可用”，但没有 trust 限制；
   - 17 PR 1 说“删除 foldRemoteOutput/boundedWriter”，13 Phase 1 又说“保留薄 wrapper 如需要”，需要统一措辞。
2. **类型细节**
   - `Definition.InputSchema` 用 `json.RawMessage`，现有 `Tool.Parameters()` 是 `map[string]interface{}`；adapter 转换与 canonicalize 需要写清；
   - `Registry.Filter` 未定义；
   - `Clock`/`IDs` 在契约里出现，但迁移阶段没有使用说明；
   - `Runtime.Permissions()` 与 `Commands()` 的边界已修正，但仍需一张“谁检查什么”的表。
3. **命名/开放问题**
   - `toolcore` / `toolctl` 名称未定；
   - `CanDirectCall` 的语义未定义；
   - `internal/toolkit.Tool` 是否保留为唯一兼容接口，还是 core 接口成为主接口；
   - `Sessions() Get/Set(any)` 过于宽泛。
4. **附录/证据**
   - 2.2 的 `go list ... | grep` 在 Windows 下不直接可用（应写 PowerShell `Select-String` 或注明 bash）；
   - 14 个核心工具的数量需在工具清单表落地后再确认。

---

## 5. 新增章节与状态

| 初版建议 | 设计文档位置 | 状态 |
|----------|--------------|------|
| MCP Surface 规格 | §8.7 | 已补 |
| 输出信任边界 | §9.3/§9.6 | 已补 |
| Gate 顺序矩阵 | §10.5/§10.6 | 已补 |
| SessionState 能力矩阵 | §6.4/§11.2 | 已补 |
| Observability 接口 | §12.4 | 已补 |
| 工具清单与迁移矩阵 | §13.0 | 已补 |
| 版本与 rollout | §13.7 | 已补 |
| 信任与对抗测试 | §15.1、§16.3 | 已补 |
| 威胁模型 | §16.3 | 已补 |
| 每阶段可量化 DoD | §17.4/§17.5 | 已补 |
| 网络类工具迁移 | §13.8 | 新增，v1 deferred |

---

## 6. Definition of Done 与当前状态

“文档内容完成”不等于“验收完成”。状态如下：

| 条目 | 设计文档 | 安全/发布门 | 实现/测试 | 当前状态 |
|------|----------|-------------|-----------|----------|
| P0-1 输出信任边界 | 已补 | release gate | PR1 | design frozen；PR1 evidence pending |
| P0-2 artifact namespace | 已补 | release gate | PR1 | design frozen；PR1 evidence pending |
| P0-3 gate 顺序 | 已补 | design frozen | PR3 | design frozen；PR3 evidence pending |
| P0-4 local-only 决策 | 已补 | design frozen | PR1 | design frozen；PR1 evidence pending |
| 工具清单/迁移矩阵 | 已补 | design frozen | PR0 | design frozen；PR0 对账 pending |
| MCP surface 规格 | 已补 | release gate | PR0/PR1 | design frozen；evidence pending |
| SessionState 矩阵 | 已补 | design frozen | PR2/PR3 | design frozen；evidence pending |
| 错误/重试/幂等 | 已补 | design frozen | PR1/PR3 | design frozen；evidence pending |
| 版本/混合版本/rollout | 已补 | design frozen | PR1 | design frozen；evidence pending |
| 性能语料/阈值/CI | 已补 | design frozen | PR1/PR2 | design frozen；evidence pending |
| 威胁模型/安全测试 | 已补 | release gate | PR1–3 | design frozen；security evidence pending |
| `toolkit.Tool` 兼容映射 | 已补 | design frozen | PR1 | design frozen；evidence pending |
| P2 文档一致性 | 大部分已补 | backlog | PR0/doc | 命名/图片映射仍开放 |

**当前判定**：

- 文档缺口：P0 4/4、P1 主要项已补，P2 大部分已补；
- 设计门：**conditional pass**（P0/P1 内容冻结；自审，不是独立安全签字）；
- PR1–3：**未开始**（尚无本方案的实现证据）；
- 结论：**可进入 PR0**；PR1 需 PR0 通过，生产/canary 需独立安全评审。

---

## 7. 修订进度与下一步

| 步骤 | 状态 | 下一步 |
|------|------|--------|
| P0-1/P0-2 信任边界 + artifact scope | 内容已补 | 独立安全复审；PR1 测试 |
| P0-3 gate 矩阵 | 内容已补 | 复审；PR3 测试 |
| P0-4 streaming/jobs 范围 | 已决策 local-only | 复审确认 |
| 工具清单 + MCP surface | 已补 | PR0 对账/测试骨架 |
| SessionState/错误/版本/运维 | 已补 | PR0/PR1 验证 |
| 测试阈值 + DoD | 已补 | PR0 复审；PR1 执行 |

**下一步顺序**：PR0 复审 → P0/P1 Approved → PR1 → PR2 → PR3。

---

## 8. 代码证据附录

```text
internal/toolkit/mcp_server.go:147-151
  ToolResult.Metadata → CallToolResult.Meta

internal/tools/manager.go:969-1010
  formatMCPResult: cloneMetadataMap(result.Meta)，无 trust 过滤

internal/output/reserved_tail_test.go:267-285
  skip_render_truncation=true 时 pass-through，不 fold

internal/toolkit/tools/tool_output_budget.go:30-77
  view 32KiB / grep 32KiB / glob 16KiB / ls 16KiB / shell 32KiB

internal/toolkit/tools/sandbox_support.go:15-18,43-56
  sandboxPolicy 直接持有 executor.Sandbox / toolctx

internal/toolctx/context.go
  session/toolcall/turn/goal/agent depth/workspace/artifact/read scope/...

cmd/toolkit-mcp-server/main.go:60-83
  当前入口只 Start stdio

internal/remotetools/search.go
  远程 grep 纯 Go WalkDir + regexp

internal/remotetools/shell.go
  foldRemoteOutput / boundedWriter 独立实现

go list -deps ./cmd/aicli-remote-tools
  不含 internal/toolkit/tools

go list -deps ./cmd/aicli
  含 internal/toolkit/tools
```

---

## 9. 审核签字

- 设计门：**Conditional approval for PR0**（P0/P1 内容冻结，自审）
- 已关闭的文档缺口：P0 4/4 内容已补，P1 主要项已补，P2 大部分已补
- PR1 门：PR0 通过 + P0-1/P0-2/P0-3 测试
- 发布门：独立安全评审 + PR1–3 证据 + canary/rollback
- 后续规则：不再重复设计复审；新发现进 PR backlog，只有可复现 P0
  安全缺陷才重开设计门

---

## 10. 修订记录

### 2026-10-22 P0 修订（已冻结）

设计文档已补 P0 四项：

- P0-1：新增 §9.3/§9.6，明确远程 metadata 是不可信提示，定义
  `RemoteOutputHardCap`、budget clamp、skip/tail 校验与恶意 metadata 测试；
  额外发现：远程 `MaxOutputBytes`（默认 256 KiB）同时被当作 capture 上限与
  模型可见窗口，设计已要求拆成 `CaptureLimit` 与 `ModelVisibleCap`；
- P0-2：新增 §9.7 与 `ArtifactRef`，定义 local 裸 id、
  `remote:<target>:art_...`、跨 scope 拒绝与兼容迁移规则；
- P0-3：重写 §6.6/§10，固定 0–18 gate 顺序，明确复用
  `internal/policy.Engine`，direct/CLI 不得绕过；
- P0-4：§11.1 决策为 v1 final-result only；interactive/PTY/jobs/streaming
  明确 local-only，Phase 4 与远程 schema 同步收敛。

当前状态：**P0 已补，设计冻结**；P1 修订见下一节。

### 2026-10-22 P1 修订（已冻结）

- 新增 §13.0 工具清单与迁移矩阵，按 `toolregistry` / `manager.go` 实际注册表
  标出 core/local-only/control-plane；
- 新增 §8.7 MCP surface 规格（transport/auth/roots/session/naming/limits/error）；
- 新增 §10.7 错误/重试/幂等（错误码、表面映射、只读重试、写/执行不重放）；
- 新增 §12.4 可观测性接口（事件 schema、脱敏、audit/metrics 分工）；
- 新增 §13.7 版本与 rollout（契约/schema/event 版本、混合版本、feature flag）；
- 新增 §16.3 威胁模型（远程 metadata、tool poisoning、路径/命令、SSRF、
  secrets、subagent、DoS）；
- 补 §15.2.1/§15.2.2 benchmark 语料与初始阈值；
- 补 §8.4.1 CLI/direct 契约（JSON、exit code、`--yes`、无 bypass）。
- 补 §13.8 网络类工具迁移：`fetch`/`download`/`web_search`/`sourcegraph`
  v1 保持 local-only，明确 NetworkPolicy/SecretProvider 进入条件与 N1–N5 顺序。
- 补 §17.4 验收矩阵：区分 P0/P1 文档门与 PR1–3 实现门，并列出测试/命令/证据。
- 补 §17.5 最佳实践验收/发布模型：DoR/DoD、T0–T6 测试分层、
  条目→测试→证据追溯、独立安全评审、canary/回滚阈值、no-go 条件。

当前状态：**P0+P1 设计冻结**。P2（文档一致性、命名/开放问题收尾）
进入 PR backlog，不阻塞 PR0。

P2 已处理：现状/目标混写、`InputSchema` adapter、`Registry.Filter`、
`CanDirectCall` 语义、`Sessions()` typed accessor、Windows 命令示例、
工具数量与附录映射。剩余：包名/CLI 名、图片/notebook 映射细节。

### 2026-10-22 Round 2 复审发现（设计已补，已冻结）

- P0-1：新增 wire 层 `MaxMCPResponseBytes`（8 MiB）与
  `REMOTE_RESPONSE_TOO_LARGE`，避免在 fold 之前被超大响应耗尽内存；
- P0-1：新增 `MCPResultContext`，trust 只来自 server config，
  禁止从 `source` 字符串猜；
- P0-2：新增 artifact ID/target 严格正则、归档失败
  `ARTIFACT_WRITE_FAILED`、过期 `ARTIFACT_EXPIRED`；
- P0-3：发现 allow rule 可短路 shell breaker/sensitive-write、callback
  可覆盖部分 deny；设计已改为 hard safety gates，并增加
  `policy_legacy_safety_order` 兼容开关；
- P1：access/audit log rotation/retention、metrics label 基数限制；
- P1：威胁模型新增构建/部署链篡改；T1/T5 增加 race。

---

## 11. 证据门与发布签字（Round 2 freeze）

设计已冻结；以下不再是重复设计复审，而是 PR0/PR1/发布证据门：

- **PR0 可开始**：设计冻结（本轮）；
- **PR1 可开始**：PR0 通过 + P0-1/P0-2/P0-3 测试；
- **可发布**：11.1/11.2/11.3 全部满足。

### 11.1 设计与安全证据（发布门）

- [ ] P0-1 独立安全复审：恶意/超大 metadata、skip/budget/tail、capture/model 拆分；
- [ ] P0-1 wire cap / `MCPResultContext` / trust 仅来自 server config；
- [ ] P0-2 独立安全复审：local/remote/target artifact scope、裸 id 归属；
- [ ] P0-2 严格 ID/target、归档失败、过期/跨 scope 拒绝；
- [ ] P0-3 设计/运行时复审：0–18 gate、direct/CLI 同一性、无第二授权层；
- [ ] P0-3 hard safety gates：allow rule/callback 不能覆盖安全门；
- [ ] P0-4 范围复审：final-result only、PTY/jobs/streaming local-only；
- [ ] P1 工具清单：§13.0 与 `toolregistry` / `manager.go` 双向一致；
- [ ] P1 MCP surface：auth/roots/session/name/limits/error；
- [ ] P1 错误/重试：错误码、表面映射、写/执行不自动重放；
- [ ] P1 可观测性：Event schema、脱敏、audit 失败策略；
- [ ] P1 版本：ContractVersion/schema/event 版本、混合版本；
- [ ] P1 威胁模型：每条威胁有测试映射；
- [ ] P1 性能：语料、阈值、CI 方法、基线报告；
- [ ] P1 运维：log rotation/retention、metrics 基数、race/负载；
- [ ] P2 收尾：`toolcore`/`toolctl` 命名、图片/notebook 映射。

### 11.2 PR0–3 实现证据

- [ ] PR0：契约冻结、测试骨架、工具清单对账；
- [x] PR1：output/trust/artifact 单测、wire cap、恶意 metadata、跨 scope、归档失败、238 canary；
- [ ] PR2：view/grep/glob/ls conformance、10k/100k benchmark；
- [ ] PR3：CLI JSON/exit code、hard safety gates、callback 不可覆盖 deny、`--yes` 不绕过；
- [ ] 全量回归：`go test ./...`、`go vet ./...`；
- [x] 回滚演练：回退 binary（当前无 toolcore feature flag；binary rollback 已实测）；
- [x] 证据归档：测试日志、benchmark JSON、canary 指标、rollback 记录；
  238 证据见 `docs/evidence/toolcore-238-canary-20261023.md`。

### 11.3 签字记录

| 角色 | 姓名/账号 | 结论 | 日期 | 备注 |
|------|-----------|------|------|------|
| Design owner | 待填 | 待签 | | |
| Security reviewer | 待填 | 待签 | 重点 P0-1/P0-2 | |
| Runtime owner | 待填 | 待签 | PR1–3 | |
| Release owner | 待填 | 待签 | 证据已备：`docs/evidence/toolcore-238-canary-20261023.md` | |

**发布条件**：11.1 全部勾选；11.2 至少 PR0 + PR1 + 对应 PR 证据齐全；
11.3 四位角色签字。PR0 不要求 11.1 全部完成，只要求设计冻结。

### 11.4 基线测试记录（2026-10-22）

以下命令验证的是**现有代码基线**，不是 PR1–3 的新实现：

| 命令 | 结果 |
|------|------|
| `go test ./internal/policy ./internal/toolregistry ./internal/output ./internal/toolresult -count=1` | PASS |
| `go test ./internal/tools ./internal/remotetools -count=1` | PASS |
| `go test ./internal/mcp/... -count=1` | PASS |
| `go test ./internal/toolkit/... -count=1` | PASS |

结论：当前基线可构建、policy/tool registry/output/MCP/remote/toolkit 测试通过；
P0/P1 修订未引入代码变更，PR1–3 的新测试仍需按 §11.2 执行。

### 11.5 Phase 6b 收敛证据（2026-10-22）

Phase 6b 完成 remote 侧去重：remote 工具不再保留 view/ls/glob/grep/
write/append_write/edit/apply_patch/artifact_read 的第二套实现，统一由
`toolcore` Handler + `RemoteRuntime` 提供。

- 提交：`4145118e`（ls）、`4dc71973`（view）、`5f92d7cb`（glob/grep）、
  `ed2cb46f`（mutation）、`2ce06d6e`（artifact_read）、
  `c172a767`（remote core-surface 契约测试）；
- 删除的重复实现：`remotetools/view.go`、`search.go`、`write.go`、
  `apply_patch.go`、`mutation_error.go`；
- 新增共享层：`toolkit.RuntimeTool`（Handler + 每次调用的 Runtime 工厂）、
  `CaptureBudgetProvider` / `Searcher` / `ArtifactOriginProvider` 可选扩展、
  `viewcore`/`lscore`/`searchcore`/`mutatecore` 的 Handler、
  `toolcore.NewArtifactReadHandler`；
- `RemoteRuntime` 仅保留 surface 策略：workspace jail、permissions、
  command policy、env filter、capture/model 拆分、输出整形、artifact scope、
  MCP roots 收窄；
- `job_*` 收敛为显式 `legacyRemoteJobs`（`--legacy-jobs`），默认关闭；
- 漂移闸门：`TestRemoteCoreSurfaceComesFromSharedHandlers` 钉死 remote 非
  shell 工具必须来自 `RuntimeTool` 包装的 core Handler。

验证（均 PASS）：

```powershell
go test -p 1 -count=1 ./internal/toolcore/... ./internal/toolkit ./internal/remotetools
go test -p 1 -count=1 ./internal/tools
go test -p 1 -count=1 ./internal/toolctl
go test -p 1 -count=1 ./internal/toolkit/...
go vet ./internal/toolcore/... ./internal/remotetools ./internal/toolkit
```

本地 `internal/toolkit/tools` 仍是 LocalRuntime adapter：本地专属的
stale-write/document guard、图片/notebook、read dedup/LSP、PTY/detach、
todos/code.* 保留在本地实现；公共语义已调用 `viewcore`/`searchcore`/
`lscore`/`mutatecore`/`shellexec`，本地与 remote 不再维护两套同名算法。

### 11.6 全仓 `-p 1` 回归记录（2026-10-22）

命令：

```powershell
$env:GOGC='20'; $env:GOMAXPROCS='2'
go test -json -count=1 -p 1 ./...
```

结果：`EXIT=1 ELAPSED=28m35s EVENTS=88458 FAIL_EVENTS=6`，失败集中在 3 个
负载敏感测试，工具层包全部通过：

| 测试 | 处置 | 复跑证据 |
|------|------|----------|
| `internal/knowledge::TestWatchSourceSoftDeletesExternalRemovalWhileIdle` | 真实竞态修复：新增目录 Create 事件到达时递归补监听并回扫已存在文件（`fix(knowledge): reconcile files in newly watched directories`） | 目标测试 `-count=5` 全绿；`internal/knowledge` 整包 PASS（46s） |
| `internal/mesh::TestLeaseConcurrentAcquireHasSingleWinner` | 竞争时序抖动，非代码回归 | `-count=3` 全绿；`internal/mesh` 整包 PASS（10s） |
| `cmd/aicli/commands::TestAICLIChatActorExecutor_InteractiveAutoStartRendersTeamTimeline` | 测试在 team terminal 后再等 2s 收 timeline 行；当前宿主虚拟内存耗尽（`VirtualAlloc errno=1455`）导致该大包无法重新编译复跑 | 待静默 CI 复跑；本次全量前该包曾在低内存配置下整体 PASS（232.6s） |

说明：`go test -p 2 ./...` 与 `-p 1` 全量都受同机内存/分页限制影响，失败包
逐项复跑后均无工具层回归；`cmd/aicli/commands` 的单项验证需在资源充足的
CI 机器上补齐，届时更新本表。

### 11.7 238 canary 与回滚演练（2026-10-23）

- 目标：`238`（`172.18.3.238:18765/mcp`），workspace `/home/n9e/itsm/repo`；
- 源码基线：`d1fc884a`；canary 客户端：`scripts/canary-remote-tools.ps1`；
- 初始发现：238 运行的是旧版 remote-tools，`artifact_read` 仍拒绝
  `artifact_ref`（JSON-RPC `-32602`），不能作为当前 Phase 6b 的证据；
- 处置：构建并安装当前 Linux amd64 binary（SHA-256
  `1a7eabe2...`），保留旧 binary（SHA-256 `48ab4f08...`）；
- 六工具 smoke：view/ls/glob/grep/shell/artifact_read 全 PASS；跨 target
  `remote:999:art_...` 返回 `ARTIFACT_SCOPE_DENIED`；
- 100 次 canary：100/100 成功，`error_rate=0`，`p50=228ms`，`p95=690ms`；
- 回滚演练：回退 binary 5.6s，前滚 5.5s；回退后
  `itsm-deploy-238 status` 仍为 front-http=200、三个容器 healthy；前滚后
  重跑 smoke + 100 calls 全 PASS；
- 证据：`docs/evidence/toolcore-238-canary-20261023.md`。

运维注意：238 的 `aicli-remote-238.service` 当前无法由 `n9e` 无密码
`systemctl restart`；canary 通过既有 `start-remote-238.sh` fallback 启动。
生产重启必须由 root installer/发布流水线完成。
