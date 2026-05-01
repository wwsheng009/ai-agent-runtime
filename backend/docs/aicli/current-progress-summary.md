# aicli 当前进度总结（更新版）

## 范围

本次总结聚焦 **aicli 与 Codex 图像/多模态能力** 的当前实现状态，重点回答四个问题：

1. 现在已经打通了什么能力？
2. 距离“像 Codex CLI 一样支持 `--image` 附图对话/编辑”还差什么？
3. 参考上游 Codex 项目后，推荐采用什么落地路径？
4. 如果希望“更自动化”，应该把自动化放在哪一层？

---

## 本次更新补充了什么

相比上一版总结，这次额外补充了 **Codex 上游输入侧实现路径** 的对照分析，重点参考了：

- `E:/projects/ai/codex/codex-rs/utils/cli/src/shared_options.rs`
- `E:/projects/ai/codex/codex-rs/cli/src/main.rs`
- `E:/projects/ai/codex/codex-rs/protocol/src/user_input.rs`
- `E:/projects/ai/codex/codex-rs/core/src/prompt_debug.rs`

新增结论是：

> **上游 Codex 的关键并不是“把图片路径写进 prompt 文本”，而是“先把图片作为结构化用户输入收进来，再在协议序列化阶段转换成模型可见的 image part”。**

这对 aicli 的意义很直接：

- 不能只补一个 `--image` flag 就算完成；
- 也不建议只靠“从 prompt 文本里猜测图片路径”来实现；
- 最合理的是采用 **“显式附件优先 + 自动化辅助”** 的混合方案。

---

## 一句话结论

**当前代码已经基本打通了 Codex 原生 `image_generation` 工具的输出侧链路（能力声明、请求注入、响应解析、图片落盘、会话回放），但“用户上传图片参与对话”的输入侧链路仍未完成。**

更准确地说：

- 现在更接近“**让模型生成图片并把结果保存下来**”；
- 还不是“**用户带图发起多模态对话/分析/编辑**”。

---

## 已完成的部分

### 1. Codex 图像生成能力已经接入到工具层

相关文件：

- `internal/llm/codex_image_generation.go`
- `cmd/aicli/commands/chat_provider_turn.go`
- `internal/agent/loop.go`
- `configs/config.yaml`

当前实现里，Codex provider 已经可以基于模型能力自动注入原生工具：

- `BuildToolDefinitionsForRequest(...)` 会在满足条件时追加 `image_generation` native tool；
- 条件不是“只要是 codex 就开”，而是：
  - protocol = `codex`
  - 模型 capability 显式声明支持 image generation
  - input modalities 同时包含 `text` 和 `image`

配置层也已经有对应能力声明，例如 `configs/config.yaml` 里 `gpt-5.4` 被标记为：

- `input_modalities: [text, image]`
- `native_tools.image_generation: true`

这说明 **模型能力探测与工具注入机制已经具备**。

---

### 2. aicli / agent / gateway 三条路径都能把生成图片结果落盘

相关文件：

- `internal/llm/codex_image_generation.go`
- `internal/llm/provider.go`
- `internal/llm/gateway_client.go`
- `cmd/aicli/commands/chat_generated_images.go`
- `internal/agent/loop.go`

当前实现已经覆盖了生成图片结果的后处理：

- 从 Codex `image_generation_call` 响应里提取 base64 结果；
- 保存到本地目录；
- 计算 `sha256`、字节数等元数据；
- 在 assistant message / response metadata 中写入 `generated_images`；
- 生成一个面向用户的摘要文本，如 `Generated image saved to ...`。

目录也不是随便写到临时位置，而是已经做了会话级组织：

- aicli chat 会写到当前 session 的 `generated-images` 目录；
- agent loop 会按 session id 派生输出目录。

这意味着 **“图片生成结果可用、可存、可追溯”** 这一段已经比较完整。

---

### 3. Codex 响应的 replay / session 保真度已经明显增强

相关文件：

- `cmd/aicli/commands/chat_session.go`
- `internal/llm/reasoning_helpers.go`
- `internal/llm/codex_image_generation.go`

当前代码已经在会话持久化和回放上做了不少铺垫：

