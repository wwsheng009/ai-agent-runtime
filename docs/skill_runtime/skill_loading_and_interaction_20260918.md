# Skill 加载与交互逻辑（实况梳理）

> 时点：2026-09-18（P1 `expose_skills` + P2 `/skill` 回合化落地后）
> 范围：`backend/internal/skill`、`backend/internal/api/skills`、`backend/cmd/aicli`、`frontend/src`
> 方法：全部结论以当前工作区代码为准（附 文件:行号），并附今日对运行中服务的实测证据。
> 关联文档：`skill_invocation_mechanism.md`（两条链路的早期版本）、`../plan/skill-model-driven-invocation-plan.md`（P0–P2 方案与进度）。

---

## 0. 一句话结论

Skill 的"加载"是一次性的目录扫描 + 注册表登记（热重载负责增量），
但"模型/用户如何用到 skill"有四套**不同的投影**与三条**不同的执行入口**；
`/skill xxx` 走的是最后新增的第三条：**把指定 skill 的说明与程序清单注入 ReAct 回合，由模型自己决定调用哪些程序**。

---

## 1. 总览

```text
磁盘  <skill root>/**/skill.yaml | SKILL.md   (+ companion prompt.md)
  │
  ├─ Loader / ManifestParser          解析 manifest（legacy 或 Codex 入口）
  ├─ Registry                         注册：name/path 双索引 + keyword/pattern 索引 + summary
  └─ HotReload (fsnotify + debounce)  增量 reload / remove / reloadAll（与 embedding 索引同步）
        │
        ▼
   四种投影（同一份 skill，给四类消费者）
   ① 路由索引        triggers(keyword/pattern) + 语义文本   → Router（选 skill）
   ② function schema skill__<name> + description + 3 参数   → aicli chat 的工具面（模型选调用）
   ③ ProgramGuide    skill/程序清单/workflow steps           → 模型驱动执行（REST model 模式 / expose_skills）
   ④ Skill 实体      systemPrompt/userPrompt/workflow/tools  → Executor（确定性执行）
        │
        ▼
   三种执行入口
   A. aicli chat（skills-as-functions，route-first 暴露 → 模型 tool_call）
   B. POST /api/runtime/skills/{name}/execute（admin/debug；前端在无回合宿主时回退）
   C. POST /api/agent/chat（主入口；`/skill` 与显式 expose_skills 都落在这里）
```

---

## 2. 加载与装配

| 环节 | 事实 | 位置 |
|---|---|---|
| 目录来源 | `skills_runtime.skill_dir` / `skill_dirs` / `extra_skill_dirs`；再按 Codex 兼容规则补：`<anchor>/skills`、各祖先 `<dir>/.agents/skills`（repo scope）、`~/.aicli/skills`、`~/.aicli/agents/skills`（user scope）。只返回实际存在的目录并稳定去重 | `internal/skill/roots.go:9-97` |
| 解析入口 | 只解析 legacy manifest 与 Codex `SKILL.md`；companion `prompt.md` 支持 lazy / eager 两种加载；`systemPrompt/userPrompt` 可回写为 `prompt.md` | `internal/skill/manifest.go:224-227,371,476-498,642-648`；`loader.go:117-157` |
| 批量加载 | `Load/LoadAll/Discover/DiscoverAll`，结果按 path 去重；`Discover*` 只产出轻量 summary | `internal/skill/loader.go:32-115` |
| 注册表 | `skills` + `skillsByPath` 双索引、`summaries`、已 hydrate 缓存、MCP manager 引用；`Register` 内建 keyword/pattern 索引 | `internal/skill/registry.go:40-124,352-376` |
| 装配 | `bootstrap.Manager`：`EnsureSkillDirs` → 目录集合 → `LoadAll`（或 `DiscoverOnly`）→ 可选 HotReload → 可选 EmbeddingRouter → 注入 skills handler | `internal/bootstrap/manager.go:77-145` |
| 热重载 | fsnotify 监听多根目录 + debounce 批处理；事件分派到 `reloadSkill` / `removeSkillByManifest` / `reloadAllSkills`；source rank 决定同源覆盖顺序；暴露 stats/events 与 `/skills/hot-reload/*` API | `internal/skill/hot_reload.go:91-145,381-538,606-649` |
| 语义路由 | `RouteWithConfig`：keyword → pattern → embedding，去重 → minScore 过滤 → maxResults 截断；embedding 仅在 `Router.EnableEmbedding && Embedding.Enabled` 时构建（默认 threshold 0.5 / top-k 5） | `internal/skill/router.go:85-123`；`embedding_router.go:31-42` |

---

