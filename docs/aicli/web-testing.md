# aicli micro web client 前端测试指南

`aicli` 微型 Web 客户端（loopback `/web/`）前端的测试方法：测试环境搭建、手工回归清单、自定义下拉框（combo popup）等关键组件的专项用例。

前端源码位于 [backend/cmd/aicli/commands/web/](../../backend/cmd/aicli/commands/web/)，无构建步骤，经 `go:embed` 随二进制发布。`app.js` 为 ES module 入口（`<script type="module">`），功能域拆分为 `js/` 下 12 个模块（util / markdown / stream / chat / ui / runtime / sessions / approvals / config-admin / provider-editor / provider-import / sse），各模块导出 `initXxx()` 由入口按序调用。改动后刷新浏览器即生效（重新编译二进制则需 `go build`）。

维护约束：

- 新功能代码进对应功能域模块，不要再往 `app.js` 堆（入口只做 init 调用与启动序列）。
- 模块间可变状态不直接 import 读写（import 绑定只读，赋值会 TypeError），一律走导出的访问器函数（如 `getUiState()`、`clearPendingPrompts()`、`setInputHistoryIdx()`）。
- ES module 必须经 http(s) 加载，`file://` 直接打开 `index.html` 会因 CORS 失败——测试务必走下方方式 A/B。

## 1. 测试环境搭建

### 方式 A：真实后端（推荐做最终验证）

```bash
aicli chat --pprof
# 启动后终端 /debug display 区块会给出 Web 客户端地址
# 浏览器打开 http://127.0.0.1:<port>/web/
```

优点是 API 数据真实（provider 列表、模型列表、SSE 事件流）；缺点是依赖本机已配置的 provider，且无法随意构造边界数据（如空协议、超长模型名）。

> 鉴权提示：真实后端的 `POST /web/api/*` 需要 `X-AICLI-Token`（令牌在启动行打印，
> 页面加载后会自动注入并由 fetch 包装器附加，浏览器手工测试无需处理）。
> 开发模式下（默认在 `127.0.0.1`/`localhost` 自动开启，`--web-dev=false` 关闭）跳过令牌校验；
> 使用 `--web-host 0.0.0.0` 时，回环 IP（`127.0.0.1`/`localhost`）始终跳过校验，
> 本地网络 IP 与远程 IP 仍需令牌。

### 方式 B：静态伺服 + stub API（推荐做日常开发与边界用例）

前端是纯静态文件，用任意静态服务器指向 `web/` 目录，再把 `/web/api/*` 打成桩即可让页面完整跑起来。启动时页面会请求的最小端点集：

| 端点 | 桩数据要点 |
|------|-----------|
| `GET /web/api/runtime` | `{ current: { provider, model, reasoning_effort }, providers: [{ name, models, model_details }] }` |
| `GET /web/api/config` | `{ config_path, default_provider, chat: {}, providers: [...] }`，provider 对象需含 `name/protocol/base_url/enabled/api_key_set/api_key_source/api_key_masked/models/default_model` |
| `GET /web/api/sessions` | `{ sessions: [] }` |
| `GET /web/api/screen` | 任意 JSON |
| `GET /web/api/status?format=text` | 纯文本（调试页签状态文档，与 `aicli /debug` 内容一致；桩可为任意多行文本） |
| `GET /web/api/skills` | `{ count: N, skills: [{ name, function_name, kind, description, category, version, labels, capabilities }] }`（技能页签目录，与 `/skills` 同源） |
| `GET /web/api/skills/{name}` | 单个 skill 详情（`name` 可用目录名或可调用名）；列表字段 + `triggers` / `dependencies` / `source` / `metadata`，无值字段可省略；未知名称返回 404 `{ error: { code: "skill_not_found", message } }` |
| 其余 `/web/api/*` | 统一返回 `{ status: "ok" }`（POST 类操作直接成功） |

注意事项：

- 页面会连 `GET /web/api/events`（SSE）。静态桩返回 JSON 会导致 EventSource 报错并每 2 秒重连，页面功能不受影响，可忽略。
- 浏览器自动化时可用 `page.addInitScript` 提前 stub `window.EventSource` 消除重连噪音。

这样可以在无真实 provider 的机器上测试全部前端交互，并可自由构造边界数据（空协议 provider、空模型列表、超长字段值等）。

## 2. 手工回归清单

### 2.1 全局

- [ ] 七个页签（对话 / **技能** / 日志 / 配置 / 缓存 / 调试 / 关于）切换正常，`«` 折叠侧栏、`◐` 主题切换生效；窄屏下页签可换行且按钮不被压得过窄。
- [ ] **调试页签**：进入时拉取 `GET /web/api/status?format=text` 并原样展示状态文档（与 `aicli /debug` 命令显示的内容一致），
      等宽字体、可横向/纵向滚动；工具栏「⟳ 刷新」重新拉取，「JSON 快照」链接另开 `/web/api/status` 原始 JSON；
      后端不可用时显示"加载失败：…"且不残留旧内容。
- [ ] **关于页签**：显示客户端名 `aicli micro web client`、一行说明与页签清单/端点链接；
       「写令牌」行显示当前 `X-AICLI-Token`（与启动行/`GET /web/api/token` 一致，每进程随机），
       「复制」按钮写入剪贴板并弹「写令牌已复制」提示；
       「当前会话」行显示完整会话 ID（`GET /web/api/sessions` 的 `current_session_id`，
       切换/新建/恢复会话后自动更新），「复制」按钮写入剪贴板并弹「会话 ID 已复制」提示；
       未选择会话时显示「（未选择会话）」且不复制（不把占位文案贴进终端）；
       「调试速览」行给出四个入口（`/debug/chat/status`、`/web/api/screen?view=tui&tail=N`、
       `POST /web/api/invoke` 含 `wait_only`、写令牌与文档指针），与 `?format=text` 末尾
       「Debug 使用说明」同一口径；
       「远程调用端点」清单进入页签时拉取 `GET /debug/endpoints?format=json` 并按
       web / loopback / runtime-observe 分组渲染 `方法 + 路径 + 说明`，写操作（POST）带「需令牌」标记，
       不可用时显示原因（启动早期会自动重试数次）；与 `/debug/endpoints?format=text`、`aicli /debug`
       的「HTTP 调试端点」区块同源，新增端点无需改前端。
- [ ] 顶栏布局：最左侧依次为 `☰`（折叠会话列表）、主题切换图标、连接状态、轮次状态、
      发送瞬态提示；会话标题居中显示（窗口缩放/侧栏折叠后仍保持居中，
      长标题按省略号截断且不撑破顶栏）；会话 ID 不占顶栏，改在「关于」页签展示。
- [ ] 顶栏执行状态不重复：运行中只由「轮次状态」显示“处理中”（不再附带 provider/model），
      `#send-status` 只承载发送中/已排队/停止中/失败等瞬态文案。
- [ ] SSE 断连时顶部显示"已断开，重连中…"，恢复后消失。
- [ ] 顶栏只显示当前会话标题（会话 ID 在「关于」页签）：初始（无会话）显示"未选择会话"；
      切换会话、新建会话、重命名当前会话、刷新列表后，顶栏标题与关于页签的会话 ID
      均同步更新；长标题截断省略且悬停（title 属性）可见完整值。
- [ ] 底部栏只显示状态栏（`#status-bar`：balance / context / 目录 / git 分支 / window 等段 +
      刷新按钮），不再显示 `/debug/endpoints` 与 `/web/` 链接；调试端点入口见「关于」页签。
- [ ] **切换会话确认弹窗**：点击非当前会话先弹出确认框（显示目标会话标题，悬停可见
      会话 ID）；「取消」/`✕`/遮罩空白处/`Esc` 关闭后不发送 resume 请求；点「切换」
      后正常完成切换（顶栏与列表同步）。当前会话高亮项点击不弹窗（直接走
      already_current 刷新）。当前会话有任务进行中（发送中/执行中/正在停止）时，
      弹窗内出现"切换将在当前任务与排队输入完成后生效"提示。
- [ ] `Esc` 打开/关闭快捷键帮助；面板为不透明卡片（深色 `#1a222c` / 浅色 `#ffffff`，
      取自 `--modalBg`），不得透出底层页面内容。
- [ ] **窄屏（≤767px）布局**：顶栏两行——第一行「菜单栏（左）+ 状态簇（右，右对齐）」、
      第二行居中会话标题（≤420px 时菜单栏独占第一行，标题靠左、状态簇靠右）；`文件/视图/帮助`
      下拉面板按顶栏宽度展开且不越出视口，菜单项可折行、点按高度 ≥33px；会话列表抽屉整高
      覆盖内容区（不再上下留白）；底部状态栏单行横向滚动（不折成多行、滚动条隐藏）；
      整页无横向滚动条（`document.documentElement.scrollWidth === innerWidth`）。

### 2.2 对话页 + 底部配置栏（cfg-bar）

- [ ] 对话区工具消息（`role: tool`）的输出默认折叠，仅显示约 5 行；展开/收起控件并入「工具」抬头行内
      （文字「展开」+ 向下图标 ▼；展开后变为「收起」+ 向上图标 ▲），点击抬头即可切换，键盘
      `Enter`/`Space` 等效。内容过短（不溢出）时抬头只显示「工具」纯文本，控件隐藏且不可点击。
      会话复制（⧉ 复制）提取完整工具输出文本，不含抬头控件文字与 ▼/▲ 图标。

