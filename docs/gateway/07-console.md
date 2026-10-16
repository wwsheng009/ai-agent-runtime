# 07 · 轻量独立控制台设计（参考 micro web client）

> 上游：[02-architecture.md](./02-architecture.md)、[05-wan-and-mobile.md](./05-wan-and-mobile.md)、[06-implementation-plan.md](./06-implementation-plan.md)
> 决策：网关控制台采用**新增轻量独立控制台**，实现范式对齐 `aicli micro web client`
> （`go:embed` + 零构建 + 原生 ES 模块），首期**不复用** `frontend/`（React/Vite 完整工作台）。

---

## 1. 定位与决策

| 项 | 内容 |
|----|------|
| 是什么 | 网关自带的 hub 视图：跨设备**会话聚合 + 切换 + 打开**，设备/令牌管理，连接与审计状态 |
| 不是什么 | 不是又一个完整工作台（不做文件/Git/MCP/技能/分析的全套 UI——那些在**被代理的会话视图**里已有） |
| 技术范式 | 与 `micro web client` 同源：`//go:embed` 静态资源、`index.html` + `style.css` + `app.js` + `js/*.js`（ES 模块）、**零构建步骤** |
| 首期不选 `frontend/` | React/Vite 工作台面向"单 runtime-server 全功能管理"，体积/构建/依赖与网关"轻量 hub"目标不匹配；且它需要 `VITE_API_BASE_URL` 指向后端，与网关的反向代理模型不同 |

**与 `frontend/` 的关系**：二者共存、职责不同。`frontend/` 是 `runtime-server` 的完整工作台（可经网关
`/n/{node}/` 反向访问）；本控制台是**网关层**的跨设备入口。P2+ 若确有深度功能需求，再评估是否合并，
但首期不为它引入前端构建链。

---

## 2. 复用的 micro web client 范式（附现有实现证据）

| 范式 | 现有实现（证据） | 控制台如何复用 |
|------|------------------|----------------|
| `go:embed` 目录 | `backend/cmd/aicli/commands/web_page.go:15` `//go:embed web` | `internal/gateway/console/console/` 用 `//go:embed console` 嵌入 |
| 单 handler 伺服静态资源 | `HandleChatWebPage`（`web_page.go:23`）：按扩展名给 `Content-Type`、拒绝 `..`/绝对路径、`index.html` 走 `no-cache` | 同构实现，增加 CSP 与 owner Cookie 引导 |
| 内联引导脚本 | `chatWebAuthFetchWrapperScript`（`web_page.go:63`）在 ES 模块前注入令牌包装 | 控制台改为：注入 `window.__GATEWAY__` 配置 + CSRF 双提交引导（**不注入本机令牌**） |
| 原生 ES 模块 | `web/js/*.js`（sessions/composer/sse/stream/markdown/files/git/mcp/skills/analysis…） | `console/js/*.js`（auth/sessions/nodes/devices/iframe/sse/notify/mobile） |
| SSE 客户端独立处理 | `web/js/sse.js`（EventSource 无法设头，单独拼 URL） | 控制台不直连会话 SSE（由 iframe 内的微前端负责）；控制台自身只订阅**节点/会话状态 SSE** |
| h2c 连接预算 | `pprof.go:505` `newChatWebH2CHandler(...)` | 网关用 TLS 下的 HTTP/2（或前置明文 h2c），同一动机 |
| 缓存策略 | `index.html` `no-cache`；页面资源 `no-cache, no-store` | 控制台外壳可缓存（PWA），但 `index.html` 与 `/api/*` 不缓存 |

> 结论：控制台**不需要新前端技术栈**，把 `micro web client` 的"单页 + 原生模块 + 嵌入"范式直接平移即可，
> 复用其工程经验（路径穿越防护、引导脚本时序、SSE 独立处理、连接预算）。

---

## 3. 与 micro web client 的关键差异（不可照搬）

