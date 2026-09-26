# CommandCode Permissions 文档设计借鉴分析

- **日期**：2026-09-26
- **来源**：<https://commandcode.ai/docs/permissions>（Command Code "Permissions" 文档全文，含权限模式、规则语法、提示交互、安全行为、决策阶梯/决策表、设置与已知限制）
- **对照对象**：本仓库 aicli 的权限与审批实现（本次以只读方式完成代码盘点，全部结论附 `文件:行号`）：
  - 策略核心：`backend/internal/policy/{engine,modes,rules,grants,file_grants,permissions_file,taxonomy,capability,capability_scope,tool_policy,boundary,approval,plan_paths}.go`
  - 执行安全：`backend/internal/executor/sandbox.go`、`backend/internal/toolkit/tools/bash.go`、`backend/internal/fsscope/scope.go`、`backend/internal/patchutil/paths.go`
  - 宿主与审批：`backend/internal/chat/actor.go`、`backend/cmd/aicli/commands/chat_runtime_events.go`、`backend/internal/api/runtimeapi/{permission_mode_handlers,session_runtime_support}.go`、`frontend/src/components/workspace/pending-interaction-bar.tsx`
  - 子代理 / MCP / hooks：`backend/internal/toolbroker/spawn_agent_permission.go`、`backend/internal/policy/capability_scope.go`、`backend/internal/mcp/**`、`backend/internal/hooks/types.go`
- **结论口径**：区分「值得借鉴（缺口）」「已有且更强（不要回退）」「与 CommandCode 一致的已知限制」；每条建议给出代码落点、优先级与验证方式。

> 系列文档：`commandcode-plan-mode-design-borrowing-20260925.md`（plan mode）、`commandcode-mcp-design-borrowing-20260925.md`（MCP）、`commandcode-interactive-mode-design-borrowing-20260925.md`（交互模式）。

---

## 1. TL;DR：可借鉴清单

| # | 主题 | CommandCode 做法 | aicli 现状 | 建议 | 优先级 |
|---|------|------------------|------------|------|--------|
| 1 | **规则 specifier 语法** | 规则可写 `Shell(git push:*)`、`Read(.env*)`、`Edit(src/**)`、`WebFetch(domain:...)`，支持 `//`/`~/`/`/` 路径锚点与 `*`/`**` | `Rule` 只有 `Tools []string` 精确名 + `Capabilities`（`policy/rules.go:6-23`）；项目文件 schema 仅 `tools/capabilities/decision/reason`（`policy/permissions_file.go:47-53`）；无括号语法、无路径模式 | 新增 specifier 解析与匹配层（命令/路径/域名三类），接入 `.aicli/permissions.yaml` 与 CLI，保持纯工具名旧语法兼容 | **P0** |
| 2 | **dont-ask（fail-closed 无人值守模式）** | 第 5 种模式：不提问、只跑已允许项，其余 **deny**；CI/CD 用 | 无该模式；仅"无 AskHandler 的宿主"逐次 deny（`policy/engine.go:668-675`，StageHeadlessDeny）；交互宿主无法显式选择 fail-closed | 增加 `ModeDontAsk`（`modes.go` 枚举/ParseMode、`modeDecision` 把 ask→deny、CLI/Web/ACP 入口、settings defaultMode），hooks/硬 deny 语义不变 | **P0** |
| 3 | **根/主目录删除断路器** | `rm -rf /`、`rm -rf ~`、`rm -rf $HOME` 在任何模式（含 yolo）都 **ask**（dont-ask/plan 为 deny）；防欺骗：env 前缀、包装器、引号、命令替换、`$HOME` 变体 | 仅 bash 工具硬编码黑名单子串（`toolkit/tools/bash.go:42-53,936-941`，命中即硬拒）；`rm -rf ~`、`rm -rf $HOME`、`rm -fr /` **未覆盖**（全仓未找到） | 结构化命令解析 + 独立 breaker：bypass 也须确认，plan/dont-ask deny；覆盖 env 前缀/包装器/引号/命令替换；配防欺骗测试矩阵 | **P0** |
| 4 | **敏感写入保护** | 写 `.env*`、`*.pem`、`id_rsa`、shell rc、`.git/**`、`.ssh/**`、`.mcp.json`、IDE 配置等，default/accept-edits 必 ask；仅"内容级 allow 规则"可豁免 | **未找到**任何内建敏感路径写保护（`.env` 仅文本预览启发式 `filebrowse/stat.go:132`；`configwriteguard` 是源码 AST 测试守卫、`historyguard` 是压缩，非运行时） | 内置敏感路径分类 + ask 优先（yolo 除外）；豁免必须命中路径级 allow 规则；拒绝/批准写审计事件 | **P0** |
| 5 | **外部目录门 + /add-dir + 例外** | workspace 外读写/cwd 先 **ask 纳入会话根集合**（`/add-dir`、`additionalDirectories`）；OS 临时目录静默授权；skill 目录只读豁免；plan 文件写豁免；批准 ask 的同时即授予目录 | 无 `/add-dir`；ACP `additionalDirectories` 仅解析不生效（`internal/acp/types.go:484`、`docs/acp/README.md:382`）；无"必须位于工作区"的通用检查；CLI chat 未接线 policy sandbox（`cmd/aicli/commands` 内 `policy.Sandbox=` 仅测试）；临时目录无豁免 | 会话级 allowed roots + 工具/策略门；落地 `/add-dir` 与 `additionalDirectories`；temp 静默、skill 只读、plan 写豁免；外部目录授权可持久化 | **P0** |
| 6 | **复合命令逐子命令匹配 + 匹配不对称** | `Shell(git status)` **不**授权 `git status && rm -rf .`；每个子命令独立匹配；deny/ask 激进（env 剥离、包装器、raw 串兜底），allow 保守（须全部匹配、opaque 不自动放行） | 只读快车道对复合一律拒绝（保守但摩擦大，`policy/grants.go:242-245`）；规则层 `Shell` 是整工具粒度，无命令解析；CLI 侧另有一份拆分式分类仅用于"能否记住审批"（`chat_runtime_events.go:9199-9218`） | 引入 shell 分词/分段器：规则按子命令匹配；只读复合"每个子命令都只读且无重定向"才放行；allow 规则要求全部子命令命中；opaque 命令绝不自动 allow | **P1** |
| 7 | **只读 shell 的机密参数过滤** | 只读快车道对 secret 参数（`.env`、`id_rsa`、`*.pem`）落出 → 进入审批 | `cat` 等属无条件只读（`policy/grants.go:278`），`cat .env` 在所有模式（含 plan）自动放行（`policy/engine.go:576-587`） | 只读分类器增加 secret-arg 检测；命中后落出快车道，走 mode/审批（plan 下 deny） | **P1** |
| 8 | **审批选项统一：拒绝反馈 + remember 作用域** | `allow once / allow + remember / deny with feedback`；记忆粒度：文件→会话 accept-edits，shell→项目 `Shell(git:*)` 规则，MCP→项目 settings 工具名 | CLI 有 `[1][2][3][4 复用 10 分钟]`（`chat_runtime_events.go:5601-5633`，第 4 项是进程内 TTL map）；ACP `allow-once/allow-always/reject-once`；Web 仅 Approve/Deny 且不发送 Remember（`frontend/src/api/runtime/sessions.ts:442-477`）；**无"拒绝并附自由文本反馈"**；grants.json 存在但引擎只写 tool 级 session grant（`policy/engine.go:717-720`） | 扩展 `ApprovalResponse`（Feedback / RememberScope=session|project / Pattern）；三宿主选项对齐；批准记忆落 `grants.json`（危险工具除外），支持查看/撤销 | **P1** |
| 9 | **配置分层累积 + disableBypass** | `settings.json`(项目共享) + `settings.local.json`(私有) + 用户全局；规则**跨文件累积**，用户级 deny 不可被项目撤销；`disableBypass` 在引擎/CLI/TUI/决策存储四层强制 | 仅项目 `.aicli/permissions.yaml`（`policy/permissions_file.go:13-17,68-94`）+ profile ToolPolicy；无用户级/本地级、无累积层；无 disableBypass（仅切换 bypass 时二次确认 `chat_permission_mode.go:57`） | 增加用户级/本地级权限文件与累积/不可撤销语义；实现 `disable_bypass` kill switch（engine + CLI + ACP + Web + spawn 继承） | **P1** |
| 10 | **参数匹配 + 工具名通配符** | `Shell(run_in_background:true)` 之类按顶层参数匹配（**仅 deny/ask**）；工具名 `mcp__*`、`edit_*` 通配（deny/ask 生效；allow 仅允许命名 server） | 未找到：工具名逐项 `EqualFold` 精确匹配（`policy/rules.go:25-36`）；无参数匹配、无 `run_in_background` 概念 | 匹配器扩展 `param:value` 与名称 glob；allow 侧限制裸 `*`/`mcp__*`，防止"一刀放开" | **P1** |
| 11 | **accept-edits 安全文件命令快车道（递归删除例外）** | accept-edits 对 `mkdir/touch/cp/mv/非递归 rm/rmdir/sed` 免询问；`rm -r/-rf/--recursive`、`find -delete` 仍按 default 提示 | accept-edits 对**非只读** shell 命令一律 ask（只读命令已在快车道放行；`policy/modes.go:52-54`），无安全命令快车道 | 增加"安全文件命令"分类与快车道；显式排除递归删除/`find -delete`；配正例/反例测试 | **P1** |
| 12 | **模式入口/别名/Banner** | `--permission-mode` 与 `--accept-edits`/`--plan`/`--yolo` 简写；shift+tab 循环；模式 banner 常驻；slash **不提供** yolo 入口（slash 可被 agent 调用） | `--permission-mode`/`--yolo` 有；无 `--accept-edits`/`--plan` 简写；shift+tab/alt+m 循环已有（`ui/keymap/keymap.go:46-52`）；CLI 无常驻 banner（仅 session 信息行 `chat.go:1113`）；存在 `/yolo` slash（`chat_command_result.go:1059-1078`） | 补 flag 简写与常驻状态显示；核实 slash→yolo 的调用主体（若模型可达需隐藏/二次确认）；补模式别名（yolo/auto-accept 等） | **P2** |
| 13 | **按需命令解释（模型摘要）** | 审批时 `ctrl+e` 触发后台模型摘要；`on-demand-tool-descriptions` 开关（默认按需） | 已有**规则模板**解释自动附带（`chat_approval_explain.go:21-24,113-146`），无按需模型摘要 | 在审批面板增加"解释"动作，调用一次后台模型；设置项控制 on-demand/预生成 | **P2** |
| 14 | **文档信息架构** | 一分钟版 → 常用 recipes → 全量参考（decision ladder/table）→ design decisions → known limits | 文档散落（`docs/product/project-permissions.md`、`docs/user-guide/aicli.md`、代码注释），无规则语法参考与决策表 | 新增 `docs/aicli/permissions.md`：模式表 + 规则语法 + 决策表 + recipes + 已知限制 | **P2** |

