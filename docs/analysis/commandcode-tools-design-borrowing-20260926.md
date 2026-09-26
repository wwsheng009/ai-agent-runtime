# CommandCode Tools 文档设计借鉴分析

- **日期**：2026-09-26
- **来源**：<https://commandcode.ai/docs/reference/tools>（Command Code "Tools" 参考文档全文：通用工具管线、Filesystem / Search / Shell & processes / Work state / Scheduling / Web / Sub-agents / Interaction & session / MCP / 权限模式工具可见性）
- **对照对象**：本仓库 aicli 的工具实现（本次以只读方式完成代码盘点，结论尽量附 `文件:行号`）：
  - 工具实现：`backend/internal/toolkit/tools/*.go`（view/write/edit/multiedit/apply_patch/append_write/glob/grep/ls/shell/bash/execute_shell_command/aicli_exec/todos/fetch/web_search/download/sourcegraph/artifact_read 等）
  - 通用管线：`backend/internal/toolargs/`、`backend/internal/toolexec/`、`backend/internal/toolresult/`、`backend/internal/toolkit/{listable,search_tool}.go`、`backend/internal/output/`（预算与 artifact 折叠）
  - 代理/控制面：`backend/internal/toolbroker/`（ask_user_question / background_task / task_output / spawn_agent / spawn_team / plan_* 等）、`backend/internal/skill/`、`backend/internal/planmode/`、`backend/internal/supervision/`、`backend/internal/background/`
  - 权限与安全：`backend/internal/policy/`、`backend/internal/shellrisk/`、`backend/internal/executor/`（只读分类、进程树守卫、sandbox）
- **结论口径**：区分「值得借鉴（缺口）」「已有且更强（不要回退）」「不建议照搬」；每条建议给出代码落点、优先级与验证方式。
- **调查方式**：Command Code 文档全文落盘后逐节提炼；本仓库用只读子代理分三路盘点「文件系统与写入安全」「搜索/Shell/后台」「Web/计划/子代理/可见性」（其中两路子任务因运行时错误失败，一路重跑成功、一路由主会话补验），关键结论由主会话二次核验（view/write/edit/todos/fetch/web_search/broker/toolargs/preflight 等均直接读源码确认）。

> 系列文档：`commandcode-permissions-design-borrowing-20260926.md`（权限）、`commandcode-plan-mode-design-borrowing-20260925.md`（计划模式）、`commandcode-mcp-design-borrowing-20260925.md`（MCP）、`commandcode-agents-design-borrowing-20260926.md`（子代理）、`commandcode-interactive-mode-design-borrowing-20260925.md`（交互模式）。本文只补充「工具本身」的对比，权限/计划/子代理的体系性建议以系列文档为准，不重复展开。

---

## 1. TL;DR：可借鉴清单

| # | 主题 | CommandCode 做法 | aicli 现状（证据） | 建议 | 优先级 |
|---|------|------------------|--------------------|------|--------|
| 1 | **写前读账本 + 陈旧写默认防护** | 覆盖前必须本会话完整读过；磁盘变更后拒写 | 覆盖前自读旧内容算 diff（`toolkit/tools/write.go:156-160`）；陈旧防护是 **opt-in** 的 `expected_sha256`（`write.go:35-38,165-171`、`write_idempotency.go:35-50`）；无会话级读账本（全仓未找到 read-ledger） | 会话级记录 view/grep 读过的绝对路径+内容哈希；write/edit 覆盖前比对，未读/已变更按策略警告或拒写（可配置），复用 `ErrWritePrecondition` | **P0** |
| 2 | **原子落盘 + 编码保留** | temp 兄弟文件 + rename、保留 mode；UTF-8/UTF-16LE BOM 往返 | write/edit/multiedit 全部 `os.WriteFile` 直写且固定 0644（`write.go:184`、`edit.go:253`、`multiedit.go:300`）；仅 apply_patch 先 staging 后 commit（`apply_patch.go:844-913`）；tools 内无 BOM/utf16 处理 | 抽公共 `writeAtomic`（同目录 temp + fsync + rename，保留原 Mode、symlink 语义）；读侧检测 BOM/UTF-16 并转码展示、写侧回写原编码 | **P0** |
| 3 | **工具 schema/描述 token 成本纪律** | 合并近义工具（read_multiple_files→read_file 省 ~540 tokens/请求）；工具描述刻意短小 | 同一 shell 能力注册 3 份 schema：shell/bash/execute_shell_command（`tools/manager.go:394-396`），含 grep 实测 19 个内置工具 schema ≈ **60.4 KB（≈1.5 万 tokens，4B/token 粗估）**，其中 grep 单条 20.4 KB、execute_shell_command 10.8 KB（描述 4.5 KB） | 模型面只保留 `shell` 一份 + 旧名重写（重写映射已有 `agent/tool_vocabulary.go:90-117` 的 legacyToolSuggestions，但目前只做诊断）；把 execute_shell_command 的长说明下沉到文档/技能 | **P0** |
| 4 | **读工具格式感知（图片/PDF/Office/EPUB）** | 图片回传 image block；PDF/Word/PPT/Excel/ODT/RTF/EPUB 转 Markdown；二进制仅提示 MIME+大小 | view 命中二进制直接报错（`view.go:263-269`）；PDF/Office 无转换器（`filebrowse/stat.go` 只有文本白名单与 mime 识别）；图片链路只存在于消息附件（`imageattach/`） | view 命中图片扩展名时返回 image（复用 imageprep/imageattach）；PDF/Office 留可选转换器钩子，先做「PDF 文本层 + 图片直通」两步 | **P0→P1** |
| 5 | **后台进程的模型可见管理面** | shell_command `run_in_background`；shell_output `wait=none/output/exit`；monitor_command 定时唤醒；kill_shell 按 task/pid/port | 后台任务独立成 `background_task`/`task_output`（job_id+日志+心跳+启动探针，见 §4.4），但：无 `run_in_background` 参数、`task_output` **无 wait 阻塞语义**、无 kill/monitor 工具（`background.Manager.CancelJob` 存在但仅 API：`manager.go:409-448`、`api/runtimeapi/background_handlers.go:101`） | ① task_output 加 wait 长轮询；② 新增 kill 工具复用 CancelJob + 端口→pid；③ job 终态唤醒 turn（复用 supervision wake） | **P1** |
| 6 | **搜索超时/分页/部分结果保全** | glob/grep 20s 预算、超时保留已发现结果；glob 支持 offset、按 mtime 排序；grep head_limit/offset | glob 无自身 timeout、取消即失败（`glob.go:354-404`）；无 offset、无 mtime 排序；grep 无 head_limit/offset，`maxMatches=100` 固定（`grep.go:648,3346-3356`） | 给 glob/grep 加预算 + 超时保全（标 truncated + 已聚合行返回）；补 offset/head_limit；glob 输出按 ModTime 排序 | **P1** |
| 7 | **shell 输出首尾双窗口** | 前台输出 head+tail 中段省略，完整日志给路径 | 现为 head-only 折叠到 32 KiB，先归档完整 capture 再折叠（`tool_output_budget.go:45-54,143-192`），metadata 有 artifact 指针 | 折叠改为「首 N + 尾 M，中段省略并指向 artifact_read」，错误/测试结论通常落在尾部 | **P1** |
| 8 | **通用输入修复的「模型可见 + 可观测」** | 修复后前置 `<repair_note>`，每次修复发遥测（规则名+键，不含值） | 已有静默修复：别名提升（`toolargs/toolkit_args.go`）、`_raw` 解包、结构补全、类型强转、占位路径清理、缺参合并报错（`toolexec/preflight.go:97-127,1080-1163`）；但修复不告知模型、无规则级遥测 | 修复发生时在结果前挂 `repair_note`（或 metadata 常量），并记 `tool.input.repaired{rule,key}` 事件 | **P1** |
| 9 | **编辑歧义与边界** | 多匹配拒写并报数量；`replacement_count` 前 N 次；>10 MB/二进制拒编辑；成功回带行号片段 | 模糊级联已具备（`edit.go:426-607`）；但精确多匹配非 replace_all 静默替换第一处（`edit.go:242-250`），无「前 N 次」，无文件大小/二进制前置拒绝，成功只回 unified diff | 非 replace_all 且精确命中 >1 时返回 ambiguous+数量+行号；补 replacement_count；补大小/二进制前置检查 | **P1** |
| 10 | **Windows 设备/流路径黑名单** | 阻止 `/dev/zero`、`/dev/stdin`、`/proc/<pid>/fd/*` 等 | 未见 NUL/CON/COM1/`\\?\` 拦截（`toolexec/preflight.go` 仅路径候选过滤） | 在 `sandboxPolicy.checkPath/resolve` 统一 deny + 机器码（Windows 设备名 + Unix 设备/流路径） | **P1** |
| 11 | **规划型任务账本（依赖图）** | task_create/update/list/get：稳定 id、owner、metadata、blockedBy/blocks、完成回报 unblock | 无通用任务 CRUD；团队有内部任务账本与 `read_task_spec/report_task_outcome/block_current_task`（团队 worker 侧）；todos 只有扁平清单 | 若团队/长任务需要跨轮协调，抽出模型可见的 task_* 账本（复用 team/subagentbatch 存储与 `todos` 展示） | **P2** |
| 12 | **时间驱动调度** | cron_create/list/delete、schedule_wakeup、sleep(until/wake_on_input) | 无 cron/sleep/schedule_wakeup；内部有 supervision wake 调度器（`supervision/wake_scheduler.go`）但仅服务生命周期事件 | 先做 `sleep`（带 wake_on_input、上限策略），cron 需产品确认（kill switch、恢复语义） | **P2** |
| 13 | **web_fetch 安全守卫与缓存** | 拒凭据 URL/私网/环回/link-local/单标签；100k 字符窗口 + startIndex；15min LRU；重定向上报；http 自动升 https | fetch 仅校验 http(s) 前缀（`fetch.go:84-91`），其余交给 sandbox 的 allowed/denied hosts（sandbox 未激活时不校验，`executor/sandbox.go:191-228`）；5 MB 上限 + 32 KiB 预算 + 诚实截断；无缓存/startIndex/重定向上报 | URL 守卫下沉为 fetch 内建（私网/凭据/单标签），加 15min LRU 与 startIndex；https 升级可选 | **P2** |
| 14 | **结构化提问** | 最多 4 问、每问 2–4 选项、header≤20、multiSelect、preview、自由文本；headless 默认选项需披露 | `ask_user_question` 仅 prompt/suggestions[]/required（`toolbroker/broker.go:191-211`） | 扩展 questions[] 结构（向后兼容旧 prompt），宿主按能力降级渲染 | **P2** |
| 15 | **会话级 worktree 工具** | enter_worktree/exit_worktree，remove 失败关闭（uncommitted/超基线需 discard_changes） | 子代理级 worktree 已有（spawn isolation + apply/discard_agent_worktree）；主会话无 enter/exit 工具 | 复用 `internal/isolation/worktree` 增加会话级 enter/exit，exit 采用 fail-closed 语义 | **P2** |
| 16 | **平台化 shell 细节** | PowerShell `-NoProfile -NonInteractive -EncodedCommand`；跨调用持久 cwd；信号退出报 128+N；read-only 分类免审批 | pwsh→powershell→cmd 探测（`executor/shell_detect.go:115-137`）、UTF-8 注入（`bash.go:1489-1498`）；但用 `-NoProfile -Command`（无 -NonInteractive/EncodedCommand）、每次按 workdir 解析无持久 cwd、信号退出未按 128+N（`bash.go:1517-1518`）；只读分类已具备且更强（`policy/grants.go:228-303`） | 追加 `-NonInteractive`（或 EncodedCommand）、`WaitStatus.Signaled → 128+N`；持久 cwd 可选（需评估副作用） | **P2** |

> 「已有且更强/不要回退」清单见 §6；其中 argv 级只读分类、根目录删除熔断、Windows JobObject 进程树守卫、后台任务持久化/恢复/启动探针、统一「先归档→自折叠→声明预算」输出合同、轮询软刹车、路径 did-you-mean/auto-heal、edit 多级模糊自愈、apply_patch staging 等均超出文档描述或与其等价。

---

## 2. CommandCode 文档要点提炼

### 2.1 通用工具管线（每个工具调用都过同一管道）

1. **输入修复**：schema 驱动 — JSON 字符串化的数组/对象解析、裸标量包装、字符串数字/布尔强转、markdown 包裹路径剥离、null 占位字段丢弃、数十个别名改写到规范名（`path→file_path`、`query→pattern`、`oldValue→old_string`、`cmd→command`…）；修复后仍缺必填则给「合并后的纠正」而非堆栈。
2. **执行层注入**：工具不直接触碰 `node:fs`/`child_process`，全部走注入 runtime（可测试、可移植）。
3. **输出截断**：文本结果 25,000 tokens 上限；超限不截半句，而是替换为「用更窄查询重试」的提示（自带预算的工具如 read_file/web 豁免）。
4. **修复说明**：任何修复以 `<repair_note>` 前置，并作为遥测事件（只记规则名与键，不记值）。
5. **工作区边界**：读限定在项目（+`/add-dir`）；常规模式写不出界；yolo 首次越界写会静默把该目录纳入根；只读工具与只读分类 shell 永不询问。

### 2.2 Filesystem

- **read_file**：有界、带行号窗口；支持 `file_path` 或 `paths[]`（多文件/glob，`// File:` 头 + `Read X/Y files` 摘要，因 Gemini 对 anyOf 兼容差而拆两个字段，用 requiredAlternatives 表达二选一）；offset/limit（默认 2000 行、单行 2000 字符、128 KB 上限、负 offset 读尾）；默认排除 + `.gitignore` 开关；聚合 ~100 KB 预算并报告跳过了哪些文件；单文件错误内联；重复读未变更文件回去重存根；路径 typo 容错（macOS 变体 + 编辑距离建议）；设备/流路径黑名单；**每次读入会话读账本，写工具依赖它**；格式感知见 P0 清单。
- **read_directory**：单层列举 + 计数 + 字母序分组 + exclude。
- **write_file**：读后写（部分读不算）；陈旧写检测；原子写（temp 兄弟 + rename、保留 mode、正确处理 symlink 目标）；UTF-8/UTF-16LE BOM 保留；落盘前密钥扫描否决；同文件写串行化；计划模式仅允许写 plans 目录。
- **edit_file**：六策略匹配级联（精确→智能标点→行 trim→空白归一→缩进灵活→块锚点），并告知命中策略；多匹配歧义拒写并报数量；`replace_all` / `replacement_count`（前 N 次）；>10 MB 与二进制前置拒绝；BOM 与 CRLF/LF 字节级保真；结果带编辑区行号片段。

