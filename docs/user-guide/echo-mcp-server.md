# echo-mcp-server 使用手册

> 对应程序：`cmd/echo-mcp-server`（`echo-mcp-server` / `echo-mcp-server.exe`）
> 作用：MCP Model Context Protocol）的 WebSocket Echo 服务器，用于测试 MCP 客户端连接与消息收发。

---

## 目录

1. [概述](#1-概述)
2. [参数](#2-参数)
3. [示例](#3-示例)
4. [协议说明](#4-协议说明)
5. [相关文档](#5-相关文档)

---

## 1. 概述

`echo-mcp-server` 是一个演示用 MCP（Model Context Protocol）服务器，运行在 WebSocket 上，实现了标准的 `initialize` / `tools/list` / `tools/call` 方法。用于：

- 验证 MCP 客户端的 WebSocket 连接与握手流程
- 测试 `aicli mcp add/test` 等 MCP 客户端命令
- 开发环境中调试 JSON-RPC 消息收发

---

## 2. 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-addr <host:port>` | `localhost:8080` | WebSocket 服务监听地址 |

---

## 3. 示例

```bash
# 默认地址启动
echo-mcp-server

# 指定端口启动
echo-mcp-server -addr 127.0.0.1:9090
```

启动后输出：

```
Echo MCP Server is running on ws://localhost:8080/mcp
Press Ctrl+C to stop
```

---

## 4. 协议说明

- 传输层：WebSocket（`ws://addr/mcp`）
- 消息格式：MCP 协议 JSON-RPC 消息（`jsonrpc`、`id`、`method`、`params`）
- 支持的方法：`initialize`（协议握手）、`tools/list`（工具列表）、`tools/call`（工具调用）；未知方法不回复
- 暴露的工具：

| 工具 | 参数 | 返回 |
|------|------|------|
| `echo` | `message`（string，必填） | `Echo: <message>` |
| `add` | `a`、`b`（number，必填） | 两个数之和 |

---

## 5. 相关文档

- [toolkit-mcp-server 使用手册](toolkit-mcp-server.md)
- [docs/aicli/faq.md](../aicli/faq.md) — MCP 常见问题