> 已有且**不应回退**的能力（详见 §5）：应用层+OS 级沙箱与 profile、capability scope/只读子代理硬边界、审批补丁参数（PatchedArgs）与硬约束重验、durable grants（`grants.json`）与查看/撤销面、MCP trust level 三档与 `mcp__server__tool` 规范名、跨进程重启的 pending 恢复、denial guidance + 只读连续拒绝熔断、runtime-owned essentials 绕过窄 allowlist。

---

## 2. CommandCode 文档要点提炼

以下均来自文档原文行为（不含推测），按可借鉴维度归纳。

### 2.1 模式（mode 决定基线）

| 模式 | 语义 | 入口 |
|------|------|------|
| `default` | 变更类操作一律先问，读自由 | 默认；`/mode:default`、shift+tab |
| `accept-edits` | 工作区内常规编辑 + 安全文件命令（mkdir/touch/cp/mv/非递归 rm/rmdir/sed）免问；**不**自动接受任意 shell、MCP、递归删除 | `/mode:accept-edits`、shift+tab、`--accept-edits` |
| `plan` | 只读探索；仅计划文件可写 | `/mode:plan`、`--plan` |
| `yolo` | 权限绕过：除 deny 规则、ask 规则、根/主目录断路器外全部免问 | shift+tab、`--yolo`/`--dangerously-skip-permissions`；**slash 不可达** |
| `dont-ask` | 永不提问：只跑预允许项，其余 **deny**（fail-closed） | settings `defaultMode`、`--permission-mode dont-ask`；不在循环内 |

- 别名：`auto-accept→accept-edits`、`bypass→yolo`；Claude Code 拼写（`manual/acceptEdits/dontAsk/bypassPermissions`）、legacy `standard` 都接受。
- 模式切换也会"回答屏幕上已有的提示"：切到 accept-edits/yolo 视为批准、切到 plan 视为拒绝；但**安全检查强制**的提示（破坏性命令、敏感文件、显式 ask 规则）不会被模式切换回答。
- `--permission-mode` 优先于 `--plan`；`disableBypass` kill switch 覆盖 `--permission-mode yolo`、`--yolo`、`--dangerously-skip-permissions` 三个入口。

### 2.2 规则（deny > ask > allow，任何模式都先过规则）

- 三张列表：`deny`（永远赢）、`ask`（永远提示，yolo 也不例外；dont-ask 下变 deny）、`allow`。
- 规则形态：`Tool` 或 `Tool(specifier)`，工具名大小写不敏感，`Tool()` 等价 `Tool(*)`。
- specifier 语法：
  - Shell：精确命令、`npm run *`、`git * main`、`git:*`（后缀通配，注意 `git *` 与 `git*` 中空格语义）；**复合命令的每个子命令都要各自匹配**。
  - 路径（Read/Edit）：gitignore 风格，四个锚点 `//`(文件系统根)、`~/`(家目录)、`/`(项目根)、相对路径(项目内任意深度)；`*` 不跨段、`**` 跨段；符号链接的链接与目标都检查。
  - WebFetch：`domain:example.com` / `domain:*.example.com`。
  - MCP：`mcp__server__tool`、`mcp__server__*`、`mcp__server__get_*`、`mcp__*`（deny/ask 才允许裸 server 通配）。
- 工具名通配符：deny/ask 可用 `*`；allow 侧忽略裸 `*`/`mcp__*`（"allow 规则应当说明授了什么，而不是全部放开"）。
- 参数匹配：`Tool(param:value)`（仅 deny/ask，一次一个参数，取值支持 `*`；模型未发送的参数不匹配；`command`/`file_path`/`path`/`url` 等工具自有语义字段不可参数匹配）。
- 三个豁免工具（不经过规则）：`ask_user_question`、`agent`、`run_command`。
- **匹配不对称**（刻意设计）：deny/ask 激进——剥离 env 前缀、看穿 `timeout/nice` 等包装器、检查每个子命令、无法解析时按原始串匹配、路径大小写折叠、符号链接任一命中；allow 保守——不剥 env、复合命令必须每个子命令都命中、无法解析不自动放行、路径大小写精确且链接与目标都要命中。

### 2.3 提示交互与安全行为

- 每次提示三个选项：**允许一次 / 允许并记住 / 拒绝并反馈**；记忆范围随对象：文件操作→"本会话开启 accept-edits"；shell→写入项目设置 `Shell(git:*)`（复合命令为每个非只读基命令各写一条；任意代码解释器如 `bash/python/curl` 只记精确命令）；MCP→写工具名到项目设置。
- 命令解释：`ctrl+e`（VS Code 系终端 `ctrl+y`）按需后台模型摘要；`on-demand-tool-descriptions` 开关（默认 on）。
- 强制提示（不可被模式/记忆回答）：根/主目录删除断路器、递归删除（accept-edits 下仍提示）、敏感写、工作区外路径、显式 ask 规则。
- 根/主目录断路器：`rm -rf /`、`rm -rf ~`、`rm -rf $HOME` 及变体（env 前缀、包装器、引号 payload、命令替换、粘连分隔符、`$HOME`/`${HOME}` 拼写）都识别；除 dont-ask/plan 为 deny 外一律 ask。
- 敏感写入清单：`.env`、`.env.*`、`*.pem`、`*.key`、`id_rsa`、`id_ed25519`、credentials；`.bashrc`、`.zshrc`、`.profile`、`.gitconfig`、`.mcp.json` 等持久化向量；`.git/**`、`.ssh/**`、`.aws/**`、`.gnupg/**`、`.kube/**`、`.vscode/**`、`.idea/**`、`.husky/**`、`.devcontainer/**`、`node_modules/.bin/**`、工具自身设置。
- 外部目录：workspace 外读写/cwd → 先问是否把目录纳入会话（等同 `/add-dir`/`additionalDirectories`）；OS 临时目录在所有模式静默授予（读免问、写仍按普通写规则）；已注册 skill 目录只读免授予；plan 文件写在 plan 模式豁免；批准外部路径的 ask 同时授予该目录。
- `disableBypass`：`"disable"`/`true` 让 yolo 不可进入；建议放用户全局文件（项目改不了）。

### 2.4 决策阶梯（文档第 1 步到第 12 步，测试逐格固化）

```
1 deny 规则 → DENY（yolo 也一样）
2 ask 规则 → ASK（yolo 也一样；dont-ask 变 DENY）
3 工作区外？ → ASK 纳入目录
4 plan 模式 + 变更？ → DENY（计划文件例外）
5 taste 目录写 → 重定向
6 畸形写（无路径） → DENY
7 只读？ → ALLOW（所有模式）
8 rm -rf / ~ $HOME → ASK（yolo 也一样）
9 yolo？ → ALLOW
10 敏感写？ → ASK
11 allow 规则 → ALLOW
12 accept-edits + 工作区内？ → ALLOW
   否则 → ASK（dont-ask 下 DENY）
```

关键结论：`yolo` 位于第 9 级，**规则、外部目录门、断路器都在它前面**；"deny 在每层赢，allow 在每层都不赢"；`dont-ask` 把一切"本该 ask"变成 deny；**每次策略拒绝都带模型可执行的指导**，只有人类说"不"才结束回合。

### 2.5 设置与前置/已知限制

```jsonc
{
  "permissions": {
    "defaultMode": "default",
    "allow": ["Shell(git status:*)", "Shell(npm run *)", "mcp__github__get_issue"],
    "ask":   ["Shell(git push:*)", "Read(.env*)"],
    "deny":  ["Shell(git reset --hard*)", "Read(secrets/**)", "Edit(.git/**)"],
    "additionalDirectories": ["~/shared-libs"],
    "disableBypass": "disable"
  }
}
```

- 累积与优先级：规则跨 settings 文件累积，用户级 deny 不能被项目 allow 撤销；"任意层级 denied = denied；deny>ask>allow；specificity 不改变顺序"。
- 已知限制（文档自述）：多文件通配读的展开发生在工具内，精确文件 deny 规则可能匹配不到扩展前的通配条目；**没有严格 read allowlist**（读默认自由，只能靠 ask/deny 限制）；shell 命令里的路径参数不做外部目录门（命令参数不是可靠的路径边界）；无法解析的命令永不自动 allow；参数规则只看见模型实际发送的参数。

---

## 3. aicli 现状盘点（代码证据）

### 3.1 模式与入口

| 维度 | 现状 | 证据 |
|------|------|------|
| 模式集合 | `default / accept_edits / plan / bypass_permissions` 共 4 种 | `policy/modes.go:8-13` |
| 解析/归一 | `normalizeMode` 大小写+空白归一，未知值**静默回退 default**；`ParseMode` 严格返回 `ok=false` 供调用方拒绝 | `policy/modes.go:15-22,29-40` |
| 别名 | agentdef 同时读 `permissionMode`/`permission_mode`；`dont_ask` 被当作 default 处理（注释：ask 解析仍 headless deny） | `agentdef/build.go:24-25,62,119-121` |
| CLI | `--permission-mode`（`default/accept_edits/plan/bypass_permissions`，非法报错）；`--yolo` 映射 bypass；**无** `--accept-edits`/`--plan` 独立简写 | `chat_permission_mode.go:10-25`；`chat_command.go:127,132` |
| 键位循环 | `default → accept_edits → plan → bypass_permissions`，shift+tab / alt+m | `ui/keymap/keymap.go:46-52`；`chat_permission_mode.go:28-63` |
| Slash | `/permission-mode`、`/mode`、`/yolo`、`/plan` 存在；`/yolo` 有二次确认 | `chat_command_result.go:457,1059-1078`；`chat_permission_mode.go:57` |
| HTTP / ACP / Web | `GET/POST /sessions/{id}/permission-mode`；ACP config option（bypass 展示名含 yolo）；Web composer 下拉常驻 | `permission_mode_handlers.go:20-26,56-132`；`agent_stdio_mode.go:14-35`；`composer-permission-mode-control.tsx:35-105` |
| 常驻显示 | CLI **无** mode banner，仅 session 信息行 `Permission Mode:` 与审批 tip | `chat.go:1113`；`chat_runtime_events.go:6908-6912` |
| disableBypass | **未找到**（检索 `DisableBypass`/`AICLI_DISABLE_BYPASS` 均无命中） | — |

