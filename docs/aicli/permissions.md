# aicli 权限手册（Permissions）

一句话定位：**模式决定「默认问不问」，规则决定「哪些工具/命令/路径永不放行或必须确认」，审批是唯一的例外通道。**

本文是使用手册：一分钟快速版 → 规则语法 → 决策阶梯 → 常用 recipes → 已知限制。
**项目权限文件的完整 schema、分层与合并语义**见 [`docs/product/project-permissions.md`](../product/project-permissions.md)；文件夹信任（插件/agents/MCP 配置的准入）见 [`docs/product/folder-trust.md`](../product/folder-trust.md)。

内容与代码逐条核对：`backend/internal/policy/`（`modes.go`、`engine.go`、`rules.go`、`permissions_file.go`、`sensitive_paths.go`、`safe_file_commands.go`、`grants.go`、`file_grants.go`）、`backend/internal/toolkit/tools/bash.go`、`backend/internal/chat/`（actor 审批与 grants）。

---

## 0. 一分钟快速版

### 0.1 五种模式

| 模式 | 读 / 只读命令 | 写文件 | shell（非只读） | 网络 | 后台任务 | 典型场景 |
|------|--------------|--------|----------------|------|----------|----------|
| `default` | 放行 | 询问 | 询问 | 询问 | 询问 | 日常交互（默认值） |
| `accept_edits` | 放行 | 放行（含安全文件命令快车道） | 询问 | 询问 | 询问 | 方案已明确的小改、批量机械修改 |
| `plan` | 放行 | 仅计划白名单路径 | 拒绝 | 拒绝 | 拒绝 | 先评审再动手（配合 `/plan enter`） |
| `dont_ask` | 放行 | 拒绝 | 拒绝 | 拒绝 | 拒绝 | CI / 无人值守：**默认问的都变成拒绝** |
| `bypass_permissions` | 放行 | 放行 | 放行 | 放行 | 放行 | 受信任的批量自动化（风险最高） |

- `dont_ask` 是 fail-closed 的无人值守模式：**该问的一律拒绝**（`mode:dont_ask_denies_unapproved`），但读、只读 shell、allow 规则与已记忆授权照常生效——它是 CI 里比 `--yolo` 更该用的那个。
- `bypass_permissions` 不能越过：硬 deny 名单与显式 deny 规则、hook 的 `block`、根/主目录断路器（HardAsk）；但它**会跳过**敏感写保护与外部目录门的询问（敏感写在 yolo 下直接放行）——需要更强保证就别开 yolo，或用 `disable_bypass` 关掉它。
- 取值只有这五个，没有 `yolo` 字面值；`--yolo` / `--accept-edits` / `--plan` 是简写，分别等价于 `--permission-mode bypass_permissions` / `accept_edits` / `plan`（同时给出会**报错**，不静默取一个）。
- 兼容别名（§4.12；CLI、runtime API、ACP 走同一解析）：`manual`/`standard` → `default`，`auto-accept`/`acceptEdits` → `accept_edits`，`bypass` → `bypass_permissions`，`dontAsk` → `dont_ask`。未知值一律拒绝，别名不会让拼错的值静默降级。
- 会话内入口：`/mode`（与 `/permission-mode` 同义）查看或切换，支持冒号简写 `/mode:accept_edits`、`/permission-mode:plan`；切 `bypass_permissions` 仍要过 §1.4 的终端确认门。

### 0.2 最常用的 5 条规则

放到 `<项目根>/.aicli/permissions.yaml`（提交进仓库）或 `~/.aicli/permissions.yaml`（个人全局）：

```yaml
version: 1
rules:
  # 1) 只读 git 不再逐条问
  - name: allow-git-read-only
    tools: ["Shell(git status)", "Shell(git diff:*)", "Shell(git log:*)"]
    decision: allow

  # 2) 推送前必须人工确认（即使模式放行）
  - name: ask-before-push
    tools: ["Shell(git push:*)", "Shell(git push --force:*)"]
    decision: ask

  # 3) 保护 Git 内部状态
  - name: protect-git-dir
    tools: ["Edit(.git/**)", "Write(.git/**)"]
    decision: deny

  # 4) lockfile / 生成物改动要过目
  - name: review-lockfiles
    tools: ["Edit(package-lock.json)", "Edit(pnpm-lock.yaml)", "Edit(yarn.lock)"]
    decision: ask

  # 5) 密钥材料读取显式化（默认也已被机密过滤挡住只读快车道）
  - name: review-secret-reads
    tools: ["Read(.env)", "Read(**/*.pem)", "Read(id_rsa*)"]
    decision: ask
```