## 3. 四种投影（谁看什么）

| 投影 | 生成处 | 内容 | 消费者 |
|---|---|---|---|
| 路由索引 | `registry.buildIndex` | 仅 `triggers`：keyword / pattern 两套索引 | Router / EmbeddingRouter |
| 语义索引 | `embedding_router` | Name/Description/ShortDescription/Category/keyword triggers/Capabilities/Tags 拼成索引文本 | EmbeddingRouter |
| function schema | `skills_integration.go:297-366,469-481` | 名 `skill__<name>`（小写、非法字符折叠、>64 字符加 FNV 哈希）；description 拼 name/description/category/capabilities/tags/tools；参数固定 `prompt`(必填) / `context` / `options` | aicli 模型工具面 |
| ProgramGuide | `internal/skill/executor.go:618-656`（导出 `ProgramGuide:661-663`、`ProgramTools:665-668`） | `Skill program guide:` + skill 名/描述 + `available programs` + `workflow steps`（含 args 示例）+ **前缀说明**（`/skill <name>` 只是调用标记） | 模型驱动执行（REST `execution_mode=model`、agent chat `expose_skills`） |
| Skill 实体 | manifest + hydrate | `systemPrompt` / `userPrompt` / `workflow.steps[].args` / `tools` | Executor |

> 关键语义：`triggers` 只服务路由，**不会**原样发给模型；`tools` 既进 function schema 的 description，也决定模型驱动模式下的可用工具面。

---

## 4. 三条交互链路

### 4.1 链路 A：`aicli chat`（skills-as-functions）

```text
启动：initSkillFunctions → 每个 skill 注册为 skill__<name>（懒加载 resolver）
每轮：SelectRequestFunctions
        = 显式提及（prompt 含函数名/skill 名，忽略大小写）
        + Router 路由候选（top-k 默认 5；mode auto|prefer|only）
        + 历史回补（历史 tool_calls 出现过的 skill）
模型：tool_call(skill__xxx, {prompt, context, options})
执行：FunctionRegistry.ExecuteFunction → SkillFunction.Execute
        prompt 键序 prompt→request→input→task；context 合并 session profile；options 直传
      → skill.Executor.Execute（auto：Handler→Workflow→executeDefault）
```

- 注册与命名：`cmd/aicli/commands/skills_integration.go:21-31,1110-1197,830-895`
- 暴露选择：`skills_integration.go:101-169`、`function_catalog.go:284-311`
- 执行与参数映射：`skills_integration.go:368-444`、`function_catalog.go:441-462`
- `/functions` 命令只读内存 catalog 做"暴露预览"，不扫描目录

### 4.2 链路 B：REST 执行（admin/debug 与前端回退）

- 路由：`POST /api/runtime/skills/{name}/execute`（`handler.go:713-731` 的 `/skills` 路由族；执行实现 `ExecuteSkill` 附近）
- 入口语义：直接进 Executor；若请求带 `options.execution_mode="model"`，则跳过 Handler/Workflow，走 `executeDefault` + ProgramGuide + 工具循环
- 前端回退：宿主未接线回合提交时，composer 调 `executeSkill(name, {prompt, sessionId, options:{execution_mode:"model"}})`，成功后显示"执行成功"回执（`use-composer-command-executor.ts:357-372`）

### 4.3 链路 C：`POST /api/agent/chat`（主入口，`/skill` 走这条）

```text
请求（前端）: messages=[{role:user, content:"/skill <name> <args>"}]
             + expose_skills=[<name>] + enable_react + enable_routing + stream + resume_on_disconnect
后端:
  1) 字段解析           handler.go:1488-1491
  2) 强制 ReAct         len(ExposeSkills)>0 ⇒ EnableReAct=true        handler.go:1520-1524
  3) route 探测         routeAttempted ⇒ routeCandidatesWithRuntime()  handler.go:1810-1814
  4) 流式 + ReAct 分支   handler.go:1815-1816
  5) guide 注入         buildSkillExposureMessages() → system message   handler.go:1823-1830 / 7768-7801
  6) ReAct 循环         runtimechatcore.ExecuteNonStream/流式执行；模型自选工具
  7) 遥测                route_candidates/route_matched 写入 meta/route/result 事件（只读）handler.go:1857,2031
  8) 落库 + SSE done    权威历史持久化；前端按会话流渲染
```

- 未知 skill：第 5 步 fail loudly → **HTTP 400 `skill not found: <name>`**（实测见 §6.4）
- 非流式（`ExecuteNonStream`）与流式共用同一注入函数：`handler.go:2279-2286`
- 注意：`expose_skills` **不限制工具面**——注入的是文档，模型仍可在运行时全量工具面里选择（实测中模型用了 `grep`+`fetch`，而该 skill 只声明了 `fetch`）

