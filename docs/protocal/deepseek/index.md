# DeepSeek 文档索引

本目录内容抓取自 DeepSeek 官方中文文档站：`https://api-docs.deepseek.com/zh-cn/`。

说明：
- DeepSeek 的主 API 形态整体与 OpenAI 兼容接口接近，核心路径包括 `/chat/completions`、`/completions`、`/models` 等。
- DeepSeek 也提供自身扩展能力，例如 `thinking`、`reasoning_effort`、JSON Mode、Tool Calls、KV Cache 和 Coding Agent 接入。
- Beta 能力使用单独入口：`https://api.deepseek.com/beta`。
- Anthropic 兼容入口使用：`https://api.deepseek.com/anthropic`。

## 本仓库接入结论

- 不建议在本仓库中新增 `deepseek` 或 `deepseek_beta` 协议枚举；主接口继续走现有 `openai` 协议适配层。
- DeepSeek Anthropic 兼容入口继续走现有 `anthropic` 协议适配层。
- Beta 能力本质上仍是“同一供应商的兼容接口变体”，建议通过 provider 配置覆盖 `base_url` 与 `api_path`，而不是新增一套协议实现。
- 对话前缀续写、`reasoning_content`、`thinking`、自定义 `tool_choice` 等 DeepSeek 扩展，应该作为 OpenAI 兼容请求/消息字段的受控透传能力处理。

示例：

```yaml
providers:
  items:
    deepseek_beta:
      protocol: openai
      base_url: https://api.deepseek.com/beta
      api_path: /v1/chat/completions
      forward_url: /v1/chat/completions
```

如果要走 FIM / Completion Beta 接口，则仅需要把 `api_path` 和 `forward_url` 切换为 `/v1/completions`，不需要新增协议类型。

## OpenAI 兼容 API

- [对话补全](create-chat-completion.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
- [FIM 补全（Beta）](create-completion.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/api/create-completion
- [列出模型](list-models.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/api/list-models
- [查询余额](get-user-balance.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/api/get-user-balance

## DeepSeek 特性

- [思考模式](thinking_mode.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/thinking_mode
- [多轮对话](multi_round_chat.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/multi_round_chat
- [JSON Output](json_mode.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/json_mode
- [Tool Calls](tool_calls.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/tool_calls
- [上下文硬盘缓存](kv_cache.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/kv_cache
- [接入 Coding Agents](coding_agents.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/coding_agents

## DeepSeek Beta 接口

- [对话前缀续写（Beta）](chat_prefix_completion.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/chat_prefix_completion
- [FIM 补全（Beta）](fim_completion.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/fim_completion

## Anthropic 兼容

- [Anthropic API](anthropic_api.md)
  - 官方链接：https://api-docs.deepseek.com/zh-cn/guides/anthropic_api

## 工具

- [同步脚本](sync_docs.py)
