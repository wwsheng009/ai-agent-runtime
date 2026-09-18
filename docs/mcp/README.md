# MCP 集成文档

本目录收录 `ai-agent-runtime` 常用的 MCP（Model Context Protocol）服务器接入文档：配置方式、使用方法与注意事项。

| 文档 | 说明 |
|------|------|
| [mcp-tool-llm-integration.md](mcp-tool-llm-integration.md) | MCP 工具如何以原生 `tools` 暴露给 LLM API（含多服务重名隔离机制与验证） |
| [chrome-devtools.md](chrome-devtools.md) | Google 官方 `chrome-devtools-mcp`：浏览器自动化/调试（支持连接已打开的 Chrome/Edge） |
| [chrome-devtools.mcp.yaml.example](chrome-devtools.mcp.yaml.example) | 可直接拷贝的 `chrome-devtools` MCP 配置模板（attach / launch / browser-url 三种模式） |

通用命令与配置约定（`aicli mcp add|list|status|tools|test|reload`、配置文件优先级等）见 [docs/aicli/install.md](../aicli/install.md#mcp-服务器) 与 [docs/user-guide/aicli.md](../user-guide/aicli.md)。

---

## 快速开始（连接已打开的浏览器）

```bash
# 1) 在 Chrome/Edge(144+) 打开 chrome://inspect/#remote-debugging，勾选允许远程调试
# 2) 配置 .aicli/mcp.yaml（见 chrome-devtools.md 第 3 节）
# 3) 验证连接
aicli mcp list                          # 状态应为 connected
aicli mcp test chrome-devtools list_pages '{}'
```

> ⚠️ attach 模式下 agent 可读写你真实浏览器的所有已打开页面（含登录态），请先阅读
> [chrome-devtools.md](chrome-devtools.md) 第 7 节「注意事项」。
