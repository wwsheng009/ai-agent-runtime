# aicli UI/UX 渲染逻辑优化方案（参考 Codex TUI）

状态: **Phase 0-5 内容渲染核心实现完成并经审查收口；物理屏幕所有权与终端集成验收转入统一 TUI 架构计划**

更新时间: **2026-07-31**

关联文档：

- 实施审查与内容渲染剩余风险：[aicli-ui-rendering-implementation-review.md](./aicli-ui-rendering-implementation-review.md)；
- owned viewport 当前实现：[aicli-tui-p5-owned-viewport-design.md](./aicli-tui-p5-owned-viewport-design.md)；
- Scene、single screen owner、事务式 frame 与长期验收：[aicli-tui-unified-render-architecture-refactor-plan.md](./aicli-tui-unified-render-architecture-refactor-plan.md)。

本文中的 `done (core)` 表示内容解析、结构化 IR、主题、宽度与聚焦测试完成，不等于 raw/direct output 已全部消除，也不等于真实 PTY/ConPTY 和 fullscreen/scrollback 生命周期已经验收。`render.ApplyBlockSpacing` / `SpacingPolicy` 负责 Markdown/Document 或单个 semantic cell 内部的 block spacing；top-level transcript cells 之间的 gap 由统一架构文档的 `BoundaryPolicy` 管理。

实施进度:

- Phase 0: inventory、FormatOutput/JSON/Markdown 分叉修复、tool/shell 控制序列安全测试、40/80/120 baseline — **done**
- Phase 1: `ui/render` 模型/width/plain+ANSI backend/ANSI-to-span；`ui/style` palette/RequiredRoles（含 System）/ColorProfile/statusline/separator；message/status/separator/layout 已通过正式 Resolver/profile 编码；chat 与 fixed-bottom 状态统一传递 `StatusLineModel`，旧状态字符串 parser 已删除 — **done (core)**
- Phase 2: Goldmark AST markdown、Chroma token-to-span、宽度感知表格/records、assistant final/transcript 接入、`FormatAssistantRendered` 保留 SGR；block 间垂直节奏由 layout 阶段 `render.ApplyBlockSpacing`/`SpacingPolicy` 统一插入（同类 list/quote 保持紧凑，`markdown.Options.Spacing` 可退回稠密布局） — **done (core)**
- Phase 3: `ui/cell` typed timeline/tool + head/tail preview；runtime event bridge 直接传 `TimelineEvent`/`Document` 到 interaction，不再反向解析 planning/team/task/input/tool/progress/approval/LLM/compact/reasoning 标签；typed tool、info/session/table、input 与 shell feedback 均通过 ThemeContext 跟随 palette/profile；`ui/diff` unified/edited parser + 四档 color profile 渲染，窄终端 content row 保留 gutter/style 自动续行，背景可读取 syntax diff scopes；Diff 采用 64 KiB/2000 行独立 Chroma 预算和安全逐行 fallback；LegacyTimelineParser 只保留非 runtime supplement 与 edited-diff 兼容入口 — **done (core)**
- Phase 4: fullscreen_list hooks；`/theme` 全屏 live preview + cancel restore + confirm persist；syntax 轴与配置；富预览改为 `ThemePreviewDocument`，full-screen session 从既有 TerminalDriver 注入一次 ColorProfile，preformatted SGR 在截断后按真实色深重编码；自定义 YAML palette loader — **done (core)**
- Phase 5: `motion.Policy`；`render.FrameScheduler`+`BufferBackend`（≤30FPS）；`markdown.StreamCollector` holdback；stable prefix 直接进入 Goldmark/Chroma；active preview 使用 64 KiB/2000 行独立高亮预算并静默降级；`cell.ActiveCell` + `ui.ActiveStreamController`；Markdown document 按 source/width/syntax theme 缓存；progress/spinner 接入；chat interaction 跟踪 active viewport；theme 变更强制重绘 — **done (core)**
- Phase 5 polish: fixed-bottom 活动带（行数自适应终端高度：约 1/3 屏高，min 6 / max 14，并为输出与输入区保留 12 行；无 scrollback 污染）；`BufferBackend` 从 block tree 尾部提取 styled lines；surface 通过 TerminalDriver profile + Resolver/ANSI backend 保留 token RGB 并按色深降级，model-only status、plain active band 和 notice 同样复用该 ThemeContext；边界清理 CSI/OSC 并按 cell 截断；流式 mirror、finalize/cancel 清理；突发 delta 在 viewport 填满前不被 FPS 合并卡住，填满后恢复 30 FPS coalescing；tool running/progress → ActiveBand；无 surface 时稳定纯文本 live 增量写出；known timeline 行走 `TimelineEvent.Document`，unknown tag 仅保留字符串识别但输出同样构建 semantic spans；tool stage detail 预算 96 供 ActiveBand 进度 — **done**
- Verification polish: Markdown、Diff、theme preview（含 System role）、typed status、session info、info table 已建立 40/80/120 列 styled+plain buffer golden；新增 100 KiB Diff parse/render、单 Span 截断和 table 大单元格 benchmark — **done**
- Rendering closure: Theme 字符串兼容方法、Progress/Spinner、StatusBar、welcome、output/code/keyword adapter、commands picker/session/exec 输出、runtime timeline 和 Markdown 字符串适配器均已改为 `Document -> Resolver -> profile backend`；Status/color callback、Theme `*color.Color`、fixed-status parser、Markdown 私有旧 renderer、`color.NoColor` 全局兼容信号和 `fatih/color` 依赖均已删除；生产与测试只由 `ColorProfile` 决定颜色能力 — **done**

## 1. 文档定位

本文是 [aicli-ui-refactor-codex-inspired-plan.md](./aicli-ui-refactor-codex-inspired-plan.md) 的渲染专题方案。

已有计划主要解决终端输出所有权、fixed-bottom surface、composer、popup、输入事件和帧渲染演进；本文聚焦另一条相互独立但必须协同的主线：

- 颜色如何从业务语义映射到终端能力；
- 应用配色与代码语法主题如何解耦；
- Markdown、代码块、Diff、工具输出和状态栏如何统一渲染；
- 关键词、状态和消息类型如何从字符串猜测迁移到结构化事件；
- 如何在 TrueColor、ANSI-256、ANSI-16、无颜色、亮色和暗色终端中稳定降级；
- 如何通过结构化快照和终端矩阵避免 UI 回归。

本文不要求立即替换现有 `FixedBottomSurface`，也不建议第一步引入 Bubble Tea、tview 或全屏 alternate screen。推荐先建立独立于 ANSI 字符串的渲染中间层，再让 plain renderer 和 owned TUI presenter 分别消费该中间层。这里的“共同消费”只指复用 `Document -> Block -> Line -> Span` 内容模型，不表示二者可以在同一 owned interactive session 中同时写物理终端；物理 writer 所有权以统一 TUI 架构计划为准。

## 2. 执行结论

建议采用以下六层架构：

1. **业务事件层**：聊天、工具、审批、推理、状态等代码只产生带类型的数据，不决定颜色。
2. **内容解析层**：Markdown 使用正式解析器；代码使用词法高亮器；Diff 使用结构化行模型；外部 ANSI 输出使用白名单解析器。
3. **渲染模型层**：所有内容先生成 `Block -> Line -> Span`，样式保持为结构化字段，不提前拼入 ANSI。
4. **主题解析层**：语义配色、语法主题和终端亮暗是三条独立轴，最终由 `ThemeResolver` 合并。
5. **终端适配层**：统一处理颜色深度、默认前景/背景、Unicode 宽度、超链接、无颜色和 ASCII 降级。
6. **输出后端层**：Plain/JSON mode 使用独立线性 backend；owned interactive mode 由唯一 presenter 消费 buffer/frame 并写终端，不改上层内容组件。

关键设计判断：

- 不再让 `*color.Color` 充当主题模型。它只能是 ANSI backend 的临时兼容实现。
- 不再让 Markdown、工具调用和状态栏各自硬编码 cyan/green/red。
- 不把语法主题当成应用主题。`focus/contrast/mono` 负责 UI 语义，Chroma theme 负责代码 token。
- 不用颜色作为唯一信息。状态必须同时有文字、符号或位置语义。
- 不直接保留任意工具输出中的控制序列。只解析允许的 SGR 样式，拒绝 OSC、光标移动、清屏和终端模式切换。
- 不以一次性重写为目标。先加结构化模型和兼容 adapter，再逐类迁移。

## 3. 参考实现与适用边界

### 3.1 Codex TUI 中值得复用的设计

本方案参考以下 Codex 模块的设计原则，而不是移植 Rust 代码：