| 维度 | micro web client | 网关控制台 |
|------|------------------|-----------|
| 认证 | 本机写令牌 `X-AICLI-Token`（meta 注入 + fetch 包装） | **owner HttpOnly Cookie + CSRF**；浏览器**绝不接触**本机令牌 |
| 会话范围 | 单进程、当前会话（+本机会话列表） | **跨设备、多会话聚合** |
| 数据来源 | 本地 `/web/api/*` | 网关管理 API `/api/*`（元数据）+ 反向代理 `/s/{id}/web/`（交互） |
| 会话正文 | 自己渲染 | **不重复渲染**：以 iframe/跳转打开被代理的微前端 |
| 移动端 | 已有响应式基础 | 明确以移动优先为首要目标（hub 的主要使用场景是手机） |
| 离线外壳 | 无 | PWA service worker 缓存外壳 |

---

## 4. 信息架构与页面

```text
/                     → 登录页（未登录）/ 会话总览（已登录）
/s/{session_id}/web/  → 会话视图（iframe 嵌入被代理的 micro web client，见 §6）
/devices              → 设备与令牌管理
/status               → 连接/审计/版本
/about                → 关于（版本、协议、连接诊断）
```

### 4.1 会话总览（首页，核心）

```text
┌───────────────────────────────────────────────┐
│ aicli-gateway        [搜索…]   ⚙  🔔 2   👤   │  顶栏：品牌 / 搜索 / 设置 / 通知 / 账户
├───────────────────────────────────────────────┤
│ ▸ vince-laptop（在线 · 2 会话）                │  设备分组（可折叠）
│   ┌─────────────────────────────────────────┐ │
│   │ ● 网关设计        运行中 · 3s 前          │ │  会话卡片：状态点 + 标题 + 注意力徽标
│   │   最后：…差分屏幕与移动端优化…            │ │           + 相对时间 + 预览（截断）
│   ├─────────────────────────────────────────┤ │
│   │ ⚠ 重构任务        等待审批 · 1m 前        │ │  注意力徽标：审批 / 提问 / 完成 / 出错
│   └─────────────────────────────────────────┘ │
│ ▸ build-server（离线 · 1 会话，最近 2h 前）     │  离线设备灰化 + 最近活跃
└───────────────────────────────────────────────┘
```

- **顶部聚合条**：`N 个会话需要处理`（attention ≠ none 计数），点击过滤。
- **分组**：按设备分组；同设备多会话可折叠；设备显示在线/离线/最近活跃。
- **过滤/搜索**：筛选段 `在线`（默认）/ `全部` / `需处理` / `进行中` / `离线`，加关键词搜索
  （标题 + 预览 + 会话 ID + 动态状态）；筛选与排序记忆在浏览器（localStorage）。
- **默认视图**：默认只显示在线会话，离线快照收进「离线」筛选，避免主列表被旧数据占满。
- **新鲜度**：卡片显示"内容 x 前"（`updated_at`）与"同步 x 前"（`synced_at`，网关盖章）；
  节点显示"心跳 x 前"（`last_heartbeat`）；全部相对时间由本地 5s tick 推进，
  超过 60s 的同步/心跳加"可能过期"样式；SSE 断线时顶部持久横幅提示并在重连后自动对齐。
- **设备分组**：同一节点有 ≥2 个可见会话时折叠成设备分组（组头：名称/在线态/会话数/最近时间）。
- **子代理行（默认开启）**：宿主默认上报（`AICLI_GATEWAY_REPORT_SUBAGENTS=0` 可关闭），每个"仍在运行"的子代理任务
  显示在其父会话卡片正下方（`↳` 缩进 + agent 标识 + 状态）；父会话不可见时子行一并隐藏，
  设备分组只按顶层会话计数。工具栏「子代理：显示/隐藏」开关折叠全部子行（随会话偏好持久化）。
  子行没有自己的注意力徽标：托管/失败状态仍显示在父行上（08-notifications §3）。
