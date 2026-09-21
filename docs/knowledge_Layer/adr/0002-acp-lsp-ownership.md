# ADR-0002: ACP 下 LSP 归属与能力面

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: 项目 owner
- **Gate**: `Phase4-start`
- **Reversibility**: cheap（配置默认值 + 一个 select 选项）
- **Supersedes**: `supplement/05` §2.3 的 `external_preferred`（该表述基于一个不存在的能力面）
- **Related**: `backend/internal/acp/types.go`、`doc.go`、`server.go`、`conn.go`；`backend/cmd/aicli/commands/agent_stdio_config_option.go`、`client_capability_test.go`；`supplement/05` §2.3、§4.2

---

## 1. Context

### 1.1 已核实的证据

**证据 1 — ACP v1 中不存在任何 LSP 相关能力。**

`backend/internal/acp/types.go` 暴露的能力集合为：

| 能力 | 归属 | 内容 |
|---|---|---|
| `ClientCapabilities` | client → agent | `fs.readTextFile`、`session.configOptions.boolean` |
| `AgentCapabilities` | agent → client | `loadSession`、`promptCapabilities`、`mcpCapabilities`、`sessionCapabilities{list,resume,delete,close}` |
| `PromptCapabilities` | agent → client | 提示相关 |
| `MCPCapabilities` | agent → client | MCP 相关 |
| `FileSystemCapabilities` | client → agent | 文件读取 |

**没有 LSP 能力位。** 能力门控辅助函数只有三个：
`SupportsQuestions()`、`SupportsFormElicitation()`、`SupportsBooleanConfigOptions()`。

**证据 2 — 仓库已有"能力缺失时优雅降级"的既定模式。**

`internal/acp/conn.go:67` 注释说明 host 用它来在 client 未声明某能力时优雅降级；
`server.go:211-217` 在调用方未设置能力时安装 MVP 默认值。
说明"能力协商 + 降级"是**既有工程习惯**，不是新引入的复杂度。

**证据 3 — select 类型配置项不受能力门控。**

`internal/acp/doc.go:14-15`：

> `session/set_config_option`（select-type options need no capability; boolean options are
> filtered out unless the client advertises `clientCapabilities.session.configOptions.boolean`）

**这是本 ADR 的关键杠杆**：一个 **select** 类型的选项在所有 client 上都可用，
而 **boolean** 类型需要 client 明确声明支持。把 LSP 模式做成 select，
可用最低门槛触达所有 client。

**证据 4 — `supplement/05` §2.3 的原表述无法实现。**

原文（L154）：

> 默认 `knowledge.lsp.mode = external_preferred`：先探测 editor 是否通过 MCP/ACP 提供
> LSP 能力（v1 通常没有），否则退到自管 LSP 或 parser。

由证据 1，**没有任何信号可探测**。"先探测"这个动作在 v1 无对象。
这是本 ADR 必须修正的核心缺陷。

### 1.2 问题陈述

在 ACP 场景下，editor（Zed / VS Code 等）通常**自己已经运行了 LSP**
（gopls、volar、typescript-language-server）。如果 knowledge 层再启动一份，会出现：

- 同一 `go.mod` 上两个 `gopls` 实例；
- 内存翻倍（`gopls` 对中型 Go 仓库常在 300 MB–1.5 GB）；
- 双方各自触发 `go list` / `go env`，互相拖慢；
- 文件监听风暴（两个进程各自 watch 同一棵树）；
- **两者结果可能不一致**，模型无法判断信谁。

反过来，如果 knowledge 一律不自启 LSP，代价是 `code.find_refs` / `code.callers`
只能停在 L1（parser），精度下降（见 `supplement/05` §5.3）。

### 1.3 风险不对称

| 方向 | 失败形态 | 代价 |
|---|---|---|
| 自启 LSP（可能重复） | 内存/CPU 双倍、结果冲突、构建变慢 | **高**（机器级；`04` §6 已列 SQLite 锁同类事故先例） |
| 不自启 LSP（只用 parser） | refs/callers 精度下降，标注 `source:"parser"` | **低**（`supplement/05` §5.1 已把 L1 定义为必需可用层） |

**代价不对称 → 默认取保守一侧。** 这是本 ADR 的主要判据。

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 默认不得产生重复 LSP 实例 | 在已有 gopls 的 workspace 上启动 ACP 会话，进程数不增加 |
| D2 | 必须可达所有 client | 不依赖 client 声明可选能力 |
| D3 | 用户必须能主动打开自管 LSP | 存在一个可用的会话级开关 |
| D4 | 不得引入 no-op 选项 | 每个枚举值都有可观察的行为差异 |
| D5 | 无 workspace root 时必须完全降级 | `mode=off`，`code.*` 不注册 |
| D6 | 可回退 | 改默认值或改一个枚举即可 |
| D7 | 同一进程内不得重复起同 (lang, root) 实例 | 锁文件存在且被尊重 |