### 2.3 Search

- **glob**：`*,**,?,[]{}`；按修改时间排序；hidden/gitignored 默认搜（`.git` 永远排除）；20s 搜索时限（WSL 60s），超时/中断**保全部分结果**；limit 默认 100 上限 10,000 + offset 分页；缺失目录 did-you-mean；逐匹配边界校验。
- **grep**：捆绑 ripgrep → PATH rg → 纯 runtime 三级回退；output_mode（content/files_with_matches/count）；-A/-B/-C/-n/-i/type/head_limit(默认250,0=无限)/offset/multiline；硬化参数（禁用户配置、禁 ANSI、重排除 VCS 目录、列宽钳制）；非法正则报真错；线程耗尽退单线程重试；超时保全部分结果。

### 2.4 Shell & processes

- **shell_command**：前台/后台；timeout 默认 30s、上限 600s、0=无；`run_in_background` 返回 task id+日志路径；`description` 供 UI；核心是 **argv 级只读分类**（真分词器 + 窄白名单，`git status` 免问、`rm` 询问，引号包裹的危险参数逃不过）；拒绝 `sleep N`（≥2s）引导用 sleep 工具；输出中段截断（head+tail 内联，完整输出落日志并在结果里给路径）；约定非错退出码注释（`grep 1=no matches`、`diff 1=files differ`）；信号杀死诚实报 `128+N`。
- **powershell(Windows)**：pwsh 7 优先/5.1 回退；`-NoProfile -NonInteractive -EncodedCommand`；强制 UTF-8；理解 `$?`/`$LASTEXITCODE`；**跨调用持久 cwd**；独立 fail-closed 只读分类。
- **shell_output**：合并 bash_output/task_output/monitor_events 三个旧名（旧名保留默认语义）；`wait=none/output/exit`、`timeout_ms`、`from_offset`、`max_chars`（1–160,000）；tracked 任务带 header（id/kind/status/exit code/log path）；内联尾部 30,000 字符上限，完整日志在盘；`[still running]`/`[finished]`；输出按不可信数据 fence；未知 id 报错并指向 shell_tasks。
- **monitor_command / shell_tasks**：`checkAfterMs`（默认 45s）到点唤醒一次 + 进程退出再唤醒一次（模型无需轮询）；`maxDurationMs` 自动 SIGTERM；`notify: never|scheduled`。
- **kill_shell**：按 taskId/pid/port；端口→pid（lsof / Windows Get-NetTCPConnection）；pid 反查 tracked wrapper；SIGTERM→轮询→SIGKILL（Windows taskkill /T 再 /F）；先探活再宣称成功；**绝不对任意 pid 打整个进程组**。

### 2.5 Work state / Scheduling

- **todo_write**：整表替换；差分旧新状态；未完成项被丢弃、多于一个 in_progress 时警告；引导加验证项；尊重用户 `/todos` 手工编辑（移除保持移除、完成保持完成，归因诚实）；工具描述刻意短（token 成本）。
- **task_* 账本**：跨重启持久、稳定 id、三状态、owner、metadata、`blockedBy/blocks` 边；update 支持增删依赖；完成时报告解锁了哪些任务；四个工具在计划模式可用（规划即任务）。
- **cron/schedule_wakeup/sleep**：会话级/durable 定时任务（jitter、7 天过期、kill switch、`--resume` 恢复）；自定步调 `/loop`（delay 60–3600s，新调用替换旧唤醒）；`sleep` 支持 until/秒、`wake_on_input`（用户输入 1 秒内唤醒）、每秒进度 tick、时长策略（默认上限 10 分钟并报剩余）。

### 2.6 Web / Sub-agents / Interaction / MCP

- **web_search**：query≥2、numResults 默认 5 上限 10、allowed/blocked domains（互斥、客户端二次强制）；客户端执行，所有模型可用。
- **web_fetch**：http 自动升 https；markdown/text/html；startIndex 分页；timeout 默认 60 上限 120；客户端 URL 守卫（凭据 URL、私网/环回/link-local、单标签主机）；100,000 字符窗口；15 分钟 LRU 缓存；重定向上报。
- **agent / agent_output**：`subagent_type` 枚举每次按注册表重算（会话中新增 agent 立即可用）；`run_in_background` 返回 agent_id；agent_output 支持 wait（默认）/status/kill；子代理沿用同一权限管道但**永不给 spawn 形状的工具**（agent/agent_output/plan/worktree/run_command/ask_user_question/sleep），各自独立工具状态。
- **activate_skill**：系统提示只带技能名+描述，正文按需加载（渐进披露）。
- **ask_user_question**：≤4 问 × 2–4 选项、header≤20、multiSelect、选项 preview、自由文本随时可用；headless 默认选第一项且**必须披露**。
- **plan/review/worktree/run_command/taste/get_diagnostics**：enter/exit_plan_mode 三方审批；plan_review 重开评审面板；enter/exit_worktree 失败关闭；run_command 跑 slash 命令（校验 + 回合结束后派发）；taste 记录偏好；get_diagnostics 仅 IDE 连接时广告，LSP 诊断按文件分组并做注入清洗。
- **MCP**：`mcp__<server>__<tool>`，与内置同一权限/输出管道；后台连接并热注册；计划模式**整体隐藏**（无法验证远程只读）；输出 25k token 预算 + 分页指引。
- **权限模式与可见性**：default/accept-edits/dont-ask/yolo 可见除 exit_plan_mode 外全部；plan 移除写工具、enter_plan_mode、全部 `mcp__*`，保留 plans 目录写与只读 shell、task_*；可见性过滤有运行期 fail-closed 二次检查；deny 规则在任何模式（含 yolo）优先。

---

## 3. aicli 现状盘点（代码证据）

### 3.1 通用管线

| 能力 | 状态 | 证据 | 说明 |
|------|------|------|------|
| 参数别名提升 | 具备 | `toolargs/toolkit_args.go:35-108`；`tools/argument_aliases.go:16-33` | 单一别名表服务「执行器读取的键」与「策略检查的键」；支持顶层与数组对象字段 |
| `_raw` 解包 / 结构补全 | 具备 | `toolargs/normalize.go:15-52` | provider fallback 形状 `{"_raw":"{...}"}` 解包；只补对象/数组缺失定界符，**绝不修复字符串内部**（截断的写入不会被当完整执行） |
| 类型强转 | 具备 | `toolexec/preflight.go:1080-1163` | number→字符串、string→array（合法 JSON 数组或包成单元素）、string→bool 等；避免 preflight 拒绝工具本来能接受的形状 |
| 占位路径清理 | 具备 | `toolexec/preflight.go:1166-1180` | `path=""`/`"null"` 归一为真空，避免误报缺参并污染候选路径 |
| 缺参/类型错合并纠正 | 具备 | `toolexec/preflight.go:97-127` | `missing required argument(s): …` + `NextAction` + Diagnostic（error_code/retryable/next_action） |
| 失败熔断（同参重复失败） | 具备 | `toolexec/preflight.go:129-170` | 同工具同参 digest 的终态失败打开 circuit，重放仍带 path_candidates |
| 路径建议与唯一 auto-heal | **强于文档** | `toolexec/preflight.go:56-64,143-159,207-300,1858-1919` | substring/edit-distance 兄弟文件排序；唯一高置信候选可自动改写（只读路径），歧义则拒并给候选 |
| 修复的模型可见说明 | **缺失** | 无 `repair_note`；仅 observability 计数 | 模型不知道自己发的 `cmd=` 被提升成 `command=` |
| 修复规则级遥测 | **部分** | `observability.RecordToolPreflight`（`preflight.go:109`） | 有 preflight 结果计数，无「哪条规则、哪个键」维度 |
| 输出预算（按工具） | 具备 | `toolkit/tools/tool_output_budget.go:31-54` | view/grep/fetch/artifact_read 32 KiB、glob/ls 16 KiB、shell 32 KiB；工具自持窗口并声明 model_visible_budget |
| 先归档再折叠 | **强于文档** | `tool_output_budget.go:109-140`；`output/gateway.go:136-172` | 完整 capture 先落 artifact，再 head 折叠，提示用 `artifact_read` 翻页；比「超限就要求收窄查询」更少往返 |
| 超限时的模型引导 | 部分 | `fetch.go:202-216`（提示改用 download）；shell 折叠带 artifact 指针 | 通用层没有统一的「改用更窄查询」话术，但每个工具自带 next_action |
| 工具面压缩 | **独有** | `agent/loop.go:4032-4078,4080+`；`toolkit/search_tool.go`；`toolkit/listable.go:31-111` | 按 prompt 预算压缩工具集；shell 别名按 rank 去重；grep 模型面只留 14 个常用参数（其余走 rg_args）；`search_tool` 元工具支持「能力不在当前列表时搜索目录」，`defer_loading`/`core_tool` 元数据控制投影 |
| 退役工具名诊断 | 部分 | `agent/tool_vocabulary.go:90-117,152+` | read_file/edit_file/bash 等旧名会得到「改用 view/edit/shell」的纠错，但**不重写**调用（MCP 可能同名，刻意保守） |
| 工具 schema token 实测 | 证据 | 本次实测（`go run` 构造 19 个内置工具） | 19 个 schema 合计 60.4 KB：grep 20.4 KB、execute_shell_command 10.8 KB（描述 4.5 KB）、bash 4.6 KB、shell 4.5 KB、aicli_exec 4.3 KB、todos 1.8 KB、其余 ≤1.6 KB；合计 ≈1.5 万 tokens（4 B/token 粗估，中文实际更高）。运行时对 shell 别名与 grep 有压缩，实际广告成本低于此值 |

### 3.2 文件系统