---

## 5. 前端交互面

| 能力 | 事实 | 位置 |
|---|---|---|
| 目录加载 | `useRuntimeSkillCatalog` 在**挂载时拉一次** `listRuntimeSkills()`；失败只记录 error，不重试；无热重载后刷新 | `hooks/workspace/use-runtime-skill-catalog.ts:6-49`；`api/runtime/skills.ts:377-391` |
| API 客户端 | list / detail / search / stats / hot-reload(stats,start,stop,reload) / execute；admin 面带 `X-Skills-Admin-Token` | `api/runtime/skills.ts:43-53,377-505,534+` |
| 命令定义与分派 | `/skill` 为 execute 类命令；**点选候选 = 选技能**（回填草稿），**提交 = 执行** | `hooks/workspace/composer/use-composer-command-surface.ts:124-157` |
| 回填文本 | `composerSkillCommandText(name)` = `/skill <name> `（尾随空格，光标落在参数位） | `lib/composer-skill-options.ts:118-121` |
| 回合消息文本 | `composerSkillTurnPrompt(name, prompt)` = `/skill <name> <args>`（线程可见、与输入框一致） | `lib/composer-skill-options.ts:127-130` |
| 宿主接线 | `main-section.tsx` 用 `onSubmit({prompt, exposeSkills:[name]})` 构造 `handleRunSkillTurn` | `workspace-shell/main-section.tsx:184-199` |
| 请求体 | `expose_skills` 仅非空时写入（snake_case）；同时带 `enable_react/enable_routing/resume_on_disconnect` | `agent-chat-turn/turn-bootstrap.ts:30-34,110-121` |
| 通知语义 | `needName` / `notFound` / `notAvailable` / `needPrompt` / `failed`；**回合成功不显示回执**（消息流即回执） | `use-composer-command-executor.ts:286-372` |
| 技能市场/页面 | `WorkspaceSkillsSurface` 与 `pages/skills/*` 提供列表、详情、搜索、热重载开关；独立 landing 区块亦复用 | `components/workspace/workspace-skills-surface.tsx`、`hooks/use-skills-market.ts`、`pages/skills/*` |

---

## 6. `/skill xxx` 的端到端执行逻辑（重点）

### 6.1 前端阶段（提交前不产生任何请求）

| 步骤 | 行为 | 失败表现 |
|---|---|---|
| 1. 解析 | `args.trim()` 为空 → 打开技能弹窗（无弹窗能力则 `needName`）；否则 `parts[0]` = skill 名，其余为 prompt | — |
| 2. 目录校验 | 目录未加载（`skillNames.length===0`）→ `notAvailable`；名字不在目录 → `notFound`（本地判定，**不发请求**） | 通知条 |
| 3. prompt 校验 | 只有 skill 名没有参数 → 弹窗或 `needPrompt`（不伪造空回合） | 通知条 |
| 4. 提交 | 有 `onRunSkillTurn`（web 宿主）→ 回合化；否则 REST 回退（`execution_mode=model`） | 回合化失败 → `failed` |

### 6.2 请求体（回合化）

```json
{
  "messages": [{"role": "user", "content": "/skill fetch_url_content https://example.com"}],
  "expose_skills": ["fetch_url_content"],
  "enable_react": true,
  "enable_routing": true,
  "stream": true,
  "resume_on_disconnect": true,
  "session_id": "...", "turn_id": "...", "workspace_path": "...", "model": "...", "provider": "..."
}
```

### 6.3 后端处理顺序

1. **强制 ReAct**：`expose_skills` 非空 ⇒ 即使宿主漏配 `enable_react`，也会打开（避免"文档注入了却没有工具面"）。
2. **route 探测照跑**：`enable_routing=true` ⇒ 对 `lastMessage`（即 `/skill ...` 整串）跑 keyword/pattern/embedding 路由，结果只用于遥测（`route_attempted` / `route_matched` / `route_candidates`）。
   **不会短路**：流式 + ReAct 分支在命中后直接进入 ReAct 循环并 `return`，静态直执分支只在 `ExecutePlannedSubagents` 等其它路径使用（`handler.go:1815-1816,2062-2068,2070-2187`）。