### 3.2 规则与配置装载

| 维度 | 现状 | 证据 |
|------|------|------|
| 规则结构 | `Rule{Name, Tools []string, Capabilities, Decision, Reason}`；匹配=工具名 `EqualFold` 精确 + 能力全含 | `policy/rules.go:6-23,25-51` |
| specifier/通配/参数/路径模式 | **全部未找到**（无括号语法、无工具名 glob、无 `param:value`、无路径 glob 匹配器） | `policy/permissions_file.go:47-53`；`policy/rules.go` |
| 决策取值 | `allow|yes|true` / `deny|block|false|no` / `ask|prompt|approval`；空/非法使整个文件解析失败 | `policy/permissions_file.go:148-164,345-358` |
| 项目文件 | `.aicli/permissions.yaml` → `.aicli/permissions.yml`（命中即用）；字段 `version/deny_tools/allow_tools/rules` | `policy/permissions_file.go:13-17,38-53,68-94` |
| 合并优先级 | CLI `--deny-tool` → 项目 `deny_tools`+`rules`（原顺序）→ CLI `--allow-tool`；规则前插、重复应用幂等；CLI allow 与既有 allowlist 求交 | `policy/permissions_file.go:212-216,247-322`；`chat_permissions_overlay.go:54-58,94-106` |
| 用户级/本地级累积 | **未找到**（仅项目文件 + profile ToolPolicy 作为 base） | `policy/permissions_file.go`；`profileinput/inputs.go:173-217` |
| 硬 allowlist 例外 | runtime-owned essentials（plan/ask/collab/todos 等）绕过窄 allowlist，但仍受显式 deny/能力域/只读约束 | `policy/tool_policy.go:126-132,152-190` |

### 3.3 决策流水线（Engine.Evaluate）

文档化顺序见 `policy/engine.go:54-71`，实现逐步为：

1. 入参校验/能力解析（`engine.go:195-205`）→
2. **Permission hook**：block→deny，modify→补丁并继续（`219-258`）→
3. **静态策略**：能力域 → 工具 deny/allowlist/只读 → MCP trust/remote → sandbox 路径/URL/命令（`260-264`、`409-434`、`tool_policy.go:286-391`）→
4. **规则** first-match，deny 立即返回（`266-273`）→
5. **记忆 grants**（bypass 跳过；危险工具永不命中，`275-280`）→
6. **只读自动放行**：纯只读能力 / taxonomy 只读非 exec / shell 命令命中只读表（`282-292`、`554-589`）→
7. **模型进 plan 确认门**（`294-310`，reason 固定 `plan_mode:model_auto_enter`）→
8. **模式决策**：plan 写白名单先行，否则 `modeDecision`（`312-338`、`modes.go:42-62`）→
9. **Callback override**，补丁须过硬约束（`340-375`）→
10. 汇总补丁 → **resolveAsk**：bypass→allow；无 AskHandler→deny（StageHeadlessDeny）；有→审批，批准可写 grant，审批补丁重验硬约束（`377-390`、`654-751`）。

硬约束复验入口 `validateHardConstraints`（静态策略+硬 deny 规则）供审批/回调补丁与 `ExecuteApprovedToolCall` 重放使用（`engine.go:450-483,485-552`）。

### 3.4 只读 shell 与危险命令

| 维度 | 现状 | 证据 |
|------|------|------|
| 只读快车道 | 纯只读能力/taxonomy 非 exec 只读直接 allow；shell 按命令表判定 | `policy/engine.go:554-589` |
| 只读命令表 | `rg/grep/file/ls/dir/pwd/cat/type/head/tail...` + `git status/diff/log/...` + `go version/doc/...` + `npm/pnpm/... --version`；`cat` 等为无条件允许 | `policy/grants.go:271-291,356-554` |
| 复合/重定向 | 复合（`&&/||/;/|/&/换行`）**整体拒绝**；`> < \` $` 动态语法拒绝；cmd `%`/`!` 成对拒绝 | `policy/grants.go:240-258` |
| 批量 | `commands[]` 全部只读才放行（空数组不放行） | `policy/engine.go:581-586,591-626` |
| 机密参数过滤 | **未找到**：`cat .env` 命中只读表 → 所有模式（含 plan）自动放行 | `policy/grants.go:278`；`policy/engine.go:576-587` |
| 危险命令黑名单 | bash 工具硬编码子串黑名单（`rm -rf /`、`rm -rf /*`、`mkfs`、`dd if=/dev/zero`、fork bomb、`chmod -R 777 /`…），命中即硬拒，无用户确认通道 | `toolkit/tools/bash.go:42-53,936-941` |
| 根/主目录断路器 | **未找到**（无 `rm -rf ~`/`$HOME`/`rm -fr /` 变体；无 env/包装器防欺骗） | — |
| accept-edits 递归删除 | 无特例：accept-edits 对非只读 shell 一律 ask（只读命令已自动放行；更保守，但也没有"安全文件命令"快车道） | `policy/modes.go:52-54` |
| 审批"危险表" | TUI 仅用危险表禁止"记住审批"，不改变执行决策；解释器只输出人话说明 | `chat_runtime_events.go:9095-9110,9223-9250`；`chat_approval_explain.go:126-127` |

### 3.5 路径、沙箱与外部目录

| 维度 | 现状 | 证据 |
|------|------|------|
| 沙箱模型 | 应用层 `Sandbox{Enabled, AllowedPaths, DeniedPaths, ReadOnlyPaths, Allowed/DeniedCommands, Allowed/DeniedHosts, BlockNetwork, MaxExecutionTime}` + `OSSandbox=off|auto|require`；profile `off|workspace|read-only|strict` | `executor/sandbox.go:26-49`；`executor/sandbox_profile.go` |
| 生效条件 | `active()` 为假时所有检查短路返回 nil；策略侧 `p.Sandbox == nil` 直接跳过路径/URL/命令校验 | `executor/sandbox.go:104-142,261-266`；`policy/tool_policy.go:320-324,363-388` |
| 路径解析 | 相对路径锚定 session workspace root（ctx）→ 工具 basePath → 进程 cwd；**绝对路径原样使用** | `toolkit/tools/sandbox_support.go:67-80,87-105`；`policy/tool_policy.go:400-423` |
| 符号链接/大小写 | agent 工具层用 `filepath.Rel` 判包含，不做 `EvalSymlinks`、无显式大小写归一；强 symlink 防逃逸只在 Web `fsscope`（不是 agent 工具链） | `executor/sandbox.go:367-382`；`fsscope/scope.go:311-322,383-439` |
| 宿主接线 | Web/API 在 workspacePath 非空时把 workspace 加进 `AllowedPaths` 并启用 sandbox；**CLI chat 未见生产接线**（仅测试中出现 `policy.Sandbox=`） | `api/runtimeapi/handler.go:5231-5243`；`tools/manager.go:62-66` |
| 外部目录授权 | 无 `/add-dir`；ACP `additionalDirectories` 仅解析不生效；`workspaceregistry` 只服务文件浏览 scope，不参与工具允许集合 | `internal/acp/types.go:484`；`docs/acp/README.md:382`；`workspaceregistry/store.go:59-95`；`fsscope/scope.go:148-172` |
| 临时目录/skill 目录例外 | **未找到**（无 TEMP/TMPDIR 特殊授权；skill 目录无只读豁免逻辑） | — |
| 敏感写入保护 | **未找到**（`.env` 仅预览启发式；`configwriteguard` 为源码守卫、`historyguard` 为压缩；唯一写保护来自需显式配置的 sandbox `DeniedPaths/ReadOnlyPaths`） | `filebrowse/stat.go:132`；`configwriteguard/doc.go:1-11`；`historyguard/active_turn.go:20-22`；`executor/sandbox.go:114-139` |
| background_task | 能力 `CapBackgroundTask`；default/accept_edits→ask、plan→deny、bypass→allow；**不可记忆**（危险工具）、read-only 策略硬拒；执行侧无沙箱约束 | `policy/capability.go:91-92`；`policy/modes.go:52-58`；`policy/grants.go:174-182`；`policy/tool_policy.go:137-140` |

### 3.6 审批、记忆与拒绝反馈

| 维度 | 现状 | 证据 |
|------|------|------|
| 审批协议 | `ApprovalRequest{ID,SessionID,ToolCallID,ToolName,ArgsJSON,Reason,RiskLevel,ExpiresAt}` ↔ `ApprovalResponse{Allowed,Reason,PatchedArgs,Remember}`；默认超时 30 分钟 | `policy/approval.go:10-28`；`policy/engine.go:111,684-690` |
| 宿主实现 | chat actor（`RequestApproval`：写 pending、发事件、等待 waiter/超时/ctx）；`/call` `/tool` `/skill` 有独立审批循环 | `chat/actor.go:4401-4491`；`command_invoke.go:390-458` |
| CLI 选项 | `[1] 仅本次允许 [2] 拒绝 [3] 查看完整参数`；只读场景有 `[4] 允许并在会话/团队内复用同类只读审批 10 分钟`（**进程内 TTL map，非 grants.json**） | `chat_runtime_events.go:5601-5633,91,6716-6770` |
| ACP 选项 | `allow-once / allow-always / reject-once`（`reject_always` 定义未进默认） | `acp/types.go:1128-1150`；`agent_stdio_bridge.go:205-212` |
| Web 选项 | 仅 Approve/Deny 两个按钮；请求体支持可选 `patched_args`；**不发送 Remember** | `pending-interaction-bar.tsx:190-211`；`sessions.ts:442-477` |
| 拒绝反馈 | **无"拒绝并附自由文本反馈"**：CLI 固定串 `approval_denied`，Web 只有布尔值 | `actor.go:1157-1158`；`chat_runtime_events.go:5617-5633` |
| 记忆存储 | `MemoryGrantStore`（默认 session）与 `FileGrantStore`（`<project>/.aicli/grants.json`，默认 project，原子写、去重、可撤销）；危险工具（shell/bash/aicli_exec/background_task）永不记住；引擎只写 tool 级 session grant | `policy/grants.go:22-97,174-182`；`policy/file_grants.go:13-35,105-107,144-190,219-251`；`policy/engine.go:717-720` |
| grants 管理面 | Web harness `GET/POST /harness/grants`；设置页可查看/新增；`/approval-reuse` 可清本地复用 | `api/runtimeapi/handler.go:1019-1020`；`harness_handlers.go:171-247`；`chat_command_result.go:1085-1120` |
| 拒绝指导 | 只读边界有可操作 guidance（错误码+fix 文案）；连续 3 次升级、5 次硬停本轮 | `agent/denial_guidance.go:15-43,64-75,96-109` |
| turn 影响 | 策略拒绝：工具记失败结果、turn 继续；人类拒绝：写入 `approval_denied` 工具结果并 resume，turn 继续 | `agent/loop.go:2656-2660`；`chat/actor.go:1157-1158,3589-3623` |
| 并发/恢复 | waiter 按 requestID 存 map（可多 waiter），但 RuntimeState 仅单个 `PendingApproval` 投影槽（后到覆盖）；进程重启可恢复 pending 审批 | `chat/actor.go:242,4434-4439,4724-4736`；`chat/runtime_state.go:109`；`chat_restored_pending.go:299-307,413-432` |