- **键盘**：`/` 聚焦搜索；`↑/↓`（或 j/k）在卡片间移动；Enter 打开详情；Space 展开/收起。
- **移动端**：打开会话走同标签（浏览器返回键回到列表）；返回时恢复筛选与滚动位置。
- **会话 ID**：卡片元信息行内直接显示（等宽字体、超长省略、悬停看全量）；**点击 ID 即复制**（toast 反馈）；
  「详情」弹出完整元数据（会话 ID + 复制、节点、状态、动态状态、注意力、最近更新与最后一条消息）。
- **卡片密度**：默认紧凑态；最后消息与内联回复框收进「展开」区，「展开/收起」按会话记忆；
  宽屏（≥1120px）双列网格。
- **信息分层（按业务属性）**：卡片自上而下 = 身份行（状态点 + 标题 + 注意力徽标 + 内容时间）→
  **动态状态条**（状态胶囊 + 宿主活动文案 + 本地时钟；活跃/等待/中断时常驻，样式上与元信息分离）→
  内容预览 / 展开后的完整消息 → **元信息行**（节点 · 会话 ID（点击复制）· 同步时间）→ 动作行 →
  内联回复 → 实时输出（页脚，见下）。
- **内联回复**：输入框与「发送」同排（输入框随草稿自动增高、Enter 发送、Shift+Enter 换行、
  发送中按钮禁用），发送按钮不再独占一行。
- **卡片动作**：展开/收起、详情、打开（离线快照，或未声明 Web 路由的节点 —— 如未以 `--web` 启动的
  `aicli` —— 禁用并提示原因）、置顶、（可选）在微前端中新建/恢复会话。
- **变化提示**：SSE 更新时对状态/内容发生变化的卡片做一次短暂高亮（脉冲）。
- **实时输出**：活动期尾段（`status_tail`，≤512 runes，monospace）**固定为卡片页脚**——
  在动作行与内联回复框之下、始终是卡片最后一个区块；折叠/展开两种实时展示互斥，不会同屏两份：
  - **折叠卡片**：底部显示尾段**最后一行**（活动脉冲点、点击即展开完整尾流）——列表里就能看到实时进度；
  - **展开卡片**：同一底部位置换成 8em 滚动尾流（`.s-live` 隐藏），支持"跟随最新/跳到底部"；
  - **详情弹窗**：附带"实时输出（进行中）"块；
  - 工具栏"实时输出：开/关"（prefs 记忆）；增量经 `event: session` 单会话下发（≤1Hz）。
  **展开即上报 `presence tail=true`**（收起/关闭开关释放；页面可见时每 30s 续期），网关据此在
  `HEARTBEAT_ACK` 上对 agent 做 watch/unwatch 门控（P1-b），并按需保留推送抑制。展开与 agent
  心跳（默认 20s）竞态时最坏等一个心跳周期恢复推流（实测展开早于首个 gating ack 时 TAILMARK 连续到达）。
- **迁移事件条**：网关观察到的 state/attention 迁移经 `event: session-event` 推送到顶部"刚刚"条；
  **按会话去重**：同一会话的连续迁移（运行中→回答中→已完成）只保留 1 个 chip，原位刷新为最新状态并
  移动到最前，最多 10 个不同会话 / 10 分钟；点击定位并脉冲对应卡片、可一键清空；详情弹窗展示
  "状态变更/注意力变更/节点心跳/同步"四个网关时间。
- **默认排序**：需处理（approval/question/error）→ 进行中 → 其余按最近活动。
- **行点击**：整行不响应点击（避免误触/选中文本时跳转），打开会话只通过行内按钮：
  「会话」= 就地打开实时面板（§4.3），「打开」= 跳转完整微前端。

### 4.2 会话视图

- 主体是 **iframe**：`src=/s/{session_id}/web/`（同源，见 §6），承载完整的会话交互 UI。
- 顶部**网关工具栏**（在 iframe 之上，固定）：返回列表、会话切换下拉、设备名、连接状态、全屏。
- **不重复实现**完整的聊天/文件/Git/MCP UI——它们由被代理的微前端提供（唯一的轻量例外见 §4.3）。

