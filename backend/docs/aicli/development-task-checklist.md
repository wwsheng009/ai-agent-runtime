# aicli 多模态输入开发任务清单

## 本次已执行

- [x] 在用户 prompt 侧增加本地图片路径自动识别能力
  - 入口：`internal/llm/multimodal_input.go`
  - 支持从普通文本、引号、反引号、Markdown 图片语法中提取本地图片路径
- [x] 将识别到的本地图片持久化为 runtime message metadata
  - metadata key：`input_images`
- [x] 在 Codex 协议映射阶段把本地图片转换为 `input_image` content part
  - 入口：`internal/llm/reasoning_helpers.go`
- [x] 在 Codex adapter 请求构造阶段保留结构化 user content parts
  - 入口：`internal/llm/adapter/codex.go`
- [x] 打通 shared chat / actor chat 两条 prompt 注入链路
  - `internal/chatcore/provider_loop.go`
  - `internal/chatcore/core.go`
  - `internal/chat/actor.go`
- [x] 补充回归测试
  - `internal/llm/multimodal_input_test.go`
  - `internal/llm/adapter/codex_request_test.go`
  - `internal/chatcore/provider_loop_test.go`
- [x] 增加显式附件参数：`aicli chat --image/-i <path>`
  - `cmd/aicli/main.go`：注册 `--image` / `-i` flag（支持 StringSlice，可重复指定）
  - `cmd/aicli/commands/chat_options.go`：`chatCommandOptions.ImagePaths` 字段
  - `cmd/aicli/commands/chat.go`：`ChatSession.ImagePaths` 字段
  - `cmd/aicli/commands/chat_setup.go`：opts → session 传递
  - `cmd/aicli/commands/chat_core.go`：传入 `ToolLoopRequest.ExplicitImagePaths`
  - `internal/chatcore/provider_loop.go`：`ToolLoopRequest.ExplicitImagePaths` 字段 + 使用 `NewUserPromptMessageWithImages`
  - `internal/chatcore/core.go`：`ExecuteRequest.ExplicitImagePaths` + 使用 `NewUserPromptMessageWithImages`
- [x] 为缺失/失效的显式图片路径增加用户可见 warning
  - `internal/llm/multimodal_input.go`：`ValidateLocalInputImagePaths()` 函数
  - `internal/chatcore/provider_loop.go`：通过 `EventWarning` 事件发送 warning
- [x] 为 OpenAI 协议路径增加图片输入适配
  - `internal/llm/reasoning_helpers.go`：`buildOpenAIProtocolMessage` 在 user message 中构造 `image_url` content parts
- [x] 为 Anthropic 协议路径增加图片输入适配
  - `internal/llm/reasoning_helpers.go`：`buildAnthropicProtocolMessage` 在 user message 中构造 `image` + `source.base64` blocks
  - 需要将 `messageMetadata` 参数传入该函数
- [x] 为 Gemini 协议路径增加图片输入适配
  - `internal/llm/reasoning_helpers.go`：`buildGeminiProtocolMessage` 在 user message 中构造 `inline_data` parts
  - 需要将 `messageMetadata` 参数传入该函数
- [x] 为交互式会话增加"当前待发送附件"提示与管理命令
  - `cmd/aicli/commands/command.go`：`/image [path]` 添加、`/image` 查看、`/image clear` 清空
  - `cmd/aicli/commands/chat.go`：ImagePaths 在消息发送后清空（而非下一轮迭代开始时）
  - `cmd/aicli/ui/input.go`：`FormatUserPromptWithAttachments()` 提示符显示待发送附件数量
  - `cmd/aicli/commands/chat_interaction.go`：使用带附件数的提示符
  - `cmd/aicli/commands/chat_runtime_events.go`：使用带附件数的提示符