- [ ] **长会话窗口化**（消息懒加载）：会话超过 40 条消息时，首屏只渲染最新一页（`msg_limit=40`），
      对话区顶部出现「↑ 上滚加载更早消息」提示行；上滚到顶部附近自动加载更早一页（`msg_before` 游标，
      请求中提示变「加载更早消息…」），插入后视野停在原处不跳动；加载到最早一条后提示行消失，
      再上滚不再发请求。实时刷新/流式回合结束后已加载的更早内容不丢失、不重复。
      会话复制（⧉ 复制）在只加载了部分消息时仍复制**完整**会话（服务端全量 transcript）。

- [ ] **assistant 消息 md|txt 渲染**：每条 assistant 气泡右上角有 `md` / `txt` 二选一控件，
      默认 `md`（Markdown 渲染）；点 `txt` 切回纯文本（Markdown 标记原样显示）；再切 `md`
      立即恢复渲染；反复切换不丢内容、不叠加、不重解析。Markdown 渲染支持标题 / 粗体 /
      删除线 / 列表 / 任务列表 / 引用 / 表格 / 代码块，代码块悬停出现「复制」按钮且可复制；
      块级排版与 TUI 侧一致（块之间恰好一个空行，列表项之间、引用块内紧凑，文档首尾不留空行）：
      源码里连写多个空行收敛为一个，也不会在块级元素（标题/列表/引用/表格/代码块）前后多渲染空行；
      切换只作用于该条消息（`data-render-mode` 属性驱动 CSS 显隐，无内联样式）；
      实时流式气泡固定按 Markdown 渲染，不参与切换。会话复制（⧉ 复制）仍取 `.msg-text`
      原文，不含代码块「复制」按钮文字。

- [ ] **单条消息复制（所有角色）**：每条消息（你 / aicli / 推理 / 工具 / 系统 / 命令 / 诊断 /
      事件）抬头行最右都有 `⧉` 复制图标，点击**只复制该条消息的正文**：不含角色标签、控件文字
      （`md|txt`、展开/收起）与相邻消息。assistant 行复制 `.msg-text` 原文（切到 `md` 后不会
      复制到渲染产物或代码块「复制」按钮文字）；推理行复制内容不带 `[推理] ` 前缀（那是会话
      复制的语义标注，单条复制保持原文）；工具行复制完整工具输出。复制成功后图标短暂变 `✓`。
      生成中的流式气泡右上角同样有复制图标，复制的是**已累积的完整**助手文本（不是打字机
      当前已揭示的部分）。会话复制（⧉ 复制）行为不变，两者共存。

- [ ] Provider / Reasoning 原生 `<select>` 可切换，当前生效配置（`openai · gpt-4o`）随之更新。
- [ ] **浮动 composer 面板（任意页签可用）**：输入区 / 配置栏 / 动态状态条在 `.layout` 内的浮层
      `#composer-panel`（停靠态 `position: absolute`，自由拖动改 `fixed`，见 `js/composer.js`），
      切到技能 / 文件 / GIT / MCP / 日志 / 配置 / 缓存 / 分析 / 调试 / 关于任一页签都仍在
      （旧结构挂在 `#tab-main` 内，切页签整块消失）。标题行左侧把手 `⠿` 可拖动（自由位置落盘）、
      `⇲` 复位回停靠位——**`⇲` 只在面板离开原位（自由拖动后）时出现**，停靠态它无事可做、由
      CSS 按 `data-composer-mode` 隐藏（键盘仍可用把手 `Home` / 双击复位）；折叠按钮或
      `Ctrl+J`（macOS `Cmd+J`）收起为单行，「视图」菜单同一入口；
      **面板与状态栏是两个互不影响的图层**：停靠时面板停在状态栏上方 8px（居中），
      `#footer` 永远贴底、不因面板让位或移动（也绝不写 `--composer-reserve` 之类的页面级变量）；
      位置与折叠态记在 `localStorage`（`aicli.web.composer.v1`，隐私模式静默降级）；
      把手可聚焦，方向键微调 8px、`Shift+方向键` 1px、`Home` 复位。
      回归：`scripts/verify-micro-web-composer.mjs`（沙盒）+ 真实浏览器临时脚本
      `backend/cmd/aicli/commands/web/tmp/composer-layer-check.mjs`（断言状态栏坐标零变化）。
 - [ ] **任务列表浮层（贴在 composer 面板上沿）**：模型调用 `todos` 工具后，`#todo-panel`
       （`#composer-panel` 内的绝对定位子层，`bottom: calc(100% + 1px)`）在 composer **上面**
       展开，**底边与 composer 面板上沿严丝合缝**（左右各外扩 1px 对齐外边框、只有上圆角、
       不画底边），因此停靠 / 自由拖动 / 折叠 composer 时都随它一起移动，不需要 JS 同步几何，
       也不吃页面任何元素的高度；无快照时 `hidden` 不占位。标题行给出「已完成 N · 进行中 N ·
       待处理 N」计数、进度条（`role=progressbar`）与当前进行项的执行态文案；标题行**最右端**的
       折叠按钮 `▾/▸` 收起/展开（只收列表，计数保留），折叠态记 `localStorage`（`aicli.web.todos.v1`）。
       数据两条通道：SSE `tool_end.todo_snapshot`（实时，按 `_event.sequence` 单调推进）与
       `GET /web/api/screen?format=json` 的 `todo_snapshot`（刷新页面 / 会话切换后回放兜底，
       不覆盖实时值）；会话开始/结束/切换先清空面板，等新会话事件或回放。
       回归：`scripts/verify-micro-web-todos.mjs`（沙盒 + 静态契约）+ 真实 Chromium 几何探针
       `backend/cmd/aicli/commands/web/tmp/todo-panel-geometry.mjs`（1280×800 / 390×844：
       衔接 gap ≤1px、左右与宽度对齐、折叠与自由位置下仍衔接、无横向滚动条）。
- [ ] **窄屏 composer（≤767px）**：输入框与发送键同排，发送键右对齐且贴底（`#prompt` 多行增高时
      按钮不跟着拉高）；`#prompt` 与 `#cfg-model` 字号 ≥16px（低于 16px 时 iOS 聚焦会放大整页）；
      可点按控件高度 ≥44px；输入框空态回到 CSS 最小高度（占位提示折行不再把空输入框撑成两行，
      `autoGrow()` 在空值时清掉内联高度）；占位提示在窄屏切短文案（`enterkeyhint="send"`，
      手机键盘回车键显示「发送」）；底部按 `env(safe-area-inset-bottom)` 让出手势条；
      配置栏窄屏折叠为单行按钮 + 向上弹出的面板（面板绝对定位覆盖在正文之上，不挤压正文、
      不触发 ResizeObserver 重排；三个选择器在面板内各占一行，模型输入框解除桌面 150px 上限）；
       配置文案（provider · model · reasoning）在窄屏由 ⚙ 触发按钮承载（超长省略号），
       桌面**不在配置栏底部重复**（原 `#cfg-current` 已删除：三个选择框已经把它显示全了，
       只保留标题行 `#composer-summary` 供折叠后查看）；
      点面板外或按 Esc 收起，`aria-expanded` 同步；面板内模型列表限高 `min(240px, 40vh)`
      避免顶部越出视口；横屏矮视口（高 ≤480px）只收紧输入框上限与页脚留白。
- [ ] **桌面（>767px）不受上述折叠影响**：`.cfg-controls{display:contents}`、`#cfg-toggle{display:none}`，
      三个选择器仍平铺在 `#cfg-bar` 一行内（底部不再有重复的配置文案，`#cfg-status` 靠
      `margin-left:auto` 保持右对齐），cfg-bar 高度保持约 36px。
- [ ] Model 字段：直接输入自定义模型名可生效；点 ▼ 弹出全量模型列表，**向上展开**（`bottom: calc(100% + 4px)`），当前模型高亮 + "当前"徽标、默认模型带"默认"徽标；徽标显示"共 N 个"。
- [ ] Model 输入框聚焦/输入时**不应**出现原生 datalist 下拉（`list` 属性已移除，避免与自定义 popup 叠成双层）。
- [ ] 点击 popup 外部或按 `Esc` 关闭 popup；点选后立即应用并关闭。
- [ ] **web↔TUI 切换契约**：底部栏切换注入的 `/model ...` 命令必须带 `--direct`
      （见 `js/runtime.js` 的 `applyRuntimeConfig`）。回归方法：web 端切换
      provider/model 时，观察同会话的 aicli chat TUI——应只打印切换结果，
      **不得**弹出全屏 provider/model/reasoning 选择器；若 TUI 卡在全屏选择
      器、web 端显示"已提交（配置可能未同步）"，即 `--direct` 链路被破坏。

### 2.3 配置页

- [ ] Provider 表格：名称/协议/状态/默认模型/模型数列与配置一致，分页、搜索、排序、"每页条数"正常。
- [ ] 行操作：编辑、启用/禁用（带确认）、删除（带确认）均正常，操作后列表刷新。
- [ ] **Provider 编辑弹窗**（见 2.4 专项）。
- [ ] **协议下拉框专项**（见第 3 节，历史 bug 回归重点）。
- [ ] 自动导入弹窗：协议为原生 `<select>`（含"自动探测"），导入结果表格化展示，完成后 toast 并刷新列表。
- [ ] 弹窗可拖动（标题栏）、可缩放（右下角）、尺寸记忆；关闭后协议 popup 不残留。

### 2.4 Provider 编辑弹窗