| Codex 模块 | 可借鉴能力 | aicli 对应方向 |
| --- | --- | --- |
| `codex-rs/tui/src/render/highlight.rs` | `syntect + two-face` 的语言识别、主题注册、scope 查询、高亮上限、自定义主题 | 使用 Go 生态的 Chroma v2，输出结构化 span |
| `codex-rs/tui/src/theme_picker.rs` | 搜索、实时预览、取消恢复、确认持久化、宽窄布局 | 扩展现有 `fullscreen_list.go` 和 bottom popup |
| `codex-rs/tui/src/terminal_palette.rs` | 颜色深度探测、OSC 10/11 默认颜色、RGB 到 ANSI-256 降级 | 扩展 `TerminalCapabilities` 和新增 `ColorProfile` |
| `codex-rs/tui/src/color.rs` | 亮暗判断、颜色混合、感知距离 | 主题选择、对比度修正和色彩量化 |
| `codex-rs/tui/src/markdown_render.rs` | `pulldown-cmark` 事件解析、宽度感知、代码块高亮、链接和表格布局 | Goldmark AST renderer + Chroma |
| `codex-rs/tui/src/markdown_render/table_key_value.rs` | 窄屏表格自动转纵向 key/value records | 为 40 到 80 列终端提供可读降级 |
| `codex-rs/tui/src/diff_render.rs` | 行号、正负标记、语法前景、整行背景、亮暗和色深降级 | 新增独立 Diff renderer |
| `codex-rs/tui/src/bottom_pane/status_line_style.rs` | 状态项映射 TextMate scope，从语法主题派生柔化色 | 可选的 theme-derived 状态栏配色 |
| `codex-rs/tui/src/exec_cell/render.rs` | 工具 ANSI 输出先解析为 span，再叠加 dim/布局 | 新增受限 ANSI-to-span 入口 |
| `codex-rs/tui/src/render/renderable.rs` | `desired_height(width)` 与 `render(area, buffer)` 分离 | 先定义可测量组件接口，后接 frame renderer |
| `codex-rs/tui/src/motion.rs` | 动画集中管理和 reduced-motion fallback | 统一 spinner/shimmer/ticker 策略 |

Codex 的固定语义色和语法主题不是同一套配置。其 `/theme` 主要选择语法高亮主题；正文、成功、失败、交互提示等仍遵循稳定语义。aicli 当前已经提供 `focus/classic/contrast/mono` 应用配色，推荐保留这一产品能力，同时增加独立的 `syntax` 轴，不把两者合并为一个含义模糊的 `theme.name`。

### 3.2 不直接照搬的部分

- Codex 基于 Ratatui buffer；aicli 仍处于 stdout、fixed-bottom 和未来 frame renderer 并存阶段。
- Codex 使用 `syntect/two-face` 和 `.tmTheme`；Go 中优先使用 Chroma v2，首期不自行实现 TextMate grammar。
- Codex 的 ANSI-16 路径会主动放弃部分 RGB 背景；aicli 应采用同样的保守原则，而不是强行量化所有背景。
- Codex 的大量 `HistoryCell` 已建立在统一状态模型上；aicli 需要先通过兼容 adapter 逐步消除字符串关键字判断。

## 4. 范围与非目标

### 4.1 本方案范围

- Chat transcript、assistant Markdown、用户消息、system/timeline、reasoning 和 tool cell；
- status line、popup、selector、配置 TUI 的共享样式规则；
- 代码块、行内代码、JSON、Diff、shell command 与 shell output；
- 主题选择、预览、持久化、亮暗适配、色深降级和无颜色模式；
- 宽度、换行、截断、Unicode、OSC 8 超链接和终端文本安全；
- 单元、golden、buffer snapshot、ANSI snapshot、PTY/ConPTY smoke test。

### 4.2 非目标

- 不在本方案中重写输入编辑器、paste burst 或 popup modal stack；
- 不强制切换到 alternate screen；
- 不让 JSON、pipe、CI 输出携带 UI ANSI；
- 不追求首期兼容 Codex 的全部 `.tmTheme` 文件；
- 不通过正则继续扩充一个“看起来像 Markdown”的半解析器；
- 不在业务事件中保存已经着色的字符串。

## 5. 当前实现审计

### 5.1 已有基础

当前实现并非从零开始，以下能力应保留并演进：

- `ui/theme.go` 已定义 user、assistant、tool、reasoning、approval、success、warning、error、muted 等语义角色。
- `ui/theme_presets.go` 已将明暗模式与 `classic/focus/contrast/mono` 配色分成两个配置轴，并提供预览与持久化。
- `/theme` 已支持 status、list、preview、set 和交互选择，相关逻辑有单元测试。
- `message.go` 已对普通消息做控制序列清理、双向文本隔离和多行 gutter 对齐。
- `fixed_bottom_surface.go` 已具备 status、prompt、popup、composer、viewport 和 owner stack 的实际能力，并有较多光标/布局测试。
- `fullscreen_list.go` 已具备搜索、选中项预览、取消和窄终端 fallback，可作为主题选择器基础。
- `formatter/markdown.go` 已处理基础标题、列表、引用、表格、代码围栏、行内样式和 OSC 8 链接。
- `chat_interaction.go` 已区分纯文本流和 Markdown 流，Markdown 最终会整体格式化，避免部分半结构内容直接落屏。
- `NO_COLOR`、非 TTY、legacy terminal 和 profile matrix 已存在基本降级路径。

### 5.2 核心问题

| 编号 | 当前问题 | 直接影响 | 主要位置 |
| --- | --- | --- | --- |
| R1 | 主题字段直接保存 `*color.Color`，组件调用 `Sprint` 后立即生成 ANSI | 无法在最终输出时统一降色、重排、测宽或切换 backend | `ui/theme.go`、`ui/theme_presets.go` |
| R2 | Markdown、tool call 等模块硬编码颜色，绕过主题 | `/theme` 只影响部分界面，同一语义在不同页面颜色不一致 | `formatter/markdown.go`、`ui/toolcall.go` |
| R3 | 自动明暗主要依赖 `COLORFGBG`，无法获知大量现代终端的真实背景 | 亮色 Windows Terminal、VS Code 等场景可能按暗色主题渲染 | `ui/theme.go:detectTerminalThemeType` |
| R4 | 能力模型只有 ANSI/scroll region 等布尔值，没有颜色深度和默认前景/背景 | RGB、ANSI-256、ANSI-16 无法可靠分级 | `ui/terminal_driver.go` |
| R5 | Markdown 是逐行正则和字符串扫描 | 嵌套列表、转义、复杂 emphasis、代码围栏、表格 pipe 和链接边界容易误判 | `formatter/markdown.go` |
| R6 | 代码块只使用统一亮白色，没有语言识别和 token 高亮 | 代码可扫描性弱，主题选择也无法预览真实效果 | `formatter/markdown.go:formatCodeBlock` |
| R7 | 表格按内容最大宽度排版，不接收目标 viewport 宽度 | 长路径、长中文、窄屏会溢出或形成不可读横向表格 | `formatter/markdown.go:formatTable` |
| R8 | 宽度、截断、换行逻辑分散并混用字节长度、rune 宽度和 ANSI 正则 | CJK、emoji、组合字符、OSC 8、彩色 span 容易错位 | `ui/output.go`、`ui/message.go`、`formatter/markdown.go`、`ui/inputbox_editor.go` |
| R9 | timeline/关键字通过 `[tool]`、`failed:`、`• Edited` 等字符串猜测类型 | 本地化或内容变化会造成误着色，新增状态需要继续堆分支 | `ui/theme_render.go` |
| R10 | tool/shell 输出没有统一的受限 ANSI 解析入口 | 原有颜色要么被全部清除，要么可能夹带 OSC、清屏和光标控制 | `ui/toolcall.go`、`ui/shell_feedback.go` |
| R11 | status line 通过字符串首段判断 Ready/Thinking/模型名 | 业务状态、显示文字和颜色绑定，难以配置/国际化 | `ui/fixed_bottom_surface.go:formatFixedStatusLine` |
| R12 | 存在重复或未完成 formatter | `ui.FormatMarkdown` 是 no-op；`FormatJSON` 非结构化解析；`FormatOutput` 的 `formattedLine` 未实际写入结果 | `ui/output.go` |
| R13 | 动画开关只局部存在于 terminal title，spinner 自行启动 ticker | reduced motion 不能统一执行，后台 ticker 可能多处存在 | `ui/progress.go`、`commands/chat_notification.go` |
| R14 | 视觉测试主要验证字符串和转义序列，缺少 style/buffer 快照 | 颜色层级、降色、背景、宽窄布局的回归难以发现 | `ui/*_test.go`、`formatter/markdown_test.go` |

### 5.3 当前颜色链路

当前路径大体是：

