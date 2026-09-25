# chrome-devtools MCP 使用手册

> 对应 MCP：`chrome-devtools`（Google 官方 `chrome-devtools-mcp` npm 包，stdio 传输）
> 作用：把 Chrome / Edge 的 DevTools 能力（导航、页面快照、点击/表单、截图、控制台/网络、性能 trace、Lighthouse 等 29 个工具）通过 MCP 暴露给 aicli 会话及其它 MCP 客户端。
> 实测环境：Windows 11 + Edge 153.0.4234.32 / Chrome 153.0.8010.48（均 ≥ 144），`aicli mcp`（2026-09-18 验证）。

---

## 目录

1. [概述](#1-概述)
2. [前置条件](#2-前置条件)
3. [配置](#3-配置)
4. [连接已打开的浏览器](#4-连接已打开的浏览器)
5. [工具清单](#5-工具清单)
6. [使用示例](#6-使用示例)
7. [注意事项](#7-注意事项)
8. [故障排查](#8-故障排查)
9. [相关文档](#9-相关文档)

---

## 1. 概述

`chrome-devtools-mcp` 是 Google 官方维护的 MCP 服务器，底层通过 DevTools Protocol 驱动浏览器。它有两种运行形态：

| 模式 | 行为 | 适用场景 |
|------|------|----------|
| **自启浏览器（launch）** | MCP 自己拉起一个浏览器（可 headless、可 `--isolated` 临时 profile） | CI、无人值守自动化、不希望影响日常浏览器 |
| **连接已有浏览器（attach）** | MCP 连接到**已经打开**的 Chrome/Edge，复用其窗口、标签页与登录态 | 人工浏览器 + agent 协作、需要登录态的站点、调试真实页面 |

attach 有三种连接方式：

| 方式 | 参数 | 说明 |
|------|------|------|
| 自动连接（推荐，Chrome/Edge 144+） | `--auto-connect`（可配合 `--user-data-dir` 指定 profile） | 读取浏览器 user-data-dir 下的 `DevToolsActivePort` 文件，直连其浏览器级 WebSocket 端点 |
| 手动调试端口 | `--browser-url http://127.0.0.1:9222` | 需浏览器以 `--remote-debugging-port=9222` 启动（Chrome 136+ 还必须使用**非默认** `--user-data-dir`） |
| WebSocket 端点 | `--ws-endpoint ws://127.0.0.1:9222/devtools/browser/<id>` | 已知浏览器 WS 端点时使用；可用 `--ws-headers` 附加鉴权头 |

> 本仓库当前 `.aicli/mcp.yaml` 采用 **attach：`--auto-connect` + `--user-data-dir <Edge profile>`**，即操作日常 Edge 浏览器（见第 3 节）。

---

## 2. 前置条件

| 项 | 要求 | 检查 / 操作 |
|----|------|-------------|
| Node.js / npx | 可用（`npx -y chrome-devtools-mcp@latest` 能拉起） | `node -v`、`npx --version` |
| 浏览器版本 | attach（`--auto-connect`）需 **Chrome/Edge 144+**；低于 144 只能走手动调试端口（3.5）或 launch（3.4） | `(Get-Item "C:\Program Files\Google\Chrome\Application\chrome.exe").VersionInfo.ProductVersion`；Edge 换成 `"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"` |
| 浏览器侧手动配置 | **必需**：在运行中的浏览器里开启远程调试并允许连接（见 4.2），MCP 侧无法代替 | 打开 `chrome://inspect/#remote-debugging` / `edge://inspect/#remote-debugging` |
| 目标 profile | `--auto-connect` 只连接 `--user-data-dir` 指向的 profile；Edge、非默认 profile 必须显式指定（见 3.2） | 确认该 profile 目录下已生成 `DevToolsActivePort`（见 4.2 校验清单） |
| MCP 配置 | attach 需 `--auto-connect`；Edge 需 `--auto-connect` + `--user-data-dir`（见 3.2） | `aicli mcp list` |
| aicli | 支持 MCP 的版本（`aicli mcp` 子命令可用） | `aicli mcp --help` |

> Node 首次运行 `npx` 会下载 `chrome-devtools-mcp` 包，耗时取决于网络；建议保持 `@latest` 以获取新工具。
---

## 3. 配置

### 3.1 配置文件与优先级

MCP 配置文件按以下顺序查找（命中即用；写操作也写到命中的文件，全部不存在时落到 `~/.aicli/mcp.yaml`）：

1. `./.aicli/mcp.yaml`（工作区级）
2. `~/.aicli/mcp.yaml`（用户级）
3. 显式 `--config-file` / `-c` 指定
4. 从 cwd 逐级向上搜索 `.aicli/mcp.yaml`、`configs/mcp.yaml`
5. `configs/mcp.yaml`（兜底）

本仓库使用工作区级 `E:\projects\ai\ai-agent-runtime\.aicli\mcp.yaml`。

> **未设置 `aicli.mcp.config_file` 的语义（2026-09-25 修复）**：该键未设置（或为空、或 MCP 节整体缺失）时
> **不再**等价于“无 MCP 配置”，解析器按“发现”语义直接执行上表 1→4：工作区 `./.aicli/mcp.yaml` 命中即用，
> 其次用户级 `~/.aicli/mcp.yaml`，再向上搜索；只有磁盘上任何候选都不存在时才回到“未配置”
> （空路径，chat 静默跳过，`aicli mcp list` 报「没有配置任何 MCP 服务器」）。因此**不需要**为了使用
> 工作区配置而额外写 `config_file`；`MCP_CONFIG_FILE` 环境变量优先于 YAML 值。
> 实现：`backend/internal/aiclipaths/paths.go`（`ResolveMCPConfigPath` 的“发现”分支）+
> `backend/internal/agentconfig/config.go`（`EffectiveAICLIMCPConfigFile`），调用点为 CLI/chat、runtime-server
> 与配置热重载。
>
> 注意：不要把 `aicli.mcp.config_file` 写进**工作区** `.aicli/config.yaml`——分层合并默认关闭
> （`AICLI_CONFIG_MERGE` 未设时只读取第一个存在的配置文件），工作区配置文件会整体遮蔽用户级配置
> （providers 等全部丢失）。

### 3.2 attach：连接已打开的 Edge / Chrome（当前仓库采用）

```yaml
mcpServers:
  chrome-devtools:
    name: chrome-devtools
    description: Chrome DevTools MCP（Google 官方；attach 到已开启 remote debugging 的 Edge/Chrome 实例）
    type: stdio
    trustLevel: local
    maxParallelCalls: 1
    command: npx
    args:
      - -y
      - chrome-devtools-mcp@latest
      - --auto-connect
      - --user-data-dir
      - C:\Users\vince\AppData\Local\Microsoft\Edge\User Data
      - --no-usage-statistics
      - --no-performance-crux
    url: ""
    env: {}
    enabled: true
    disabled: false
    timeout: 2m0s
    maxRetry: 2
global:
  healthCheckInterval: 1m0s
  connectTimeout: 1m0s
```

> `--user-data-dir` 指向**目标浏览器的 profile 目录**（Edge 默认 `%LOCALAPPDATA%\Microsoft\Edge\User Data`，Chrome 默认 `%LOCALAPPDATA%\Google\Chrome\User Data`）。MCP 会读取该目录下的 `DevToolsActivePort` 完成连接（见第 4 节）。

### 3.3 attach：仅用 `--auto-connect`（Chrome stable 默认 profile）

```yaml
args: ["-y", "chrome-devtools-mcp@latest", "--auto-connect", "--no-usage-statistics", "--no-performance-crux"]
```

官方 `--auto-connect` 会按 `--channel`（默认 stable）定位 Chrome 的默认 profile；Edge 或非默认 profile 建议按 3.2 显式给 `--user-data-dir`。

### 3.4 launch：自启无头浏览器（不影响日常浏览器）

```yaml
args: ["-y", "chrome-devtools-mcp@latest", "--headless", "--isolated", "--no-usage-statistics", "--no-performance-crux"]
```

- `--headless`：无界面运行；`--isolated`：临时 profile，退出即清理。
- 该模式下 **不会**连接你已打开的浏览器；`aicli mcp test` 的一次性进程结束后页面即回收（会话内的 MCP 连接不受影响）。
- 回退到该模式：恢复备份 `E:\projects\ai\ai-agent-runtime\.aicli\mcp.yaml.bak-attach-20260918`（2026-09-18 attach 改造前的原始配置）。

### 3.5 手动调试端口方式（沙箱环境 / 非 144+ 浏览器）

```yaml
args: ["-y", "chrome-devtools-mcp@latest", "--browser-url=http://127.0.0.1:9222", "--no-usage-statistics", "--no-performance-crux"]
```

浏览器需这样启动（Chrome 136+ 强制要求非默认 user-data-dir，否则调试端口不生效）：

```powershell
& "C:\Program Files\Google\Chrome\Application\chrome.exe" --remote-debugging-port=9222 --user-data-dir="$env:TEMP\chrome-profile-debug"
```

```bash
# 若已知 WS 端点（例如来自 DevToolsActivePort 第二行）
npx -y chrome-devtools-mcp@latest --ws-endpoint "ws://127.0.0.1:9222/devtools/browser/<id>"
```

### 3.6 主要参数速查

| 参数 | 说明 |
|------|------|
| `--auto-connect` | 自动连接正在运行的 Chrome/Edge 144+（读取 `DevToolsActivePort`）；连接时浏览器可能弹出“允许调试”确认，需点 Allow |
| `--user-data-dir <dir>` | 指定目标浏览器 profile；与 `--auto-connect` 配合可精确指定 Edge/Chrome 及 profile |
| `--browser-url <url>` | 连接 `--remote-debugging-port` 暴露的调试端点（如 `http://127.0.0.1:9222`） |
| `--ws-endpoint <ws>` / `--ws-headers <json>` | 直连浏览器 WS 端点；需要鉴权头时用 `--ws-headers` |
| `--headless` / `--isolated` | 自启模式：无头 / 临时 profile（两者互相独立，可与 `--channel` 等组合） |
| `--channel <stable\|beta\|dev\|canary>` | 指定浏览器通道 |
| `--executable-path <path>` | 指定自定义浏览器可执行文件 |
| `--viewport 1280x720` | 自启模式的初始视口 |
| `--page-id-routing` / `--no-page-id-routing` | 默认开启：页面级工具带 `pageId` 参数并按页路由，适合并发会话 |
| `--log-file <path>` | 输出调试日志（配合 `DEBUG=*` 环境变量更详细） |
| `--no-usage-statistics` / `--no-performance-crux` | 关闭遥测 / 关闭 CrUX 真实用户数据（性能工具仍可用） |
| `--slim` | 只暴露 3 个工具（导航、JS 执行、截图） |

### 3.7 配置热重载

| 场景 | 方式 |
|------|------|
| 聊天会话内 | 输入 `/mcp reload`（热重载配置并重连，成功后把最新 MCP 工具重新注册进当前会话） |
| 命令行 | `aicli mcp reload` |
| 微型 Web 客户端 | `POST /web/api/mcps/reload`（MCP 页签） |
| runtime-server | `POST /api/runtime/mcps/reload` |

> 注意：`aicli mcp reload` 只对执行该命令的进程生效；如果聊天会话仍连着旧实例，请在**会话内**执行 `/mcp reload` 或重启 aicli。
---

## 4. 连接已打开的浏览器

### 4.1 工作原理

浏览器（Chrome/Edge 144+）在 `chrome://inspect/#remote-debugging` 中开启“允许远程调试”后，会在其 **user-data-dir 根目录**写入 `DevToolsActivePort` 文件：

```
9222
/devtools/browser/0f172cb8-fb5c-49db-8507-2310d18a9847
```

- 第 1 行：调试端口；第 2 行：浏览器级 WebSocket 路径。
- `--auto-connect`（配合 `--user-data-dir`）就是读取该文件并连接 `ws://127.0.0.1:<port><path>`。

> 实测（Edge 153 / 2026-09-18）：该模式下调试端口**只暴露 WebSocket 端点**，标准 CDP HTTP 发现接口不可用——
> `GET http://127.0.0.1:9222/json/version` 与 `/json/list` 均返回 `HTTP/1.1 404`（空 body）。
> 浏览器级路径 `/devtools/browser/<id>` 的 WS 握手正常返回 `101`（2026-09-19 复测；早期测到 403 是连到了错误路径）。
> 因此 **不要**对这种实例使用 `--browser-url`（会报 `Failed to fetch browser webSocket URL ... HTTP Not Found`），
> 应使用 `--auto-connect`（或已知路径时用 `--ws-endpoint`）。
>
> 实测补充（2026-09-19，Edge 153 / Node 24.14）：绕过 MCP 直接手写脚本连该 WS 端点时，
> **Node 内置全局 `WebSocket`（undici）会永久挂起**——连接建立后收不到任何帧（握手/消息事件均不触发）；
> 改用原始 `net.Socket` 手工完成 WebSocket 握手（`Sec-WebSocket-Key` + `101`）后通信正常。
> 且由于 HTTP 发现接口 404，拿不到 page 级 `webSocketDebuggerUrl`，只能通过浏览器级端点
> `Target.attachToTarget({flatten:true})` 路由会话，手工实现极易丢事件（attach 后 console/network 常抓不到）。
> **结论：页面诊断请直接用 MCP 工具（`list_pages` / `take_snapshot` / `list_console_messages` 等），不要手写 CDP 脚本。**

### 4.2 浏览器侧手动配置（Chrome / Edge，必需）

> 这是 attach 模式**唯一无法由 MCP 侧代劳**的部分：必须在目标浏览器里手动开启远程调试并授权连接，
> 否则 `--auto-connect` 会报 `Could not find DevToolsActivePort` / `Could not connect to Chrome`。

#### 4.2.1 Chrome（144+）

1. 升级并确认版本 ≥ 144（地址栏打开 `chrome://version`，或菜单「帮助 → 关于 Google Chrome」）；
2. 保持 Chrome 运行，新标签页打开 `chrome://inspect/#remote-debugging`；
3. 在页面中勾选/开启 **Enable remote debugging**（允许远程调试）；
4. 首次有 MCP 客户端连接时，浏览器会弹出授权对话框，点击 **Allow**（拒绝会表现为连接失败/超时，需重连后重新授权）；
5. 多 profile 场景：在**目标 profile 的窗口**中重复第 2–3 步，并让 MCP 的 `--user-data-dir` 指向该 profile 根目录。

#### 4.2.2 Edge（144+）

1. 同样升级到 ≥ 144（地址栏打开 `edge://version`）；
2. 保持 Edge 运行，新标签页打开 `edge://inspect/#remote-debugging`，开启 **允许远程调试 / Enable remote debugging**；
3. 首次连接时同样点击 **Allow**；
4. Edge 不在官方支持范围内（官方仅保证 Chrome / Chrome for Testing），且 `--channel` 只用于定位 Chrome 通道，
   因此 MCP 配置**必须显式**传 `--user-data-dir <Edge profile>`（默认 `%LOCALAPPDATA%\Microsoft\Edge\User Data`，本仓库配置见 3.2）。

#### 4.2.3 MCP 侧需要同步更新的配置

| 场景 | 配置改动 |
|------|----------|
| 任意 attach | 必须有 `--auto-connect`；缺少它会被当成 launch 模式（报 `The browser is already running ...`） |
| Edge / 非默认 profile | 必须加 `--user-data-dir <profile 根目录>`（不能只靠 `--channel`） |
| 浏览器版本 < 144 | 升级浏览器；或按 3.5 手动开调试端口（Chrome 136+ 必须非默认 `--user-data-dir`） |
| 换机器 / 换用户 / 换浏览器通道 | 更新 `--user-data-dir` 中的用户名与路径（配置模板里的 `<you>` 需替换为真实值） |

#### 4.2.4 校验清单（4 项全过才算完成浏览器侧配置）

1. 浏览器正在运行，且版本 ≥ 144；
2. `chrome://inspect/#remote-debugging`（Edge：`edge://inspect/#remote-debugging`）中的开关处于开启状态；
3. 目标 profile 目录下 `DevToolsActivePort` 存在且为两行（端口 + WS 路径）：

```powershell
Get-Content "$env:LOCALAPPDATA\Microsoft\Edge\User Data\DevToolsActivePort"   # Edge
Get-Content "$env:LOCALAPPDATA\Google\Chrome\User Data\DevToolsActivePort"    # Chrome
```

4. `aicli mcp list` 显示 `connected`（见 4.4），且 `aicli mcp test chrome-devtools list_pages '{}'` 能列出真实标签页。

> 远程调试开关针对**当前运行的浏览器实例（profile）**生效；浏览器重启/升级后端口与 WS 路径会变化
> （`DevToolsActivePort` 随之更新）。若连接失败，先回 inspect 页确认开关仍在（必要时重新勾选），
> 再在会话内执行 `/mcp reload` 重连。

### 4.3 需要更新配置 / 重新手动配置的典型场景

| 场景 | 需要做的事 |
|------|------------|
| 浏览器升级、重启或长时间未用后连接失败 | 回 `chrome://inspect/#remote-debugging` / `edge://inspect/#remote-debugging` 确认开关仍开启（被重置则重新勾选）→ 检查 `DevToolsActivePort` 已更新 → 会话内 `/mcp reload` |
| 换目标浏览器（Chrome ↔ Edge）或换 profile | 更新 `--user-data-dir` 为新 profile 根目录；在**新浏览器/新 profile 窗口**的 inspect 页重新开启开关与授权 |
| 首次在新机器 / 新用户上部署 | 按 3.2 修改配置模板中的 `<you>` 等占位符；按 4.2 完成浏览器侧配置 |
| 浏览器低于 144 | 升级到 144+；或改用手动调试端口（3.5）或 launch（3.4） |
| 从 launch 模式切换为 attach | 在 args 中补 `--auto-connect` 与 `--user-data-dir`，并移除 `--headless` / `--isolated` |
| 同时运行多个 MCP 客户端 / 多个 agent 会话 | 同一浏览器只保持一个活跃调试连接；新连接会再次弹出 Allow，需在浏览器中确认 |
| 浏览器标签页很多（尤其冻结/未加载标签） | Chrome ≤ 149 已知问题：连接可能超时（官方 issue #1921）；减少标签页或先关闭不用的窗口 |

### 4.4 验证连接

```bash
# 连接状态 + 工具数（应为 connected / 29）
aicli mcp list
aicli mcp status chrome-devtools

# 列出浏览器中真实打开的页面（attach 成功的直接证据）
aicli mcp test chrome-devtools list_pages '{}'
```

输出示例（attach 到日常 Edge）：

```
## Pages
1: Inspect with Edge Developer Tools (edge://inspect/#remote-debugging) [selected]
2: aicli micro web client (http://127.0.0.1:63889/web/)
3: AI Agent Runtime Console (http://localhost:5193/runtime/config)
...
```

---

## 5. 工具清单

共 **29** 个工具（`chrome-devtools-mcp@latest`，随版本可能增加）。页面级工具默认要求传 `pageId`（`--page-id-routing` 默认开启），可用 `list_pages` / `new_page` 获取页码。

| 分类 | 工具 | 说明 |
|------|------|------|
| 页面/导航 | `new_page` | 打开新标签页并加载 URL |
| | `navigate_page` | 跳转/后退/前进/刷新 |
| | `select_page` | 选定后续工具调用的目标页 |
| | `list_pages` | 列出浏览器中打开的页面 |
| | `close_page` | 关闭指定页面（最后一个页面不可关闭） |
| | `resize_page` | 调整页面窗口尺寸 |
| | `wait_for` | 等待页面上出现指定文本 |
| 快照/截图 | `take_snapshot` | 基于 a11y 树的文本快照（含 uid，优先于截图） |
| | `take_screenshot` | 页面或元素截图 |
| 交互 | `click` | 点击元素 |
| | `hover` | 悬停元素 |
| | `fill` | 输入文本 / 选择下拉项 |
| | `fill_form` | 一次填充多个表单元素（推荐优先使用） |
| | `type_text` | 向已聚焦元素键入文本 |
| | `press_key` | 按键/组合键 |
| | `drag` | 拖拽元素 |
| | `upload_file` | 上传文件 |
| | `handle_dialog` | 处理浏览器对话框 |
| 脚本/仿真 | `evaluate_script` | 在页面中执行 JS（返回值需可 JSON 序列化） |
| | `emulate` | 仿真设备/网络/时区等特性 |
| 控制台/网络 | `list_console_messages` / `get_console_message` | 控制台消息列表 / 单条详情 |
| | `list_network_requests` / `get_network_request` | 网络请求列表 / 单条详情（含请求/响应头、Cookie） |
| 性能与审计 | `performance_start_trace` / `performance_stop_trace` | 性能 trace 录制（Core Web Vitals） |
| | `performance_analyze_insight` | 分析 trace 中的具体 insight |
| | `lighthouse_audit` | Lighthouse 审计（可访问性/SEO/最佳实践等，不含性能项） |
| 内存 | `take_heapsnapshot` | 抓取堆快照（排查内存泄漏） |
---

## 6. 使用示例

### 6.1 命令行（`aicli mcp test`）

```bash
# 打开新页面（attach 模式下会在你真实的浏览器里开新标签；timeout 单位毫秒）
aicli mcp test chrome-devtools new_page '{"url":"https://www.baidu.com","timeout":30000}'

# 列出页面，拿到页码
aicli mcp test chrome-devtools list_pages '{}'

# 对指定页面做 a11y 快照（page-id routing 开启时需传 pageId）
aicli mcp test chrome-devtools take_snapshot '{"pageId":2}'

# 截图（可指定保存路径与格式）
aicli mcp test chrome-devtools take_screenshot '{"pageId":2,"filePath":"E:/tmp/shot.png"}'
```

### 6.2 会话内调用（推荐）

在 aicli 聊天中直接用自然语言下达任务，agent 会经工具搜索发现 `chrome-devtools` 工具并调用：

```text
打开百度并搜索“ai agent runtime”，把前三条结果的标题和链接整理成表格。
检查 http://localhost:5193/runtime/config 的控制台报错和失败请求。
对 https://developers.chrome.com 录制一次性能 trace，给出 LCP/INP 结论。
```

> 会话内调用共享**同一个** MCP 连接（同一个浏览器实例），页面状态、登录态可持续复用。

### 6.3 典型工作流

```text
1) list_pages / new_page          → 确定目标页面（拿到 pageId）
2) take_snapshot                  → 获取元素 uid（优先于截图）
3) fill / fill_form / click       → 交互
4) take_snapshot / wait_for       → 校验结果
5) list_console_messages / list_network_requests → 排查前端错误
6) performance_start_trace → … → performance_stop_trace → performance_analyze_insight → 性能结论
```

### 6.4 多会话并发

`--page-id-routing`（默认开启）要求页面级工具显式传 `pageId`，多个 agent 会话可同时连同一个浏览器并各自路由到不同标签页；若希望“只操作当前选中页”，用 `--no-page-id-routing` 关闭。

---

## 7. 注意事项

### 7.1 安全与隐私（重要）

- **attach 模式下 agent 拥有对真实浏览器的完全控制能力**：可读取任意已打开页面的内容、Cookie/登录态、网络流量，并可执行 JS、点击、上传文件。请勿在 agent 运行期间让浏览器停留在敏感页面（网银、后台、密钥管理页等）。
- 手动端口方式（`--remote-debugging-port`）会向本机所有程序开放调试入口（任意本地应用可连接并控制浏览器）；Chrome 136+ 出于该原因**强制要求非默认 user-data-dir**，即调试实例与日常 profile 隔离。
- `--auto-connect` 首次连接会在浏览器中弹出授权对话框，只有点击 **Allow** 才会建立连接；被拒绝时 MCP 侧表现为连接失败/超时。
- 遥测：默认开启使用统计与 CrUX（性能 trace 会把 URL 上报 CrUX）；本仓库配置已加 `--no-usage-statistics --no-performance-crux`。

### 7.2 行为与生命周期

- **不会关闭被连接的浏览器**：`chrome-devtools-mcp` 对 attach 的实例执行 `disconnect()` 而非 `close()`（源码 `build/src/browser.js` 的 `closeBrowser` 分支），MCP 进程退出不会杀掉你的浏览器；浏览器也不会因 MCP 退出而关闭标签页。
- **浏览器重启后需重连**：`DevToolsActivePort` 的端口/WS 路径会变化，会话内执行 `/mcp reload`（或重启 aicli）后重新读取。
- **浏览器侧配置需随环境更新**：浏览器升级/重启、切换 profile 或切换浏览器（Chrome ↔ Edge）后，`chrome://inspect/#remote-debugging`（Edge：`edge://inspect/#remote-debugging`）开关可能被重置，需按 4.3 重新检查并授权后再 `/mcp reload`。
- **attach 模式的工具子集差异**：扩展（extension）与 PWA 相关工具在“连接已有实例”下不可用（Chrome < 149 限制）；需要这些工具时使用 launch 模式。
- **`aicli mcp test` 的一次性实例**：每次命令都会拉起独立的 MCP server 进程；在 launch 模式下它自己的浏览器会随进程退出而回收，而 attach 模式下则连的是同一个目标浏览器（页面不会丢）。
- **冷启动耗时**：首次 `new_page`/`navigate_page` 触发浏览器启动或页面首次加载时，默认 10s 导航超时可能不足，建议传 `"timeout":30000`，或先 `list_pages` 预热。

### 7.3 模式选择建议

| 需求 | 建议模式 |
|------|----------|
| 让 agent 操作“我正登录着的站点” | attach（`--auto-connect` + `--user-data-dir`） |
| 隔离环境 / 不想影响日常浏览器 | launch（`--headless` + `--isolated`） |
| 沙箱内连接宿主浏览器 | attach（`--browser-url` 或 `--ws-endpoint`） |
| 只用导航/JS/截图（省上下文） | launch 或 attach + `--slim` |
---

## 8. 故障排查

| 现象/报错 | 原因 | 处理 |
|-----------|------|------|
| `/json/version`、`/json/list` 等所有 HTTP 端点返回 404（空 body），但端口在监听 | 目标浏览器是「inspect 远程调试」模式：只暴露浏览器级 WS 端点，无 CDP HTTP 发现接口（非故障，属预期安全行为） | 不用 HTTP 发现：MCP 配置用 `--auto-connect --user-data-dir <profile>`（内部读 `DevToolsActivePort` 直连 WS）；手工脚本可读 `DevToolsActivePort` 第二行拿 WS 路径 |
| Node ≥ 22 全局 `WebSocket`（undici）连浏览器 WS 端点后无响应/超时 | Node 内置 WebSocket 对该端点兼容性问题：TCP 已连上但握手帧不回 | 用原始 `net.Socket` 手工 WebSocket 握手，或安装 `ws` 包；**更推荐直接改用 MCP 工具，放弃手写 CDP** |
| `Failed to fetch browser webSocket URL from http://127.0.0.1:9222/json/version: HTTP Not Found` | 同 HTTP 404 问题：inspect 模式实例却配置了 `--browser-url` | 改用 `--auto-connect --user-data-dir <profile>`（或已知路径时 `--ws-endpoint`）；或按 3.5 以调试端口 + 非默认 profile 重启浏览器后再用 `--browser-url` |
| `The browser is already running for <user-data-dir>. Use --isolated ...` | 只传了 `--user-data-dir`（缺 `--auto-connect`），MCP 走「自启浏览器」路径与已开实例冲突 | 补上 `--auto-connect`（attach），或加 `--isolated`（自启独立实例） |
| `Could not connect to Chrome. Check if Chrome is running.` | 浏览器未运行 / 未开远程调试 / profile 路径不对 | 确认浏览器在运行、`chrome://inspect/#remote-debugging`（Edge：`edge://inspect/#remote-debugging`）开关已开、路径与实际 profile 一致 |
| `Could not find DevToolsActivePort`（或 `Could not connect to Chrome in <dir> ...`） | 目标 profile 从未开启远程调试，目录下无 `DevToolsActivePort` | 在目标浏览器开启开关后重试；确认 `--user-data-dir` 指向 profile 根目录 |
| inspect 页找不到“允许远程调试 / Enable remote debugging”开关 | 浏览器低于 144，或打开的地址与浏览器不匹配 | 升级到 144+；确认 Edge 用 `edge://inspect/#remote-debugging`、Chrome 用 `chrome://inspect/#remote-debugging` |
| `ProtocolError: Network.enable timed out` / `The socket connection was closed unexpectedly`（`--auto-connect`） | 与运行中的浏览器握手失败：未开开关、未点 Allow、有其他客户端争抢同一调试连接，或大量冻结/未加载标签页（Chrome ≤ 149 已知问题） | 按官方顺序排查：浏览器已运行 → inspect 开关已开 → 已在弹窗点 Allow → 无其他 MCP/工具连接同一浏览器；并减少标签页/关闭不用的窗口（官方 issue #1921） |
| 开关已开启但仍无 `DevToolsActivePort` | 开关开在了别的 profile 窗口，或 `--user-data-dir` 指向错误目录 | 在目标 profile 的窗口重新开启；按 4.2.4 的命令逐个 profile 检查文件 |
| 首次 `new_page` 报 `Navigation timeout of 10000 ms exceeded` | 浏览器/页面冷启动超过默认 10s | 传 `"timeout":30000`；或先调 `list_pages` 预热 |
| `missing required argument(s): pageId` | 开启了 page-id routing，页面级工具必须带页码 | 先 `list_pages` 取页码，再传 `{"pageId":N}` |
| 只看到约 9 个工具 | 客户端以只读模式加载该 MCP | 检查客户端的只读/权限设置 |
| 工具列表为空 / 连接状态异常 | 配置未生效或需要重连 | 会话内 `/mcp reload`；`aicli mcp list` / `aicli mcp tools chrome-devtools` 复查 |
| 需要更多诊断信息 | — | 配置中加 `--log-file E:\tmp\cdmcp.log`，并设置环境变量 `DEBUG=*` |

---

## 9. 相关文档

- [docs/mcp/README.md](README.md) — 本目录索引
- [docs/mcp/chrome-devtools.mcp.yaml.example](chrome-devtools.mcp.yaml.example) — 配置模板（attach / launch / browser-url）
- [docs/aicli/install.md](../aicli/install.md) — `aicli mcp` 子命令、配置文件优先级、`/mcp` 会话内命令
- [docs/user-guide/aicli.md](../user-guide/aicli.md) — aicli 使用手册（MCP 章节）
- 官方文档：
  - <https://github.com/ChromeDevTools/chrome-devtools-mcp>（README / 工具清单）
  - <https://github.com/ChromeDevTools/chrome-devtools-mcp/blob/main/docs/advanced-usage.md>（连接运行中的浏览器、并发会话）
  - <https://github.com/ChromeDevTools/chrome-devtools-mcp/blob/main/docs/troubleshooting.md>
  - <https://developer.chrome.com/blog/remote-debugging-port>（Chrome 136+ 默认 profile 远程调试限制）

---

## 附录：本仓库变更记录

| 日期 | 变更 |
|------|------|
| 2026-09-18 | `.aicli/mcp.yaml` 的 `chrome-devtools` 由 launch（`--headless --isolated`）切换为 attach（`--auto-connect --user-data-dir <Edge profile>`）；原配置备份于 `.aicli/mcp.yaml.bak-attach-20260918`。实测 Edge 153 连接成功并列出真实标签页。 |
| 2026-09-19 | 跨项目复用验证：将本配置复制到 `E:\projects\ai\ai-sites-client\.aicli\mcp.yaml`（workspace 级，已被该仓库 gitignore），会话重启后 MCP 直接生效，用 `list_pages`/`take_snapshot`/`list_console_messages` 一次定位 React 白屏（根因：`useToast` 未包在 `<ToastProvider>` 内）。同时修正 4.1 实测注记（WS 握手实际为 101 而非 403），新增两条排查经验：① inspect 模式下所有 CDP HTTP 发现端点 404 属预期行为；② Node ≥22 内置 WebSocket 连该端点会挂起，手写 CDP 脚本不可靠，页面诊断应直接使用 MCP 工具。已同步到第 4.1 节与第 8 节排查表。 |
| 2026-09-19 | 可用参考脚本留档：绕过 Node 内置 WebSocket 挂起问题的手工实现归档于本目录 [list-pages.js](list-pages.js)（Node 原始 `net.Socket` + 手工 WebSocket 握手 + 最小帧编解码 + 客户端掩码，经浏览器级 WS 端点调 `Target.getTargets` 成功列出真实标签页）。要点：① 用 `Sec-WebSocket-Key` 随机 16 字节 base64，校验服务端返回 `101`；② 客户端帧必须带掩码（4 字节 XOR）；③ 支持 126/127 扩展长度；④ HTTP 发现接口不可用时，WS 路径从 user-data-dir 下 `DevToolsActivePort` 第二行读取；⑤ 该脚本仅适合拿 target 列表等简单 CDP 调用，attach 后的 console/network 事件路由手工实现仍会丢事件——复杂诊断务必用 MCP 工具。同目录 [probe-devtools.js](probe-devtools.js)（握手探测）与 [debug-page.js](debug-page.js)（失败的反例）仅供参考/留证。注：脚本中的 `DevToolsActivePort` 路径硬编码为作者本机用户目录，复用时需按实际 profile 路径修改。 |
| 2026-09-18 | 新增「Chrome/Edge 浏览器侧手动配置 / 需要更新配置」内容：4.2 分浏览器步骤（Chrome / Edge）、MCP 配置同步项与 4 项校验清单；4.3 需重新手动配置的典型场景；第 2 节前置条件改为可操作检查项；第 7/8 节补充环境变更重配提示与 inspect 开关缺失、`--auto-connect` 握手超时、profile 选错等排错条目。本机实测：Edge 153.0.4234.32 已生成 `DevToolsActivePort`，Chrome 153.0.8010.48 未开启远程调试（无该文件）。 |
| 2026-09-25 | 按本手册重做安装校验（Node 24.14.0 / Edge 153.0.4234.48 / aicli v0.4.5）：浏览器侧 4 项校验全过（Edge 运行中、inspect 开关已开、`DevToolsActivePort` 两行、attach 成功）；`aicli mcp list` → `connected`、工具数 **30**（`@latest` 已由 29 增至 30，第 5 节表格为 29 项基线，以实际 `aicli mcp tools` 为准）；`aicli mcp test chrome-devtools list_pages '{}'` 列出真实标签页。 |
| 2026-09-25 | 修复配置解析缺陷：`aicli.mcp.config_file` 未设置（或 MCP 节缺失）时被当作“无 MCP 配置”，工作区 `./.aicli/mcp.yaml` 被忽略、静默退化到 `configs/mcp.yaml` 空兜底——与 3.1/install.md 承诺的优先级矛盾。`aiclipaths.ResolveMCPConfigPath` 增加“发现”语义（空值即按 工作区 > 用户 > 向上搜索 命中，皆无才回到“未配置”），CLI/chat、runtime-server、配置热重载三处调用点同步去掉 nil/空值短路；同时补齐 `MCP_CONFIG_FILE` 环境变量支持（`agentconfig.EffectiveAICLIMCPConfigFile`，此前 `env:` 标签只是声明、实测无效）。验证：无任何 `config_file` 配置时 `aicli mcp list` → `connected`/30 工具、`list_pages` 列出真实标签页；临时修复（用户级 `aicli.mcp.config_file: configs/mcp.yaml`）已从 `~/.aicli/config.yaml` 移除；新增回归用例（`paths_test.go`、`mcp_config_resolution_test.go`、`main_mcp_resolution_test.go`、`mcp_config_file_test.go`）。 |