> 注意：`deny_tools` / `allow_tools`（以及 `--deny-tool` / `--allow-tool`）是**硬精确工具名**，写 `Shell(git push:*)` 这类语法会被拒绝装载；specifier 语法只在 `rules[].tools` 里生效。

### 0.3 怎么进这些模式

| 入口 | 写法 |
|------|------|
| CLI 启动 | `aicli chat --permission-mode plan`、`aicli chat --yolo`、`aicli exec --permission-mode dont_ask --prompt "..."` |
| CLI 会话内 | `/permission-mode <mode>`（别名 `/mode`）、`/plan enter`（完整 plan 生命周期）、`/yolo`（等同于切到 bypass，需二次确认） |
| 键位 | `shift+tab` / `alt+m` 循环 `default → accept_edits → plan → bypass_permissions`（**不含 `dont_ask`**，它必须显式选择） |
| Web | composer 的权限模式下拉（`GET/POST /api/runtime/sessions/{id}/permission-mode`） |
| ACP | 客户端 config option（`mode`）；值与 CLI 相同 |

---

## 1. 模式与入口细节

### 1.1 模式行为矩阵（按能力）

| 能力 | `default` | `accept_edits` | `plan` | `dont_ask` | `bypass_permissions` |
|------|-----------|----------------|--------|------------|----------------------|
| 纯只读工具（`view`/`grep`/`glob`/`ls`…） | 放行 | 放行 | 放行 | 放行 | 放行 |
| 只读 shell（命令表命中） | 放行 | 放行 | 放行 | 放行 | 放行 |
| 写文件（`write`/`edit`/`apply_patch`） | 询问 | 放行 | 仅白名单路径 | 拒绝 | 放行 |
| 安全文件命令（`mv`/`cp`/`mkdir` 等，见 §3.4） | 询问 | **快车道放行** | 拒绝 | 拒绝 | 放行 |
| 非只读 shell | 询问 | 询问 | 拒绝 | 拒绝 | 放行 |
| 网络（`web_search`/`fetch`/`download`） | 询问 | 询问 | 拒绝 | 拒绝 | 放行 |
| 后台任务（`background_task`） | 询问 | 询问 | 拒绝 | 拒绝 | 放行 |
| `ask_user_question` / 计划工具 / 协作控制面 | 放行 | 放行 | 放行 | 放行 | 放行 |
| 显式 `deny` 规则 / 硬 deny 名单 | 拒绝 | 拒绝 | 拒绝 | 拒绝 | **拒绝** |

`accept_edits` 的快车道只覆盖「安全文件命令」（`ShellSafeFile*`，reason `mode:accept_edits_safe_file`）；递归删除、目标在工作区外、命中敏感路径、动态语法、不可解析命令都会**掉回询问**（`safe_file:recursive_delete`、`safe_file:target_outside_workspace`、`safe_file:sensitive_target`、`safe_file:dynamic_syntax`、`safe_file:unparsable_command`）。

### 1.2 `dont_ask` vs `bypass_permissions`：CI 怎么选

| 问题 | `dont_ask` | `bypass_permissions` |
|------|-----------|----------------------|
| 默认行为 | 该问的 → 拒绝（fail closed） | 该问的 → 放行（fail open） |
| 只读与 allow 规则 | 照常生效 | 照常生效 |
| 适合 | CI 里跑「确定只需要读」的检查、不可信输入 | 受信任仓库里的自动化修复（有人审 diff） |
| 被 `disable_bypass` 影响 | 不受影响 | 会被降级为 `default` |

CI 推荐组合：`dont_ask` + 项目 `permissions.yaml` 把需要的只读/白名单写成 `allow` 规则 + `--deny-tool` 兜底。要在 CI 里跑写操作，用 `accept_edits` 并显式列出允许的写规则，而不是 `--yolo`。

### 1.3 关掉 bypass（`disable_bypass`）

任一层权限文件写 `disable_bypass: true` 即生效（多层取 OR）：