| 能力 | 状态 | 证据 | 说明 |
|------|------|------|------|
| 行号 + offset/limit | 具备 | `view.go:91-98,181-189,591-602` | 0-based；默认 400、硬上限 2000、批量默认 200 |
| 负 offset 读尾 | 缺失 | `view.go:181-183` | 负值直接归零 |
| 单行截断 + 诚实标记 | 具备 | `view.go:471-495,537-545` | 2000 rune + `…[line truncated: N more chars]`，计 hidden_bytes |
| 续读提示 | 具备 | `view.go:307-340` | `suggested_next_offset`、eof、is_truncated、efficiency_advisory |
| 批量读 | 部分 | `view.go:73-90,151-173,342-427` | `files[]`（逐项 file_path/offset/limit）+ compact 扫描模式；无 `paths`+glob 入参、无默认排除/.gitignore 开关、无跨项聚合预算；单文件错误内联 + partial_failure |
| 重复读去重 | 缺失（view） | `toolexec/preflight.go:172-203` 仅空结果负缓存 | 未变更文件的重复读仍全量进 context |
| 图片/文档读取 | 缺失 | `view.go:263-269`；`imageattach/` 仅消息附件 | 图片/PDF/Office 一律「二进制不支持」 |
| 设备路径黑名单 | 缺失 | 未找到 | 未拦 NUL/CON/COM1、`\\?\`、`/dev/*`、`/proc/*/fd` |
| 路径 typo 建议 | 具备 | `toolexec/preflight.go`（同上） | 与读工具联动 |
| 读后写 | 部分 | `write.go:156-160` | 覆盖前自读旧内容用于 diff，不要求「本会话读过」 |
| 陈旧写检测 | 部分（opt-in） | `write.go:35-38,102-104,165-171`；`write_idempotency.go:35-50` | `expected_sha256` 不符 → `ErrWritePrecondition`；默认不校验 |
| 原子写 | 缺失 | `write.go:184`、`edit.go:253`、`multiedit.go:300` | 直写；apply_patch 有 staging→commit（`apply_patch.go:844-913`） |
| 权限位/BOM | 部分/缺失 | 同上；`apply_patch.go:865,904-908` 保留原 Mode | write/edit 固定 0644；无 BOM/UTF-16 处理 |
| 内容密钥扫描 | 缺失 | `policy/sensitive_paths.go:45,135` 只有敏感路径门禁 | 无 `BEGIN PRIVATE KEY`/`sk-`/AKIA 等内容级否决 |
| 计划模式写限制 | 具备（可配置） | `policy/engine.go:104-107,455-465,921-943` | 默认只许写 plan.md，白名单可配；含 base-name 混淆防护 |
| 编辑模糊匹配 | **强于文档** | `edit.go:426-607`；`apply_patch.go:32-50,947-1092` | exact→CRLF/LF→trim→空白+智能标点→空行块漂移→缩进重建；apply_patch 另有 4 级 matcher |
| 歧义/前 N 次/大小拒绝 | 部分/缺失 | `edit.go:242-250,496-511` | 模糊命中不唯一会失败；**精确多命中静默替换第一处**；无 replacement_count；无 10 MB/二进制前置拒绝 |
| 编辑结果片段 | 部分 | `edit.go:274-304,396-417`；`apply_patch.go:1361+` | 成功回 unified diff；失败/STALE 回带行号 current_snippet + suggested_view_offset |
| 写幂等回放 | **独有** | `write_idempotency.go:52-138`；`write.go:162-164`；`append_write.go:157-173` | 同内容回放 unchanged；append 同 offset+chunk 回放 append_replay，`mutated_paths=[]` |

### 3.3 搜索

| 能力 | 状态 | 证据 | 说明 |
|------|------|------|------|
| rg 三级回退 | 具备 | `ripgrep/resolver.go:86-117`；`grep.go:774-784` | `AICLI_RG_PATH`→捆绑/相邻→PATH→内置 walker |
| grep 模式族 | **强于文档** | `grep.go:32-37,1503-1523,3143-3152,3584-3610,4026-4036` | content/files_with_matches/files_without_match/count，context、type、literal、word、hidden/no_ignore、max_columns、json、pcre2、multiline、replace、stats、max_filesize、type_add/clear 等 rg 参数结构化透传 + rg_args |
| grep 分页/超时保全 | 缺失 | `grep.go:648,3346-3356,3320-3408` | 无 head_limit/offset；`maxMatches=100` 固定；无自身 timeout（ctx 取消即返回错误，不保留已聚合行） |
| grep 线程耗尽重试 | 缺失 | 未找到 | 无 `--threads 1` 重试 |
| 非法正则报错 | 具备 | `grep.go:1721-1728,3328-3332` | 内置引擎给可行动提示；rg 侧错误上抛 |
| glob 排序/分页/超时 | 缺失/部分 | `glob.go:56-61,163,354-404` | 有 limit（默认 100、上限 1000）；无 offset、无 mtime 排序、无独立超时与部分保全 |
| glob 默认忽略 | 不同 | `glob.go:370` | rg 走 `--files --hidden --no-ignore`（默认全含）；文档是「hidden+gitignored 也搜，但 .git 永远排除」 |
| ls | 具备 | `ls.go:46-63,95-107,143-144,200-216` | depth（默认 1、上限 10）、计数、目录优先排序、条目上限 1000；无 exclude、无 mtime/size 排序 |
| 边界校验 | 具备 | `glob.go:106-135`；`sandbox_support.go:87-175` | 会话 workspace 锚定 + `checkPath(OpRead)` + 拒绝绝对/`..` pattern |

### 3.4 Shell / 进程 / 后台

| 能力 | 状态 | 证据 | 说明 |
|------|------|------|------|
| shell 三别名 | 具备（成本问题） | `tools/manager.go:391-396`；`shell.go:13-29`；`execute_shell_command.go:10-25` | 同一 BashTool 三份 schema；描述重复且 execute_shell_command 描述 4.5 KB |
| 前台/后台 | 部分/不同 | `bash.go:155-158,1170-1286`；`broker.go:1404-1477` | 无 `run_in_background` 参数；`detach=true` 是 fire-and-forget（PID、无输出无日志）；后台任务走 `background_task`（job_id + LogPath + 心跳 + 启动验收 + restart_policy + priority） |
| timeout 语义 | 具备（策略不同） | `bash.go:56-72,1143-1168` | 默认 30s、go test≥5m、搜索 12s；运行时上限默认「无上限」，可用 `AICLI_SHELL_MAX_COMMAND_TIMEOUT` 设定（无 600s 默认上限） |
| argv 级只读分类 | **强于文档** | `policy/grants.go:228-303`；`policy/tool_policy.go:328-352` | 分段 + wrapper/env 剥离 + argv0 白名单（rg/grep/ls/get-childitem/cat/git 只读子命令/go 只读/`--version` 类）；敏感路径与动态语法 fail-closed |
| 根目录删除熔断 | **独有** | `shellrisk/shellrisk.go:20-45,713-758` | 防 env 前缀/包装器/引号/命令替换变体 |
| shell 输出折叠 | 部分 | `tool_output_budget.go:45-54,143-192`；`bash.go:1454-1468` | head-only 32 KiB + 完整 capture 归档（256 KiB 捕获上限）；无 head+tail 双窗口 |
| 退出码语义 | 部分 | `bash.go:166,450,1517-1518,1820-1830` | 描述与 metadata 明确「非零=内容结果」；信号退出用 ExitCode()（-1），未换算 128+N |
| Windows PowerShell | 具备（细节缺口） | `executor/shell_detect.go:71-92,115-137`；`bash.go:1489-1498` | pwsh 7→5.1→cmd；UTF-8 注入；`-NoProfile -Command`；无 `-NonInteractive`/`-EncodedCommand`；无跨调用持久 cwd |
| shell_output 语义 | 缺失/替代 | `broker.go:1483-1512`；`background/types.go:70-74` | `task_output(job_id,offset,limit)` 有 offset/next_offset/exit_code/心跳/静默/恢复诊断；无 wait 阻塞、无 from_offset/max_chars 流式、无 `[still running]` 文本标记 |
| kill / monitor | 缺失（模型面） | `background/manager.go:409-448`；`api/runtimeapi/background_handlers.go:101` | CancelJob 与端口→pid（`runtimeserver/service_control.go:464`，Windows 用 netstat）存在，但无模型工具；无定时唤醒监控 |
| 进程树/优雅终止 | **强于文档** | `executor/process_guard_windows.go:19-109`；`background/detached.go:750`；`bash.go:1301-1321` | Windows JobObject 优先 + taskkill /T /F 回退；Unix TERM→0.2s→KILL；上报树杀/残留 |
| 轮询抑制 | **独有** | `agent/polling_guard.go:22-40,110-156`；`agent/doom_loop.go:179-180` | 批指纹阈值 3 次、累计等待 >5min 升级为 advisory；只提示不阻塞 |
| 自动唤醒 | 缺失（工具面） | `api/runtimeapi/handler.go:5502-5557` | job 事件更新 session `active_job_ids`，未见唤醒 turn；唤醒设施在 `supervision/wake_scheduler.go`（生命周期用） |

### 3.5 任务 / 调度 / Web / 交互 / 子代理 / MCP

| 能力 | 状态 | 证据 | 说明 |
|------|------|------|------|
| todos | 具备（语义略弱） | `todos.go:92-102,114-224` | 整表替换；多 in_progress 软修复（保留最后一个）；`compareTodoLists` 记录新增/更新/移除；无「未完成项被丢弃」告警与「验证步骤」引导 |
| 通用 task_* 账本 | 缺失 | 未找到 `task_create/update/list/get/stop` | 团队/批量子代理内部有任务存储与 worker 侧 `read_task_spec/report_task_outcome/block_current_task` |
| cron/schedule_wakeup/sleep | 缺失 | 全仓仅 supervision wake（生命周期） | 无时间驱动工具 |
| web_search | 部分 | `web_search.go:63-83` | query/count（默认 5、上限 10）；**无 allowed/blocked domains**；DuckDuckGo/Bing |
| fetch | 部分 | `fetch.go:40-58,84-91,136-139,202-224` | url/format(markdown/text/html)/timeout（默认 30、上限 120）、5 MB 上限、32 KiB 预算 + 诚实截断 + 提示 download；无 startIndex/缓存/重定向上报；URL 安全仅 http(s) 前缀 + sandbox 主机白/黑名单（sandbox 未激活时不校验，`executor/sandbox.go:191-228`） |
| download / sourcegraph / artifact_read | 具备（文档外补充） | `download.go`、`sourcegraph.go`、`artifact_read.go` | download 支持 100 MB 流式 + 重试；sourcegraph 公共代码搜索；artifact_read 分页读归档 |
| ask_user_question | 部分 | `toolbroker/broker.go:191-211` | prompt/suggestions[]/required；无结构化多问题、header、multiSelect、preview、headless 披露 |
| 计划模式工具 | 具备 | `broker.go:213-280+`；`policy/plan_enter.go`；`planmode/` | enter（可带 plan_path/plan_write_paths）/exit（approve/request_changes/quit，模型 approve 只记待决）/plan_review；进入是用户门控 |
| worktree | 子代理级具备 | `isolation/worktree`；broker `apply_agent_worktree/discard_agent_worktree` | 主会话无 enter/exit_worktree |
| 子代理工具族 | **强于文档** | `toolbroker/broker.go:34-76,298+`；系列文档 | spawn_agent/spawn_subagents/spawn_team + wait/list/read_events + subagent_status/inspect/control/ack + supervision_* 快照/谱系 |
| 技能 | 具备（机制不同） | `skill/catalog_render.go:136-207,332-356` | 目录（名+描述+定位，默认 8K 字符预算、超预算降级）+ 纪律块；正文由模型自行 view 读取（渐进披露）；无 `activate_skill` 工具名 |
| MCP | 具备 | `mcp/registry/registry.go:231`；系列 MCP 文档 | `mcp__server__tool` 规范名、通配策略、热注册、quarantine；计划模式隐藏按系列文档 |
| run_command / taste / get_diagnostics | 未找到（模型面） | — | 有 slash 命令体系（但非模型工具）、memory/factledger 自动注入（非工具）、LSP 有文档无工具 |

---

## 4. 重点借鉴项：设计映射与落点

### 4.1 【P0-A】文件写入硬化三件套：读账本 / 陈旧防护默认化 / 原子落盘

**CommandCode 语义**：覆盖既有文件必须先在本会话完整读过（部分读不算）；磁盘在「读之后、写之前」变化则拒写；落盘用同目录临时文件 + rename，保留 mode，正确处理 symlink；BOM 往返保真。

**aicli 现状**：写工具覆盖前会自读旧内容算 diff（`write.go:156-160`），陈旧防护靠调用方显式传 `expected_sha256`（`write_idempotency.go:35-50`），默认不校验；write/edit/multiedit 都是 `os.WriteFile` 直写（`write.go:184`、`edit.go:253`、`multiedit.go:300`），固定 0644，无 BOM/UTF-16 处理。

**最小落地点**：

1. 新增会话级读账本（进程内即可起步）：`view`/`grep`（以及 `read` 类 MCP 由宿主选择）记录 `abs_path → {content_sha256, full_read: bool, read_at}`；存储挂到 `toolctx` 或 session 级 singleton，随会话清理。
2. `write`（覆盖既有文件时）与 `edit`/`multiedit`（写回前）取账本条目：
   - 未读或仅部分读 → 结果中带诊断（可配置 `warn|deny`，默认 warn 起步，避免打断存量工作流）；
   - 已读但磁盘哈希 ≠ 账本哈希 → 拒写（复用 `runtimeerrors.ErrWritePrecondition`，`NextAction` 引导重新 view）。
3. 抽共享 `writeAtomicFile(path, data, mode)`：同目录 `CreateTemp` → 写入 → `Sync` → `Chmod` 原 mode → `Rename`；`write`/`edit`/`multiedit`/`append_write` 全量替换路径统一改走它（append 走 O_APPEND + fsync 即可）。
4. 编码：读侧检测 UTF-8 BOM / UTF-16LE BOM 并记录；写侧把「原文编码+BOM」随读账本一起回写。先在 view/write 打通 UTF-8 BOM，UTF-16LE 可跟随。

**验证**：新增表驱动测试——「未读→warn」「读过但被外部修改→deny」「正常覆盖→原子 rename 后 inode 变化且 mode 保留」「BOM 文件 view→write 往返字节不变」；`go test ./internal/toolkit/tools/... ./internal/toolexec/...`。

### 4.2 【P0-B】工具 schema/描述 token 预算治理

**CommandCode 语义**：一个能力只留一份 schema，被吸收的旧工具用「到达即重写」保持兼容；工具描述刻意短（每请求都付费）；文档明确给出「省 540 tokens/请求」的量化。

**aicli 现状（实测）**：19 个内置工具 schema ≈ 60.4 KB；`grep` 20.4 KB、`execute_shell_command` 10.8 KB（单条描述 4.5 KB）、`bash` 4.6 KB、`shell` 4.5 KB、`aicli_exec` 4.3 KB。运行时已有三道压缩（`shellToolSurfaceRank`、`compactGrepParametersForModel`、`compactToolSurfaceToBudget`），但**源码层仍维护三份 shell 工具与超长描述**，并且 `listable.go:186-207` 把 shell/bash/execute_shell_command 全列为核心工具。

**最小落地点**：

1. 模型面只广告 `shell`：把 `bash`/`execute_shell_command` 标为 `MetaListWhen: never`（或 `should_list=false`），执行层保留注册以接旧名调用；在结果或系统提示中回报 `bash→shell` 等价（复用 `agent/tool_vocabulary.go` 的 legacy 建议逻辑，但落点是「可执行 + 不再广告」，不是拒绝）。
2. 把 `execute_shell_command` 里的 rg 参数迁移长说明抽到 `docs/tools/grep.md`（或技能），schema 参数描述只留一句「rg 参数可按 rg_args 透传，详见 grep 文档」。
3. 建立契约测试：统计所有 core 工具的 `len(description)+len(schema JSON)`，对单工具与总量设上限（例如单工具 ≤6 KB、core 总量 ≤48 KB），超限即失败；同时保证 `search_tool` 目录索引仍包含隐藏别名，模型找不到时可检索到。

**验证**：`go test ./internal/toolkit/... ./internal/agent/... -run 'ToolSurface|Compact|Vocabulary'`；复跑本次的 `go run` 度量脚本对比前后总量。

### 4.3 【P0-C】读工具格式感知：图片直通 + 文档抽取

**CommandCode 语义**：view 返回「渲染结果」而非字节——图片作为 image block（缩放系数披露）、PDF 文本层转 Markdown（扫描件给 `pdftoppm` 提示）、Word/PPT/Excel/ODT/RTF/EPUB 转 Markdown、`.ipynb` 带输出、其他二进制只给 MIME+大小；`write_file` 明确不会拿 Markdown 覆盖 `.docx`。

**aicli 现状**：`view.go:263-269` 二进制直接失败；图片能力只在消息附件链路（`imageattach/imageattach.go`、`imageprep`）；`filebrowse/stat.go` 已有扩展名/mime 白名单，可作为类型判定入口。

**最小落地点（分两步，避免一次引入大依赖）**：

1. **图片直通（P0）**：`view` 命中图片扩展名时读取并返回 `KindImage`（ToolResult 已有 `Data`+`MIMEType` 与图片输出类型），复用 `imageprep` 的压缩/缩放系数披露；超尺寸给出缩放比。对应「附件链路」与「读取链路」共用同一渲染器。
2. **文档抽取（P1）**：定义 `DocumentExtractor` 接口 + 内置 fallback：
   - PDF：优先纯 Go 文本层解析（或系统 `pdftotext` 探测），无文本层时返回「第 N 页无文本」并建议 `download` 后用图像通道；
   - Office/EPUB：先接可选外部转换器（本机存在才启用），不存在时退回「二进制 + MIME + 大小」提示——与 CommandCode 的「转换器未安装时降级」一致；
   - 明确契约：view 的文档结果是「渲染」，write 不允许用渲染文本覆盖原文档（在 write 的类型拒绝里加扩展名守卫）。

**验证**：`view_test.go` 增图片断言（MIME/Data/metadata 缩放比）；文档路径用 testdata 小样本（含扫描型 PDF 的降级分支）。

### 4.4 【P1-A】后台进程管理面：wait 语义 / kill / 定时唤醒

**CommandCode 语义**：`shell_output(wait=none|output|exit, from_offset, max_chars)`；`monitor_command(checkAfterMs)` 到点与退出各唤醒一次；`kill_shell(task/pid/port)` 优雅升级且不误杀无关进程组。

**aicli 现状**：`background_task` 提交面很强（job_id+别名、日志、心跳、静默时长、重启策略、启动验收探针）；`task_output` 只有 `job_id/offset/limit`（`broker.go:1483-1512`），轮询靠模型自觉（有 polling_guard 软刹车）；`CancelJob` 存在但无模型工具；唤醒设施在 supervision 层（生命周期）而非后台 job 层。

**最小落地点**：

1. `task_output` 加 `wait: "none"|"output"|"exit"` 与 `timeout_ms`（内部长轮询 job 事件：output=等待新输出或终态，exit=等待终态）；返回时明确「哪个条件先到」，无需模型轮询。
2. 新增 `task_kill`（或 `kill_shell`）：参数 `task_id | pid | port`；task_id 直接走 `background.Manager.CancelJob`；pid/port 复用 `runtimeserver/service_control.go:464` 的端口→pid 与进程树守卫；结果区分「未知 id / 已结束 / 已终止」。
3. 唤醒：job 进入终态（completed/failed/timed_out/cancelled）时给所属 session 调度一次 wake（复用 `supervision/wake_scheduler.go` 的预算与去重键），把「等待中的父回合」唤起；`monitor_command` 作为可选第二步（checkAfterMs 定时唤醒 + maxDuration 自动终止）。

**验证**：后台可靠性测试（`broker_background_reliability_eval_test.go` 扩展）：wait=exit 在 job 完成时立即返回、超时返回明确条件；kill 未知 id/已完成/运行中三分支；Wake 只发一次且不重复投递。

### 4.5 【P1-B】搜索/日志类工具的「预算 + 部分保全 + 分页」

**CommandCode 语义**：glob/grep 有独立时限，超时/中断返回已找到的部分结果；glob 有 offset 与 mtime 排序；grep 有 head_limit/offset；shell 前台输出 head+tail 双窗口。

**aicli 现状**：glob/grep 无自身超时（ctx 取消即整体失败）；glob 无 offset、无 mtime 排序；grep `maxMatches=100` 固定；shell 输出 head-only 折叠。

**最小落地点**：

1. `glob.go`：schema 加 `offset`；输出前按 `ModTime` 排序（同 mtime 再按 path）；在 walker 外层包 `context.WithTimeout`（默认 20s，可配置），超时或 ctx 取消时返回已收集条目 + `truncated=true` + `next_action`。
2. `grep.go`：schema 加 `head_limit`（默认 250，0=不限）与 `offset`，在 `buildGrepResult` 截断层消费；把 `maxMatches` 从硬编码改为「head_limit 的兜底安全值」；rg 运行外包预算，超时保全已聚合行。
3. `foldShellOutputToWindow`：改「首 16 KiB + 尾 16 KiB + 中段省略标记（含 artifact 指针）」，保持先归档后折叠的既有顺序。

**验证**：glob/grep 单测注入慢 walker/慢 rg 脚本，断言超时仍返回部分结果与 truncated 标记；shell 折叠测试断言首尾都在、中段标记字节数正确。

### 4.6 【P1-C】修复过程对模型可见（repair note）

**CommandCode 语义**：修复前置 `<repair_note>`，同时发规则级遥测。

**aicli 现状**：别名提升、强转、占位清理都在结果生成前静默发生；模型只能从「工具成功但参数不是它写的样子」中猜。

**最小落地点**：在 `toolexec.ApplyPreflight` 里收集本次修复列表（rule=alias/coerce/placeholder/path_auto_heal，key=参数名，不含值），执行成功后在结果文本前挂一行 `<repair_note>…</repair_note>`（或 metadata `repair_notes`，由渲染层决定是否展示）；同时 `observability.RecordToolInputRepair(rule, key)`。保持「不含值」的隐私约定。

**验证**：preflight 测试断言 `cmd=` 被提升后结果/元数据出现 alias 修复记录；遥测事件维度断言。

### 4.7 【P1-D】编辑歧义与边界（补齐 CommandCode 的差异项）

- 非 `replace_all` 且精确命中 >1 → 返回 `ambiguous edit: N occurrences (line …, line …)`，不静默取第一处（现行为 `edit.go:242-250`）。
- 增加 `replacement_count`（只替换前 N 处），与 `replace_all` 互斥校验。
- 覆盖前做文件大小（>10 MB 拒）与二进制嗅探拒绝（复用 `view.isBinaryFile` 逻辑方向，但作用在写侧）。
- 成功结果可附「编辑区带行号片段」（现只有 diff；`edit.go:396-417` 已有 snippet 构造可复用）。

### 4.8 【P2】其余借鉴项落点速览

| 项 | 落点 |
|----|------|
| Windows 设备/流路径黑名单 | `toolkit/tools/sandbox_support.go:{checkPath,resolve*}` 统一 deny（NUL/CON/COM1/LPT1、`\\?\`、`/dev/{null,zero,stdin}`、`/proc/*/fd/*`） |
| 128+N 信号退出码 | `bash.go:1517-1518,1820-1830` 的 exit 提取处检测 `syscall.WaitStatus.Signaled()` → `128+signal`；附带 `termination=signal` |
| PowerShell 硬化 | `executor/shell_detect.go:71-92`：agent shell 分支追加 `-NonInteractive`（不可用则忽略）、长脚本改 `-EncodedCommand`（免 cmd.exe 引号问题） |
| web_fetch 守卫/缓存/分页 | `fetch.go`：内建私网/环回/link-local/凭据/单标签拒绝（与 sandbox 主机策略叠加）；`startIndex` 参数 + 100k 字符窗口；进程内 15min LRU；结果 metadata 记录 `redirected_to` |
| web_search 域名过滤 | `web_search.go` schema 加 `allowed_domains/blocked_domains`（互斥校验），客户端二次过滤结果链接 |
| ask_user_question 结构化 | `broker.go:191-211` 扩展 `questions[]`（question/header/options[{label,description,preview}]/multiSelect），旧 `prompt` 保持兼容；headless 默认选项在结果里披露 |
| 会话级 worktree | `toolkit/tools` 新增 enter/exit（内部用 `internal/isolation/worktree` + `foldertrust` 口径），exit `remove` 前检查未提交变更/超基线提交，需 `discard_changes=true` |
| task_* 账本 | 复用 `team`/`subagentbatch` 的持久化与依赖语义，暴露 `task_create/update/list/get`；与 todos 的分工写进工具描述（todos=当前回合清单，task_*=跨回合依赖图） |
| sleep | 新增 `sleep(seconds|until, wake_on_input, reason)`：计时器 + 用户输入事件唤醒 + 时长上限策略（默认 10 分钟，截断时报剩余）；与 polling_guard 协同 |
| cron / schedule_wakeup | 单独立项：durable jobs 文件、jitter、7 天过期、`--resume` 恢复、两个 kill switch；先确认产品是否要「代理自定步调循环」 |

---

## 5. 不建议照搬 / 需要保留的差异

| 主题 | CommandCode | aicli | 判断 |
|------|-------------|-------|------|
| 超限输出处理 | 25k tokens 上限后**替换**为「用更窄查询重试」提示 | 每个工具自持窗口 + 完整输出归档 + `artifact_read` 翻页 | **保留 aicli**：归档式能让模型在需要时读回全部内容，信息不丢；只需统一「收窄建议」话术 |
| 工具门禁 | 工具只用注入 runtime；yolo 下越界写静默纳入新根 | 应用层 sandbox + workspace 边界 + profile/策略多层 | **保留 aicli**：越界写静默扩根不符合本项目安全基线（见权限文档 §5） |
| shell 输出读取 | 合并 bash_output/task_output/monitor_events 为一个 `shell_output` | `background_task`/`task_output` 已带丰富健康诊断与别名句柄 | **不要为改名而改名**：可加 wait 语义，但保持 job 语义与诊断字段 |
| 计划模式可写面 | 只允许 `~/.commandcode/plans/` | 白名单默认 `plan.md`（`PlanWriteAllowPaths`），可配 | **保留 aicli**：项目内计划文件更符合仓库工作流 |
| sleep 的默认上限 | 默认 10 分钟，截断报剩余 | 无 sleep（有 wait_agent 等真实等待） | 引入 sleep 时采用相同「上限 + 报剩余」，但优先宣传 wait_* 而非 sleep |
| 文档转换器 | 未随 CLI 分发、首次读时联网安装 | 本项目离线优先 | 若做 PDF/Office，**只做可选本机转换器 + 纯 Go 文本层**，不引入「首次读联网安装」 |
| 技能正文加载 | `activate_skill` 工具把正文塞回上下文 | 系统提示目录 + 模型自行 `view` 读取 | 两者等价；aicli 现有方式可审计（读了哪些文件），**不必新增工具**，可考虑在目录块里显式提示「用 view 读 SKILL.md」 |

---

## 6. 已有且不应回退的能力（对照 CommandCode 的优势）

1. **argv 级只读分类 + 敏感路径/动态语法 fail-closed**：`policy/grants.go:228-303`，比文档版白名单更细（wrapper/env 剥离、go 子命令级）。
2. **根/主目录递归删除熔断**：`shellrisk/shellrisk.go:20-45,713-758`，覆盖 env 前缀、包装器、引号与命令替换变体。
3. **Windows 进程树守卫**：JobObject/TerminateJobObject 优先 + taskkill /T /F 回退 + 残留 PID 上报（`executor/process_guard_windows.go:19-109`）。
4. **后台任务持久化与恢复语义**：SQLite 事件序列、重启恢复、restart_policy、startup_acceptance 探针、队列/饱和诊断（`background/types.go:5-45,77-119`）。
5. **统一输出合同**：先归档→工具自折叠→声明 `model_visible_budget`→渲染层不二次折叠（`tool_output_budget.go:31-140`）。
6. **轮询软刹车**：批指纹 3 次 + 累计等待 5 分钟预算，只提示不阻塞（`polling_guard.go`）。
7. **路径 did-you-mean + 唯一 auto-heal + circuit 重放候选**（`toolexec/preflight.go`）。
8. **edit 多级模糊自愈**（CRLF/trim/智能标点/空行漂移/缩进重建）+ STALE 诊断与可复制的带行号片段（`edit.go:201-224,396-607`）。
9. **apply_patch 多文件 staging 后统一 commit + Move/Delete**（`apply_patch.go:132-173,844-913`）。
10. **写幂等回放**（内容 SHA / offset+chunk SHA，回放标 `mutated_paths=[]`）与参数截断防线（64K/128K）。
11. **工具目录投影**：`search_tool` + `core_tool`/`defer_loading`/按预算压缩工具集，是比「全部常驻」更省 token 的机制，应继续演进而不是退回常驻。
12. **子代理/团队监督体系**：`subagent_status/inspect/control/ack`、worktree 隔离、审批转交与心跳预算（详见系列子代理文档）。

---

## 7. 建议落地顺序与验证

### Wave 1（P0，低成本高收益：先做「不改变行为契约」的加固）
1. 写工具原子落盘 + BOM 处理（§4.1 第 3/4 步）——纯内部替换，行为兼容。
2. 读账本 + 陈旧防护默认化（§4.1 第 1/2 步）——默认 warn、可配置为 deny，先观察误伤率。
3. shell 别名收口 + 描述瘦身（§4.2）——仅改广告面与描述，执行面不变。
4. 修复说明与遥测（§4.6）——preflight 内挂 metadata/文案。

### Wave 2（P1，能力补齐：工具面行为增强）
5. 后台 wait/kill/唤醒（§4.4）。
6. glob/grep 预算 + 保全 + 分页（§4.5）。
7. 图片直通（§4.3 第 1 步）。
8. edit 歧义/前 N 次/大小拒绝（§4.7）。
9. shell 首尾双窗口（§4.5 第 3 条）与 128+N、PowerShell 硬化（§4.8）。

### Wave 3（P2，产品向/依赖外部能力）
10. 文档抽取（§4.3 第 2 步，依赖转换器策略）。
11. task_* 账本、sleep、会话级 worktree、web_fetch 守卫/缓存/分页、ask_user_question 结构化、cron（需产品确认）。

### 通用验证约定
- 每个改动配 `go test`（包内表驱动 + 回归断言），优先复用既有测试文件：`view_test.go`、`write_idempotency_test.go`、`grep_test.go`、`glob_test.go`、`bash_test.go`、`broker_background_reliability_eval_test.go`、`preflight_test.go`。
- 涉及策略/权限的改动，同时跑 `policy` 与 `toolkit` 契约测试，避免「模型面隐藏但策略仍放行」的不一致。
- token 治理类改动（§4.2）用「度量脚本 + 阈值契约测试」双保险。

---

## 8. 盘点方法与限制

- **文档侧**：`https://commandcode.ai/docs/reference/tools` 全文落盘（`tmp/commandcode-tools.md` → 文本抽取 `tmp/commandcode-tools.txt`，约 31 KB 纯文本，覆盖 450 行小节），本文 §2 的内容均来自该文本。
- **代码侧**：主会话直接核验了 `toolargs`、`toolexec/preflight.go`、`toolkit/{listable,search_tool}.go`、`toolkit/tools/{view,write,edit,todos,fetch,web_search,shell,execute_shell_command,bash}.go`、`tool_output_budget.go`、`toolbroker/broker.go`、`policy/taxonomy.go`、`mcp/registry`、`skill/catalog_render.go`、`executor/sandbox.go` 等；三个只读子代理分别产出「文件系统与写入安全」「搜索/Shell/后台」「Web/计划/子代理」盘点，其中两个因运行时错误失败（无影响：搜索/Shell 子代理重跑成功；Web/计划/子代理部分由主会话补验），结论均已交叉核对，未证实处标注「未找到/缺失」。
- **token 估算**：用一次性 `go run` 程序构造 19 个内置工具并 `json.Marshal(name+description+parameters)` 统计字节数，按 4 B/token 粗估；中文描述实际 token 更高。运行时压缩逻辑（shell rank 去重、grep 参数压缩、工具面预算）会让**实际广告量低于**该值，报告已在 §3.1 注明。
- **限制**：CommandCode 的运行时行为（转换器安装、缓存、唤醒调度）只能依据文档描述，未做外部实测；本仓库侧未覆盖 `frontend/` 与 IDE/ACP 宿主渲染路径，涉及「工具可见性在 UI 的呈现」的结论以代码层为准。
- **临时产物**：`tmp/commandcode-tools.md`、`tmp/commandcode-tools.txt`、`tmp/extract_docs.js` 可随时删除；本次用于度量的一次性 Go 程序已清理。

---

## 9. 实施记录（2026-09-26，Wave 1 P0 切片）

本轮已按 §7 Wave 1 落地 4 项（第 5 项「view 图片直通」属 Wave 2，留待下一批）：

### 9.1 写工具原子落盘 + 编码保真
- 新增 `backend/internal/toolkit/tools/atomic_write.go`：同目录 temp + `Sync` + `Rename`，保留既有文件 mode；失败清理临时文件。
- 新增 `backend/internal/toolkit/tools/file_encoding.go`：UTF-8 / UTF-8 BOM / UTF-16LE / UTF-16BE 检测、解码与按原编码回写；`isBinaryBytes` NUL 比例嗅探。
- `write.go` / `edit.go` / `multiedit.go`：覆盖前解码原文件、二进制拒绝、按原编码回写、原子替换、结果 metadata 增加 `encoding`。

### 9.2 读账本 + 陈旧写防护
- 新增 `backend/internal/toolkit/tools/read_ledger.go`：会话级（`toolctx.SessionID`）路径 → {SHA-256, size, full_read, source, read_at}，每会话上限 4096 条，单文件哈希上限 8 MiB。
- `view.go`：成功读取后写入账本（单文件路径；批量路径后续接入），UTF-16/BOM 文件整文件解码（≤8 MiB）并在 metadata 标注 `encoding`。
- `write/edit/multiedit` 覆写前评估：
  - 账本记录来源为 **view** 且哈希不匹配 → **拒绝**（`WRITE_PRECONDITION_FAILED` + `stale_write`，提示先 view 或显式传 `expected_sha256=<当前哈希>`）；
  - 记录来源为本会话写入（write/edit/multiedit）且被外部修改 → **告警放行**（`read_before_write=stale`），避免 `edit → 格式化 → edit` 这类循环被误伤；
  - 无记录 → `read_before_write=unread` 提示（覆盖仍允许）。
- 写/编辑成功后回写账本，避免自写被下一次编辑误判为陈旧。

### 9.3 shell 别名收口与描述瘦身
- `bash.go` 的 `DefinitionMetadata` 增加 `alias_of: shell`（`execute_shell_command` 通过嵌入继承）；模型面唯一 shell 由既有 `optimizeModelToolSurface` 保障（已有测试覆盖）。
- `execute_shell_command` 工具描述 3360 → 约 420 字符、`command` 参数描述 3271 → 约 140 字符；rg→grep 迁移与 Windows 细节全文迁至 `docs/tools/shell-grep-migration.md`，原测试要求的关键引导（workdir / 裸 cd / Get-Location / head / Select-Object）保留在短描述中。

### 9.4 输入修复可见化（repair note）
- `tools/argument_aliases.go` 新增 `toolkitArgRepairNotes`：报告 `alias:<旧键>→<规范键>` 与 `unwrap:_raw`，**只含规则与键，不含值**。
- `tools/manager.go` 新增 `executeLocalToolkitTool`：规范化后执行，命中修复时在结果前挂 `<repair_note>…</repair_note>` 并附 `repair_notes` metadata。
- 覆盖范围：本地 toolkit 工具（view/write/edit/shell/grep…）；broker 工具与执行器内的类型强转修复尚未接入，见 §9.6。

### 9.5 测试与验证
- 新增 `write_safety_test.go`（原子写 mode/无残留、UTF-8 BOM 覆盖、UTF-16LE 编辑、读账本 fresh/stale/显式确认、二进制拒绝）与 `argument_aliases_repair_test.go`（别名与 `_raw` 修复说明、值不泄漏）。
- 全绿：`go test ./internal/toolkit/tools/`、`./internal/tools/`、`./internal/toolkit/`、`./internal/toolexec/`、`./internal/agent/ -run 'ToolSurface|ToolList|ToolSearch|Grep|Shell'`。
- 回归中修正了一处过度严格：最初对所有「记录与磁盘不一致」一律拒写，命中了 `TestMultieditTool` 的自写-重置循环；最终策略见 §9.2（仅 view 快照陈旧时硬拒）。

### 9.6 已知边界与后续
- 账本为进程内会话级；跨进程/重启不保留（重启后退化为 `unread` 提示）。
- 批量 `view`（`files[]`）暂未写账本；`append_write` 走 filetransport，未接入原子写与陈旧检查。
- UTF-16 文件 >8 MiB 时 `view` 报错并建议转换；`write/edit` 读取无上限但只有在能完整解码（BOM 合法）时才按原编码回写。
- 未接入：图片直通/文档抽取、glob/grep 超时保全与分页、后台 wait/kill/唤醒、edit 歧义报数量与 `replacement_count`、shell 首尾双窗口、web_fetch 守卫/缓存、task_* 账本、sleep/cron（对应 §7 Wave 2/3）。

### 9.7 图片直通（Wave 2 第 7 项，2026-09-26 续做）

- **契约**（`internal/toolresult/image.go`）：新增 `image_passthrough` / `image_path` / `image_mime_type` / `image_width` / `image_height` / `image_bytes` / `image_note` 元数据键与 `ImagePassthroughFromMetadata`（同时兼容平铺与 `tool_metadata` 嵌套布局）。
- **读侧**（`internal/toolkit/tools/view_image.go` + `view.go` 钩子）：`view` 在读取前按扩展名（png/jpg/jpeg/gif）筛选，再用 `imageprep.Prepare` 校验真实格式并复用长边 1568px / 32MB 上限：
  - 命中 → 返回 `KindStructured` + 图片元数据（尺寸、MIME、字节数、缩放说明），内容为中文摘要；
  - 缩放/转码产物复制到内容寻址的稳定目录 `$TMP/ai-agent-runtime-images/<sha16>.<ext>`（幂等），避免系统临时目录清理导致下一轮请求读不到；
  - 超过体积上限 → 返回「图片 + 大小 + 未附加」说明（不报错，不静默丢）；
  - 假图片扩展名 / 非图片二进制 → 回退原有文本读取与二进制拒绝路径，行为不变。
- **注入侧**（`internal/agent/tool_result_images.go` + `message_builder.go`）：`MessageBuilder.AppendToolResults` 在整批 tool_result 追加完成后，把声明的图片（存在且去重）合成一条 **user 消息**（`llm.NewUserPromptMessageWithImages`，携带 `input_images` + ContentParts）；单张不可读时退化为纯文本提示。放在批次末尾保证 `tool_calls → tool_result` 顺序不被破坏，且 provider 只在 user 角色渲染 image block 的约束得到满足。
- **测试**：`view_image_test.go`（PNG 直通元数据、假图片回退、非图片二进制仍拒绝）、`tool_result_images_test.go`（注入 user 消息、无元数据不注入、文件缺失不注入）。
- **验证**：`go test ./internal/toolkit/tools/ ./internal/toolresult/ ./internal/tools/ ./internal/agent/ ./internal/chatcore/ ./internal/llm/ ./internal/toolbroker/ ./internal/toolexec/` 全绿。
- **边界**：批量 `view files[]` 尚未接图片直通；历史里引用的稳定目录图片若被清理，provider 层会跳过该图（降级为文本）；文本模型路由收到图片输入属既有产品面问题（附件链路同）。

### 9.8 后台任务管理面：task_output wait 语义 + task_kill（Wave 2 第 5 项，2026-09-26 续做）

- **task_output wait**（`toolbroker/broker_task_wait.go` + `broker.go`）：
  - 新增 `wait: none|output|exit`（默认 none）与 `timeout_ms`（默认 30000，收敛到 1000..120000）；
  - `output` 等到「有新字节（offset 之后）或进入终态」，`exit` 只等终态，250ms 步进轮询；
  - 返回 metadata `wait_condition`（immediate/output/exit/timeout）与 `waited_ms`；**超时是内容结果而非错误**，模型据此决定继续等待或先做别的事；
  - ctx 取消立即返回取消错误；未知 wait 值显式报错（不再默默无效）。
- **task_kill**（`ToolTaskKill`）：参数 `job_id`（别名 `task_id`）+ 可选 `reason`；复用 `background.Manager.CancelJob`（先 cancel 再进程树终止）与 job 别名解析；结果区分三分支：未知 id（`ErrJobNotFound` + 修复提示）、已结束（`cancelled=false` + 终态与 exit_code）、已受理（`cancelled=true` + `cancel_source=user_request`）。
- **策略面**：`policy/taxonomy.go` 记 `task_kill` 为 control；`capability.go` 映射 `CapBackgroundTask`（accept_edits/dont_ask/default 模式下与 background_task 同样 ask/deny，read-only 直接拒绝）；`grants.go` 列为 dangerous（不可 always-allow 记住）；`tool_policy.go` read-only 拦截；`broker_arg_audit.go` / `broker_arg_kinds.go` 契约表补齐（audit 测试要求每个 broker 工具与参数键都有登记）。
- **测试**：`broker_background_wait_test.go`（wait=exit 终态即返回、wait 超时是内容结果且任务仍在跑、未知 wait 报错、kill 未知 id / 已结束 / 运行中三分支、timeout 收敛表）。
- **验证**：`go build ./...`、`go test ./internal/toolbroker/ ./internal/background/ ./internal/policy/ ./internal/agent/` 全绿。
- **未做（保留）**：job 终态唤醒所属会话回合（复用 supervision wake 调度）与 `monitor_command` 定时唤醒——两者都要接触 supervision 生命周期，风险面大于本轮收益，留在下一批；当前 `wait=exit` 已消除「模型自觉轮询」的主要成本。

### 9.9 glob/grep 分页与预算保全（Wave 2 第 6 项，2026-09-26 续做）

- **glob（`toolkit/tools/glob.go`）**：
  - 新增 `offset`（非负，配合 limit 分页）；结果先抓取 `offset+limit` 个候选（硬上限 5000，命中即标 truncated），再按 **mtime 从新到旧**（同 mtime 按路径）排序后切片；
  - 20s 搜索预算：walker（`walkGlobTree`）与 `findMatchesInCurrentDir` 逐项检查 ctx，超时返回已收集结果并标 `timed_out=true`（不再整体失败）；rg 分支在预算/取消时把已产出的 stdout 解析为部分结果，完全无产出才返回错误；
  - metadata 新增 `offset`/`next_offset`/`has_more`/`timed_out`；输出追加 `next_offset` 分页提示与预算说明；`offset` 超出抓取窗口时给 next_action；未命中且非截断仍走既有 empty-success。
- **grep（`toolkit/tools/grep.go`）**：
  - 新增 `head_limit`（缺省沿用内置 100；0 = 不限，受 5000 条抓取安全上限与既有字节预算保护）与 `offset`（跳过前 N 个匹配）；
  - 抓取上限统一由 `grepCollectLimit(opts)` 计算（offset+headLimit，兼容既有 `maxMatches` 语义），rg per-file `--max-count` 与内置 walker 的早停同步更新；
  - `buildGrepResult` 在渲染前切片分页，metadata 新增 `offset`/`head_limit`/`head_limit_explicit`/`next_offset`/`has_more`；分页截断提示为「显示第 X-Y 个匹配；next_offset=N」；offset 超出抓取窗口时给 next_action。既有「显示前 N 个匹配」文案在未显式分页时保持不变。
- **测试**：`glob_pagination_test.go`（mtime 排序+分页、深 offset 指引、预算耗尽返回部分结果、walker 保留已收集匹配、offset 解析边界）、`grep_pagination_test.go`（rg 输出切片分页、深 offset 指引、内置 walker 分页一致性）。
- **验证**：`go test ./internal/toolkit/tools/ ./internal/toolkit/ ./internal/tools/ ./internal/toolexec/ ./internal/agent/` 全绿。
- **未做（保留）**：grep 侧的独立预算与 rg 部分输出保全（本轮只做了 glob；grep 的 ctx 取消仍整体失败），以及预算值可配置（当前 glob 固定 20s）。
- **边界**：mtime 排序发生在抓取窗口内（深分页受 5000 条硬上限保护）；`head_limit=0` 的「不限」仍受安全上限与字节预算约束。

### 9.10 edit 歧义与边界（Wave 2 第 8 项，2026-09-26 续做）

- **歧义拒写**（`toolkit/tools/edit.go`）：非 `replace_all`、非 `replacement_count` 且精确命中 >1 → 返回 `ambiguous edit: old_string 命中 N 处（行 …）`，metadata 带 `failure_class=ambiguous_edit`/`occurrences`/`occurrence_lines`（最多采样 20 行）与 next_action（提示扩上下文或显式 replace_all/replacement_count），文件不落盘；模糊（空白/缩进容忍）路径原本就要求窗口唯一，保持不变。
- **`replacement_count`**：新增参数，只替换前 N 处（N > 命中数时按实际命中数），与 `replace_all` 互斥、必须 > 0（两者都在解析期显式报错）；成功 metadata 增 `occurrences`/`replace_all`/`replacement_count`。
- **大小拒绝**：`editMaxFileBytes = 10 MB`，在读取前按 `os.Stat` 检查，超限直接拒绝（`failure_class=file_too_large` + next_action 指向 shell/拆分），不再读入内存；二进制嗅探沿用既有 `isBinaryBytes` 检查。
- **成功片段**：成功结果 metadata 增 `edited_snippet`（带行号、最多 16 行）与 `edited_snippet_start_line`，复用 `findClosestEditSnippetWithLine`/`formatEditClosestLines`；内容仍保留 unified diff。
- **测试**：`edit_ambiguity_test.go`（歧义拒写且文件未变、replacement_count 前 N 处、互斥/非正数校验、>10 MB 拒绝且文件未变、成功片段）。
- **验证**：`go test ./internal/toolkit/tools/ ./internal/toolkit/ ./internal/tools/ ./internal/toolexec/ ./internal/agent/` 全绿（含既有 Edit/Multiedit 回归）。
- **边界**：`multiedit` 的每个 edit 仍是单处替换语义，未引入 ambiguity 报错（保持其“逐项替换第一处”契约）；歧义错误码复用 `ErrToolInvalidArgs`（不是 stale_context）。

### 9.11 shell 首尾双窗口（Wave 2 第 9 项第 1 步，2026-09-26 续做）

- **折叠策略**（`toolkit/tools/tool_output_budget.go`）：`foldShellOutputToWindow` 由 head-only 改为 **head + tail**：内容预算对半，head 切在 rune 边界，tail 额外优先从行首开始（退行不超过 512B 且至少保留一半 tail 窗口），notice 计入同一 32 KiB 窗口；迭代收敛保证 `head+notice+tail ≤ budget`，退化窗口只保留 notice。
- **中间值怎么读**（notice 直接给出路线，见 `shellWindowFoldNotice`）：
  - 精确区间 `omitted middle bytes [start,end)`；
  - 第一页配方 `artifact_read offset=start, limit=<artifactReadMaxLimitBytes()=32256>`，随后跟 `next_offset` 续读；
  - 整段中区的**页数**（成本预告）+ `bisect from offset=<中点>`（怀疑中段才有关键信息时直接二分开翻）；
  - 更便宜的替代：重跑窄化命令（更紧的 pattern、Select-String / Select-Object -First/-Last），而不是翻完整 capture。
- **metadata**：新增 `output_window_head_bytes` / `output_window_tail_bytes` / `output_window_omitted_start` / `output_window_omitted_end` / `output_window_omitted_bytes` / `output_window_middle_pages`，模型无需从正文反推。
- **成本口径**：模型可见窗口恒为 32 KiB（与输出总长无关）；完整补读成本 = ⌈omitted/32 KiB⌉ 次 `artifact_read`，每次 ≤32 KiB；shell capture 默认上限 256 KiB ⇒ 补读整段中区 ≤ ~8 页，且按需支付；语义定位优先窄化重跑（或落盘后用 grep/view 定位），避免线性翻页。
- **测试**：`TestFoldShellOutputToWindowKeepsHeadAndTail`（深中段标记不外泄、tail 结论保留、notice 含区间/首屏/页数/bisect/便宜路线）、`TestFoldShellOutputToWindowDoesNotSplitRunes`（多字节边界）、`TestOwnShellOutputWindowPublishesMiddleRoute`（metadata 与页数自洽）；`tool_budget_l4_render_test.go` 断言更新为「前缀=head + 后缀=tail + notice 新文案 + middle metadata」。
- **验证**：`go test ./internal/toolkit/tools/ ./internal/toolkit/ ./internal/tools/ ./internal/toolexec/ ./internal/agent/ ./internal/output/ ./internal/toolbroker/` 全绿；`go vet ./internal/toolkit/tools/` 干净。
- **边界**：tail 从行首开始属于「尽量」（找不到换行或退行过多时保留原窗口）；render 层对非 shell 工具的 L4 折叠仍是 head-only（本次只改 shell 自有窗口）。

### 9.12 128+N 信号退出码 + PowerShell 硬化（Wave 2 第 9 项第 2 步，2026-09-26 续做）

- **128+N 信号退出码**（`toolkit/tools/bash.go`）：
  - `exitCodeFromError` 对 `*exec.ExitError` 先查 `syscall.WaitStatus.Signaled()`，命中即报 `128+signal`（SIGKILL→137、SIGTERM→143），不再让 Go 的 `ExitCode()=-1` 把「被信号杀死」变成「未知失败」；Windows 的 `WaitStatus` 永不 Signaled，行为不变；
  - 新增 `terminationFromError`（返回 `termination=signal` + 信号名）与 `annotateTerminationMetadata`：非零退出（内容成功）与硬失败两条路径都写入 `signal`；若进程守卫已写 `termination`（如 timeout/tree kill），保留守卫口径（「我们按超时杀了它」比裸信号更可行动）；
  - `friendlyHintFor` 的 exit 提取改用同一入口，消除重复逻辑。
- **PowerShell 硬化**（`executor/shell_detect.go`）：
  - agent 分支（login=false）→ `-NoProfile -NonInteractive -Command`（profile 与交互式提示再也不能阻塞/污染工具调用）；login 分支保持原语义；
  - 脚本长度 > `PowerShellEncodedCommandThreshold`（7000 字符）→ 两种模式都改 `-EncodedCommand`（base64 UTF-16LE），绕开 Windows 命令行长度与 cmd.exe/CreateProcess 多层引号陷阱；`EncodePowerShellCommand` 内嵌 `PowerShellCommandPrefix`（UTF-8 输出指令），因此 `bash.go` 与 `cmd/aicli/functions/shell.go` 的 `prefixPowershellUTF8*` 在检测到 `-EncodedCommand`（`ShellArgsUseEncodedCommand`）时跳过前缀，避免破坏 base64；
  - `PowerShellCommandPrefix` 常量收敛原先散落的 UTF-8 指令字面量。
- **测试**：`executor/shell_detect_test.go`（agent 分支参数、长脚本 base64/UTF-16LE 往返、login 分支仍编码、bash/cmd 不变）、`tools/bash_exit_status_test.go`（普通状态码不误判、信号退出 137+termination=signal（Unix 真实 `kill -9 $$`，Windows skip）、BashTool 信号退出仍是内容成功且 metadata 正确）；`background/detached_test.go` 期望更新为 `-NoProfile -NonInteractive -Command`。
- **验证**：`internal/executor`、`internal/background`、`cmd/aicli/functions` 全绿（含 `go vet ./internal/executor/`）；`internal/toolkit/tools` 全量与 `internal/agent`/`output`/`toolkit`/`background` 通过 **`-overlay` 替换为 HEAD 版 `view.go`** 后全绿（见下方已知阻塞）。
- **已知阻塞（并行会话）**：工作区中 `internal/toolkit/tools/view.go` 存在另一路未完成改动，引用 `viewReadResult` 上尚未定义的字段（`Tail`/`WindowStart`/`EmptyFile`/`ReaderClampedLines`/`ReaderClampedBytes`），导致 tools 包当前无法编译；本次验证用 `go -overlay` 将该文件替换回 HEAD 版本完成，未触碰对方文件。该改动落地后需重跑一次常规 `go test ./internal/toolkit/tools/`。
- **边界**：`-NonInteractive` 仅在非 login（agent）分支；`\\?\` 等设备路径黑名单、web_fetch 守卫仍属 Wave 3（未做）。

### 9.13 grep 部分输出保全（Wave 2 第 6 项收尾，2026-09-26 续做）

- **rg 侧**（`toolkit/tools/grep.go:searchWithRipgrep`）：
  - 引入 `stopErr`：超时/取消、或 rg 报错但已产出 stdout 时，不再丢弃已打印的匹配；`ctx.Err()!=nil 且无产出` 仍如实返回 ctx 错误（没有证据就不编造部分成功）；
  - exit 1（no-match）继续走正常解析（`--stats` 仍需读取已产出内容）；rg 报错且有产出时错误细节留在 `stopErr`，仅在「无任何匹配可保全」时回退原有硬失败；
  - 部分结果元数据：`partial=true`、deadline 时 `timed_out=true`、`next_action` 明确「收窄再试，不要原样重试」。
- **内置 walker 侧**（content/files/files_without/count 四种模式）：取消/超时时保留已收集匹配/文件/计数，走同一 `grepWalkerPartialResult` 判定（`truncated=true` + 部分提示）；无产出或非中断错误保持原有硬失败。content 模式的上下文渲染在中断时退回无上下文渲染，不让 ctx 错误吞掉已收集匹配。
- **预算一致性**：`buildGrepResult` / `buildGrepResultWithEngine` 新增变参 `extraNotices`，部分提示的长度与截断提示一样在切片前从 `grepOutputBudgetBytes` 中预留并追加，payload 仍不超预算。
- **测试**：`grep_partial_test.go`（rg 超时保全 2 条匹配 + partial/timed_out + engine=rg；无产出超时保持失败；取消保全且不标 timed_out；walker 部分结果判定与「无产出/非中断不制造部分结果」；部分提示不撑破字节预算）。
- **验证**：并行会话的 `view.go` 已完成，本轮起 tools 包常规编译恢复；`go test ./internal/toolkit/tools/ ./internal/toolkit/ ./internal/tools/ ./internal/toolexec/ ./internal/agent/ ./internal/toolbroker/ ./internal/output/ ./internal/executor/` 全绿。
- **边界**：JSON 模式下部分行（被截断的 JSONL 尾行）由 `normalizeRipgrepJSONOutput` 逐行解析兜底，解析不到的尾行自然丢弃；部分结果的 `match_count` 是「已收集」而非全量。

### 9.14 设备/流路径黑名单推广到全部文件工具（Wave 3 第 10 项，2026:09: 26 续做）

- **复用而非重造**：read 工具一路已提交 `toolkit/tools/path_guard.go`（`unsupportedPathNameReason` / `unsupportedFileModeReason`，带 `path_guard_test.go`）。本轮不再重复实现检测，而是把同一道门推广为**所有文件工具**的统一拒绝，并补上缺失的设备名。
- **检测面补齐**：`windowsReservedDeviceNames` 增加 `CONIN$`/`CONOUT$`；设备命名空间增加 `\??\`（原有 `\\.\` 与 `\\?\GLOBALROOT` 保留；`\\?\C:\dir\file.txt` 这类普通扩展长度路径按既有设计**继续允许**）；Unix 侧增加 `/dev/tty`、`/dev/console`。
- **统一 deny + 机器码**（`path_guard.go:devicePathRefusalError`）：包装为 `runtimeerrors.ErrToolInvalidArgs` + context `{policy: device_path, failure_class: device_path, target_path, refusal_reason, path_refused, next_action}`；runtime 的 `copyRuntimeErrorMetadata` 会把这些键并入工具结果 metadata，模型拿得到可行动的恢复路线（ls/glob 选普通文件）。消息文案与 view 既有拒绝保持一致。
- **落点**（`sandbox_support.go:checkPath`）：设备检查放在 `sandbox==nil` 早退**之前**，因此 view/write/edit/multiedit/append_write/apply_patch/download/glob/grep/ls 全部受同一路径形状不变量保护，且不依赖 sandbox 是否激活（此前未激活时 checkPath 直接放行）。
- **view 顺序调整**（`view.go`）：view 的自身名称检查移到 `checkPath` 之前，保留其 `path_refused`/`refusal_reason` 元数据的既有结果形状；checkPath 里的同一道门是其余工具的兜底。
- **测试**（`device_path_deny_test.go`）：扩展名拒绝（CONIN$/CONOUT$、`\??\`、/dev/tty、/dev/console）与近邻不误伤（`\\?\C:\dir\file.txt`、`/dev/ttyS0`）；无 sandbox 时 checkPath 仍拒绝且带机器码；write 端到端拒绝（NUL / /dev/null，不落盘）；view 仍返回 `path_refused` 元数据。
- **验证**：`go test ./internal/toolkit/tools/ ./internal/toolkit/ ./internal/tools/ ./internal/toolexec/ ./internal/agent/ ./internal/toolbroker/` 全绿。
- **边界**：只拒绝「路径形状」；FIFO/socket/字符设备等由 `unsupportedFileModeReason` 在 stat 后兜底（view 路径），其余工具仍依赖 sandbox/权限层处理实际文件类型。

### 9.15 web_search 域名过滤（Wave 3，2026:09: 26 续做）

- **参数**：`allowed_domains` / `blocked_domains`（数组或逗号/分号分隔字符串），**互斥**；schema 与 description 同步（`web_search.go`）。
- **归一化**（新文件 `web_search_domains.go`）：小写、去 `*.` 前缀、`https://Example.com/docs`→`example.com`、去端口/尾点；归一化后非合法主机名（如裸 `https://`）直接丢弃，整表无有效项→`ErrToolInvalidArgs` + `failure_class=invalid_domain_filter` + next_action。
- **匹配**：子域包含（`example.com` 命中 `www.example.com`，不命中 `notexample.com`/`example.com.evil.test`）；blocked 优先，allowed 只保留命中；无法解析出 host 的结果**保留**（不静默隐藏畸形 URL）。
- **结果面**：过滤后 `count/match_count/returned_count/result_count`=返回数，新增 `provider_count`、`filtered_out` 与生效规则；「搜索成功但全部被过滤」保持 Success 并给过滤器专属 next_action（区别于真·零命中）；未启用过滤时元数据不变（既有测试不受影响）。
- **测试**：`web_search_domains_test.go`（归一化/子域边界、互斥与无效表拒绝、blocked/allowed 端到端、全过滤专属路由、未过滤元数据不变），provider 用 stub 注入，无网络。

### 9.16 后台 job 终态唤醒（Wave 3 第 5 项，2026:09: 26 续做：API 侧第一步）

专家级只读调研（子代理）给出接入设计，本轮落地**最小可独立验证切片**：API 宿主侧，零 `background` 包改动、零 sqlite schema 改动。

- **接线**（`api/runtimeapi/handler.go:handleBackgroundEvent`）：仅当事件类型在终态白名单（completed/failed/timed_out/orphaned/cancelled）且带 session_id 时，异步 `go projectBackgroundJobTerminal`；output 等高频事件在 `appendJobEvent` 热路径上被白名单挡住，绝不触发调度。
- **投影**（新文件 `api/runtimeapi/background_supervision.go`）：
  - `UpsertNotification`：`SubjectKind=background_job`（`supervision/types.go` 新增 `SubjectJob`）、`SubjectID=job_id`、`SubjectVersion=事件 CreatedAt`（稳定 epoch）、`EventType=background_job_{completed,failed,timed_out,orphaned,cancelled}`、严重度（failed/timed_out/orphaned=critical，completed/cancelled=warning）、状态（terminated/timed_out/orphaned）、`ResolutionState=unresolved`、`AllowedActions=[inspect]`、Reason=command（截断 160）+ exit_code/error_code/message/cancel_source（整行 ≤320）；
  - `ScheduleWake`：`WakeEventTerminal` + `ObligationID=job_id` + `EventSeq=notification.EventSeq` → `DeriveNotifyKey` 提供 exactly-once；**不论严重度都调度**（completed 也必须唤醒，否则「后台任务完成」这一异步证据会静默丢失）；
  - root scope：`sessionManager.Get` → `apiAgentRootSessionID`，失败降级为该 session id；store/scheduler/session 缺失一律 no-op（best-effort，5s 超时）。
  - 预算分类自动落位：`background_job_failed`/`_timed_out`→failure 类，`completed`/`cancelled`/`orphaned`→other 类（`WakeBudgetClassOf` 子串匹配）。
- **测试**（`background_supervision_test.go`，复用 `newAPIWakeTestHandler` 夹具）：终态恰一次 + 同 epoch 重放不复发（inbox 行数与 wake 数都不变、seq 不变）；digest 确实携带该 job（证明 wake 可投递、不是空转）；failure 类严重度/Reason/预算类；白名单（含大小写）与非终态拒绝；缺 identity/非终态 no-op；**接线级**：output 事件零 wake、timed_out 事件 `Eventually` 出现且仅出现 1 条 wake；无 background manager 时 Reason 构造仍安全。
- **验证**：`go test ./internal/api/runtimeapi/ ./internal/supervision/ ./internal/background/` 全绿（并行会话的 `view.go` 中途半成品时用 `-overlay` 完成过一次等价验证）。
- **未做（下一步）**：① CLI 宿主接线（`cmd/aicli/commands/chat_actor_host.go:2800-2822` 的 `buildLocalChatBackgroundManager` 需补 EventHandler 与本地投影实现，并给 `background.Config` 增加宿主可注入的 hook 或改构造参数）；② 会话关闭时 resolve 该 session 的 pending wake（避免永久 pending）；③ 与 `wait=exit` 长轮询的「已观察」标记（避免回合结束后重复唤醒）；④ `monitor_command`（timer 用 `time.AfterFunc`，到点仍走 ScheduleWake + progress 类预算；registry 需随会话关闭清理）。

### 9.17 后台 job 终态唤醒：共享投影抽取 + CLI 宿主接线（2026:09: 26 续做）

**抽取（host-neutral，避免两套 admission 语义）**：新增 `internal/supervision/background_job.go`
- `BackgroundJobTerminalDisposition(status)`（completed→warning/terminated、failed→critical/terminated、timed_out→critical/timed_out、orphaned→critical/orphaned、cancelled→warning/terminated）与 `IsTerminalBackgroundJobStatus`（同一张表，宿主白名单直接用）；
- `BackgroundJobTerminalInput`（RootScopeID/TargetParentSessionID/JobID/Status/Command/ExitCode/ErrorCode/Message/CancelSource/Epoch）；
- `ProjectBackgroundJobTerminal(ctx, store, wakes, in)`：`UpsertNotification`（`SubjectJob`、epoch 身份、unresolved、AllowedActions=[inspect]）+ `ScheduleWake`（`WakeEventTerminal`，不论严重度都调度）；零 store/零 identity 明确报错，`wakes=nil` 只落通知；
- `FormatBackgroundJobTerminalReason`（命令/消息折叠空白，单字段 160 rune、整行 320 rune，截断带 `…`）。
- 测试 `background_job_test.go`：五态映射与白名单（含大小写）、恰一次 + 同 epoch 重放不复发 + 重跑换 epoch 可再唤、预算类落位、非法输入、无 scheduler 只落通知、reason 截断/折叠、零 epoch 兜底。

**API 侧**改为薄适配器（`api/runtimeapi/background_supervision.go`）：解析 root scope、补 command（manager `GetJob`，manager 缺失时为空）、把 payload 字段转成输入；行为不变，原 7 个测试保持全绿。

**CLI 侧接线**（`cmd/aicli/commands/chat_actor_background_supervision.go`）：
- `localBackgroundJobEventRelay` 延迟绑定 host：manager 早于 host 装配（`chat_actor_host.go:1221`），host 建好后 `bind`（`:1282`），期间事件安全丢弃（`sync.RWMutex`，manager 热路径不受影响、零 `background` 包改动）；
- `handleLocalBackgroundEvent`：终态白名单 → 异步 `projectLocalBackgroundJobTerminal`（5s 超时）→ 落通知 + `wakeSupervisedParent`（与 progress check 同款：runnable 起 turn，busy 保持 durable）；
- root scope 复用 `localSupervisionProgressCheckSessionID()`；epoch 与 API 同口径（事件时间戳）；`buildLocalChatBackgroundManager` 增加 `onEvent` 参数（nil 安全）。
- 测试 `chat_actor_background_supervision_test.go`：output 事件同步零副作用、终态异步恰一条（Eventually）+ 重放不增行、relay 未绑定/无控制面安全 no-op、缺 identity 不投影、rootScope 退化与 epoch/字符串辅助。

**验证**：`supervision`（BackgroundJob 6 例）、`api/runtimeapi`（BackgroundJob 7 例）、`cmd/aicli/commands`（LocalBackgroundJob 3 例）、`toolkit/tools`（web_search/device-path 用例）全绿。`cmd/aicli/commands` 整包当前有 3 个与本改动无关的失败（`TestChatDebugDisplayShowsStorageSection` 缺 "Maintenance: runs="、`TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning`、`TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled` 的 `AmbientRunMeta` 为 nil）：三者都不引用 background manager（仅命中 `context.Background()`），属于并行会话在工作树中的半成品改动（`internal/agent/message_builder.go`、`internal/toolkit/tools/*`、`internal/policy/*`、未跟踪的 `internal/agent/tool_result_images.go` 等）；另注意并行会话曾把 `internal/chat/plan_mode_bypass_restore_test.go` 写成缺 package 声明的半成品，导致 CLI 包一度无法构建测试（用 overlay 替换该文件后本轮用例仍全绿）。

**未做（下一步）**：
① 「模型已经看过终态」的重复唤醒抑制：`wait=exit` 返回终态时所属 turn 仍在跑，wake 会等 turn 结束再补一轮冗余 turn（digest 只是重复信息，但会消耗 other/failure 预算）。设计取向：manager 记 in-flight waiter（`BeginOutputWait/EndOutputWait`，broker 的 `readTaskOutputWithWait` defer 收尾），终态事件发射前若 waiter 活跃就把「终态将被读取」写进 job metadata；宿主投影看到该标记时只落已 resolved 的 inbox 记录、跳过 ScheduleWake（`SuppressWake`）；
② 会话关闭时 resolve 该 session 的待投递 wake（避免永久 pending 与后续会话复用时的陈旧投递）；
③ `monitor_command`/`task_monitor`：timer 用 `time.AfterFunc` 只做计时，到点仍走 `ScheduleWake`（progress 类预算）+ 宿主 drain；`maxDurationMs` 复用 `CancelJob`；registry 随 job 终态/会话关闭清理；注意 progress wake 必须携带通知内容（`digestDeliverable` 判空会静默 release）。
④ 预算可配置（保留项，待确认配置面：env + `Resolve*` 还是配置文件）。

### 9.18 已观察终态抑制重复唤醒（2026:09: 26 续做，§9.17 未做①收口）

**问题**：`task_output(wait=exit)` 命中终态时所属 turn 仍在跑；终态事件同时触发宿主的唤醒投影，于是 turn 结束后会再补一轮「你已经看过的终态」digest，白耗 other/failure 预算。仅靠 broker 事后打标记会晚于投影（等待循环 250ms tick + SQLite 写），**必然输掉竞态**，所以标记必须由 manager 在终态事件发射前完成。

- **manager（`internal/background/observation.go`）**：
  - `outputWaitRegistry`：按 job 计数的 in-flight waiter 表（`BeginOutputWait`/`EndOutputWait`，`sync.Mutex`，惰性创建 → 直接字面量构造的 `Manager` 仍然可用，nil 全安全）；
  - `appendJobEvent` 在**调用 EventHandler 之前**，若事件类型是终态且该 job 有活跃 waiter，就写 job metadata `terminal_observed=true`（`MetadataTerminalObserved` 导出，读侧用 `TerminalObserved(job)`）；manager 内部快照优先，投影侧 `GetJob` 立刻可见；
  - 泄漏保护：超过 5 分钟未释放的登记按不存在处理（最长支持的 wait 是 2 分钟），多余 `End` 是 no-op。
- **broker（`broker_task_wait.go`）**：`wait=output|exit` 时 `BeginOutputWait` + `defer EndOutputWait`（取消/超时也必然释放）；`wait=none` 不登记。
- **supervision**：`BackgroundJobTerminalInput.Observed` → **照常落 durable 通知（保持 unresolved，证据不丢）但不调度 ScheduleWake**；digest 仍携带该 job（测试钉住），因此「抑制」只抑制多余 turn，不抑制证据。
- **宿主**：API 的 `backgroundJobDigestInfo`（原 `backgroundJobCommand`）与 CLI 的投影都从同一份 job 快照读 command + 观察标记；CLI 在 observed 时直接跳过即时投递。
- **测试**（新增 10 例）：manager 层「终态事件在 handler 前打标记」「无 waiter 不打」「End 后不打」「output/running 不打」「registry 计数/TTL/多余 End」「nil 安全」；broker 层「wait=exit 终态 → 标记置位」「wait=none 立即读 → 不置位」；supervision「Observed → 有通知无 wake，digest 仍含该 job」；API/CLI 宿主「observed job → 有通知（带 command 摘要）无 wake」。
- **验证**：`go test ./internal/background/ ./internal/supervision/ ./internal/toolbroker/` 全绿；`./internal/api/runtimeapi/` 全绿（并行会话把 `approval_explain_settings_test.go` 写成 `undefined: mux` 的半成品时用 `-overlay` 完成等价验证）；CLI 侧 4 例全绿。
- **残留（下一批）**：①「wake 已先于观察登记落库」的窗口（投影先跑完、waiter 稍后才观察到）仍会投递一轮冗余 digest ——彻底解决要在投递回调里按 job 标记做二次抑制并 resolve 已认领 wake，收益有限（仅该窗口内），暂不引入投递侧耦合；② 会话关闭时 resolve pending wake；③ `monitor_command`；④ 预算可配置。

### 9.19 会话关闭作废本会话待投递 wake（§9.18 残留②收口）

**问题**：CLI 退出时，本会话的 pending wake（含已认领未投递）仍留在 ledger；下次 resume 同一会话后，turn 结束的 drain 会再补投一轮「平轮前 preflight 已经展示过」的过时 digest，白耗一次投递与 wake 预算。

- **supervision（`internal/supervision/session_close.go`）**：`ResolvePendingWakesForSession(ctx, store, rootScopeID, sessionID)` —— 按 (root scope, target session) 列出 pending wake（**不筛 claim 状态**：会话没了，认领与否都无投递对象）后逐条 `ResolveWakePending`；返回清理条数。**只删 wake 义务，不删 notification**：证据仍在 inbox（unresolved）与 job store，由 resume 后首个自然 turn 的 preflight digest 呈现。
- **CLI（`finalizeChatSessionWithError`）**：新增 `resolveLocalChatSessionPendingWakes(session)`，best-effort（退出路径绝不因监督面失败而卡住），并保持既有「退出不冷开存储」约定——store 暴露 `Opened()` 且为冷时直接跳过（复用 `SQLiteSupervisionStore.Opened`），nil/空会话/空 store 全安全。
- **测试（3 例新增）**：supervision 侧「只清本会话、其他会话与其他 root scope 不受影响」「空 session/nil store 是 no-op 且 ledger 不动」；CLI 侧「关掉本会话 1 条、其他会话 1 条保留、重复调用与 nil 幂等」（复用巡检测试的监督控制面 fixture）。
- **验证**：`go test ./internal/supervision/ -run ResolvePendingWakesForSession` 2 例全绿；CLI 侧 5 例全绿（本轮又遇并行会话把 `cmd/aicli/commands` 写成半成品导致一次 `[build failed]`，重跑即恢复——非本改动）。
- **残留**：API 侧 `DeleteSession` / `CloseSession` / `CloseSessionAgent` 尚未接同一清理（需要该侧 root scope 口径与 store accessor，且 `internal/api/runtimeapi/handler.go` 当前有并行会话写入；接入点已定位，留待并入该侧改动时一起做）。