```text
业务状态/字符串
  -> 组件选择 fatih/color.Color
  -> Sprint/Sprintf 立刻插入 ANSI
  -> 字符串继续被换行/截断/拼接
  -> fmt.Print 或 FixedBottomSurface.WriteOutput
```

这个顺序是多数错位问题的根源。颜色代码一旦进入字符串，后续逻辑必须不断 strip ANSI 才能测宽，并且无法分辨原始内容中的终端控制序列与应用生成的安全 SGR。

目标顺序应改为：

```text
Typed UI Event
  -> Content Parser / Cell Renderer
  -> Document{Block, Line, Span, Style}
  -> Layout(width, height, policy)
  -> ThemeResolver + TerminalColorProfile
  -> ANSIBackend / BufferBackend / PlainBackend
  -> Surface
```

只有 backend 可以生成 ANSI。业务层、Markdown parser、Diff renderer 和组件层都不得返回“已经着色的字符串”。

## 6. 目标渲染模型

### 6.1 最小结构化类型

建议新增 `backend/cmd/aicli/ui/render` 子包，第一阶段只提供不依赖完整 TUI 框架的最小类型：

```go
type Document struct {
    Blocks []Block
}

type Block struct {
    Kind     BlockKind
    Lines    []Line
    KeepWithNext bool
}

type Line struct {
    Spans []Span
    Style Style
}

type Span struct {
    Text  string
    Style Style
    Link  string
}

type Style struct {
    Foreground Color
    Background Color
    Bold       bool
    Dim        bool
    Italic     bool
    Underline  bool
    Reverse    bool
}

type Color struct {
    Kind  ColorKind // Default, ANSI, Indexed, RGB
    Index uint8
    R, G, B uint8
}
```

约束：

- `Text` 不得包含 ESC、OSC、CSI 等控制序列；
- `Link` 是单独字段，只有支持并允许 OSC 8 时由 backend 编码；
- 换行属于 `Line` 边界，不放入 `Span.Text`；
- `Style` 合并遵循显式覆盖规则，例如 Diff 背景覆盖 line，语法 token 只提供前景；
- Plain backend 丢弃全部 style 和 link 控制序列，但保留可见文字；
- ANSI backend 根据 `ColorProfile` 最后一次性选择 RGB、Indexed、ANSI 或默认色。

### 6.2 Renderable 接口

在不引入完整 frame renderer 的前提下，先定义可测量组件：

```go
type Constraints struct {
    Width, Height int
    Compact       bool
}

type Renderable interface {
    Measure(Constraints) Size
    Render(Constraints) render.Document
}
```

`Measure` 和 `Render` 必须共享同一套宽度算法。现有 `FixedBottomSurface` 初期可把 `Document` 交给 ANSI backend 得到字符串，长期 frame renderer 可直接把 span 写入 cell buffer。

### 6.3 兼容层

为了控制改动范围，保留现有公开函数，但让它们变成 adapter：

```text
FormatAssistantMessage(string) string
  -> NewAssistantCell(source).Render(constraints)
  -> DefaultANSIBackend.Render(document)

FormatAssistantDocument(source, constraints) Document
  -> 新代码直接调用，不经历 ANSI 字符串
```

兼容函数需要标记迁移期用途，禁止新组件继续依赖。这样不会迫使 Phase 1 同时重写所有 `fmt.Print` 路径。

## 7. 主题与颜色架构

### 7.1 三个独立轴

必须明确区分以下概念：

| 轴 | 回答的问题 | 示例 |
| --- | --- | --- |
| Semantic palette | 工具、成功、失败、审批、正文等 UI 语义应如何区分 | `focus`、`classic`、`contrast`、`mono` |
| Syntax theme | 代码 token 和可选的状态栏 scope 使用哪些颜色 | `dracula`、`github`、`monokai`、自定义 Chroma style |
| Terminal profile | 当前设备能否可靠显示这些颜色和控制能力 | TrueColor dark、ANSI-256 light、ANSI-16、NoColor |

`mode=auto|dark|light` 是 palette 和 syntax theme 的选择输入，不等于某个具体主题。用户显式选择 `light/dark` 时优先；`auto` 才使用终端背景探测。

### 7.2 语义角色

建议用稳定角色替换散落字段和硬编码颜色：

| 角色 | 用途 | 默认表现原则 |
| --- | --- | --- |
| `TextPrimary` | assistant 正文、普通内容 | 使用终端默认前景，不给大段正文染色 |
| `TextSecondary` | 工具摘要、补充说明 | 比正文弱一级，但仍可读 |
| `TextMuted` | 时间、分隔符、省略提示 | dim 或柔化前景，不用于关键错误 |
| `Accent` | 当前选择、prompt、交互焦点 | cyan/blue 类高辨识色 |
| `User` | 用户 gutter 或标签 | 只强调前缀和必要标题，避免整段高饱和 |
| `Tool` | 工具名称、命令 | magenta/cyan 类，与状态色分离 |
| `Reasoning` | reasoning/planning 标签 | yellow 类，但正文仍使用 secondary/muted |
| `Approval` | 需要用户行动 | 强调色加粗，同时显示明确文字 |
| `Info` | 一般提示 | 非阻断色 |
| `Success` | 已完成、exit 0、add count | green，同时有 `✓` 或状态文本 |
| `Warning` | 重试、等待、接近限制 | yellow，同时有文字 |
| `Error` | 失败、拒绝、delete count | red，同时有 `✗` 或错误文本 |
| `Link` | 可点击链接/路径 | underline；颜色可由主题或默认前景派生 |
| `Border` | 表格、popup、分隔线 | muted，不抢正文层级 |
| `Selection` | selector 当前行 | reverse 或前景+字重，不能只靠背景色 |
| `CodeInline` | 行内代码 | 轻微背景仅在高色深下启用；ANSI-16 使用反显/加粗 |

建议移除“assistant 整段固定绿色”的默认观念。AI 回复通常是页面最大文本区域，应优先使用终端默认前景；颜色用于建立层级，而不是覆盖所有内容。

### 7.3 Palette 数据结构

```go
type Role string

type Palette struct {
    Name    string
    Variant Variant // Light or Dark
    Styles  map[Role]render.Style
}

type ThemeSelection struct {
    PaletteName string
    SyntaxName  string
    Mode        ThemeMode
}
```

`Palette` 必须是纯数据，不能包含 `Sprint`、writer 或环境探测。主题解析完成后应视为不可变对象，通过 session/context 注入组件，避免组件随时读取全局 `currentTheme`。

迁移期间可以保留全局默认主题，但新 renderer 接口必须显式接收 `ThemeContext`：

```go
type ThemeContext struct {
    Palette      Palette
    Syntax       SyntaxTheme
    Terminal     ColorProfile
    UseHyperlink bool
    Unicode      UnicodeMode
}
```

### 7.4 终端颜色能力

扩展 `TerminalCapabilities`：

```go
type ColorDepth int

const (
    ColorNone ColorDepth = iota
    ColorANSI16
    ColorANSI256
    ColorTrueColor
)

type ColorProfile struct {
    Enabled       bool
    Depth         ColorDepth
    DefaultFG     *RGB
    DefaultBG     *RGB
    Background    BackgroundKind // Light, Dark, Unknown
    Hyperlinks    bool
    Forced        bool
}
```

探测优先级：

1. 非 TTY、`TERM=dumb`、JSON/pipe、`NO_COLOR`：`ColorNone`。
2. 用户显式 `color=never|always` 和 `color_depth`：作为可诊断的覆盖值。
3. `COLORTERM=truecolor|24bit`、`TERM=*-256color`、`WT_SESSION`、已知 terminal profile：推断色深。
4. 有界 OSC 10/11 启动探测：读取默认前景/背景 RGB，设置严格超时并缓存。
5. `COLORFGBG`：只作为背景亮暗 fallback。
6. 无法判断时选择暗色 variant，但不得假装已探测到真实背景。

Windows 注意事项：

- 必须先成功启用 VT processing，再允许 ANSI；
- Windows Terminal 在部分探测库中可能只报告 ANSI-16，可结合 `WT_SESSION` 修正为 TrueColor，但保留显式覆盖开关；
- OSC 查询必须通过统一 input decoder 协调，不能与 raw editor 同时抢读 stdin；
- 首期无法安全协调响应时，可以只实现环境推断，把 OSC 探测放到与输入事件循环合并的阶段。

### 7.5 亮暗判断与对比度

建议使用感知亮度而不是 ANSI index 猜测：

```text
relative luminance = 0.2126R + 0.7152G + 0.0722B
```

实现时应先将 sRGB 转为线性空间，再计算 WCAG contrast ratio。建议阈值：

- 普通正文与背景至少 `4.5:1`；
- 大号/加粗文本至少 `3:1`，但 TUI 大多仍按 `4.5:1` 检查；
- muted 文本允许更低饱和度，但不应低于 `3:1`；
- Diff、selection 和 inline code 的前景必须分别与其背景检查，不只与终端默认背景检查。