- 引擎把以 `bypass_permissions` 进入的请求按 `default` 处理（该问的照问、headless 场景照拒）；`dont_ask` 与 `plan` 不受影响；
- CLI 的切换入口（`/permission-mode bypass_permissions`、shift+tab 循环、`/yolo`）会被拒绝（提示后不切换）；ACP 客户端在模式选择器里点选 bypass 同样不会被应用；
- runtime API 的切换接口（`POST /api/runtime/sessions/{id}/permission-mode`）在目标为 `bypass_permissions` 时直接返回 **403**，不会写入一个求值期又被降级的 bypass 元数据；
- plan 退出等**还原路径**同样不会把 bypass 带回来：从 bypass 进入 plan 后退出（`quit`）写回的是 `default`（approve 本来就映射为 `accept_edits`，属既有设计）；
- `--yolo` 启动参数只能靠引擎侧降级（进程启动时无法询问），此时元数据可能仍显示 `bypass_permissions`——以实际审批行为为准。

适合把 `disable_bypass: true` 放在用户级 `~/.aicli/permissions.yaml`，让个人环境永远保留审批。

### 1.4 谁能把会话切到 bypass（提权边界；复核细节见分析文档 §13）

| 输入面 | 需要人类手势吗 |
|--------|----------------|
| 终端里输入 `/yolo` / `/permission-mode bypass_permissions` | 需要：必须在**本机终端**敲 `bypass_permissions` 全文；非交互模式直接拒绝（确认门按输入来源判定，Web/外部注入的行不生效，见下行） |
| 键位循环（`shift+tab` / `alt+m`） | 需要（同上确认） |
| TUI 的 Web 注入面（`/web/api/input`、`/web/api/invoke`、网格 call） | **不能提权**：注入的文本按用户输入路由（含命令），但 bypass 的确认门只接受本机终端来源——注入 `bypass_permissions` 会被拒绝（普通文本按原顺序回填队列，`/` 命令直接忽略，见 §13 F3 收口）；写操作需要 `X-AICLI-Token`，而该令牌按设计可由本机进程经 `GET /web/api/token` 读取（审批/提问回答仍可注入，那是 Web 的正规交互面） |
| runtime API（`POST /api/runtime/sessions/{id}/permission-mode`） | 切 `bypass_permissions` **需要** `confirm:true`：缺省返回 400 且不改写模式（`disable_bypass` 生效时 403 优先）；其它模式无需确认、对运行中会话立即生效。该接口没有令牌机制，`confirm` 是**契约层的显式意图**（防误触发与工具化提权），不是对同机进程的强隔离——强隔离请用 `disable_bypass`（403） |

由此得出三条使用边界：

1. **可信边界是「同一台机器上的进程」，不是写令牌**——写令牌防的是浏览器跨站与 DNS rebinding，不防同机进程（`web_auth.go` 的设计前提）。不要在跑着不可信本地代码的机器上依赖它。
2. **模型要自我提权，得先说服你批准一次可疑调用**（或已经在放宽的模式里）：`shell`/网络/`aicli_exec`/`background_task` 都受同一引擎门控，`plan` 下直接拒绝。看到「请求本机 HTTP 端口 / 调用改权限模式的接口」这类命令请按高风险处理。
3. **`disable_bypass: true` 是最后一道闸**：CLI/ACP 的切换入口会被拒绝、runtime API 会 403；即使有入口绕过去（如 `--yolo` 启动参数），引擎求值也会把 bypass 降级为 `default`（该问的照问）——后者是兜底，此时元数据可能仍显示 `bypass_permissions`，以实际审批行为为准。

---

## 2. 规则语法参考

### 2.1 文件位置与分层（累积语义）

```text
~/.aicli/permissions.yaml                  # 用户级：个人全局（建议放 disable_bypass）
<project>/.aicli/permissions.yaml          # 项目级：提交进仓库、团队共享
<project>/.aicli/permissions.local.yaml    # 本地级：个人覆盖，建议写进 .gitignore
```

- 规则按 `user → project → local` 顺序**拼接**，规则名自动带层前缀（如 `project/allow-git-read-only`），仍然是 **first-match-wins**：因此用户级 deny 会先于项目级 allow 求值。
- `deny_tools` / `allow_tools` 跨层取**并集**（deny 单调不可撤销）；`disable_bypass` 取 OR。
- 缺文件不是错误（跳过该层）；语法/取值非法时**不会部分生效**——整个文件被丢弃（CLI 记一条 warning 后跳过该 overlay，会话照常启动）。也就是说**写坏了的权限文件等于没有这条限制**，请把权限文件当代码评审，并在 CI 里校验（见 §6.10）。
- `.yml` 与 `.yaml` 都接受，解析顺序见 [`project-permissions.md`](../product/project-permissions.md)（`permissions.yaml` → `permissions.yml`）。

