# 工具面逻辑依赖与自动暴露方案

> 状态：**已实施并通过测试（D1–D12 全部确认；P0–P4 代码落地完成）**  
> 日期：2026-10-29（第二轮修订：2026-10-30；决策确认：2026-10-30；实施：2026-10-30）  
> 范围：`shell` / `job_*` 的逻辑依赖、tool registry 元数据、local aicli 工具策略、remote-tools profile、MCP 工具暴露、子代理工具面继承  
> 关联文档：`docs/plan/shell-timeout-job-promotion-core-design-20261029.md`  
> 核心结论：`shell` 不调用 `job_*`，但它的 promoted job 结果需要 `job_output` 才能被消费；这是**结果/数据契约依赖**。必须把依赖声明收敛到共享 tool registry，并在 local/remote 的工具面装配阶段做依赖闭包与校验，且补入结果必须复用既有 `policy.ContractualTools` 授权路径（§3.4）。

---

## 1. 问题定义

### 1.1 不是调用依赖

当前实现中不存在：

```text
shell -> job_start / job_output / job_cancel
```

shell 与 job tools 都是 `background.Manager` 的上层适配器：

```text
shell      -> shelljob.Execute -> ManagerRuntime -> background.Manager
job_start  -> background.Manager.SubmitShell
job_output -> background.Manager.ReadOutput
job_cancel -> background.Manager.CancelJob
job_list   -> background.Manager.ListJobs
```

因此不能把依赖实现成 shell 内部调用 `job_output`，否则会形成工具层循环依赖，并重复 job 控制面逻辑。

### 1.2 是结果依赖

Shell 有两种结果形态：

1. **终态结果**：命令在 wait timeout 内结束，输出已经完整返回；不需要任何 `job_*`。
2. **promoted 结果**：wait timeout 后命令继续运行，shell 只返回 job id、当前输出窗口和 next offset；后续输出必须通过 `job_output` 读取。

当前代码已经把这种结果依赖写进了模型可见提示：

- local `bash/shell`：`job_output job_id=... offset=...`
- remote shell：`next_action = "job_output job_id=... offset=..."`
- `job_start`：`Poll with job_output job_id=...`

如果工具面没有 `job_output`，模型会拿到一个无法继续的 job 引用；同时 `next_action` 会指向一个当前不可调用的工具。这是业务契约断链，不是调用栈断链。

### 1.3 当前缺口

#### Remote-tools

`enabledTools()` 当前实现顺序是：

```text
profile 基础工具
  -> 若 jobs.root 非空，追加 jobTools
  -> 应用 tools allowlist
  -> 应用 readOnly
```

因此以下配置可以只暴露 `shell`：

```yaml
profile: all
tools: ["shell"]
```

或：

```yaml
profile: custom
tools: ["shell"]
```

结果：shell 仍然依赖 `jobs.root` 和 `background.Manager`，超时会返回 promoted job，但 `job_output` / `job_cancel` / `job_list` 已被 allowlist 裁掉。

证据：

- `backend/internal/remotetools/tools.go:67-72`：job tools 追加
- `backend/internal/remotetools/tools.go:99-112`：`Tools` allowlist 收窄
- `backend/internal/remotetools/registry.go:773-788`：只要 effective surface 含 shell，就需要 job runtime
- `backend/internal/remotetools/shell.go:252-257`：timeout 返回 `job_output` next action
- `backend/internal/remotetools/jobs_tools.go:189`：job_start 返回 `Poll with job_output`

#### Local aicli

`buildLocalChatToolPolicy` 的默认合成路径会加入 broker definitions：

```text
toolSurface.ListTools() + brokerToolNames(broker.Definitions()) + ...
```

但显式 allowlist 路径明确不扩宽：

- `backend/cmd/aicli/commands/chat_actor_host.go:2926`：默认合成包含 broker tools
- `backend/cmd/aicli/commands/chat_actor_host.go:2929`：explicit profile/allowlist 不扩宽
- `backend/cmd/aicli/commands/chat_permissions_overlay.go:46-63`：CLI `--allow-tool` / permissions overlay 可建立 allowlist
- `backend/internal/policy/tool_policy.go:167`：essential 不绕过 allowlist
- `backend/internal/toolregistry/registry.go:200,206`：`job_output` 是 essential，但 `job_cancel` 不是

因此：

```text
aicli exec --enable-tools --allow-tool shell ...
```

可能只允许 shell；shell 超时后 `job_output` 被策略拒绝。

#### 命名面

remote-tools 有 tool prefix：

```text
canonical: job_output
advertised: remote_dev__job_output
```

当前 shell 与 job_start 的 `next_action` 仍使用未加前缀的 `job_output`。即使自动暴露了 job_output，模型看到的名字和提示仍可能不一致。

证据：

- `backend/internal/remotetools/registry.go:37-66`：`prefixedTool` 重写 advertised name
- `backend/internal/remotetools/shell.go:257`：未解析 prefix 的 next_action
- `backend/internal/remotetools/jobs_tools.go:189`：未解析 prefix 的 body

---

## 2. 目标与非目标

### 2.1 目标

1. 用**共享元数据**描述工具之间的逻辑/结果依赖。
2. 在 local aicli 与 remote-tools MCP 两个装配面应用同一套依赖闭包。
3. `shell` / `job_start` 可见时，至少保证 `job_output` 可见；显式 deny 时 fail-closed。
4. profile、`tools` allowlist、`readOnly` 仍保持权限边界，不因依赖机制产生未声明的控制面扩权。
5. MCP `tools/list` 暴露最终可用工具，`next_action` 使用实际 advertised name。
6. 不引入第二套 job runtime，不改变 shell 的 `Submit -> Wait -> Read -> Forget` 状态机。

### 2.2 非目标

- 不把 `job_cancel` 无条件自动暴露。它是控制/终止能力，必须继续受 `background_task` 权限控制。
- 不改变 `job_output` 的 owner/session 校验。
- 不新增 MCP 协议级依赖语义；MCP 只接收解析后的最终工具列表。
- 不把依赖元数据直接塞进模型 prompt 或工具描述来“让模型自己推断”。
- 不保留 profile/allowlist 的第二套硬编码工具关系。

---

## 3. 依赖模型

### 3.1 角色

| 工具 | 角色 | 结果是否含 job reference |
|---|---|---|
| `shell` | producer + foreground waiter | timeout 时是 |
| `aicli_exec` | **已核实不是 producer** | 否（见下） |
| `job_start` | producer | 始终是 |
| `job_output` | consumer / reader | 否 |
| `job_cancel` | control | 否 |
| `job_list` / `job_search` | discovery | 否 |
| `job_monitor` / `job_control` / `job_input` | control/monitor | 否 |

### 3.2 必需与推荐

- **Required**：没有它，生产者的公开结果无法消费；缺失时 fail-closed。
- **Recommended**：缺失不破坏读取，但会降低完整控制/发现能力；只提示，不自动扩权。
- **AutoExpose**：解析器允许在生效面上自动加入的工具；必须同时满足权限/readOnly 约束。

当前建议：

```text
shell:
  required: job_output
  autoExpose: job_output
  recommended: job_cancel, job_list

job_start:
  required: job_output
  autoExpose: job_output
  recommended: job_cancel, job_list

job_output:
  required: none

job_cancel / job_control / job_input / job_monitor:
  required: none
  # 控制能力独立授权，不自动补入
```

说明：

- `shell` 不需要 `job_start`：shell 自己通过 `background.Manager.SubmitShell` 产生 job。
- `job_start` 不需要 shell：它通过 manager 直接提交 job。
- `job_output` 不需要 producer：它只要求 manager 和目标 job 存在；在没有 job 时返回业务错误，不应因此自动扩权。
- `job_cancel` 等控制工具只在用户/profile 明确允许时暴露；不因为 shell 超时就自动获得终止权限。

### 3.2.1 为什么 `aicli_exec` 不声明依赖

`toolregistry/registry.go:144` 声明了 `{Name: "aicli_exec", ShellLike: true, ToolkitBuiltin: true}`，它与 `shell` 同属 shell 组，因此必须显式给出结论，否则角色表会被质疑不完整。

