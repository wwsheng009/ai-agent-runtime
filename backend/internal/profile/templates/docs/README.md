# {{name}}

{{description}}

由 `aicli profile create <name> --template docs` 生成。

## 裁剪了什么

- 工具面：文件读写 + 搜索 + 命令执行（allowlist）
- skills：默认不声明（全部可见）；如需只暴露文档技能，取消 profile.yaml 中的注释

## 用法

```bash
aicli chat --profile {{name}}
aicli profile validate {{name}}
```