当主题颜色不达标时，resolver 按顺序处理：去掉背景、回退默认前景、降低饱和度或改用标准 ANSI 色。不要静默生成肉眼不可辨的前景/背景组合。

### 7.6 色深降级

| 能力 | 前景 | 背景 | Diff | Inline code |
| --- | --- | --- | --- | --- |
| TrueColor | RGB | 可用柔和 RGB | 主题 scope 或亮暗专用 tint | 轻背景 + token 前景 |
| ANSI-256 | 感知距离量化到 xterm palette | 仅使用验证过的 index | 量化 tint | indexed 背景或反显 |
| ANSI-16 | 标准语义前景 | 默认不使用彩色背景 | green/red 前景 + `+/-` | bold/reverse，无 RGB |
| NoColor | 无颜色 | 无背景 | `+/-`、行号、文字 | 反引号或纯文本边界 |

ANSI-16 不建议把任意 RGB 背景强制映射到高饱和 16 色背景。Codex 的 Diff renderer 在低色深下主动使用前景色而不画背景，aicli 应沿用这一保守策略。

## 8. 代码语法主题

### 8.1 依赖选择

推荐引入 `github.com/alecthomas/chroma/v2`：

- 语言覆盖和 lexer registry 成熟；
- 支持按文件名、MIME、显式语言选择 lexer；
- 内置多套 style；
- token 类型和 style 可以转换为本项目 `render.Span`，不必直接生成 ANSI/HTML；
- 可以为 Diff、Markdown code block、shell command、JSON 和 theme preview 复用同一高亮入口。

不推荐把 Glamour 或 Lip Gloss 作为核心渲染模型：它们适合快速生成 ANSI 字符串，但会再次把样式和内容提前揉成字符串。可以在原型阶段对比效果，正式链路仍应输出内部 span。

### 8.2 SyntaxHighlighter 接口

```go
type HighlightRequest struct {
    Code     string
    Language string
    Filename string
    Theme    string
}

type SyntaxHighlighter interface {
    Highlight(HighlightRequest) ([]render.Line, HighlightMeta)
}
```

语言解析顺序：显式 fence info -> filename extension -> lexer analyse -> plain text。别名应规范化，例如 `js/javascript`、`ts/typescript`、`sh/bash/shell`、`py/python`、`yml/yaml`。

### 8.3 资源边界

参考 Codex 的防护，建议默认限制：

- 单个高亮块最大 `512 KiB`；
- 单个高亮块最大 `10,000` 行；
- 超限时降级为 plain code block，并显示 muted 提示；
- theme 和 lexer registry 全局只初始化一次；
- 同一 active cell 可按 `(theme, language, content hash)` 缓存结果；
- 流式代码围栏未闭合时，不对每个 token 重新高亮整个块，按节流周期或稳定行增量处理。

### 8.4 自定义主题

首期支持 Chroma 内置 style 并在启动时校验配置。后续可支持 `$AICLI_HOME/themes/*.yaml`，内容映射 Chroma token type 到前景、背景和 modifier。加载要求：

- 结构化 YAML 解析，不手工扫描文本；
- 非法颜色、未知 token、重复主题返回诊断并回退默认；
- 自定义主题名与内置名大小写不敏感去重；
- 目录扫描稳定排序；
- 单文件大小设置上限；
- `.tmTheme` 兼容放到独立 adapter，只有选定成熟解析库后再承诺，不在核心 renderer 中自行解析 plist/XML。

## 9. Markdown 渲染

### 9.1 正式解析器

推荐使用 `github.com/yuin/goldmark`，启用与 GFM 对齐的 table、strikethrough、task list 和链接扩展。选择 Goldmark 的原因是它提供稳定 AST，可由本项目控制终端布局；不建议直接使用一个只输出 ANSI 字符串的 Markdown renderer 作为核心。

新的 `MarkdownRenderer` 输入必须包含 viewport 宽度、主题和工作目录：

```go
type MarkdownOptions struct {
    Width          int
    CWD            string
    Theme          ThemeContext
    TableMode      TableMode
    Hyperlinks     bool
    PreserveMarkup bool // Plain/no-color transcript policy
}
```

AST 到 render model 的映射：

| Markdown 节点 | 终端表现 |
| --- | --- |
| Paragraph | 默认正文，按 grapheme/word wrap |
| Heading | 加粗 + 稳定前缀，不使用超大视觉层级 |
| Emphasis/Strong | italic/dim 或 bold；无能力时保留文本而非标记 |
| Inline code | `CodeInline` role；低色深用反显或加粗 |
| Fenced code | 语言规范化后调用 `SyntaxHighlighter` |
| Blockquote | muted gutter `│`，续行对齐 |
| List | 保留嵌套层级和 continuation indent |
| Link | 可见 label + 独立 Link 字段；策略决定是否追加 URL |
| Table | 宽度分配；过窄时切换 records 模式 |
| Rule | muted 分隔线，宽度受 viewport 限制 |
| HTML | 默认转义或纯文本，不执行终端控制内容 |

### 9.2 表格布局

表格不能只按最长 cell 计算宽度。建议流程：

1. 统计每列的最小 token 宽度、理想宽度、内容类型和可换行性。
2. 扣除列分隔符后分配 viewport 宽度。
3. 优先保证数字、状态、短枚举等 compact 列完整。
4. 路径列允许在 `/`、`\\`、`.`、`-`、`_` 等边界换行。
5. 叙述列按单词和 CJK grapheme 换行。
6. 如果多列持续碎裂，切换纵向 records：每条数据按 `Header: Value` 渲染。
7. header-only 或异常 AST 使用 pipe fallback，不能丢数据。

推荐行为：

- `>= 100` 列：优先 grid；
- `60..99` 列：按列指标动态选择；
- `< 60` 列：多列叙述表格优先 records；
- 任何宽度下都必须保证单行可见宽度不超过 constraints，除非用户显式关闭 wrapping。

### 9.3 流式 Markdown

当前 `chat_interaction.go` 已有 Markdown buffer 和最终整体格式化，这是正确基础，但依赖 `IsMarkdown` 启发式且容易发生“前缀已纯文本输出，后半段才发现是 Markdown”的双路径问题。

建议引入 `MarkdownStreamCollector`：

- 保存 source buffer，而不是保存 ANSI 输出；
- 识别稳定完成的 block 和尚未完成的 tail；
- 未闭合代码围栏、表格 header、未闭合链接和列表 continuation 留在 holdback；
- 已稳定的 paragraph 可提交为 transcript block；
- active tail 在 fixed-bottom/frame 区域重绘，不写入历史；
- final event 到达后，对完整 source 做一次权威解析并核对已提交 block；
- 任何时候不得通过“清屏再重打一遍”修正 scrollback。

首期若 active-cell 重绘尚未可用，采用更保守策略：所有 Markdown 流完整缓冲，纯文本流才逐 delta 输出。正确性优先于逐 token 动画。

## 10. Diff 渲染

### 10.1 数据模型

新增独立 `ui/diff` renderer，不再通过 `theme_render.go` 识别形如 `12 +...` 的字符串：

```go
type FileDiff struct {
    OldPath, NewPath string
    Hunks            []Hunk
    Language         string
}

type DiffLine struct {
    Kind       DiffLineKind // Context, Add, Delete, Header
    OldLineNo  int
    NewLineNo  int
    Text       string
}
```

输入来源应优先是 apply patch/tool event 的结构化结果；只有 legacy 文本路径才通过统一 diff parser 转换。

### 10.2 样式合成

每行按以下顺序合成：

```text
line number style
  + add/delete sign style
  + syntax token foreground/modifier
  + diff line background overlay
  + selection/focus overlay（如存在）
```

规则：

- Add/Delete 永远同时显示 `+/-`，颜色不是唯一信号；
- 行号 gutter 固定宽度，wrap 后的 continuation 与代码列对齐；
- 高亮整段 hunk，而不是逐行重新初始化 lexer，保证多行注释/字符串状态正确；
- 主题定义了 add/delete scope 背景时优先使用，否则使用亮暗专用 fallback；
- ANSI-16 和 NoColor 不使用彩色整行背景；
- Delete 行可以对 token 前景叠加 dim，但不能降低到不可读；
- 大 Diff 超过高亮上限时保留行号和 diff 语义，关闭语法高亮。

### 10.3 JSON 与结构化数据

`ui.FormatJSON` 应被正式 JSON renderer 替换：

- 使用 `encoding/json` 解析和缩进；
- 解析失败时原样作为安全纯文本，不做基于括号的字符串重排；
- JSON token 可通过 Chroma `json` lexer 高亮；
- 超大对象使用统一截断策略，显示省略字节/行数；
- 永远不截断到 UTF-8 字节中间。