- [ ] 名称：编辑已有 provider 时只读，新增时可输入。
- [ ] API Key：明文不回传，输入框始终为空；状态行按凭据来源显示（Key Store / OAuth / 密钥池 / 内联 / 未配置）+ 掩码回显；已保存时显示"清除"按钮。
- [ ] Base URL / API Path / 转发 URL / 默认模型：回显与保存一致。
- [ ] 支持模型 textarea："获取模型列表"按钮调 `POST /web/api/config/providers/fetch-models`，**整体覆盖**原支持模型列表（去重保序）；网关未返回可合并模型时保留原列表并提示。
- [ ] Reasoning 编辑器：获取模型列表后按 `model_metadata`（`/models` 元数据 → model card → 协议默认值重匹配结果）整体覆盖各模型 reasoning 配置，未命中元数据的模型清空旧配置；保存模型列表后按模型逐行生成。
- [ ] 覆盖仅作用于表单草稿：获取后不点保存不落盘；旧模型 ID / 旧 reasoning 草稿不再保留。
- [ ] 保存后 payload 中各字段值与表单一致（可在 DevTools Network 面板检查 `POST /web/api/config/providers`）。

### 2.5 缓存分析页

- [ ] 总览卡片：请求总数 / 缓存命中率 / 缓存写入率 / 缓存读取 tokens / 缓存写入 tokens / prompt tokens / **输出 tokens** / **合计 tokens** / **推理 tokens** 均有值；与同会话 `aicli chat` 的 `/usage cache` 总览逐项一致（同一 `cacheanalytics` Source，前端只做展示）。
- [ ] 请求明细表格（共 10 列）：时间 / provider/model / step / 状态 / 缓存 / 命中率 / prompt / **输出** / 读缓存 / 写缓存；`not_reported` 行的缓存列显示 `--`。
- [ ] 点击明细行：详情面板 token 段含 `prompt tokens` / `completion tokens` / `total tokens` / `缓存读取` / `缓存写入` / `reasoning tokens`。
- [ ] 消息追溯：`产出请求` 下方的 `产出用量` 行显示 `prompt … · 输出 … · 读缓存 … · 写缓存 …`；无产出请求时显示 `-`。
- [ ] 边界：空会话显示"暂无 LLM 请求记录（发送一条消息后刷新）"；旧记录缺 usage 时显示 `-`，不得出现 `NaN`/`undefined`。

### 2.6 技能页签（会话的第二页签）

数据源与 TUI `/skills` 同源：列表取 `GET /web/api/skills`（当前会话 `FunctionCatalog` 的 skill 描述符），
点击条目现取 `GET /web/api/skills/{name}` 打开详情面板（不复用列表快照）。

- [ ] 列表：进入页签拉取目录并显示「共 N 个」；每个条目显示 skill 名、可调用名（`skill__*`）与描述；
      无 skill 时显示"当前会话没有可用的 skill"（不是空白）。
- [ ] 会话感知：同一会话内重复切页签**不重复请求**；切换会话后（后台切换）再进页签**强制刷新**；
      页签可见时切换会话立即重拉；快速切换会话时迟到的旧响应被丢弃，不覆盖当前目录。
- [ ] 详情面板：点击条目打开面板（portal 挂在 `body` 上，层级在设置域弹窗之上），标题为 skill 名，
      头部展示后端返回的 `kind` / `category` / `version` 徽标（**无值不出徽标**，不得回落到列表快照里的值）。
- [ ] 详情分组页签：内容按「概览 / 触发 / 依赖 / 来源 / 元数据」分组显示，**只有后端有值的分组才生成页签**
      （无内容时整条页签栏不占位）；默认选中第一个页签且**只渲染当前页签的内容**（其余分组不预先渲染）。
      切换方式：点击页签，或焦点在页签上按 `←` / `→`（首尾环绕）、`Home` / `End`；打开面板时焦点落在选中页签，
      关闭后焦点回到触发的列表条目。
- [ ] 分字段渲染：目录名 / 可调用名 / 类型 / 分类 / 版本 / 描述 / 标签 / 能力渲染为字段行或标签徽标；
      触发词 / 依赖 / 来源 / 元数据按「对象 → 键值行、对象数组 → 卡片、嵌套值 → JSON」展示；
      后端未返回的字段**整行省略**（不出现占位默认值）。
- [ ] 页签栏样式：只允许横向滚动，页签栏内**不出现上下滚动条**。两条硬约束 ——
      ① 页签栏 `overflow-x: auto` 必须同时显式写 `overflow-y: hidden`（只有一个轴不是 `visible` 时，
      另一轴的 `visible` 会按规范计算成 `auto`，页签栏会静默变成纵向滚动容器）；
      ② 页签**不得溢出页签栏的 padding box**（选中下划线画在按钮内部，不能用 `margin-bottom: -1px`
      去压页签栏外边框）。真机上只要页签多出 1px，①就会把它渲染成一条滚动条。
- [ ] 关闭方式：`✕`、点遮罩、`Esc` 均关闭面板；面板打开时 `Esc` 只关面板，**不触发**会话中断；
      关闭后迟到的详情响应不再写入面板。
- [ ] 失败如实显示：无活动会话时列表显示 `加载失败: skills_unavailable`；未知 skill 详情显示 `加载失败: skill_not_found`；
      网络异常显示 `加载失败: network_error`；失败时列表不残留旧条目、面板不静默关闭。
- [ ] 刷新：工具栏「⟳ 刷新」重新拉取列表（不依赖会话是否变化）。

### 2.7 网格窗口（会话列表「⧉ 在新窗口打开」+ 深链）

数据源：`POST /web/api/mesh/spawn`（架构 §5.7，契约见
[web-remote-api.md](web-remote-api.md) §9.6）。前置：两个 `aicli chat`/`resume --pprof --mesh`
进程（或同一进程即可覆盖「复用」路径），DevTools 打开 Network 与 Application 面板。

> 状态（2026-09-24 回填）：`⧉` 流程、深链与 spawn 端点**已落地**（S9）；侧栏徽标 / 端点行 /
> 跨工作区分组 / 打开方式开关 / resume 冲突弹窗**已落地**（S11，见 §2.7.1）；
> 「实时徽标」**已落地**（S12：前端订阅 `mesh/events`，退避重连 + 轮询兜底，见 §2.7.2）；
> 窗口标题（会话标题 + 节点后缀）与 spawn `refused` 文案**已落地**（节点后缀 S13、
> 会话标题段 2026-09-25，见 §2.7.3）→
> 本节各条均可执行。权威状态表见 `docs/plan/aicli-micro-web-client-session-window-plan.md` §0.1。

- [ ] **弹窗资格**：悬停会话 → 点 `⧉` → 新窗口**必须**打开（不是被拦截的提示条）。
      实现要点：占位窗口在点击手势内同步 `window.open('', '_blank')`，spawn 返回后才 `location.replace`；
      若改成「await 之后再 window.open」，Chrome 会拦截——这是本用例的回归重点。
- [ ] **复用不新起进程**：同一会话连点两次 → 第二次响应 `status:"reused"`（Network 面板可见），
      进程数不增加（`aicli-mesh ls --json` 的 `counts.live` 不变），新窗口地址端口与第一次相同。
- [ ] **失败关窗**：把 `--mesh-allow-spawn=false` 的进程当目标（或断网/改坏 `session_id`）→
      占位窗口**自动关闭**（不残留 `about:blank` 标签页）+ Toast 显示 `code — reason`
      （如 `mesh_spawn_not_allowed — spawning is disabled`）。
 - [ ] **无令牌残留（M7）**：新窗口地址栏在加载后**不含** `token=`（只剩 `?session=<sid>`），
       `Application → Local Storage / Session Storage` 里没有 `aicli-web-token` 之外的令牌副本，
       页面 DOM 中无令牌原文；`history.length` 回退一步也看不到带令牌的 URL。
 - [ ] **刷新回读（非回环导航 cookie）**：以 `--web-host 0.0.0.0` 启动、用局域网 IP 打开带 `?token=` 的地址
       （加载后地址栏已被脚本抹成 `/web/`），按 **F5** → 页面照常渲染（**不出现** 403 JSON 页）、
       `/web/api/status` 200；`Application → Cookies` 可见 `aicli_web_token`（**HttpOnly**、`SameSite=Strict`、
       会话级无 Expires）。再删除该 cookie 后 F5 → 403；无凭据 `curl` 复查 `GET /web/api/status`（GET API）
       与 `POST /web/api/invoke`（写方法）即使带 cookie 也必须 403（cookie 只放行页面导航）。
       进程重启换随机令牌后旧 cookie 失效 → F5 回到 403，用启动行带 `?token=` 的地址重开一次即恢复。
- [ ] **深链对齐**：手动打开 `http://127.0.0.1:<port>/web?token=<t>&session=<sid>` →
      页面正常加载（首个 `/web/api/sessions` 请求已带 `X-AICLI-Token`），列表高亮该会话；
      若把 `session` 改成另一个存在的会话 → 自动走 `/web/api/sessions/resume` 切换；
      不存在的会话 id → Toast「深链会话不存在」，页面不白屏。
- [ ] **实时徽标**：新窗口连上后，原窗口会话列表的「当前 / 活节点」状态与
      `mesh/peers` 在 ≤2s 内反映新进程（SSE 扇入，见 [web-remote-api.md](web-remote-api.md) §9.4）；
      在另一个窗口发一轮 prompt → 原窗口徽标翻成 `◐ 忙碌 @host:port`，回合结束后回落
      `● 运行中`。全程**无需手动刷新页面**（S12；细化清单见 §2.7.2）。

#### 2.7.1 侧栏网格视图与 resume 冲突（S11）

