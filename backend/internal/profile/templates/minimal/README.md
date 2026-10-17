# {{name}}

{{description}}

由 `aicli profile create <name> --template minimal` 生成。

## 裁剪了什么

- 工具面：读取/检索 + 基础编辑 + 命令执行（allowlist: view / grep / glob / ls / write / edit / apply_patch / shell）
- `read_only: false`：允许写操作与命令执行，agent 可完成基础的文件编辑与命令
- skills：`denylist: ["*"]`，全部技能关闭
- MCP：`exclude_servers: ["*"]`，所有服务器在连接前被排除
- 提示词：最短 role.md

## 用法

```bash
aicli chat --profile {{name}}
aicli profile validate {{name}}
```

需要更完整的编辑/命令能力，或需要子代理/团队等控制面工具时，改用 `coding` / `docs` / `default` 模板。
