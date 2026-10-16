# full — 全量恢复模板

- 用途：从窄面 profile（minimal/review/自定义 allowlist）恢复**全部工具**，
  形成"裁剪 → 恢复"闭环。
- 工具面：`tools.allowlist: ["*"]` 显式放行一切（含运行时控制面
  `job_*` / `plan_*` / `subagent_*` / `team_*` 等——2026-10-16 起 essential
  豁免已移除，窄 allowlist 会真裁掉它们，恢复需显式通配或本模板）。
- 无 denylist、无 disabled_capabilities、`read_only: false`。
- 会话内恢复：`/profile use <本 profile>`（下一 turn 边界生效，actor 按新面重建）；
  或 `/profile off` 回到无 profile 基线（同为全量）。
- 命令行生成：`aicli profile create <name> --template full`。
