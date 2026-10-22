# {{name}}

{{description}}

由 `aicli profile create <name> --template remote` 生成。

## 它解决什么

远程工具模式下有两个容易出事的地方：

1. **同名冲突**：远程 server 若暴露 `write` 而本地也有 `write`，模型以为在改远端、
   实际落在本机。这个 profile 把冲突的本地工具全部 deny。
2. **多 server 混入**：`mcp.yaml` 里通常还配着其他 server。`mcp.use_servers`
   只保留一个，其余在建连前就被过滤（不连接、不注册工具）。

## 用前必改

`profile.yaml` 里的 `mcp.use_servers` 默认写的是 `wsl`，改成你自己的 server 名：

```yaml
mcp:
  use_servers:
    - <你的 server 名>
```

该 server 还需要在 `mcp.yaml` 里声明 `executionScope: remote`，
否则远程写/命令会被本地策略拒绝。

## 用法

```bash
aicli profile validate {{name}}
aicli profile show {{name}}          # 看最终生效的工具面与 MCP 启停
aicli chat --profile {{name}}
```

## 局限

`tools.denylist` 是**黑名单**：它列出的是已知的本地工具。将来新增的本地工具
不会自动被排除，需要有人补进列表。

需要"本地工具在结构上不存在"的强保证时，用独占模式：

```bash
aicli chat --mcp-only <server 名>
```

两者可以叠加：profile 负责声明意图与 MCP 收窄，`--mcp-only` 负责结构性隔离。
