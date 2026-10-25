# 07 · 桌面端体验

> 目标平台：Windows（当前主验证目标）、macOS、Linux  
> 原则：Web 控制台仍是唯一业务界面；桌面壳只补窗口、托盘、通知和系统集成。

## 1. 窗口生命周期

| 操作 | 行为 |
|------|------|
| 首次启动 | 显示主窗口 |
| 再次启动 | 不创建第二个进程，唤醒并聚焦已有窗口 |
| 点击关闭按钮 | 默认隐藏到系统托盘，不退出进程 |
| 托盘“显示主窗口” | 恢复窗口、取消最小化并聚焦 |
| 托盘“隐藏到托盘” | 隐藏窗口，继续接收通知 |
| 托盘“重新加载控制台” | 唤醒窗口并刷新当前 WebView |
| 托盘“退出” | 结束进程，不再拦截关闭 |
| 关闭到托盘不可用 | 自动退化为普通关闭，避免窗口消失后无法找回 |

“关闭窗口时最小化到托盘”是持久化偏好，默认开启。首次隐藏时会发送一次
系统通知说明应用仍在后台运行；该提示只出现一次。

## 2. 系统托盘菜单

```text
显示主窗口
隐藏到托盘
重新加载控制台
发送测试通知
────────────────
开机启动（最小化到托盘）     [✓/空]
关闭窗口时最小化到托盘       [✓/空]
────────────────
退出
```

交互约定：

- 左键单击托盘图标：显示并聚焦主窗口。
- 右键单击：打开菜单。
- 双击托盘图标：显示并聚焦主窗口。
- 菜单使用完整文字标签，不依赖图标识别。

## 3. 单实例与窗口状态

- 单实例使用 Tauri `single-instance` 插件；第二次启动只把参数交给首实例，
  然后立即退出。
- 第二次启动默认唤醒主窗口；传 `--quit` 可请求已有实例退出。
- 窗口尺寸、位置、最大化和全屏状态持久化。
- 不持久化“可见/隐藏”状态：手动启动必须可见；开机启动使用
  `--minimized` 显式隐藏到托盘。
- 最小窗口尺寸为 `900×600`；首次启动居中。

## 4. 开机启动

“开机启动（最小化到托盘）”通过操作系统原生自启动机制注册，并附带
`--minimized`：

```text
Windows: 当前用户 Run 注册项
macOS:   LaunchAgent（Tauri 默认）
Linux:   XDG autostart
```

开发构建中该菜单不写注册表/自启动项，避免把 `target/debug` 注册进去。
发布构建中菜单勾选状态来自系统的真实状态；切换失败会回滚勾选。

## 5. 通知点击

所有 Tauri 原生通知都注册了统一 action handler：

- 点击通知：唤醒并聚焦主窗口。
- 若通知 `extra` 含 `node_id` / `session_id`，导航到：

  ```text
  {gateway}/n/{node_id}/?session={session_id}
  ```

- 若通知 `extra` 含同主机 `url`，优先使用该 URL。
- 只允许网关主机和 HTTPS（loopback 开发可用 HTTP），防止通知载荷把
  WebView 导航到外部站点。
- 通知正文仍只包含节点、会话标题和 attention 标签，不含聊天正文。

## 6. 本地状态与日志

桌面偏好文件：

```text
<app-config-dir>/desktop-settings.json
```

Windows 通常位于：

```text
%APPDATA%\online.ttai.aicli.gateway\desktop-settings.json
```

字段：

```json
{
  "close_to_tray": true,
  "close_hint_shown": true
}
```

窗口状态由 `tauri-plugin-window-state` 保存在同一应用配置目录。

启动与桌面行为日志：

```text
%TEMP%\aicli-gateway-app.log
```

日志包含：

```text
system tray ready
window hidden to system tray
second instance launched
notification action
notification deep link
started minimized to system tray
```

## 7. 启动参数

| 参数/环境变量 | 行为 |
|---------------|------|
| `--minimized` / `--background` / `--hidden` / `--autostart` | 启动后隐藏到托盘 |
| `--quit`（第二实例） | 请求已有实例退出 |
| `AICLI_START_MINIMIZED=1` | 环境变量方式启动到托盘 |
| `AICLI_GATEWAY_URL` | 网关地址；通知深链也使用该主机 |

## 8. 验收清单

- [x] 托盘图标和中文菜单可见
- [x] 左键托盘唤醒窗口
- [x] 关闭按钮隐藏到托盘且进程存活
- [x] 托盘“退出”能结束进程
- [x] 第二次启动不产生第二个常驻进程
- [x] 第二次启动唤醒已有窗口
- [x] 窗口尺寸/位置恢复
- [x] 开机启动开关使用系统真实状态
- [x] 通知点击唤醒窗口
- [x] 通知带 node/session 时导航到会话
- [x] 关闭到托盘提示只出现一次
- [ ] Windows 安装包下的通知点击与开机启动待实机复验
- [ ] macOS Dock/菜单栏行为待实机验证
- [ ] Linux AppIndicator 左键事件兼容性待真机验证