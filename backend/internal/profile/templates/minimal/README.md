# {{name}}

{{description}}

由 `aicli profile create <name> --template minimal` 生成。

## 裁剪了什么

- 工具面：读取/检索 + 基础编辑（allowlist: view / grep / glob / ls / write / edit / apply_patch）
- `read_only: false`：允许写操作，agent 可完成基础的文件编辑
- denylist：shell / bash / aicli_exec（不提供命令执行；deny 恒优先）
- skills：`denylist: ["*"]`，全部技能关闭
- MCP：`exclude_servers: ["*"]`，所有服务器在连接前被排除
- 提示词：最短 role.md

## 用法

```bash
aicli chat --profile {{name}}
aicli profile validate {{name}}
```

需要更完整的编辑/命令能力时，改用 `coding` / `docs` 模板。