前置：两个 `aicli chat --pprof --mesh` 进程（**不同工作区**，用于覆盖分组），DevTools 打开 Network；
在其中一个进程的 web 页操作。数据源：`GET /web/api/sessions?scope=all`（契约见
[web-remote-api.md](web-remote-api.md) §9.7）。

- [ ] **徽标 + 端点行**：会话条目第三行显示「工作区 · 状态徽标 · 节点短后缀」；徽标与
      `sessions[]` 的 `session_state` / `ownership` / `conflict_count` 逐条对应
      （`running` / `busy` / `idle` / `unknown`，`peer` / `conflict`），且**形状 + 文本双编码**
      （不靠颜色单独区分，色盲可用）。
- [ ] **同源**：条目 `endpoint.node_id` 与 `GET /web/api/mesh/peers` 的 `nodes[].node_id` 一致；
      对端进程退出（或 `aicli-mesh stop`）后条目回落 `idle` / `unknown`，端点行显示 `last_known`
      （「上次 @host:port」），**不残留**「运行中」。
- [ ] **跨工作区分组**：另一个工作区的会话落在「其他工作区（N）」分组里，展开/折叠可切换且刷新后记忆；
      分组只改变呈现，不改变会话集合（本工作区会话仍在主列表）。
- [ ] **打开方式开关**：侧栏「打开方式」切到「当前进程切换」→ 点条目走 `POST /web/api/sessions/resume`；
      切回「新窗口打开」→ 点条目走 `POST /web/api/mesh/spawn`（Network 可见）；
      刷新页面后开关值保持（`localStorage: webSessionOpenMode`）。
- [ ] **resume 冲突三段式**：点「正被另一个活节点服务」的会话 → 弹窗给出
      「打开那个窗口（推荐）/ 仍在本进程切换 / 取消」（`status=running_elsewhere`）；
      选「打开那个窗口」→ 新窗口落在对端端口；选「仍在本进程切换」→ 请求体带 `"force":true` 且切换成功。
- [ ] **conflict 只读**：造出两个活节点声称同一会话（或直接改节点档案）→ 弹窗列出冲突节点
      （node_id / pid / workspace），「仍在本进程切换」**禁用**并提示 `aicli-mesh doctor`，
      「打开那个窗口」隐藏；取消后列表状态不变。
- [ ] **降级（网格关闭）**：以 `--mesh=false` 启动 → 列表仍是旧视图（无徽标、无分组）；
      `GET /web/api/sessions` 的 `self=null`、`workspaces=[]`、`endpoint` / `last_known` 全 `null`；
      resume 不做归属检查（`queued`）；页面不报错、不出现空分组。
- [ ] **关于页网格小节**：切到「关于」→ 只读展示 `node_id` / `mesh.root` / `counts` / 建议命令；
      **没有** gc / stop / spawn 按钮；网格关闭时显示降级文案而不是报错。

#### 2.7.2 网格实时刷新与降级（S12）

前置：两个 `aicli chat --pprof --mesh` 进程（不同工作区）；在 A 的 web 页操作，DevTools 打开
Network（筛 `mesh/events`）与 Console。数据源：`GET /web/api/mesh/events`（SSE 扇入，契约见
[web-remote-api.md](web-remote-api.md) §9.4）；前端行为 = Web 子方案 §5.6。

- [ ] **订阅建立**：页面加载后 Network 出现一条 `mesh/events` 请求（`type=eventsource`，**pending** 不结束），
      EventStream 面板首帧为 `mesh.ready`（回显 `since_seq` / `peers=auto` / `clients`）；
      网格关闭（`--mesh=false`）时**没有**这条请求（前端不订阅、不轮询）。
- [ ] **实时翻转（≤2s）**：在 B 的窗口发一轮 prompt → A 的侧栏在 ≤2s 内把 B 的会话徽标翻成
      `◐ 忙碌 @host:port`，回合结束后回落 `● 运行中`；A 自己的条目不变（定向投递，不是全量抖动）。
- [ ] **节点上下线**：再起一个 C 进程 → A 的列表与「其他工作区（N）」计数自动出现 C 的会话；
      退出 C（或 `aicli-mesh stop`）→ 条目回落 `idle` / `last_known`（「上次 @host:port」），计数减一。
      全程不手动刷新。
- [ ] **节流**：B 连续跑多个工具调用 → A 的 `sessions?scope=all` 请求被合并（200ms 窗口内多帧只拉一次），
      侧栏不出现逐帧重排的闪烁。
- [ ] **退避重连 + 续传**：DevTools 勾 Offline 再取消（或临时断网）→ `mesh/events` 断开后按
      1s→2s→4s…（≤30s）重连，重连 URL 带 `?since_seq=<最后收到的 seq>`；恢复后徽标继续实时。
- [ ] **轮询兜底（降级）**：断开期间 `sessions?scope=all` 每 10s 出现一次（Network 可见）；
      订阅恢复（`mesh.ready`）后该轮询停止；SSE 完全不可用时徽标仍随轮询更新（不实时但不失能）。
- [ ] **`mesh.lagged` 全量兜底**：EventStream 面板出现 `mesh.lagged`（缓冲溢出跳号）→
      立刻看到一次 `sessions?scope=all` 全量拉取，列表与 `mesh/peers` 重新一致。
- [ ] **双流独立（R11）**：`/web/api/events`（本进程）与 `mesh/events`（扇入）各自独立连接与退避；
      断开其中一条不影响另一条（本窗口的流式输出照常）。
- [ ] **无令牌残留（红线）**：回环模式下 `mesh/events` 的 URL **不带** `token=`；非回环模式
      （`--web-host 0.0.0.0`）下只带**本进程**令牌；`sessions.js` 源码与 `localStorage` /
      `sessionStorage` 中都没有 peer 令牌（M7）。

#### 2.7.3 P2 打磨：窗口标题（会话标题 + 节点后缀）与 refused 文案（S13 / 2026-09-25）

前置：两个 `aicli chat --pprof --mesh` 进程（不同工作区）。

- [ ] **标题会话 + 节点后缀**：窗口标题为
      `[●|…|✗ ]<会话标题> · aicli micro web client · <工作区> · <节点短 id>`（`node_id` 前 8 位）。
      会话标题取自 `/web/api/sessions` 的当前会话（`(untitled)` 显示 `(未命名会话)`，超 40 字符
      截断为 `…`）；两个窗口并排时先看会话、再看归属（`·` 后两段与各自 `GET /web/api/mesh/self` 一致）。
- [ ] **会话变化即重算**：侧栏切换会话（或终端 `/resume`）→ 标签页标题立即换成新会话标题；
      在侧栏重命名当前会话 → 标题同步更新；新建会话 → 标题变为新会话标题（未命名时为
      `(未命名会话) · aicli micro web client`）。以上都不需要手动刷新页面。
- [ ] **降级不加后缀**：以 `--mesh=false` 启动（或 mesh 根不可读）→ 标题为
      `<会话标题> · aicli micro web client`，**不出现**空占位（没有悬空的 ` · `）；网格可用后
      标题自动补齐后缀，无需手动刷新页面（`self` 段到达即重算）。
- [ ] **refused 文案（策略拒绝）**：对端以 `--mesh-allow-spawn=false` 启动，在它的窗口点会话主点击
      （或 `⧉`）→ Toast 为「打开新窗口失败: mesh_spawn_not_allowed — 本节点已关闭 spawn
      （--mesh-allow-spawn=false）；改用 CLI：aicli-mesh open <session> --print-url」，
      **不**在前端重试（Web 子方案 §5.2 回退路径）。
- [ ] **失败态诊断命令**：让 spawn 失败（例如把会话工作区目录移走 → `mesh_workspace_missing`）→
      Toast 带「诊断：aicli-mesh show <session>」，可直接复制执行。
- [ ] **收敛开关文案（前瞻项）**：`mesh_cross_workspace_denied` 目前只在 CLI/Agent 面的 `mesh/call`
      上触发（前端不用 `call`，Web 子方案 §6.1），浏览器侧无需复现；只确认
      `sessions.js::SPAWN_CODE_TEXT` 含该 code（由 `web_handlers_mesh_polish_test.go` 锁定）。

#### 2.7.4 会话切换事件化与断连兜底（S14）

前置：一个 `aicli chat --pprof --mesh` 进程；DevTools 打开 Network（筛 `web/api/events`）与 Console。
数据源：SSE `session_switched`（服务端合成：handler 每 250ms 看 `current_session_id` 变化，
契约见 `web_handlers.go::chatWebSSESchema()`；实施计划 §22）。

- [ ] **切换即时刷新**：在侧栏点另一个会话 → 输入区状态先显示「已切换，刷新中…」，随后
      `session_switched` 到达（Network 里该帧 `data` 含 `session_id` / `previous_session_id`），
      侧栏、屏幕与顶栏标题一次性对齐到目标会话；状态行显示「会话已切换」。旧实现要等最多
      2.4s（8×300ms 轮询），现在应在 ~250ms 内完成。
- [ ] **终端发起的切换也会刷新**：在**终端**里输入 `/resume <id>`（或 `/new`）→ 网页窗口同样
      收到 `session_switched` 并刷新（旧实现完全看不到终端发起的切换）。
- [ ] **无轮询残留**：切换后 Network 里**没有**连续的 `/web/api/sessions?...&scope=all` 轮询
      （只剩事件到达后的一次重拉）。