- runtime message metadata 中已经保留结构化的 `reasoning_details`、`response_output_items` 等字段；
- assistant reasoning metadata 中会保留 `response_output_items`；
- 即便 `tool_calls` 没直接落到标准字段，也能从 Codex output items 中恢复；
- 对图片生成结果，保存到磁盘后会把 replay metadata 里的大块 base64 去掉，避免历史膨胀；
- 在需要重新构造 Codex replay payload 时，又能根据已保存文件把结果重新 hydrate 回去。

这部分很关键，因为它说明当前实现 **不是一次性“看完就丢”的图片处理**，而是在朝“可恢复、可续聊、可回放”的方向走。

---

### 4. 已有较完整测试覆盖

相关文件（示例）：

- `cmd/aicli/commands/chat_core_test.go`
- `cmd/aicli/commands/chat_session_test.go`
- `internal/llm/codex_image_generation_test.go`
- `internal/llm/provider_wrapper_test.go`
- `internal/llm/gateway_client_test.go`
- `internal/llm/reasoning_helpers_test.go`
- `internal/agent/loop_test.go`

从测试分布看，下面这些点都已经有验证：

- native `image_generation` tool 注入；
- generated image 保存与 metadata 回填；
- reasoning / `response_output_items` 保留；
- replay 时恢复 output items；
- agent loop 为图片输出设置目录。

因此，**“生成图片后如何处理”这一段不只是概念上支持，而是有测试兜底的。**

---

## 当前最大的缺口

### 1. aicli 还没有 `--image` / 图片附件入口

相关文件：

- `cmd/aicli/main.go`
- `cmd/aicli/commands/chat_options.go`
- 对照参考：`E:/projects/ai/codex/codex-rs/utils/cli/src/shared_options.rs`

当前 `aicli chat` 已有大量 flag：`--provider`、`--model`、`--message`、`--stream`、`--session` 等，但 **没有图片输入相关 flag**。

而上游 Codex CLI 的 `SharedCliOptions` 已经明确有：

- `--image`
- `-i`
- 支持多个文件
- 支持将图片附件作为独立输入源，而不是要求用户把路径写进 prompt

这说明 **CLI 入口层还没开始真正接收用户图片**。

---

### 2. runtime 数据结构仍然是“纯文本消息模型”

相关文件：

- `internal/types/message.go`
- `internal/types/request.go`
- `internal/llm/runtime.go`

当前核心结构仍然是：

- `types.Message.Content string`
- `types.Request.Prompt string`
- `llm.LLMRequest.Messages []types.Message`

也就是说，**运行时主数据模型仍假设消息只有一段文本**。

虽然 `internal/types/codex/types.go` 里已经定义了 Codex 的 `ContentPart`，并支持 `input_text` / `input_image`，但它目前更像是 **协议层结构体准备好了**，并没有真正贯穿到 aicli / agent / runtime 主干类型中。

这是目前最核心的架构缺口。

---

### 3. Codex adapter 发送 user message 时仍只会构造 `input_text`

相关文件：

- `internal/llm/adapter/codex.go`
- `internal/llm/reasoning_helpers.go`

`convertMessagesToCodexInput(...)` 当前对 user message 的处理方式是：

- 读取文本 content；
- 固定生成 `{"type":"input_text","text":...}`；
- 没有把本地图片路径、data URL 或附件对象转换成 `input_image` part。

同时，`providerMessageToAdapterMessage(...)` 的输入仍来自 `types.Message`，而这个结构本身也只有字符串内容。

所以目前即便底层 Codex 协议类型支持 `input_image`，**请求构造链路也还没真正用起来**。

---

### 4. actor / agent 接口还是“prompt string”风格

相关文件：

- `internal/chat/actor.go`
- `internal/agent/agent.go`

当前关键入口仍然是：

- `SubmitPrompt(ctx, prompt string, ...)`
- `Agent.Run(ctx, prompt string)`

这会带来一个很现实的问题：

> 就算 CLI 层加了 `--image`，图片附件也没有自然的位置继续往下传。

也就是说，**不仅 CLI 缺 flag，整个调用栈目前也缺一个“富消息输入对象”**。

---

## 参考 Codex 上游后的更准确判断

结合上游实现，可以更清楚地看到 Codex 的输入侧分层：

### 1. CLI 层先显式收集图片，而不是从 prompt 猜

在 `shared_options.rs` 中，上游直接定义：

