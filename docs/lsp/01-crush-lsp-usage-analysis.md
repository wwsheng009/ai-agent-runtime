# 01 — crush 项目 LSP 使用分析（参考实现）

> 最后更新：2026-09-21
> 分析对象：`E:\projects\ai\crush`（Charm 官方项目）
> 目的：把"LSP 如何配合 AI Agent"这件事拆成**可移植的机制**，而不是照抄代码。
> 本文只描述**已核实的实现事实**，不含推测；推测部分统一标注 `[推断]`。

---

## 1. 一句话结论

crush 没有把 LSP 做成"一个把诊断塞给模型的工具"，而是做成了一条**与编辑动作绑定的反馈环**：

```
Agent 调 edit/write ──► 落盘 ──► notifyLSPs(文件) ──► 语言服务器重新分析
                                       │
                                       ▼
                        getDiagnostics(文件) ──► 拼进工具返回值 ──► 模型下一轮看到
```

关键点有三个，都属于**工程约定**而非协议能力：

1. **LSP 客户端由宿主进程统一持有**（`csync.Map[string, *lsp.Client]`），工具函数只拿到这个 map 的引用，不负责启动/停止。
2. **编辑后立即通知 + 立即读诊断**，诊断作为**工具返回值的一部分**回灌，而不是单独等一次 tool call。
3. **有独立的诊断查询工具与重启工具**，用于"编辑之外的时机"（显式体检、服务器崩溃恢复）。

---

## 2. 分层结构

| 层 | 位置 | 职责 |
| --- | --- | --- |
| 配置层 | `config.LSPConfig`、`config.VariableResolver` | 声明有哪些 server、启动命令、语言/文件匹配、变量展开 |
| 生命周期层 | `internal/lsp/client.go`、`internal/app/lsp.go` | 创建、初始化、复用、重启、状态记录 |
| 协议层 | `internal/lsp/handlers.go`、`internal/lsp/util/edit.go` | 处理 server 发来的通知/请求（window/logMessage、applyEdit 等） |
| 语言映射层 | `internal/lsp/language.go` | 文件扩展名/语言 ID ↔ server 能力 |
| 工具层 | `internal/agent/tools/*.go` | 把 LSP 能力暴露成 agent 可调用的工具 + 注入诊断 |
| 提示层 | `internal/prompt/*` | 告诉模型"有 LSP 可用、什么时候用" |

---

## 3. 生命周期：谁启动、谁持有、失败怎么办

### 3.1 入口

`internal/app/lsp.go` 提供 `initLSPClients(ctx)`，逐个配置调用 `createAndStartLSPClient(...)`。
创建单个客户端走 `internal/lsp/client.go`：

```go
func New(ctx context.Context, name string, cfg config.LSPConfig, resolver config.VariableResolver) (*Client, error)
func (c *Client) Initialize(ctx context.Context, workspaceDir string) error
func (c *Client) HandlesFile(path string) bool
```

三个方法对应三件事：**构造 → 握手 → 归属判定**。

### 3.2 失败是降级，不是致命

```go
slog.Error("Failed to create LSP client for", "name", name, "error", err)
updateLSPState(name, lsp.StateError, err, nil, 0)
return
```

要点：

- LSP 不可用**不阻塞** agent 启动；只是该 server 进入 `StateError`。
- `updateLSPState(name, state, err, ...)` 把状态外化成**可观测状态**（名字、状态、错误、附加数据），说明它被 UI/诊断面板消费，而不只是日志。
- `[推断]` 由于有独立的状态查询路径，crush 才敢让 LSP 完全可选。

### 3.3 服务器自身消息

`internal/lsp/handlers.go` 把 JSON-RPC 通知分级映射到日志：

```go
case protocol.Warning: slog.Warn("LSP Server", "message", msg.Message)
case protocol.Info:    slog.Info("LSP Server", "message", msg.Message)
```

以及 `protocol.Error` / `protocol.Log` 等分支。**未做特殊处理的 server 消息不应静默丢弃**——这是排查"LSP 明明启动了却没有诊断"的第一现场。

---

## 4. 工具层：三个协作原语

`internal/agent/tools/` 下与 LSP 相关的文件：

- `diagnostics.go` + `diagnostics.md` —— 显式诊断查询工具及其模型可见说明
- `lsp_restart.go` —— 重启服务器
- `edit.go` / `write.go` / `multiedit.go` —— 写文件后触发通知与诊断
- `view.go`、`references.go` —— 读取与符号引用（配合 LSP 结果）
- 另外还有 `bash.go`、`download.go` 等与 LSP 无关的工具