- [ ] **断连兜底**：DevTools 里断开 SSE（Network 面板右键 `web/api/events` → Block request URL）
      后再点切换 → 4s 后仍会重拉列表并显示「已切换(状态未同步)」，页面不卡死。
- [ ] **新建按钮恢复**：点「新建会话」→ 切换完成后按钮恢复可用（S14 起由 `session_switched`
      分支调用 `notifySessionSwitchedCompleted()`；SSE 断连时由兜底定时器恢复）。

### 2.8 审批 / 提问模态框（反向交互）

前置：真实后端（方式 A）；分别触发一次工具审批与一次 `AskUserQuestion`。
无真实 provider 时也可走方式 B：`web/tmp/serve-question-answer-e2e.mjs`（gitignore 的本地沙盒）
静态伺服 `web/` 并 stub `/web/api/*`，`/__recorded` 回显收到的 `/web/api/input` payload。

- [ ] **提问必须能写入答案**：`question_asked` 到达时模态框内出现回答输入区（建议项下方，自动聚焦），
      输入文本按 Enter 或点「提交回答」→ `POST /web/api/input` 的 payload 为
      `{"type":"question_answer","question_id":"…","answer":"…"}`，模态框收起。
      修复前模态框内没有任何文本输入框，而遮罩层盖住了底部 composer，开放型提问**无法写入答案**。
- [ ] **建议项是快捷入口**：点建议项直接作为答案提交，与自由输入走同一 payload。
- [ ] **空答案不发送**：输入全空白按 Enter → 只在输入区内提示「请输入回答」，不产生请求、模态框不收起。
- [ ] **换行与 IME**：Shift+Enter 换行；中文输入法组合态按 Enter 只确认候选词（`isComposing` /
      `keyCode 229`），不提交。
- [ ] **收起对话框不丢答案路由**：点 ✕ / 遮罩空白 / 输入区 Esc → 模态框收起并提示「可在底部输入区…」，
      随后在底部输入区输入并回车仍能作为答案提交（`question_answer`）。
- [ ] **审批语义不变**：审批模态框不显示回答输入区，收起即放弃本次决议（决议仍可在终端完成）；
      允许/拒绝按钮 payload 为 `{"type":"approval","request_id":"…","allow":true|false}`。
- [ ] **未送达必须可见（stale）**：提问在服务端已无挂起项（已被其它入口回答 / 本轮已终止）时提交回答，
      服务端回 `{"status":"stale","reason":"…回答未送达模型"}`（不再冒充 `resolved`），前端弹错误提示并写状态行，
      绝不显示「已提交」——静默成功会让「提交了回答但没有回传 LLM」变成无感故障。

### 2.9 文件页签（文件管理器）

数据源：`/web/api/fs/*`（契约见 [web-remote-api.md](web-remote-api.md)「文件与 Git 浏览」），
与 runtime-server 文件浏览器共用 `internal/filebrowse`；作用域根为**当前会话工作目录**。
前置：真实后端（方式 A），会话已绑定工作目录且目录内有子目录 / 文本 / 二进制文件。

- [ ] 进入页签：拉取 `GET /web/api/fs/roots` 并填充根下拉（当前会话工作目录），列表显示条目 +
      「共 N 项」计数 + 路径面包屑；空目录显示空态文案（不是空白）。
- [ ] 导航：点目录行进入下一层；「⬆ 上级」返回上一层（根目录时禁用）；点面包屑任意层级跳转；
      面包屑与列表始终同源（不出现「路径显示 A、列表是 B」）。
- [ ] 排序与隐藏项：切换排序 / 勾选「隐藏项」后重拉列表（服务端排序），勾选后能看到点开头的文件。
- [ ] 分页：「加载更多」按 `cursor` 追加，不重复条目、不丢条目；追加失败时不破坏已加载列表。
- [ ] 打开文件：点文件行**不开弹窗**，在该页签栏新建（或复用）一个二级页签就地显示（见 2.11）；
      文本 / 图片 / 二进制 / 过大四种 `kind` 各有明确文案（不出现空白面板）；文本被截断时提示 `truncated`。
- [ ] 下载：页签面板的「⤓ 下载」触发浏览器落盘，响应为 `Content-Disposition: attachment`；大文件下载期间
      其它页签请求不被阻塞。
- [ ] 搜索：输入关键词回车（或点 🔍）走 `GET /web/api/fs/search`，列表切换为搜索结果并显示扫描/截断信息；
      点 `✕` 清除搜索回到目录浏览；搜索结果里点文件同样在新页签里打开。
- [ ] 会话感知：同一会话内重复切页签**不重复请求**；切换会话后（含后台切换）再进页签**强制刷新**，
      列表与根下拉都换到新会话的工作目录；快速切换会话时迟到的旧响应被丢弃。
- [ ] 失败如实显示：无活动会话 / 会话无工作目录时显示 `加载失败: scope_has_no_root`（不回落进程 cwd、
      不显示旧目录内容）；网络异常显示 `加载失败: network_error`；失败时列表不残留旧条目。

### 2.10 GIT 页签（git 管理器）

数据源：`/web/api/git/*`（同上契约），与 runtime-server git 查看器共用 `internal/gitbrowse`；
作用域根与「文件」页签同一口径（同一个会话工作目录）。

- [ ] 进入页签：`GET /web/api/git/status` 渲染仓库标签（分支 / 干净或脏）与变更分组
      （冲突 / 已暂存 / 未暂存 / 未跟踪 / 重命名）；干净仓库显示明确空态。
- [ ] 侧栏子页签（变更 / 提交记录）：左栏一次只显示一条列表（不再上下堆叠），页签上有条数
      （变更 = 各分组条目数之和，提交 = 已加载条数、`has_more` 时带 `+`，加载中不显示旧数字）；
      切换是**纯客户端状态**——不发请求、不重建列表；`←/→` 环绕、`Home/End` 到两端，且 Tab
      只落在当前页签上（roving tabindex：两条都能 Tab 到就是退化）；大屏折叠成窄轨时页签栏一起收起。
      大屏下页签栏在 sticky 块内部（`.git-side-bar` 的最后一行），左栏滚到列表深处它仍在视野里——
      一滚就没说明页签栏被挪到块外了（Go 侧 `nestedInClass` 锁这个嵌套）。
- [ ] 非仓库目录：如实显示错误码（`not_a_repo` 等），**不**显示"干净仓库"这类伪状态。
- [ ] stage / unstage：行内按钮 → `POST /web/api/git/stage`，响应里的 `status` 直接替换本地缓存
      （不二次拉取），条目在两个分组间即时移动；失败时提示错误并保持原分组。
- [ ] diff 弹窗：点变更行打开；「工作区 / 已暂存」切换与「忽略空白」开关都能重拉；
      hunk 头 + 增删着色正确；行号**只有一列**（add→新侧、del→老侧、context→新侧，
      `nonewline` 留空；不再出现「老 新」两列并排），两侧行号不同时行号格内只出现该侧那一个数字；
      `parse_error` 非空时降级显示 `raw`（不显示空白）。
- [ ] 大屏左右分栏（≥900px）：左栏是 git 侧栏（标题行 + `«` 折叠按钮 + 仓库标签 / 刷新 / 状态行 +
      变更/提交记录两个子页签），右栏是选中项的 diff——**不再**是覆盖全页的遮罩弹窗
      （`#git-diff-overlay` 由 `position: fixed` 退回 `static`、`#git-diff-modal` 撑满右栏），
      两栏各滚各的；左栏列表滚动时「标题行 + 工具行 + 子页签栏」整块 sticky 贴顶。
- [ ] 选中联动：打开某条变更的 diff 后，左栏对应行标 `.active` + `aria-current`；同一个文件同时在
      「已暂存」「未暂存」两组里时，只标右栏正在看的那一条（按 `data-group` 对齐，不两条都亮）。
- [ ] 选中联动的两个换挡口（同文件在两组里时最容易露馅）：「工作区 / 已暂存」切换后，标记要跟着
      挪到对应分组那一条（右栏换了 diff、左栏还停在原组就是 bug）；`✕` / `Esc` 关闭 diff 后左栏
      **不留**任何 `.active` / `aria-current`——右栏已经是空态，左栏就不该再说"正在看这条"。
- [ ] 右栏空态与语义：分栏且没打开 diff 时右栏显示空态文案（不是空白），`✕` / `Esc` 关闭后回到空态；
      同一份 DOM 在两种断点下语义正确切换——大屏 `role=region` 且**不带** `aria-modal`，
      窄屏 `role=dialog` + `aria-modal=true`（读屏不该以为页面被模态挡住）。
- [ ] 折叠与拖拽调宽：与「文件」页签同一套交互（`«` 收成 34px 窄轨、把手 8px 命中区拖拽、
      `←/→` 每步 24px、`Home/End` 到两端、双击复位 320px，范围 200–640px 且右栏始终 ≥320px），
      状态记在 `localStorage.webGitSideWidth` / `webGitSideCollapsed`，刷新后沿用。
- [ ] 窄屏回退（<900px）：回到上下结构，diff 仍以居中弹窗打开（点遮罩空白处 / `Esc` 关闭）；
      左栏标题行与折叠按钮隐藏，列表与提交区照旧可用。
- [ ] 提交列表：分页「加载更多」按 `cursor` 追加；每条显示 short sha / subject / 作者 / 时间 / refs。
- [ ] 刷新与会话感知：进入页签即重拉 status + commits；「⟳ 刷新」手动重拉；切换会话后作用域根随之切换
      （与「文件」页签显示同一个工作目录）。