## 11. 工具输出与 ANSI 安全

### 11.1 两类输入必须分开

| 输入 | 处理方式 |
| --- | --- |
| 模型、MCP、工具返回的普通文本 | `SanitizeTerminalText` 后作为无样式 span |
| 明确声明为 terminal ANSI 的本地命令输出 | 使用受限 parser 转换 SGR 到 span |

不得通过“看到 ESC 就原样放行”判断 ANSI 输出。默认应是普通文本路径。

### 11.2 受限 ANSI parser

优先评估 `github.com/charmbracelet/x/ansi` 的 parser/decoder 能力，并在其上构建 `ANSIToSpans`。允许：

- SGR reset；
- bold、dim、italic、underline、reverse；
- ANSI 16/256/RGB 前景和背景。

拒绝并移除：

- OSC 0/2 title、OSC 8 任意链接、OSC 52 clipboard；
- CSI 光标移动、清屏、清行、scroll region、insert/delete；
- bracketed paste、mouse、focus、alternate screen 模式切换；
- BEL、DCS、APC、PM 和未知控制序列。

外部 ANSI 的 style 解析完成后，才能叠加 tool output 的 muted 策略。叠加 dim 不应破坏原有错误红色或背景；需要明确 style merge 规则。

### 11.3 工具 cell UX

将 `ToolCallDisplay` 扩展为数据模型而不是最终字符串：

- 第一行：状态标记、工具名、主要参数摘要；
- 运行中：稳定 activity marker，不让 spinner 改变行宽；
- 成功：`✓`、duration、exit code/结果摘要；
- 失败：`✗`、exit code、首个可行动错误；
- 输出预览：默认 head/tail，而不是只取前 N 行；
- 大输出：显示 `… N lines omitted` 和 transcript/pager 提示；
- path、command、URL 使用独立 span，允许主题和链接策略处理；
- 参数 map 必须稳定排序，避免每次渲染顺序变化。

## 12. 关键词、状态与页面层级

### 12.1 用类型替代字符串关键词

当前 `[tool]`、`[reasoning]`、`failed:` 等前缀可作为 legacy 输入，但不应继续作为渲染 API。建议定义：

```go
type TimelineEvent struct {
    Kind    TimelineKind
    Status  EventStatus
    Title   string
    Detail  string
    Source  string
    Time    time.Time
}
```

`TimelineKind` 至少包含 `Tool`、`Reasoning`、`Planning`、`Approval`、`Question`、`Team`、`Task`、`Progress`、`Notice`。renderer 根据 enum 选择 role、prefix、折叠策略和详情层级。

迁移方式：

1. 新业务事件直接构造 typed event。
2. 现有字符串进入单一 `LegacyTimelineParser`。
3. `StyleAssistantSupplementLine` 仅作为兼容包装。
4. 完成调用点迁移后删除散落的 prefix/suffix 判断。

### 12.2 视觉层级规则

- 一行最多一个主强调色，其他信息使用 primary/secondary/muted。
- 运行状态和错误优先于 model、token、cwd 等环境信息。
- separator 永远 muted，不能与状态值同色。
- assistant 正文使用默认前景；代码、链接、状态、工具名才使用强调色。
- 大段 reasoning 默认 secondary/muted，标题可使用 Reasoning role。
- popup 当前选择使用 selection style，未选项保持主文本，不把整屏染成同一色。
- 成功/失败必须有符号或文字；mono/NoColor 模式下信息仍完整。
- Emoji 图标必须有 ASCII/Unicode-width-stable fallback，不能假定所有终端等宽显示 emoji。

## 13. Status line 与主题 scope

### 13.1 结构化 segment

`formatFixedStatusLine` 不应再拆字符串首段。建议 surface 接收：

```go
type StatusSegment struct {
    Kind     StatusSegmentKind
    Text     string
    Priority int
    MinWidth int
    Link     string
}

type StatusLineModel struct {
    State    RunState
    Segments []StatusSegment
}
```

布局步骤：

1. `RunState` 始终保留，除非状态为 Ready 且宽度极窄；
2. 按 priority 从低到高删除可选 segment；
3. 对 path、session id 和 model 使用中间截断；
4. separator 使用独立 muted span；
5. 只在仍无法适配时对最后一个 segment 截断；
6. 所有截断均使用统一可见宽度算法。

### 13.2 从语法主题派生可选色

参考 Codex，可为 status item 定义 Chroma token 映射：

| Status item | Chroma token 候选 | fallback semantic role |
| --- | --- | --- |
| model/reasoning | `NameClass`、`NameVariable` | `Accent` |
| path | `LiteralString` | `Success`/`TextSecondary` |
| branch | `NameFunction`、`NameTag` | `Tool` |
| state | `Keyword` | `Accent` |
| usage/tokens | `LiteralNumber` | `Success` |
| limits | `KeywordType` | `Warning` |
| metadata | `Comment` | `TextMuted` |
| mode | `Operator` | `Accent` |

从语法主题取到 RGB 后应做柔化和对比度检查，避免 footer 比正文更抢眼。配置 `status_line_colors=semantic|syntax|mono` 控制策略；默认建议 `semantic`，在 syntax theme 能稳定后再评估改为 `syntax`。

## 14. `/theme` 交互方案

### 14.1 目标行为

参考 Codex theme picker，主题选择必须具备：

- 输入即搜索 palette/syntax theme；
- 上下移动时实时更新预览；
- Esc/Ctrl+C 恢复打开前主题；
- Enter 确认后才写配置；
- 列表项显示 light/dark 兼容信息和 custom/builtin 来源；
- 宽屏显示列表 + 预览，窄屏显示列表下方紧凑预览；
- popup/alternate screen 退出后不把所有候选项写进 transcript；
- 非交互和 legacy terminal 保留 `/theme status|list|set` 文本路径。

### 14.2 复用现有能力

`fullscreen_list.go` 已有 `Preview`、`SearchText`、取消和 fallback。建议增加以下可选 hook：

```go
type FullScreenListOptions struct {
    // existing fields...
    OnSelectionChanged func(index int)
    OnCancel           func()
    OnConfirm          func(index int) error
    PreviewRenderer    Renderable
}
```

主题 picker 打开时保存 `ThemeSelection` 快照；selection hook 只更新 session 内存主题并 request redraw；cancel 恢复快照；confirm 调用现有 theme persistence。hook 不应直接 `fmt.Println`。

### 14.3 预览内容

单纯显示 `user assistant tool reason err ok` 不足以评估代码主题。宽屏预览应包括：

- 2 到 4 行具有 string、number、keyword、function、comment 的代码；
- 一行 add、一行 delete、一行 context 的 Diff；
- success/warning/error/tool/reasoning 语义标签；
- primary、secondary、muted 正文；
- selection 和 inline code 样式。

窄屏保留 4 到 6 行，优先代码 + add/delete + semantic status。预览必须使用与真实 transcript 相同的 renderer，不能维护第二套演示专用颜色逻辑。

## 15. 宽度、换行和 Unicode

### 15.1 单一宽度服务

当前至少存在 formatter、message、input editor 等多套宽度实现。建议新建 `render.WidthService`，统一提供：

```go
Width(text string) int
SpanWidth(span Span) int
Truncate(line Line, width int, marker string) Line
Wrap(line Line, width int, opts WrapOptions) []Line
Pad(line Line, width int, align Align) Line
```

要求：

- 按 grapheme cluster 处理组合字符和 emoji sequence；
- CJK、全角、variation selector、ZWJ、directional isolate 正确计宽；
- style 和 link 不占宽度；
- tab 在进入 render model 前按明确 tab stop 展开；
- 不能把 UTF-8、grapheme、ANSI sequence 或 OSC 8 link 截断一半；
- Windows/Unix 使用同一算法，不依赖终端返回的光标位置反推宽度。

依赖建议先对 `github.com/charmbracelet/x/ansi` 和 `github.com/rivo/uniseg` 做小型兼容性验证，然后锁定一个统一实现。验收样本必须包含中文、阿拉伯文隔离、组合音标、国旗、肤色 emoji、ZWJ 家庭 emoji 和 Powerline 字符。

### 15.2 换行策略

- prose：英文按 word，CJK 按 grapheme；
- code：默认不折单词语义，但必须在 viewport 内 wrap 或按配置 horizontal clip；
- path：优先在分隔符边界断开；
- URL：保留完整 hyperlink target，可见 label 可换行；
- status：不换行，按优先级折叠和截断；
- popup item：标题可截断，detail 在下一行或隐藏；
- table：cell 内换行，整行高度取各 cell 最大高度；
- tool output：保留前导空格，禁止 `strings.TrimSpace` 破坏日志/代码缩进。

## 16. 动画与可访问性

