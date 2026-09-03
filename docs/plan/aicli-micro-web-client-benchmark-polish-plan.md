# aicli micro web client — 对标成熟项目优化方案（Benchmark Polish）

> 状态：📋 方案已定，待实施
> 关联：`docs/plan/aicli-micro-web-client-upgrade-plan.md`（上一轮架构升级，已完成）

## 1. 背景与参考基准

本轮以成熟/知名 AI chat 前端为基准，优化 aicli micro web client 的体验。
参考项目与要点：

| 项目 | 参考要点 |
| --- | --- |
| **Open WebUI** | 多行输入自动增高、消息级操作（复制/重发）、欢迎空状态、Toast 轻提示 |
| **ChatGPT-Next-Web (NextChat)** | 快捷键帮助面板、页面标题反映运行状态、智能滚动跟随 |
| **Chatbot UI** | 会话卡片式列表、输入区高度自适应、引用块/表格 Markdown |
| **ChatGPT / Claude 官方** | 用户上滚阅读历史时暂停自动跟随、回到底部恢复；消息时间戳 |

## 2. 现状差距（Gap 分析）

已具备（上一轮完成）：深/浅主题、流式打字机、发送/停止状态机、基础 Markdown
（代码块+复制、标题、粗体、链接、无序列表）、推理折叠、审批模态框、图像预览、
会话搜索/排序/重命名/删除、SSE 重连、输入历史、移动端适配。

| # | 差距 | 现状 | 成熟项目做法 | 优先级 |
| --- | --- | --- | --- | --- |
| G1 | 输入框单行 | `<input id="prompt">` 单行，长输入体验差 | textarea 自动增高，Enter 发送、Shift+Enter 换行 | P0 |
| G2 | Markdown 覆盖不足 | 无表格/引用/有序列表/任务列表/删除线/自动链接 | 完整 CommonMark 子集 | P0 |
| G3 | 滚动强制拉底 | 每次渲染 `scrollIntoView(false)`，用户上滚读历史会被拽回 | 用户上滚时暂停跟随，回到底部恢复 | P0 |
| G4 | 无空状态引导 | 空白页直接显示 `(empty)` | 欢迎页 + 示例问题快捷按钮 | P1 |
| G5 | 反馈只有状态栏文字 | `#send-status` 一闪而过 | Toast 轻提示（复制成功/失败/排队） | P1 |
| G6 | 页面标题不反映状态 | 恒定 "aicli micro web client" | busy 时标题加「● 处理中」等 | P1 |
| G7 | 无消息级复制 | 只有代码块复制 | 助手消息整体复制按钮 | P1 |
| G8 | 快捷键不可发现 | 无帮助面板 | `Ctrl+/` 或 `?` 打开快捷键列表 | P2 |
| G9 | 无消息时间戳 | 无 | 每条消息旁显示时间 | P2 |

## 3. 技术方案

### 3.1 多行输入（G1）
- `index.html`：`<input id="prompt">` → `<textarea id="prompt" rows="1">`。
- `app.js`：`autoGrow()` 监听 input 事件，`height = scrollHeight` 上限约 6 行；
  Enter（无 Shift）发送、Shift+Enter 插入换行；↑↓ 历史浏览逻辑保留
  （Shift+Enter 时不计入历史浏览分支）。
- `style.css`：textarea 复用原 #prompt 样式 + `resize: none; overflow-y: hidden;`。
- 兼容性：所有 `promptEl.value` 读写不变（textarea.value 同 API）。

### 3.2 Markdown 增强（G2）
在 `renderMarkdown()` 现有正则链上追加（保持幂等、不引入第三方库）：
- 表格：`| a | b |` 两行结构 → `<table><thead>…<tbody>…`，分隔行 `|---|` 识别；
- 引用：`> text` → `<blockquote>`（连续多行合并为一个 blockquote）；
- 有序列表：`1. item` → `<ol><li>`；
- 任务列表：`- [ ] / - [x]` → `<li class="task unchecked/checked">` + `☐/☑`；
- 删除线：`~~text~~` → `<del>`；
- 自动链接：裸 `https?://…` → `<a target="_blank">`（注意避免与 markdown 链接重叠，先处理 `[..](..)` 后再处理裸 URL，且排除已生成的 `<a>` 标签内）。
- CSS：`#stream-msg table/blockquote/ol/del` 样式；表格边框、斑马纹可选。

