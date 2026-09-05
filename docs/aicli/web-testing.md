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
| 其余 `/web/api/*` | 统一返回 `{ status: "ok" }`（POST 类操作直接成功） |

注意事项：

- 页面会连 `GET /web/api/events`（SSE）。静态桩返回 JSON 会导致 EventSource 报错并每 2 秒重连，页面功能不受影响，可忽略。
- 浏览器自动化时可用 `page.addInitScript` 提前 stub `window.EventSource` 消除重连噪音。

这样可以在无真实 provider 的机器上测试全部前端交互，并可自由构造边界数据（空协议 provider、空模型列表、超长字段值等）。

## 2. 手工回归清单

### 2.1 全局

- [ ] 三个页签（对话 / 日志 / 配置）切换正常，`«` 折叠侧栏、`◐` 主题切换生效。
- [ ] SSE 断连时顶部显示"已断开，重连中…"，恢复后消失。
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
```

模块化后另有一层静态检查：用带 DOM stub 的 Node 脚本对 `app.js` 入口做动态 `import()`，可在不启浏览器的情况下抓出语法错误、缺失导出、模块求值期错误（拆分落地时即靠它在浏览器回归前拦截了两处问题）。检查思路：stub `document/window/localStorage/fetch/EventSource` 后 `await import("./app.js")`，任何模块图断裂都会在这里抛错。

## 5. 已知问题（拆分时保持原行为，未修）

- 代码块"复制"按钮的事件委托注册在 `#stream-msg` 元素上，且注册时机早于该元素的惰性获取（`streamMsgEl` 初始为 null，`beginStream` 时才 `getElementById`），因此该委托实际从未生效。拆分时原样保留在 `js/stream.js` 的 `initStream()` 中；如需修复，改为 document 级事件委托即可。