### 16.1 集中 MotionPolicy

参考 Codex `motion.rs`，所有 spinner、shimmer、闪烁提示和 title animation 都必须通过一个策略对象：

```go
type MotionMode int // Full, Reduced, Off

type MotionPolicy interface {
    ActivityFrame(now time.Time) string
    NeedsNextFrame() bool
    Interval() time.Duration
}
```

- `Full`：允许稳定宽度的 spinner；
- `Reduced`：使用静态 bullet 或阶段文字，不做 shimmer；
- `Off`：不创建 ticker；
- 非 TTY、CI、NoInteractive 默认 Off；
- 配置显式值优先于自动判断；
- 组件不得自行 `time.NewTicker`。

### 16.2 无颜色和高对比

- `NO_COLOR` 的存在即禁用颜色，除非用户明确的命令行 `--color=always` 覆盖；
- `mono` palette 是用户审美选择，`ColorNone` 是终端输出能力，两者不能混为一谈；
- high-contrast palette 需要基于真实背景或保守 ANSI 色验证；
- selection、focus、error、success 不得只通过色相区分；
- emoji/icon 宽度不稳定时使用 `>`, `*`, `!`, `x`, `+`, `-` fallback；
- 终端不支持 italic/dim 时，文字层级仍应由前缀和布局表达；
- screen reader/复制场景优先保证纯文本 transcript 可理解。

## 17. 输出模式策略

| 模式 | 样式 | Markdown | 控制序列 | 目标 |
| --- | --- | --- | --- | --- |
| Interactive inline/frame | 完整能力降级 | 结构化终端渲染 | 仅 backend 生成 | 最佳 UX |
| Legacy TTY | ANSI 或 NoColor | 稳定线性布局 | 不使用 cursor UI | 最大兼容 |
| Pipe/plain | 无颜色 | 可选保留可读 Markdown 标记 | 禁止 | 人类可读 |
| JSON/JSONL | 无 UI 渲染 | 原始结构字段 | 禁止 | 机器可读 |
| Transcript/export | 样式元数据可选 | 原始 source + plain projection | 禁止 | 可追溯/可重放 |

同一业务事件可以有多个 projection，但 source of truth 必须是 typed model 和原始内容，不能从彩色终端字符串反向还原。

## 18. 推荐包结构与依赖方向

建议逐步形成以下目录，不要求一次全部创建：

```text
backend/cmd/aicli/ui/
├── render/
│   ├── model.go              # Document/Block/Line/Span/Style/Color
│   ├── width.go              # 唯一可见宽度、wrap、truncate
│   ├── backend_ansi.go       # 结构化样式到安全 ANSI
│   ├── backend_plain.go      # 纯文本 projection
│   ├── backend_buffer.go     # 后续 frame renderer 使用
│   └── ansi_input.go         # 受限 ANSI-to-span
├── style/
│   ├── roles.go              # semantic role
│   ├── palette.go            # focus/classic/contrast/mono
│   ├── resolver.go           # role/syntax/profile -> resolved style
│   ├── terminal_color.go     # color depth、背景、量化、对比度
│   └── registry.go           # palette/syntax/custom registry
├── syntax/
│   ├── chroma.go
│   ├── language.go
│   └── limits.go
├── markdown/
│   ├── renderer.go
│   ├── table.go
│   └── stream.go
├── diff/
│   ├── model.go
│   ├── parser.go
│   └── renderer.go
├── cell/
│   ├── message.go
│   ├── timeline.go
│   ├── tool.go
│   └── statusline.go
└── compatibility_*.go       # 旧 API adapter，迁移完成后删除
```

依赖只能单向：

```text
render <- style <- syntax
render/style/syntax <- markdown/diff/cell
render backend <- surface
commands -> typed event/cell -> surface
```

禁止 `render` 反向依赖业务 `commands`，禁止 `markdown` 调用 `fmt.Print`，禁止 `style` 读取 session 或写配置。

## 19. 配置设计

### 19.1 兼容原则

现有配置：

```yaml
aicli:
  theme:
    name: focus
    mode: auto
```

必须继续有效。`name` 解释为 semantic palette，`mode` 保持明暗偏好。新增项全部可选，未配置时不改变现有命令行为。

### 19.2 建议配置

```yaml
aicli:
  theme:
    name: focus                    # semantic palette
    mode: auto                     # auto | dark | light
    syntax: auto                   # auto 或 Chroma style 名称
    custom_dir: ""                # 默认 $AICLI_HOME/themes
    status_line_colors: semantic   # semantic | syntax | mono

  ui:
    color: auto                    # auto | always | never
    color_depth: auto              # auto | truecolor | ansi256 | ansi16
    hyperlinks: auto               # auto | always | never
    unicode: auto                  # auto | unicode | ascii
    motion: auto                   # auto | full | reduced | off

    markdown:
      syntax_highlight: true
      tables: auto                 # auto | grid | records | plain
      wrap: true

    diff:
      syntax_highlight: true
      background: auto             # auto | always | never
      line_numbers: true
```

配置优先级：CLI flag -> environment override -> config file -> terminal auto detection -> built-in default。

建议环境变量：

- `NO_COLOR`：行业通用禁色；
- `FORCE_COLOR`：显式启色和测试用途；
- `AICLI_COLOR_DEPTH`：诊断/兼容逃生开关；
- `AICLI_THEME`、`AICLI_THEME_MODE`：保留；
- `AICLI_THEME_SYNTAX`：新增（兼容读取旧文档别名 `AICLI_SYNTAX_THEME`）；
- `AICLI_MOTION`：新增；
- `AICLI_DISABLE_OSC_PROBE=1`：禁用 OSC 10/11 探测。

配置解析必须收敛到一个 `ResolveUIConfig`，不能由 theme、terminal、formatter 各自读取环境变量。

## 20. 依赖决策

| 能力 | 推荐 | 理由 | 约束 |
| --- | --- | --- | --- |
| Markdown AST | `github.com/yuin/goldmark` | 成熟、可控 AST、GFM 扩展 | 自己实现终端 renderer，不直接输出 HTML |
| 代码高亮 | `github.com/alecthomas/chroma/v2` | lexer/style 丰富、可转 token | 不使用 ANSI formatter 作为核心接口 |
| grapheme | `github.com/rivo/uniseg` | Unicode grapheme 和 width 边界成熟 | 封装在 `WidthService`，业务不得直接调用 |
| ANSI decode/strip | 优先验证 `github.com/charmbracelet/x/ansi` | 维护活跃，覆盖现代终端序列 | 只允许 SGR；未验证前不承诺具体 parser API |
| 颜色输出 | 内置 `ui/render` + `ui/style` | 已实施并作为唯一 owner | `ColorProfile` 统一输出 TrueColor/ANSI-256/ANSI-16/Plain，不保留字符串着色依赖 |

引入依赖前必须完成最小 spike，验证 Windows 构建、许可证、二进制体积、初始化耗时和所需 API。不要同时引入 Glamour、Lip Gloss、Bubble Tea、tcell 等多套重叠栈。

## 21. 分阶段实施计划

### Phase 0：基线与缺陷收敛（P0，1 个短迭代）

目标：在不改变视觉设计的情况下，清除会阻碍新架构的明显分叉。

任务：

- 建立所有颜色硬编码、`Sprint/Sprintf`、宽度 helper、Markdown/JSON formatter、直接 `fmt.Print` 的 inventory；
- 修复 `ui/output.go` 中 line number/indent 未写入结果的问题；
- `FormatJSON` 改用 `encoding/json`，或标记 deprecated 并切到唯一实现；
- 明确 `ui.FormatMarkdown` 和 `formatter.MarkdownFormatter` 的唯一入口，删除 no-op 调用可能性；
- 为 tool/shell output 增加控制序列安全测试；
- 固化当前 40/80/120 列、color/no-color 的 golden baseline。

完成标准：没有新增硬编码颜色或字符串着色全局变量；已知 formatter 分叉都有 owner 和迁移标记。

### Phase 1：结构化 render core 与终端色彩 profile（P0，1 到 2 个迭代）

任务：

- 实现 `render` 基础模型、plain backend、ANSI backend；
- 实现统一 width/wrap/truncate；
- 新增 `ColorProfile`、色深推断、RGB 到 ANSI-256 量化和 ANSI-16 conservative fallback；
- 把现有 semantic palette 转为纯数据，并让兼容字符串 API 也通过统一 backend 编码；
- 先迁移 status line、separator、popup item 等短内容验证接口。

完成标准：同一 `Document` 可稳定输出 styled ANSI 和 plain text；所有 snapshot 的可见宽度一致。

### Phase 2：Markdown 与代码高亮（P0/P1，2 个迭代）

任务：