---

## 3. Considered Options

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A** | ACP 默认 `knowledge.lsp.enabled=false`，完全依赖 editor 的 LSP | 零重复风险（D1） | 用户若 editor 无 LSP，knowledge 永久停在 parser，且无开关可开（违反 D3） |
| **B** | ACP 默认自管 LSP，与 TUI 相同 | 行为一致 | 违反 D1，风险最高 |
| **C** | 通过 MCP/环境变量探测 editor 是否已有 LSP | 若能实现最理想 | v1 无任何可用信号（证据 1），纯属臆测 |
| **D** | 默认 `off`，暴露 **select** 类型 `knowledge.lsp.mode ∈ {off, self}` | 满足 D1/D2/D3/D6 | 用户需主动开启（可接受） |
| **E** | 默认 `off`，暴露 **boolean** `knowledge.lsp.enabled` | 简单 | boolean 需 client 声明能力（证据 3），触达面小，违反 D2 |
| **F** | 默认 `off`，加 `external` 枚举值表示"editor 拥有 LSP" | 语义完整 | v1 中 `external` 与 `off` 行为完全相同 → no-op 选项，违反 D4 |

---

## 4. Decision

采纳 **选项 D**，并做四项配套。

### 4.1 ACP 默认

```yaml
knowledge:
  lsp:
    mode: off        # ACP 场景默认；TUI / runtime-server 默认 self
    enabled: true    # 全局总开关，mode 之上的硬闸
```

- `knowledge.lsp.enabled=false` → 任何入口都不起 LSP（逃生舱）。
- `knowledge.lsp.mode ∈ {off, self}`，**v1 只有两个值**。
- 默认值按入口区分：`runtime-server` / `aicli tui` → `self`；`aicli acp` → `off`。

### 4.2 通过 select 选项暴露

在 ACP `session/set_config_option` 路径上注册一个 **select** 类型选项：

```text
key:     knowledge.lsp.mode
type:    select
values:  [off, self]
default: off
```

- 依据证据 3，select 不受 `clientCapabilities.session.configOptions.boolean` 门控，
  **所有 client 都可见可改**（满足 D2）。
- 复用既有实现路径（`agent_stdio_config_option.go`；`server.go` 的
  `SupportsBooleanConfigOptions()` 仅用于 boolean 分支），**select 分支无需新能力协商**。

### 4.3 保留 `external` 但不实现

`external` 语义为"editor 拥有 LSP，knowledge 不得 spawn，但未来可经 MCP 委托查询"。
**v1 不提供该枚举值**，理由见 §5 的 D4 论证。
当且仅当出现"MCP LSP 委托"的真实接口时，再以新 ADR 增加该值。

### 4.4 进程内重复防护（补 D7）

无论 `mode` 取值，spawn 前必须获取锁：

```text
<workspace>/.aicli/knowledge/lsp/<language>-<sha256(project_root)[0:12]>.lock
内容: { "pid": ..., "root_uri": "...", "command": "...", "started_at": "..." }
```

- 锁新鲜（heartbeat < 30s）→ 复用该实例或直接降级到 parser（本进程不 spawn）。
- 锁过期 → 接管。
- 与 `supplement/05` §1.3 的 owner 锁**同一套机制**，不引入第二种锁语义。

> 这解决的是"同一台机器上两个 aicli ACP 会话"（例如 editor 里开了两个 workspace 窗口指向同一项目），
> 而不是"editor 的 gopls vs 我们的 gopls"——后者在 v1 无法探测（证据 1），由默认 `off` 规避。

### 4.5 无 workspace root 时

`session/new` 未携带 roots/cwd → knowledge 整体 `mode=off`（不只是 LSP），
`code.*` 不注册，模型只用 `grep` / `view`。**不得报错，不得静默用进程 cwd 兜底**（`supplement/05` §2.3）。

---

## 5. Rationale

逐条回应 Decision Drivers：