### 4.1 `notifyLSPs`：只通知"管得着"的 server

```go
func notifyLSPs(ctx context.Context, lsps *csync.Map[string, *lsp.Client], filepath string) {
    if filepath == "" {
        return
    }
    for client := range lsps.Seq() {
        if !client.HandlesFile(filepath) {
            continue
        }
        // ... 通知该 client 文件已变更
    }
}
```

设计要点：

- **空路径直接返回**：防止把 `""` 当成工作区根去通知。
- **`HandlesFile` 过滤**：一个工作区可能同时挂着 gopls / tsserver / clangd，通知发错服务器既浪费又可能产生噪声诊断。
- **非阻塞遍历**：任一 server 报错不能中断其它 server 的通知。

### 4.2 `getDiagnostics`：把诊断变成可读文本

```go
func getDiagnostics(filePath string, lsps *csync.Map[string, *lsp.Client]) string {
    fileDiagnostics := []string{}
    projectDiagnostics := []string{}
    for lspName, client := range lsps.Seq() {
        // 取该 client 的诊断结果
        // 逐条转换 location URI -> 本地路径
        isCurrentFile := path == filePath
        for _, diag := range ... {
            // 按 isCurrentFile 分流到 fileDiagnostics / projectDiagnostics
        }
    }
}
```

设计要点（**这是最值得抄的部分**）：

| 机制 | 原因 |
| --- | --- |
| URI→路径转换每处都做，失败则 `continue` 并打日志 | 有些 server 会报虚拟文档/未保存缓冲区的 URI，转不了不能崩 |
| 区分 `fileDiagnostics`（当前文件）与 `projectDiagnostics`（其它文件） | 当前文件诊断"刚改的就错"要对模型强提示；跨文件结论信息量低、优先级低，混在一起会稀释注意力 |
| URI 转失败走 `slog.Error` + `continue` | 单条坏了不影响整批 |
| 返回 `string` 而非结构化对象 | 直接拼进工具返回值，无需模型再解析 JSON |

### 4.3 绑定点：编辑后立刻回灌

已核实的调用点：

```
agent/tools/edit.go:95   notifyLSPs(ctx, lspClients, params.FilePath)
agent/tools/edit.go:98   text += getDiagnostics(params.FilePath, lspClients)
agent/tools/diagnostics.go:35  notifyLSPs(ctx, lspClients, ...)
```

`edit.go` 的形状是：

```
执行编辑 → 若 IsError 直接返回 → notifyLSPs → text += getDiagnostics(...) → 返回 text
```

这条顺序意味着：**模型在一次 tool call 内就拿到了"我改坏了什么"**，不需要额外一次诊断调用。这是反馈环闭合的临界动作，也是与"只在需要时手动查诊断"的实现之间的核心差异。

### 4.4 `diagnostics.md`：工具说明里明说 LSP

`internal/agent/tools/diagnostics.md` 的 `<tips>` 段落写到：

```
- Use with other tools for comprehensive code review
- Combine with LSP client for real-time diagnostics
```

即**工具描述文件本身就是给模型的 LSP 提示位**。这解释了为什么模型会主动在编辑后用诊断工具，而不是把它当成陌生工具。

---

## 5. 参照系：与"朴素做法"的对比

| 维度 | 朴素做法 | crush 的做法 |
| --- | --- | --- |
| 启动时机 | 每次工具调用按需启动 | 宿主启动时统一初始化，复用长驻进程 |
| 归属判定 | 用统一命令处理所有文件 | `HandlesFile` 按 server 能力过滤 |
| 诊断获取 | 单独一次 tool call | 编辑返回值内联回灌 |
| 失败处理 | 报错或忽略 | 状态外化 + 降级继续 |
| 当前文件 vs 其它文件 | 不区分 | 分两个桶，分别成段 |
| 位置编码 | 一般不管 | 有转换边界约定（见 ADR-0006，本目录 02 文档沿用） |

---

## 6. 可移植结论（→ 直接支撑 `02` 文档）

按"值得移植 / 需要改造 / 不可照搬"三档：

### 值得直接移植

1. **宿主持有客户端池**，工具层只做只读遍历。
2. **`HandlesFile` 式归属判定**，通知与诊断都先过滤。
3. **编辑后立即通知 + 立即读诊断**，诊断内联进工具返回值。
4. **当前文件 / 其它文件分桶**输出。
5. **失败降级 + 状态外化**，LSP 永远可选。
6. **工具描述文件承载模型提示**。