- 引入 Goldmark AST renderer；
- 引入 Chroma token-to-span；
- 实现 code fence、inline code、heading、quote、list、link；
- 实现宽度感知表格和 records fallback；
- 用新 renderer 替换 assistant final response 与 transcript replay；
- 加入高亮资源上限和 plain fallback。

完成标准：复杂 Markdown、中文、链接、嵌套列表、长路径表格在 40/80/120 列无溢出，代码块存在真实 token 颜色。

### Phase 3：Typed cell、工具 ANSI 与 Diff（P1，2 个迭代）

任务：

- 引入 typed timeline/tool/status model；
- 新业务路径停止构造 `[tool]` 等渲染字符串；
- 实现受限 ANSI-to-span 和工具 head/tail preview；
- 实现结构化 Diff、hunk 级语法高亮、行号和色深降级；
- 保留 legacy parser 作为单一兼容入口。

完成标准：任意 tool output 不能移动光标、改标题或写 clipboard；Diff 在四种 color profile 下都可读。

### Phase 4：主题选择器与配置（P1，1 个迭代）

任务：

- 扩展 `fullscreen_list` selection hooks；
- `/theme` 支持 palette 与 syntax 两级搜索/预览；
- 实现 live preview、cancel restore、confirm persist；
- 加载自定义 YAML style；
- 为 theme-derived status line 提供 opt-in。

完成标准：取消不修改运行态和配置；确认后新消息、status、Markdown code、Diff 使用一致主题。

### Phase 5：流式 active cell 与 frame backend（P2，与整体 TUI 计划协同）

任务：

- 实现 Markdown stable block/holdback collector；
- 将 active assistant/tool/status 渲染到 buffer backend；
- 合并 redraw request，默认上限 30 FPS；
- 主题 live preview 和 resize 通过状态重绘，而不是清屏打印；
- 移除大部分 string compatibility adapter。

完成标准：流式 Markdown 不重复、不闪烁、不污染 scrollback；resize/theme change 可重绘活动 viewport。

## 22. 测试策略

### 22.1 纯逻辑单元测试

`render`：

- style merge 的前景、背景和 modifier 覆盖顺序；
- plain backend 不包含 ESC/BEL/C0/C1；
- ANSI backend 在 span 边界正确 reset，不产生 style 泄漏；
- RGB -> ANSI-256 的稳定量化；
- ANSI-16 不生成 `38;2`、`48;2`、`38;5`、`48;5`；
- NoColor 不生成任何 ANSI/OSC。

`width`：

- CJK、ASCII、combining mark、emoji modifier、ZWJ、directional isolate；
- OSC 8 label、styled span、空 span；
- truncate marker 也计入宽度；
- middle truncation 不破坏 path 和 UTF-8；
- wrap 后每行宽度不超过目标值。

`theme`：

- 每个 palette/mode 的必需 role 完整；
- mono 不泄漏 chromatic style；
- light/dark auto fallback 稳定；
- contrast ratio 不低于约定阈值；
- invalid custom theme 诊断且回退；
- theme registry 排序和去重稳定。

`markdown`：

- 嵌套有序/无序列表；
- emphasis/strong/code 的嵌套和转义；
- fenced code 的 language alias；
- table 中 escaped pipe、链接、中文和长路径；
- 40 列 records fallback；
- 未闭合 fence 和流式 table header holdback；
- HTML/控制字符不会成为终端控制序列。

`diff`：

- context/add/delete/header；
- line number gutter 和 wrapped continuation；
- 多行字符串/注释保持 lexer state；
- light/dark + TrueColor/256/16/none；
- 超限自动关闭 syntax highlight；
- theme scope background 与 fallback background 优先级。

`ansi input`：

- 允许 SGR 16/256/RGB；
- reset 和嵌套 modifier；
- 移除 title、OSC 8、OSC 52、clear、cursor、alt screen、DCS；
- 不完整或恶意 escape sequence 不 panic、不泄漏；
- 单次输入和累计 parser state 有明确大小上限。

### 22.2 结构化 golden

不要只快照 ANSI 字符串。每个关键组件同时保存两种 golden：

1. **Styled golden**：文本 + 标准化 style 属性，便于 review 颜色角色；
2. **Plain golden**：最终可复制文本，保证无颜色语义完整。

示例：

```text
line 0:
  [Tool bold] "Shell"
  [TextSecondary] " go test ./..."
line 1:
  [Success] "✓"
  [TextMuted] " 1.24s"
```

ANSI golden 只用于 backend 编码和 terminal control 集成，不作为所有组件的唯一断言。

### 22.3 Buffer/viewport snapshot

固定场景至少覆盖：

- 主题预览 wide/narrow；
- Markdown code + table + link；
- Diff gallery；
- running/success/error tool cell；
- status line 从完整到逐级折叠；
- popup selection、warning、empty state；
- assistant streaming active tail；
- 40x12、80x24、120x40；
- light/dark、TrueColor/ANSI-256/ANSI-16/NoColor；
- Unicode 与 ASCII icon mode。

### 22.4 终端集成矩阵

自动化 profile：

- Windows Terminal + PowerShell；
- Windows Terminal + WSL；
- VS Code integrated terminal；
- Linux VTE/xterm；
- tmux；
- Zellij；
- legacy Windows console；
- dumb/pipe/CI。

真实 PTY/ConPTY smoke 用例：

- 启动时 OSC 探测超时不会吞用户首个按键；
- theme picker 预览、取消和确认；
- resize 后 code/table/diff/status 不溢出；
- tool output 注入 `ESC[2J`、OSC 52 时屏幕和 clipboard 不受影响；
- `NO_COLOR=1` 输出无 ESC；
- `TERM=xterm-256color` 不生成 TrueColor，除非显式 override；
- Windows Terminal 的 TrueColor override 与 VT processing 一致；
- 退出后恢复 cursor、scroll region、paste/focus mode。

## 23. 性能与资源指标

建议建立以下非功能预算：

| 指标 | 目标 |
| --- | --- |
| 普通 5 KiB Markdown final render | P95 < 10 ms（开发机基线，允许 CI 放宽） |
| 100 KiB code block | 不阻塞输入超过 50 ms；超预算可 plain fallback |
| redraw 频率 | 默认不超过 30 FPS |
| theme/lexer registry | 单例初始化，主题切换不重复全量加载 grammar |
| render cache | 有总字节上限和 LRU，不按 session 无限增长 |
| tool ANSI parser | 输入大小有界，线性时间，不保存无限 incomplete sequence |
| snapshot determinism | 不依赖 wall clock、map 顺序或终端真实主题 |

流式输出不建议继续按每个 rune 固定 sleep 作为主要 UX。应合并 delta，在 16 到 33 ms 窗口内请求一次 redraw；用户可感知的流畅度更好，也减少 ANSI 写入和锁竞争。

## 24. 验收标准

### 24.1 功能验收

- `/theme` 的 semantic palette 能影响所有 UI 语义组件，不再出现 tool call 固定 cyan 而主题无效；
- syntax theme 独立影响 Markdown code、Diff code、shell command 和 theme preview；
- light/dark auto 在可探测终端使用真实背景，探测失败有可解释 fallback；
- TrueColor、ANSI-256、ANSI-16、NoColor 都有确定输出；
- 复杂 Markdown 在 40/80/120 列不越界；
- Diff 同时具有 `+/-`、行号和可选颜色；
- tool ANSI 仅保留 SGR，可证明不能执行终端控制；
- status line 由 typed segment 渲染，不依赖 Ready/Thinking 字符串判断；
- theme picker 取消恢复，确认持久化，非交互路径不变；
- JSON/pipe 输出无 ANSI、OSC 和 UI 提示符。

### 24.2 质量门禁

- 新 UI 组件不得 import `fatih/color`；
- 新 renderer 不得调用 `fmt.Print*`；
- 新业务事件不得保存带 ESC 的“格式化文本”；
- 不新增字节 `len(text)` 作为终端可见宽度；
- 每个新多行组件必须有 40 列和 Unicode golden；
- 每个新颜色角色必须有 NoColor/mono 验证；
- `go test ./cmd/aicli/ui/... ./cmd/aicli/formatter/... ./cmd/aicli/commands/...` 通过；
- 关键终端矩阵 smoke 结果进入发布检查表。

## 25. 风险、回滚与兼容

### 风险 1：新 Markdown renderer 改变既有纯文本布局

控制措施：保留 `markdown_renderer=legacy|structured` 内部 feature flag；先对 transcript replay 和测试 fixture 做双渲染 diff，再切默认。

回滚：切回 legacy formatter，不回退 render core 和 terminal profile。

### 风险 2：OSC 探测吞输入或在 multiplexer 中超时

控制措施：启动阶段有界超时、统一 input decoder、结果缓存、Zellij/tmux profile 和禁用开关。