### 4.3 会话实时面板（消息渲染 + SSE live）

有些场景下用户不想离开列表去开 iframe（手机、弱网、只想扫一眼最近几条）。因此会话卡片行内
再加一个「会话」按钮，弹出一个**接近全屏**的模态面板，就地渲染该会话的历史与实时消息：

- **入口与可用性**：卡片行内按钮「会话」。仅当节点同时上报 `screen` 与 `events` 能力
  （aicli 需以 `--web` 启动）且非离线快照、非子代理行时显示；`input` 能力另决定输入区是否可用。
  子代理行**不给**按钮——节点 `/web/api/screen` 返回的是**节点当前活跃会话**的 transcript，
  子代理共享父级 transcript，给了按钮反而误导（页头也显式显示节点名）。
- **数据来源**（经网关反向代理到节点自身的微前端接口，与「打开」同一通道）：
  - 历史 `GET /n/{node}/web/api/screen?format=json&msg_limit=40[&msg_before=N]`
    → `{available, reason?, messages[{role,content}], message_window{total,start,end,has_more}}`。
    **键名是 `message_window`，没有 `window` 别名**（服务端 `chatDebugScreenSnapshot.MessageWindow`）；
    读错键不会报错，只会让 `has_more` 恒 false、翻页游标恒 0——即「加载更早消息」静默失效，
    所以这一条必须按字段名精确对齐。
  - 实时 `EventSource /n/{node}/web/api/events`；
  - 发送 `POST /n/{node}/web/api/input`，body `{"prompt":"…"}`，成功返回 `{status:"queued"}`
    （与卡片行内回复表单同一契约，失败读 `body.reason`）。
- **范围差异（与节点原版 Web UI 相比）**：只做**消息渲染 + 输入**，不做 provider / model /
  reasoning effort 切换，也不做审批与提问的内联回答、停止回合、附件上传；需要这些时用面板内的
  「打开原版 Web」跳到完整界面（审批/提问只给一条提示横幅）。
- **分页与窗口**：首屏 40 条；滚到顶部附近或点「加载更早消息」时按 `msg_before` 前插 60 条，
  并按新增高度补偿滚动位置。`msg_before` 的游标必须是**本地最老一条的绝对索引**（`msgStart`），
  不能取上一份响应里 `message_window.start`——刷新会把后者推回「最新 N 条」窗口，用它翻页会取回
  一段已经在手的区间，「紧邻才前插」判据失配后走整块重建，反而丢掉最新消息。
  刷新是**保前缀合并**（老行不重建，流式刷新不打断阅读），因此本地
  最多保留 400 行，超出从头部丢弃并重新开放翻页。关键心算法：行 key 用**绝对索引**，
  丢头部不改变留存行的 key，DOM 身份 / 文本选区 / 折叠状态因此都保住。
- **正文 Markdown 子集**（`renderMarkdown`，输入全量转义后才写 innerHTML）：标题、粗体/斜体/
  删除线、行内代码、围栏代码块、有序/无序列表、引用、分割线、链接，以及 **GFM 管道表格**
  （含 `:---` / `:---:` / `---:` 对齐）。表格的识别规则与节点 micro web client 的 `markdown.js`
  逐条对齐（行须以 `|` 起止；第二行是分隔行则首行为表头，否则整块按无表头渲染），否则同一条
  消息在面板与原版 Web 里会长得不一样。判块顺序上表格必须排在列表规则**之前**——否则分隔行的
  `-` 会先被列表吃掉，整张表退化成段落。
