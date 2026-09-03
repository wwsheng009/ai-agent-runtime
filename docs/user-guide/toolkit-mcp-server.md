# toolkit-mcp-server 使用手册

> 对应程序：`cmd/toolkit-mcp-server`（`toolkit-mcp-server` / `toolkit-mcp-server.exe`）
> 作用：将 `ai-agent-runtime` 内置的 toolkit 工具（文件读写、搜索、shell、patch 等）通过 MCP（Model Context Protocol）暴露为 MCP 服务，供支持 MCP 的客户端集成。

---

## 目录

1. [概述](#1-概述)
2. [传输协议](#2-传输协议)
3. [参数](#3-参数)
4. [暴露的工具列表](#4-暴露的工具列表)
5. [示例](#5-示例)
6. [相关文档](#6-相关文档)

---

## 1. 概述

`toolkit-mcp-server` 是一个 **MCP（Model Context Protocol）服务器**，将 toolkit 中 16 个内置工具（文件读写、文件搜索、Shell 命令、补丁应用、网络下载等）以 MCP 标准接口暴露。支持 MCP 的客户端（如 `aicli mcp add`）可以连接并调用这些工具。

**典型用途**：

- 将 ai-agent-runtime 的工具能力集成到支持 MCP 的 IDE 或 AI 编排框架中
- 在进程内通过 `aicli mcp add` 注册为本地 MCP server，扩展 aicli 的工具集
- 开发与调试 MCP 工具接入

---

## 2. 传输协议

使用 **stdio** 传输（MCP 标准输入输出传输），通过 stdin/stdout 交换 JSON-RPC 消息。启动后等待客户端通过 stdin/stdout 连接，无需网络端口。

---

## 3. 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-tools <names>` | （全部） | 要暴露的工具列表，逗号分隔。为空时暴露全部 |
| `-exclude <names>` | （无） | 要排除的工具列表，逗号分隔。优先级低于 `-tools` |

---

## 4. 暴露的工具列表

默认暴露全部 16 个工具。可通过 `-tools` 与 `-exclude` 筛选：

| 工具名 | 说明 |
|--------|------|
| `shell` | 执行 Shell 命令 |
| `bash` | 执行 Bash 命令 |
| `view` | 查看文件内容 |
| `apply_patch` | 应用补丁 |
| `edit` | 编辑文件 |
| `write` | 写入文件 |
| `append_write` | 追加写入文件 |
| `glob` | 文件路径模式匹配 |
| `grep` | 文件内容搜索 |
| `ls` | 列出目录内容 |
| `download` | 下载 URL 内容 |
| `fetch` | 获取 URL 内容 |
| `multiedit` | 多替换编辑 |
| `todos` | 任务列表管理 |
| `sourcegraph` | Sourcegraph 代码搜索 |
| `web_search` | 网络搜索 |

---

## 5. 示例

```bash
# 启动服务器（暴露全部工具）
toolkit-mcp-server

# 仅暴露 shell、view、grep 三个工具
toolkit-mcp-server -tools shell,view,grep

# 暴露全部工具，但排除 bash 和 web_search
toolkit-mcp-server -exclude bash,web_search

# 在 aicli 中注册为 MCP 工具
aicli mcp add toolkit-mcp toolkit-mcp-server
aicli mcp tools toolkit-mcp
```

启动后输出：

```
========================================
  Toolkit MCP Server
========================================
已注册工具: 16 个
工具列表:
  - shell
  - bash
  - view
  - apply_patch
  ...
========================================
等待客户端连接...
```

---

## 6. 相关文档

- [echo-mcp-server 使用手册](echo-mcp-server.md) — MCP 演示服务器
- [docs/aicli/faq.md](../aicli/faq.md) — MCP 常见问题
- [aicli 使用手册](aicli.md) — `aicli mcp` 子命令