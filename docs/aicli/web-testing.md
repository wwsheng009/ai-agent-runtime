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

- [ ] Provider / Reasoning 原生 `<select>` 可切换，当前生效配置（`openai · gpt-4o`）随之更新。
- [ ] **窄屏 composer（≤767px）**：输入框与发送键同排，发送键右对齐且贴底（`#prompt` 多行增高时
      按钮不跟着拉高）；`#prompt` 与 `#cfg-model` 字号 ≥16px（低于 16px 时 iOS 聚焦会放大整页）；
      可点按控件高度 ≥44px；输入框空态回到 CSS 最小高度（占位提示折行不再把空输入框撑成两行，
      `autoGrow()` 在空值时清掉内联高度）；占位提示在窄屏切短文案（`enterkeyhint="send"`，
      手机键盘回车键显示「发送」）；底部按 `env(safe-area-inset-bottom)` 让出手势条；
      配置栏窄屏折叠为单行按钮 + 向上弹出的面板（面板绝对定位覆盖在正文之上，不挤压正文、
      不触发 ResizeObserver 重排；三个选择器在面板内各占一行，模型输入框解除桌面 150px 上限）；
      当前生效配置 `#cfg-current` 窄屏不再显示，同一份文案由折叠按钮承载（超长省略号）；
      点面板外或按 Esc 收起，`aria-expanded` 同步；面板内模型列表限高 `min(240px, 40vh)`
      避免顶部越出视口；横屏矮视口（高 ≤480px）只收紧输入框上限与页脚留白。
- [ ] **桌面（>767px）不受上述折叠影响**：`.cfg-controls{display:contents}`、`#cfg-toggle{display:none}`，
      三个选择器仍平铺在 `#cfg-bar` 一行内，`#cfg-current` 照常显示，cfg-bar 高度保持约 36px。
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
> 窗口标题节点后缀与 spawn `refused` 文案**已落地**（S13，见 §2.7.3）→
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

#### 2.7.3 P2 打磨：窗口标题节点后缀与 refused 文案（S13）

前置：两个 `aicli chat --pprof --mesh` 进程（不同工作区）。

- [ ] **标题节点后缀**：窗口标题为 `aicli micro web client · <工作区> · <节点短 id>`（`node_id` 前 8 位）；
      两个窗口并排时一眼分辨归属（`·` 后两段与各自 `GET /web/api/mesh/self` 一致）。
- [ ] **降级不加后缀**：以 `--mesh=false` 启动（或 mesh 根不可读）→ 标题保持
      `aicli micro web client`，**不出现**空占位（没有悬空的 ` · `）；网格可用后标题自动补齐后缀，
      无需手动刷新页面（`self` 段到达即重算）。
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
node scripts/verify-micro-web-msg-window.mjs   # 长会话窗口化：首屏只渲染最新一页、尾部增量替换与窗口右移保留历史、上滚以 msg_before 前插并补偿 scrollTop、到顶停止、pending 气泡确认、游标异常守卫
node scripts/verify-micro-web-menu.mjs         # 顶部菜单栏 + 会话导出：菜单栏/右侧状态簇结构、data-menu-action 均指向真实控件、开合与 Esc 回焦、导出请求与 Content-Disposition 命名下载、失败不下载；CSS 侧校验「无 fallback 的 var(--token) 必须已定义」与快捷键面板背景为不透明语义变量
```

模块化后另有一层静态检查：用带 DOM stub 的 Node 脚本对 `app.js` 入口做动态 `import()`，可在不启浏览器的情况下抓出语法错误、缺失导出、模块求值期错误（拆分落地时即靠它在浏览器回归前拦截了两处问题）。检查思路：stub `document/window/localStorage/fetch/EventSource` 后 `await import("./app.js")`，任何模块图断裂都会在这里抛错。

页签栏「上下滚动条」这类纯布局问题在沙盒里量不出来，本地改用真实 Chromium 量盒模型：`web/tmp/measure-skill-tabs-scroll.mjs`（同目录已 gitignore、不随仓库发布）把 `style.css` 内联进 `index.html`，注入页签后打印页签栏的 `clientHeight` / `scrollHeight` 与滚动条占位像素，并扫描整个详情弹层找出所有纵向滚动容器；脚本尾部还会把旧写法注入回来做对照，确认测量方法本身捕捉得到这条滚动条。

`scripts/verify-micro-web-skills-tab.mjs` 即按此思路写成：它 stub `document`（含 `documentElement` / `body` / 元素 `classList` / `querySelector(All)`）与 `fetch`，先单测 `js/skills.js` 的行为（含详情分组页签：只生成非空分组、只渲染当前页签、点击与 `← → Home End` 导航、打开聚焦选中页签、关闭把焦点还给列表条目、缺字段不补默认值），最后 `import` `js/ui.js` 并点一次 `#tab-skills-btn`，验证按钮 → 激活面板 → 拉取目录的接线。缓存页签有同思路的本地沙盒脚本（`web/tmp/cache-session-aware.verify.cjs`，该目录已 gitignore、不随仓库发布）。