- **推理/工具折叠**（与节点 micro web client 的 `chat.js` / `style.css` 同一语义与状态机）：
  - **推理** → 原生 `<details>` 折叠面板，抬头行即 `<summary>`（默认收起一行，`▸/▾` 图标，
    悬停高亮），内容区独立内滚（260px 上限）。
  - **工具** → 默认折叠约 5 行（`max-height:8em` + 底部渐变遮罩）；展开/收起控件（`展开/收起`
    文字胶囊 + `▼/▲` 图标）并入「工具」抬头行；**完整文本始终在 DOM 里**（CSS 截断）。
    判定规则同原版：内容溢出 → 启用控件；未溢出 → 隐藏控件、直接显示全文。面板未排版时
    （clientHeight 0）不打 ready 标记，下轮渲染重测。
  - **活跃回合状态机**（busy 时的快照刷新走）：最后一块推理/工具总是自动展开，其余折回；
    **用户手动展开过的块永不自动折回**——按服务端绝对索引记账（`chatUserExpanded`），
    窗口刷新重建 DOM 后按 key 恢复。换会话清账本（索引跨会话无意义）。
  - 只有**服务端历史行**带折叠结构；live/pending 行是瞬态内容，保持平铺。
- **实时语义**：增量帧（`assistant_delta` / `reasoning_delta` / `assistant_image_progress`）只喂
  「实时气泡」；权威内容一律以 `screen` 快照为准，`turn_end` 用其携带的
  `assistant_snapshot` / `reasoning_snapshot` 收敛气泡，随后拉一次快照把气泡落成正式行。
  触发型事件走 1000ms 尾随节流，`document.hidden` 时断开流（省流量、避免后台反复抖动）。
- **必须显式注册事件名**：`EventSource` 只把**注册过**的命名事件派发给 listener，漏注册等于整帧
  被浏览器静默丢弃（历史事故是 `assistant_message` 终稿整帧丢失）。面板只注册自己消费的子集，
  清单见 `app.js` 的 `CHAT_SSE_EVENTS`；`console_test.go` 的 `TestConsoleChatPanelAsset` 做
  **双向**守卫（消费清单必须全部注册，注册的也必须有分支消费）。
- **`session_start` / `session_end` 不是会话切换**：二者是**一次 run 的起止边界**——actor 在每次
  run 开头与 `assistant_message` 之后各发一次，载荷只带 `turn_id`（`internal/chat/actor.go`）。
  把它们接到「整块重建面板」上会**在每个回合清空消息列表、滚动位置与阅读进度**，并闪一次
  「正在加载消息…」，正好摧毁「保前缀合并不打断阅读」的设计意图。只有 `session_switched`
  （载荷带新 `session_id`）才允许重置面板，且必须同时把标题与 presence 搬到新会话，否则会出现
  「标题写着 A、内容却是 B」，推送抑制还挂在已经不看的会话上。`console_test.go` 守这条。
- **5 分钟重连**：网关反代 `agent/forward.go` 有 5 分钟请求超时，SSE 会被周期性断开。状态点显示
  「连接断开，重连中…」并依赖浏览器自动重连；**重连成功后必须重拉一次 screen 快照**补齐断线
  期间丢掉的帧（节点侧队列满时同样静默丢帧，二者都靠这一步自愈）。
- **降级**：无 `EventSource` 的浏览器退化为 3s 轮询 `screen`；`screen` 返回 `available:false` 或
  非 200 时面板内联提示 + 保留「打开原版 Web」，并且**只提示一次**（持续失败不刷 toast）。
- **状态栏**（与节点 micro web client 底部状态栏同源同义，只读、无切换入口）：
  - **动态状态行**：`dynamic_status` 事件驱动，载荷 `{active, text, role, interruptible, elapsed_ms}`
    （`chat_interaction.go`）。服务端**只在状态变化时推一次**，所以时钟必须本地走：用
    `elapsed_ms` 反推起点后每秒推进（面板是中途打开的，这是常态），`active:false` 或空文本即收起。
    `interruptible` 不渲染「esc to interrupt」——本面板没有打断入口，不承诺不存在的操作。
  - **静态段**：`GET /web/api/statusbar` → `{available, segments[{kind,text}], full_line}`，
    与 micro web client 的 `statusbar.js` 同一端点与渲染口径；`model_changed` 事件触发重取，
    换会话时复位重取。取失败只清空，不打扰用户（静态段缺失不影响可用性）。