- [ ] 失败如实显示：git 不可用 / 非仓库 / 超时按 `code` 提示（`git_unavailable` / `git_failed` 等），
     不把失败渲染成空列表。

### 2.11 文件页签：二级页签（目录浏览 + 每个打开的文件一个页签）

数据源仍是 `/web/api/fs/*`（无新增端点）；页签的开合与 `md|txt` 切换都是**客户端状态**。
查看文件**不开弹窗**：每个文件在自己的二级页签里就地显示（`.md` 默认 Markdown 渲染）。
前置：会话工作目录内有至少一个 `.md` 文件、一个非 Markdown 文本文件（如 `.go`）、一个子目录与一张图片。

- [ ] 页签栏：文件页签内出现「目录浏览 | <文件名>…」页签栏，默认选中「目录浏览」；
      `←/→` 环绕、`Home/End` 首尾，切换后焦点停在页签上（roving tabindex：选中 0、其余 -1）；
      隐藏的面板不占位（`hidden`），切回「目录浏览」不重复请求目录；页签多时横向可滚动、不出纵向滚动条；
      **没打开任何文件时整条页签栏收起**（只剩「目录浏览」一条、没得可切，窄屏大屏都不白占一行），
      此时浏览面板不再挂 `tabpanel` 语义（`aria-labelledby` 会指向被 `display:none` 的页签），
      与分栏时同样降级成 `role=region` + `aria-label=文件浏览器`。
- [ ] 控件归属：文件浏览器相关控件（作用域根 / 排序 /「⬆ 上级」/「⟳ 刷新」/「隐藏项」/ 计数）
      都在**左栏面板内**，页面顶部没有文件工具栏；左栏收窄到 200px 时控件行自动换行（不横向溢出）。
- [ ] 大屏左右分栏（≥900px）：左栏是文件浏览器（标题行 + 控件行 + `«` 折叠按钮），右栏是打开的文件页签 + 面板；
      「目录浏览」页签在分栏下隐藏（由左栏取代），浏览面板改为常显区域（`role=region` + `aria-label`）；
      两栏各自滚动（左栏列表与右栏面板各滚各的，整块不再整体滚动），页签栏只横跨右栏；
      左栏列表滚动时「标题行 + 控件行」sticky 贴顶（排序 / 上级 / 刷新随时可达，不被列表滚走）。
- [ ] 折叠左栏：点 `«` 收成 34px 窄轨（只剩 `»` 按钮可点，`aria-expanded=false`，焦点不丢）；
      再点展开回 300px；控件行与浏览列表一起收起（收窄轨里只留按钮）；状态记在
      `localStorage.webFilesSideCollapsed`，刷新后沿用。
- [ ] 拖拽调宽：把手压在左右栏交界处（8px 命中区，负外边距吃掉自身宽度，不改变两栏几何，
      `cursor: col-resize`）；拖动时实时变宽变窄（拖拽期间 `body.files-resizing`，文本不可选中），
      松手才写 `localStorage.webFilesSideWidth`；范围 200–640px 且右侧始终 ≥320px（越界自动夹住）。
- [ ] 键盘与复位：把手可 Tab 聚焦（`role=separator` + `aria-orientation=vertical` +
      `aria-valuenow/min/max`），`←/→` 每步 24px、`Home/End` 到两端、双击复位到 300px；
      在端点继续按方向键不改变记忆值、也不产生多余请求。
- [ ] 窗口缩放：窗口变窄时左栏被 CSS 挤小（右栏不低于 320px、不出现横向溢出），
      拉回宽屏后左栏回到记忆宽度——「记忆值」不被挤压过程改写；刷新后宽度沿用。
- [ ] 右栏空态与自动接管：分栏且没有打开任何文件时收起空页签栏、显示「从左侧文件浏览器选择一个文件打开…」；
      在窄屏「目录浏览」选中状态下把窗口拉宽，应自动切到**最后打开的文件**（不是空态），且该页签
      `aria-selected=true` / `tabindex=0`（可见页签必须可聚焦，不能留下键盘进不去的悬空选中态）。
- [ ] 窄屏回退（<900px）：回到上下结构（页签栏在上、面板在下），左栏标题行与折叠按钮隐藏，
      控件行跟在浏览面板上方、**随浏览面板一起隐藏**（切到某个文件页签时不残留在列表位置）；
      「目录浏览」页签照旧可见可点、`←/→` 环绕包含它；分栏时它不参与环绕（隐藏页签不能被键盘选中）。
- [ ] 分栏不污染主页签：切到其它主页签时「文件」面板整块 `display:none`，不残留、不叠在别的页签上。
- [ ] 查找或创建：点文件行即新建二级页签（标题为文件名、图标按扩展名区分），焦点跟随新页签；
      重复打开同一文件**不新建**，复用原页签并重读内容（页签数与面板数都不变）。
- [ ] 就地查看：内容显示在页签面板里；面板头显示 文件名 / 作用域内路径 / meta（mime · 大小 · 时间 · kind）；
      文本按等宽原文排版，图片走 `files-view-image`。
- [ ] Markdown 渲染：打开 `.md` 默认渲染（标题 / 列表 / 引用 / 表格 / 代码块 / 行内代码），
      正文不按等宽原文排版；代码块右上角有「复制」按钮且可复制（复制委托挂在页签面板自身，
      不依赖 `#conversation` 的委托）；空文档显示「Markdown 文档为空…」提示，不留白。
- [ ] `md|txt` 切换：按钮只在 Markdown 文件上出现（非 Markdown 文本 / 图片 / 二进制一律隐藏，
      不给无效开关）；按钮文字即当前方式（`md` / `txt`），切换只重绘、**不重新请求**
      （DevTools 网络面板无新增 `/fs/preview`）；复用页签时保留上次的方式。
- [ ] 关闭：页签上的 `✕` 是 `<span>`（页签内不嵌套 button），只关页签、磁盘文件不动；
      关掉选中页签后选中左邻且焦点跟随；点非选中页签的 `✕` 不影响选中；`Delete` 关闭当前文件页签、
      在「目录浏览」上无效；全部关掉后回到「目录浏览」。
- [ ] 页签右键菜单（页签多时的批量关闭）：在文件页签上右键（或 `Shift+F10` / 菜单键）弹出小菜单，
      含「关闭当前 / 关闭其它 / 关闭左侧 / 关闭右侧 / 关闭全部」；「目录浏览」是固定页签——
      关闭类动作对它一律禁用（它恒在最左侧，「关闭左侧」也禁用），它也不计入任何页签的
      「左侧 / 右侧」范围；没有可关对象时对应项置灰不可点（不是点了没反应）。
- [ ] 菜单几何与键盘：菜单贴右 / 下边时向内侧翻转、始终夹在视口内；打开即把焦点落到第一个可用项，
      `↑↓` 只在可用项之间环绕、`Home/End` 到首尾、`Enter/Space` 执行并收起；`Esc`（焦点回到触发的页签）、
      点击菜单外、滚动页面、窗口尺寸变化都会收起；在另一个页签上右键就地重开到新目标。
- [ ] 批量关闭后的选中与焦点：被关掉的那批里有当前页签时，选中**触发菜单的那个页签**并把焦点落上去
      （「关闭全部」回「目录浏览」）；当前页签没被关到时选中态不动（不抢用户正在看的上下文）；
      关掉的是非选中页签时不影响选中。
- [ ] 上限与提示：同时最多 12 个文件页签；超限自动关闭最早打开的页签并 toast 提示（不静默丢弃）；
      挑牺牲者时**跳过当前正在查看的页签**（只有除它之外已无可关时才关它——上限是硬约束，
      宁可关掉看得见的那个，也不能超），所以正在读的文件不会因为随手多开一个页签就被抽走。
- [ ] 失败与降级：二进制 / 超限 / 读取失败在页签内如实提示（文案与 2.9 一致），
      失败页签保留「⟳ 重读」（可重试）、禁用「⤓ 下载」；`truncated=true` 时 meta 行仍提示「已截断（reason）」。
- [ ] 下载：`⤓ 下载` 走 `/fs/download`，`scope` 取**该页签记录的 scope**（不拿当前浏览根顶替）。
- [ ] 会话感知：切换会话后文件页签全部关闭（作用域根已变），只剩「目录浏览」，不残留旧会话路径。

## 3. 协议下拉框专项用例（combo popup）

Provider 编辑弹窗的协议字段曾用原生 `<input list=datalist>`，存在**有值与无值显示不一致**的缺陷：浏览器会按 input 当前值过滤 datalist 选项，编辑 `openai` 协议的 provider 时下拉只剩匹配项，新增（空值）时才显示全部。已改为 ▼ 按钮 + 自定义 popup（与底部 Model 字段同方案）。以下用例为该组件的回归重点：

| # | 前置 | 操作 | 预期 |
|---|------|------|------|
| P1 | 新增 Provider（协议无值） | 点 ▼ | popup 向下展开（`top: calc(100% + 4px)`），完整列出 `openai / openai_image / anthropic / gemini / codex`，无高亮 |
| P2 | 编辑协议为 `openai` 的 provider | 点 ▼ | **仍完整列出 5 项**，`openai` 高亮 + "当前"徽标（修复前：只剩 openai 一项） |
| P3 | popup 打开 | 点选任意项 | input 值更新为所选项，popup 关闭；重新点 ▼ 后高亮跟随新值 |
| P4 | popup 打开 | 在 input 输入未预置的协议（如 `my-proto`） | 列表实时附加 `my-proto` 并标"当前"，预置 5 项保持不变；点选后 input 为该值 |
| P5 | popup 打开 | 按 `Esc` | popup 关闭 |
| P6 | popup 打开 | 点击 popup 与 ▼ 以外的区域 | popup 关闭 |
| P7 | popup 打开 | 关闭整个编辑弹窗（取消/×/点 overlay） | popup 一并收起，重新打开弹窗时不残留 |
| P8 | 填好表单 | 保存 | `POST /web/api/config/providers` payload 中 `protocol` 为 input 当前值（含自定义值，自由输入能力不被破坏） |
| P9 | 编辑协议为空的 provider | 点 ▼ | 与 P1 一致（空值场景全量展示） |