### 3.7 子代理、MCP、hooks 与无交互宿主

| 维度 | 现状 | 证据 |
|------|------|------|
| 子代理模式继承 | 省略 `permission_mode` 继承父模式；父 bypass→子请求 default/accept_edits 被钉回父模式；父 plan 钉住一切；**升权默认阻断**，需 `AICLI_AGENTS_ALLOW_PERMISSION_ESCALATION=1` | `toolbroker/spawn_agent_permission.go:33-37,78-87,91-95,104-113,119-127` |
| 只读子代理 | 能力集仅 `read_only/exec_shell/network/ask_user/agent_management`；写型工具与 `background_task` 从模型工具面移除并在执行时硬拒；shell 逐命令分类；**approval/bypass 不能放宽** | `policy/capability_scope.go:23-31,43`；`policy/tool_policy.go:120-147`；`agent/tool_surface_binding.go:151-156` |
| 子代理审批 | 子会话 actor 自挂 AskHandler；父侧经 `resolve_agent_approval` 决策；无 AskHandler 时 fail-closed deny | `chat/actor.go:5366-5383`；`toolbroker/broker.go:45`；`toolbroker/types.go:530-533`；`policy/engine.go:668-675` |
| MCP 命名/信任 | 规范名 `mcp__<server>__<tool>`；trust level `local|trusted_remote|untrusted_remote`（stdio 默认 local、remote 默认 untrusted）；默认 `BlockUntrustedMCP/BlockRemoteWrites=true`，untrusted 写型工具被拒；trust level 进入 hook payload | `mcp/registry/registry.go:231`；`mcp/config/types.go:13-18,258-269`；`policy/tool_policy.go:57-63,245-250`；`policy/engine.go:233-236` |
| MCP 审批 UI | 无专用 UI，走通用审批面（CLI 解释器对 `mcp__` 前缀有人话分支） | `chat_approval_explain.go:35-40`；`agent_stdio_bridge.go:182-192` |
| Hooks | 事件表 `pre_tool_use/permission_request/post_tool_use...`；block→不执行、modify→替换 args；权限 hook deny 即使 bypass 也硬拒；audit 异步 | `hooks/types.go:8-34`；`agent/loop.go:2613-2652`；`policy/engine.go:219-244` |
| Hooks 接线 | CLI/chat（ACP 复用）与 Web/API 已装配；**exec/headless 未找到装配点** | `chat_actor_host.go:2112-2118`；`api/runtimeapi/handler.go:5271` |
| 无交互宿主 | 无 AskHandler → `StageHeadlessDeny`、reason `approval_required`（每次 ask 逐次 deny，无会话级 fail-closed 模式） | `policy/engine.go:668-675` |
| 命令解释 | 规则模板自动附带（动作/目标/影响），**不调用模型**；无 ctrl+e 式按需模型摘要 | `chat_approval_explain.go:21-24,82-146`；`chat_runtime_events.go:5663` |

---

## 4. 重点借鉴项：设计映射与落点

### 4.1 【P0】规则 specifier 语法：命令模式 / 路径模式 / 域名 / MCP 通配

- **CommandCode 语义**：`Rule = Tool` 或 `Tool(specifier)`；shell 支持精确/前缀/`git:*` 与空格敏感匹配；路径支持 `//`（文件系统根）、`~/`（家目录）、`/`（项目根）、相对（项目内任意深度）四锚点与 `*`/`**`；WebFetch 支持 `domain:*.example.com`；MCP 支持 `mcp__server__*` / `mcp__server__get_*` / `mcp__*`。
- **aicli 缺口**：`Rule.Tools []string` 是逐项 `EqualFold` 精确匹配（`policy/rules.go:15-36`）；项目文件 schema 无括号字段（`policy/permissions_file.go:47-53`）；无法表达"只允许 `git status` 不许 `git push`""禁止读 `secrets/**`"这类最常见的诉求。`--deny-tool/--allow-tool` 同样只有整工具粒度。
- **设计映射**：
  1. 新增 `policy/specifier.go`：把 `Tools` 条目中形如 `Shell(...)`/`Read(...)`/`Edit(...)`/`WebFetch(...)` 的字符串解析为 `Specifier{Kind, Pattern}`；纯工具名保持旧语义（兼容现有 permissions.yaml）。
  2. 匹配器（`Rule.Matches` 扩展）：工具名做**友好名→规范名**归一（`Shell→shell/bash/aicli_exec`、`Read→view/grep/glob/ls`、`Edit→write/edit/multiedit/append_write/apply_patch`，映射表放 `policy/toolnames` 邻域，避免规则随工具改名失效）。
  3. 命令匹配：按 4.6 的分段器对每个子命令匹配；路径匹配：`//`（绝对）、`~/`（home）、`/`（工作区根，用 ctx 的 workspace root，与 `tool_policy.resolvePolicyPath` 同源）、相对（工作区任意深度）；`*` 不跨段、`**` 跨段；deny/ask 大小写折叠、allow 精确。
  4. 保留 `ToolExecutionPolicy` 的 `DenyTools/AllowTools` 为**硬闸精确名**（不做 specifier），specifier 只经 `Engine.Rules`；这样 bypass 语义、allowlist 语义不变，改动面可控。
  5. CLI 可选补 `--deny-rule/--ask-rule/--allow-rule`（重复参数），与项目文件同语法。
- **验证**：`policy/specifier_test.go` 表驱动矩阵：精确命令 vs 前缀、空格敏感（`ls *` vs `ls*`）、复合命令每段独立匹配、env 前缀在 deny 折叠、路径四锚点、`*`/`**`、大小写策略、MCP 三段通配；再加 decision-table 级别的 engine 测试（见 4.2/4.3 共用矩阵）。

### 4.2 【P0】dont-ask 模式：无交互宿主的 fail-closed 一等公民

- **CommandCode 语义**：`dont-ask` 永不提问——只运行预允许项（读、只读 shell、allow 规则），其余 **deny**；ask 规则在此模式下变 deny；不在 shift+tab 循环内，由 settings/flag 选择。
- **aicli 缺口**：只有"宿主没有 AskHandler"这一隐式路径会 deny（`policy/engine.go:668-675`，reason `approval_required`）；交互宿主无法显式选择"不提问、直接拒绝"的 CI 行为；`agentdef/build.go:119-121` 只是把 `dont_ask` 静默降级成 default。
- **设计映射**：
  1. `policy/modes.go`：新增 `ModeDontAsk = "dont_ask"`，纳入 `ParseMode`/`SupportedModes`/`normalizeMode`；`modeDecision` 在该模式下把"本该 ask"的请求返回 deny，allow/deny 分支不变（读与只读 shell 仍放行）。
  2. `resolveAsk`：`dont_ask` 下不做审批等待，直接 `DecisionDeny`（stage=mode，reason 建议 `mode:dont_ask_denies_unapproved`，与 headless 的 `approval_required` 区分，便于宿主与统计区分"策略拒绝"与"无人可问"）。
  3. 入口：CLI `--permission-mode dont-ask`、settings/profile `defaultMode: dont-ask`、HTTP/Web/ACP 枚举；`shift+tab` 循环**不含** dont-ask（与 CommandCode 一致）；`/mode dont-ask` 可选但建议要求显式确认。
  4. 语义边界：hook deny、硬 deny 规则、capability scope、sandbox、只读边界全部不变；已存在 grants/allow 规则继续生效（"预允许项"）；`enter_plan_mode` 确认门在 dont-ask 下应视为 deny（无人确认），并给模型可操作 guidance（`plan_mode:requires_confirmation`）。
- **验证**：engine 模式矩阵单测（5 模式 × 写/执行/网络/后台/只读/计划进入）；API handler 测试（`permission_mode_handlers_test.go` 同类）；`aicli exec --permission-mode dont-ask` 端到端冒烟（含"未预允许的写必须失败而不是挂起"）。

### 4.3 【P0】根/主目录删除断路器（防欺骗）