最小可用文件：

```yaml
version: 1
deny_tools: [shell]          # 硬精确名，不走 specifier 语法
allow_tools: []              # 省略/空 = 不启用项目 allowlist 门
rules:
  - name: example
    tools: ["Shell(git status)"]
    decision: allow          # allow|yes|true / deny|block|false|no / ask|prompt|approval
    capabilities: [exec_shell]   # 可选：能力域全含才命中
    reason: allow_git_status     # 可选：进入审批/日志的 reason
```

### 2.2 命令规则：`Shell(...)` / `Bash(...)`

```yaml
tools:
  - "Shell(git status)"          # 精确匹配整条命令
  - "Shell(git diff:*)"          # 前缀匹配：git diff 及其任意后续参数
  - "Shell(npm run *)"           # * / ? 通配（匹配参数片段）
  - "Shell(rm -rf .)"            # 允许写确切的危险串，交给 deny/ask 精确收紧
```

- **复合命令会被拆段**：`&& || ; | &` 与换行都作为分隔；`deny`/`ask` 只要**任意一段**命中即生效，`allow` 要求**每一段**都命中。
- **不可解析/包装器命令永不自动 allow**：含 `> < \` $` 等动态语法、`xargs`/`sh -c`/解释器 payload、变量拼接等，`allow` 规则不生效（掉回模式询问）；`deny`/`ask` 仍可命中。
- 命令前缀按**基命令 + 参数**匹配：`Shell(git:*)` 覆盖 `git ...` 所有子命令，`Shell(git diff:*)` 只覆盖 `git diff ...`。
- 大小写：`allow` 侧折叠大小写（`Shell(Git Status)` 可命中 `git status`），`deny`/`ask` 侧精确区分——收紧用的规则别指望大小写通配。

### 2.3 路径规则：`Read(...)` / `Edit(...)` / `Write(...)` / `View(...)`

```yaml
tools:
  - "Read(.env)"            # 相对形态：任意深度命中同名文件
  - "Read(**/*.pem)"        # ** 跨目录分隔符
  - "Edit(.git/**)"         # .git 下一切
  - "Edit(//tmp/out/**)"    # // 前缀 = 文件系统绝对路径
  - "Read(~/.ssh/id_rsa)"   # ~ = 用户主目录
  - "Write(/generated/**)"  # 单个 / 前缀 = 会话工作区根
```

- 锚点语义：`//abs` 绝对路径、`~/` 主目录、`/` 工作区根、其余为**相对路径**（任意深度）。
- `*` 不跨越 `/`；`**` 跨越任意层级。
- 大小写：`deny`/`ask` 折叠大小写（`.ENV` 命中 `Read(.env)`）；`allow` 精确匹配（Windows 语义上大小写不敏感）。
- 路径规则作用于**结构化路径参数**（`file_path`/`path`/`patch` 等）。shell 命令**字符串内部**的绝对路径不参与此匹配（见 §6 已知限制）。

### 2.4 域名规则：`WebFetch(...)` / 网络工具

```yaml
tools:
  - "WebFetch(example.com)"          # 精确主机
  - "WebFetch(domain:*.example.com)" # 子域（* 不包含 apex，example.com 本身不命中）
```

- 匹配对象是网络工具 URL 的 **host**；`*` 只匹配一级或多级子域前缀，apex 需单独列出。

### 2.5 参数规则（仅 `deny` / `ask`）

```yaml
tools:
  - "Shell(run_in_background:true)"   # 顶层参数名:值
  - "Edit(dry_run:false)"             # 也支持值前缀 value*
```

- 按模型**实际发送的顶层参数**匹配；`allow` 规则中使用参数语法会在装载时报错（不可执行地「放行」是危险的）。
- 参数名属于工具「已拥有」的字段时不能用 param 形态：`command`/`cmd`/`commands`/`file_path`/`path`/`paths`/`url`/`urls`/`patch`/`diff`——这类语义请用对应的命令/路径/域名 specifier 表达（装载期会报专门的错）。