- `--image`
- `-i`
- `images: Vec<PathBuf>`

这意味着“图片是显式附件”，不是普通文本的一部分。

### 2. 运行时使用结构化 `UserInput`

在 `protocol/src/user_input.rs` 中，上游并不是只保留字符串 prompt，而是有：

- `UserInput::Text`
- `UserInput::Image`
- `UserInput::LocalImage`

尤其关键的是：

> `LocalImage` 表示“用户给的是本地文件路径”，它会在后续序列化时再转换成模型真正需要的图片输入格式。

### 3. 请求构造阶段再把结构化输入变成模型可见内容

在 `cli/src/main.rs` 中，上游会把 `images` 转成 `UserInput::LocalImage`；
在 `core/src/prompt_debug.rs` 中，`build_prompt_input(...)` 再基于 session / model modality 构造最终 prompt input。

这说明上游采用的是：

**CLI 显式附件 → 运行时结构化输入 → 协议层序列化为模型输入**

而不是：

**把路径拼进 prompt 文本 → adapter 再去猜是不是图片**。

---

## 对 aicli 的设计启示

### 不建议的方案：只在 prompt 中自动识别图片路径

这种方案表面上“更自动”，但问题很多：

1. **语义不稳定**：用户写 `/tmp/a.png` 可能只是想讨论这个路径，不一定是要上传图片。
2. **误判成本高**：一旦误把本地路径当附件发送，可能产生隐私或安全问题。
3. **session/replay 难保真**：历史里如果只剩一段文本，很难稳定恢复“当时到底附了哪张图”。
4. **跨 provider 难兼容**：不同 provider 对图片输入支持不同，纯文本猜测会让降级行为很混乱。
5. **工程边界不清**：CLI、runtime、adapter 都会开始夹杂“猜测逻辑”，后续很难维护。

### 更推荐的方案：显式附件优先 + 自动化辅助

推荐把“自动化”放在 **辅助体验层**，而不是放在“输入语义定义层”。

也就是说：

- **主契约**：用户通过 `--image` / `-i` 明确声明附件；
- **辅助能力**：在交互模式里可以做自动补全、路径校验、拖拽文件、粘贴路径后确认是否附加等增强；
- **最终发送语义**：仍然以结构化附件对象为准，而不是以 prompt 里的某段文本为准。

这是更接近上游 Codex、也更适合长期演进的方向。

---

## 推荐落地方案

## 方案 A：目标架构（推荐最终形态）

### 1. CLI 层补齐显式附件入口

优先在 `aicli chat` 增加：

- `--image`
- `-i`
- 支持多次传入 / 多文件
- 基本校验：文件存在、可读、类型、大小限制
- 相对路径按当前工作根解析

目标文件：

- `cmd/aicli/main.go`
- `cmd/aicli/commands/chat_options.go`

### 2. runtime 主干引入“富输入模型”

建议不要继续只靠：

- `Message.Content string`
- `Request.Prompt string`

而是引入统一的输入分片模型，例如：

- text part
- image part

或者引入类似上游 `UserInput` 的结构化类型。

目标文件：

- `internal/types/message.go`
- `internal/types/request.go`
- `internal/llm/runtime.go`

### 3. adapter 层负责协议序列化

在 `internal/llm/adapter/codex.go` 中，把 user message 构造成混合 content：

- `input_text`
- `input_image`

而不是继续只发 `input_text`。

也就是说，**图片路径不应在 CLI 层就直接变成协议细节**，而应该在 adapter / provider 序列化阶段落成目标格式。

### 4. session / replay 层补齐附件保真

需要明确：

- 用户图片如何持久化；
- resume 时如何恢复；
- replay 时如何重建图片输入；
- 历史里是保存原始路径、复制后的 artifact 引用，还是规范化后的附件元数据。

建议采用“会话内可追溯引用”而不是只依赖原始绝对路径，这样更稳定。

### 5. provider 行为要显式定义

当用户传了图片，但 provider / model 不支持 image input 时，需要明确策略：

- 直接报错；
- 显式降级并提示；
- 或仅在支持图像输入的 provider 上启用。

这里不建议静默忽略。

---

## 方案 B：最小可用路径（适合先快速打通）

