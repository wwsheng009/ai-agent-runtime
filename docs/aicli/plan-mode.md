# aicli Plan Mode（计划模式）

本文说明 `aicli` 的 plan 模式：何时用、怎么进、计划如何评审与归档、模型侧与控制面之间的裁决边界，以及当前**尚未实现**的能力。

内容依据仓库内权威材料整理，并逐条与代码核对：

- 设计分析 `docs/analysis/commandcode-plan-mode-design-borrowing-20260925.md` §2 / §4 / §5 / §8（§8 为 2026-09-25 落地记录，§8.5 为未实施清单）；
- 代码：`backend/internal/planmode/state.go`、`planmode/archive.go`、`planmode/reopen.go`、`planmode/diff.go`、`backend/internal/planstore/store.go`、`backend/internal/chat/plan_mode_tools.go`、`backend/internal/toolbroker/types.go`、`backend/internal/api/runtimeapi/plans_handlers.go`、`backend/cmd/aicli/commands/chat_plan_command.go`、`chat_plans_command.go`。

> 一句话定位：plan 模式把「先出计划、再动手」变成运行时状态机——写操作被收窄到计划白名单，模型的 approve/quit 只是**请求裁决**，真正的裁决权在用户/宿主手里，计划与评审轮次额外归档到会话之外。

---

## 1. 模式选择：plan / accept_edits / bypass_permissions

| 模式 | 写操作与副作用行为 | 典型使用场景 |
|------|-------------------|--------------|
| `default` | 写文件、shell、网络、后台任务等按审批策略逐次询问 | 日常交互，默认值 |
| `accept_edits` | 文件编辑放行（不再逐次确认）；shell / 网络 / 外部副作用 / 后台任务仍会询问 | 方案已经明确的小修小改、批量机械修改 |
| `plan` | 只读探索与 `ask_user_question` 放行；写型工具仅允许计划白名单路径；其余写 / 副作用能力直接拒绝 | 新功能范围不清、复杂调试、跨文件重构、需要先评审再实现的安全敏感变更 |
| `bypass_permissions` | 所有审批关闭（yolo），风险最高；CLI 切换到它需要二次确认 | 受信任的批量自动化 / CI 等非交互场景 |

**批准计划后不再回到 bypass（新语义）**：如果进入 plan 模式之前是 `bypass_permissions`，用户 `/plan approve` 批准后**不会**恢复 `bypass_permissions`，而是降级为 `accept_edits`——避免「批准计划 = 顺带关闭全部审批」。其余情况下 approve 恢复进入前的模式，`quit` 直接恢复进入前的模式（含 bypass）。

补充约定：

- **durable plan 状态与裸权限模式是两层**：`/plan enter` / `enter_plan_mode` 会登记计划路径、写白名单、previous mode 与评审记录；`/mode plan` 只把 permission-mode 设为 `plan`（写白名单走默认计划文件）。
- **离开 plan 会收口（运行时/HTTP 路径）**：通过运行时或 `POST /api/runtime/sessions/{id}/permission-mode` 从 plan 切到其他权限模式时，会以 `quit` 关闭持久 plan 状态（并归档），避免「界面显示 plan 生效、实际已不再施加写白名单」的分叉。本地 CLI 的 `/mode` 只更新 permission-mode 字段、不清理持久 plan 状态，请用 `/plan quit`（或 `/plan approve` / `/plan request_changes`）显式收口。
- 权限模式取值只有四个：`default`、`accept_edits`、`plan`、`bypass_permissions`（无 `yolo` 字面值，`--yolo` 等价于 `bypass_permissions`）。

---

## 2. 命令参考

### 2.1 `/plan` 子命令

以下按 `chat_plan_command.go` 的实际解析列出（别名一并标注）：