### 2.6 工具名与 glob

```yaml
tools:
  - "mcp__github__get_*"   # MCP 工具前缀通配
  - "edit_*"               # 普通工具名前缀通配
```

- 规范 MCP 名：`mcp__<server>__<tool>`。
- `allow` 规则必须是**具体**前缀：裸 `*`、`mcp__*` 之类的宽通配会在装载时被拒绝（避免一条规则放开整个 MCP/工具面）。
- `deny`/`ask` 可以用宽通配（先拒绝再逐条放开的安全姿势）。

### 2.7 硬名单与 CLI 参数

```bash
aicli chat --deny-tool shell --allow-tool view
aicli exec --deny-tool download --enable-tools --prompt "..."
```

| 项 | 语义 |
|----|------|
| `deny_tools` / `--deny-tool` | 硬 deny（ToolExecutionPolicy + 引擎 first-match deny 规则），**不接受 specifier 语法**；可重复 |
| `allow_tools` / `--allow-tool` | 参与 allowlist 门 + 引擎 allow 规则；工具名精确匹配；与 profile 既有 allowlist **求交**（不会放大权限） |
| 项目 `permissions.yaml` 的 `deny_tools` | 与 CLI deny 取并集 |

合并优先级（引擎 first-match）：**CLI `--deny-tool` → 项目 `deny_tools` + `rules` → CLI `--allow-tool`**；CLI deny 永远赢过同工具的项目 `allow` 规则。

---

## 3. 决策阶梯：一次工具调用会经过什么

### 3.1 阶梯（自上而下，命中即定，后段不再执行）

| # | 阶段 | 做什么 | 典型 reason | 规则/bypass 能否改变 |
|---|------|--------|-------------|----------------------|
| 1 | 入参校验 / 能力解析 | 参数非法、未知工具、缺能力声明 → 拒绝 | 校验类 reason | 不能 |
| 2 | Permission hook | hook `block` → 拒绝；hook `modify` → 改写参数后继续 | hook 自带 | **block 连 `bypass_permissions` 也挡** |
| 3 | 静态策略 | 能力域（读/写/网络/后台）、工具 deny/allowlist、只读约束、MCP 信任分级与远端写策略、sandbox 路径/URL/命令校验 | `policy:*` | allow 规则**不能放大**能力域 |
| 4 | 安全前置 | 根/主目录断路器、敏感写保护、外部目录门 | `shell_breaker:root_home_removal`、`sensitive_write:secret`、`external_dir:admit` | 规则不能放开；**断路器是 HardAsk（连 bypass 也要人工确认）**，敏感写与外部目录门在 bypass 下被跳过/静默准入 |
| 5 | 规则 | 分层 `rules` first-match（deny 立即返回） | 规则自带 `reason` | —— |
| 6 | 记忆授权（grants） | `session`/`project` 记忆命中直接放行 | `grant:*` | bypass 跳过 grants；危险工具永不命中 |
| 7 | 只读快车道 | 纯只读能力 / 只读 shell 命令表 / `commands[]` 全只读 | `readonly:*` | 命中机密路径参数（`.env`、`id_rsa*`…）的读取**掉出快车道** |
| 8 | plan 自动进入门 | 模型请求进入计划模式需人工批准 | `plan_mode:model_auto_enter` | —— |
| 9 | 模式决策 | §1.1 的矩阵（含 `accept_edits` 安全文件命令快车道） | `mode:permission_mode_requires_approval`、`mode:accept_edits_safe_file`、`mode:plan_denies_non_readonly`、`mode:dont_ask_denies_unapproved`、`mode:bypass_permissions` | —— |
| 10 | Callback override | 宿主回调可补参数/改决策，**补丁必须重验硬约束** | —— | 不能越过 1–4 |
| 11 | resolveAsk | 询问 → 等待人工；批准可写记忆；无 AskHandler → 拒绝 | `ask:user_declined`、`headless_deny:approval_required` | bypass 在该阶段直接 allow |

### 3.2 规则管不到的三件事

