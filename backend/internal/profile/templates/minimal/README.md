# {{name}}

{{description}}

由 `aicli profile create <name> --template minimal` 生成。

## 裁剪了什么

- 工具面：仅 view / grep（allowlist），`read_only: true`
- skills：`denylist: ["*"]`，全部技能关闭
- MCP：`exclude_servers: ["*"]`，所有服务器在连接前被排除
- 提示词：最短 role.md

## 用法

```bash
aicli chat --profile {{name}}
aicli profile validate {{name}}
```