### 3.3 智能滚动跟随（G3）
- `app.js`：维护 `userScrolledAway` 标志。
  - `conversation` 滚动事件：若 `scrollTop + clientHeight < scrollHeight - 40` 且用户主动滚动 → `userScrolledAway = true`，显示「↓ 回到底部」浮动按钮；
  - `scrollToBottom()` 增加参数 `force`：流式 tick 渲染调用 `scrollToBottom(!userScrolledAway)`；
  - 浮动按钮点击 → `userScrolledAway = false` + 强制滚底 + 隐藏按钮；
  - 用户滚回底部附近 → 自动清除标志并隐藏按钮。
- `style.css`：`.scroll-bottom-btn` 浮动小圆钮（右下角，z-index 高于内容）。

### 3.4 空状态欢迎页（G4）
- `index.html`：`#conversation` 内增加 `#welcome` 区块（隐藏），含标题 + 3~4 个示例问题按钮 + 提示文案。
- `app.js`：`updateWelcome()` 在 refreshScreen/finishStream/connected 后调用：
  当 `screenEl` 为空/`(empty)` 且无流式消息且无活跃 turn 时显示欢迎页，否则隐藏。
  示例按钮点击 → 填入输入框并聚焦（不直接发送，符合 ChatGPT 风格）。
- 文案：中文，示例问题围绕 aicli 能力（如「帮我列一个 Go 项目的目录结构建议」）。

### 3.5 Toast 轻提示（G5）
- `index.html`：`<div id="toast-container">`（fixed 底部居中）。
- `app.js`：`showToast(msg, kind)`，2.5s 自动消失，同消息去重；
  用于复制成功/失败、排队成功、会话删除/重命名结果（保留 #send-status 同步更新）。
- `style.css`：`.toast` 深色圆角小条，`.toast.ok/.toast.err` 绿色/红色边框。

### 3.6 页面标题状态（G6）
- `app.js`：`updateTitle()` 在 `setUI`/`setStatus` 中调用：
  - busy → `● aicli（执行中）`；posting → `… aicli`；disconnected → `✗ aicli（已断开）`；idle → `aicli micro web client`。

### 3.7 消息级复制（G7）
- `app.js`：`#screen` 旁增加「复制消息」小按钮（仅当 screen 非空时显示）；
  点击复制 `screenEl.textContent`，Toast 反馈。
- 流式消息结束时（finishStream）同逻辑：复制按钮针对最近完成内容。

### 3.8 快捷键帮助（G8，P2）
- `index.html`：`#shortcut-help` 模态（隐藏），列出全部快捷键。
- `app.js`：`Ctrl+/`（或 `?` 在非输入态）切换；点击遮罩/Esc 关闭。
- 快捷键清单：Enter 发送 / Shift+Enter 换行 / ↑↓ 历史 / Esc 中断 / Ctrl+L 主题 /
  Ctrl+K 清屏 / Ctrl+/ 帮助。

### 3.9 消息时间戳（G9，P2）
- `app.js`：`finishStream` 时在流式消息容器记录 `hh:mm` 角标；`refreshScreen` 权威帧不强制改（避免与终端渲染冲突），仅信息流模式追加。

## 4. 实施范围与风险

- **范围**：仅 `backend/cmd/aicli/commands/web/` 下 `index.html`、`style.css`、`app.js`，零后端改动，零 API 变更。
- **风险**：低。纯前端渐进增强；回归面集中在输入事件与渲染函数。
- **验证**：
  - `node --check app.js`（语法）；
  - `go build ./cmd/aicli/`（embed 完整性）；
  - `go test ./cmd/aicli/commands/ -run TestHandleChatWeb -count=1`（页面服务不回归）；
  - 手动清单：多行输入、表格/引用渲染、上滚暂停跟随、欢迎页出现/消失、Toast、标题变化。

## 5. 实施顺序（Phase）

| Phase | 内容 | 对应 Gap |
| --- | --- | --- |
| P0 | G1 多行输入 + G2 Markdown 增强 + G3 智能滚动 | P0 全部 |
| P1 | G4 欢迎页 + G5 Toast + G6 标题 + G7 消息复制 | P1 全部 |
| P2 | G8 快捷键帮助 + G9 时间戳 | P2（视时间） |

每 Phase 完成后同步验证（node --check + go build），最后统一提交。