1. **根/主目录断路器**：`rm -rf /`、`rm -rf ~`、`$HOME` 变体、env/包装器伪装等（reason `shell_breaker:root_home_removal`）——任何模式、任何 `allow` 规则都不能放行，只能拒绝或人工确认（按模式）。
2. **敏感写保护**：向密钥/凭据类路径（`.env`、`.env.*`、`.envrc`、`id_rsa*`、`credentials(.json)`、`.git-credentials`、`.ssh/**` 等，见 `sensitive_paths.go`）写入（reason `sensitive_write:secret`）——不可 remember、不能被 allow 规则放开；但 **`bypass_permissions` 会跳过它**（`TestEngineSensitiveWriteBypassSkipsGate` 固化了这条），要硬保证请写 `deny` 规则或别开 yolo。
3. **外部目录门**：路径参数（含 shell 的 `cwd`/`workdir`）与补丁文本落在会话工作区之外时，先要一次准入（`external_dir:admit`）。工作区内 / 已准入目录 / OS 临时目录不触发；`bypass_permissions` 静默准入；`dont_ask` 直接拒绝。

### 3.3 审批阶段

- 需要询问时，宿主（CLI/Web/ACP/子代理）接管：CLI 面板给出 `[1] 仅本次允许 [2] 拒绝 [3] 查看完整参数`（只读场景另有 `[4] 会话/团队内复用 10 分钟`）；Web 为批准 / 拒绝 + 可选「记住」与说明；ACP 为 `allow-once / allow-always / reject-once`。
- **无 AskHandler 的宿主**（headless、无 TTY 的 `exec`、未挂审批的子代理）一律 **fail closed**：reason `headless_deny:approval_required`。
- 批准可以携带**记忆**（`once` / `session` / `project`，见 §5）；也可以携带**补丁参数**，但补丁会重新过 1–4 的硬约束。
- 拒绝可附**自由文本反馈**：会并入决策 reason（`…; user feedback: <文本>`）随工具错误回到模型上下文，模型据此换方案而不是重试原命令。

---

## 4. 常用 recipes（可直接抄）

### 4.1 只读 git 不再逐条问

```yaml
version: 1
rules:
  - name: allow-git-read-only
    tools: ["Shell(git status)", "Shell(git diff:*)", "Shell(git log:*)", "Shell(git show:*)"]
    decision: allow
```

复合命令要**每段都命中**才放行：`git status && git log` 需要两条规则都覆盖（这里已覆盖）；`git status && npm test` 会整条掉回询问。未覆盖的其余 `git` 子命令（`checkout`/`reset`/…）仍按模式询问。

### 4.2 推送前必须人工确认

```yaml
version: 1
rules:
  - name: ask-before-push
    tools: ["Shell(git push:*)", "Shell(git push --force:*)", "Shell(git push -f:*)"]
    decision: ask
```

注意 `ask` 与 `bypass_permissions` 的关系：yolo 下**普通的 `ask` 规则会被解析为放行**（reason `bypass_permissions`），唯一连 yolo 也拦得住的是**硬问询（HardAsk）**——目前是根/主目录断路器（`shell_breaker:root_home_removal`）。要在 yolo 里真正拦住动作，请用 `deny` 规则或 hook `block`，不要依赖 `ask`。

### 4.3 保护 lockfile 与生成物

```yaml
version: 1
rules:
  - name: review-lockfiles
    tools: ["Edit(package-lock.json)", "Edit(pnpm-lock.yaml)", "Edit(yarn.lock)", "Edit(Cargo.lock)"]
    decision: ask
  - name: keep-generated-out-of-edits
    tools: ["Edit(dist/**)", "Edit(coverage/**)", "Edit(.next/**)"]
    decision: deny
```

相对路径（`dist/**`）在任意深度命中；要只针对工作区顶层，用 `/dist/**` 写法。

### 4.4 保护 `.git/` 与密钥材料

```yaml
version: 1
rules:
  - name: protect-git-dir
    tools: ["Edit(.git/**)", "Write(.git/**)"]
    decision: deny
  - name: review-secret-material
    tools: ["Read(.env)", "Read(**/*.pem)", "Read(id_rsa*)", "Edit(.ssh/**)"]
    decision: ask
```

`.env` 这类读取即使不加规则也不会静默放行（机密路径参数会被踢出只读快车道）；这条规则把意图固化到仓库里，避免依赖默认行为。

### 4.5 CI / 无人值守用 `dont_ask`

```yaml
# .aicli/permissions.yaml（随仓库提交）
version: 1
deny_tools: [upload_artifact]        # 硬名单兜底
rules:
  - name: ci-allow-read-only-tests
    tools: ["Shell(go test:*)", "Shell(npm test:*)", "Shell(npm run lint:*)"]
    decision: allow
  - name: ci-block-publish
    tools: ["Shell(npm publish:*)", "Shell(docker push:*)"]
    decision: deny
```