如果当前目标是 **先尽快让 `aicli chat --image ...` 在 Codex provider 上跑通**，可以分两步走：

### 第一步：先用旁路字段打通首轮输入

短期内可以先不立刻重构所有 runtime 主干类型，而是：

- CLI 层接收 `--image`；
- 将图片附件先放入 `Request.Options` 或 `Request.Metadata` 的结构化字段；
- provider / adapter 在 Codex 路径上读取该字段并序列化为 `input_image`。

这样做的价值是：

- 改动面更小；
- 能先验证端到端可用性；
- 能尽快补测试和交互。

但必须明确：

> 这只是过渡方案，不是最终架构。

因为它会继续保留“文本主干 + 附件旁路”的双轨模型，长期看不够干净。

### 第二步：再把附件升级为一等公民

等 MVP 稳定后，再把 `Message` / `Request` / `Actor` / `Agent` 的输入接口升级为结构化富输入对象。

这样既能加快落地，也不会把临时方案永久固化。

---

## 自动化建议：应该自动到什么程度

### 推荐答案

**推荐“显式附件优先，自动化作为辅助”，而不是“纯自动识别图片路径”。**

### 原因

1. **可预测**：用户明确知道哪些文件会被发送。
2. **可测试**：CLI、runtime、adapter 的行为更容易写测试。
3. **可回放**：session / replay 容易还原附件语义。
4. **可扩展**：后续不只是图片，还可以扩展到音频、PDF、文档附件。
5. **更贴近上游**：与 Codex 的 `SharedCliOptions.images` + `UserInput::LocalImage` 设计一致。

### 合适的自动化增强

可以考虑做这些“安全的自动化”：

- 自动判断 MIME 类型；
- 自动做大小检查与错误提示；
- 交互模式支持拖拽图片文件；
- 粘贴本地图片路径时，询问“是否作为附件加入本轮消息”；
- 多图自动去重或规范化路径；
- 支持 `--image a.png,b.png` 与多次 `-i` 混用。

### 不太建议的自动化

不建议默认做这些事：

- 无提示扫描 prompt 文本并自动上传命中的本地路径；
- 根据类似 `![img](path)` 或普通字符串路径直接默默转附件；
- 在 provider 不支持图片时静默丢弃附件。

---

## 当前状态怎么判断

### 可以认为“已经完成”的能力

1. **Codex 图像生成工具接入**
2. **生成结果本地落盘与元数据记录**
3. **session/replay 对生成图片结果的保真**
4. **agent / gateway / provider 多路径一致处理**

### 还不能认为“已经完成”的能力

1. **用户附图发起多模态对话**
2. **用户附图做图像理解/分析**
3. **用户附图做图像编辑指令**
4. **aicli 对齐 Codex CLI 的 `--image` 交互方式**
5. **runtime 主干对附件输入的一等公民支持**

---

## 建议的实施顺序

### 第一阶段：补 CLI 显式入口

先增加：

- `--image`
- `-i`
- 多图支持
- 基础路径校验

### 第二阶段：补一条可工作的 Codex 输入侧链路

先让 Codex provider 能把图片附件发出去，即便暂时通过 `Request.Metadata` / `Request.Options` 过渡。

### 第三阶段：补 session / replay 附件持久化

保证附件不会只停留在一次性请求中，而是能被恢复和续聊。

### 第四阶段：重构 runtime 为富输入模型

把图片从“旁路字段”升级为正式输入结构。

### 第五阶段：补交互自动化体验

在确保显式语义稳定后，再做：

- 拖拽
- 自动 MIME
- 路径确认
- 交互提示

这样顺序更稳，风险也更可控。

---

## 最终判断

**当前进度可以概括为：Codex 图像生成结果链路已经基本完成；Codex 图片输入链路仍未完成。**

如果进一步结合上游 Codex 的设计方式，那么更完整的判断应当是：

> **aicli 当前已经完成了 Codex 图像能力的“输出侧”集成，但“输入侧”要想做对，应该采用“CLI 显式附件 + runtime 结构化输入 + adapter 协议序列化 + session/replay 保真”的实现路径。**

再简化成一句话就是：

> **现在已经能“生成图并保存”，还不能真正“带图聊天”；而且“带图聊天”的最佳实现方式不是猜 prompt 里的路径，而是补齐结构化附件输入链路。**