核实结论：**`aicli_exec` 不是 job 生产者，不需要声明 `job_output` 依赖。**

证据：

- `backend/internal/toolkit/tools/aicli_exec.go:230-231`：`cmdCtx, cancel := context.WithTimeout(ctx, req.Timeout)`，子进程生命周期由单次调用的 `context.WithTimeout` 约束；
- `backend/internal/toolkit/tools/aicli_exec.go:265-271`：超时分支把 `context.DeadlineExceeded` 转成失败结果（`aicli exec 执行超时（超过 %v）`），**不返回 job id，也不写 `next_action`**；
- `backend/internal/toolkit/tools/aicli_exec.go:194-195`：结果只经 `ownShellOutputWindow` 做输出窗口折叠，没有 job-first / promoted 分支；
- 全文件不存在 `job_first`、`promoted`、`background.Manager` 或 `SubmitShell` 引用。

因此 `aicli_exec` 的超时是**终态失败**而非 promoted 结果：模型拿到的是可读错误，而不是一个需要 `job_output` 继续消费的引用。它既不进入依赖闭包，也不进入 §6.2 的 job runtime 必要性判定。

判据（供未来新增 shell 类工具时复用）：**只有"超时后仍返回 job id + offset，且要求后续读取"的工具才算 producer**。仅以 `ShellLike` 或 `GroupShell` 判定会过度声明依赖。

### 3.2.2 `job_search` 与 recommended 的适用范围

`recommended` 只做"缺失时提示"，不参与 fail-closed，但**必须按实际存在的工具面裁剪**，否则 resolver 会对不存在的能力持续告警，把真正的缺口淹没在噪声里。

核实事实：

- `backend/internal/remotetools/jobs_tools.go:26`：`jobTools = []string{ToolJobStart, ToolJobOutput, ToolJobCancel, ToolJobList}` —— remote 侧**没有 `job_search`**，也没有 `job_monitor` / `job_control` / `job_input`；
- `backend/internal/toolregistry/registry.go:202`：`job_search` 只在 local 注册表中存在。

因此：

- `job_search` 只能作为 **local 专属** recommended，不得写进共享声明的推荐项（本文 §3.2 已移除）；
- 由于 `Entry.Dependencies` 位于共享 registry，实现上必须为 recommended 项增加宿主面过滤：仅当目标面确实存在该工具时才提示，可复用 `Entry.Exposure` 的判定，或在 `ToolDependency` 增加 `ApplicableSurfaces []Exposure`；
- §9.1 单测必须覆盖"recommended 项在缺少实现的面上不产生告警"。

### 3.3 为什么不做能力级抽象

第一阶段建议直接使用 canonical tool name：

```text
Requires: ["job_output"]
```

原因：

- 当前只有 shell/job 这一组明确的依赖，直接名最容易审计。
- toolregistry 已有 canonical name + aliases，可先归一后查表。
- MCP prefix 在依赖解析之后应用，不会影响 canonical 依赖。
- 若未来出现多条同类链路，再加 `Provides/RequiresCapabilities` 能力层；不要为了一个依赖先造抽象。

如果后续出现多个 reader（例如 job output resource），可增加能力映射：

```text
job.read    -> job_output
job.control -> job_cancel
job.discover-> job_list
```

但本期不以能力层为前置条件。

### 3.4 与既有 `Contractual` 契约机制的关系

本节是方案的**前置定调项**，必须在 P0 冻结前回答，否则会与"不新增第二套"的约束冲突。

代码里已经存在一个语义高度同构的机制——**运行时契约工具**：

| 既有机制 | 位置 | 现有语义 |
|---|---|---|
| `Entry.Contractual` | `toolregistry/registry.go:90-97` | 标记"某条运行时契约的组成部分"，当前仅 `artifact_read` |
| `toolregistry.ContractualNames()` | `toolregistry/registry.go:298-302` | 契约工具名的单一真源 |
| `policy.ContractualTools` | `policy/tool_policy.go:34-38` | 策略内的契约集合 |
| `AllowTool` 判定顺序 | `policy/tool_policy.go:142-201` | deny 优先 → **契约工具** → BlockDelegation → allowlist 闸门 → capability → read-only |
| `appendContractualToolNames` | `agent/tool_vocabulary.go:305-320` | 子代理显式白名单补入契约工具，deny 跳过 |

注意其注释与本文目标几乎逐字同构：`"a profile that omits the tool from its allowlist still gets it, while an explicit deny wins"`（`policy/tool_policy.go:35-36`）正是 §11 验收 1–2 要求的语义。

它与本方案 `Dependencies.AutoExpose` 的差异：

| 维度 | `Contractual`（既有） | `Dependencies`（本方案） |
|---|---|---|
| 授权来源 | 全局常量集合，与具体 enable 了什么工具无关 | **条件性**：只有当 producer 在生效面上才触发 |
| 粒度 | 工具级 | producer → consumer 关系级 |
| 触发条件 | allowlist 省略即补入 | producer 可见才补入 |
| 失败策略 | 静默补入（无 required 概念） | required 缺失即 fail-closed |

**结论与决策（写入 D10）**：二者**不合并为同一字段，但必须共用同一条授权实现路径**，理由是 `Contractual` 是"无条件契约"，`Dependencies` 是"条件性契约"，合并会让 `artifact_read` 在无 producer 时也被依赖图逻辑处理，反而破坏其现有语义。

具体收敛要求：

1. `AutoExpose` 的补入结果**必须最终表达为 `policy.ContractualTools` 的成员**，而不是在 `AllowedTools` 里塞名字。这样 deny 优先、DisableTools 空面、子代理白名单继承三条既有不变量自动复用，不需要在 resolver 里重新实现一遍。
2. resolver 只负责**算出**有哪些工具应被补入；**授权判定沿用 `AllowTool` 的既有顺序**，不新增判定分支。
3. `toolregistry` 中 `Contractual` 与 `Dependencies[].AutoExpose` 同时存在时，必须有单元测试钉死二者互不干扰（§9.1 新增）。
4. 文档层需在 `Contractual` 字段注释里补一句"条件性契约见 `Dependencies`"，避免后续读者误以为有两套并行机制。

**已确认（2026-10-30，见 §13.3 D10）**：本节结论成立，并补充三条实现硬约束——

- `contractualToolAllowed` 在空 allowlist（`!AllowlistEnabled || len(AllowedTools)==0`）时返回 `false`（`tool_policy.go:192-195`），故条件性契约只能"填补遗漏项"，不能重启已关闭的面；
- `Clone` 会克隆 `ContractualTools`（`:616`），resolver 必须早于 subagent policy 派生写入；
- `ContractualTools` 为空时回退 `toolregistry.IsContractual`（`:196-198`），条件性契约必须显式写入 map，不能只靠 registry 声明。

---

## 4. 元数据设计

### 4.1 toolregistry 扩展

`internal/toolregistry` 是工具身份与声明面的单一真源，适合承载“结果依赖”的**声明**，不适合承载执行实现或工具面装配。解析器放在 `internal/toolsurface`：该包已经承担工具面装配与来源账本，且可以依赖 toolregistry 而不形成环。

建议新增：

```go
type DependencyMode string

const (
    DependencyRequired    DependencyMode = "required"
    DependencyRecommended DependencyMode = "recommended"
)

type ToolDependency struct {
    Tool       string
    Mode       DependencyMode
    AutoExpose bool
    Reason     string
}

type Entry struct {
    // existing fields...

    // Dependencies describes logical/result dependencies, not function calls.
    Dependencies []ToolDependency
}
```

约束：

- `Tool` 使用 canonical name；解析前先 `Normalize`。
- `AutoExpose` 只对 required/推荐显式声明为 true 的项生效。
- `Reason` 用于启动日志、doctor、错误信息，禁止只写“depends on”。
- 不把 `Dependencies` 直接映射到 MCP annotation；这是宿主内部装配语义，不是模型可见的工具契约，也不是 `ToolResult.Metadata`。

### 4.2 声明示例

