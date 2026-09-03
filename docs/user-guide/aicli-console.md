# aicli-console 使用手册

> 对应程序：`cmd/aicli-console`（`aicli-console.exe`，仅 Windows）
> 作用：aicli 的原生 Windows 控制台启动器（launcher）。当终端是管道/PTY 类（如 MobaXterm、mintty）时创建新的 conhost 窗口；在真实 Windows Console（cmd / PowerShell）中则直接沿用当前控制台。

---

## 目录

1. [概述](#1-概述)
2. [使用场景](#2-使用场景)
3. [参数](#3-参数)
4. [示例](#4-示例)
5. [行为细节](#5-行为细节)
6. [相关文档](#6-相关文档)

---

## 1. 概述

`aicli-console` 解决的是「第三方终端（MobaXterm / mintty / 其它 Cygwin/MSYS 终端）下原生 Windows 程序拿不到真实控制台句柄」的问题。

它本身不提供聊天功能，只负责：

1. 解析启动器专属参数（`--target`）。
2. 解析出要启动的 aicli 可执行文件路径。
3. 通过 `consolehost.RunWithConsole` 在当前是真实 Windows Console 时直接执行，在 pipe/PTY 环境下用 `CREATE_NEW_CONSOLE` 开新窗口重启目标程序。
4. 将其余所有参数**原样透传**给 aicli，并返回其退出码。

---

## 2. 使用场景

- **MobaXterm / mintty 用户**：`aicli` 直接启动时可能因 stdin/stdout 是管道而无法使用方向键/全屏渲染；改用 `aicli-console` 会弹出原生 conhost 窗口，获得完整 Windows Console 支持。
- **快捷方式 / 打包分发**：安装目录内同时提供 `aicli.exe` 与 `aicli-console.exe`，桌面快捷方式指向后者即可获得最佳体验。
- **构建脚本**：构建时可通过 `--target` 指定其它版本的 aicli 可执行文件（如 Win7 兼容版）。

---

## 3. 参数

启动器只消费以下参数，其余全部转发给目标程序：

| 参数 | 说明 |
|------|------|
| `--target <可执行文件路径>` | 指定要启动的 aicli 可执行文件。支持 `--target PATH` 与 `--target=PATH` 两种写法。可重复出现，**最后一次生效**。指向启动器自身时报错（防递归） |
| `--` | 分隔符：`--` 之后的参数（包括字面的 `--target`）一律原样转发给 aicli，不再被启动器消费 |

环境变量：

| 环境变量 | 说明 |
|----------|------|
| `AICLI_CONSOLE_TARGET` | 指定目标 aicli 可执行文件路径（`--target` 未给出时作为回退） |

目标解析顺序（从高到低）：

1. `--target` 命令行参数
2. `AICLI_CONSOLE_TARGET` 环境变量
3. 启动器同目录下的 `aicli.exe` / `aicli`
4. `PATH` 中的 `aicli.exe` / `aicli`

找不到目标时打印错误并返回退出码 1。

---

## 4. 示例

```powershell
# 直接启动（同目录 aicli.exe）
aicli-console

# 启动并带一个参数给 aicli（进入指定会话的 chat）
aicli-console chat --session <session-id>

# 显式指定目标程序
aicli-console --target "D:\apps\aicli\aicli-win7.exe"

# 透传字面 --target 给 aicli（放在 -- 之后）
aicli-console -- chat --target something
```

---

## 5. 行为细节

| 场景 | 行为 |
|------|------|
| 当前终端是真实 Windows Console（cmd / PowerShell / conhost 直接运行） | 直接在当前控制台运行目标程序，不新开窗口 |
| 当前终端是 pipe/PTY（MobaXterm、mintty 等） | `CREATE_NEW_CONSOLE` 新开 conhost 窗口运行目标程序 |
| `--target` 指定路径不存在 | 打印错误到 stderr，退出码 1 |
| 参数解析错误（如 `--target` 后缺值） | 打印错误到 stderr，退出码 2 |
| 目标程序正常退出 | 返回目标程序的退出码 |

> 说明：`aicli` 自身也内置了 `--console-host` 自举逻辑（见 aicli 手册）；`aicli-console` 是独立的原生启动器，两者解决同一问题，可任选其一。使用 `aicli-console` 时目标 aicli 不需要再加 `--console-host`。

---

## 6. 相关文档

- [aicli 使用手册](aicli.md)
- [docs/aicli/quickstart.md](../aicli/quickstart.md)
- [docs/aicli/windows7.md](../aicli/windows7.md)