### 需要按本项目改造

1. **`csync.Map[string, *lsp.Client]` 的并发模型**——本项目需要落到知识层的所有权模型上（见 ADR-0002：谁 spawn、谁拥有、跨 ACP 边界如何声明能力），不能只做一个进程内 map。
2. **URI↔路径转换**——crush 散落在 `getDiagnostics` 内，本项目按 ADR-0006 要求收敛到**单一转换边界**，并作为缓存键的一部分。
3. **诊断文本格式**——crush 直接拼字符串，本项目需要与 `03` §5.2 的 `lsp_diagnostics` 列约定对齐。
4. **重启工具**——crush 有 `lsp_restart.go`，本项目需要考虑 ACP 场景下客户端与后端的归属（后端重启 vs 客户端重启语义不同）。

### 不可照搬

1. **进程内 map 作为唯一事实源**：本项目是多进程/ACP 结构，LSP 归属必须显式建模。
2. **无位置编码约定**：ADR-0006 已明确要求转换只在边界发生（"4.4 转换只发生在边界 (D3)"），不能用"处处转换"的方式实现。
3. **无阈值基线**：crush 的实现里没有"超过 N 条诊断就截断"这类硬编码；本项目按知识层约束，**不得写死阈值**，需要基线数据或做成可配置。

---

## 7. 待确认项（写 `03` 前需要核实的实现细节）

以下细节在本次分析中**未完全确认**，落地前应回到源码核对，不要据本文直接编码：

- `getDiagnostics` 中诊断文本的确切格式（是否含行列号、severity 文案、code）。
- 诊断是否做数量上限/去重（`[未确认]`）。
- `lsp_restart.go` 的重试与退避策略（`[未确认]`）。
- `HandlesFile` 的匹配规则细节（扩展名？语言 ID？配置 glob？）。
- 编辑路径写入后是否等待 `textDocument/didChange` 的静默期（影响诊断是否为"陈旧结果"）。

---

## 8. 证据索引（源码位置）

| 事实 | 位置 |
| --- | --- |
| 客户端构造 / 握手 / 归属判定 | `internal/lsp/client.go` — `New`、`Initialize`、`HandlesFile` |
| 批量初始化 | `internal/app/lsp.go` — `initLSPClients`、`createAndStartLSPClient` |
| 失败降级 + 状态外化 | `internal/app/lsp.go` — `slog.Error("Failed to create LSP client for", ...)` + `updateLSPState(name, lsp.StateError, err, nil, 0)` |
| server 消息分级日志 | `internal/lsp/handlers.go` — `protocol.Warning` / `protocol.Info` / `protocol.Error` |
| 通知原语 | `internal/agent/tools/*.go` — `notifyLSPs` |
| 诊断原语 | `internal/agent/tools/diagnostics.go` — `getDiagnostics`（`fileDiagnostics` / `projectDiagnostics` / URI→path） |
| 编辑绑定 | `internal/agent/tools/edit.go:95,98` |
| 显式查询绑定 | `internal/agent/tools/diagnostics.go:35` |
| 模型提示 | `internal/agent/tools/diagnostics.md`（`<tips>`）、`internal/prompt/prompt.go`、`internal/prompt/templates/coder.md.tpl` |
| 重启 | `internal/agent/tools/lsp_restart.go` |
| 其它引用 | `agentic_fetch_tool.go`、`coordinator.go`、`tools/{view.go,write.go,multiedit.go}`、`common_test.go`、`multiedit_test.go` |
| LSP 包文件清单 | `internal/lsp/{client.go, client_test.go, handlers.go, language.go, rootmarkers_test.go, util/edit.go}` |

---

## 9. 与知识层的接口

本文是 `docs/knowledge_Layer` 的**参考实现输入**，不产生新的规格：

- 不定义 schema、不新增表列 → 数据只落入 `03_agent_harness_supplement.md` §5.2 的表（`lsp_diagnostics`、`lsp_servers`）；这两张表的列以上游为准，可能由 ADR 追加（如 ADR-0006 的 `position_encoding`）。
- 不覆盖归属决策 → 归属问题以 `adr/0002-acp-lsp-ownership.md` 为准。
- 不覆盖位置编码 → 以 `adr/0006-lsp-position-encoding-boundary.md` 为准。
- 本文发现的**待确认项**（§7）应在 `04` 的验收环节闭环，而不是在本目录里拍板。