```go
{
    Name: "shell",
    // ...
    Dependencies: []ToolDependency{
        {
            Tool:       "job_output",
            Mode:       DependencyRequired,
            AutoExpose: true,
            Reason:     "promoted shell result is a job reference; output must be consumable",
        },
        {
            Tool:       "job_cancel",
            Mode:       DependencyRecommended,
            AutoExpose: false,
            Reason:     "long-running shell job may need an explicit cancel path",
        },
        {
            Tool:       "job_list",
            Mode:       DependencyRecommended,
            AutoExpose: false,
            Reason:     "discover promoted jobs without a remembered id",
        },
    },
},
{
    Name: "job_start",
    Dependencies: []ToolDependency{
        {
            Tool:       "job_output",
            Mode:       DependencyRequired,
            AutoExpose: true,
            Reason:     "job_start always returns a job reference",
        },
    },
},
```

`job_output` 自己不需要依赖声明；`job_start` / `shell` 声明的是**生产者对消费者的要求**，不是消费者反向依赖生产者。

### 4.3 依赖图不变量

1. 依赖图必须是 DAG；当前只允许 producer -> consumer，禁止 consumer -> producer。
2. 任何 `Required` 依赖缺失，最终有效面校验失败。
3. 显式 deny 优先于 auto-expose；若 required 依赖被 deny，生产者本身也不得暴露。
4. `readOnly` 不得通过依赖闭包获得 `shell` / `job_start` / `job_cancel` 等控制能力。
5. `DisableTools` 语义不变：空工具面，不跑依赖闭包。
6. `job_output` 必须继续做 session owner 校验；自动暴露不等于跨 session 读权限。

---

## 5. 解析器设计

### 5.1 统一入口

建议在 `internal/toolsurface` 提供纯函数；`internal/toolregistry` 只负责 `Entry.Dependencies` 声明，避免把“暴露哪些工具”的职责塞回身份注册表。

```go
type SurfaceResolutionInput struct {
    Base        []string
    Denied      []string
    ReadOnly    bool
    Disabled    bool
    // Available producers/consumers are checked by the caller.
}

type SurfaceResolution struct {
    Tools        []string
    AutoExposed  []string
    Warnings     []string
    Errors       []error
}

func ResolveDependencies(in SurfaceResolutionInput) SurfaceResolution
```

实现细节：

- 输入是 canonical names。
- 先 normalize，再查 `Toolregistry`。
- 依赖闭包按 BFS/队列做；每加入一个工具继续扫描其依赖。
- 幂等：重复解析不重复加、不重复告警。
- 不允许在 resolver 内部创建 manager、读配置或启动 job；它只做名字解析。
- manager 可用性、jobs.root 可写性由调用方在 resolver 之后做运行时校验。

### 5.2 闭包算法

```text
resolved = base - denied - readOnlyIncompatible
changed = true
while changed:
    changed = false
    for tool in resolved:
        for dep in tool.Dependencies:
            if dep.Tool in denied:
                if dep.Mode == Required:
                    error("tool %s requires denied %s: %s", tool, dep.Tool, dep.Reason)
                else:
                    warning(...)
                continue
            if dep.Tool not in resolved:
                if dep.AutoExpose && dependencyCanBeExposed(dep, readOnly):
                    resolved += dep.Tool
                    autoExposed += dep.Tool
                    changed = true
                else if dep.Mode == Required:
                    error("tool %s requires %s, but auto-expose is disabled by policy", ...)
                else:
                    warning(...)
validateRequiredDependencies(resolved)
```

`dependencyCanBeExposed` 的最小规则：

- `readOnly` 下，只允许只读工具；`job_output` / `job_list` / `job_search` 可以是只读，但只有 producer 存在时才需要。
- 任何控制/终止工具（`job_cancel`、`job_monitor`、`job_control`、`job_input`）不得由依赖闭包自动加入。
- 显式 deny 恒优先。

**落地面（与 §3.4 一致，勿在此另建一套）**：`resolved` 只是 resolver 的内部计算结果，不是授权来源。`autoExposed` 集合必须交给 policy 层，并以 **`policy.ContractualTools` 成员**的形式生效，最终由既有 `AllowTool` 顺序判定（deny → contractual → BlockDelegation → allowlist → capability → read-only）。禁止在 resolver 里直接把名字塞进 `AllowedTools`，否则会绕过 deny 优先与子代理继承两条既有不变量。

### 5.3 profile 与 allowlist 的位置

解析顺序固定：

```text
profile 基础工具
  -> tools allowlist / custom
  -> readOnly hard filter
  -> explicit deny
  -> dependency closure
  -> required validation
  -> MCP prefix / surface naming
  -> tools/list
```

注意：

- **依赖闭包在 allowlist 之后**，否则 allowlist 仍会裁掉 companion。
- `profile` 不得在自身数组里手写 job companion；依赖关系只在 registry 声明一次。
- `ProfileCustom` 的“精确列表”语义直接切换：required companion 是隐式成员，由闭包自动补入并记录审计日志；不提供 `auto_expose=false` 兼容开关。用户若不允许该 companion，只能显式 deny，随后 surface 构建失败。
- 显式 deny companion 时，整个 effective surface 构建失败（fail closed）；解析器不得静默删除 producer 后继续，否则用户会得到一个看似可用但已失去 shell 的 profile。

注：remote-tools 当前没有独立的 `deniedTools` 列表；它的负向控制主要是 `readOnly`、`tools` allowlist 和 `jobs.root` 不可用。本文的“显式 deny required companion”场景首先适用于 local aicli 的 `DeniedTools`；remote 若需要同等表达能力，应单独增加配置项并复用同一 resolver，而不是把 `tools` 的缺省当成 deny。

### 5.4 错误与观测

建议新增稳定的观测字段/日志：

```text
surface.dependency.auto_exposed = ["job_output"]
surface.dependency.missing_required = ["job_output"]
surface.dependency.denied_required = ["job_output"]
surface.dependency.producer = "shell"
surface.dependency.reason = "promoted shell result is a job reference"
surface.dependency.desync = "surface|executor|model mismatch"
```

**落地位置（原稿只给了字段名，未指定载体）**：

| 载体 | 现状 | 本轮要求 |
|---|---|---|
| 结构化日志 | `remotetools` 已有 `AuditSink` / `Metrics`（`registry.go:790-804`） | 复用同一 sink，不新建日志通道；字段名前缀固定 `surface.dependency.` |
| `doctor` 输出 | 已有 `cmd/aicli/commands/doctor.go` 检查链 | 新增一条"工具面依赖"检查项，输出 auto-exposed / missing / denied |
| MCP banner | 启动 banner 打印 target 与 profile | 追加上一段的摘要，仅在非空时打印，避免噪声 |
| local `/tools` 诊断 | 现有诊断命令 | 展示 effective name（含 prefix 语义）与依赖来源 |

`doctor` / MCP banner / local `/tools` 诊断应能看到：

- 哪些工具因依赖被自动加入；
- 哪些 required 依赖缺失；
- 显式 deny 导致哪个 producer 被禁用；
- 最终 MCP advertised name（含 prefix）。

**观测约束**：依赖相关日志**不得**包含 job id、命令正文或输出内容，只输出工具名与 reason 字符串——沿用 `auditTool` 的既有脱敏约定（`registry.go:791-795`）。

---

## 6. Remote-tools / MCP 接入

### 6.1 enabledTools 重写

`Config.enabledTools()` 当前是 profile + tools + job tools 的拼接。建议拆成：

```text
profileTools(profile)
  -> applyToolsAllowlist
  -> applyReadOnly
  -> ResolveDependencies
  -> validateEffectiveTools
```

`enabledTools()` 仍作为最终有效工具的单一入口，供：

- `buildRegistry`
- `configNeedsJobRuntime`
- profile/doctor/banner/tests
使用，避免出现“工具面一套、manager 判断另一套”。

### 6.2 shell 与 job runtime 校验

当 effective set 含 `shell` 或 `job_start`：

- 必须有有效 `jobs.root`；
- `configNeedsJobRuntime` 必须为 true；
- `newRemoteBackgroundManager` 必须在启动阶段成功；
- 失败时返回配置错误，而不是注册一个调用时才报 `CodeUnsupported` 的 shell。