`scripts/verify-micro-web-msg-window.mjs` 自带一套迷你 DOM（含 `className` ↔ `class` 映射、`scrollHeight` / `clientHeight` 与插入后撑高容器的高度记账），用来验证 `js/chat.js` 的消息窗口状态机：行索引与 `serverMessagesHtml` 的绝对索引、`parseMessageWindow` / `shouldRebuildForWindow` 的分支判定、首屏只渲染最新一页（不随会话总 turn 数增长）、尾部增量替换不重建既有节点（以节点身份断言）、窗口右移时保留已加载的早期节点、上滚以 `msg_before` 前插且按插入高度补偿 `scrollTop`、到达最早一条后停止、实时刷新不与已加载历史重复、pending 气泡随服务端窗口确认释放、服务端返回重叠/断开一页时的游标守卫（不重复插入且停止继续上滚），以及会话复制的取材：窗口不完整时取服务端全量 transcript，窗口已覆盖全部消息时不发额外请求、仍按 DOM 顺序收集（保留 `[推理]` 前缀等既有格式）。`fetch` 在该沙盒里是同步消费预置队列的，因此任何会触发请求的操作前必须先 `screenQueue.push(...)`。

`scripts/verify-micro-web-menu.mjs` 解析真实的 `index.html` 建迷你 DOM（`#id` 选择器、`classList`、事件冒泡与 `stopPropagation`），把 `js/menu.js` 挂上去跑：断言菜单栏含「文件/视图/帮助」三个下拉、右侧状态簇含连接/轮次/发送状态与主题图标、每个 `data-menu-action` 都能点到真实存在的目标控件（转发既有控件点击，不另起实现）、点击开合与切换菜单/点外部关闭/Esc 关闭并回焦、导出项请求 `/web/api/export?format=…` 并按 `Content-Disposition`（`filename*` 优先，回退 `filename=`）命名下载、HTTP 非 2xx 时只提示不产生下载；最后用 DOM stub 动态 `import("./app.js")` 检查入口模块图与 `initMenu` 接线。场景 6 对 `style.css` 做静态不变量检查：所有不带 fallback 的 `var(--token)` 都必须在主题块里有定义（未定义的自定义属性会让整条声明在 computed-value 阶段失效，快捷键面板正是因 `--bg1` 未定义而渲染成透明卡片），并断言 `#shortcut-help .shortcut-panel` 的背景取自 `--modalBg`、该变量在深色 / 浅色 / 跟随系统三种主题下都已定义且不是 `transparent`。

窄屏布局同理在真实 Chromium 里量（沙盒量不出 `scrollWidth` 与断点行为）：视口切到 360×640 / 390×844 / 414×896 / 640×360（横屏）/ 768×1024 / 1280×800，断言 `document.documentElement.scrollWidth === innerWidth`（无横向滚动条）、顶栏在 ≤767px 折成两行（约 62px；>767px 保持单行约 31px）、状态簇右缘贴合顶栏内容右缘、`文件/视图/帮助` 三个下拉面板的 `getBoundingClientRect()` 完全落在视口内、底部 `.status-bar` 单行（`scrollHeight === clientHeight`）且内容超宽时横向滚动（`scrollWidth > clientWidth`）；另用长文案（"连接已断开 / 轮次 12/99 / 发送中…排队 3 条"）复测，确认状态簇不会把菜单栏挤到第二行。

底部 composer 按同一组视口量：`#prompt` 与 `#send-btn` 底边对齐（多行增高时按钮贴底不拉高）、输入框空态高度 = CSS 最小高度（45px，清空后内联高度被移除）、`#cfg-bar` 折叠行高度（竖屏 360×640 / 414×896 与横屏 640×360 均为单行 45px；展开面板 122–128px 且完全落在视口内、正文区高度不变）、正文区高度（竖屏 ≥345px、横屏 ≥112px）与整页 `verticalFit`。压测：发送键瞬态文案「正在停止…」不得把输入框压到 200px 以下；输入 12 行时输入框被 `max-height: min(160px, 38vh)` 截住（横屏 30vh）且正文区不被挤到 0；面板内点 ▼ 弹出的模型列表（≤240px，矮视口 40vh）不得越出视口顶部。

## 5. 已知问题（拆分时保持原行为，未修）

- 代码块"复制"按钮的事件委托注册在 `#stream-msg` 元素上，且注册时机早于该元素的惰性获取（`streamMsgEl` 初始为 null，`beginStream` 时才 `getElementById`），因此该委托实际从未生效。拆分时原样保留在 `js/stream.js` 的 `initStream()` 中；如需修复，改为 document 级事件委托即可。