```bash
aicli exec --permission-mode dont_ask --prompt "跑测试与 lint，报告失败"
```

语义：读与 allow 规则照常、其余「该问的」全部变成拒绝（fail closed）；任何试图写文件/联网/起后台任务的命令会直接失败并带 `mode:dont_ask_denies_unapproved`。**不要**在不可信输入下用 `--yolo` 替代。

### 4.6 外部目录：预准入而不是每次批准

```bash
aicli chat --add-dir ../shared-lib --add-dir /srv/data      # 启动即准入（可重复）
```

```text
/add-dir                 # 在当前会话内准入（等价入口）
/add-dir list            # 查看当前会话的准入集合
/add-dir remove <path>   # 撤销
```

- 集合随会话持久化，恢复会话仍有效；agent 选项 `allowed_roots`（别名 `additional_directories` / `additionalDirectories`）与 ACP `session/new|load|resume` 的 `additionalDirectories` 并入同一集合。
- 没有预准入时，工作区外的路径参数会触发一次 `external_dir:admit` 询问；批准后该**目录**并入本次会话集合，不再重复询问。
- 已注册 skill/plugin 目录读免门、写仍需准入；OS 临时目录读写都免门。

### 4.7 团队共享 vs 个人本地

```yaml
# <project>/.aicli/permissions.yaml —— 提交进仓库：团队一致的规则
version: 1
rules:
  - name: allow-git-read-only
    tools: ["Shell(git status)", "Shell(git diff:*)"]
    decision: allow
  - name: ask-before-push
    tools: ["Shell(git push:*)"]
    decision: ask
```

```yaml
# <project>/.aicli/permissions.local.yaml —— 写进 .gitignore：个人偏好
version: 1
allow_tools: []              # 只做「收窄」，不会放大团队权限
rules:
  - name: my-strict-mode
    tools: ["Shell(git push:*)"]
    decision: deny           # 个人永远不自动推
```

```yaml
# ~/.aicli/permissions.yaml —— 个人全局
version: 1
disable_bypass: true         # 我的机器上永不开 yolo
```

分层是**累积 + first-match**：用户级规则先求值，因此用户级 deny 可以压过项目级 allow；`deny_tools` 三层并集、只增不减。

---

## 5. 审批与记忆

### 5.1 审批选项

| 宿主 | 选项 | 备注 |
|------|------|------|
| CLI chat | `[1] 仅本次允许` `[2] 拒绝` `[3] 查看完整参数`（只读场景另有 `[4] 复用 10 分钟`） | `[4]` 是进程内 TTL 复用，不写 grants 文件 |
| Web | 批准 / 拒绝 + 可选「记住」（作用域 `仅本会话` / `本项目`）+ 可选说明 | 后端下发 `remember_pattern` 时才出现勾选，勾选前即展示将记住什么 |
| ACP | `allow-once` / `allow-always` / `reject-once` | —— |
| 子代理 | 父会话侧 `resolve_agent_approval` 决策 | 子代理无独立审批 UI |

### 5.2 记忆（grants）

- 作用域：`once`（默认，不写）、`session`（会话内存 store，会话结束即失效）、`project`（写 `<workspace>/.aicli/grants.json`，**新会话仍生效**）。
- 记忆模式以 specifier 形态存储：`cmd:<base>:*`（仅当整条命令是单一基命令且非高风险时才泛化）、`path:`、`host:`、`exact:`；旧的无前缀模式按历史子串语义兼容。
- **不可记忆**：危险工具（`shell`/`bash`/`aicli_exec`/`background_task`）、根/主目录断路器、敏感写、外部目录准入——每次都需要人工确认。
- 记忆授权在求值时仍要先过断路器、敏感写与外部目录门，不会「一记了之」。
- 管理入口：CLI `/approval-reuse`（查看/清理本地复用）、`aicli exec --approval-reuse off|session_readonly_shell|team_readonly_shell`、Web 设置页（`GET/POST /harness/grants`）。
- **project 记忆会写进工作区**：请把 `.aicli/grants.json` 视作个人授权文件（建议加入 `.gitignore`，不要提交）。

### 5.3 拒绝反馈

