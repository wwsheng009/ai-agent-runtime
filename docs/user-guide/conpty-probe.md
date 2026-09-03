# conpty-probe 使用手册

> 对应程序：`cmd/conpty-probe`（`conpty-probe.exe`，仅 Windows）
> 作用：探测本机 ConPTY（Pseudo Console）API 是否可用，用于验证 Windows 终端子系统的兼容性。

---

## 目录

1. [概述](#1-概述)
2. [使用条件](#2-使用条件)
3. [输出说明](#3-输出说明)
4. [示例](#4-示例)
5. [相关文档](#5-相关文档)

---

## 1. 概述

`conpty-probe` 是 Windows 平台上的 ConPTY 可用性探测工具。它直接调用 `kernel32.dll` 的 `CreatePseudoConsole`、`ResizePseudoConsole`、`ClosePseudoConsole` 等 API，尝试创建一个伪终端并在其中运行一个简单的命令（`cmd /c echo CONPTY_PROBE_OK`），然后读取输出并与预期值比较。

**用途**：

- 验证 Windows 10 1809+ 的 ConPTY 功能是否完整可用
- 诊断 aicli 或 aicli-console 在第三方终端（MobaXterm、mintty 等）下渲染异常的根本原因
- 作为 CI 或首次运行时的环境检查脚本

---

## 2. 使用条件

- **仅 Windows**（`//go:build windows`）
- 需要 Windows 10 1809（October 2018 Update）以上版本，或 Windows Server 2019+
- 无需管理员权限

---

## 3. 输出说明

程序成功时输出类似：

```
commandLine="C:\Windows\System32\cmd.exe" /c echo CONPTY_PROBE_OK
started pid=1234
[read 18 bytes] "CONPTY_PROBE_OK\r\n"
wait code=0 err=<nil> output="CONPTY_PROBE_OK\r\n"
done, total output="CONPTY_PROBE_OK\r\n"
CONPTY_PROBE_OK
```

关键行：

- `started pid=...` — 进程创建成功
- `[read N bytes] "..."` — 逐次读取的子进程输出
- `wait code=0` — 子进程正常退出（退出码 0）
- 最后一行输出 `CONPTY_PROBE_OK` — 全部功能正常

失败时输出错误信息（如 `CreatePseudoConsole not found`）并退出码 1。

---

## 4. 示例

```powershell
# 直接运行（无需参数）
conpty-probe
```

成功时退出码 0，失败时退出码 1。

---

## 5. 相关文档

- [aicli-console 使用手册](aicli-console.md)
- [aicli-render-fixture 使用手册](aicli-render-fixture.md)