当前 `Normalize` 会为 jobs.root 提供默认值；仍应保留显式校验，覆盖：

- `jobs.root` 不存在且无法创建；
- 只读文件系统；
- 当前 backend/transport 不支持该 job runtime 的场景（按其既有约束返回明确配置错误）。

### 6.3 MCP tools/list

服务端解析依赖后再生成 registry：

```text
effectiveTools = ResolveDependencies(profile + tools + ...)
registry = buildRegistry(effectiveTools)
tools/list = prefixed registry tools
```

因此 MCP 客户端不需要理解依赖；它只会看到：

```text
remote_dev__shell
remote_dev__job_output
remote_dev__job_cancel   # 仅当 profile/allowlist 允许
remote_dev__job_list     # 仅当 profile/allowlist 允许
```

### 6.4 next_action / body 名称解析

必须在 `remoteToolBase` 或 `Config` 提供：

```go
func (c *Config) EffectiveToolName(canonical string) string
```

规则：

- `ToolPrefix == ""` -> canonical
- 否则 -> `prefix + "__" + canonical`

所有模型可见引用必须走该函数：

- shell promoted `next_action`
- shell read-failure hint
- job_start body
- job_output next_action
- MCP banner / doctor 示例

否则自动暴露的工具名和提示不一致，模型无法正确调用。

### 6.5 只读与权限

- `readOnly` 下不暴露 shell；依赖闭包不得反向引入 shell/job_start。
- `job_output` 是只读 consumer，可作为 shell/job_start 的 required companion。
- `job_cancel` / `job_control` / `job_input` / `job_monitor` 不得自动补入；它们需要 `background_task` 能力。
- local 显式 deny `job_output` + 启用 shell：启动失败，错误文本应指出：
  - 哪个 producer 需要它；
  - 为什么需要（promoted result）；
  - 如何修复（允许 `job_output` 或禁用 shell）。
- remote 当前没有独立 deny 列表；其对应负向校验是 required companion 无法建立（例如 jobs.root 不可用）或未来新增 deny 配置后复用同一错误模型。

---

## 7. Local aicli 接入

### 7.1 buildLocalChatToolPolicy

本地工具策略生成在：

```text
buildLocalChatToolPolicy
  -> session.ToolPolicy（显式 allowlist）
  -> 或 toolSurface + broker definitions 合成
  -> ApplyPermissionsOverlayToPolicy
```

需要增加：

```text
after overlay / after synthesized allowlist:
  surface = ResolveDependencies(
    base = allowed tools,
    denied = policy.DeniedTools,
    readOnly = policy.ReadOnly,
    disabled = session.DisableTools,
  )
  apply resolved tools to policy.AllowedTools
```

要求：

- 显式 `DeniedTools` 仍优先；required companion 被 deny 时 fail closed。
- 合成默认面保持现状：broker definitions 已含 job tools。
- 显式 allowlist 只允许 shell 时，自动补 `job_output`，不自动补 `job_cancel`。
- `DisableTools` 不执行闭包，保持空工具面。
- `Background == nil` 时：
  - 若 shell 仍可见，视为装配错误并 fail closed；
  - 不注册一个运行时报 `job_runtime_missing` 的 shell，也不静默删除 shell；
  - 错误应指出 shell/job_start 需要共享 background manager。

### 7.2 本地 host 的 manager 校验

`initializeLocalChatRuntimeHost` 已经：

- 构造 `backgroundManager`；
- 给 toolManager 注入 `ShellJobRuntime`；
- 给 broker 绑定 `Background`。

需要在同一装配点增加：

```text
if effective tools contain shell/job_start:
    require backgroundManager != nil
    require broker != nil
    require job_output is in effective policy
```

这样可以保证 local 和 remote 使用同一条不变量，而不是依赖 model 在运行时发现。

### 7.3 动态工具面

`syncLocalChatToolPolicyAllowlist` 每轮会合并 live tool surface。需要确保：

- dependency closure 是幂等的；
- 已 auto-expose 的 `job_output` 不因后续 toolSurface 刷新被移除；
- 显式 deny 永远不被重新加回；
- 依赖解析结果写入同一 policy 指针，模型面与执行面一致。

### 7.4 子代理工具面继承

这是原稿的覆盖缺口：§7.1–§7.3 只覆盖了主会话的策略合成，但 local 还有第三条装配路径——**子代理工具面的派生**。

相关实现：

- `backend/internal/agent/tool_vocabulary.go:298-303`：`finalizeChildToolSurface` 在 `ToolsWhitelist` 非空时调用 `appendContractualToolNames`；
- `backend/internal/agent/tool_vocabulary.go:305-320`：`appendContractualToolNames` 只读 `policy.ContractualTools`，并逐项 `DeniesTool` 跳过；
- `backend/internal/agent/contractual_tool_test.go`：已有"显式 deny 仍压制契约授予"的既有测试。

风险：父会话允许 `shell`、子代理白名单只写 `shell` 时，如果 `job_output` 没有被表达进 `ContractualTools`，子代理会拿到一个新的断链面——与主会话刚修好的问题同形。

要求：

- dependency closure 的结果必须**在派生 subagent policy 之前**完成，使 `policy.ContractualTools` 已包含 `job_output`；
- 这样 `appendContractualToolNames` 无需改动即可正确补入，`DeniesTool` 优先语义也自动成立；
- 若无法保证顺序，则 `finalizeChildToolSurface` 必须显式调用同一 resolver，不得在 agent 包内二次实现依赖逻辑；
- `test`: 父 policy 含 `shell`、子任务 `ToolsWhitelist=["shell"]` 时，子代理可见 `job_output`；父 policy deny `job_output` 时子代理装配失败而非静默缺工具。

### 7.5 动态刷新与 executor 的两面一致性

`syncLocalChatToolPolicyAllowlist` 只更新**授权面**，模型可见面可能由另一份 registry 快照生成。原稿 §7.3 的"写入同一 policy 指针"不足以覆盖这一点，需补一条显式校验：

- 刷新后断言"resolver 输出 == 模型可见工具名集合 == executor 允许集合"三者一致；
- 不一致时记 `surface.dependency.desync` 并按 fail-closed 处理（拒绝 shell 而非放行半可用面）。

---

## 8. 兼容性与迁移

### 8.1 行为变化

| 场景 | 当前 | 目标 |
|---|---|---|
| remote `profile: all` + `tools: ["shell"]` | shell only，timeout 断链 | shell + job_output |
| remote `profile: custom` + `tools: ["shell"]` | shell only | shell + job_output |
| local `--allow-tool shell` | shell only | shell + job_output |
| local explicit deny job_output + shell | 运行期断链 | 启动/装配失败，给出可操作错误 |
| profile all/ops + jobs.root | 已含 job tools | 保持并经过统一闭包 |
| readOnly | 无 shell | 保持无 shell |
| DisableTools | 空面 | 保持空面 |

### 8.2 不保留双轨

不新增“legacy dependency mode”或“auto_expose=false by default”的兼容开关。按项目约束直接切换：

- 依赖闭包成为唯一 surface 解析路径；
- 旧的 profile 手工 jobTools 拼接删除，改为 registry 驱动；
- 旧测试若固定“显式 allowlist 绝不扩宽”需区分：
  - 通用工具不被扩宽；
  - required result companion 是显式例外，需更新测试语义。

### 8.3 文档

需要同步更新：

- `docs/user-guide/aicli-remote-tools.md`
- `docs/aicli/exec.md` / `docs/aicli/chat` 中 `--allow-tool` 语义
- remote-tools profile 说明
- tool registry 契约测试/开发文档

需要新增/明确说明的语义（否则用户会遇到"我明明没允许 `job_output`"的困惑）：

1. `--allow-tool shell` 会隐式带入 `job_output`，这是 required companion 语义，不是权限扩权；
2. `--deny-tool job_output` 与 `shell` 同时存在会导致装配失败，修复方式是二者取一；
3. remote `tools` allowlist 是精确字符串匹配、不做别名归一（`tools:["bash"]` 不等于 `shell`），需在 user guide 显式写出；
4. `toolregistry` 的 `Entry.Contractual` 注释补一句"条件性契约见 `Entry.Dependencies`"，避免后续读者误以为存在两套并行机制（见 §3.4）；
5. `docs/plan/shell-timeout-job-promotion-core-design-20261029.md` 需回链本文，声明"结果依赖的装配规则由本文定义"。