- **CommandCode 语义**：`rm -rf /`、`rm -rf ~`、`rm -rf $HOME` 及变体在**任何模式**（含 yolo）都 ask；dont-ask/plan 为 deny；识别 env 前缀、`timeout/nice` 包装器、复合命令、引号 payload、命令替换、粘连分隔符、`$HOME`/`${HOME}` 拼写。
- **aicli 缺口**：`toolkit/tools/bash.go:42-53` 的黑名单只覆盖 `rm -rf /`、`rm -rf /*` 等字面子串；`rm -rf ~`、`rm -rf $HOME`、`rm -fr /`、`rm -r -f /` **没有检测**；命中后是硬拒，没有"用户确认"通道；没有统一解析器，策略/工具/审批各自为政（另有两份拆分式分类器 `chat_runtime_events.go:9199-9218`）。
- **设计映射**：
  1. 新增共享 shell 分段/词法层（建议放 `internal/policy/shellrisk` 或 `policy/shellparse.go`，供 engine、bash 工具、审批 UI 复用）：段切分（`&&/||/;/|/&/换行`）、env 前缀剥离、常见包装器（`timeout/nice/env/nohup/xargs`）穿透、引号/`$()`/反引号解引用、`cd` 目标跟踪（可选）。
  2. 断路器判定：递归删除（`rm` + `-r/-R/--recursive`，含组合短参 `-fr`）且目标集包含 `/`、`/*`、`~`、`~/`、`~/*`、`$HOME`、`${HOME}`、`$HOME/*`；`find <root> -delete` 同类处理。
  3. 决策：在 `Engine.Evaluate` 增加"根/主目录 breaker"阶段，位置在模式决策+yolo **之前**（对应 CC 第 8 级）：bypass → ask；plan/dont-ask → deny；default/accept_edits → ask。审批响应**不可 remember**（复用 `IsDangerousTool` 危险语义或新标记），审批文案给一次性 yes/no。
  4. 与现有黑名单合并：`mkfs`、`dd if=/dev/zero of=/dev/sdX`、fork bomb 等仍保留硬拒（无确认通道）；把黑名单判定收敛到同一个解析层，避免三份实现漂移。
  5. accept-edits 下的递归删除：借 CommandCode 的"递归删除仍需提示"结论——若未来给 accept-edits 增加安全命令快车道（4.11），必须显式排除递归删除。
- **验证**：防欺骗矩阵（逐条覆盖文档列举的绕过：`FOO=bar rm -rf /`、`timeout 5 rm -rf /`、`bash -c "rm -rf /; echo ok"`、`echo $(rm -rf /)`、`$HOME`/`${HOME}`、`rm -fr /`）；断言 bypass 下仍 ask、plan/dont-ask 下 deny、审批不可 remember；integration 覆盖 batch/`commands[]` 两条路径。

### 4.4 【P0】敏感写入保护（内容级豁免）

- **CommandCode 语义**：写入 secret 材料（`.env`、`*.pem`、`id_rsa`…）、持久化向量（`.bashrc`、`.gitconfig`、`.mcp.json`…）、控制面（`.git/**`、`.ssh/**`、`.aws/**`、`.gnupg/**`、`.kube/**`、IDE/`.husky`/`.devcontainer`、`node_modules/.bin/**`、工具设置）在 default/accept-edits 必 ask；yolo 跳过；豁免只能靠**内容级 allow 规则**（裸 `Edit` 不放行）；敏感**读**默认自由（文件工具），shell 读另见 4.7。
- **aicli 缺口**：未找到任何运行时敏感写保护（`filebrowse/stat.go:132` 只是预览启发式）；现状"写 `.env` 在 accept_edits 下直接 allow"，只有用户自己写 `permissions.yaml` 才能拦。
- **设计映射**：
  1. `policy/sensitive_paths.go`：分类器（basename 精确/前缀 + 目录前缀 + 扩展名），清单可内置为默认值，并允许配置追加/移除（项目文件 `sensitive_paths` 字段，可选）。
  2. 决策插入点：按 CommandCode 阶梯放在**只读快车道之后、allow 规则之前**（`engine.go:282-292` 与 `296-338` 之间）：命中且调用含写意图（`CapWriteFS` 或 sandbox `OpWrite/OpDelete`）→ default/accept_edits: ask；bypass: allow；plan: 保持 deny；dont-ask: deny。
  3. 豁免规则：只有当匹配到**路径级** allow（specifier `Edit(.env.local)` 之类）才跳过 ask；裸 `Edit` 整工具 allow 不豁免（对应 CC 的 content-specific 语义）。
  4. 审批呈现：reason 带分类（secret/persistence/control-surface）+ 路径，复用 `chat_approval_explain.go` 模板渲染中文说明；批准/拒绝各发事件便于审计。
- **验证**：分类表驱动单测（每类 ≥2 样例 + 反例如 `notes.env.md`）；engine 矩阵（5 模式 × 敏感/普通 × 裸 allow / 路径 allow）；`accept_edits` 下写 `.env` 必问、写 `README.md` 不问的行为测试。

### 4.5 【P0】外部目录门：/add-dir、additionalDirectories 与豁免

- **CommandCode 语义**：workspace 外的读/写/shell cwd 先 **ask 纳入会话根集合**（等价 `/add-dir`、`permissions.additionalDirectories`），然后按普通模式规则；yolo 静默授予、dont-ask 除非预批准否则 deny；OS 临时目录所有模式静默授权（读免问、写按普通写规则）；已注册 skill 目录只读豁免；plan 文件写在 plan 模式豁免；**批准外部路径 ask 的同时授予目录**；shell 命令内的绝对路径参数不做此门。
- **aicli 缺口**：无 `/add-dir`；ACP `additionalDirectories` 解析但不生效（`docs/acp/README.md:382`）；无"必须位于工作区"的通用检查，边界完全依赖可选 sandbox（`policy/tool_policy.go:363-388` 在 `p.Sandbox == nil` 时直接放行）；CLI chat 未接线 sandbox（生产路径仅测试）；临时目录/skill 目录无豁免概念。
- **设计映射**：
  1. 会话级 `AllowedRoots`（工作区根 + 已授权外部目录）写入 session runtime state，并随 run 下发到 policy ctx；与现有 `toolctx.WorkspaceRoot` 同机制扩展为 roots 集合。
  2. Engine 新增"外部目录门"阶段（规则之后、模式之前，对应 CC 第 3 级）：对工具**路径参数**与 shell cwd 判定；目录外 → ask（`external_dir:admit`）；批准 → 目录入会话集合并继续原流程；yolo 静默授予；dont-ask → deny。
  3. 入口：`/add-dir <path>` slash、HTTP `POST /sessions/{id}/directories`、ACP `additionalDirectories` 生效化、settings `additionalDirectories`（见 4.9 分层）；`/directories` 可查看/移除。
  4. 豁免：`os.tempdir()` 前缀静默授予（读免问；写仍按普通写规则，敏感名如 `/tmp/.env` 仍触发 4.4）；skill 注册目录只读豁免（写仍门）；plan 允许路径在 plan 模式豁免（与现有 `PlanWriteAllowPaths` 合并判定，避免两套）。
  5. 与 sandbox 的关系：外部目录门是"会话授权层"，sandbox `AllowedPaths` 是"执行兜底层"；两层同时生效，且 CLI chat 需要把 workspace 注入 policy/sandbox（补 `cmd/aicli/commands` 的生产接线）。
- **验证**：engine 外部路径矩阵（读/写/cwd × 5 模式 × 已授权/未授权）；批准授予目录后的同类调用免问；ACP e2e 断言 `additionalDirectories` 真正生效；CLI `/add-dir` 冒烟。
- **风险/分阶段**：默认开启会改变现有"工作区外静默可写"的行为。建议 M3 先做"支持 + 显式开启（settings/env）+ 审计"，再在下一个版本默认开启（与 CommandCode 对齐）。

### 4.6 【P1】复合命令逐子命令匹配 + "deny/ask 激进、allow 保守"

- **CommandCode 语义**：`Shell(git status)` 不授权 `git status && rm -rf .`；每个子命令独立匹配；deny/ask 会剥离 env 前缀、穿透包装器、解析失败按 raw 串兜底；allow 必须每个子命令都命中、无法解析绝不自动放行。
- **aicli 缺口**：只读快车道对复合**整体拒绝**（`policy/grants.go:242-245`）——比 CommandCode 更保守，但代价是 `cat a | head -5` 这类常用只读管线也要走审批；规则层完全没有命令解析。
- **设计映射**：
  1. 复用 4.3 的共享分段器；`AssessShellReadOnlyCommand` 改为"逐段判定 + 无重定向/动态语法"才放行（CommandCode：`cat file | head -5 || echo none` 免问）。保守兜底：任一段无法解析 → 不放行。
  2. 规则匹配：specifier 的命令模式对每个子命令分别匹配；allow 要求全部命中；deny/ask 任一段命中即命中（并在 reason 中标注命中的子命令）。
  3. 保持 CLI 审批"能否记住"的第二套分类器与 policy 共用同一解析结果，消除 `chat_runtime_events.go:9199-9218` 的重复实现（长期收敛为单一来源）。
- **验证**：read-only 管线正/反例（`cat|head` 放行、`cat|tee`/带 `>` 不放行）；`git status && rm -rf x` 在 allow 规则下必须 ask/deny；batch 与单串两条路径一致。

### 4.7 【P1】只读 shell 的机密参数过滤

- **CommandCode 语义**：只读快车道对 secret 参数（`.env`、`id_rsa`、`.pem` 等）落出 → 走审批；这是"读默认自由"的唯一 shell 侧例外。
- **aicli 缺口**：`cat`/`type`/`head` 等在只读表里无条件允许（`policy/grants.go:278`），`cat .env` 在任何模式（含 plan）自动放行（`policy/engine.go:576-587`）。
- **设计映射**：
  1. `AssessShellReadOnlyCommand` 的参数扫描增加 secret 检测（复用 4.4 的分类器，输入改为命令 argv 中的文件参数）。
  2. 命中后返回新 reason（如 `secret_arg`）落出快车道：plan → deny；default/accept_edits → ask；bypass → 按模式 allow（或保持 ask？建议与 CC 一致：bypass 下允许，但不得被"记住"）。
  3. 文件工具（view/grep）的敏感读维持现状（读默认自由，靠 4.1 的路径规则限制）——与 CommandCode 明确对齐，不做默认拦截。
- **验证**：`cat .env`、`head id_rsa`、`grep -r key ~/.ssh` 落出；`cat README.md` 仍免问；plan 模式下 `cat .env` 变 deny 的矩阵测试。

### 4.8 【P1】审批选项统一：拒绝反馈 + remember 作用域