> 维护约束：协议字段**不要再改回** `input list=datalist`。原生 datalist 的"按值过滤"行为正是本缺陷根因，且无下拉箭头、跨浏览器表现不一。需要新增预置协议时，在 [index.html](../../backend/cmd/aicli/commands/web/index.html) 的 `cfg-provider-protocol-options` datalist 里加 `<option>` 即可，popup 会自动读取。

## 4. 快速检查项（提交前端改动前）

```bash
# 1. JS 语法检查（无构建工具，node --check 即可）
node --check backend/cmd/aicli/commands/web/app.js

# 2. 起 stub 服务（见第 1 节方式 B），浏览器过一遍第 2 节清单中受影响的功能区

# 3. 涉及 cfg-bar / 弹窗样式的改动，确认两个 popup 方向都正常：
#    底部 Model（向上）与编辑弹窗协议（向下）共用 .cfg-model-popup / .cfg-combo-popup 外观规则

# 4. 页签行为沙盒（Node，stub document/fetch，无需浏览器；在仓库根目录运行）：
node scripts/verify-micro-web-skills-tab.mjs   # 技能页签：列表/会话感知/详情分组页签与键盘导航/错误/竞态/页签接线
node scripts/verify-micro-web-tool-output.mjs  # 工具输出折叠/展开：抬头控件（文字 + ▼/▲ 图标）在「工具」行内、默认折叠（≤5 行）、溢出判定、点击/键盘切换、控件隐藏时不响应、复制不含控件文字
node scripts/verify-micro-web-msg-window.mjs   # 长会话窗口化 + 权威窗口对账 + SSE 序号守卫：首屏只渲染最新一页、尾部增量替换与窗口右移保留历史、上滚以 msg_before 前插并补偿 scrollTop、到顶停止、pending 气泡确认、游标异常守卫；本地兜底行被权威窗口覆盖后只保留一行（重复渲染回归）、半截兜底行让位、顺序按绝对索引收敛、pending 按发送基线 + 宽松文本匹配释放（不再钉在末尾）；重复序号帧丢弃 / 跳号触发权威对账 / connected 复位序号基线
node scripts/verify-micro-web-event-sequence.mjs # SSE 帧序号守卫（纯逻辑）：首帧基线、连续递进、重复序号判 duplicate（不推进基线）、跳号判 gap、无序号帧放行、connected 复位、守卫实例相互独立
node scripts/verify-micro-web-msg-filter.mjs   # 对话页签过滤面板：面板结构（sticky 居中吸附）与样式不变量、角色多选（aria-pressed/查询串顺序）、搜索图标展开与 300ms 去抖/回车立即提交/Esc 先清词再收起、服务端过滤接线（roles/q + msg_limit 组合 = 搜索结果分页）、匹配计数与 0 命中空态、条件变化作废在途分页请求、复制带过滤条件
node scripts/verify-micro-web-menu.mjs         # 顶部菜单栏 + 会话导出：菜单栏/右侧状态簇结构、data-menu-action 均指向真实控件、开合与 Esc 回焦、导出请求与 Content-Disposition 命名下载、失败不下载；CSS 侧校验「无 fallback 的 var(--token) 必须已定义」与快捷键面板背景为不透明语义变量
node scripts/verify-micro-web-render-mode.mjs  # assistant 消息 md|txt 渲染：默认 md（行生成时同步渲染）、切换控件在气泡右上角（正文之前）、按钮 active/aria-pressed 同步、点击委托、复制取 .msg-text 原文、代码块复制委托（流式气泡 + md 气泡）
node scripts/verify-micro-web-copy-msg.mjs     # 单条消息复制：所有角色（含 error 回退）抬头行 ⧉ 图标、只取本行正文（标签/控件/相邻消息/md 产物均不入内）、行间隔离与 ✓ 反馈、整会话复制不受影响、工具行展开收起仍可用、流式气泡复制不重复响应
node scripts/verify-micro-web-question-answer.mjs # 提问回答写入：建议项 / 自由回答（Enter、提交按钮、Shift+Enter、IME、空答案）→ question_answer payload、收起对话框保留 composer 答案路由、服务端回执分支（stale 未送达告警 / resolved 不误报）、审批语义不变、index.html/style.css 静态不变量
node scripts/verify-micro-web-pane-split.mjs   # 左右分栏助手（文件 / GIT 页签共用）：折叠 class 只在大屏生效、宽度写 CSS 变量并夹进可用范围（容器变窄时上限自己降）、端点空操作不抹记忆值、←→/Home/End 与落盘、拖拽（pane-resizing / 松手才落盘 / 折叠与非主键不起拖 / 失焦兜底）、双击复位、跨断点 onApply 与 onBreakpoint、记忆恢复与隐私模式降级、matchMedia 缺失按窄屏降级、元素缺失静默降级
node scripts/verify-micro-web-git-diff.mjs     # GIT 页签 diff 行渲染（单列行号）：每行只有一个行号格（回归：曾把老/新行号拼成「老 新」两列）、add→新侧 / del→老侧 / context→新侧 / nonewline→空、两侧不同时只出现该侧数字、null 缺字段不补 0（0 是合法行号保留）、文本转义、renderDiff 复用同一行构造器、style.css 行号列宽度按单列给
node scripts/verify-micro-web-composer.mjs     # 浮动 composer 面板：面板是 .layout 内浮层子节点（与 #main-col 平级，输入区/配置栏/动态状态条不在 #tab-main 内）、必需元素与既有 cfg-* id 全保留、菜单与 Ctrl+J 折叠入口接线（无第二份折叠逻辑）、style.css 浮层几何（.layout 内 absolute 停靠在状态栏上方 / 自由位置改 fixed 且清 transform / z-index 低于模态框 / 折叠规则）、**无让位机制**（没有 --composer-reserve、body 不为面板留白、#footer 规则无 composer 耦合）、行为（拖动跟随与夹取、键盘微调与 Home 复位、**全程不写页面级 CSS 变量**、Ctrl+J、localStorage 回放）
node scripts/verify-micro-web-todos.mjs        # 任务列表浮层（贴在 composer 上沿）：结构（#todo-panel 在 #composer-panel 内且在标题行之前）与样式不变量（bottom:calc(100% + 1px) 衔接、[hidden] 不占位、折叠只收 .todo-body、进行中加粗 / 已完成删除线）；接线（sse.js 在 switch 前分流 tool_end / 会话边界、chat.js 应用 screen 回放、app.js 初始化、后端 web_schema.go 与 chat_debug_screen_http.go 两个字段名）；纯函数（解析裁剪：坏条目丢弃 / 整组不可用 → null、计数与进度、当前项、快照合并：runtime 按 seq 单调 / history 只兜底）；面板行为（无快照隐藏、回放恢复计数与逐项状态、实时旧序号不回退、会话切换清空、折叠与 localStorage 记忆、面板缺失静默降级）
```

模块化后另有一层静态检查：用带 DOM stub 的 Node 脚本对 `app.js` 入口做动态 `import()`，可在不启浏览器的情况下抓出语法错误、缺失导出、模块求值期错误（拆分落地时即靠它在浏览器回归前拦截了两处问题）。检查思路：stub `document/window/localStorage/fetch/EventSource` 后 `await import("./app.js")`，任何模块图断裂都会在这里抛错。

页签栏「上下滚动条」这类纯布局问题在沙盒里量不出来，本地改用真实 Chromium 量盒模型：`web/tmp/measure-skill-tabs-scroll.mjs`（同目录已 gitignore、不随仓库发布）把 `style.css` 内联进 `index.html`，注入页签后打印页签栏的 `clientHeight` / `scrollHeight` 与滚动条占位像素，并扫描整个详情弹层找出所有纵向滚动容器；脚本尾部还会把旧写法注入回来做对照，确认测量方法本身捕捉得到这条滚动条。

`scripts/verify-micro-web-skills-tab.mjs` 即按此思路写成：它 stub `document`（含 `documentElement` / `body` / 元素 `classList` / `querySelector(All)`）与 `fetch`，先单测 `js/skills.js` 的行为（含详情分组页签：只生成非空分组、只渲染当前页签、点击与 `← → Home End` 导航、打开聚焦选中页签、关闭把焦点还给列表条目、缺字段不补默认值），最后 `import` `js/ui.js` 并点一次 `#tab-skills-btn`，验证按钮 → 激活面板 → 拉取目录的接线。缓存页签有同思路的本地沙盒脚本（`web/tmp/cache-session-aware.verify.cjs`，该目录已 gitignore、不随仓库发布）。