拒绝时可附自由文本说明，会并入决策 reason（`approval_denied; user feedback: 只改这一个文件`）随工具结果回到模型上下文——比单纯拒绝更能让模型换方案。

### 5.4 按需解释（模型摘要）

- 审批界面上的「解释」是**只读**动作：调用一次后台模型对命令/补丁做摘要（成本、影响、风险点），**不进入决策链**、不写状态。
- 模型不可用/超时会退回规则摘要（工具、原因、风险、参数摘要、可否记忆），界面会标注解释来源（模型名 / 规则）。
- 解释的每次调用都计入 usage 账本（`origin=approval_explain`）。
- 生成策略可切换：`off`（只用规则摘要）/ `on_demand`（默认，点「解释」才调用）/ `pre_generate`（读路径后台预热）。运行时用 `AICLI_APPROVAL_EXPLAIN_MODE` 或 Web 设置页「审批解释模式」切换；设置页是**进程级临时开关**（`GET/PUT /api/runtime/config/approval-explain`），不写配置文件，重启后回到 env / 默认值。

---

## 6. 已知限制与边界

1. **只读不放宽、allowlist 只收窄**：只读工具与只读 shell 默认放行；`allow_tools` / `--allow-tool` 是「白名单门」，只能限制不能放宽。想「只允许读某目录」请用 `Read(...)` 规则或 sandbox，而不是 allowlist。
2. **shell 命令字符串内的绝对路径不做目录门**：`cat /etc/hosts` 既不触发 `external_dir:admit`，也不参与路径 specifier 匹配；参与的是结构化路径参数与 `cwd`/`workdir`。
3. **opaque 命令不自动 allow**：不可解析、含动态语法（`> < \` $`）、或包装器/解释器 payload 的命令，`allow` 规则一律不生效（掉回模式询问）；`deny`/`ask` 仍可命中。
4. **大小写与符号链接**：路径包含判定不做 `EvalSymlinks`；symlink 逃逸依赖 OS 权限与 sandbox 配置（Web 文件浏览另有 `fsscope` 防逃逸，不覆盖 agent 工具链）。
5. **复合命令的 allow 是「全段命中」**：一条 `&&` 链里有任何一段没有被 allow 覆盖，整条掉回询问。
6. **`disable_bypass` 只管 bypass**：`dont_ask` 与 `plan` 不受它影响；它也不会让 `default` 变得更严。
7. **记忆是便利不是沙箱**：`project` 记忆落工作区文件，需要像代码一样评审；危险工具与安全前置永远不可记忆。
8. **平台差异**：CLI 的 `/mode` 只改权限模式字段，不清理 durable plan 状态（请用 `/plan quit`）；HTTP/Web 从 plan 切出会自动 `quit` 收口并归档。ACP/Web 的入口集合小于 CLI（例如 Web 只能「切出」plan）。
9. **解释与规则模板的边界**：解释只是提示，任何审批结果都以决策阶梯为准；识别不出语义时宁可不解释。
10. **权限文件写坏了等于没有**：语法/取值非法会让整个文件被丢弃（CLI 只记 warning 后继续），不会 fail closed——把它当代码评审，并在 CI 里做一次装载校验（本仓库用手册示例测试同源的 `ParsePermissionsFile` 兜底）。

---

## 7. 相关文档

| 文档 | 内容 |
|------|------|
| [`docs/product/project-permissions.md`](../product/project-permissions.md) | 项目权限文件 schema、specifier 参考、合并优先级、配置分层、外部目录门、grants 细节（**权威细节**） |
| [`docs/product/folder-trust.md`](../product/folder-trust.md) | 文件夹信任：项目插件 / agents / MCP 配置的准入（与权限流水线正交） |
| [`docs/aicli/interactive-mode.md`](./interactive-mode.md) §3 | CLI 权限模式入口与审批面板逐行说明 |
| [`docs/aicli/plan-mode.md`](./plan-mode.md) §1/§2.3 | plan 模式与 `/mode` 的差异、`/plan` 子命令全表 |
| [`docs/aicli/exec.md`](./exec.md) §八 | `exec` 的 `--permission-mode` / `--approval-reuse` / `--deny-tool` |
| [`docs/analysis/commandcode-permissions-design-borrowing-20260926.md`](../analysis/commandcode-permissions-design-borrowing-20260926.md) | 设计来源、落地状态（§9–§12）与剩余项 |