- **CommandCode 语义**：三选项（允许一次 / 允许并记住 / 拒绝并反馈）；记忆落到具体对象（文件编辑→会话 accept-edits；shell→项目 `Shell(git:*)`；复合命令按非只读基命令逐条；任意代码解释器只记精确命令；MCP→项目工具名）。
- **aicli 现状**：CLI 的 `[4]` 复用是 10 分钟进程内 TTL（`chat_runtime_events.go:91,6716-6770`），与 `grants.json` 双轨；ACP `allow-always` 映射 `Reuse`（`agent_stdio_bridge.go:205-212`）；Web 只有布尔 Approve/Deny 且**不传 Remember**（`sessions.ts:442-477`）；无"拒绝并反馈"。
- **设计映射**：
  1. `ApprovalResponse` 增加 `Feedback string`、`RememberScope`（`once|session|project`）、可选 `RememberPattern`（命令/路径片段）；`IsDangerousTool` 仍禁 remember；破坏性/敏感提示强制 once-only（拒绝 remember）。
  2. 引擎把 `RememberScope=project` 写 `FileGrantStore`（`<project>/.aicli/grants.json`），`session` 写内存 store；CLI `[4]` 改为走同一 `ApprovalResponse.Remember + Scope`，TTL map 仅保留为纯 UI 提示缓存。
  3. 各宿主选项对齐：CLI 增 `[5] 拒绝并说明原因`（自由文本进入 `Feedback`，作为 tool error/guidance 的一部分回给模型）；Web 增加 remember 勾选与反馈输入框；ACP 可映射到 `reject_always`（已定义未启用，`acp/types.go:173-179`）。
  4. `grant Pattern` 目前只有子串匹配实现（`policy/grants.go:184-200`），配合 4.1 升级为 specifier 存储格式，避免"记住 `git *`"退化为字符串包含。
- **验证**：三宿主一致性的 handler/组件测试；"remember project → 新会话仍生效"的 e2e；危险工具 remember 被拒测试；拒绝反馈进入模型上下文的测试。

### 4.9 【P1】配置分层累积 + disableBypass

- **CommandCode 语义**：`settings.json`（项目共享）+ `settings.local.json`（个人、gitignore）+ 用户全局；规则**累积**，用户级 deny 不可被项目 allow 撤销；`disableBypass` 在引擎、CLI 入口、TUI 循环、决策存储四层强制，建议放用户全局文件。
- **aicli 缺口**：仅项目 `.aicli/permissions.yaml`（命中即用，无累积）；无 disableBypass，仅 `/yolo` 二次确认（`chat_permission_mode.go:57`）。
- **设计映射**：
  1. 权限文件分层：`~/.aicli/permissions.yaml`（用户级）→ `<project>/.aicli/permissions.yaml`（项目共享）→ `<project>/.aicli/permissions.local.yaml`（本地，建议加入 `.gitignore`；沿用现有 `permissions.yml` 兼容后缀）；`deny/ask/allow` 三列表与 `additionalDirectories` 跨文件累积，deny 单调不可撤销（`deny_tools` 保持单调并集）。
  2. `disable_bypass`（或 `permissions.disableBypass`）实现为进程级策略：engine 把进入 bypass 的请求按 default 处理；CLI 入口中和 `--yolo`/`--dangerously-skip-permissions`/`--permission-mode bypass_permissions`；`/yolo`、Web/ACP 模式切换一并拒绝；spawn 继承时同规则（不允许子代理"借父级 bypass"）。
  3. 与 profile ToolPolicy 的关系：profile 仍为 base，三层权限文件作为 overlay 叠加（保持"只增不减 deny"的不变量）。
- **验证**：分层合并单测（用户 deny 被项目 allow 试图撤销 → 仍 deny；同层 deny>ask>allow）；disableBypass 在 engine/CLI/HTTP/ACP 四处的拒绝测试；文档注明默认关闭（兼容）。

### 4.10 【P1】参数匹配 + 工具名通配符

- **CommandCode 语义**：`Shell(run_in_background:true)` 按顶层参数匹配（仅 deny/ask；一次一个参数；模型未发送的参数不匹配）；工具名通配 `mcp__*`/`edit_*` 在 deny/ask 生效，allow 侧仅允许命名 server 的 `mcp__github__get_*` 这类"明确授权"。
- **aicli 缺口**：均未实现（`policy/rules.go` 精确匹配）。
- **设计映射**：
  1. specifier 增加参数形态：`Tool(param:value)`（支持 `*` 后缀），只允许出现在 deny/ask 规则；`command/file_path/path/url` 等工具自有字段继续用命令/路径模式，禁止 `param:` 形式（对应 CommandCode 的"工具已匹配的字段不可参数匹配"）。
  2. 工具名 glob：deny/ask 支持 `*`；allow 侧规则校验器拒绝裸 `*`/`mcp__*`（解析期报错或降级为不匹配并告警）。
  3. 与现有 `DenyTools/AllowTools` 兼容：旧 `deny_tools` 精确名保持；新通配只经 `Rules`。
- **验证**：`Shell(run_in_background:true)` 在 background 调用上命中、普通调用不命中；`mcp__*` deny 拦截全部 MCP；`allow:["*"]` 被解析器拒绝并给出可操作错误。

### 4.11 【P1】accept-edits 安全文件命令快车道（递归删除例外）

- **CommandCode 语义**：accept-edits 只对"安全文件命令"免问（`mkdir/touch/cp/mv/非递归 rm/rmdir/sed`），不自动接受任意 shell、网络、MCP、递归删除；`rm -r/-rf/--recursive`、`find -delete` 维持 default 行为。
- **aicli 现状**：accept-edits 对非只读 shell 一律 ask（`policy/modes.go:52-54`），没有快车道（更安全但更吵；只读命令仍由快车道放行）。
- **设计映射**：
  1. 在只读表旁新增"安全文件命令"分类（同一解析层）：`mkdir/touch/cp/mv/rmdir` 与 `sed -i`（可选，需谨慎）、**非递归** `rm`；目标必须全部在工作区/已授权 roots 内，且不命中 4.4 敏感清单。
  2. `modeDecision`：accept_edits 下，安全文件命令 → allow；递归删除/`find -delete`/目标越界/敏感路径 → 保持 ask。
  3. 与 4.3/4.4/4.7 的先后关系：breaker > 敏感写 > 安全快车道。
- **验证**：`mkdir dist` 免问；`rm -rf dist` 在 accept-edits 仍问；`rm file.txt` 免问但不适用于 `~/.ssh/id_rsa`；矩阵测试固化。

### 4.12 【P2】模式入口、别名与常驻显示

- 补 `--accept-edits`/`--plan` 简写（对齐 `--yolo`）；接收 CommandCode/Claude Code 别名（`auto-accept`、`bypass`、`acceptEdits`、`dontAsk`、`manual`、`standard`），映射进现有 4+1 模式。
- CLI 增加常驻模式标识（banner/状态行），ACM/Web 已有控件；`/mode` 统一入口（含 `/mode:<name>` 简写）。
- **安全复核**：CommandCode 刻意不提供 slash→yolo（slash 可能被 agent 调用）；aicli 存在 `/yolo`。当前证据显示 slash 由用户输入解析，但建议核实模型工具面是否可达（如 skill/`aicli_exec` 路径），若可达则隐藏/要求二次确认并在文档中明确边界。

### 4.13 【P2】按需命令解释（模型摘要）

- 现状已有规则模板解释（`chat_approval_explain.go:82-146`，覆盖 shell/路径/MCP 前缀）；建议在审批提示增加"解释"动作：调用一次后台模型对完整命令/补丁做摘要（显示成本/风险点），并用设置项控制 on-demand（默认按需）与预生成两种模式；解释结果不进入工具调用决策链（纯 UI）。

### 4.14 【P2】文档信息架构

- 新增 `docs/aicli/permissions.md`：一分钟快速版（模式表 + 最常用 5 条规则）→ 规则语法参考（specifier/通配/锚点）→ 决策阶梯与 decision table → 常用 recipes（只读 git、推送前确认、保护 lockfile、保护 `.git/`、CI 用 dont-ask）→ 与 `docs/product/project-permissions.md`（项目文件 schema）互链 → 已知限制（不严格的 read allowlist、shell 路径参数不做目录门、opaque 命令不自动 allow）。
- 在 `docs/analysis/README.md` 或 `docs/aicli/` 索引中登记本文档。

---

## 5. 已有且不应回退的能力

| # | 能力 | aicli 现状（证据） | 与 CommandCode 的关系 |
|---|------|--------------------|------------------------|
| 1 | **应用层 + OS 级沙箱** | `SandboxConfig` 含路径/命令/主机白黑名单、`BlockNetwork`、`EnvWhitelist`、超时；profile `off/workspace/read-only/strict`；`OSSandbox=off/auto/require` 支持 OS 隔离并 fail-closed | 文档未描述 OS 级隔离与命令/主机白名单；这是 aicli 更强的一层，不要为对齐而弱化 |
| 2 | **capability scope 与只读子代理硬边界** | 能力域独立于 permission_mode；`read_only` 子代理移除写型工具与 `background_task` 并在执行时硬拒，approval/bypass 均不能放宽（`policy/capability_scope.go:23-31,43`；`policy/tool_policy.go:120-147`） | CommandCode 子代理在"主循环会 ask"处 **auto-allow**；aicli 是硬边界 + 父级裁决（更严格） |
| 3 | **审批补丁参数（PatchedArgs）与硬约束重验** | hook/callback/审批三处补丁都必须重过静态策略+硬 deny 规则（`policy/engine.go:361-371,450-483,725-744`）；Web 已支持改参批准 | 文档未描述"改参批准"；保留 |
| 4 | **durable grants 与可管理面** | `<project>/.aicli/grants.json` 原子写、去重、可撤销；Web harness `GET/POST /harness/grants`；`/approval-reuse` 清本地复用 | CommandCode 把记忆写进 settings 规则（同样 durable）；aicli 的独立 grants 文件 + 撤销/查看面是补充优势 |
| 5 | **MCP trust level 三档 + 默认阻断** | `local/trusted_remote/untrusted_remote`；默认 `BlockUntrustedMCP`/`BlockRemoteWrites`，untrusted/remote 写型工具直接拒；规范名 `mcp__server__tool` | 文档只描述 MCP 命名与规则 key，未描述信任分级；保留并保持默认安全 |
| 6 | **多宿主同构与审批恢复** | CLI/TUI、Web、ACP、console 共用同一 engine/审批协议；进程重启可恢复 pending 审批（`chat_restored_pending.go`）；运行终态时迟到审批只记录不重放（`actor.go:1077-1106`） | 文档偏 TUI；aicli 的跨宿主一致性更强 |
| 7 | **denial guidance + 只读熔断** | 只读拒绝渲染 `[TOOL_DENIED:code] + boundary + rule + fix`；连续 3 次升级、5 次硬停（`agent/denial_guidance.go:15-43,96-109`） | 文档原则（"策略拒绝给模型可执行指导"）一致，且 aicli 有熔断上限 |
| 8 | **runtime-owned essentials 豁免窄 allowlist** | plan/ask/collab/todos 等控制面工具不被窄 `--allow-tool` brick，但仍受显式 deny/能力域约束（`policy/tool_policy.go:126-132,152-190`） | 文档用"三个无条件豁免工具"实现类似意图；两种做法都保留 |
| 9 | **plan 写白名单精确语义** | 绝对路径相等或分隔符边界目录前缀；base-name 不匹配；apply_patch 解析全部补丁头、任一目标越界即拒（`policy/plan_paths.go:12-111`） | CommandCode 的豁免是"plans 目录 + .md 扩展名"；aicli 的按路径精确校验更细 |
| 10 | **审批超时/等待一致性** | 默认 30 分钟超时；按 requestID 注册 waiter；超时/ctx 取消都会收敛状态并发事件（`chat/actor.go:4450-4490`） | 文档未描述超时语义；保留，并修复 §7 的"单投影槽"问题 |