---

## 9. 测试矩阵

### 9.1 registry / resolver 单元

1. shell -> job_output 自动加入。
2. job_start -> job_output 自动加入。
3. job_output 不反向引入 shell/job_start。
4. shell + deny job_output -> required error。
5. shell + readOnly -> shell 不存在，不自动加 job_output。
6. custom `tools:["shell"]` -> closure 加入 job_output。
7. alias `task_output` 归一后识别为 job_output。**注意**：该用例只在 local 侧成立；remote 的 `tools` allowlist 是精确字符串匹配（`remotetools/tools.go:103-111`），不调用 `toolregistry.Normalize`，因此 `tools:["bash"]` 目前既不匹配 `shell`，也不触发闭包（见 §9.2 第 10 项）。
8. 幂等：重复 resolve 不重复 auto-expose。
9. DAG / 无环校验。
10. `aicli_exec` 不产生任何依赖、不触发 `job_output` 自动暴露（钉死 §3.2.1 的结论，防止后人按 `ShellLike` 误加依赖）。
11. recommended 项在目标面不存在该工具时不产生告警（`job_search` 在 remote 面静默，见 §3.2.2）。
12. `Entry.Contractual` 与 `Dependencies[].AutoExpose` 并存时互不干扰：`artifact_read` 的契约授予不受依赖图影响，反之亦然（见 §3.4）。

### 9.2 remote-tools

1. `profile: all` / `tools:["shell"]` 的 effective tools 包含 job_output。
2. `profile: custom` 仅 shell 的 effective tools 包含 job_output。
3. MCP `tools/list` 包含 `remote_<target>__job_output`。
4. jobs.root 不可用或缺少 job runtime 时构建失败，错误含 producer 和 reason；若未来增加 remote deny，同样走同一校验。
5. shell timeout 返回的 next_action 使用 advertised 名称。
6. job_start body 使用 advertised 名称。
7. 现有 `TestProfileAllAddsJobToolsWhenStoreConfigured` 保持通过。
8. `TestReadOnlyProfileDoesNotExposeJobTools` 保持通过。
9. 新增 `TestRemoteShellOnlyAutoExposesJobOutput`。
10. `tools:["bash"]`（shell 的别名）当前**不生效**：`enabledTools` 的 allowlist 是精确字符串匹配（`remotetools/tools.go:103-111`），不做 `toolregistry.Normalize`。本项需明确：按 §9.1 第 7 项，若本期不在 remote 引入归一，则此处断言"`tools:["bash"]` 得到空工具面（无 shell、无 job_output）"并把该行为写入 user guide；若引入归一，则断言与 local 一致。**不可两处都不断言**。
11. recommended 不告警：remote 面无 `job_search` 时，`job_start` 不产生 missing-recommended 告警（§3.2.2）。

### 9.3 local aicli

1. `buildLocalChatToolPolicy` + explicit allow shell -> policy allows job_output。
2. `--allow-tool shell` 的实际 executor policy 包含 job_output。
3. `--deny-tool job_output` + shell -> 装配错误。
4. `DisableTools` 不产生任何 job 工具。
5. `Background == nil` + shell -> 装配失败（fail closed），错误含 manager 依赖。
6. `syncLocalChatToolPolicyAllowlist` 反复调用幂等。
7. 子代理继承：父 policy 含 `shell`、子任务 `ToolsWhitelist=["shell"]` -> 子代理可见 `job_output`（走 `appendContractualToolNames`，见 §7.4）。
8. 子代理负向：父 policy deny `job_output` + 含 `shell` -> 子代理装配失败或父面构建失败（deny 不被绕过）。
9. 两面一致性：resolver 输出 / 模型可见工具名 / executor 允许集合三者一致；人为制造不一致时 `surface.dependency.desync` 被记录且 shell 被拒（§7.5）。
10. `aicli_exec` 在 `--allow-tool aicli_exec` 下不因依赖补入 `job_output`（§3.2.1 的 local 侧镜像）。

### 9.4 端到端

1. remote MCP：
   - 调用 `remote_*__shell`，命令超时；
   - 从结果提取 job id；
   - 调用 `remote_*__job_output`；
   - 断言完成输出/exit code。
2. local aicli：
   - `aicli exec --enable-tools --allow-tool shell` 或当前会话等价策略；
   - 调用 shell 产生 promoted job；
   - 调用 job_output 读取；
   - 断言 job_output 在有效工具面。
3. 负向：
   - 只允许 shell、显式 deny job_output；
   - 断言启动失败且错误可操作。

### 9.5 回归

- `go test ./internal/toolregistry`
- `go test ./internal/remotetools`
- `go test ./internal/tools`
- `go test ./internal/toolbroker`
- `go test ./cmd/aicli/commands`
- `go test ./internal/policy`
- `go test ./internal/toolkit/tools`
- `go test ./internal/shelljob`
- `go test ./internal/background`

---

## 10. 分阶段实施

### P0：方案冻结（本文件）——已完成 2026-10-30

- 已确认依赖模型、profile/custom 语义、`job_cancel` 是否自动暴露（D1–D3）。
- 已确认 metadata 是 direct canonical tool 还是 capability 层（D4）。
- 已确认显式 deny 的失败策略（D8）。
- 已确认与既有 `Contractual` 机制的关系（D10）。

**P0 的第一件代码工作（先于 P1）**：

- 打通授权路径 `AutoExpose` → `policy.ContractualTools`，不改 `AllowTool` 判定顺序。
- 落地 §9.1 第 12 项测试（`Contractual` 与 `AutoExpose` 互不干扰）。
- 验证 §13.3 的三条硬约束：(a) 空 allowlist 不重启、(b) 写入早于 subagent 派生、(c) 不依赖 `IsContractual` 兜底。
- 此项先行的原因：D10 触碰共享授权路径，blast radius 覆盖既有 `artifact_read`，地基错则 P1–P4 返工。

### P1：registry 元数据 + resolver —— 已完成

- `toolregistry` 新增 `DependencyMode`（`required` / `recommended`）与 `ToolDependency`（`Tool` / `Mode` / `AutoExpose` / `Reason`），`Entry` 增加 `Dependencies` 字段。
- `shell` 声明 `job_output`（required + auto-expose）、`job_cancel` / `job_list`（recommended）；`job_start` 声明 `job_output`（required + auto-expose）。
- `aicli_exec` 显式不声明依赖，并在 `Entry` 上方注释写明判据（§3.2.1）。
- 解析器落在 `internal/toolsurface/dependency.go`：`ResolveDependencies` / `ResolveContractualTools` / `DependenciesOf`。
- 单测：`internal/toolsurface/dependency_test.go` 13 项全通过。

### P2：remote-tools / MCP —— 已完成

- `enabledTools()` 拆为 `resolveEnabledTools()`（含解析详情）+ `enabledToolsBase()`（profile/allowlist 基线），依赖闭包在装配点执行。
- 采用 `LiteralNames: true`：remote 的 tools allowlist 是精确匹配，不引入别名归一（D12）。
- fail-closed：`buildRegistry` 里 required 依赖无法满足时拒绝构建，错误经 `errors.Join` 汇总。
- 测试：`internal/remotetools/dependency_test.go` 3 项（shell 单独也能补出 `job_output`、无 shell 不凭空补入、read-only 不含 `job_output`）。

### P3：local aicli —— 已完成

- `buildLocalChatToolPolicy` 末尾调用 `applyLocalToolSurfaceDependencies`。
- 授权经 `policy.GrantConditionalContractualTools` 并入 `ContractualTools`，未新增 `AllowTool` 分支。
- 失败策略与 remote 有意不同：local 该函数无 error 通道，改为**撤掉断链生产者**并循环重解析至稳定，避免整面清空。
- 测试：`internal/policy/conditional_contract_test.go` 5 项 + `internal/agent/conditional_contract_inherit_test.go` 3 项（子代理继承、deny 压制、空白名单不复活）。

