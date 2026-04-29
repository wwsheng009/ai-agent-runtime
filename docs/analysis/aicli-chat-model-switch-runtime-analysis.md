# aicli chat 运行时切换模型与 reasoning_effort 可行性分析

- 调研日期：2026-04-29
- 结论先行：可以实现，但应作为终端内运行时“选择菜单/弹出面板”来做，不是 GUI 弹窗。
- 关键前提：当前代码里 `/model` 还会被 `/mode` 前缀误伤，必须先修复命令路由，否则新增模型切换命令会被错误分发。

## 1. 现状结论

- `backend/cmd/aicli/commands/command.go` 里已经有完整的 slash 命令分发，但没有真正的 `/model` 分支。
- 同一个文件里，`strings.HasPrefix(cmdLower, "/mode")` 会把 `/model` 误判成 `permission-mode` 命令，因为 `/model` 也是 `/mode` 的前缀。
- `backend/cmd/aicli/ui/welcome.go` 已经把 `/model [name]` 写进帮助文案，说明产品预期里本来就有这个入口，但实现没有跟上。
- `backend/cmd/aicli/commands/chat.go` 的 `runChatLoop` 已经支持在聊天过程中处理 slash 命令，所以从主循环角度看，运行时切模型并不是架构性障碍。
- 现有启动期已经有 `selectProviderWithReader`、`selectModelWithReader`、`selectReasoningEffortWithReader` 这一套交互选择器，但它们更适合“启动时一次性询问”，不适合直接拿来做运行时菜单。

## 2. 可行性判断

- 模型切换是可行的。`ChatSession` 里保存了 `ProviderName`、`Provider`、`Model`、`BaseURL`、`ReasoningEffort` 等运行时字段，下一轮请求会直接读取这些字段。
- 持久化也是可行的。`backend/cmd/aicli/commands/chat_session.go` 的 `syncRuntimeSessionFromChat` 会把 provider、model、reasoning_effort 等信息写回 runtime session metadata。
- 请求层也是动态的。`backend/cmd/aicli/commands/chat_provider_turn.go` 每轮都会从 `session.Model` 和 `session.ReasoningEffort` 构造请求参数，并直接使用 `session.BaseURL` 发起 HTTP 请求。
- 因此，只要在 `/model` 命令中正确更新这些字段，并同步持久化，下一轮对话就会切到新模型。
- 但这里的“更新字段”不能理解成简单把用户输入原样写进 `session.Model`，运行时切换仍然要走一次 provider/model 解析和映射，确保请求模型、实际模型、能力查询和 URL 构造保持一致。

## 3. 运行时切换的关键耦合点

- `session.BaseURL` 不是装饰字段，而是实际请求地址。它在 `backend/cmd/aicli/commands/chat_setup.go` 初始化，`backend/cmd/aicli/commands/chat_provider_turn.go` 直接拿来发请求。切模型后必须重算。
- `selectModelWithReader` 不能直接复用为运行时选择器，原因有两个。
- 第一，它直接读取 `*bufio.Reader`，而聊天循环运行时已经启用了 `chatInputQueue`，同一个 stdin 被两个读取路径同时消费会产生竞争。
- 第二，它的回车语义是“回到 `provider.DefaultModel`”，而运行时 `/model` 更合理的语义通常是“保留当前模型”。
- `selectReasoningEffortWithReader` 比模型选择器更接近运行时需求，因为它在回车时会保留当前值，但它同样是基于 `bufio.Reader` 的启动期实现，不能直接放进聊天循环里。
- `backend/cmd/aicli/commands/chat_input_buffer.go` 的 `discardPendingInteractiveInputForPriorityPrompt` 和 `backend/cmd/aicli/commands/chat_input_queue.go` 的 `chatInteractiveReadPriorityLine` 已经是运行时“优先级弹出提示”的标准做法，`/model` 很适合沿用这条路径。
- `backend/cmd/aicli/commands/chat_runtime_events.go` 里审批提示和问题提示已经在用同样的模式，这说明运行时菜单不需要重新发明输入框，只需要复用现有优先级输入通道。

