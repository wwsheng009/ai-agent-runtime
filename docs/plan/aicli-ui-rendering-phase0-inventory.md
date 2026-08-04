# aicli UI Rendering Phase 0 Inventory

状态: **Phase 0 inventory complete（历史基线）**；后续内容渲染迁移转入实施审查，物理 writer/Scene 审计转入统一 TUI 架构计划

更新时间: **2026-07-31**

本文对应 [aicli-ui-ux-rendering-codex-reference-plan.md](./aicli-ui-ux-rendering-codex-reference-plan.md) Phase 0。
实施后的内容渲染缺陷收口见 [aicli-ui-rendering-implementation-review.md](./aicli-ui-rendering-implementation-review.md)；owned viewport 当前实现见 [aicli-tui-p5-owned-viewport-design.md](./aicli-tui-p5-owned-viewport-design.md)；全量 raw/direct writer、Scene、single-owner 和 terminal lifecycle 审计见 [aicli-tui-unified-render-architecture-refactor-plan.md](./aicli-tui-unified-render-architecture-refactor-plan.md)；主界面、scrollback handoff 和 `Ctrl+T` pager 的具体迁移见 [aicli-tui-transcript-overlay-renderer-mode-plan.md](./aicli-tui-transcript-overlay-renderer-mode-plan.md)。

> 本文的 direct `fmt.Print` 列表是 Phase 0 的抽样和内容渲染迁移记录，不是 owned interactive 生命周期的完整 writer allowlist。该范围的可执行 P0 debt inventory / regression fence 位于 `commands/chat_command_result_test.go` 的 `TestChatInteractiveDirectWriterInventory`；详见统一架构计划的“实施状态”和 §14.2。

## 1. 颜色硬编码与 Sprint 调用

| 区域 | 模式 | Owner / 迁移标记 |
| --- | --- | --- |
| `ui/theme.go` | 原 `*color.Color` 公开字段 | 已删除；Theme 只保留 mode/name、图标与边框字符，semantic style 唯一 owner 为 `style.Palette` |
| `ui/theme_presets.go` | 原 preset 直接写 fatih colors | 已删除重复颜色 overlay；仅管理 palette/mode 选择、描述和预览入口 |
| `ui/theme_render.go` | unknown timeline 的字符串关键词兼容 | known/unknown fallback 均输出 typed spans；只保留 `LegacyTimelineParser` 的字符串识别入口 |
| `ui/toolcall.go` | 原包级 `color.New(...)` 全局变量 | 已删除；typed tool cell 走 `CurrentThemeContext` |
| `formatter/markdown.go` | Goldmark/Chroma | **唯一 Markdown owner**；字符串适配器注入 live ThemeContext，旧 line scanner/helper 已删除 |
| `ui/output.go` | FormatOutput / FormatCodeBlock / HighlightKeywords | 公开字符串 API 保留为 Document encoder adapter；对应 Document API 已落地 |
| `ui/statusbar.go`、`ui/progress.go` | 独立组件 | 已迁 typed role/Document/profile；任意 color callback 字段和 API 已删除 |
| `commands/*` | 菜单、session、exec 与 runtime timeline 输出 | 已改 typed spans / trusted-SGR parser / RenderDocument；runtime bridge 直接传 `TimelineEvent`/`Document`；生产审计无直接 Theme Sprint |

## 2. 宽度 helper 分叉

| 实现 | 位置 | 状态 |
| --- | --- | --- |
| `messageDisplayWidth` / `DisplayWidth` | `ui/message.go` | legacy 公共兼容；新 Document/surface 路径使用 `render.Width` |
| `displayWidth` / `runeWidth` | `formatter/markdown.go` | 已删除，统一使用 `render.Width` |
| editor visual width | `ui/inputbox_editor.go` | 输入专用；应对齐 `render.Width` |
| `truncateFixedStatusLine` | `ui/fixed_bottom_surface.go` | 已删除，状态行统一使用 `StatusLineDocument` 的 folding/truncate |
| **`render.Width` / `Wrap` / `Truncate`** | `ui/render/width.go` | **唯一新标准** |

## 3. Markdown / JSON formatter 所有权

| API | Owner | 迁移标记 |
| --- | --- | --- |
| `formatter.MarkdownFormatter.Format` | **canonical** → Goldmark + Chroma（`ui/markdown`） | `ThemeContextProvider` 跟随实际 palette/profile；legacy line helpers 已删除 |
| `ui.FormatMarkdown` / `FormatMarkdownDocument` | adapter → formatter/markdown | Phase 2 已接结构化 Document |
| `ui.FormatJSON` | `encoding/json` pretty print | **fixed in Phase 0**；失败原样返回 |
| `ui.FormatCodeBlock` | compatibility adapter | `CodeBlockDocument` 使用 Chroma token spans，无 fence 污染 |
| `ui.FormatOutput` | compatibility adapter | `FormatOutputDocument` 统一行号、缩进、cell-width wrap |

## 4. 直接 fmt.Print 路径（抽样）

- `ui/separator.go` / `ui/status.go` / `ui/message.go` / `ui/layout.go` — 已改 Document + Resolver/profile + `WriteTerminal*`
- `ui/fixed_bottom_surface.go` — surface owner，继续持有光标写入；状态、活动带和 notice 内容均走同一 ThemeContext
- `ui/statusbar.go` / `ui/progress.go` / `ui/welcome.go` — typed Document + Resolver/profile + `WriteTerminal*`
- `commands/exec_event_processor.go` — typed semantic parts；stream delta 消毒后按 Assistant role 输出
- `commands/chat_*.go` — selection/session 彩色内容已结构化；线性 plain writer 仅允许 plain/noninteractive/JSON 或 owned surface 生命周期之外使用，owned interactive 输出必须进入 Scene/presenter

规则：新组件禁止 `fmt.Print` 着色字符串；内容应进入 `Document`。在 owned interactive mode 中进一步要求 `Document/RenderEvent -> Scene -> presenter`，只有 plain/JSON mode 可由线性 backend 直接写 writer。

## 5. 控制序列安全

| 路径 | 处理 |
| --- | --- |
| 普通消息 | `SanitizeTerminalText` |
| 工具结果预览 | `cell.BuildPreview` / `ToolCell`（默认 strip all ESC；`AllowANSI` 仅 SGR） |
| Shell feedback | head/tail preview via `cell`；禁止 raw CSI |
| Diff 内容 | `diff.sanitizeContentSpans` → `ANSIToSpans` |
| 本地命令 ANSI | `render.ANSIToSpans`（只保留 SGR） |
| Timeline 字符串 | `cell.LegacyTimelineParser` 单一兼容入口 |
| 测试 | `tool_output_safety_test.go`、`cell/*_test.go`、`diff/*_test.go` |

## 6. Golden baseline（40/80/120 × color/no-color）

- Status line plain truncate：`TestGoldenStatusLineWidths`
- Render plain/ANSI：`ui/render/*_test.go`
- Phase 1+ styled golden 与 buffer snapshot 已落在 `ui/testdata/rendering/`，覆盖 Markdown、Diff、theme preview、typed status、session info 与 info table

## 7. Phase 0 完成标准核对

- [x] 全仓 Go 代码已移除 `fatih/color`；toolcall 原有全局颜色与 `color.NoColor` 兼容信号均已删除
- [x] formatter 分叉有 owner 与迁移标记（本文）
- [x] `FormatOutput` line number/indent 写入结果
- [x] `FormatJSON` 使用 `encoding/json`
- [x] `FormatMarkdown` 唯一入口到 formatter
- [x] tool/shell 控制序列安全测试
- [x] 40/80/120 宽度 baseline 测试