### P4：文档与观测 —— 部分完成

- 已在 registry 字段注释与本文记录依赖语义、判据与三条硬约束。
- 待补：user guide / exec / profile 的正文说明，以及 doctor / banner 的具体输出接线（字段名已定于 §5.4）。

### P5：验收与回归 —— 见 §14 实施记录

### 10.1 上线与回退

原稿缺少存量配置的迁移窗口与回退路径，补如下。

**存量风险面**（这些配置今天能启动，切换后会改变行为）：

| 存量配置 | 切换后行为 | 风险 |
|---|---|---|
| remote `profile: custom` + `tools:["shell"]` | 多出 `job_output`，`tools/list` 变化 | 客户端若缓存了 tools/list 会看到新增项；一般不破坏调用 |
| local `--allow-tool shell` | 执行面多出 `job_output` | 与"最小授权"预期不符者需被告知 |
| local `--deny-tool job_output` + `shell` | **从可用变为启动失败** | 唯一的破坏性变更 |

**分步策略**：

1. **先观测后生效**（P1–P2 之间）：resolver 以 dry-run 模式运行，只写 `surface.dependency.*` 日志、不改工具面。用真实部署跑一轮，收集会被影响的配置清单。
2. **告警期**（P2 完成后）：对"deny required companion 将导致失败"的配置在 `doctor` 与启动 banner 打 WARN，但暂不 fail-closed，给一个版本的迁移窗口。
3. **生效**（P4 之后）：启用 fail-closed。此时存量破坏性配置应已为零，`doctor` 的检查项转为阻断。

**回退路径**：

- 代码回退：resolver 的调用点应集中在一处（`enabledTools` / `buildLocalChatToolPolicy`），回退时把该调用变为 pass-through 即可恢复旧行为，**不引入需要同时回退的开关**；
- 数据面不涉及迁移：`jobs.root` 与既有 job 记录格式不变；
- 若需紧急关闭：仅回退 P2/P3 的接线，保留 P1 的 registry 元数据（元数据本身无副作用）。

**明确不做**：不新增 `surface.dependency.mode: off|warn|enforce` 运行时开关常驻。它只作为上述分步策略期间的临时构建标记，生效后删除——否则会退化成 §8.2 禁止的"双轨"。

---

## 11. 验收标准

1. 任何 effective surface 中只要有 `shell` 或 `job_start`，就一定有 `job_output`；否则构建失败。
2. local `job_output` 被显式 deny 时，不允许通过隐式补入绕过 deny；remote 的负向配置若未来补齐，必须复用同一规则。
3. `job_cancel` 不会因为 shell 自动获得；控制权限仍由 profile/allowlist 决定。
4. remote MCP `tools/list` 中 advertised name 与 `next_action` 完全一致（含 prefix）。
5. local `--allow-tool shell` 的实际 policy 和模型可见工具面都包含 `job_output`。
6. profile all/ops/custom、tools allowlist、readOnly、DisableTools 均有表驱动测试。
7. 不新增第二套 job runtime、第二套 profile 关系或第二套工具名单。
8. shell 的 `Submit -> Wait -> Read -> Forget` 语义不变。
9. `aicli_exec` 不被声明为 producer，也不因它触发 `job_output` 自动暴露；未来新增 shell 类工具按 §3.2.1 判据逐个确认。
10. 子代理工具面与主会话一致：父面含 `shell` 时子代理也能消费 promoted 结果（§7.4）。
11. `AutoExpose` 的补入结果通过 `policy.ContractualTools` 生效，`AllowTool` 判定顺序未新增分支（§3.4）。
12. resolver 输出 / 模型可见工具名 / executor 允许集合三者一致；不一致时 fail-closed 并记 `surface.dependency.desync`（§7.5）。
13. recommended 项在缺少实现的宿主面上不产生告警（§3.2.2）。

---

## 12. 代码索引

### 现有实现

- `backend/internal/shelljob/runtime.go:118-181`：唯一 shell job 状态机
- `backend/internal/toolkit/tools/bash.go:354-370`：local shell job-first 入口
- `backend/internal/toolkit/tools/bash.go:473-483`：promoted next_action
- `backend/internal/remotetools/shell.go:135-155`：remote shell job runtime 依赖
- `backend/internal/remotetools/shell.go:221-257`：remote promoted 输出和 next_action
- `backend/internal/remotetools/jobs_tools.go:126-190`：job_start
- `backend/internal/remotetools/tools.go:33-112`：profile/tools/readOnly 有效面
- `backend/internal/remotetools/registry.go:90-99`：job runtime 构造条件
- `backend/internal/remotetools/registry.go:770-790`：configNeedsJobRuntime
- `backend/internal/remotetools/registry.go:37-66`：MCP prefix
- `backend/internal/toolregistry/registry.go:74-120`：Entry 身份字段
- `backend/internal/toolregistry/registry.go:198-209`：job 家族声明
- `backend/internal/policy/tool_policy.go:147-175`：allowlist / deny / readOnly
- `backend/cmd/aicli/commands/chat_actor_host.go:2909-2954`：local policy 合成
- `backend/cmd/aicli/commands/chat_actor_host.go:2584-2593`：broker Background 绑定
- `backend/internal/toolregistry/registry.go:90-97`：`Entry.Contractual`（无条件运行时契约，见 §3.4）
- `backend/internal/toolregistry/registry.go:298-302`：`ContractualNames()` 契约名单真源
- `backend/internal/policy/tool_policy.go:34-38`：`ContractualTools` 字段
- `backend/internal/policy/tool_policy.go:142-201`：`AllowTool` 判定顺序与 `contractualToolAllowed`
- `backend/internal/agent/tool_vocabulary.go:298-320`：子代理白名单派生与契约工具补入（见 §7.4）
- `backend/internal/toolkit/tools/aicli_exec.go:230-271`：`aicli_exec` 超时即终态失败，非 producer（见 §3.2.1）
- `backend/internal/toolregistry/registry.go:144`：`aicli_exec` 的 `ShellLike` 声明
- `backend/internal/remotetools/jobs_tools.go:26`：remote 侧 jobTools 实际集合（无 `job_search`）

### 计划改动

- `backend/internal/toolregistry/registry.go`：Entry.Dependencies
- `backend/internal/toolsurface/`：依赖闭包 resolver + tests
- `backend/internal/remotetools/tools.go`：effective surface resolver
- `backend/internal/remotetools/registry.go`：effective name resolver / validation
- `backend/internal/remotetools/shell.go`：next_action 使用 effective name
- `backend/internal/remotetools/jobs_tools.go`：body/metadata 使用 effective name
- `backend/cmd/aicli/commands/chat_actor_host.go`：local dependency closure
- `backend/cmd/aicli/commands/chat_tool_policy_sync.go`：动态面幂等
- 相关 `_test.go` 与 docs

---

## 13. 审查结论与决策确认

| # | 决策 | 审查结论 | 状态 |
|---|---|---|---|
| D1 | `job_output` 是否 required + auto-expose | 是。只读 companion，风险最低 | **已确认** |
| D2 | `job_cancel` 是否自动补 | 否。作为 recommended，由 `background_task` 权限独立控制 | **已确认** |
| D3 | `ProfileCustom` / `tools:["shell"]` 的精确列表语义 | required companion 作为隐式成员自动补入并记录审计；显式 deny 则 surface 构建失败。**注意 `tools:[]` 仍为空面**（见 §13.3 D10-b 约束 a） | **已确认** |
| D4 | 依赖元数据用 direct canonical name 还是 capability 层 | 本期 direct canonical name；capability 层只保留扩展点 | **已确认** |
| D5 | MCP 是否把依赖元数据暴露给客户端 | 不暴露；服务端解析最终 tools/list，`next_action` 使用 advertised name | **已确认** |
| D6 | `job_list` / `job_search` 是否自动补 | 否。作为 recommended，避免控制面扩权 | **已确认** |
| D7 | resolver 落点 | 放在 `internal/toolsurface`；`internal/toolregistry` 只放声明 | **已确认** |
| D8 | local required companion 被显式 deny | effective surface 构建失败（fail closed），不静默删除 producer；remote 未来若加 deny 复用同一规则 | **已确认** |
| D9 | local `Background == nil` 且 shell 可见 | 装配失败（fail closed），错误指出共享 manager 缺失 | **已确认** |
| D10 | 与既有 `Contractual` 契约机制的关系 | 不合并字段，但 `AutoExpose` 结果必须表达为 `policy.ContractualTools` 成员并复用 `AllowTool` 既有判定顺序（§3.4）。**P0 首个动作** | **已确认（最高优先级）** |
| D11 | `aicli_exec` 是否算 producer | 否。超时即终态失败，不返回 job reference，不声明依赖（§3.2.1） | **已确认** |
| D12 | remote `tools` allowlist 是否引入别名归一 | 本期**不引入**，但要按 §9.2 第 10 项钉死现状并写入 user guide；引入归一列为 P2 之后的独立改动 | **已确认** |