---

## 6. 实施建议（里程碑与验证）

> 总原则：**先安全、后语法；默认行为不变、显式开关先行**；所有行为改动必须落 decision-table 风格测试。建议新增 `backend/internal/policy/decision_table_test.go`（5 模式 × 操作矩阵），作为所有权限改动的统一验收闸门。

| 里程碑 | 内容 | 依赖 | 验收 |
|--------|------|------|------|
| **M1 安全护栏（P0）** | 4.3 根/主目录断路器（+共享 shell 解析层）、4.4 敏感写保护、4.7 只读机密参数过滤、4.2 dont-ask 模式 | 无（可与规则重构解耦） | 防欺骗矩阵、敏感路径分类表驱动、5 模式决策矩阵、`aicli exec --permission-mode dont-ask` 冒烟 |
| **M2 规则引擎（P0/P1）** | 4.1 specifier（命令/路径/域名/MCP）、4.6 复合命令逐段匹配与不对称、4.10 参数匹配+工具名通配、4.11 accept-edits 安全命令快车道 | M1 的解析层 | specifier 单测矩阵、旧 permissions.yaml 全量回归、`git status && rm -rf x` 反例 |
| **M3 边界与配置（P0/P1）** | 4.5 外部目录门（/add-dir、additionalDirectories 生效、temp/skill/plan 例外）、4.9 分层配置累积 + disableBypass、CLI chat 的 sandbox/policy 接线 | M2（豁免规则用到 specifier） | 外部路径矩阵、分层合并单测、disableBypass 四层拒绝、ACP e2e |
| **M4 体验与文档（P1/P2）** | 4.8 审批选项统一（反馈+remember 作用域）、4.12 模式入口/banner、4.13 按需解释、4.14 文档 IA | M1–M3 | 三宿主一致性与新会话 grant 生效 e2e、`docs/aicli/permissions.md` 评审 |

分阶段风险控制：

1. **兼容优先**：所有新语法（specifier/通配/参数）为增量；旧的 `tools: [shell]`、`deny_tools`、CLI `--deny-tool/--allow-tool` 语义不变。
2. **默认值克制**：外部目录门与 disableBypass 第一版显式开启（settings/env），观察一个版本后再评估默认打开；敏感写保护与机密读过滤可直接默认（与 CommandCode 一致且安全方向明确）。
3. **单一解析来源**：shell 分段/风险判定必须收敛为一份实现（engine、bash 工具、审批 UI、CLI "可否记住"四处共用），防止三份分类器继续漂移。
4. **可观测**：每个新阶段沿用 `Stage*` 常量与 reason 命名（`StageExternalDir`、`StageSensitiveWrite`、`StageShellBreaker`），事件/审计可查；对模型输出 guidance 文案统一从 `agent/denial_guidance.go` 渲染。

---

## 7. 已知限制与风险

### 7.1 与 CommandCode 一致（可接受的边界）

- **没有严格 read allowlist**：读默认自由，只能靠 ask/deny 规则限制（CommandCode 亦如此）。
- **shell 命令内的路径参数不做外部目录门**：命令参数不是可靠的路径边界，依赖 shell 规则/模式（CommandCode 文档明确同一取舍）。
- **opaque 命令永不自动 allow**：解析失败时 deny/ask 可按 raw 串匹配，但不自动放行（需在 4.1/4.6 实现中固化）。
- **参数规则只看见模型实际发送的参数**：未发送的参数不匹配。
- **多文件通配读的展开**：工具内部展开通配（`view`/`grep` 的 glob 参数），specifier 的精确文件规则需同时匹配调用参数与展开目标；建议先按"调用参数 + 目录根"匹配，并在文档中说明（对应 CommandCode 的 known limit）。

### 7.2 aicli 特有风险（实施时应一并处理）

| 风险 | 说明 | 证据 |
|------|------|------|
| CLI chat 未接线 sandbox | `p.Sandbox == nil` 时策略直接跳过路径/URL/命令校验；CLI 侧无生产注入 | `policy/tool_policy.go:363-388`；`cmd/aicli/commands` 仅测试出现 `policy.Sandbox=` |
| symlink 逃逸未兜底 | agent 工具链用 `filepath.Rel` 判包含、不 `EvalSymlinks`；强实现只在 Web `fsscope` | `executor/sandbox.go:367-382`；`fsscope/scope.go:408-439` |
| grants 双轨与 scope 脱节 | 引擎只写 tool 级 `Scope:"session"` grant，`FileGrantStore` 只在 harness 装配；`Find` 不按 scope 过滤 | `policy/engine.go:717-720`；`harness_handlers.go:171-247`；`policy/grants.go:54-74` |
| 审批投影单槽 | waiter map 可多，但 `RuntimeState.PendingApproval` 单槽，后到覆盖投影 | `chat/actor.go:4434-4439`；`chat/runtime_state.go:109` |
| Web 审批能力弱 | 仅布尔 allow/deny，无 remember/反馈/解释 | `pending-interaction-bar.tsx:190-211`；`sessions.ts:442-477` |
| hooks 未覆盖 exec/headless | 仅在 CLI chat 与 Web/API 装配；CI 场景无法用 hook 兜底 | `chat_actor_host.go:2112-2118`；`api/runtimeapi/handler.go:5271` |
| `dont_ask` 静默降级 | agentdef 把 `dont_ask` 当 default，与"fail-closed"预期相反（4.2 需一并修正） | `agentdef/build.go:119-121` |
| `/yolo` 入口待复核 | CommandCode 刻意不做 slash→yolo；aicli 有 `/yolo`，需确认 slash 是否可能被模型/自动化触达 | `chat_command_result.go:1059-1078` |

### 7.3 明确不做（防止过度对齐）

- 不引入任何"绕过 deny 规则/断路器"的新通道；bypass 的硬约束集合只增不减。
- 不改变"策略拒绝继续 turn、人类否决也不终止通用 turn（只回 `approval_denied` 工具错误）"的核心语义——与 CommandCode "只有人类说不才结束回合"的原则一致（实现细节上 aicli 让模型继续自适应，保留现状）。
- 不为了对齐而移除只读 shell 的保守拒绝（复合/动态语法）；只在 4.6 中把"全只读复合"作为**可选**放行能力。

---

## 8. 参考与证据来源

- 外部文档：<https://commandcode.ai/docs/permissions>（2026-09-26 抓取全文；本地留存 `.tmp/commandcode-permissions.html` / `.txt`，属于临时文件，可清理）。
- 相关仓库文档：`docs/product/project-permissions.md`（项目权限文件 schema）、`docs/analysis/commandcode-plan-mode-design-borrowing-20260925.md`（plan mode 借鉴，含 `plan_review`/plans 存储/评审闭环）、`docs/analysis/commandcode-mcp-design-borrowing-20260925.md`（MCP 借鉴）、`docs/analysis/commandcode-interactive-mode-design-borrowing-20260925.md`（交互模式借鉴）。
- 调查方式：3 个只读子代理分别盘点「策略核心+规则/配置」「执行安全面（shell/路径/沙箱）」「宿主交互面（审批/子代理/MCP/hooks）」，并对关键文件做第一手复核（`policy/engine.go`、`policy/modes.go`、`policy/grants.go`、`policy/file_grants.go`、`policy/tool_policy.go`、`executor/sandbox.go`、`chat/actor.go`、`cmd/aicli/commands/chat_runtime_events.go` 等）；全部结论附 `文件:行号`。
- 后续落地时建议把 §4 的每一项转成对应里程碑的 issue/测试清单，并以 §6 的 `decision_table_test.go` 作为统一验收。

---

## 9. 落地状态（2026-09-26，M1 已实施）

按 §6 里程碑实施 **M1 安全护栏**（P0 全部 4 项），默认开启、可显式回退：

| 项 | 状态 | 代码落点 | 验证 |
|----|------|----------|------|
| 4.2 `dont-ask` 模式 | ✅ 已实施 | `policy/modes.go`（`ModeDontAsk`、`modeDecision` 把 ask→deny）、`policy/engine.go` resolveAsk fail-closed（含 `enter_plan_mode` 门与 ask 规则）、CLI/HTTP/ACP/agentdef/`spawn_agent` schema 入口 | `policy/engine_safety_test.go`、`api/runtimeapi/permission_mode_handlers_test.go`、`toolbroker` 测试 |
| 4.3 根/主目录删除断路器 | ✅ 已实施 | 新包 `internal/shellrisk`（分段/env 前缀/包装器/引号/命令替换/`$HOME` 变体/PowerShell/cmd 形式）；`policy/engine.go` 阶段 5b（bypass 不可解析的 `HardAsk`） | `shellrisk_test.go` 防欺骗矩阵、`engine_safety_test.go`（bypass/plan/dont-ask/callback/批量） |
| 4.4 敏感写入保护 | ✅ 已实施（内置清单；配置扩展留待 M3） | `policy/sensitive_paths.go`（secret/persistence/vcs/control_plane 四类，含 `.aicli` 控制面）；`policy/engine.go` 阶段 5c（plan/bypass 跳过、dont-ask deny、其余 ask） | `sensitive_paths_test.go`、`engine_safety_test.go` |
| 4.7 只读 shell 机密参数过滤 | ✅ 已实施 | `policy/grants.go`（`sensitive_argument`，`cat/type/head/tail/get-content/gc` 命中机密路径落出快车道）、`policy/tool_policy.go` 文案、`agent/denial_guidance.go`（`ERR_READONLY_SHELL_SECRET`） | `policy` 引擎测试（default ask / plan deny / 普通文件仍免问） |

实现说明与兼容性：

