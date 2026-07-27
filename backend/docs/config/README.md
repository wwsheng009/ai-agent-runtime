# Provider / Runtime 配置说明

本目录记录 `aicli` / runtime 使用的 provider 配置约定，以及与能力开关相关的字段说明。

实际配置文件通常位于：

- `backend/configs/config.yaml`：主配置
- `backend/configs/config.runtime.snapshot.yaml`：运行时快照
- `backend/configs/model_cards.yaml`：模型卡片能力模板
- `~/.aicli/config.yaml` 或用户本地覆盖配置

## 文档索引

| 文档 | 说明 |
|------|------|
| [enable_image_generation.md](./enable_image_generation.md) | Codex 原生 `image_generation` 的 **provider 级 opt-in** 开关 |
| [examples/codex-native-image-generation.yaml](./examples/codex-native-image-generation.yaml) | 可复制的 Codex 原生图片生成配置示例 |

## 相关实现

- Provider 结构：`backend/internal/agentconfig/config.go`
- 图片能力判定：`backend/internal/agentconfig/images.go`
- 请求注入：`backend/internal/llm/codex_image_generation.go`
- 配置持久化：`backend/internal/agentconfig/provider_persistence.go`

## 相关产品文档

- `docs/codex/image-generation-capability-flow.md`：能力解析与请求注入链路
- `docs/aicli/tool_image_generate.md`：`openai_image_generate` / `aicli image` 使用说明