### 13.1 本轮自审修正

1. 初稿把 resolver 放在 `toolregistry` 或 `toolsurface` 二选一；自审后收敛为 **toolregistry 只声明、toolsurface 做装配**，与两个包现有职责边界一致。
2. 初稿对 `ProfileCustom` 的可选项描述不够明确；自审后直接切换为“required companion 隐式补入，不接受则显式 deny 并失败”，不留兼容开关。
3. 初稿对 `Background == nil` 允许“移除 shell 或失败”二选一；自审后收敛为 fail-closed，不静默改变用户工具面。
4. 初稿遗漏 remote prefix 与 `next_action` 的一致性；已加入 §1.3、§6.4 与 §9.2 测试项。
5. 初稿把 envd 作为具体限制写出；自审后改为按 backend/transport 既有约束返回明确错误，避免把未核实的假设固化进方案。

### 13.1.1 第二轮完整性审查修正

本轮针对"方案是否完整"做了一次代码核验审查（逐条比对引用行号与真实实现），修正如下：

6. **补 `aicli_exec` 的显式结论**。原角色表只列 `shell` / `job_start`，遗漏了同样 `ShellLike` 的 `aicli_exec`（`toolregistry/registry.go:144`）。核实其超时走 `context.DeadlineExceeded` 失败分支、不返回 job id（`toolkit/tools/aicli_exec.go:230-231, 265-271`），结论是**不是 producer**，已写入 §3.2.1 并加判据，防止后人按 `ShellLike` 误加依赖。
7. **补与 `Contractual` 机制的关系定调**。既有 `Entry.Contractual` / `policy.ContractualTools` / `appendContractualToolNames` 已实现"省略即补入、deny 优先"的语义，与原稿 `AutoExpose` 高度同构，原稿却未讨论，与 §8.2 和验收第 7 条自相矛盾。已在 §3.4 定调为"条件性契约"与"无条件契约"并存但共用授权路径，并新增 D10。
8. **补子代理工具面继承**。原 §7 只覆盖主会话合成，遗漏 `finalizeChildToolSurface` / `appendContractualToolNames`（`agent/tool_vocabulary.go:298-320`）这条派生路径，子代理会重新出现同形断链。已新增 §7.4 与 §9.3 第 7–8 项。
9. **修正 `job_search` 的声明范围**。原 §3.2 给 `job_start` 声明 `recommended: job_search`，但 remote `jobTools` 仅 `start/output/cancel/list`（`jobs_tools.go:26`）。已从共享声明移除并新增 §3.2.2 说明 recommended 需按宿主面裁剪。
10. **补充 remote 别名归一的现实约束**。原 §9.1 第 7 项只测 `task_output` 归一，但 remote allowlist 是精确匹配（`remotetools/tools.go:103-111`），`tools:["bash"]` 实际不生效。已在 §9.1/§9.2 双向钉死，并新增 D12。
11. **补上线与回退策略**。原稿只有阶段性改动清单，无存量配置迁移窗口与回退路径。已新增 §10.1，明确"dry-run → 告警期 → 生效"三步与单一回退点。
12. **修正观测落地与一处行号**。§5.4 原只给字段名未指定载体，已补载体映射表与脱敏约束；§1.3 引用的 `registry.go:773-780` 修正为 `773-788`。
13. **补 §7.5 两面一致性校验**。`syncLocalChatToolPolicyAllowlist` 只更新授权面，可能与模型可见面脱节，原 §7.3 的"同一 policy 指针"不足以保证一致，已加断言与 `surface.dependency.desync` 处理。

### 13.2 实施门槛

- D1–D12 已于 2026-10-30 全部确认（依据见 §13.3），可进入 P0。
- **D10 是 P0 的第一个动作**：先完成授权路径接线（`AutoExpose` → `policy.ContractualTools`）并落地 §9.1 第 12 项测试，再开始 §4 的元数据落地。理由见 §13.3。
- 保留的例外：若后续决定 `job_cancel` 也必须 required，需额外评估 `background_task` 权限、审批复用和 read-only 语义，不能直接套用 `job_output` 的只读自动补入策略（本期不做，见 D2）。

### 13.3 决策确认依据（最佳实践核定）

确认所依据的原则，按优先级排列：

1. **单一真源**优先于重复声明——依赖关系只在一处声明、一处解析；
2. **复用既有不变量**优先于新增并行机制——不得在既有授权链之外再造一套；
3. **最小授权**——自动补入只限只读 companion，控制面一律不自动扩权；
4. **fail-closed** 优于静默降级——宁可装配失败，也不静默改变用户工具面；
5. **结论须有代码证据**——未核实的事实一律标注为待验证，不写入方案；
6. 变更须**可观测、可回退**。

逐项依据：

| # | 依据 |
|---|---|
| D1 | 原则 3。`job_output` 是 Essential 且只读（`toolregistry/registry.go:199-206`），自动补入不扩权 |
| D2 | 原则 3。终止属控制面，与 `background_task` 权限绑定，不应由依赖图隐式授予 |
| D3 | 原则 3+4。隐式补入需可审计；显式 deny 恒优先（`tool_policy.go:142`），冲突即失败而非静默取舍 |
| D4 | 原则 1 + YAGNI。当前仅一组依赖，延迟抽象；registry 已有 canonical name + aliases 可用 |
| D5 | 原则 6。依赖是服务端装配细节，不外泄；但 `next_action` 必须用 advertised name 才真正可执行 |
| D6 | 原则 3。发现类工具作为 recommended，不自动补 |
| D7 | 原则 1。`toolregistry` 只做声明，`toolsurface` 做装配，与两包既有职责边界一致 |
| D8 | 原则 4。静默删除 producer 会让工具面不可预测，且掩盖配置错误 |
| D9 | 原则 4。同上 |
| D10 | 原则 2。既有 `ContractualTools` 已实现"省略即补入、deny 优先"，复用即可，不新增判定分支 |
| D11 | 原则 5。按返回契约判定而非 `ShellLike` 标记判定（`aicli_exec.go:230-271`） |
| D12 | 原则 6。先钉死现状、控制本次变更面，归一作为独立演进 |

#### D10 为何优先级最高（含三条新核实约束）

**优先理由**：D10 是唯一会改动**共享授权代码路径**的决策，影响面包含既有 `artifact_read` 等契约工具，blast radius 超出本特性本身。若地基定错，P1–P4 全部返工，因此必须先落定。

**可行性已核实**：`ContractualTools` 是 `ToolExecutionPolicy` 上的 `map[string]bool` 字段（`tool_policy.go:38`），由 `buildContractualToolsMap()` 从 `toolregistry.ContractualNames()` 初始化（`tool_policy.go:203-210`），并随 `Clone` 一并克隆（`tool_policy.go:616`）。因此"per-request 动态补入"**不需要改结构**，只需在派生之前写入该 map。

**本次新核实的三条硬约束**（补充 §3.4，实现时必须遵守）：