3. **文档注入**：`buildSkillExposureMessages` 取 skill → hydrate → `ProgramGuide` → 作为 **system message** 追加；同时注入 agent/workspace 上下文消息，然后 `ReplaceHistory` + 追加本轮 user 消息。
4. **ReAct 循环**：模型读 guide（skill 名、描述、可用程序、workflow 步骤与参数示例、前缀说明），自行决定调用哪些工具与参数；工具面是**运行时全量工具面**，不是 skill 声明的子集。
5. **产出**：最终回答写入权威历史（`persistChatTurn` 等价路径）并通过 SSE `done/result` 下发；回合中若前端刷新，`resume_on_disconnect` 允许回合继续，前端按 `/runtime` 游标续传。
6. **失败**：未知 skill 在注入前就以 400 结束（fail loudly）；模型/工具失败按 ReAct 既有错误路径呈现。

### 6.4 实测证据（2026-09-18）

| 观测 | 结果 |
|---|---|
| 未知 skill 探针 `expose_skills:["__no_such_skill__"]` | HTTP 400 `{"error":"skill not found: __no_such_skill__"}` |
| 注入探针（真实模型） | 模型复述："有。…skill 名称：`fetch_url_content`；可用程序：`fetch`；workflow 步骤 `fetch_content` → 工具 `fetch`，参数 `{"format":"text","timeout":30,"url":"{{prompt}}"}`" |
| `/skill run_shell_command pwd`（会话 `…094957_u9ub1BES`） | 11:03:37 `shell` 成功（模型直接执行 pwd，未走 workflow 直执） |
| `/skill fetch_url_content baidu`（同会话） | 11:04:59–11:05:14 回合：`grep`+`fetch`+`fetch`；模型按 guide 尝试 text/markdown 两种格式；因参数不是 URL 且目标页 JS 渲染，返回空内容后向用户反问 |
| `/skill fetch_url_content https://example.com`（会话 `…111739_wHvQp6dB`） | 11:17:54 `fetch` 成功，返回 `Example Domain…`，模型给出总结 |

### 6.5 与其它入口的差异（一句话版）

| 入口 | 谁决定调用 | 工具面 | 是否落会话历史 |
|---|---|---|---|
| `/skill`（链路 C） | 模型（读 ProgramGuide 自选程序） | 运行时全量工具面 | 是（普通回合） |
| REST execute + `execution_mode=model` | 模型（同样读 ProgramGuide） | skill 声明的程序（`ProgramTools`） | 视 `persistChatTurn` 配置 |
| REST execute（默认 auto） | 后端确定性（Handler→Workflow） | workflow 步骤内的工具 | 同上 |
| aicli `skill__*` | 模型（读 function schema） | skill 声明的程序 | 由 aicli 会话维护 |

### 6.6 TUI 的 `/skill` 与 Web 的 `/skill` 不是同一条路（2026-09-18 远程实测 · 变更前快照）

> 变更提示（2026-09-18 下午）：本节记录的是**改造前**的 TUI 行为；交互式 `/skill` 已改为回合机制，见 §6.7。`/skill --direct` 仍保留本节描述的直执语义。

对运行中的 aicli 会话（`/web/api/invoke` 远程驱动，日志 `C:\Users\vince\.aicli\chat-logs\...`）实测：

| 项 | TUI（aicli chat） | Web（workspace） |
|---|---|---|
| 命令实现 | `chat_skill_picker.go:130-175`：resolve → parse args → authorize → `executeDirectFunction` → `SkillFunction.Execute` → `skill.Executor` | `use-composer-command-executor.ts:286-372` → 宿主 `onRunSkillTurn` → `submitPrompt({prompt, exposeSkills})` |
| 是否发起 ReAct 回合 | **否**（日志 `turn_id: "direct"`；`/web/api/turn` recent 始终为 0） | **是**（`source=agent_react`，落会话历史、渲染为线程消息） |
| 默认执行路径 | `execution_mode=auto`：Handler→Workflow→executeDefault（确定性） | 模型驱动：ProgramGuide 注入 system message，模型自选程序 |
| 模型参与的条件 | 仅当显式传 `options.execution_mode=model` 时，**skill executor 内部**跑工具循环（工具面 = 该 skill 的 `ProgramTools`） | 始终由模型决定调用哪些程序；工具面是**运行时全量工具面** |
| 实测证据 | `/skill run_shell_command echo TUI_SKILL_DIRECT_OK` → 2.2s 完成、无 turn；`/skill fetch_url_content {"prompt":"https://example.com","options":{"execution_mode":"model"}}` → 日志 `execution_mode=model`，TUI 输出模型对 Example Domain 的总结，仍无 chat turn | 见 §6.4：`expose_skills` 回合中模型用 `grep`+`fetch` 自选程序并落库 |