`scripts/verify-micro-web-msg-window.mjs` 自带一套迷你 DOM（含 `className` ↔ `class` 映射、`scrollHeight` / `clientHeight` 与插入后撑高容器的高度记账），用来验证 `js/chat.js` 的消息窗口状态机：行索引与 `serverMessagesHtml` 的绝对索引、`parseMessageWindow` / `shouldRebuildForWindow` 的分支判定、首屏只渲染最新一页（不随会话总 turn 数增长）、尾部增量替换不重建既有节点（以节点身份断言）、窗口右移时保留已加载的早期节点、上滚以 `msg_before` 前插且按插入高度补偿 `scrollTop`、到达最早一条后停止、实时刷新不与已加载历史重复、pending 气泡随服务端窗口确认释放、服务端返回重叠/断开一页时的游标守卫（不重复插入且停止继续上滚），以及会话复制的取材：窗口不完整时取服务端全量 transcript，窗口已覆盖全部消息时不发额外请求、仍按 DOM 顺序收集（保留 `[推理]` 前缀等既有格式）。`fetch` 在该沙盒里是同步消费预置队列的，因此任何会触发请求的操作前必须先 `screenQueue.push(...)`。

`scripts/verify-micro-web-msg-filter.mjs` 复用同一套迷你 DOM，验证对话页签的「消息过滤面板」（`js/msg-filter.js` + `js/chat.js` 接线）。静态契约先钉住页面结构：`#msg-filter` 在 `#conversation` 内、`#screen` 之前（sticky 相对滚动容器吸附），搜索输入默认 `display:none`、清除按钮默认 `disabled`，`style.css` 的 `.msg-filter` 为 `position: sticky; top: 0` 且水平居中、外层 `pointer-events: none` 而面板本体 `auto`（两侧空白不拦截消息点击）、窄屏下移避让会话复制按钮。行为侧覆盖角色多选（`aria-pressed` 回写、查询串按角色词表固定顺序、未知角色不会出现）、搜索图标点击展开并聚焦、输入 300ms 去抖合并为一次请求、回车立即提交、Esc 先清词再收起、清除按钮一次清掉角色与搜索词且只触发一次刷新、面板内点击不冒泡到对话区。接线侧验证「服务端过滤 + 搜索结果分页」：刷新/上滚分页/复制三类请求都带 `roles`/`q` 且仍保留 `msg_limit`（复制为不带 `msg_limit` 的全量取回），`message_window.total` 是匹配数、`unfiltered_total` 是过滤前总数（计数显示「匹配 N / 共 M 条」），0 命中显示「没有匹配的消息（已过滤）」而不是空白，以及过滤条件变化后**在途的更早消息分页响应被代次守卫丢弃**（否则新结果集里会混入旧条件的消息）。

`scripts/verify-micro-web-menu.mjs` 解析真实的 `index.html` 建迷你 DOM（`#id` 选择器、`classList`、事件冒泡与 `stopPropagation`），把 `js/menu.js` 挂上去跑：断言菜单栏含「文件/视图/帮助」三个下拉、右侧状态簇含连接/轮次/发送状态与主题图标、每个 `data-menu-action` 都能点到真实存在的目标控件（转发既有控件点击，不另起实现）、点击开合与切换菜单/点外部关闭/Esc 关闭并回焦、导出项请求 `/web/api/export?format=…` 并按 `Content-Disposition`（`filename*` 优先，回退 `filename=`）命名下载、HTTP 非 2xx 时只提示不产生下载；最后用 DOM stub 动态 `import("./app.js")` 检查入口模块图与 `initMenu` 接线。场景 6 对 `style.css` 做静态不变量检查：所有不带 fallback 的 `var(--token)` 都必须在主题块里有定义（未定义的自定义属性会让整条声明在 computed-value 阶段失效，快捷键面板正是因 `--bg1` 未定义而渲染成透明卡片），并断言 `#shortcut-help .shortcut-panel` 的背景取自 `--modalBg`、该变量在深色 / 浅色 / 跟随系统三种主题下都已定义且不是 `transparent`。

窄屏布局同理在真实 Chromium 里量（沙盒量不出 `scrollWidth` 与断点行为）：视口切到 360×640 / 390×844 / 414×896 / 640×360（横屏）/ 768×1024 / 1280×800，断言 `document.documentElement.scrollWidth === innerWidth`（无横向滚动条）、顶栏在 ≤767px 折成两行（约 62px；>767px 保持单行约 31px）、状态簇右缘贴合顶栏内容右缘、`文件/视图/帮助` 三个下拉面板的 `getBoundingClientRect()` 完全落在视口内、底部 `.status-bar` 单行（`scrollHeight === clientHeight`）且内容超宽时横向滚动（`scrollWidth > clientWidth`）；另用长文案（"连接已断开 / 轮次 12/99 / 发送中…排队 3 条"）复测，确认状态簇不会把菜单栏挤到第二行。

底部 composer 是**浮层**（`#composer-panel`，逻辑在 `js/composer.js`）：拖动 / 夹取 / 键盘微调 /
复位 / 折叠 / 记忆回放由 `scripts/verify-micro-web-composer.mjs` 在沙盒里覆盖，但「浮层与页面
是否互不影响、会不会跑出视口」是盒模型问题，沙盒量不出，仍需真实 Chromium 按同一组视口量。
**口径（2026-09 修订，用户明确要求）**：面板与状态栏是**两个互不影响的图层**——
`#footer`（状态栏）是页面自己的一行、永远贴底，**绝不因面板让位 / 上移**；面板只是浮在它上面的
另一层。实现方式：面板挂在 `.layout` 内（`position: absolute; left: 50%; bottom: 8px`），
因此天然停在状态栏上方且**不参与常规流**——不吃任何元素的高度、不写任何页面级 CSS 变量
（旧的 `--composer-reserve` + `body padding-bottom` 让位机制已删除；它还有个副作用：页面出现
根滚动条时 `100vh` 与布局视口高度不等，会让面板与状态栏重叠 2px）。自由拖动
（`data-composer-mode="free"`）改回 `position: fixed` 跟手并清 `transform`，拖到视口四角不越界
（夹取 8px）；面板 z-index 低于审批 / 会话切换模态框（模态打开时面板落在其下方）；窄屏（≤767px）
面板降到会话抽屉之下（抽屉是临时覆盖层，打开时盖住面板，此时把手不可拖，需先收起抽屉）。
真实浏览器回归用临时 CDP 脚本（`backend/cmd/aicli/commands/web/tmp/composer-layer-check.mjs`，
`tmp/` 已 gitignore、按需重建）：6 个视口（1440×900 / 1280×800 / 1024×768 / 800×600 / 640×480 /
375×667）各跑「停靠 → 折叠 → 展开 → 拖动 → 点 ⇲ 复位 → 切到「配置」页签」，核心断言是
**状态栏的 top/bottom/height 在整条路径上零变化**（不让位的硬指标），外加：面板父节点是
`.layout`、停靠 `absolute` / 自由 `fixed`、停靠时 `panel.bottom ≤ footer.top − 8px` 且状态栏
可命中、body padding 是基础值、无 `--composer-reserve`、根滚动容器无滚动条、
**`⇲` 停靠态隐藏 / 拖动后出现 / 复位后再次隐藏**、底部不再有重复的配置文案（无 `#cfg-current`）。
注意面板浮在内容区底部时会盖住内容约 132–149px
（浮层固有代价，可用折叠 / 拖动让开）；「最新」按钮按矩形重叠自动抬到面板上方，避免被盖住点不到。
以下原口径继续适用（数值需按新结构复测）：`#prompt` 与 `#send-btn` 底边对齐（多行增高时
按钮贴底不拉高）、输入框空态高度 = CSS 最小高度（45px，清空后内联高度被移除）、`#cfg-bar` 折叠行
高度（竖屏 360×640 / 414×896 与横屏 640×360 均为单行 45px；展开面板 122–128px 且完全落在视口内）、
正文区高度（竖屏 ≥345px、横屏 ≥112px）与整页 `verticalFit`。压测：发送键瞬态文案「正在停止…」
不得把输入框压到 200px 以下；输入 12 行时输入框被 `max-height: min(160px, 38vh)` 截住（横屏 30vh）
且正文区不被挤到 0；面板内点 ▼ 弹出的模型列表（≤240px，矮视口 40vh）不得越出视口顶部。

## 5. 历史遗留问题（拆分时保持原行为）

- **已修复**（提问回答写入，S15）：提问模态框 `#approval-overlay` 是全屏阻塞遮罩，遮住了底部 composer，
  而模态框内只有建议项按钮、**没有自由文本输入框**；同时 `hideApproval()` 会清空 `pendingQuestionID`，
  用户点 ✕ / 遮罩 / Esc 后答案路由永久丢失。三者叠加使开放型提问在 Web 客户端上「答案无法写入」。
  现已在 `#approval-modal-body` 内加入 `#question-answer-row`（仅 question 显示，Enter / 按钮提交，
  含 Shift+Enter 换行、IME 组合输入与空答案防护），并把主动收起改为「仅隐藏对话框、保留
  `pendingQuestionID`」，底部 composer 分支（`chat.js` 的 `hasPendingQuestion()`）继续可用。
  回归：`scripts/verify-micro-web-question-answer.mjs`。
- **已修复**（随 assistant 消息 md|txt 渲染切换一并处理）：代码块"复制"按钮的事件委托注册在 `#stream-msg` 元素上，且注册时机早于该元素的惰性获取（`streamMsgEl` 初始为 null，`beginStream` 时才 `getElementById`），因此该委托实际从未生效。现改为挂在 `#conversation` 上（`#stream-msg` 与 `#screen` 都是它的子节点），一处覆盖流式气泡与 assistant 切到 md 后的代码块，且该元素在 `initStream()` 执行时已存在。回归：`scripts/verify-micro-web-render-mode.mjs` 第 8 组用例（两类气泡各复制一次）。