| 命令 | 行为 |
|------|------|
| `/plan`、`/plan status` | 显示状态：plan mode active/inactive、当前 permission-mode、plan path、write allow、previous mode、entered at、last exit decision、notes、pending exit request。两者完全等价（`/plan status <多余参数>` 同样只打印状态） |
| `/plan enter [path]` | 进入 plan 模式；省略 `path` 时使用默认计划文件 `plan.md`。已处于 active 时重复进入会保留最初的 previous mode、只刷新计划路径 |
| `/plan on [path]`、`/plan start [path]` | `enter` 的别名 |
| `/plan exit <approve\|request_changes\|quit> [notes]` | 按决策退出或保留；缺决策时打印用法；非法决策直接报错（`approve\|request_changes\|quit`）。决策 token 也接受别名：`approved/yes/y`、`request-changes/changes/revise`、`cancel/abort/no/n` |
| `/plan approve [notes]`、`/plan approved / yes / y [notes]` | 批准计划并退出 plan 模式，随后按 §1 的规则恢复模式 |
| `/plan request_changes <notes>`、`/plan request-changes / changes / revise <notes>` | **不退出**：保持 plan 模式，把 notes 记为待交付评审反馈（下一回合一次性送达模型） |
| `/plan quit [notes]`、`/plan cancel / abort / off / no / n [notes]` | 关闭 plan 模式且不执行计划（归档状态为 `not_implemented`） |
| `/plan review`（别名 `/plan show`） | 打印当前计划正文、状态、轮次与三种裁决入口；plan 未生效时提示改用 `/plan enter` 或 `/plans` |
| `/plan <看起来像路径的 token>` | 直接以该路径进入 plan 模式（识别规则：`.md`/`.txt` 后缀，或含 `/`、`\` 等路径分隔符；无扩展名的裸词会打印用法） |

说明：

- `/plan request_changes` 与 `/plan approve`/`quit` 的差别是「留在 plan 继续修订」还是「离开 plan」。带 notes 的 `request_changes` 才会把反馈写入待交付队列并让评审轮次 +1。
- 命令执行后会尽量同步会话记录；同步失败不影响 plan 状态本身，只是提示告警。
- 在非 plan 状态下执行 `/plan exit ...`：只要当前 permission-mode 是 `plan` 仍可退出（会按「previous=default」补一个临时状态）；否则提示先执行 `/plan enter`。
- `/plan status` 在「plan active + 计划文件已有内容 + 模型尚未请求裁决」时会额外提示「计划已就绪待评审」；模型已请求裁决时改为提示「待裁决」。

### 2.2 `/plans [id]`：浏览与恢复已归档计划

`/plan status` 报告的是**当前会话**的 plan 状态；`/plans` 报告的是**归档存储**（`$HOME/.aicli/plans`，见 §5）：

| 命令 | 行为 |
|------|------|
| `/plans` | 列出全部归档记录：`ID`、状态徽标、版本（`vN`）、更新时间、计划路径 |
| `/plans <id>` | 打印单条记录的状态、会话、项目、版本、各轮次决策与 **最新快照正文**（`id` 可含 `/`，如 `ai-agent-runtime/plan`） |
| `/plans diff <id> [vA [vB]]` | 对比归档两轮正文的 unified diff（默认最近两轮；只给 `vA` = `vA → 最新`，给 `vA vB` = 指定区间），行首 `+`/`-`/空格 与 git 一致（`compare` 是别名） |
| `/plans reopen <id> [vN] [--force]` | 把某轮快照**恢复回工作区计划文件**并直接进入 plan mode，继续下一轮评审（`restore` 是别名，`--version N` 等价于 `vN`） |

两种入口等价：直接输入命令，或在统一 TTY 下由命令面板渲染（与 `chat_slash_command_catalog.go` 中的条目一致）。

**Web**：Artifact 面板里的「归档计划」（`plans` 面）是同一份数据的图形入口——列表（状态徽标 / 版本 / 更新时间 / 计划路径 / 会话）与详情（轮次决策 + 最新快照正文），并订阅 `plan_review_requested` / `plan_review_available` 事件自动刷新。它是只读阅读面：裁决仍走计划面板的三种决策或 `/plan ...`；`DELETE` 端点目前只在 HTTP 层暴露，尚未接入 UI。

**重新评审（reopen）的语义与安全边界**：归档是快照副本，工作区文件才是 plan 模式实际编辑的对象，因此 reopen 会把选中的快照写回工作区文件，并进入 plan mode（`reopened_from` / `reopened_version` 会记入会话 plan 状态，`/plan status` 与模型侧 `plan_review`/`plan_mode_*` 结果都能看到来源）。写入策略是三选一：

- 文件不存在 → 按快照创建（含父目录）；
- 文件内容与快照一致 → 不改写，直接进入 plan mode；
- 内容不一致 → **拒绝并提示加 `--force`**（避免覆盖工作区里更新的计划正文），加 `--force` 才覆盖。

相对计划路径逃逸工作区、记录无快照、记录不存在这三种情况都会直接报错，不会写出工作区或静默降级。Web 面板的图形入口（归档右键「重新评审」）尚未接入，CLI/HTTP 之外的宿主可调用 `planmode.ReopenPlan`。

**轮次 diff 的边界**：渲染由 `planmode.UnifiedDiff`（LCS 逐行匹配、3 行上下文）与 `planmode.DiffArchivedVersions`（带轮次元信息头 `--- v1 <decision> (<source>, <time>)`）提供；输出上限 400 行并附截断提示，两侧改动各超过 600 行时不做逐行匹配、退化为「整块删除 + 整块插入」并在结果里标注 `Coarse`（避免二次方内存）。同一版本自比（`v2 v2`）返回「内容完全相同」而不是错误；保留策略裁掉的版本号不可读，diff 会明确报错。Web 端的变更行高亮仍走同一渲染器，尚未接入。

### 2.3 `/mode`（`/permission-mode` 的别名）

| 命令 | 行为 |
|------|------|
| `/mode` | 显示当前 permission-mode；plan 生效期间统一显示为 `plan` |
| `/mode default` / `/mode accept_edits` / `/mode bypass_permissions` | 切换权限模式；切到 `bypass_permissions` 需要二次确认 |
| `/mode plan` | 只设置裸 permission-mode（不登记计划路径 / 写白名单 / previous mode）。需要完整 plan 生命周期请用 `/plan enter` |
| `/mode <其他值>` | 报错，提示可选值 `default\|accept_edits\|plan\|bypass_permissions` |

从 plan 切到其他模式时，**运行时/HTTP 路径**会以 `quit` 收口持久 plan 状态并归档（`backend/internal/chat/permission_mode.go`）；本地 CLI 的 `/mode` 只更新 permission-mode 字段（切换后 durable plan 状态仍然生效，`/plan status` 会继续显示 plan），需要显式 `/plan quit`。HTTP 侧的 `POST /api/runtime/sessions/{id}/permission-mode` 只允许「切出」plan，切向 plan 会返回提示改走 `/api/runtime/sessions/{id}/plan`（`permission_mode_handlers.go`）。

---

## 3. 工具参考（模型侧）

`enter_plan_mode` / `exit_plan_mode` 只在宿主注入了 plan 模式控制器时才会出现在模型可见面；`plan_review` 只在注入了计划评审控制器时出现。三者都归类为只读控制工具（能力为 `CapReadOnly + CapAskUser`），在 plan 模式下放行；同时属于 runtime-owned essentials，可以绕过 `--allow-tool` / profile 的窄名单，但仍受**显式 deny**、能力域与只读子代理约束。

### 3.1 `enter_plan_mode`

| 参数 | 说明 |
|------|------|
| `plan_path` | 主计划工件路径，默认 `plan.md`；也接受字符串数组（首项为主工件，其余并入写白名单） |
| `plan_write_paths` | 额外的可写计划文件，与 `plan_path` 取并集去重 |

- 效果：写入 durable 会话状态（`status=active`、`plan_path`、`previous_mode`、`write_allow_paths`），把权限引擎钉在 `plan`，发布 `plan_mode_changed` 事件，并向计划归档登记元数据（**enter 不产生轮次快照**）。
- 嵌套重复进入会保留最初的 `previous_mode`，只刷新计划路径。
- 从「裸」permission-mode=plan 进入时，previous mode 记 `default`，避免退出后回到 `plan`。
- `plan_path` 允许尚不存在：该工具显式关闭了通用读路径预检，计划文件通常在进入之后才被创建。
- 显式关闭的计划路径可以位于工作区外；归档读取相对路径时会拒绝逃逸工作区（见 §5）。
- **模型自主进入需用户确认**（默认）：模型调用 `enter_plan_mode` 时，宿主先弹一次审批（reason `plan_mode:model_auto_enter`）；用户拒绝则本次调用被拒且 plan 模式不变。`/plan enter`、`/mode plan`、`--permission-mode plan` 是用户显式动作，不触发该确认。无交互宿主（如 `aicli exec`）没有审批通道，默认直接拒绝，需要自治时设置 `AICLI_PLAN_MODE_MODEL_AUTONOMY=1`（§3.4）。显式规则（permissions 文件里对该工具的 allow/ask/deny）优先于该默认门控。

### 3.2 `exit_plan_mode` 与 verdict 语义

| 参数 | 说明 |
|------|------|
| `decision` | 必填，枚举 `approve \| request_changes \| quit` |
| `notes` | 可选，随决策记录的备注 |

**关键语义：模型的 `approve` / `quit` 只是「请求裁决」，不是决定。**

- 交互式宿主（默认，含 chat / TUI / Web）下，模型调用 `exit_plan_mode(decision=approve|quit)` 只会把状态记为**待裁决请求**（`pending_exit_request=true`，来源标注 `model`），会话**保持 plan 模式与写白名单**，发布 `plan_mode_changed`，等待用户/宿主裁决。工具返回与工具描述都按「已提交评审、等待用户裁决」表达。
- `decision=request_changes` 不受影响：模型可以继续留在 plan 模式做自我修订；模型自己填的 notes **不会**被当成用户评审反馈回灌给模型。
- 用户裁决路径（Web 面板、`/plan approve|request_changes|quit`、`POST /api/runtime/sessions/{id}/plan`）写 `ExitDecision` 并把来源标注为 `user`。
- 用户 `approve` 后退出 plan；`request_changes` 保持 active 并记录待交付 notes；`quit` 退出且不执行计划。批准时的模式恢复规则见 §1。

### 3.3 `plan_review`：打开评审面（只读）

在模式之外（或会话重启后）把某个计划重新拉回评审视野，**不改变** plan 状态、不做裁决。

| 参数 | 说明 |
|------|------|
| `plan_id` | 归档记录 id（可含 `/`，如 `ai-agent-runtime/plan`）；给了它就优先按归档读取 |
| `plan_path` | 计划文件路径（工作区相对）；未归档的计划用它 |
| `version` | 归档快照版本；0/省略 = 最新 |

- 返回 `content`（正文，上限 64 KiB，超出置 `truncated`）、`status`、`version`、`review_round`、`source`（`session` / `archive`）、`verdict_options` 与一句可直接转述的 `hint`。
- 会发布 `plan_review_requested` 事件，Web 面板据此聚焦/刷新；CLI 侧等价入口是 `/plan review` 与 `/plans <id>`。
- 会话计划若已归档（同路径有记录），结果会带上 `plan_id` / `version`，便于后续按归档地址引用。
- 归档记录不存在时返回错误（`plan ... not found in the plan archive`），不会静默降级。

### 3.4 无头宿主开关：`AICLI_PLAN_MODE_MODEL_AUTONOMY`

- 取值 `1 | true | yes | on`（大小写不敏感）= 恢复旧行为：模型自带 verdict 直接生效、模型可自主进入 plan 模式，不再等待裁决/确认。
- 未设置或其他值 = 交互语义（默认收口）：`approve`/`quit` 等待用户裁决，`enter_plan_mode` 需要用户确认。
- 这是**进程级环境变量**：在交互宿主进程里同样生效，请只在确实需要自治的宿主（如 `aicli exec` / ACP）中设置。`request_changes` 的语义不变。
- 无头入口仍是 `aicli exec --permission-mode plan "复杂任务"`（用户显式进入，不经确认门控）。

---

## 4. 评审闭环：模型请求 → 用户裁决 → notes 回流

1. **模型提交**：模型在 plan 模式内写好计划后调用 `exit_plan_mode(decision=approve, notes=...)` → 记 `pending_exit_request=true`、`last_exit_source=model`，模式与写白名单保持不变。
2. **用户裁决**（`/plan approve`、`/plan quit`、`/plan request_changes <notes>`、Web 面板或 HTTP 接口）：
   - `approve`：退出 plan；恢复进入前模式，**但进入前是 `bypass_permissions` 时降级为 `accept_edits`**；
   - `request_changes`：保持 plan active；带 notes 时写入 `pending_review_notes`，评审轮次 +1；
   - `quit`：退出 plan 且不执行计划，归档状态 `not_implemented`。
3. **notes 到达模型**：用户**下一次**发起模型回合时，运行时会消费 `pending_review_notes`，作为一次性系统提醒（kind `plan_review`，`Durable=false`）注入该回合并同时清除持久副本；清除写入失败会回滚，保证反馈「只送达一次、且不丢失」。提醒文案要求模型据此修订计划、再次总结并等待裁决。
4. **不会自动新开回合**：批准或请求修改后，运行时不会自动触发模型回合——推进由用户的下一次输入驱动。评审反馈的「自动修订回合」属未实施范围（见 §9）。
5. **事件**：每次 plan 状态迁移发布 `plan_mode_changed`；归档失败额外发布 `plan_archive_failed`（不影响状态机）。
6. **Run 结束兜底（自动呈现）**：一次 run 干净结束（`session_end` 为 idle、无错误）时，若 plan 仍 active、模型**尚未**请求裁决、当前模式是 `default`/`plan`、且计划文件有内容，运行时会发布一次 `plan_review_available`（payload 含 `plan_path`、`plan_hash`、`plan_bytes` 与三个裁决入口）。同一份正文只提示一次，计划被改写（哈希变化）后再次提示；`accept_edits`/`bypass_permissions` 按设计跳过。Web 收到该事件会刷新计划面板，CLI 侧对应 `/plan status` 的「计划已就绪待评审」提示与 `/plan review`。

---

## 5. 计划工件归档（planstore）

计划工件会额外归档到会话之外，使 `quit`/取消后的计划仍然可浏览，并保留每一轮评审的正文快照。

### 5.1 位置与目录布局

- 根目录优先取 `AICLI_PLANS_DIR`；未设置时使用 `$HOME/.aicli/plans`；HOME 解析不到时回退到进程工作目录下的 `.aicli/plans`。
- 布局：

```text
<root>/index.json                                 # {"version":1,"records":[...]}
<root>/versions/<project>/<plan>-v<N>.md          # 每轮评审一份快照，N 从 1 开始
```

- 记录 ID 为 `<projectSlug>/<planName>`：项目名取工作区最后一段路径、计划名取计划文件名（去掉 `.md`），两者都做 slug 归一化——只保留 `[a-z0-9-_]`，其余字符（中文、空格、分隔符等）折叠为单个 `-`，空结果回退为 `project` / `plan`。
- 归档是**附加副本**：工作区里的计划文件仍是模型实际读写、权限白名单判定的对象；归档目录不参与权限判定。
- **反向回灌**：`/plans reopen <id> [vN]` 会把归档快照写回工作区计划文件并进入 plan mode（见 §2.2）；回灌同样是显式动作，归档本身保持只读。
- **保留策略**：`AICLI_PLANS_MAX_VERSIONS=N`（正整数）在每次归档后只保留最新 N 轮快照，旧快照文件与轮次条目一起删除；未设置或非法值 = 保留全部轮次（历史行为）。`version` 计数器不回退，被裁掉的版本号随之不可读。显式删除走 `DELETE /api/runtime/plans/{id}`（幂等）。归档删除/裁剪失败不会回滚 plan 模式迁移。

### 5.2 记录字段与状态

`index.json` 中的每条记录包含：`id`、`session_id`、`project_slug`、`project_path`、`plan_path`、`title`、`status`、`version`、`rounds[]`、`created_at`、`updated_at`。

状态取值：

| 状态 | 含义 |
|------|------|
| `pending` | 已登记但尚未裁决（含模型刚提交评审） |
| `approved` | 用户已批准 |
| `not_implemented` | 评审取消 / `quit`，计划保留但未实施 |

### 5.3 轮次概念

- `enter`（含 `/plan on`、`/plan start`）只登记记录与元数据，**不产生轮次快照**。
- 每次带正文的裁决追加一轮 `versions/<project>/<plan>-v<N>.md` 并把 `version` +1：用户 `approve` / `request_changes` / `quit`，以及模型发出的退出请求（`request_exit`）。
- 每个轮次记录：`version`（从 1 开始）、`decision`（自由文本，常见值 `approve`、`request_changes`、`quit`、`request_exit`）、`notes`、`source`（`user` / `model`）、`snapshot`（相对根目录的路径）、`created_at`。
- 计划文件不存在或读不到时，只更新记录状态、不产生快照（例如模型尚未写出计划）；`request_changes` / `enter` 不改变状态。
- 归档是 best-effort：归档失败只发 `plan_archive_failed` 事件，不回滚 plan 模式迁移；快照路径越出 store 根目录会被拒绝，相对计划路径逃逸工作区时归档直接跳过正文。

---

## 6. HTTP API

归档工件的读取接口（注册在 `/api/runtime` 下）：

| 方法与路径 | 说明 |
|------------|------|
| `GET /api/runtime/plans` | 列出全部归档记录，按 `updated_at` 倒序；可选查询参数 `?project=<slug>` 过滤项目；响应 `{"plans":[...],"count":N}` |
| `GET /api/runtime/plans/{id}` | 单条记录 + **最新快照正文**；`id` 允许包含 `/`（如 `ai-agent-runtime/plan`）；兼容旧写法 `?id=<id>` |
| `DELETE /api/runtime/plans/{id}` | 删除一条归档记录及其全部快照（保留策略见 §5.1）；幂等：不存在时返回 200 + `{"deleted":false}`，存在时 `{"deleted":true}` |

列表项与详情条目字段一致（详情多出正文相关字段）：

`id`、`session_id`、`project_slug`、`project_path`、`plan_path`、`title`、`status`、`version`、`rounds[]`（每项 `version`、`decision`、`notes`、`source`、`snapshot`、`created_at`）、`created_at`、`updated_at`；详情额外返回 `content`、`content_available`、`content_truncated`、`content_error`。

- `content` 只在该记录 `version > 0` 时读取最新快照；超过 200,000 个 rune 会截断并置 `content_truncated=true`。
- 快照读取失败时返回 200，但把原因放进 `content_error`（记录元数据仍然可用）。
- 记录不存在返回 404；`index.json` 损坏返回 500；store 未配置返回 503。

注意区分两套入口：会话内 plan 状态（进入/退出/预览计划文件）走 `GET|POST /api/runtime/sessions/{id}/plan`；这里的 `/plans`、`/plans/{id}` 只读归档索引与快照。

---

## 7. 与 checkpoint / 回滚的关系

plan 模式与 checkpoint 是两条互补但独立的链路：

- **checkpoint（还原点）** 是运行时在会修改文件的工具调用前后自动拍摄的文件 + 会话快照，用于把工作区/会话回滚到该时刻（`code` / `conversation` / `both`）；它是 opt-in 能力（`checkpoint.enabled` 默认 `false`）。设计基线见 [docs/design/checkpoint-design.md](../design/checkpoint-design.md)。
- **planstore** 只归档「计划正文 + 评审轮次」，是可浏览的历史，**不是回滚底座**：它不保存工作区文件版本，也不能把仓库恢复到某个计划时刻。
- plan 模式不改变 checkpoint 的采集规则：计划文件的写入同样是一次文件 mutation，是否产生 checkpoint 取决于 checkpoint 配置。
- **当前未绑定**：`/plan approve` 不会自动创建 checkpoint；「approve → 实现」之间自动打点属于报告的开放问题（§7 问题 3），尚未实施。

因此，批准计划、开始实现前若需要可回滚点，请按 checkpoint 自身的开关与配置准备（本仓库 checkpoint 链路的细节以 `docs/design/checkpoint-design.md` 为准）。

---

## 8. 常见问题

**Q1：无头模式（`aicli exec`）下计划卡在「等待裁决」怎么办？**

默认行为：模型的 `approve`/`quit` 只是请求，需要宿主裁决；无头场景没有交互用户，因此需要显式放开模型自治——设置环境变量 `AICLI_PLAN_MODE_MODEL_AUTONOMY=1`（`true|yes|on` 同义）。注意这是进程级开关，交互宿主进程也会受影响，请按宿主类型分别设置。

**Q2：用 `--allow-tool` 给了很窄的名单，为什么能进 plan 模式却写不了计划文件？**

`enter_plan_mode` / `exit_plan_mode` 是 runtime-owned essentials，窄名单不会把它们屏蔽（控制面不被 `--allow-tool` 锁死）。`write` / `apply_patch` 也已对齐：**plan active 时**，只要该次调用的**全部目标路径**都命中计划写白名单（`write_allow_paths`），就会绕过 allowlist 门；进入 plan 时白名单会被解析为工作区绝对路径（`write_allow_paths_resolved`），比较用「绝对路径相等或带分隔符的目录前缀」，不再有 base-name 兜底，`apply_patch` 逐 `*** Add/Update/Delete File:`、`*** Move to:` 头校验。离开 plan 后豁免立即失效。

优先级没有变化：**显式 deny > 只读子代理 > 能力域 > 沙箱路径边界 > allowlist 豁免**。所以 `--deny-tool write` 依然拒；补丁里只要含一个非计划文件就整体拒绝；`docs/plan.md` 的白名单不会放行 `other/plan.md` 或 `docs/plan-notes.md`。

**Q3：模型写完计划却不调用 `exit_plan_mode` 怎么办？**

已有 run 结束兜底（§4.6）：只要计划文件里有内容、模型没提交裁决请求，run 干净结束后运行时会发布 `plan_review_available`，CLI 的 `/plan status` 也会显示「计划已就绪待评审」。状态机仍不会自行推进——请用 `/plan review` 查看正文，再 `/plan approve [notes]`、`/plan request_changes <notes>` 或 `/plan quit [notes]` 完成裁决。

**Q4：为什么我批准计划后权限没有回到原来的 bypass？**

按设计：进入 plan 前若是 `bypass_permissions`，批准后恢复为 `accept_edits`，避免「批准计划」被当成「关闭全部审批」。如确需 bypass，请显式用 `/mode bypass_permissions` 切换。

**Q5：`/plan request_changes <notes>` 之后模型会马上改计划吗？**

不会自动开新回合：notes 保存在 durable 状态的 `pending_review_notes`，在**下一次模型回合**作为一次性提醒注入并清除。你需要再发一条消息（例如「继续」或补充要求）来驱动修订。

**Q6：计划被 `quit` 后还能找回吗？**

正文与轮次已归档（状态 `not_implemented`）。CLI 用 `/plans` 列出、`/plans <id>` 打开；HTTP 用 `GET /api/runtime/plans`、`GET /api/runtime/plans/{id}`。目前没有「重开评审」的 API 或 UI 入口（§9）。

**Q7：一个会话能同时维护多份计划吗？**

`enter_plan_mode` 的 `plan_write_paths` 支持多个写路径，但归档记录 ID 由主计划文件决定，会话的「当前计划」仍是单一 `plan_path`；多计划并发的产品语义尚未定义（报告 §7 问题 4）。

**Q8：模型请求进入计划模式却失败了（headless / 薄名单），怎么办？**

默认下 `enter_plan_mode` 需要用户确认：交互宿主弹审批卡片（reason `plan_mode:model_auto_enter`），无交互宿主（`aicli exec` 等）没有审批通道，引擎直接拒绝（`headless_deny:approval_required`）。三条出路：

1. 用户显式进入：`aicli exec --permission-mode plan "任务"`，或在交互宿主里先 `/plan enter`；
2. 授予模型自治：`AICLI_PLAN_MODE_MODEL_AUTONOMY=1`（进程级，§3.4）；
3. 写一条显式规则：在 permissions 配置里对 `enter_plan_mode` 给 `allow`，规则优先于该默认门控。

`/plan review`、`plan_review` 与 `/plans` 都只是打开评审面，不会因为被拒而改变 plan 状态。

**Q9：之前归档的计划，怎么接着评审 / 继续改？**

`/plans reopen <id> [vN] [--force]`：把归档的第 N 轮（默认最新）快照写回工作区计划文件，并直接进入 plan mode，随后照常走 `/plan request_changes <notes>` / `/plan approve`。工作区文件与快照不一致时默认拒绝（保护你手改过的正文），确认要覆盖时加 `--force`。恢复来源会记在 plan 状态里（`/plan status` 的 `reopened from`、模型侧结果的 `reopened_from`）。

---

## 9. 尚未实现（依据报告 §8.5）

以下能力在已落地范围（2026-09-25：报告 §8 + §9 + §10）之外，本文档不为它们承诺时间：

- §4.4 行级评论与轮次 diff：**CLI 侧轮次 diff 已落地**（`/plans diff <id> [vA [vB]]`，复用 `planmode.UnifiedDiff` / `planmode.DiffArchivedVersions`）；仍未做的是前端评审面的变更行高亮与行级评论。
- §4.5 的 `/plans` 浏览器（Web 面板）、`plan_review` 工具、run 结束兜底与 **CLI 的 `/plans reopen`（归档回灌重评审）**均已落地；仍是缺口的是 Web 面板里的图形化 reopen 入口。
- §4.6 模式循环键位（`shift+tab`）与常驻模式横幅：模型自主进入的确认门控已落地，键位/横幅未做。
- 评审反馈的**自动修订回合**：当前是「下一次用户输入时交付」，Web 裁决后主动 trigger-turn 未接入。

---

## 相关文档

- [docs/aicli/exec.md](./exec.md) —— 无头 `aicli exec`（含 `--permission-mode plan`）。
- [docs/user-guide/aicli.md](../user-guide/aicli.md) —— 安装、启动与日常操作手册。
- [docs/design/checkpoint-design.md](../design/checkpoint-design.md) —— checkpoint / 回滚设计基线。
- [docs/analysis/commandcode-plan-mode-design-borrowing-20260925.md](../analysis/commandcode-plan-mode-design-borrowing-20260925.md) —— 设计借鉴分析与 §8 实施记录。
