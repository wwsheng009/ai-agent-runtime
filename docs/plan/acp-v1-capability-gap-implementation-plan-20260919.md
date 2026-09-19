# ACP v1 能力覆盖与缺口实施计划

更新时间: 2026-09-19（文档同步：§1/§3/§4 状态已对齐工作树；剩余未完成项见 §8、§11）

关联文档: [../acp/README.md](../acp/README.md)（协议实现与端到端自测说明）

## 1. 结论

1. **reasoning effort 切换（`thought_level`）已完成**：协议层、`session/set_config_option`
   路径、单测与真实二进制端到端验证全部落地，本轮交付完毕，详见第 2 节。
2. **工作树另有 3 项能力已落地（复核补录，不计入待办）**：`agent_thought_chunk`
   （推理流 → 折叠思考区）、`messageId`（实时路径分块归组）、session modes 双通道
   （`mode` category 配置项 + legacy `session/set_mode` / `current_mode_update` /
   `config_option_update`）。证据与剩余收口动作见 §2.5、§4.6、§4.7。
3. 除上述外，原 **6 项待办缺口**（按 5 个小节组织）**已全部落地**（提交 `c2a9c973`，见 §10.1/§10.2；§4.1–4.5 保留设计记录）：`session/list`、
   `session/delete`、`usage_update`、`session_info_update`、`available_commands_update`、
   `session/close`。本文件给出排序、逐项改动点、验收标准与成本估计。
4. 原分批建议如下（批次 A / B 均已落地，保留作历史记录）：
   - **批次 A（低成本、可一次交付）**：`usage_update` → `session_info_update` →
     `available_commands_update` → `session/close`，并补 `agent_thought_chunk` /
     `messageId`（回放路径）的测试与一致性收口。全部是通知类或单点改动，能在 Zed 内
     一次性补齐上下文仪表、自动标题、斜杠命令菜单与会话释放。
   - **批次 B（高价值、独立实施）**：`session/list` + `session/delete`。用户可见价值最高
     （Zed 的历史会话列表与切换器），但需要新增后端接口、能力协商与存储枚举，工作量中等，
     不与批次 A 混做。
5. **范围边界**：客户端宿主能力族（`fs/*`、`terminal/*`、`elicitation`）为 N/A
   （见 §3 范围说明）；鉴权族、`session/resume` 等低优先级项见 §8。
6. **当前状态（2026-09-19 盘点）：上述 6 项均已落地；剩余为 §8 低优先级 backlog 与 §11 验证收口项。**

## 2. 本轮已完成：`thought_level`（reasoning effort 切换）

### 2.1 协议语义

`thought_level` 是 ACP v1 Session Config Options 的一等 `category` 取值（"Thought/reasoning
level selector"）。关键约束：

- category 只服务客户端 UX，**不做能力协商**：「Categories are for UX purposes only and MUST
  NOT be required for correctness」「No capability negotiation is required for category values」。
- 只有 `boolean` **类型**被门控（客户端须声明 `clientCapabilities.session.configOptions.boolean`）；
  `select` 是基线能力，默认可用。
- `session/set_config_option` 的响应必须回传**完整**选项集（便于依赖项联动，如模型切换后
  重新计算可用 effort 档位），且 `currentValue` 必须 ∈ 已下发的取值集合。
- agent 自行变更选项时，用 `session/update` 的 `config_option_update` 广播同一份完整列表。

### 2.2 实现清单

| 文件 | 改动 |
|---|---|
| `backend/cmd/aicli/commands/agent_stdio_config_option.go` | ID 常量 `acpThoughtLevelConfigOptionID = "thought_level"`；选项构造器 `acpThoughtLevelConfigOption`；取值校验 `acpThoughtLevelOptionAllowed`；`SetSessionConfigOption` 的 `thought_level` 分支；响应回传完整选项集 |
| `backend/cmd/aicli/commands/chat_reasoning_switch.go`（新增） | `applyRuntimeReasoningEffortSwitch`，与既有 `applyRuntimeModelSwitch` 对称 |
| `backend/cmd/aicli/commands/agent_stdio_thought_level_test.go`（新增） | 选项形状、目录门控、取值校验、切换生效、请求体传播 |
| `backend/scripts/acp_e2e_thought_level.go`（新增） | 真实二进制 stdio 端到端：隔离 `USERPROFILE` + 本地 mock 流式 provider，断言**上游请求体** |
| `backend/internal/acp/types.go` | `SessionConfigOptionCategoryThoughtLevel`（category 常量，与 `model`、`model_config` 并列） |
| `docs/acp/README.md` | 「模型、thinking effort 与 provider 切换」章节：JSON 示例、语义说明、E2E 运行方式 |

### 2.3 设计决策（后续实施须保持一致）

- **不写全局偏好**：ACP 侧的四类切换（`mode` / `model` / `thought_level` / `provider`）只改会话状态，
  绝不写用户的全局配置文件。这与 `/model` 等交互式斜杠命令的语义刻意区分。
- **下一轮生效**：不做流式中途切换；带内 prompt 进行中调用切换会返回错误，请客户端稍后重试，
  避免与运行中的 turn 竞争会话状态。这是对协议「值可在会话任意时刻变更」的**有意收窄**。
- **合成值 `"default"`**：ACP 要求 `currentValue` ∈ 已下发取值，而「未覆盖、用 provider 默认」
  本身是合法状态，因此需要一个真实取值承载它；选中后清除会话级覆盖。
- **目录门控**：仅当当前模型声明了 reasoning-effort 目录时才下发该选项；目录为空则不下发，
  避免客户端选出运行时无法识别的值。
- **残留旧值保留可选**：切换模型/provider 后新目录不再包含的旧值仍列出并可再次选中，
  保证切换后会话状态始终可表达。
- **未知取值拒绝**：既不在目录中、也不是当前值的取值返回 JSON-RPC `invalid params`。
- **复用交互式解析路径**：切换逻辑复用 `/model`、`/provider`、`/reasoning_effort` 已有的
  runtime 解析实现，不复制第二套。

### 2.4 验证证据