- **忙时兜底轮询（实时性的第二条腿）**：服务端 SSE 队列在压力下**静默丢帧**（enqueue 满即丢，
  不报错），断线重连期间的帧也永久缺失。只靠增量帧驱动渲染时，一旦丢的正是 `assistant_delta`
  那一段，面板就表现为「连接正常但内容不动」。因此以 `heartbeat`/`connected` 的权威
  `session_busy` 为闸门，**忙时每 2s 重取一次 screen 快照**，把实时性从「帧必须到达」解耦为
  「快照必须到达」；空闲即停（不空转）。刷新是保前缀合并，因此不会打断阅读。
- **可观测性**：连接状态点直接显示**累计帧数**（`已连接 · 137 帧`）与最近事件名（悬浮）。
  「连上了但零帧」与「连接失败」是两类完全不同的故障，排障时第一个分叉点必须一眼可读。
- **阅读状态**：面板打开即视为「正在看这个会话」——复用 presence（`tail:false`）抑制该会话的
  推送，关闭时释放并 `markRead`；`Esc` 关闭，焦点由既有 modal 机制（`inert` + Tab 陷阱 + 归还）托管，
  面板打开期间全局快捷键不再穿透到卡片。

### 4.4 设备与令牌、状态、关于

- 设备页：列出设备与令牌前缀、创建（仅显示一次）、撤销、最近使用。
- 状态页：在线节点数、活跃隧道/流、延迟分位、最近审计事件、协议版本。
- 关于页：网关版本、协议版本、连接诊断（浏览器↔网关 RTT、隧道 RTT）。

---

## 5. 关键 UX 流程

### 5.1 首次运行（空态引导）

- 登录后若无任何设备：显示"还没有设备接入"，给出三步指引：
  1. 在网关创建**设备令牌**（页内按钮，生成后仅显示一次 + 复制按钮）；
  2. 在本机运行：`aicli chat --gateway <url> --gateway-token <tok>`（可复制命令）；
  3. 等待设备出现在列表（自动刷新 + SSE 推送）。
- 空态是**首次成功的关键路径**，必须有明确下一步，而不是空白页。

### 5.2 设备离线

- 卡片灰化 + "最近活跃 X 前"；设备分组标题显示离线。
- 打开离线会话时给出明确提示（"设备离线，无法连接"），而不是无限加载。
- 设备重连后自动恢复（SSE `node.online` 事件）。
- 重启恢复/下线遗留的**离线快照**条目在「节点 → 离线快照」页可单个移除，也可**一键清除全部**
  （`DELETE /api/nodes?stale=1`；仅清理 stale，live 节点不受影响）。

### 5.3 会话切换

- 列表 → 会话视图：**懒加载 iframe**；返回列表时保留滚动位置与过滤条件。
- 会话视图内切换：更新 iframe `src`（或复用同一 iframe + 会话深链），不整页刷新。
- 记住"上次打开的会话"（localStorage，仅 ID，不含正文）。

### 5.4 注意力与通知

- 徽标实时更新（订阅网关 `/api/events` SSE 的会话状态事件）。
- 可选 Web Push / ntfy 双通道（见 [05 §2.5](./05-wan-and-mobile.md)、[08 §10](./08-notifications.md)）：
  点击通知直达对应会话视图；侧栏「发送测试通知」按通道显示投递结果（`channels.webpush` / `channels.ntfy`）。
- 去重：同一会话同一注意力状态在时间窗内只提醒一次。
- 完整的**通知/注意力模型**（事件→`attention` 映射、`NOTIFY` 帧、presence 抑制、免打扰）见
  [08-notifications.md](./08-notifications.md)；控制台只消费网关聚合结果，不直接解析本地事件。

### 5.5 错误态（必须显式，不静默）