## 4. 还需要关注的缓存点

- `backend/cmd/aicli/commands/chat_actor_host.go` 和 `backend/cmd/aicli/commands/skills_integration.go` 都会把 `session.Model` / `session.ReasoningEffort` 传入本地 runtime、agent config 或 alias 注册逻辑。
- 这意味着主对话请求层可以热切换，但如果启用了 actor-first / skills 路径，最好顺手检查本地 runtime 的默认模型和别名是否也需要刷新。
- `backend/cmd/aicli/commands/logger.go` 的 `ChatLogger` 只在初始化时保存一次 `provider / protocol / model / baseURL`。如果切模型后希望日志文件名和日志元数据完全一致，日志层也需要额外同步。

## 5. 建议的实现形态

- 新增一个真正的 `/model` 命令分支，命令匹配要改成精确 token 判断，不能再用 `strings.HasPrefix(cmdLower, "/mode")` 这种会误伤 `/model` 的写法。
- `/model` 无参数时，弹出一个“当前模型 + 可选模型”菜单；回车应保留当前模型，而不是回退到默认模型。
- `/model <name>` 有参数时，允许直接切换到指定模型，作为交互菜单之外的快捷路径。
- 模型切换完成后，若新模型支持 reasoning_effort / thinking_effort，再弹出第二步选择菜单，让用户决定是否保留当前 reasoning_effort。
- 如果当前 `reasoning_effort` 不在新模型允许列表内，不能默认原样保留；要么强制用户重新选择，要么清空后走新模型的默认路径。
- 运行时菜单的输入应该走 `chatInteractiveReadPriorityLine`，并在弹出前调用 `discardPendingInteractiveInputForPriorityPrompt(session, "模型选择")`，避免旧输入串入新菜单。
- 切换成功后，至少要更新 `session.Model`、`session.ReasoningEffort`、`session.BaseURL`，然后调用 `syncRuntimeSessionFromChat(session)` 持久化。
- 模型解析建议复用 `resolveProviderExecutionContext`，或者显式调用 `config.ApplyModelMapping`，避免运行时切换绕过现有 model mapping。
- 如果希望日志完整一致，建议同步更新 `ChatLogger` 里的会话元数据，或者记录一条明确的模型切换事件。
- 对外文案可以叫 `thinking_effort`，但这更适合做“UI 文案/菜单标签”，不是一个已经存在的独立状态字段。
- 底层当前真正会被请求层消费的是 `ReasoningEffort` / `reasoning_effort`；如果后续要支持 Anthropic 或 Gemini 那种真实的 `thinking` 配置，需要另外引入 `Thinking` 状态和请求构造路径，不能只靠改名覆盖。

## 6. 风险与边界

- `NoInteractive` 模式下不能弹出菜单，只能支持直接参数或直接回显当前状态。
- 如果 `SupportedModels` 为空，现有模型选择器没有候选列表，运行时选择器需要补一个“允许手输模型名”的兜底逻辑。
- 如果用户按回车的期望是“保留当前模型”，就不能直接复用现有 `selectModelWithReader`，因为它默认回到 provider 默认模型。
- 当前 `/model` 会被 `/mode` 前缀吞掉，所以在实现前必须先修路由冲突，否则新命令会一直进错分支。
- 如果未来要做跨 provider 切换，那就不只是改模型了，还要重新 bootstrap provider、adapter、BaseURL、HTTP client，复杂度会明显上升。

## 7. 最终判断

- 结论是“能做，而且技术上不重”，但前提是把它做成终端内运行时选择菜单，而不是 GUI 窗口。
- 最小可行路径是：修正命令路由冲突 + 新增运行时模型选择器 + 切换后同步 `BaseURL` / `ReasoningEffort` / 持久化会话。
- 如果后续还要扩展到跨 provider 切换，那需要单独拆成“重新 bootstrap 会话”的工程改造，不建议和 `/model` 一起做。