```powershell
cd backend
go test ./internal/acp/ -count=1                      # ok
go test ./cmd/aicli/commands/ -count=1                # 全绿，无失败用例
gofmt -l cmd/aicli/commands/agent_stdio_config_option.go `
         cmd/aicli/commands/agent_stdio_thought_level_test.go `
         cmd/aicli/commands/chat_reasoning_switch.go `
         scripts/acp_e2e_thought_level.go               # 无输出（格式干净）
go build -o $env:TEMP\aicli-thought-e2e.exe ./cmd/aicli/
go run ./scripts/acp_e2e_thought_level.go $env:TEMP\aicli-thought-e2e.exe
```

端到端实测输出：

```text
OK initialize
OK session/new sessionId=session_... thought_level=[low medium high default]
OK session/set_config_option thought_level=high
OK upstream request carries reasoning_effort=high
OK session/set_config_option thought_level=default
OK upstream request omits reasoning_effort after clearing
OK session/set_config_option rejects unknown effort
E2E PASS
```

### 2.5 工作树已落地补录（2026-09-19 20:1x 只读复核）

复核发现以下能力已存在于工作树（部分尚未提交），与 §2 同属 ACP v1 交互增强，登记备查、
**不计入待办**。复核时段工作树仍在推进，引用以符号名为主、行号为辅：

| 能力 | 关键证据（符号名） | 说明 |
|---|---|---|
| `agent_thought_chunk` 发射 | `agent_stdio_bridge.go` reasoning 事件分支；`types.go` `AgentThoughtChunk`；回放路径 | 推理流独立成折叠思考区，不再混入正文；**单测 + E2E 已覆盖**（§4.6/§10.6） |
| `messageId`（实时 + 回放） | `types.go` `WithMessageID`；`agent_stdio_bridge.go` `sessionUpdate()`；`agent_stdio.go` `replayACPSessionHistory` | 同一回合的文本块共享 id，思考块用 `<id>_thought`；**回放路径已打标**（§4.7/§10.6） |
| legacy `session/set_mode` | `server.go` `handleSessionSetMode`；`agent_stdio_mode.go` `SetSessionMode` | 与 `mode` 配置项同源；prompt 进行中拒绝；未知取值 `invalid params` |
| legacy `modes` 状态 | `types.go` `NewSessionResponse.Modes` / `SessionModeState`；`server.go` `handleSessionLoad`；`agent_stdio.go` `NewSession` | `session/new` 内联、`session/load` 经 `SessionModeProvider` 附带 |
| `mode` category 配置项 | `types.go` `SessionConfigOptionCategoryMode`；`agent_stdio_mode.go` `acpModeConfigOption` | 固定四项（default / accept_edits / plan / bypass_permissions），无需目录 |
| `current_mode_update` / `config_option_update` 发射 | `agent_stdio_mode.go` `broadcastACPModeChange`；`agent_stdio_config_option.go` 模式分支 | 模式切换后双通道同步；`config_option_update` marshal 有单测（`internal/acp/config_option_test.go`），发射路径与 `current_mode_update` 仍无单测（§11-B2） |

维护约束：ACP v1 已声明专用 modes API 将在未来版本移除，保留目的是兼容仍驱动 legacy
选择器的客户端；任何后续改动必须维持「两条通道状态同源」（同一 session permission mode）。

## 3. ACP v1 覆盖现状

状态键：**YES** 已实现 / **PARTIAL** 部分实现 / **NO** 未实现。

> 说明：状态已对齐 **2026-09-19 晚工作树**（含提交 `c2a9c973` 与本轮文档同步；原 20:1x 快照中的 6 项待办均已完成）。
> 引用以符号名为主；行号会漂移，实施前请以当前工作树重新确认。

| 能力 | 协议名 | 状态 | 本地证据 | 缺口 |
|---|---|---|---|---|
| 初始化 | `initialize` | PARTIAL | `server.go` dispatch；`types.go` 请求/响应结构；`server.go` 保存 `clientCaps` | `Questions` 扩展已消费 clientCapabilities（`server.go`）；boolean 门控与 fs/terminal/elicitation 感知仍未实现（§8） |
| Agent 能力声明 | `agentCapabilities` | PARTIAL | `types.go`（`DefaultAgentCapabilities`）；`server.go` `effectiveAgentCapabilities` | `loadSession:true`；prompt 能力全 false（文本-only，属预期）；MCP false；`sessionCapabilities` 为强类型，list/delete/close 按后端实现裁剪声明，resume 未声明；`auth` 为空 |
| 鉴权方法 | `authMethods` | NO | `server.go` 恒返回空数组 | 未声明任何鉴权方式 |
| 鉴权 | `authenticate` | NO | dispatch 无该分支 | — |
| 登出 | `logout` | NO | ACP 目录内无字面量 | — |
| 会话创建 | `session/new` | YES | `server.go`；`agent_stdio.go` | `additionalDirectories` / `mcpServers` 字段已解析但被忽略；`os.Chdir(cwd)` 是进程级副作用；legacy `modes` 已内联附带 |
| 会话加载 | `session/load` | YES | `server.go` `handleSessionLoad`；`agent_stdio.go` 回放 | 回放只覆盖 user/agent 文本与工具调用（无权限请求）；configOptions 与 modes 已随响应附带 |
| 会话恢复 | `session/resume` | NO | 协议文档 `sessionCapabilities.resume`；无 dispatch | 与 `session/load` 的边界未定义（见 §8） |
| 会话关闭 | `session/close` | YES | `server.go` `handleSessionClose`；`agent_stdio_session_mgmt.go` `CloseSession`；`session_management_test.go` | 幂等；进行中 prompt 的取消语义与 E2E 未收口（§11-B1/B3） |
| 会话列举 | `session/list` | YES | `types.go` `SessionListRequest/Response`；`server.go` `handleSessionList` + `SessionLister`；`agent_stdio_session_mgmt.go` `ListSessions` | 数字游标分页（页 50）+ cwd 过滤；E2E 未收口（§11-B1） |
| 会话删除 | `session/delete` | YES | `server.go` `handleSessionDelete` + `SessionDeleter`；`agent_stdio_session_mgmt.go` `DeleteSession` | 先 detach 再删存储；not-found 语义见 §10.1；E2E 未收口（§11-B1） |
| 配置项 | `session/set_config_option` | YES | `server.go`；`agent_stdio_config_option.go` | 四类：`mode` / `model` / `thought_level` / `provider` |
| 配置项广播 | `config_option_update` | YES | `agent_stdio_mode.go` `broadcastACPModeChange`；`types.go` 类型与 marshal | 仅模式切换路径有发射；其余选项经 `set_config_option` 响应回传（符合协议）；marshal 有单测，发射路径无单测（§11-B2） |
| legacy 模式切换 | `session/set_mode` | YES | `server.go` `handleSessionSetMode`；`agent_stdio_mode.go` `SetSessionMode` | prompt 进行中拒绝；无单测覆盖（§11-B2） |
| legacy 模式状态 | `modes`（new/load 结果字段） | YES | `types.go` `SessionModeState`；`agent_stdio.go` `NewSession`；`server.go` `handleSessionLoad` | 与 `mode` 配置项同源（§2.5） |
| 提示词 | `session/prompt` | YES | `server.go`；`agent_stdio.go` | — |
| 取消 | `$/cancel_request` | YES | 见 `docs/acp/README.md` | — |
| 通知：agent 文本 | `agent_message_chunk` | YES | bridge 与回放路径 | 实时路径已打 `messageId`；回放路径已打标（§4.7） |
| 通知：用户文本 | `user_message_chunk` | YES | `types.go` `UserMessageChunk`；`agent_stdio.go` 回放 | 仅用于 `session/load` 回放 |
| 通知：思考 | `agent_thought_chunk` | YES | `types.go` `AgentThoughtChunk`；bridge reasoning 分支；回放路径 | 单测 + E2E 已覆盖（§4.6/§10.6） |
| 通知：工具调用 | `tool_call` / `tool_call_update` | YES | bridge | — |
| 通知：计划 | `agent_plan` | NO | — | 见 §8 |
| 通知：用量 | `usage_update` | YES | `types.go` `UsageUpdate` + marshal；`agent_stdio_session_mgmt.go` `emitACPSessionUsage`；`agent_stdio.go` 回合结束后发射 | 窗口未知时不发；仅回合结束发射（§4.2 曾建议回合开始/结束各一次，以 §10.2 为准）；E2E 未收口（§11-B1） |
| 通知：会话信息 | `session_info_update` | YES | `types.go` `SessionInfoUpdate`；`emitACPSessionInfo`；session/new、load 回放后、回合结束 | 空 title 经 omitempty 省略；未做「同标题去重」（§11-B4）；E2E 未收口（§11-B1） |
| 通知：可用命令 | `available_commands_update` | YES | `types.go` `AvailableCommandsUpdate`；`agent_stdio_session_mgmt.go` `acpAvailableCommands`；session/new、load | 静态目录（help/status/clear/compact/model/mode）；E2E 未收口（§11-B1） |
| 通知：模式 | `current_mode_update` | YES | `agent_stdio_mode.go` `broadcastACPModeChange`；`agent_stdio_config_option.go` 模式分支 | 无单测覆盖（§11-B2） |
| 权限请求（server→client） | `session/request_permission` | YES | `server.go` `PermissionRequester`；bridge 审批桥 | — |
| 未知方法处理 | — | YES | dispatch 默认分支 | 请求 → `-32601`；通知 → 静默忽略，符合规范 |
| `_meta` | — | PARTIAL | `types.go` `ContentBlock.Meta`（内容块级） | 请求/响应级 `_meta` 未保留（initialize 的 trace context 被丢弃；`LoadSessionResponse` 无 `Meta` 字段） |

**范围说明（客户端宿主能力族，N/A）**：ACP v1 另定义 agent → client 的请求族
`fs/read_text_file`、`fs/write_text_file`、`terminal/create|output|wait_for_exit|kill|release`、
`elicitation/create`。本 agent 直接使用本地文件系统与终端工具，不依赖客户端宿主能力，
故不纳入覆盖矩阵；`session/request_permission` 同族但已实现（见上表）。若未来支持
远程/沙箱宿主，再评估接入。

**一句话总结**：reasoning effort、思考流、`messageId`（实时 + 回放）、会话模式双通道、会话管理三方法
（`session/list` / `session/delete` / `session/close`）与三项通知（`usage_update` /
`session_info_update` / `available_commands_update`）均已落地；剩余为 §8 低优先级 backlog 与
§11 验证收口项（以 E2E 缺失为主）。

## 4. 待办与收口清单（按「Zed 用户价值 × 成本」排序）

4.1–4.5 已按 §10 落地；4.6 / 4.7 的验证与一致性收口已完成（见 §10.6）。

### 4.1 优先级 P1 — `session/list` + `session/delete`

**协议语义**（ACP v1 session-list / session-delete）：

- `session/list`：请求可选 `cursor` 与 `cwd`（绝对路径过滤）；响应 `sessions` + 可选
  `nextCursor`；**空结果必须返回空数组**；无 `nextCursor` 表示到底；`SessionInfo` 必填
  `sessionId`、`cwd`（绝对路径），可选 `title`、`updatedAt`（ISO 8601）、`_meta`；
  无效 cursor 应报错。
- `session/delete`：请求 `{sessionId}`，成功返回 `{}`；删除已删除/不存在的会话
  **应静默成功**；软删/硬删、删除活动会话、对已删会话 `session/load` 均属实现自定。
- 能力声明：`sessionCapabilities.list` / `.delete`；客户端在 `initialize` 后先检查。

**落地现状（已实现，见 §10.1）**：`server.go` 提供 `SessionLister` / `SessionDeleter` 可选接口
与 dispatch，`types.go` 提供请求/响应类型，`agent_stdio_session_mgmt.go` 基于
`runtimechat.SessionManager` 实现枚举/删除（数字游标分页、cwd 过滤、先 detach 再删存储）；
能力位经 `effectiveAgentCapabilities` 按后端实现裁剪。
与下方原验收标准的差异：实现选择「删除活动会话 = 先 detach 再删存储」而非「明确拒绝」
（以 §10.1 为准）；E2E 部分未收口（§11-B1）。

**改动点**：

1. `backend/internal/acp/server.go`：新增 `SessionLister` / `SessionDeleter` 可选接口
   （与 `SessionLoader` 同层、同风格），dispatch 新增两个 case；宿主未实现接口时返回
   `-32601`（沿用既有约定）。
2. `backend/internal/acp/types.go`：新增请求/响应结构（`ListSessionsRequest/Response`、
   `SessionInfo`、`DeleteSessionRequest`），在 `DefaultAgentCapabilities` 声明
   `sessionCapabilities: {list:{}, delete:{}}`。
3. `backend/cmd/aicli/commands/`：宿主侧实现两个接口，基于 `runtimechat.SessionManager`
   枚举/删除；列表映射 `sessionId` / `cwd`（绝对路径）/ `title` / `updatedAt`，
   分页按 `updatedAt` 降序 + 不透明 cursor（offset 或 updatedAt+id 游标）；
   `cwd` 过滤语义（相等或前缀）择一实现并写入文档。
4. 删除语义须明确并测试：已删除/不存在 → 静默成功（协议 SHOULD）；**本进程占用的活动会话**
   → 返回错误；跨进程活动会话无法可靠感知时，先按「删除持久化记录 + 文档声明限制」实现，
   并预留运行标记扩展点；删除后 `session/load` 返回 `invalid params`。

**验收标准**：

- E2E：`session/new` → `session/list`（断言分页 `nextCursor` 行为与空结果空数组）→
  `session/delete` → 再次 `session/list` 不再出现；重复 delete 仍成功；
  `session/load` 已删会话报错。
- `cwd` 过滤与 `updatedAt` 排序可断言；无效 cursor 报错。
- 删除运行中会话被明确拒绝（错误码可断言）。
- 未声明能力时客户端调用返回 `-32601`（协议层单测）。

**成本**：中等（新增接口 + 宿主实现 + 存储枚举）。**独立成批**。

### 4.2 优先级 P2 — `usage_update`

**协议语义**：agent → 客户端的通知，携带上下文窗口用量（`used` / `size` 为必填非负整数，
可选 `cost`），用于 Zed 的上下文仪表；`used` 不得超过 `size`。

**落地现状（已实现，见 §10.2）**：`types.go` `UsageUpdate` + marshal；宿主在每轮 prompt
成功后经 `emitACPSessionUsage` 发射（`used=ContextTokenCount`、`size=ContextWindowTokenCount`），
窗口未知时跳过。实现收窄为「仅回合结束发射」（原建议回合开始/结束各一次，以 §10.2 为准）；
E2E 未收口（§11-B1）。

**改动点**：在事件桥（`agent_stdio_bridge.go`）中新增 `EventUsageUpdated` →
`session/update`(`usage_update`) 的转换与发射；回合开始/结束时各推一次，回合中增量按
runtime 既有节流策略；**无用量信息时不发**（不要发 `size=0` 占位）。

**验收标准**：E2E 断言 mock provider 回合结束后客户端收到至少一条 `usage_update`，
且 `used ≤ size`、`size > 0`；mock provider 需在响应中返回 usage 元数据（否则事件不产生）。

**成本**：低（纯通知，无协议协商）。

### 4.3 优先级 P2 — `session_info_update`

**协议语义**：通知，`session_info_update` 只允许可选字段 `title`（可空以清除）、
`updatedAt`（ISO 8601，可空）、`_meta`；**不得**包含 `sessionId` / `cwd` /
`additionalDirectories`（这些属于 `session/list` 的 `SessionInfo`）。

**落地现状（已实现，见 §10.2）**：`types.go` `SessionInfoUpdate`；宿主在 session/new、
session/load 回放后与每轮 prompt 结束经 `emitACPSessionInfo` 发射（数据源为
`Session.Metadata` 标题与最近活动时间；空 title 经 omitempty 省略）。
与下方验收标准的差异：未做「同一标题只发一次」去重，当前每轮重发（§11-B4）；
E2E 未收口（§11-B1）。

**改动点**：在标题首次生成/变更时发射 `session_info_update`；数据源直接用
`Session.Metadata` 的有效标题；`updatedAt` 取会话最近活动时间（与 §4.1 的
`SessionInfo.updatedAt` 同源，便于 B 批复用）；标题未生成前不发，避免抖动。

**验收标准**：E2E 断言首轮 prompt 后收到带非空 `title` 的通知，且通知体不含
`sessionId` / `cwd`；标题稳定不抖动（同一标题只发一次，变更才再发）。

**成本**：低（通知，复用既有标题体系）。


### 4.4 优先级 P2 — `available_commands_update`

**协议语义**：通知，`availableCommands: AvailableCommand[]`；每项必填 `name`
（**不带前导 `/`**）与 `description`，可选 `input: {hint}`（纯文本提示）。
可在会话创建后任意时刻发送；命令以普通 prompt 文本（`/name args`）触发。

**落地现状（已实现，见 §10.2）**：`types.go` `AvailableCommandsUpdate`；宿主在 session/new、
session/load 经 `emitACPSessionCatalog` 发射静态目录（help/status/clear/compact/model/mode，
`acpAvailableCommands`），name 不带前导 `/`，空目录序列化为 `[]`。
E2E 未收口（§11-B1）。

**改动点**：把注册表投影为协议结构（`name` 去掉前导 `/`；`description` 用 summary；
`input.hint` 可用 args 描述），在 `session/new` 完成后与命令集变化时各发射一次；
**过滤交互式 TUI 专属命令**（`Interactive` / `Hidden` 标记）与 ACP 上下文无意义的命令。

**验收标准**：E2E 断言 `session/new` 后收到 `available_commands_update`，列表包含
`model` / `provider` / `reasoning_effort` 等 ACP 可用命令，且不含 TUI 专属项；
所有 `name` 均无前导 `/`。

**成本**：低到中（取决于命令元数据是否已有描述字段）。

### 4.5 优先级 P3 — `session/close`

**协议语义**：`session/close` 取消该会话进行中的工作并释放运行时资源，由
`sessionCapabilities.close` 声明；**close ≠ delete**：持久化历史保留，之后仍可
`session/load`；`session/delete` 才移除历史（§4.1）。

**落地现状（已实现，见 §10.1）**：`server.go` `handleSessionClose` + `SessionCloser` 可选接口；
宿主 `CloseSession` 幂等（未挂载会话返回成功），对已挂载会话调用 `closeSessionLocked`
（复用 prompt 取消路径）并释放内存状态。与进行中 prompt 的竞态语义与 E2E 未收口
（§11-B1/B3）。

**改动点**：新增 dispatch case 与 `SessionCloser` 可选接口；关闭时先取消该会话进行中的
prompt（复用现有 prompt 取消路径），再清理 host 侧会话状态并释放资源；关闭后对该会话的
prompt / load 等请求返回明确错误（`invalid params`）；重复 close 语义明确（建议幂等，
或 `invalid params`，写入文档）；能力声明加入 `close`；进程退出路径行为不回归。

**验收标准**：E2E：`session/new` → 发起长 prompt → `session/close`（断言 prompt 被取消、
无悬挂 goroutine）→ 对该会话再发 prompt 被拒；close 后 `session/load` 仍可恢复历史；
进程退出路径不回归。

**成本**：低到中（复用既有实现，但需处理与进行中 prompt 的竞态）。

### 4.6 状态更新 — `agent_thought_chunk`（已收口：单测 + E2E 通过，见 §10.6）

原计划的实现工作已在工作树完成：`types.go` `AgentThoughtChunk` +
`agent_stdio_bridge.go` 的 reasoning 事件分支把推理流映射为 `agent_thought_chunk`，
与正文严格分流（§2.5）。

**验证收口（已完成，证据见 §10.6）**：

- 命令层单测：reasoning 事件 → `agent_thought_chunk`，正文 chunk 不含推理文本；
  思考块 `messageId` 为 `<回合 id>_thought`。
- E2E：mock provider 返回带 reasoning 的流，断言收到 `agent_thought_chunk` 且
  `agent_message_chunk` 不含该内容。
- 回放路径：`agent_stdio.go` `replayACPSessionHistory` 在正文前重放留存推理为
  `agent_thought_chunk`（id 为回放消息 id + `_thought`）。
- 若实现与本节分流约定不一致（例如同时发 `assistant.message` 全文），以单测锁定行为。

**成本**：低（仅补测试）。

### 4.7 状态更新 — `messageId`（已收口：实时 + 回放均打标，见 §10.6）

**协议语义**：消息块上的可选标识，供客户端做分块归组与「重新生成」定位；
`session/load` 回放**允许**携带（MAY），并非强制。

**当前代码事实**：`types.go` `WithMessageID`（仅对 user / agent / thought chunk 生效）；
`agent_stdio_bridge.go` `sessionUpdate()` 在实时路径为同一回合的文本块注入共享 id、
为思考块注入 `<id>_thought`；`agent_stdio.go` `replayACPSessionHistory` 在回放路径复用
同一归组语义：优先取消息持久化的 canonical `metadata.message_id`，缺失时回退
`replay_<index>`，思考块为 `<id>_thought`（见 §10.6）。

**改动点**：已实施（回放路径打标）；如需跨刷新稳定，需把 id 与消息存储关联，
否则仅要求会话内稳定。

**验收标准**：E2E：单轮多 chunk 的 assistant 回复中所有 `agent_message_chunk` 共享同一
`messageId`；回放路径同一消息的多块共享 id、不同消息不同 id；实时与回放的归组语义一致。

**成本**：低（纯字段注入；回放侧为可选项，按产品要求决定是否实施）。


## 5. 实施顺序建议

| 批次 | 内容 | 理由 | 预估 |
|---|---|---|---|
| **A** | 4.2 `usage_update` → 4.3 `session_info_update` → 4.4 `available_commands_update` → 4.5 `session/close`；并补 4.6 / 4.7 的测试收口 | 4.6 / 4.7 实现已落地，先补验证锁定行为；再按纯发射（4.2）→ 复用既有数据（4.3/4.4）→ 能力声明收口（4.5）推进 | 一次交付 |
| **B** | 4.1 `session/list` + `session/delete` | 价值最高但需要新增接口 + 能力协商 + 存储枚举，且与 4.3 的标题数据有依赖（4.3 先落地可为 B 复用） | 独立一批 |

批次 A 内部建议逐项独立提交、逐项跑 E2E，便于定位回归；批次 A 全部完成后再启动批次 B。
（历史建议：批次 A / B 均已落地，见 §10；其中 4.1–4.5 的 E2E 部分尚未补齐，见 §11-B1。）

## 6. 约束与红线

1. **不写全局偏好**：ACP 侧所有切换只改会话状态，绝不写用户全局配置文件。
2. **下一轮生效**：不做流式中途切换；prompt 进行中调用切换返回错误请客户端重试。
   这是对协议的有意收窄，实施新选项（如未来的 `model_config` 类选项）时须沿用。
3. **只发 `select` 类型选项**：`boolean` 类型必须先解析并确认客户端
   `session.configOptions.boolean` 能力后才能下发；`select` 是基线能力。
4. **`currentValue` 必须 ∈ 已下发取值**：需要表达「未设置」语义时，提供真实的合成取值
   （如 `thought_level` 的 `"default"`），不要用空串或协议外约定。
5. **`session/set_config_option` 响应必须回传完整选项集**，不能只回传被改的那一项。
6. **P0 迁移闸门**：`TestChatInteractiveDirectWriterInventory`
   （`backend/cmd/aicli/commands/chat_command_result_test.go`）锁定 `chat*.go` / `command.go`
   中可达的终端直写。**任何新增的 `fmt.Fprint(os.Stderr)` 都会导致构建失败**；
   新功能只走统一渲染/事件路径，遇到遗留直写应删除，绝不往清单里加条目。
7. **未知方法处理遵循规范**：未识别的请求返回 JSON-RPC `-32601`，未识别的通知静默忽略。
8. **可选接口缺失时优雅降级**：宿主未实现某可选接口 → 对应方法返回 `-32601`，
   不 panic、不静默成功（与 `SessionConfigOptionSetter`、`SessionLoader` 现有约定一致）。
9. **能力声明与实际实现一致**：`DefaultAgentCapabilities` 里声明了什么，就必须真的支持；
   未实现的能力不得提前打开。
10. **`session/close` ≠ `session/delete`**：close 取消进行中的 turn 并释放运行时资源，
    持久化历史保留（可再次 `session/load`）；delete 才删除历史。两者不得混用实现。
11. **双通道同源**：`mode` 配置项与 legacy `modes` / `session/set_mode` 必须由同一
    session permission mode 驱动，任何一侧的改动都要同步另一侧（见 §2.5）。

## 7. 验证方法论

每个缺口项按三层验证，缺一不可：

1. **协议层单测**：`go test ./internal/acp/ -count=1`，覆盖新的出站/入站编解码与 dispatch 分支。
2. **命令层单测**：`go test ./cmd/aicli/commands/ -count=1`，覆盖宿主侧行为与错误分支。
3. **真实二进制端到端**：新增 `backend/scripts/acp_e2e_<feature>.go`，范式照抄
   `backend/scripts/acp_e2e_thought_level.go` 与 `acp_e2e_cancel.go`：
   - 编译真实产物：`go build -o $env:TEMP\aicli-e2e.exe ./cmd/aicli/`
   - 起真实 `aicli agent stdio` 子进程，使用**隔离 `USERPROFILE`**
   - 内置**本地 mock 流式 provider**，断言方向要覆盖**上游请求体**（客户端→agent 的语义
     是否真的落到模型调用）与**下游通知**（agent→客户端的推送是否真的发出）
   - 对新增通知，同时断言**不发送**的条件（如无用量信息时不发 `usage_update`），
     避免噪声与误导性数据
4. **格式检查**：`gofmt -l` 对全部改动文件无输出。
5. **能力声明一致性矩阵**：新增/变更 `DefaultAgentCapabilities` 时，须有测试断言
   「声明 list / delete / close / resume 等能力 ⇔ dispatch 可达且宿主实现对应接口」；
   宿主未实现接口时对应方法返回 `-32601`。
6. **文档同步**：每个落地能力更新 `docs/acp/README.md`（沿用 `thought_level` 的做法）。
7. **回归基线**：改动 `DefaultAgentCapabilities` 或 bridge 通知路径后，重跑
   `scripts/acp_e2e_thought_level.go`、`scripts/acp_e2e_cancel.go`，确认无回归。

```powershell
cd backend
go test ./internal/acp/ -count=1
go test ./cmd/aicli/commands/ -count=1
gofmt -l <改动的每个文件>
go build -o $env:TEMP\aicli-e2e.exe ./cmd/aicli/
go run ./scripts/acp_e2e_<feature>.go $env:TEMP\aicli-e2e.exe
```

## 8. 附：低优先级缺口（本计划暂不实施）

以下项对 Zed 体感影响较小或需要更大范围改动，记录备查，不在本计划范围内：

- **鉴权族**：`authMethods` 声明、`authenticate`、`logout`（当前恒返回空数组，无鉴权流程）。
- **`session/resume`**：与 `session/load` 语义重叠（重连不重放 vs 重放），需先明确边界；
  `sessionCapabilities.resume` 目前未声明。
- **客户端宿主能力族（`fs/*`、`terminal/*`、`elicitation`）**：当前 agent 直连本地
  fs/terminal，不使用客户端宿主能力，故为 N/A（§3 范围说明）；远程/沙箱宿主场景再评估。
- **`clientCapabilities` 解析与协商**：当前解析后即丢弃，导致无法做 boolean 选项门控、
  无法感知客户端 fs/terminal/elicitation 能力。是多项能力的前置依赖，宜作为独立议题。
- **`promptCapabilities`（image / audio / embeddedContext）**：当前全 false（文本-only 属预期），
  开放需先补齐多模态输入链路。
- **`mcpCapabilities`**：MCP 相关能力未声明。
- **`session/new` 的 `additionalDirectories` / `mcpServers`**：字段已解析但被忽略。
- **`os.Chdir(cwd)` 进程级副作用**：`session/new` 会改整个进程的工作目录，
  多会话并发时不安全，需改为会话级路径解析。
- **请求/响应级 `_meta`**：initialize 请求中的 `_meta`（如 trace context）被丢弃；
  内容块级 `ContentBlock.Meta` 已保留。
- **`agent_plan`**：计划展示通知无发射点（低优先级；Zed 侧已有工具调用/计划模式 UI 兜底）。
- **legacy modes 清理**：`mode` 配置项 + legacy `modes` / `set_mode` 双通道已落地（§2.5）；
  协议声明专用 modes API 未来将移除，后续不得新增对 legacy 通道的依赖，
  待客户端生态切换后再评估删除死面。

## 9. 参考

- ACP v1 协议文档：<https://agentclientprotocol.com/protocol/v1/session-config-options.md>
  （Session Config Options：category 语义、boolean 门控、`set_config_option` 响应契约）
- ACP v1 会话列表 / 删除：<https://agentclientprotocol.com/protocol/v1/session-list.md>、
  <https://agentclientprotocol.com/protocol/v1/session-delete.md>
  （cursor/cwd 分页、`SessionInfo` 字段、delete 语义与能力声明）
- ACP v1 会话建立：<https://agentclientprotocol.com/protocol/v1/session-setup.md>
  （load / resume / close 语义与能力声明）
- ACP v1 斜杠命令：<https://agentclientprotocol.com/protocol/v1/slash-commands.md>
  （`AvailableCommand` 字段与 `available_commands_update`）
- ACP v1 会话模式（已标记未来移除）：<https://agentclientprotocol.com/protocol/v1/session-modes.md>
  （legacy `modes` / `set_mode` / `current_mode_update` 与 config option 的替代关系）
- ACP v1 扩展性：<https://agentclientprotocol.com/protocol/v1/extensibility.md>
  （未知方法/通知的处理约定）
- 本仓库实现说明：[../acp/README.md](../acp/README.md)
- 端到端脚本：`backend/scripts/acp_e2e_thought_level.go`、`backend/scripts/acp_e2e_cancel.go`

## 10. 实施状态（2026-09-19 落地记录）

本节记录本轮实际落地的能力与验证结果，与 §2–§7 的差距清单一一对应。

### 10.1 会话管理（session/list / session/delete / session/close）

| 层 | 文件 | 内容 |
|---|---|---|
| 协议 | `backend/internal/acp/types.go` | `MethodSessionList/Delete/Close` 常量；`SessionListRequest/SessionSummary/SessionListResponse`、`SessionDeleteRequest/Response`、`SessionCloseRequest/Response`；`SessionCapabilities` 由 `json.RawMessage` 改为强类型结构（`List/Delete/Close/Resume`） |
| 服务端 | `backend/internal/acp/server.go` | `SessionLister/SessionDeleter/SessionCloser` 可选接口；dispatch 三个方法；`effectiveAgentCapabilities()` 按后端实际实现裁剪能力位；`sessions` 空值归一为 `[]`；`sessionId` 缺失返回 `-32602` |
| 宿主 | `backend/cmd/aicli/commands/agent_stdio.go`、`agent_stdio_session_mgmt.go` | 懒加载持久化 store（与 session/new 同源配置解析，测试可注入临时目录）；`ListSessions` 合并内存活跃会话与持久化记录，按 `updatedAt` 倒序、数字游标分页（页大小 50）；`session/new` 把 workspace（客户端 `cwd`，缺省取进程工作目录）写入会话元数据，`cwd` 过滤据此生效且不隐藏未记录 workspace 的历史会话；`DeleteSession` 先 detach 再删存储；`CloseSession` 幂等 |

行为契约（测试固化）：

- 能力位为 `true` ⇔ 方法可达；后端未实现对应接口时调用返回 `-32601`（`session_management_test.go`）。
- `sessions` 恒为数组；空命令目录序列化为 `[]`（不返回 `null`）。
- `session/delete` 对「内存与存储都不存在」返回 not-found；`session/close` 对未挂载会话返回成功。

### 10.2 通知面补齐

| 通知 | 发射点 | 说明 |
|---|---|---|
| `usage_update` | 每轮 prompt 成功后 | `used=ContextTokenCount`、`size=ContextWindowTokenCount`；窗口未知时**不发**，避免客户端渲染坏掉的计量条 |
| `session_info_update` | session/new、session/load 回放后、每轮 prompt 结束 | 携带标题与 `updatedAt`；自动标题生成后历史面板不再陈旧 |
| `available_commands_update` | session/new、session/load | 静态命令目录（help/status/clear/compact/model/mode），只列 headless 客户端真正可触发的命令 |

### 10.3 权限模型切换与 yolo

结论：**有**。ACP v1 中权限模式以「配置项」为准（`category: "mode"`），legacy
`session/set_mode` + `modes` 保留双通道（§2.5），两条通道读写同一份会话状态。

本地权限模式取值：`default` / `accept_edits` / `plan` / `bypass_permissions`。
**yolo 即 `bypass_permissions`**：CLI 的 `--yolo` 与 `--permission-mode bypass_permissions`
等价；客户端在模式下拉中切到 `bypass_permissions` 即获得 yolo 行为。

### 10.4 交互回答面板兼容性（--no-interactive 无法回答运行时提问）

问题复现：非交互模式（`--no-interactive`，含 ACP 宿主）下运行时 `ask_user_question`
直接失败并返回 `nonInteractiveQuestionError`（`chat_runtime_events.go`），
客户端表现为「提问无法回答 / 回合直接失败」。

修复：新增 ACP 扩展 `session/request_question`，仅在客户端 `clientCapabilities`
声明 `questions` 时启用；宿主把 `askQuestionHeadless` 路由到该扩展，
客户端未声明能力时降级为明确错误而不是静默挂起。

使用建议（同样适用于纯 CLI）：

- 需要一次成型时，把必要信息直接写进 `aicli exec` 输入；
- 纯文本问答使用 `aicli exec --disable-tools "..."`；
- 需要交互追问（面板 / 多轮）时使用 `aicli chat`。

### 10.5 验证

```powershell
cd backend
go build ./internal/... ./cmd/...                          # exit 0
go test ./internal/acp/ -count=1                           # ok
go test ./cmd/aicli/commands/ -run 'ACP|Stdio' -count=1    # ok
go test ./cmd/aicli/commands/ -run 'TestChatInteractiveDirectWriter' -count=1  # P0 红线 ok
gofmt -l <改动文件>                                        # 无输出
```

新增测试：`backend/internal/acp/session_management_test.go`（能力门控、方法 dispatch、
通知 wire shape）、`backend/cmd/aicli/commands/agent_stdio_session_mgmt_test.go`
（list/delete/close、活跃会话合并、命令目录、usage 跳过条件）。

### 10.6 §4.6 / §4.7 收口（2026-09-19 晚）

**§4.6 `agent_thought_chunk` 验证收口**

- 命令层单测已覆盖 reasoning 分流：`TestACPEventBridge_RuntimeReasoningEmitsThoughtChunk`
  （dotted + legacy 两种事件形态；thought 块共享 `<回合 id>_thought`；answer 块不泄漏推理文本；
  wire 形状含 `sessionUpdate`/`messageId`）。
- 新增真实 E2E：`backend/scripts/acp_e2e_thought_chunk.go`。mock provider 依次流式返回
  `reasoning_content`（"先确认" + "需求。"）与 `content`（"hello" + " chunk"），断言：
  - 收到 `agent_thought_chunk`，拼接文本 = `先确认需求。`，全部共享同一 `..._thought` id；
  - `agent_message_chunk` 拼接文本 = `hello chunk`，全部共享同一 id 且与 thought id 不同；
  - 答案块不包含推理文本。
- 回放路径发射：`agent_stdio.go` `replayACPSessionHistory` 在正文前重放留存推理
  （`internal/types/reasoning.go` `GetReasoningBlock` / `DisplayText`）；单测
  `TestReplayACPSessionHistory_RestoresThoughtChunk` 锁定 user → thought → answer 顺序。

**§4.7 `messageId` 回放路径收口**

- `agent_stdio.go` `replayACPSessionHistory` 为回放块打标：优先使用消息持久化的
  canonical `metadata.message_id`（`internal/types/message_id.go`），缺失时回退为
  回放内唯一且确定的 `replay_<index>`，与实时路径「同一消息多块共享 id、不同消息不同 id」
  的归组语义一致。
- 单测 `TestReplayACPSessionHistory_TagsMessageIDs` 锁定：metadata id 原样透传、
  无 id 消息获得非空且互不重复的 fallback id。
- E2E 同一脚本覆盖 `session/load` 回放：用户块与助手块均携带非空且互不相同的 messageId
  （实测为持久化 metadata id，非 fallback）；留存推理在正文前重放为 `agent_thought_chunk`，
  拼接文本含 `先确认需求。`，共享同一 `<id>_thought` 且与助手块 id 不同。

**顺带修复：流式空白保真**

E2E 首跑暴露：`agent_stdio_bridge.go` 的 delta 路径复用会 `TrimSpace` 的
`payloadStringValue`，导致分块边界空格丢失（`"hello"` + `" chunk"` → `hellochunk`）。
新增 `acpUntrimmedString` 用于答案/推理流式文本（标识类字段仍用原函数），
单测 `TestACPEventBridge_StreamDeltaKeepsWhitespace` 锁定 `hello ` + ` chunk` = `hello  chunk`。

**验证**

```powershell
cd backend
go build ./internal/... ./cmd/...                       # exit 0
go test ./cmd/aicli/commands/ -count=1                  # ok（含 P0 红线）
go test ./internal/acp/ -count=1                        # ok
go build -o $env:TEMP\aicli-e2e.exe ./cmd/aicli/
go run ./scripts/acp_e2e_thought_chunk.go $env:TEMP\aicli-e2e.exe   # E2E PASS
go run ./scripts/acp_e2e_cancel.go $env:TEMP\aicli-e2e.exe          # E2E PASS
go run ./scripts/acp_e2e_thought_level.go $env:TEMP\aicli-e2e.exe   # E2E PASS
gofmt -l <改动文件>                                     # 无输出
```

## 11. 剩余未完成项盘点（2026-09-19 晚，与工作树核对）

> 本节是按当前工作树（含提交 `c2a9c973` 与本轮文档同步）重新盘点的剩余项；§1 / §3 / §4 的
> 陈旧状态已在本轮同步修正。状态键沿用 §3：**YES** / **PARTIAL** / **NO**。

### 11.1 实现类 backlog（§8 已声明暂不实施）

| # | 项 | 状态 | 证据 / 缺口 |
|---|---|---|---|
| A1 | 鉴权族 `authMethods` / `authenticate` / `logout` | NO | `server.go` 恒返回空数组；无 dispatch 分支 |
| A2 | `session/resume` | NO | 无 dispatch；`SessionCapabilities.Resume` 未声明；与 `session/load` 边界未定义 |
| A3 | `clientCapabilities` 协商 | PARTIAL | 仅 `Questions` 扩展消费（`server.go`）；boolean 门控、fs/terminal/elicitation 感知未做 |
| A4 | `session/new` 的 `additionalDirectories` / `mcpServers` | PARTIAL | 字段已解析、未使用（README 已注明） |
| A5 | `os.Chdir(cwd)` 进程级副作用 | PARTIAL | `session/new` / `session/load` 改进程工作目录，多会话并发不安全 |
| A6 | 请求/响应级 `_meta` | PARTIAL | initialize trace context 丢弃；仅内容块级保留 |
| A7 | `agent_plan` | NO | 无发射点 |
| A8 | `promptCapabilities` / `mcpCapabilities` | NO | 全 false（文本-only 属预期） |
| A9 | legacy modes 清理 | N/A（未来） | 待客户端生态切换后再评估删除死面 |

### 11.2 验证收口类（实现已做，验证未闭环；§7 三层验证不完整）

| # | 项 | 现状 | 缺口与建议 |
|---|---|---|---|
| B1 | 4.1–4.5 真实二进制 E2E 缺失 | `scripts/` 仅 `acp_e2e_cancel.go` / `acp_e2e_thought_chunk.go` / `acp_e2e_thought_level.go` | §4.1–4.5 验收标准点名的 E2E 断言（分页 `nextCursor`、delete→list 消失、usage `used≤size`、title 非空且不含 sessionId/cwd、命令无前导 `/`、close 取消进行中 prompt）均未覆盖。建议新增 `scripts/acp_e2e_session_mgmt.go` 与 `scripts/acp_e2e_notifications.go` |
| B2 | mode 通道单测缺失 | 测试中 `acpModeConfigOptionID` 仅被 `withoutModeConfigOption` 过滤；`broadcastACPModeChange` / `SetSessionMode` / `handleSessionSetMode` 零引用 | `current_mode_update` 发射、legacy `session/set_mode`、mode 配置项行为、`config_option_update` 发射路径均无测试（`internal/acp/config_option_test.go` 仅覆盖 marshal 形状） |
| B3 | `session/close` × 进行中 prompt 竞态无测试 | 单测仅覆盖幂等 / 未挂载 | §4.5 验收要求「长 prompt → close → 断言取消」；需单测或 E2E 固化 |
| B4 | `session_info_update` 未去重 | 每轮 prompt 结束无条件重发（`agent_stdio.go`） | 与 §4.3 验收「同一标题只发一次」不符；wire 层空 title 被 omitempty 省略，无实际破坏；建议实现 last-title 去重，或在 §4.3 明确接受重发 |
| B5 | §4.1 删除活动会话语义分歧 | 实现「先 detach 再删存储」（§10.1） | 原验收写「明确拒绝」；§4.1 已按实现同步为落地现状，如需改为拒绝需产品裁定 |

### 11.3 文档同步记录（本轮）

- §1 第 3 / 6 条、§3 状态说明与矩阵 6 行、§3 一句话总结、§4.1–4.5「当前代码事实」、§5 注记已对齐工作树。
- `agent_stdio_config_option_test.go` 中「The mode option has its own tests」注释不属实，已修正为指向本节 B2。
- 关联文档 `docs/acp/README.md` 经核对无陈旧表述（`session/list` / `delete` / `close`、三项通知、
  `agent_thought_chunk`、`messageId` 均已收录；MCP / `additionalDirectories` 的「暂不支持」说明与实现一致）。
