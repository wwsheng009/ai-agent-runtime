# 工具层抽象设计审核（2026-10-22）

- 被审文档：`docs/plan/tool-layer-abstraction-design-20261022.md`
- 审核范围：架构完整性、契约完整性、安全边界、输出契约、状态模型、多表面适配、迁移与验收
- 审核结论：**Changes requested**
- 总体判断：**方向正确，但当前版本不足以直接进入实现**。建议先关闭 P0，再开始 PR 1。

> 2026-10-22 修订：设计文档已补 P0 四项，见 §10 修订记录；本轮仍维持
> **Changes requested**，待复审后再决定是否放行实现。

## 1. 总体结论

方案的“一份定义 + 一份实现 + 多表面 adapter”方向是对的，也正确识别了当前本地/远程工具重复、输出策略漂移、`toolkit/tools` 与 `toolctx` 耦合等问题。

但目前文档更接近“架构愿景 + 迁移提纲”，还不是可执行的实现规格。主要缺口：

| 维度 | 覆盖度 | 结论 |
|------|--------|------|
| 分层架构 | 80% | 方向正确，依赖规则清楚 |
| 核心契约 | 70% | 接口草图有了，但边界/兼容映射不足 |
| 权限与安全 | 50% | 缺少 gate 顺序、信任边界、威胁模型 |
| 输出契约 | 75% | 复用了本地资产，但缺少不可信远程输出的硬边界 |
| 会话与状态 | 35% | 严重低估 `toolctx` 与本地会话态 |
| 多表面适配 | 65% | builtin/MCP/remote/CLI 有描述，缺 MCP 规格与 CLI 契约 |
| 迁移与兼容 | 65% | 有阶段，但缺工具清单、双跑、版本策略 |
| 测试与性能 | 60% | 有方向，缺 oracle、阈值、负载/竞态测试 |
| 运维与版本 | 30% | 缺 rollout、混合版本、审计版本、token 轮换 |
| 文档一致性 | 75% | 少量“现状 vs 目标”混写 |

建议整体完成度：**约 65%**。补齐 P0/P1 后可进入实现。

---

## 2. P0 阻塞项（必须先补）

### P0-1 远程输出 metadata 的信任边界（安全）

**问题**

方案第 9 节建议远程复用本地的 `skip_render_truncation`、`model_visible_budget`、`reserved_tail_bytes`、`artifact_id`。但当前本地链路会**无条件信任** MCP 返回的 `Meta`：

- `internal/toolkit/mcp_server.go:147-151`：远程 `ToolResult.Metadata` → `CallToolResult.Meta`；
- `internal/tools/manager.go:969-1010`：`formatMCPResult` 直接 `cloneMetadataMap(result.Meta)`；
- `internal/output`：`skip_render_truncation=true` 时直接 pass-through；
- `internal/output/reserved_tail_test.go:267-285` 明确验证了“owned window 不再 fold”。

也就是说：一个被攻陷/配置错误的远程 MCP server 可以返回：

```json
{"skip_render_truncation": true, "model_visible_budget": 999999999}
```

并附带巨大 body，从而**绕过本地 render 层的模型上下文预算**。这不是抽象层的小问题，而是跨信任域的输出注入。

**必须在方案中明确**

1. 远程 metadata 是**不可信提示**，不是授权；
2. 本地 surface 对 `executionScope=remote`（或 trustLevel != local）的响应强制硬上限，不因 `skip_render_truncation` 放行；
3. `model_visible_budget` 只能**收窄**，不能超过本地 `modelToolTextByteBudget`/硬上限；
4. `reserved_tail_bytes` 必须校验（不得超过 hard cap，必须能从 body 中找到对应尾部），否则忽略；
5. `artifact_id` 必须带 namespace（见 P0-2）；
6. 增加恶意 metadata 测试：
   - 远程声明 skip + 超大 body → 本地仍然 fold/拒绝；
   - 远程声明超大 budget → 本地 clamp；
   - 远程声明虚假 reserved tail → 忽略；
7. 文档第 9.3 节要增加“trust boundary”小节，不能只写“链路已通”。

**结论**：不关闭此项，PR 1 不应该上线。

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

---

## 3. P1 缺口（实现前应补齐或明确 defer）

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

## 4. P2 文档一致性问题

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

## 5. 必须新增的章节

建议在方案中新增：

1. **8.7 MCP Surface 规格**：auth/roots/stateless/names/limits/trust/error mapping。
2. **9.6 输出信任边界**：remote metadata hard cap、artifact namespace、reserved tail 校验。
3. **10.5 Gate 顺序矩阵**：deny-tool / mode / profile / command policy / approval / direct call。
4. **11.x SessionState 能力矩阵**：toolctx 全量、TTL/GC、stateless 差异。
5. **12.4 Observability 接口**：Logger/Tracer/Progress/Metrics + 事件 schema 版本。
6. **13.x 工具清单与迁移矩阵**：逐工具 core/local-only/surface/phase。
7. **14.x 版本与 rollout**：core/surface/protocol/audit 版本、混合版本、remote 升级。
8. **15.x 信任与对抗测试**：恶意 metadata/artifact/schema/tool poisoning。
9. **16.3 威胁模型**：secrets/SSRF/subagent/OS 隔离。
10. **17.x 验收标准**：每阶段可量化的 DoD。

---

## 6. 修订后的 Definition of Done

方案达到以下条件才算“完整、可进入实现”：

- [ ] P0-1 输出信任边界：有硬上限、clamp、reserved tail 校验、恶意 metadata 测试；
- [ ] P0-2 artifact namespace：scope/target/id 路由与跨 scope 拒绝测试；
- [ ] P0-3 gate 顺序矩阵：direct/CLI/MCP/remote 同一顺序，无第二授权层；
- [ ] P0-4 streaming/jobs/interactive 决策：纳入契约或明确 local-only；
- [ ] 逐工具清单与迁移矩阵完成；
- [ ] MCP surface 规格完成（auth/roots/stateless/names/limits/trust）；
- [ ] SessionState 矩阵完成（含 toolctx 全量能力）；
- [ ] 错误/重试/幂等映射表完成；
- [ ] 版本/混合版本/rollout 方案完成；
- [ ] 性能语料、阈值、CI 方法完成；
- [ ] 威胁模型与安全测试完成；
- [ ] 现有 `toolkit.Tool` → core Handler 的兼容映射完成；
- [ ] P2 文档一致性问题修正。

---

## 7. 建议修订顺序

1. 先补 P0-1/P0-2（信任边界 + artifact scope），这是安全阻塞；
2. 补 P0-3（gate 矩阵），避免实现时再长出第二授权层；
3. 决定 P0-4（streaming/jobs 是否进入 core）；
4. 补工具清单 + MCP surface 规格；
5. 补 SessionState/错误/版本/运维；
6. 最后补测试阈值与 DoD，再开始 PR 1。

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

- 结论：**Changes requested**
- 可进入实现的条件：P0 全部关闭，P1 全部解决或明确 defer（含 owner/日期）
- 建议下一次评审重点：P0-1 信任边界、P0-3 gate 矩阵、P0-4 streaming/jobs 范围

---

## 10. 修订记录

### 2026-10-22 P0 修订（待复审）

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

当前状态：**P0 已补，等待复审**。P1/P2 仍未全部关闭，因此不建议开始实现。
