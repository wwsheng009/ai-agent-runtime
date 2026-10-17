# {{name}}

{{description}}

由 `aicli profile create <name> --template default` 生成。

## 定位

介于 `minimal` 与 `full` 之间的**常用工具中间面**：覆盖日常读/搜/改/执行、联网
取回/检索、产物读取、计划与目标面，以及后台任务与子代理委派；但不像 `minimal`
那样关闭技能与 MCP，也不像 `full` 那样全量放行。

## 工具面

- allowlist（只收窄，硬闸门；支持 `prefix_*` 通配）：
  - 读取/检索：`view` / `grep` / `glob` / `ls`
  - 编辑：`write` / `edit` / `multiedit` / `apply_patch` / `append_write`
  - 命令执行：`shell` / `aicli_exec`
  - 网络：`fetch` / `web_search` / `download`
  - 产物：`artifact_read`
  - 控制/计划/目标：`ask_user_question` / `todos` / `plan_*` / `get_goal` / `read_goal` / `update_goal`
  - 后台任务/子代理：`job_*` / `subagent_*`（含批量委派 `subagent_spawn_batch`）
- `read_only: false`：允许写操作
- 未列入的工具（`team_*` / `schedule_*` / `code_*` / `openai_image_generate` 等）**不可用**

## 与其它模板的关系

| 模板 | 工具面 | 技能 | MCP |
| --- | --- | --- | --- |
| `minimal` | 读/搜 + 基础编辑，无 shell | 全部关闭 | 全部排除 |
| **`default`** | 读/搜 + 编辑 + shell + 网络 + 计划/目标 + job/subagent | 全部可见 | 全部沿用 |
| `full` | `allowlist: ["*"]` 全量 | 全部可见 | 全部沿用 |

需要全量（`team_*` / `schedule_*` / `code_*` 等）时改用 `full`；需要最小面时改用 `minimal`。

## 用法

```bash
aicli chat --profile {{name}}
aicli profile validate {{name}}
```