| 场景 | 呈现 |
|------|------|
| 隧道不可用 | 会话视图横幅："设备已离线"；列表卡片灰化 |
| 上游 502/504 | iframe 内/上方提示："本地服务无响应（502/504）" + 重试按钮 |
| 路由 403 | "该路径未被授权访问"（白名单/能力位） |
| 设备令牌被撤销 | 设备消失 + 状态页审计记录；agent 侧提示重新签发 |
| owner 会话过期 | 跳登录页，保留原目标路径（登录后回跳） |

### 5.6 断线

- 顶部持久横幅："与网关连接中断，正在重连…"（SSE 断开 + 重连退避）。
- 重连成功后自动刷新会话列表并对齐状态（不整页刷新）。

---

## 6. 与反向代理 / 微前端的边界

- **同源**：控制台与被代理的微前端都在网关同源下（`/` 与 `/s/{id}/web/`），iframe 无跨域问题，
  owner Cookie 自动携带（`SameSite=Lax` 同源导航）。
- **切换即打开本地 Web UI**：点会话卡片上的「打开」→ iframe 载入 `/s/{id}/web/`（aicli 微前端）
  或 `/s/{id}/`（runtime-server SPA），网关反向代理到本机回环；完整链路与要求见
  [08-notifications.md](./08-notifications.md) §7。
- **可打开性判定**：控制台只对声明了对应 Web 路由的节点（aicli `/web`、runtime-server `/`）提供
  「打开」；未声明的节点按钮禁用并给出原因，网关对未声明路由一律 403
  （fail-closed，见 [04-security.md](./04-security.md) §3）。
- **前置依赖**：微前端需 **base-path 感知**（[06](./06-implementation-plan.md) §4.1），否则
  `/s/{id}/web/` 前缀代理下其根绝对路径会指回网关根。这是 P1/P2 的前置改造。
- **iframe 安全**：微前端响应可设 `Content-Security-Policy: frame-ancestors 'self'`；
  控制台设 `frame-src 'self'`；避免第三方嵌入。
- **不重复实现**：控制台不重建聊天/文件/Git/MCP 的**完整**交互 UI（那是被代理微前端的职责），
  自身只做"外壳 + 导航 + 状态"。唯一例外是 §4.3 的**会话实时面板**：它复用节点自身的
  `screen` / `events` / `input` 接口做轻量消息渲染与输入，且不含 provider / model /
  reasoning effort 切换等配置面；控制台自身的 `/api/events` 仍只用于列表/通知，不解析其内容。

---

## 7. 技术实现

### 7.1 目录布局

```text
internal/gateway/console/
├── console.go              # //go:embed console；伺服 handler（参考 HandleChatWebPage）
├── console/                # 静态资源（零构建，直接编辑后 go build 生效）
│   ├── index.html
│   ├── style.css
│   ├── app.js              # 引导 + 路由
│   ├── manifest.webmanifest
│   ├── sw.js               # service worker（缓存外壳）
│   └── js/
│       ├── auth.js         # 登录/登出/会话过期
│       ├── api.js          # 管理 API 封装（含 CSRF 头）
│       ├── nodes.js        # 设备分组渲染
│       ├── sessions.js     # 会话列表/过滤/卡片
│       ├── view.js         # 会话视图 iframe 生命周期
│       ├── events.js       # 控制台自身 SSE（节点/会话状态）
│       ├── notify.js       # 通知/PWA 安装引导
│       └── mobile.js       # 视口/键盘/抽屉
└── console_test.go         # 静态资源伺服 / 路径穿越 / 外壳资产契约（版本号一致 + 会话面板）测试
```

### 7.2 伺服 handler 要点（照搬 micro web 的成熟做法）