- **(a) 空 allowlist 语义**：`contractualToolAllowed` 在 `!AllowlistEnabled || len(AllowedTools) == 0` 时直接返回 `false`（`tool_policy.go:192-195`）。即契约只"填补遗漏项"，**不会"重启被完全关闭的面"**。故 D3 中 `tools:[]` 仍得到空工具面，这是既有设计语义，不是 bug，不得在实现中"顺手修复"。
- **(b) 顺序约束**：因 `Clone` 会复制 `ContractualTools`（`:616`），resolver 必须在派生 subagent policy **之前**写入，否则子代理继承的是旧集合（与 §7.4 一致）。
- **(c) 兜底路径盲区**：`ContractualTools` 为空时会回退 `toolregistry.IsContractual`（`:196-198`）。条件性契约若只写进 `Entry.Dependencies`，该兜底路径**不会感知**，因此必须显式写入 policy 的 map，不可依赖兜底。
---

## 14. 实施记录（2026-10-30）

### 14.1 代码落点

| 层 | 文件 | 内容 |
|---|---|---|
| 声明 | `backend/internal/toolregistry/registry.go` | `DependencyMode`、`ToolDependency`、`Entry.Dependencies`；`shell` / `job_start` 声明；`aicli_exec` 显式不声明（附判据注释） |
| 装配 | `backend/internal/toolsurface/dependency.go` | `ResolveDependencies`（纯函数闭包）、`ResolveContractualTools`（授权落地面）、`canAutoExpose` / `readOnlyCompatible`（安全闸）、`DependenciesOf` |
| 授权 | `backend/internal/policy/tool_policy.go` | `GrantConditionalContractualTools`（写入 `ContractualTools`，deny 优先） |
| remote | `backend/internal/remotetools/tools.go` / `registry.go` | `enabledTools` → `resolveEnabledTools` + `enabledToolsBase`；`LiteralNames`；构建期 fail-closed |
| local | `backend/cmd/aicli/commands/chat_actor_host.go` | `applyLocalToolSurfaceDependencies`（闭包 + 撤断链生产者 + 循环至稳定） |

### 14.2 与方案的偏差（均为有意，已记录理由）

1. **local 失败策略不用 fail-closed，改为撤生产者**。`buildLocalChatToolPolicy` 无 error 通道，整面清空对用户过于粗暴。撤掉断链生产者能保证"不把能起 job 却读不到结果的工具交给模型"，同时会话仍可用。remote 侧仍为严格 fail-closed，因为它是"构建一个面"，失败即拒绝构建。
2. **remote 使用 `LiteralNames: true`**。落实 D12：allowlist 精确匹配语义不变，只在补入时按声明的字面名写入。
3. **`ResolveDependencies` 的 `Disabled` 分支直接返回空面**。防止闭包复活 `DisableTools` 场景。

### 14.3 测试证据

| 包 | 测试文件 | 用例数 | 结果 |
|---|---|---|---|
| `internal/toolsurface` | `dependency_test.go` | 13 | PASS（含 12 项 §9.1 矩阵 + Disabled/LiteralNames） |
| `internal/policy` | `conditional_contract_test.go` | 5 | PASS（授予 / deny 压制 / 空面不复活 / 别名归一 / 不干扰无条件契约） |
| `internal/remotetools` | `dependency_test.go` | 3 | PASS（shell 单独补出 job_output / 无 shell 不补 / read-only 不含） |
| `internal/agent` | `conditional_contract_inherit_test.go` | 3 | PASS（子代理继承 / deny 压制 / 空白名单不复活） |
| `cmd/aicli/commands` | `chat_tool_surface_dependency_test.go` | 6 | PASS（补入 companion / 撤断链生产者 / 空面不复活 / 全允许跳过 / 幂等 / 摘要文本） |
| `cmd/aicli-remote-tools` | 既有 `TestDoctor*` | — | PASS（3.540s，含新增 `auto-exposed` 行） |

回归：`go build ./...` 全仓通过；`go test ./internal/toolsurface/ ./internal/policy/ ./internal/toolregistry/` 通过；`go test ./internal/remotetools/ -run TestRemote` 通过（43.19s）。`gofmt -l` 对新增/修改文件无输出；`go vet` 无告警。

> 备注：并行跑多个包时 `internal/agent` 的 `TestReActLoop_RunWithSession_AutoCompactionRecoveryContinuesAfterPromptPreflightFailure` 曾偶发失败，单独重跑通过；本次未改动该路径，判定为既有偶发用例，非本特性引入。

### 14.4 观测与文档（P4 收尾，2026-10-30 补）

已完成：

- **remote doctor 接线**：`DoctorReport` 增加 `autoExposedTools` 字段；`printDoctorReport` 在存在补入项时输出 `auto-exposed` 行；warnings 并入报告。新增导出接口 `EffectiveTools` / `AutoExposedTools` / `ToolSurfaceWarnings`，使诊断包不必了解解析细节。
- **user guide（remote）**：`docs/user-guide/aicli-remote-tools.md` 新增 §5.5「依赖自动补入」，并在 §5.1 规则列表补一条交叉引用；写明三条不扩权保证、`doctor` 输出样例与"`tools:["bash"]` 不生效"的提醒。
- **观测测试**：`TestRemoteAutoExposedToolsAreObservable`、`TestRemoteAutoExposedEmptyWithoutProducer` 通过。

- **本地 `/tools` 观测**：`toolsurface` 观测层新增 `required_by` 元数据键，列出会把该工具自动带入的生产者（仅 `AutoExpose` 关系，不含 recommended）；本地 `/tools` 与 runtime-server 的 `GET /api/runtime/tools` 共用这份口径。测试：`TestEnrichMarksAutoExposeProducers` / `...IgnoresRecommendedOnlyRelations` / `...NormalizesAlias`。
  - 语义边界：`required_by` 是**静态登记事实**（"shell 会把 job_output 带进来"），不是本次请求的授权结论；这与既有 `contractual` / `contract_source` 的分工一致，不新增第二套判定。

- **本地启动摘要**：`applyLocalToolSurfaceDependencies` 改为返回 `localToolSurfaceSummary`（`Granted` / `Withdrawn` / `Warnings` / `Errors`），`buildLocalChatToolPolicy` 在**实际偏离用户 allowlist** 时输出一行 `tool surface dependency resolved: auto-exposed job_output`。默认日志级别为 Info，用户能直接看到。
  - 同时修掉一个真问题：闭包 `Base` 现在包含 `policy.ContractualTools`，否则每轮都会把已授予的 companion 当成"新补入"，导致摘要重复且闭包不幂等（原实现只有最终状态正确，观测失真）。

仍未完成（有意保留）：

- **§10.1 三步上线**：dry-run 与告警期属**部署节奏**，不是代码开关。本文已明确禁止常驻 `mode: off|warn|enforce`（会退化成 §8.2 禁止的双轨），因此当前直接进入生效态；存量破坏性配置（`--deny-tool job_output` + `shell`）会立即触发撤生产者 + WARN 日志。若后续确需过渡窗口，应以发布说明与版本分批实现，而非新增运行时开关。
- **`docs/aicli/permissions.md` 的 companion 说明**：已在 `--allow-tool` 精确名规则旁补充，说明 `shell` 隐式带入只读 companion `job_output`、deny 优先导致撤 shell、以及空白名单不复活。
- **§10.1 的三步上线**：dry-run 与告警期未实现，当前直接进入生效态；存量破坏性配置（`--deny-tool job_output` + `shell`）会直接触发撤生产者并打 WARN。

### 14.5 与既有 `Contractual` 机制的实证

`GrantConditionalContractualTools` 的 5 项测试确认了 §3.4 的三条不变量在真实判定链上成立：

| 不变量 | 测试 | 结果 |
|---|---|---|
| 授予后 `AllowTool` 放行 | `TestGrantConditionalContractualTools` | PASS |
| deny 恒优先（既不入契约集合，也不放行） | `TestGrantConditionalContractualToolsRespectsDeny` | PASS |
| 空 allowlist 不重启 | `TestGrantConditionalContractualToolsEmptyAllowlist` | PASS |
| 别名归一（`task_output` → `job_output`） | `TestGrantConditionalContractualToolsNormalizesAlias` | PASS |
| 不干扰无条件契约（`artifact_read`） | `TestGrantConditionalContractualToolsKeepsUnconditional` | PASS |
