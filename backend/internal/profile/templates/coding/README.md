# {{name}}

{{description}}

由 `aicli profile create <name> --template coding` 生成。

## 目录约定

- `profile.yaml` — profile 声明（工具面 / skills / mcp / prompts / agents）
- `agents/<id>/agent.yaml` — agent 级声明（provider/model/tools 覆盖）
- `agents/<id>/prompts/role.md` — agent 角色提示词

## 用法

```bash
aicli chat --profile {{name}}
aicli profile show {{name}}
aicli profile validate {{name}}
```

工具面为全量基线：如需裁剪，参考 `review` / `minimal` / `docs` 模板的 allowlist 写法。
