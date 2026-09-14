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
- [ ] **关于页签**：显示客户端名 `aicli micro web client`、一行说明与页签清单/端点链接。
- [ ] 顶栏布局：最左侧依次为 `☰`（折叠会话列表）、主题切换图标、连接状态、轮次状态、
      发送瞬态提示；会话标题与会话 ID 居中显示（窗口缩放/侧栏折叠后仍保持居中，
      超长文本按省略号截断且不撑破顶栏）。
- [ ] SSE 断连时顶部显示"已断开，重连中…"，恢复后消失。
- [ ] 顶栏显示当前会话标题与会话 ID：初始（无会话）显示"未选择会话"；切换会话、
      新建会话、重命名当前会话、刷新列表后均同步更新；长标题/长 ID 截断省略，
      悬停（title 属性）可见完整值。
- [ ] **切换会话确认弹窗**：点击非当前会话先弹出确认框（显示目标会话标题，悬停可见
      会话 ID）；「取消」/`✕`/遮罩空白处/`Esc` 关闭后不发送 resume 请求；点「切换」
      后正常完成切换（顶栏与列表同步）。当前会话高亮项点击不弹窗（直接走
      already_current 刷新）。当前会话有任务进行中（发送中/执行中/正在停止）时，
      弹窗内出现"切换将在当前任务与排队输入完成后生效"提示。
- [ ] `Esc` 打开/关闭快捷键帮助。

### 2.2 对话页 + 底部配置栏（cfg-bar）

- [ ] Provider / Reasoning 原生 `<select>` 可切换，当前生效配置（`openai · gpt-4o`）随之更新。
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
- [ ] 支持模型 textarea："获取模型列表"按钮调 `POST /web/api/config/providers/fetch-models` 并合并结果。
- [ ] Reasoning 编辑器：保存模型列表后按模型逐行生成，编辑不丢草稿。
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
```

模块化后另有一层静态检查：用带 DOM stub 的 Node 脚本对 `app.js` 入口做动态 `import()`，可在不启浏览器的情况下抓出语法错误、缺失导出、模块求值期错误（拆分落地时即靠它在浏览器回归前拦截了两处问题）。检查思路：stub `document/window/localStorage/fetch/EventSource` 后 `await import("./app.js")`，任何模块图断裂都会在这里抛错。

页签栏「上下滚动条」这类纯布局问题在沙盒里量不出来，本地改用真实 Chromium 量盒模型：`web/tmp/measure-skill-tabs-scroll.mjs`（同目录已 gitignore、不随仓库发布）把 `style.css` 内联进 `index.html`，注入页签后打印页签栏的 `clientHeight` / `scrollHeight` 与滚动条占位像素，并扫描整个详情弹层找出所有纵向滚动容器；脚本尾部还会把旧写法注入回来做对照，确认测量方法本身捕捉得到这条滚动条。

`scripts/verify-micro-web-skills-tab.mjs` 即按此思路写成：它 stub `document`（含 `documentElement` / `body` / 元素 `classList` / `querySelector(All)`）与 `fetch`，先单测 `js/skills.js` 的行为（含详情分组页签：只生成非空分组、只渲染当前页签、点击与 `← → Home End` 导航、打开聚焦选中页签、关闭把焦点还给列表条目、缺字段不补默认值），最后 `import` `js/ui.js` 并点一次 `#tab-skills-btn`，验证按钮 → 激活面板 → 拉取目录的接线。缓存页签有同思路的本地沙盒脚本（`web/tmp/cache-session-aware.verify.cjs`，该目录已 gitignore、不随仓库发布）。

## 5. 已知问题（拆分时保持原行为，未修）

- 代码块"复制"按钮的事件委托注册在 `#stream-msg` 元素上，且注册时机早于该元素的惰性获取（`streamMsgEl` 初始为 null，`beginStream` 时才 `getElementById`），因此该委托实际从未生效。拆分时原样保留在 `js/stream.js` 的 `initStream()` 中；如需修复，改为 document 级事件委托即可。