- **D1**：默认 `off` 直接消除重复风险。选项 B 被否决的**真实缺陷**是：它的失败代价是机器级的（内存/CPU），而收益只是精度提升，量级不匹配。
- **D2**：选项 E 被否决的**真实缺陷**是 `client_capability_test.go` 已经固化了"boolean 选项会被过滤"的行为——在没有该能力的 client 上，用户看不到这个开关，也就无法开启 LSP。select 没有这个限制（证据 3）。
- **D3**：`self` 值提供主动开启路径。
- **D4**：选项 F 被否决的**真实缺陷**是：v1 中 `external` 与 `off` 的行为完全一致（都不 spawn、都用 parser），用户切换后观察不到任何差异。一个切换后无变化的选项会制造"配置不生效"的错觉，是纯粹的负债。**等委托接口真实存在时再加，而不是先占位。**
- **D5**：§4.5 明确。
- **D6**：改 `mode` 默认值 = 改一行 YAML。
- **D7**：§4.4 用锁解决进程内重复。

**关于"为什么不直接跟随 editor"**：跟随需要探测信号（选项 C），而 v1 没有。
把"跟随"降级为"默认不抢 + 用户可显式开启"是当前信息量下唯一可实现的保守策略。
这不是放弃，而是**把不可判定的问题转化为可配置的问题**——这是处理信息不足的标准手法。

---

## 6. Consequences

### 6.1 Positive

- 默认不会出现双 gopls；最坏情况退化为 parser（已被 `supplement/05` §5.1 定义为必需可用层）。
- 一个 select 选项覆盖所有 client，无需任何新能力协商。
- 与 TUI/runtime-server 的默认值差异被显式记录，而不是隐含行为。
- 复用既有 owner 锁机制，无新锁语义。

### 6.2 Negative / Accepted trade-offs

- **ACP 用户默认拿不到 LSP 精度。** 主动接受：精度损失可观测（`source:"parser"`），而重复 LSP 的损失不可观测且更贵。用户可一个配置项开启。
- **各入口默认值不同**（TUI `self`，ACP `off`）。主动接受：这是有意的不一致，因为 editor 自带 LSP 的概率远高于裸终端。必须写进文档与 `/knowledge status` 输出。
- **`external` 缺席**，未来加值需要新 ADR。主动接受：换取"无 no-op 选项"。

---

## 7. Reversal Plan

- 想把 ACP 默认改为 `self`：改 YAML 默认值 + 默认装配处，成本 < 1 小时。
- 想加 `external`：新增枚举值 + 明确其与 `off` 的差异，属新增 ADR 范畴。
- 想撤销整个 LSP 能力：`knowledge.lsp.enabled=false`，运行时即可，无需发版。
- **无数据迁移**：LSP 状态表（`03.lsp_servers`）是缓存，可随时清空重建。

---

## 8. Validation

| 检查 | 形式 | 门槛 |
|---|---|---|
| 无重复实例 | 在已有 gopls 的 workspace 上开 ACP 会话，统计 `gopls` 进程数 | 不增加 |
| select 可达 | 用 `client_capability_test.go` 的"只有 select 能力"的 client 载荷，断言选项可见 | 必须通过 |
| off 生效 | `mode=off` 时 `code.find_refs` 返回 `source:"parser"` | 必须通过 |
| self 生效 | `mode=self` 时返回 `source:"lsp"`（已安装 gopls） | 必须通过 |
| 锁生效 | 两个 aicli 进程同 root，只有一个 spawn gopls | 必须通过 |
| 无 root 降级 | 无 roots 的 `session/new` 后，`code.*` 不在工具列表 | 必须通过 |
| 不阻塞 | LSP 握手超时不影响 turn 完成时间 | P95 无变化 |

---

## 9. Alternatives Rejected (and why)

| 选项 | 否决理由（一句话） |
|---|---|
| A（无开关的 off） | 用户永远无法开启，违反 D3 |
| B（默认 self） | 失败代价是机器级的，与收益量级不匹配 |
| C（探测 editor LSP） | v1 无任何可探测信号，属臆测 |
| E（boolean 开关） | boolean 受 client 能力门控，触达面小 |
| F（加 `external`） | v1 中与 `off` 行为相同，是 no-op 选项 |
| 让 knowledge 复用 editor 的 LSP 连接 | ACP 无此通道；需先有 MCP LSP 委托接口 |

---

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| MCP LSP 委托接口是否存在可行设计（决定 `external` 是否值得加） | `Phase4-start` |
| editor 侧是否存在可用的"我已启动 gopls"信号（需调研 Zed/VS Code 扩展面） | `Phase4-start` |
| ACP 默认值是否应随 `clientCapabilities` 变化 | `Phase5-start` |
| 多 workspace 窗口共享一个 project 时的 LSP 复用策略 | `Phase4-start` |
| [`../../lsp/03`](../../lsp/03-implementation-plan-and-acceptance.md) 的验收项 A1–A11 逐条关闭（含 A10 等待上限、A11 刻意差异承认） | `Phase4-start` |