- **默认开启**：`Engine.DisableShellBreaker` / `Engine.DisableSensitiveWriteGate` 提供回退开关；spawn 子代理继承父模式，`dont_ask`/`plan` 父会话对子代理是硬上限（`toolbroker/spawn_agent_permission.go`）。
- **豁免语义**：命中 `HardAsk` 的断路器审批不可 remember（危险工具本就不入 grants）；工具级 allow 规则/已记忆授权仍可豁免敏感写——内容级豁免依赖 §4.1 的 specifier 语法（M2）。
- **已知回退差异**：`agentdef` 的 `dont_ask` 不再静默降级为 `default`，而是映射为真正的 fail-closed 模式；旧配置若依赖该降级需改用 `default`。
- **验证记录**：`policy`、`shellrisk`、`toolbroker`、`agent`、`agentdef`、`api/runtimeapi`、`chat` 全量通过；`cmd/aicli/commands` 中与本次相关的用例（PermissionMode/ModeConfigOption/SessionMode/Status/ImageToken）通过。验证时另发现两处**既有问题**：`chat_image_tokens_test.go` 的单复数命名漂移（本次顺手修复以解除包编译阻塞）、`TestChatDebugDisplayShowsStorageSection` 调试面板断言过期（与本次改动无关，未修复）。
- **未实施（后续里程碑）**：4.5/4.9（外部目录门、配置分层与 `disableBypass`，M3）、4.8/4.12/4.13/4.14（审批选项统一/模式 UX/按需解释/文档 IA，M4）；M2 见 §10。

---

## 10. 落地状态（2026-09-26，M2 已实施）

按 §6 里程碑实施 **M2 规则引擎**（4.1/4.6/4.10/4.11），保持旧 `permissions.yaml` 语义不变：

| 项 | 状态 | 代码落点 | 验证 |
|----|------|----------|------|
| 4.1 规则 specifier | ✅ 已实施 | 新 `policy/specifier.go`（`Shell/Read/Edit/WebFetch` 组、命令/路径/域名/参数/工具 glob、友好名归一、`*` 不跨段、`**` 跨段、四类路径锚点）；`policy/rules.go` 的 `Rule.Specifiers` + `MatchesRequest`；`policy/permissions_file.go` 解析/校验 | `specifier_test.go` 全矩阵、`permissions_file` 回归（纯工具名保持精确；`deny_tools/allow_tools` 拒绝 specifier 语法） |
| 4.6 复合命令逐段匹配 | ✅ 已实施 | `shellrisk.Segments/ResolveSegment`（env 前缀、包装器穿透、解释器/替换不可静态解析）；`AssessShellReadOnlyCommand` 改为逐段判定；规则匹配不对称：**deny/ask 任一段命中、allow 必须每段命中且可解析** | `pipeline_test.go`（`cat a \| head -5` 免问、`git status; rm -rf build` 拒绝、未闭合引号→`unparsable_command`）、`specifier_test.go` 反例 `git status && rm -rf x` |
| 4.10 参数匹配 + 工具名通配 | ✅ 已实施 | `Tool(param:value)`（仅 deny/ask；未发送参数不匹配；own 字段禁止 param 形式）、`mcp__*`/`edit_*`（allow 侧拒绝裸 `*`/`mcp__*`，加载期报错） | `specifier_test.go`（param/glob/非法 allow 形式） |
| 4.11 accept-edits 安全文件快车道 | ✅ 已实施 | 新 `policy/safe_file_commands.go`（mkdir/touch/cp/mv/rmdir/非递归 rm + PowerShell 形式；目标必须在工作区内且非敏感；递归删除、`find -delete`、glob/越界/敏感目标一律回落）；`engine.go` 第 7 阶段接线 + `DisableSafeFileFastPath` 回退 | `safe_file_commands_test.go`、`decision_table_test.go`（`mkdir dist` 免问、`rm -rf dist` 仍问） |
| 统一验收闸门 | ✅ 已实施 | 新 `policy/decision_table_test.go`（5 模式 × 读/写/只读 shell/敏感读/breaker/安全文件命令 + headless/hard-ask 两行） | 全量通过 |

实现说明与兼容性：

- **旧语义不变**：裸工具名仍是精确匹配；`deny_tools`/`allow_tools` 仍是硬闸精确名；CLI `--deny-tool/--allow-tool` 不解释 specifier。
- **规则细节**：命令模式 `git status`（精确、空格敏感）、`git:*`（前缀）、`*`/`?` glob；路径 `//`（文件系统）、`~/`（家目录）、`/`（工作区根，取 `toolctx.WorkspaceRoot`，回退 `PathAnchorRoot`）、相对（任意深度，单段模式匹配 basename）；域名从 URL 参数取 host；`*.example.com` 不匹配 apex。
- **决策明细**：rules 阶段新增 `Decision.RuleDetail`（命中的 segment/path/host），便于审计与后续审批文案使用。
- **已知限制（文档 §7 对齐）**：specifier 的读规则按"调用参数"匹配，不展开工具内部 glob；shell 命令内的绝对路径参数不做目录门；opaque 命令永不自动 allow。
- **未实施（后续里程碑）**：4.5/4.9（M3）、4.8/4.12/4.13/4.14（M4）；CLI 审批"能否记住"分类器与 policy 的第二套实现仍待收敛（§4.6 长期项）；CLI `--deny-rule/--ask-rule/--allow-rule` 入口未加（可选）。

---

## 11. 落地状态（2026-09-26，M3 第一切片：配置分层 + disable_bypass）

实施 §4.9 的配置分层与进程级 `disable_bypass`，CLI chat 已接线：

| 项 | 状态 | 代码落点 | 验证 |
|----|------|----------|------|
| 三层权限文件累积 | ✅ | 新 `policy/permissions_layers.go`：`ResolvePermissionsLayerPaths` / `LoadLayeredPermissions` / `MergePermissionsLayers`，`~/.aicli/permissions.yaml` → `<project>/.aicli/permissions.yaml` → `<project>/.aicli/permissions.local.yaml`（`.yml` 兼容）；rules 按层拼接并加 `user/`、`project/`、`local/` 名称前缀，`deny_tools`/`allow_tools` 并集（deny 单调），`disable_bypass` OR | `permissions_layers_test.go`（三层累积、无项目层、空层、overlay 传播；测试用 `HOME`/`USERPROFILE` 隔离） |
| `disable_bypass` 引擎语义 | ✅ | `Engine.DisableBypass` + `effectiveMode()`：bypass 请求按 `default` 求值（能力按降级后模式重新解析），`resolveAsk` 统一用有效模式判定 dont_ask/bypass；HardAsk 语义不变 | `decision_table_test.go:TestEngineDisableBypassDowngradesToDefault`（ask 而非静默 allow；headless deny） |
| CLI 接线与切换入口 | ✅ | `chat_permissions_overlay.go` 改用分层加载并把 `DisableBypass` 写入引擎；`setChatPermissionMode` 拒绝切入 bypass 并提示 | `cmd/aicli/commands:TestApplyChatPermissionsOverlayWiresDisableBypass` |
| 文档 | ✅ | `docs/product/project-permissions.md` 新增"配置分层（§4.9）"章节 | — |

本切片明确**未做**（M3 剩余）：

- `--yolo` / `--permission-mode bypass_permissions` 启动参数的 CLI 横幅与解析期拒绝（当前由引擎侧降级兜底，会话仍会显示 bypass 文案）；
- ACP/Web 模式切换入口与 spawn 继承的 disable_bypass 处理；`harness` 权限读取接口仍只返回单层项目文件；
- 4.5 外部目录门的**策略侧**已落地（见 §12）；CLI `/add-dir`、会话状态持久化与 ACP `additionalDirectories` 接线仍待实施。

---

## 12. 落地状态（2026-09-26，M3 第二切片：外部目录门 policy/toolctx 侧，§4.5）

| 项 | 状态 | 代码落点 | 验证 |
|----|------|----------|------|
| 会话根集合 | ✅ | `toolctx.WithAllowedRoots/AllowedRoots`（工作区根之外的已准入目录，含去重与返回值拷贝隔离） | `toolctx/allowed_roots_test.go` |
| 外部目录门阶段 | ✅ | `policy/external_dirs.go` + `Engine` 4b 阶段（rules/grants 之后、只读快车道之前）：按 `policyArgKeysForTool` 抽取路径参数（含 shell `cwd/workdir/working_dir`），相对路径按工作区根锚定、`~` 展开、符号链接与大小写（Windows）归一；目录外 → `external_dir:admit`，`RuleDetail` 记录目录集合 | `policy/external_dirs_test.go`（10 例） |
| 模式语义 | ✅ | default/accept_edits/plan → ask；bypass → 静默准入（`admit_bypass`，不弹窗）；dont_ask → deny；批准后经 `Engine.ApproveExternalDir` 回写会话根集合；`Decision.ExternalDirs` 供审计 | 同上（批准回写、静默准入、fail-closed 各有断言） |
| 豁免 | ✅ | OS 临时目录全模式静默（读写都免门，写仍走普通模式规则；`Engine.ExternalDirTempRoots` 可覆盖/追加）；plan 模式下 plan 文件写豁免；`Engine.ExternalReadOnlyRoots`（skill/plugin 目录）**只读**豁免，写仍需准入；shell 命令串内的绝对路径不做门（与 §7 契约一致） | 同上（临时目录、只读根读/写、命令内路径各一例） |
| 随 run 下发 | ✅ | `internal/agent`：`toolCallContext` / `approvedToolCallContext` 绑定 `toolctx.WithAllowedRoots`，来源为 agent options（`allowed_roots` → `additional_directories` → ACP 风格 `additionalDirectories`，支持 []string / []interface{} / 逗号分号空格分隔字符串） | `agent/allowed_roots_binding_test.go` |

本切片明确**未做**（4.5 剩余）：

- CLI `/add-dir` 与会话状态持久化（把准入目录写回 session runtime state 并由 CLI 侧再下发）、`--add-dir` 启动参数、`sandbox_dirs` 接线；
- ACP `additionalDirectories`（`internal/acp/types.go` 已解析）到 `allowed_roots` 的映射；
- CLI chat 生产路径的 `Sandbox` 接线与「已注册 skill 目录」的实际填充（`ExternalReadOnlyRoots` 已留好挂点）；
- 审批文案（`chat_approval_explain.go`）对 `external_dir:admit` 的中文解释与 deny guidance 条目。
