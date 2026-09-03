# aicli-render-fixture 使用手册

> 对应程序：`cmd/aicli-render-fixture`（`aicli-render-fixture.exe`，仅 Windows）
> 作用：aicli 终端渲染的 E2E 测试 fixture。在真实终端中输出预定义的 AICLI-E2E-* 标记，供自动化测试或人工验证终端渲染效果。

---

## 目录

1. [概述](#1-概述)
2. [使用条件](#2-使用条件)
3. [环境变量](#3-环境变量)
4. [输出标记](#4-输出标记)
5. [示例](#5-示例)
6. [相关文档](#6-相关文档)

---

## 1. 概述

`aicli-render-fixture` 是面向 aicli 终端 UI 渲染引擎的 E2E 测试工具。它：

- 初始化一个真实的 aicli UI 控制器（`UIController`）与终端会话（`TerminalSession`）
- 按顺序注入一系列 UI 动作：设置状态栏、显示提示符、填充历史记录（含 Markdown 混合内容）
- 在终端窗口标题中写入 `AICLI-E2E-READY-{runID}` 标记
- 保持输出一段时间（默认 30 秒）让测试框架捕获并验证渲染结果

所有输出标记以 `AICLI-E2E-` 前缀开头，便于自动化测试脚本抓取。

---

## 2. 使用条件

- **仅 Windows**（`//go:build windows`）
- 需要**真实终端**（stdout 必须是 `isatty`），否则直接退出码 2
- 终端尺寸至少 40×10

---

## 3. 环境变量

| 环境变量 | 说明 |
|----------|------|
| `AICLI_RENDER_FIXTURE_RUN_ID` | 运行标识符，写入输出标记。默认 `manual` |
| `AICLI_RENDER_FIXTURE_HOLD_MS` | 保持输出后等待的毫秒数（默认 30000ms）。设为 0 可立即退出 |

---

## 4. 输出标记

程序启动后按顺序输出以下标记（均为 stdout；终端标题使用 OSC 转义序列）：

| 标记 | 位置 | 说明 |
|------|------|------|
| `AICLI-E2E-BUFFER-{runID}` | stdout 首行 | 缓冲区已就绪 |
| `AICLI-E2E-HISTORY-{000-071}` | 历史记录内容 SRC | 共 72 条历史消息（含 Markdown 混合内容） |
| `AICLI-E2E-STATUS-VIEWPORT` | 状态栏文本 | 状态栏显示内容 |
| `AICLI-E2E-PROMPT-VIEWPORT> ` | 提示符 | 输入提示符 |
| `AICLI-E2E-MARKDOWN-HEADING` | 历史记录中的 Markdown | 标题渲染测试 |
| `AICLI-E2E-MARKDOWN-BOLD` | 历史记录中的 Markdown | 粗体渲染测试 |
| `AICLI-E2E-MARKDOWN-CODE` | 历史记录中的 Markdown | 行内代码渲染测试 |
| `AICLI-E2E-READY-{runID}` | 终端标题 | 输出就绪（OSC 转义序列 `\x1b]0;...\x07`） |

---

## 5. 示例

```powershell
# 手动运行（默认 hold 30 秒）
aicli-render-fixture

# 指定 run ID 以便抓取
$env:AICLI_RENDER_FIXTURE_RUN_ID = "test-20260902"
aicli-render-fixture

# 快速运行（hold 1 秒）
$env:AICLI_RENDER_FIXTURE_HOLD_MS = 1000
aicli-render-fixture
```

---

## 6. 相关文档

- [aicli 使用手册](aicli.md)