- `//go:embed console`；按扩展名给 `Content-Type`；拒绝 `..` 与绝对路径（防穿越）。
- `index.html`：`Cache-Control: no-cache`，并注入引导脚本（`window.__GATEWAY__` 配置、CSRF token）。
- 静态资源：PWA 外壳可长缓存（带指纹）或 `no-cache`（首期简单起见统一 `no-cache`，与 micro web 一致）。
- 安全响应头：`Content-Security-Policy`、`X-Content-Type-Options: nosniff`、`Referrer-Policy`。
- **不注入任何本机令牌**（与 micro web 的令牌注入形成对照）。

### 7.3 开发体验

- 与 micro web 一致：编辑 `console/` 下文件 → `go build` → 生效；无 npm/vite/watch 依赖。
- 可选：`--console-dev-dir` 指向本地目录（仅开发），避免每次 rebuild（默认关闭，仅回环）。
- **改外壳资源必须同步 bump 版本号**（5 处）：`index.html` 的 `app.js?v=N` 与 `style.css?v=N`、
  `sw.js` 的 `CACHE` 与 SHELL 里两条 `?v=N`、`app.js` 的 `register("./sw.js?v=N")`。漏一处就会
  让已装 PWA 的浏览器继续用旧外壳（CDN 对带版本号的 `*.js`/`*.css` 有 4h 边缘缓存，SW 还会自己
  缓存一份），本地开发完全看不出来。`console_test.go` 的 `TestConsoleAssetVersionsMatch` 守这条。

---

## 8. 无障碍与 i18n

- **a11y**：语义化标签、`aria-label`、键盘可达（Tab/Enter/Esc）、焦点可见、对比度 ≥ 4.5:1。
- **i18n**：中文优先；文案集中为键值表（`js/i18n.js`），为后续扩展留接口；
  与 `frontend/` 的 i18n 口径不必强绑（控制台文案量小）。
- **主题**：跟随系统 `prefers-color-scheme`，支持手动切换（复用 micro web 的主题思路）。

---

## 9. 移动端（与 05 对齐）

- 响应式断点：`<640px` 单列卡片 + 抽屉；`≥1024px` 双栏（列表 + 会话视图）。
- PWA：`manifest.webmanifest` + `sw.js`（缓存外壳，**不缓存** `/api/*` 与 iframe 内容）。
- 软键盘：会话视图的输入在 iframe 内，由微前端负责键盘适配（见 05 §2.2）；控制台自身表单同样适配。
- 低带宽：控制台外壳极小，主要带宽在 iframe 内的会话视图（由 05 的低带宽模式覆盖）。
- 触控：卡片点击区 ≥ 44px；滑动手势（左滑返回列表）为增强，不阻塞基本操作。

---

## 10. 验收清单

- [ ] `cmd/gateway` 单二进制即可提供控制台（`//go:embed`，无外部静态文件依赖）。
- [ ] 零构建：修改 `console/` 后 `go build` 即生效；CI 无 npm/vite 依赖。
- [ ] 静态资源伺服通过路径穿越负向测试（`..`、绝对路径、编码绕过）。
- [ ] 登录 → 空态引导 → 设备接入 → 会话出现（全链路可见）。
- [ ] 会话列表：设备分组、状态、注意力徽标、过滤/搜索。
- [ ] 会话视图：iframe 打开被代理微前端，切换会话不整页刷新。
- [ ] 会话实时面板：行内「会话」按钮打开近全屏面板，历史消息可滚动、SSE 增量实时追加、可发送输入。
- [ ] 会话实时面板降级：SSE 断开自动重连并显示连接态；无订阅能力时回退轮询，不白屏。
- [ ] 设备离线/隧道 502/路由 403/会话过期均有显式错误态。
- [ ] 移动端：可安装 PWA、软键盘不遮挡、横向不溢出、触控目标达标。
- [ ] 控制台**不接触**任何本机令牌；`/api/*` 不缓存。
- [ ] a11y：键盘可完成"登录 → 打开会话 → 返回列表"；面板可用 `Esc` 关闭、焦点被困在面板内。
- [ ] 外壳资产版本号 5 处一致（`TestConsoleAssetVersionsMatch` 通过）。