回滚：关闭 OSC probe，继续使用环境变量与 dark fallback。

### 风险 3：Chroma 增加二进制大小和启动成本

控制措施：记录 before/after 体积和冷启动；registry lazy init；限制自定义 style；必要时裁剪不需要的 formatter 路径。

回滚：syntax highlight 关闭后仍走 structured plain code renderer。

### 风险 4：半迁移期间 ANSI 字符串与 span 混用

控制措施：兼容 adapter 只能位于输出边界；为 `Span.Text` 添加 ESC invariant test；代码审查禁止 component 内 `Sprint`。

回滚：单个 cell 可临时走 legacy adapter，不让 mixed string 进入 render core。

### 风险 5：Unicode width 变更导致既有 snapshot 大量变化

控制措施：先建立 width conformance fixture；集中更新并人工审核布局变化；禁止各模块自定义补丁。

回滚：WidthService 可切换旧/新实现做问题定位，但发布时只保留一个权威实现。

### 风险 6：色彩主题在真实终端不可读

控制措施：contrast test、实际终端截图/人工检查、ANSI-16 禁背景、mono 和 NoColor 逃生路径。

回滚：`status_line_colors=mono`、`diff.background=never`、`syntax_highlight=false` 可独立关闭高风险能力。

## 26. 建议 PR 拆分

每个 PR 应保持可运行和可回退，避免同时修改 `fixed_bottom_surface.go`、Markdown、theme 和所有 command 输出。

| PR | 内容 | 主要文件 | 必须测试 |
| --- | --- | --- | --- |
| 1 | 渲染 inventory、修复 output formatter 明显问题、建立 baseline | `ui/output.go`、现有 tests | legacy golden |
| 2 | `render` model + plain/ANSI backend | 新 `ui/render/*` | style/plain/ANSI unit |
| 3 | WidthService 并先迁移 status/popup | `render/width.go`、`fixed_bottom_surface.go` | Unicode + 40 列 |
| 4 | Semantic palette 纯数据化 + compatibility adapter | `theme.go`、`theme_presets.go`、新 `ui/style/*` | palette matrix |
| 5 | `ColorProfile`、depth、quantize、NoColor | `terminal_driver.go`、新 terminal color | capability matrix |
| 6 | Chroma highlighter | 新 `ui/syntax/*` | lexer/theme/limits |
| 7 | Goldmark 基础 renderer | 新 `ui/markdown/*` | AST fixture/golden |
| 8 | table grid/records + link/code integration | markdown table | 40/80/120 snapshots |
| 9 | assistant final/transcript 接入 structured Markdown | `chat_interaction.go`、`chat_transcript_renderer.go` | streaming/final no duplicate |
| 10 | typed status/timeline adapter | `theme_render.go`、interaction event mapping | legacy equivalence |
| 11 | tool cell + restricted ANSI parser | `toolcall.go`、`shell_feedback.go` | injection suite |
| 12 | structured Diff renderer | 新 `ui/diff/*` | color profile gallery |
| 13 | theme picker live preview/persistence | `fullscreen_list.go`、`chat_theme_command.go` | cancel/confirm snapshots |
| 14 | active Markdown stream + buffer backend | stream/surface/render backend | PTY resize/stream smoke |

每个 PR 的兼容 adapter 必须在说明中标出删除条件，避免临时层永久化。

## 27. 现有文件迁移映射

| 当前文件 | 短期动作 | 目标状态 |
| --- | --- | --- |
| `ui/theme.go` | 已删除 `*color.Color`；字符串方法委托 RoleTextDocument/backend | 仅保存 mode/name、图标和边框字符的轻量 facade |
| `ui/theme_presets.go` | 已删除 fatih preset overlay | registry 管理 semantic palette |
| `ui/theme_render.go` | known/unknown fallback 均构建 semantic spans | 字符串 parser 仅服务 legacy 识别 |
| `formatter/markdown.go` | Goldmark/Chroma 为唯一 owner；字符串 API 注入 live ThemeContext | 已删除仅测 legacy 私有 helper |
| `ui/output.go` | 字符串 API 作为 Document encoder；code 使用 Chroma | 只保留必要通用 adapter |
| `ui/message.go` | 保留 sanitize/bidi 测试，输出改为 cell Document | message cell renderer |
| `ui/toolcall.go` | 移除全局硬编码颜色，参数稳定排序 | typed tool cell |
| `ui/shell_feedback.go` | 输出经 restricted ANSI/plain parser | 合并到 tool/exec cell |
| `ui/statusbar.go` | item 已改 typed role/cell width；Color callback API 已删除 | status line renderer |
| `ui/fixed_bottom_surface.go` | 仅接收 `StatusLineModel` 和 backend 输出；状态字符串 parser 已删除 | 只管理 surface/layout/cursor |
| `ui/fullscreen_list.go` | 增加 selection/cancel/confirm hook | 通用 searchable selection view |
| `ui/progress.go` | Document/Format + MotionPolicy；Spinner 生命周期已并发收口 | 组件只描述活动状态 |
| `commands/chat_theme_command.go` | 使用 picker state 和 theme registry | 命令只协调选择/持久化 |
| `commands/config_tui.go` | 后期共享 style/render backend | 仍保留线性交互 fallback |

## 28. 架构决策记录

### ADR-1：先建 render IR，不先换 TUI 框架

**决定**：采用自有最小 `Document/Line/Span/Style`，适配现有 surface。

**原因**：当前 fixed-bottom、popup、输入和 terminal driver 已有大量行为与测试。直接换框架会把输入、布局、颜色和业务事件同时置于高风险迁移中。

**后果**：需要维护少量基础模型，但可同时服务 linear、fixed-bottom 和 frame 三种 backend。

### ADR-2：Goldmark + Chroma，而不是继续扩正则

**决定**：Markdown 和代码分别使用成熟 parser/lexer。

**原因**：嵌套语法、表格、围栏和 token 解析不应由 UI 项目重复实现；结构化输出又要求比直接 ANSI formatter 更底层的 API。

**后果**：增加依赖和初期 renderer 工作，但行为可测试、可宽度感知。

### ADR-3：semantic palette 与 syntax theme 分轴

**决定**：保留 aicli 产品语义配色，同时增加独立语法主题。

**原因**：错误/成功/审批需要稳定含义，代码 token 需要语言 scope。把二者合并会导致 `/theme` 行为难以解释。

**后果**：配置多一个字段，但默认 `syntax=auto` 不增加普通用户负担。

### ADR-4：ANSI 只在边界生成，外部 ANSI 只允许 SGR

**决定**：内部不传 ANSI 字符串，工具 ANSI 经过 parser 白名单转换。

**原因**：统一测宽、降色、主题、布局和安全边界。

**后果**：需要 compatibility adapter；部分复杂命令输出控制效果会被有意移除。

### ADR-5：低色深优先可读，不追求近似还原

**决定**：ANSI-16 下禁用大多数自定义背景和 RGB 模拟。

**原因**：高饱和背景容易导致不可读，颜色也不能作为唯一信息。

**后果**：低色终端更朴素，但语义稳定。

## 29. Definition of Done

本方案不是以“新增主题数量”作为完成标准。只有同时满足以下条件，渲染重构才算完成：

1. 所有主要 UI 内容先成为结构化 render model，再由 backend 输出。
2. Semantic palette、syntax theme、terminal profile 三轴分离且可独立测试。
3. Markdown 使用 AST，代码使用 lexer，JSON 使用 parser，Diff 使用结构化模型。
4. 工具输出有明确的 plain/ANSI 信任边界，控制序列注入测试通过。
5. 全项目只有一套 terminal visible width、wrap 和 truncate 逻辑。
6. `/theme` 支持真实内容预览、取消恢复和确认持久化。
7. TrueColor、ANSI-256、ANSI-16、NoColor 都有快照与真实终端验证。
8. 40 列窄屏下 transcript、table、diff、status、popup 不发生不可控溢出。
9. JSON/pipe/CI 行为保持脚本兼容，没有 UI 控制序列。
10. legacy adapter 数量有明确清单，主要新代码不再依赖 `fatih/color` 或格式化字符串。

## 30. 最终建议

实施顺序应坚持“先表示，再解析，再美化，最后动画”：

```text
结构化 Span/Style
  -> 统一宽度与终端 profile
  -> Markdown/Chroma/Diff/ANSI parser
  -> typed cell 和 status segment
  -> theme picker 与 live preview
  -> active cell/frame diff/motion
```

如果先增加更多颜色和关键词分支，而内部仍传递 ANSI 字符串，主题配置越丰富，宽度、降级、安全和一致性问题会越难收敛。对 aicli 而言，最有价值的 Codex 经验不是某一组配色，而是把样式当作结构化数据、把终端能力当作运行时输入、把所有可见输出当作同一渲染系统的投影。