**运维要点**：`/web/api/invoke` 只等待「chat turn」结束。TUI 的 `/skill` 属于 slash 命令执行、不是 turn，因此 invoke 会在 ~2.2s 提前返回 `status=settled`，而命令可能仍在跑（实测 model 模式跑了 40s+）。远程驱动脚本应轮询 `GET /debug/chat/screen?format=text`（或 `/debug/chat/status`）判断命令终态，而不是只看 invoke 的返回。

### 6.7 变更：交互式 TUI `/skill` 已改为回合机制（2026-09-18 下午）

| 项 | 变更后行为 |
|---|---|
| 默认路径 | `/skill <name> <args>` **不再直执**：命令层返回 `CommandResult.SendSkillTurn`（`chat_skill_picker.go`），dispatch 经既有 send 管线提交普通回合（`command.go` → `chat_skill_turn.go`） |
| 回合注入 | `ProgramGuide` 作为一次性 system 消息进入本回合请求历史（`internal/agent/turn_context.go` → `loop.go:574`，随 ctx 生命周期，不落持久历史） |
| 函数面 | skill 函数与其声明程序（命中的）按回合叠加（`overlayTurnPinnedTools`，`loop.go:1625`），**不写回**会话级稳定工具面缓存 |
| 直执保留 | `/skill --direct <name> <args>` 仍走 `executeDirectFunction`（确定性、无模型），用于兜底/调试 |
| 非交互投影 | plain / JSON / headless 行为不变（结构化分支仍以 `unifiedDirectInteractiveOutput` 为门） |
| 测试 | `chat_skill_turn_test.go`（命令层 / pin 一次性 / 叠加不污染）、`internal/agent/turn_pin_overlay_test.go`（context 与工具叠加）；`cmd/aicli/commands`、`internal/agent`、`internal/chat`、`internal/skill` 四包回归全绿 |

---

## 7. 现状边界与缺口

1. **前端目录不随热重载刷新**：`useRuntimeSkillCatalog` 只在挂载时拉一次；后端热重载后候选可能滞后（需刷新页面）。
2. **`/skill` 只支持"单 skill + 自然语言参数"**：没有 workflow 参数表单，参数语义完全交给模型；`{{prompt}}` 由模型具体化。
3. **route 遥测与执行解耦后的观感**：`/skill` 回合会出现 `route_attempted=true / route_matched=false` 的记录（对 `/skill ...` 串文本做路由本就难以命中关键词），属预期而非故障。
4. **两条链路的"模型感知"投影不同**：aicli = function schema；web/agent chat = ProgramGuide，字段与措辞未完全对齐。
5. **前端"有帧活动但页面不更新"渲染闸门问题**：已定位（`docs/analysis/frontend-sse-render-gate-analysis-20260918.md`），修复方案 v2 尚未实施。
6. **skill 参数合法性无前端校验**：如 `fetch_url_content` 要求绝对 URL，前端不拦截，模型会自行猜测目标地址。

---

## 8. 附：验证与索引

**本次梳理期间跑过的验证**

- `go test ./internal/skill/ ./internal/api/skills/ -count=1`：通过
- `go build ./cmd/runtime-server`：通过
- `npx vitest run`（composer/agent-chat-turn 相关 9 文件）：69 用例通过；`npx tsc --noEmit`、`npx eslint`（改动文件）通过

**关键文件索引**

| 主题 | 文件 |
|---|---|
| 目录/发现 | `backend/internal/skill/roots.go`、`codex_discovery.go`、`codex_manifest.go` |
| 解析/加载 | `backend/internal/skill/manifest.go`、`loader.go`、`hydrate.go` |
| 注册表/索引 | `backend/internal/skill/registry.go` |
| 热重载 | `backend/internal/skill/hot_reload.go` |
| 路由/语义 | `backend/internal/skill/router.go`、`embedding_router.go` |
| 执行器/投影 | `backend/internal/skill/executor.go`、`exposure_projection.go` |
| 装配 | `backend/internal/bootstrap/manager.go` |
| HTTP 入口/注入 | `backend/internal/api/skills/handler.go` |
| aicli 链路 | `backend/cmd/aicli/commands/skills_integration.go`、`function_catalog.go`、`chat_tool_executor.go` |
| 前端客户端 | `frontend/src/api/runtime/skills.ts` |
| 前端命令面 | `frontend/src/hooks/workspace/composer/*`、`lib/composer-skill-options.ts` |
| 前端接线/请求体 | `frontend/src/components/workspace/workspace-shell/main-section.tsx`、`hooks/workspace/agent-chat-turn/turn-bootstrap.ts` |
| 技能市场 | `frontend/src/components/workspace/workspace-skills-surface.tsx`、`hooks/use-skills-market.ts`、`pages/skills/*` |