- [x] Actor 路径支持显式图片附件传递
  - `internal/chat/commands.go`：`SubmitPrompt.ImagePaths` 字段
  - `internal/chat/actor.go`：`SubmitPromptOption` 结构体（含 `ImagePaths` + `ImageArtifactDir`）
  - `internal/chat/actor.go`：`handleSubmitPrompt` 使用 `NewUserPromptMessageWithImages`
  - `cmd/aicli/commands/chat_actor_executor.go`：传递 `session.ImagePaths` + artifact dir
- [x] Session/replay 图片附件持久化保真
  - `internal/llm/multimodal_input.go`：`PersistLocalInputImages()` 函数复制图片到 session artifact 目录
  - `internal/chatcore/provider_loop.go`：`ToolLoopRequest.ImageArtifactDir` 字段 + 创建消息后调用持久化
  - `internal/chat/actor.go`：`handleSubmitPrompt` 中对 `cmd.ImageArtifactDir` 调用持久化
  - `cmd/aicli/commands/chat_core.go`：`chatSessionImageArtifactDir()` 辅助函数
  - 持久化路径：`<SessionDir>/<sessionID>.artifacts/images/`
  - 持久化后更新 `ResolvedPath` 指向副本，`Source` 追加 `+persisted` 标记
- [x] 将图片输入抽象上移到统一 runtime request/message 模型
  - `internal/types/message.go`：新增 `ContentPart` 类型（`ContentPartText` | `ContentPartImage`）
  - `internal/types/message.go`：`Message.ContentParts []ContentPart` 字段 + `HasContentParts()` / `ImageContentParts()` 方法
  - `internal/types/message.go`：`Message.GetInputImagesMetadata()` 桥接方法
  - `internal/types/message.go`：`Clone()` 方法同步复制 `ContentParts`
  - `internal/llm/provider.go`：`llm.Message.ContentParts` 字段
  - `internal/llm/multimodal_input.go`：`NewUserPromptMessageWithImages` 同时填充 `ContentParts` 和 `Metadata`
  - `internal/llm/reasoning_helpers.go`：`buildProtocolMessageMap` 接收 `contentParts` 参数（预留直通路径）
  - 当前协议构建器仍通过 metadata sideband 读取图片，`ContentParts` 为未来重构预留直通路径

## 建议继续推进

- [ ] 将协议构建器从 metadata sideband 迁移到 ContentParts 直通路径
  - 当前 `buildOpenAIProtocolMessage` / `buildAnthropicProtocolMessage` / `buildGeminiProtocolMessage` 仍通过 `ExtractLocalInputImages(messageMetadata)` 读取
  - 可改为优先从 `ContentParts` 读取图片，仅当 `ContentParts` 为空时 fallback 到 metadata
- [ ] 为交互式会话增加更丰富的附件管理
  - `/image remove <n>` 按序号移除
  - `/image info` 显示图片详细信息（大小、分辨率等）
- [ ] Session 加载时校验已持久化图片完整性
  - 检查 artifact 目录中的图片文件是否存在
  - 若缺失则尝试从原始路径回退

## 验证记录

已执行并通过的验证：

- `go build ./...`（全量构建验证）
- `go test ./internal/llm/... ./internal/chatcore/... ./internal/chat/... ./internal/types/... -count=1`（核心模块测试）
- `go test ./cmd/aicli/commands/... -count=1`（CLI 命令测试）
- `go test ./internal/llm/... -run TestPersistLocalInputImages -v`（新增持久化测试）

## 说明

本次在上一阶段基础上完成了四项关键推进：

1. **交互式附件管理**：`/image` 命令支持在交互模式下动态添加/查看/清空图片附件，提示符显示待发送附件数量
2. **Actor 路径图片支持**：`SubmitPromptOption` 结构体统一传递图片路径和 artifact 目录，`handleSubmitPrompt` 使用 `NewUserPromptMessageWithImages`
3. **Session 图片持久化**：`PersistLocalInputImages()` 将原始图片复制到会话 artifact 目录，session replay 不再依赖原始绝对路径
4. **结构化消息模型**：`ContentPart` / `ContentParts` 类型使图片成为消息的一等公民，为后续协议构建器直通路径重构奠定基础
