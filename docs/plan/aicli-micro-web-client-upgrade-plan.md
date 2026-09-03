# aicli 微型 Web 客户端升级方案

> 文档状态：初稿  
> 适用范围：`backend/cmd/aicli/commands/web_page.go`、`backend/cmd/aicli/commands/web_handlers.go`、`backend/cmd/aicli/pprof.go`  
> 关联方案：`docs/plan/aicli-micro-web-client-plan.md`（原方案设计）  
> 关联代码：`web_page.go`、`web_handlers.go`、`web_schema.go`、`chat_busy_input.go`  
> 搜索参考：GitHub 类似项目 `siteboon/claudecodeui`、`poco-ai/poco-claw`、`wbopan/cui`、`hoangsonww/Claude-Code-Agent-Monitor`、`JPeetz/Hermes-Studio`

---

## 目录

1. [背景与现状](#1-背景与现状)
2. [升级目标](#2-升级目标)
3. [架构升级：HTML/CSS/JS 独立目录 + go:embed](#3-架构升级htmlcssjs-独立目录--goembed)
4. [前端体验升级](#4-前端体验升级)
5. [功能增强](#5-功能增强)
6. [GitHub 同类项目参考](#6-github-同类项目参考)
7. [实现路线图](#7-实现路线图)
8. [验收标准](#8-验收标准)
9. [附录](#9-附录)

---

## 1. 背景与现状

### 1.1 当前实现概况

aicli 微型 Web 客户端（`/web/`）是一套运行在 loopback HTTP 服务器上的实时双向交互界面，当前实现包含：

| 文件 | 行数 | 职责 |
| --- | --- | --- |
| `web_page.go` | 940 行 | 内嵌 HTML/CSS/JS 页面（Go 原始字符串常量 `const chatWebPageHTML`） |
| `web_handlers.go` | 837 行 | 8 个 HTTP 端点处理 + SSE 事件映射 + 事件 schema |
| `web_schema.go` | 282 行 | SSE 事件 schema 定义与示例 |
| `chat_busy_input.go` | 172 行 | 输入队列 busy 捕获 |
| `web_handlers_test.go` | 27 测试 | 端点测试（含 SSE、输入注入、interrupt、会话管理） |

### 1.2 端点清单

| 方法 | 路径 | 功能 |
| --- | --- | --- |
| GET | `/web/` | 返回前端页面（内嵌 HTML） |
| GET | `/web/api/screen` | 屏幕快照（text/json） |
| GET | `/web/api/status` | 状态快照 |
| GET | `/web/api/events` | SSE 事件流 |
| POST | `/web/api/input` | 输入注入（prompt/approval/question_answer/interrupt） |
| GET | `/web/api/events/schema` | 事件 schema 文档 |
| GET | `/web/api/sessions` | 会话列表 |
| POST | `/web/api/sessions/resume` | 恢复历史会话 |

### 1.3 前端页面结构

当前前端代码全部内嵌在 `web_page.go` 的一个 Go 常量字符串中：

```
const chatWebPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <style>         /* 约 155 行 CSS */
  </style>
</head>
<body>
  <!-- HTML 结构：约 65 行 -->
  <script>        /* 约 760 行 JS */
  </script>
</body>
</html>`
```

**CSS 覆盖**：
- 布局（flexbox 双栏：侧边栏 + 主区域）
- 审批面板样式
- 对话区域（`<pre>` 屏幕 + 流式消息）
- 输入行 + 按钮状态机（idle/posting/busy/interrupting 四态）
- 侧边栏（会话列表）
- 标签页切换（对话/日志）
- 页脚
- 脉冲动画（`@keyframes pulse`）

**JS 功能**：
- SSE 连接与事件驱动（`EventSource`）
- 四个状态机：turn 状态机（streamActive/streamEnded）+ 按钮状态机（uiState）
- 打字机效果（typewriter，20ms/char）
- 审批面板（approve/deny）
- 提问面板（含建议按钮）
- 会话列表（排序、刷新、切换、折叠）
- 日志面板（事件记录、清空）
- 自动重连 + 序列号补偿
- 浏览器端 localStorage 记忆（侧边栏折叠、排序方式）

### 1.4 现有问题与痛点

| 问题 | 影响 | 严重程度 |
| --- | --- | --- |
| 前端代码在 Go 字符串常量中，无法独立编辑/调试 | 无语法高亮、无 lint、无格式化 | 高 |
| 无响应式设计，在手机端查看体验差 | 移动端布局错乱 | 中 |
| 屏幕内容以 `<pre>` 纯文本呈现，不支持 Markdown | 消息格式丢失，可读性差 | 高 |
| 无代码高亮 | 代码块难以阅读 | 中 |
| 无深色模式 | 长时间使用眼睛疲劳 | 低 |
| 消息流式揭示后不持久化（finishStream 追加到 `<pre>`） | 消息历史难回溯 | 中 |
| 无图像生成预览 | 图像生成不可见 | 低 |
| 审批面板样式简陋 | 用户体验一般 | 低 |
| 无输入历史（上箭头恢复） | 重复输入不便 | 低 |

---

## 2. 升级目标

### 2.1 核心目标

1. **架构升级**：将 HTML/CSS/JS 从 Go 内嵌字符串迁移到独立目录，用 `go:embed` 编译嵌入，实现开发时热编辑、构建时静态嵌入
2. **体验升级**：优化前端 UI/UX，包括响应式布局、Markdown 渲染、代码高亮、深色模式
3. **功能增强**：完善消息历史、图像预览、输入历史等

### 2.2 非目标

- 不引入 npm/webpack/vite 等前端构建工具（保持零外部构建依赖）
- 不引入前端框架（React/Vue/Svelte）
- 不改动后端端点 API 协议（保持兼容）

---

## 3. 架构升级：HTML/CSS/JS 独立目录 + go:embed

### 3.1 目录结构

```
backend/cmd/aicli/commands/
├── web/                       # 新建：前端静态资源目录
│   ├── index.html             # HTML 结构（从 web_page.go 移出）
│   ├── style.css              # 样式表（从 web_page.go 移出）
│   └── app.js                 # 应用逻辑（从 web_page.go 移出）
├── web_page.go                # 改为：go:embed 嵌入 + 页面常量移除
├── web_handlers.go            # 不变：HTTP 端点
├── web_schema.go              # 不变：事件 schema
├── web_handlers_test.go       # 不变：测试
└── chat_busy_input.go         # 不变
```

### 3.2 迁移步骤

**Step 1：创建 `web/` 目录**（`backend/cmd/aicli/commands/web/`）

```
web/
├── index.html      # 从 web_page.go 提取 HTML 骨架
├── style.css       # 从 web_page.go 提取 CSS
└── app.js          # 从 web_page.go 提取 JS
```

**Step 2：使用 `go:embed` 嵌入**

```go
// web_page.go
package commands

import (
    "embed"
    "net/http"
)

//go:embed web/index.html
//go:embed web/style.css
//go:embed web/app.js
var webFS embed.FS

// HandleChatWebPage 返回微型 Web 客户端页面
func HandleChatWebPage(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    data, _ := webFS.ReadFile("web/index.html")
    w.Write(data)
}
```

三种实现策略对比：

| 策略 | 方案 | 优点 | 缺点 |
| --- | --- | --- | --- |
| A | 完全独立文件，`go:embed` 嵌入，`HandleChatWebPage` 返回 `index.html`；CSS/JS 通过 `style.css` / `app.js` 路径引用 | 浏览器可缓存静态资源，开发时直接编辑文件 | 需额外处理 `/web/style.css` 和 `/web/app.js` 路由 |
| B | 单 `index.html` 含内联 `<style>` 和 `<script>`，`go:embed` 嵌入 | 保持单文件返回，路由简单 | 无缓存分离，内联代码量大 |
| C | 构建时合并（`//go:generate` 将 CSS/JS 注入 HTML） | 零运行时开销 | 需要 generate 脚本 |

**推荐策略 A**：独立文件 + `go:embed` + 新增两个静态路由。

### 3.3 路由注册变更

```go
// pprof.go
mux.HandleFunc("/web/style.css", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/css; charset=utf-8")
    data, _ := webFS.ReadFile("web/style.css")
    w.Write(data)
})
mux.HandleFunc("/web/app.js", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
    data, _ := webFS.ReadFile("web/app.js")
    w.Write(data)
})
```

### 3.4 迁移注意事项

- **相对路径引用**：`index.html` 中通过 `<link rel="stylesheet" href="style.css">` 和 `<script src="app.js">` 引用（同目录）
- **Content-Type**：CSS 和 JS 端点需正确设置 Content-Type 头
- **缓存策略**：开发阶段可加 `Cache-Control: no-cache`
- **测试更新**：现有 `TestHandleChatWebPage` 需适配新输出方式
- **go:embed 路径限制**：`embed.FS` 路径不能包含 `..` 或以 `/` 开头，目录需在包内

---

## 4. 前端体验升级

### 4.1 Markdown 渲染（最高优先级）

**现状**：streamText 直接以纯文本显示在 `<pre>` 中，Markdown 语法（`**粗体**`、`# 标题`、`` `代码` ``）原样输出。

**升级方案**：

```js
// 使用轻量级客户端 Markdown 渲染库，如 marked.js 或 micromark
// 方案 A：CDN 加载 marked.min.js（~10KB gzipped）
// 方案 B：在 app.js 中实现精简 Markdown 子集解析器

// 推荐方案 A（CDN），但需注意同源策略
// 因 loopback 无外网，需内嵌 marked.min.js 或自实现精简解析
```

因 loopback 服务器无法访问 CDN，推荐两种子方案：

| 子方案 | 实现 | 大小 | 维护 |
| --- | --- | --- | --- |
| A1 | 将 `marked.min.js` 拷贝到 `web/` 目录，用 `go:embed` 嵌入 | ~30KB | 定期更新 |
| A2 | 在 `app.js` 中实现精简 Markdown 解析（支持 `**`、`` ` ``、`[link]()`、`- list`、`# heading`） | ~5KB 新增 | 自维护 |

**推荐 A2**：自实现精简解析器，避免外部依赖，且与 aicli 自身渲染风格一致。

### 4.2 代码高亮

**现状**：代码块（```` ``` ````）在 Markdown 渲染后仍需高亮。

**方案**：在精简 Markdown 解析器中，为 ` ``` ` 代码块生成 `<pre><code class="lang-xxx">` 结构，用 CSS 实现基础关键字着色（支持 Go/Python/JS/Shell 等常见语言）。

不引入 highlight.js（避免大体积），采用 CSS 类 + 有限语法高亮（仅关键字着色、字符串高亮、注释灰化）。

### 4.3 消息历史（会话式消息流）

**现状**：
- 流式消息揭示后，`finishStream()` 将流式文本追加到 `<pre id="screen">` 中
- 屏幕内容累积在同一个 `<pre>` 中，用户难以区分多条消息
- 切换标签页或页面刷新后，消息历史丢失

**升级方案**：
- 将消息流从 `<pre>` 覆盖模式改为会话式消息列表
- 每条消息显示为独立的消息块（类似 ChatGPT 界面）
- 用户消息（prompt）和助手消息（assistant）分左右对齐
- 消息包含时间戳和角色标识
- 页面加载时通过 `GET /web/api/screen` 恢复历史消息

### 4.4 响应式布局

**现状**：固定宽度布局，手机端横向滚动。

**方案**：
- 使用 CSS Media Queries 支持 <768px 的移动端
- 小屏时侧边栏默认隐藏，通过汉堡菜单展开
- 输入框和按钮自适应宽度
- 消息区域自适应高度

### 4.5 深色模式

**现状**：仅浅色主题。

**方案**：
- 使用 CSS 自定义属性（`--bg: #fff; --text: #333` 等）
- 通过 `prefers-color-scheme: dark` 自动切换
- 提供手动切换按钮（本地存储记忆）
- 状态机的颜色适配深色主题

### 4.6 输入历史

**现状**：输入框无历史记忆。

**方案**：
- 按 ↑/↓ 键浏览已发送的 prompt（本地存储最近 50 条）
- 配合原来的 Enter 发送功能

### 4.7 打字机效果优化

**现状**：固定 20ms/char，每 tick 1 字符。

**方案**：
- 根据文本长度自适应速度（短文本快、长文本慢）
- 代码块快速揭示（跳过打字机效果）
- 推理文本与答案文本区分显示
- 可随时停止打字机（点击「停止」按钮）

---

## 5. 功能增强

### 5.1 图像生成预览

**现状**：`tool_call` 事件中可能包含 `image_progress` 事件，但前端未渲染图像。

**方案**：
- 监听 `assistant.image_progress` 事件
- 在消息流中插入图片预览区域（`<img>` 标签）
- 支持 base64 内嵌图片或 URL 图片

### 5.2 推理过程折叠

**现状**：`reasoning_delta` 事件累积在 `streamReasoning` 中，但前端展示方式与最终答案混在一起。

**方案**：
- 将推理过程渲染为可折叠面板（`<details>` / `<summary>`）
- 默认折叠，用户可点击展开查看推理链
- 与最终答案文本分离显示

### 5.3 审批面板优化

**现状**：审批面板显示在顶部，样式简陋，大段 prompt 可读性差。

**方案**：
- 审批面板改为模态对话框（居中覆盖）
- 审批 prompt 支持 Markdown 渲染
- 显示工具调用详情（工具名称、参数）
- 增加「详细信息」展开/折叠

### 5.4 会话管理增强

**现状**：会话列表显示创建时间、更新时间，可排序、切换。

**方案**：
- 增加会话预览（最后一条消息摘要）
- 增加会话删除功能（POST /web/api/sessions/delete）
- 会话搜索/过滤
- 会话重命名（POST /web/api/sessions/rename）

### 5.5 键盘快捷键

| 快捷键 | 功能 |
| --- | --- |
| Enter | 发送消息（非 busy 时） |
| Shift+Enter | 换行（若需多行输入） |
| ↑/↓ | 输入历史浏览 |
| Ctrl+K | 清空对话 |
| Esc | 取消/中断（同点击「停止」） |
| Ctrl+L | 切换深色/浅色模式 |

---

## 6. GitHub 同类项目参考

### 6.1 搜索概况

通过 GitHub API 搜索 "agent web ui sse chat"、"claude code web ui" 等关键词，获得以下参考项目：

| 项目 | Stars | 技术栈 | 关键特性 |
| --- | --- | --- | --- |
| [siteboon/claudecodeui](https://github.com/siteboon/claudecodeui) | ★13,554 | Go + 内嵌 Web UI | Claude Code 会话管理，Web 终端，文件管理，远程连接 |
| [poco-ai/poco-claw](https://github.com/poco-ai/poco-claw) | ★1,352 | Python + Vue | 美观 Web UI，内置 IM 支持，沙箱运行时，团队协作 |
| [wbopan/cui](https://github.com/wbopan/cui) | ★1,145 | Go + 前端 | Claude Code Web UI，会话管理，文件上传 |
| [hoangsonww/Claude-Code-Agent-Monitor](https://github.com/hoangsonww/Claude-Code-Agent-Monitor) | ★964 | Node.js + React + Vite + WebSocket | 实时监控仪表板，会话追踪，工具使用分析，子代理编排 |
| [JPeetz/Hermes-Studio](https://github.com/JPeetz/Hermes-Studio) | ★344 | Web UI | 聊天、记忆、技能、终端、审批、多代理编排 |
| [blksails/pi-web.old](https://github.com/blksails/pi-web.old) | ★4 | Go + 流式 Web UI | 自定义 Agent 即时 Web UI，流式聊天、工具、附件 |

### 6.2 可借鉴的设计

| 项目 | 可借鉴点 |
| --- | --- |
| claudecodeui | 会话管理 UI、终端模拟、文件浏览 |
| poco-claw | 消息气泡设计、IM 风格、团队协作 |
| Claude-Code-Agent-Monitor | 实时监控面板、工具使用分析、子代理编排可视化 |
| Hermes-Studio | 审批 UI、技能管理、多代理编排 |
| pi-web | 极简流式 UI、工具调用展示 |

### 6.3 差异化定位

aicli micro web client 的独特优势：

- **零外部依赖**：纯 Go + 内嵌前端，无 npm/bundle
- **同源安全**：loopback 端口，无需 CORS
- **SSE 驱动**：非 WebSocket，更简单可靠
- **与 aicli TUI 共享后端**：输入队列、EventBus、Session 管理完全复用
- **按钮状态机**：独特的中断/停止交互设计

---

## 7. 实现路线图

### Phase 1：架构迁移（HTML/CSS/JS 独立目录 + go:embed）

| 步骤 | 文件 | 工作内容 |
| --- | --- | --- |
| 1.1 | `commands/web/` | 新建目录，从 `web_page.go` 提取 HTML 到 `web/index.html` |
| 1.2 | `commands/web/` | 从 `web_page.go` 提取 CSS 到 `web/style.css` |
| 1.3 | `commands/web/` | 从 `web_page.go` 提取 JS 到 `web/app.js` |
| 1.4 | `web_page.go` | 改为 `go:embed` 嵌入静态文件，新增 `webFS embed.FS` |
| 1.5 | `pprof.go` | 注册 `/web/style.css` 和 `/web/app.js` 路由 |
| 1.6 | `web_handlers_test.go` | 更新 `TestHandleChatWebPage` 适配新输出 |
| 1.7 | 验证 | `go build ./cmd/aicli/...` + `go test ./cmd/aicli/commands` |

### Phase 2：前端体验升级

| 步骤 | 文件 | 工作内容 |
| --- | --- | --- |
| 2.1 | `web/app.js` | 实现精简 Markdown 解析器（支持 `**`、`` ` ``、`[link]()`、`- list`、`# heading`、```` ``` ````） |
| 2.2 | `web/style.css` | Markdown 样式 + 代码块基础高亮（Go/Python/JS/Shell） |
| 2.3 | `web/app.js` + `web/style.css` | 消息历史架构改造：从 `<pre>` 覆盖改为会话式消息列表 |
| 2.4 | `web/style.css` | 响应式布局（Media Queries，<768px） |
| 2.5 | `web/style.css` + `web/app.js` | 深色模式（CSS 自定义属性 + prefers-color-scheme + 手动切换） |
| 2.6 | `web/app.js` | 输入历史（↑/↓ 键，localStorage 存储最近 50 条） |
| 2.7 | `web/app.js` | 打字机效果优化（自适应速度、代码块快速揭示） |

### Phase 3：功能增强

| 步骤 | 文件 | 工作内容 |
| --- | --- | --- |
| 3.1 | `web/app.js` + `web/style.css` | 推理过程折叠面板（`<details>`） |
| 3.2 | `web/app.js` + `web/style.css` | 审批面板模态框改造 |
| 3.3 | `web/app.js` + `web/style.css` | 图像生成预览（`image_progress` 事件监听） |
| 3.4 | `web/app.js` | 键盘快捷键支持（Enter/↑/↓/Ctrl+K/Esc/Ctrl+L） |
| 3.5 | `web_handlers.go` + `web/app.js` | 可选：会话删除/重命名端点 |

### Phase 4：测试与稳定性

| 步骤 | 内容 |
| --- | --- |
| 4.1 | 更新 `TestHandleChatWebPage` 验证嵌入内容 |
| 4.2 | 新增 Markdown 渲染测试（Node.js 语法校验） |
| 4.3 | 消息历史持久化验证（SSE 重连后恢复） |
| 4.4 | `-race` 测试 |
| 4.5 | 手动端到端测试（浏览器打开页面，观察交互） |

---

## 8. 验收标准

| # | 验收项 | 验证方式 |
| --- | --- | --- |
| 1 | `GET /web/` 返回 HTML 页面，内嵌 CSS 和 JS 通过 `style.css`/`app.js` 独立路径加载 | 浏览器 DevTools 网络面板 |
| 2 | 修改 `web/style.css` 后重新编译，样式更新生效 | 修改颜色后重新 `go build` 并刷新 |
| 3 | Markdown 渲染正确（**粗体**、`代码`、# 标题、- 列表、`[链接]()`） | 发送含 Markdown 的 prompt |
| 4 | 代码块有高亮着色 | 发送含代码的 prompt |
| 5 | 消息以会话气泡形式展示，用户消息和助手消息左右区分 | 发送多条消息 |
| 6 | 页面刷新后消息历史恢复（通过 `GET /web/api/screen`） | 刷新页面 |
| 7 | 移动端（<768px）布局自适应，侧边栏可折叠 | 调整浏览器窗口宽度 |
| 8 | 深色模式自动（`prefers-color-scheme`）和手动切换 | 切换系统主题/点击按钮 |
| 9 | 输入历史：↑/↓ 键可浏览历史输入 | 发送多条消息后按 ↑ |
| 10 | 推理过程可折叠展示 | 观察推理流式输出 |
| 11 | 审批面板为模态对话框，支持 Markdown 渲染 | 触发审批请求 |
| 12 | 图像生成事件在前端显示预览 | 触发图像生成 |
| 13 | 键盘快捷键：Enter 发送，Esc 中断，Ctrl+K 清空，Ctrl+L 切换主题 | 手动测试 |
| 14 | 现有测试全部通过：`go test ./cmd/aicli/commands -run 'TestHandleChatWeb' -race` | 测试运行 |
| 15 | 无竞态：`go test -race ./cmd/aicli/...` | 测试运行 |

---

## 9. 附录

### A. 文件变更清单

| 文件 | 变更类型 | 说明 |
| --- | --- | --- |
| `backend/cmd/aicli/commands/web/index.html` | **新建** | HTML 骨架（从 `web_page.go` 提取） |
| `backend/cmd/aicli/commands/web/style.css` | **新建** | 全部样式（从 `web_page.go` 提取） |
| `backend/cmd/aicli/commands/web/app.js` | **新建** | 全部 JS 逻辑（从 `web_page.go` 提取） |
| `backend/cmd/aicli/commands/web_page.go` | **修改** | 移除 `chatWebPageHTML` 常量，改为 `go:embed` + `webFS` + `HandleChatWebPage` |
| `backend/cmd/aicli/pprof.go` | **修改** | 注册 `/web/style.css` 和 `/web/app.js` 路由 |
| `backend/cmd/aicli/commands/web_handlers_test.go` | **修改** | 适配新页面输出方式 |

### B. 技术选型说明

| 选项 | 选择 | 理由 |
| --- | --- | --- |
| Markdown 渲染 | 自实现精简解析器 | 无外部依赖，loopback 无法访问 CDN，控制体积 |
| 代码高亮 | CSS 类 + 有限关键字着色 | 避免引入 highlight.js（~200KB） |
| 消息存储 | 后端 `screen` 接口 + 前端 localStorage 缓存 | 保持无状态后端，前端可离线浏览 |
| 响应式 | CSS Media Queries + Flexbox | 无需额外框架，现有 CSS 改造即可 |
| 深色模式 | CSS 自定义属性（`--bg`/`--text` 等） | 零运行时开销，自动 + 手动切换 |

### C. 风险与缓解

| 风险 | 影响 | 缓解措施 |
| --- | --- | --- |
| 自实现 Markdown 解析不完整 | 部分格式无法渲染 | 先支持核心子集（`**`、`` ` ``、`[link]()`、`- list`、`#`、```` ``` ````），后续扩展 |
| go:embed 路径引用变更导致构建失败 | 构建中断 | 迁移后立即 `go build` 验证 |
| 页面结构变更影响现有测试 | 测试失败 | 更新 `TestHandleChatWebPage` 使用更宽松的匹配 |
| JS 语法错误在提取过程中引入 | 页面空白 | 使用 `node --check` 语法校验，`go build` 集成 |
| 浏览器缓存旧 CSS/JS | 开发体验差 | 路由增加 `Cache-Control: no-cache`，或加版本号参数 |

### D. 参考文件

| 文件 | 用途 |
| --- | --- |
| `docs/plan/aicli-micro-web-client-plan.md` | 原方案设计（架构、端点、SSE 事件映射） |
| `backend/cmd/aicli/commands/web_page.go` | 当前页面代码（待迁移） |
| `backend/cmd/aicli/commands/web_handlers.go` | HTTP 端点（不变） |
| `backend/cmd/aicli/pprof.go` | 路由注册入口（需增加静态路由） |
| `backend/cmd/aicli/commands/web_handlers_test.go` | 端点测试（需适配） |
| `docs/plan/aicli-micro-web-client-upgrade-plan.md` | **本文档** |