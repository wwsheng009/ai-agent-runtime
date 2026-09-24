# {{name}}

{{description}}

由 `aicli profile create <name> --template review` 生成。

## 裁剪了什么

- 工具面：仅 view / grep / glob / ls / web_search / fetch（allowlist）
- `read_only: true`：写操作被拒绝
- MCP：`exclude_servers: ["*"]`，所有服务器在连接前被排除

## 用法

```bash
aicli chat --profile {{name}}
aicli profile validate {{name}}
```
