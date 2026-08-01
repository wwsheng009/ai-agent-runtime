# aicli UI/UX 渲染实施审查

状态: **内容渲染核心已审查并完成高优先级收口；物理终端所有权、history/gap 与终端集成验证由后续专项承接**

审查日期: **2026-07-29**

文档关系更新: **2026-07-31**

关联方案:

- [aicli-ui-ux-rendering-codex-reference-plan.md](./aicli-ui-ux-rendering-codex-reference-plan.md)
- [aicli-ui-refactor-codex-inspired-plan.md](./aicli-ui-refactor-codex-inspired-plan.md)
- [aicli-ui-rendering-phase0-inventory.md](./aicli-ui-rendering-phase0-inventory.md)
- [aicli-tui-p5-owned-viewport-design.md](./aicli-tui-p5-owned-viewport-design.md)
- [aicli-tui-unified-render-architecture-refactor-plan.md](./aicli-tui-unified-render-architecture-refactor-plan.md)

> 本审查的“无需再次重写 TUI”是指不需要推翻已经形成的 `Document -> Block -> Line -> Span`、主题、宽度、Markdown/Diff 和安全解析能力；它不表示 physical screen ownership、raw/direct output、history identity、跨 cell gap、fullscreen lifecycle 和 exactly-once handoff 已经解决。这些跨模块问题分别以 P5 专项文档和统一长期架构文档为准。本文 F32 的 `ApplyBlockSpacing` 只负责 Markdown/Document 或单个 cell 内部间距，不负责 top-level transcript cell 的 `BoundaryPolicy`。

## 1. 审查结论

当前工作树已经实现方案 Phase 0-5 的主要代码骨架，不需要再次重写 TUI。已确认存在以下核心能力：

- `Document -> Block -> Line -> Span -> Style/Color` 结构化渲染 IR；
- ANSI、Plain、Buffer/Frame backend；
- 基于 `uniseg` 的 grapheme/终端 cell 宽度；
- 语义 palette、light/dark mode、终端色深 profile；
- Goldmark Markdown AST renderer 与 Chroma v2 token 高亮；
- 宽度感知表格、records 降级、结构化 Diff；
- typed timeline/tool/status/active cell；
- 受限 ANSI 输入解析、head/tail 工具结果预览；
- `/theme` 搜索、实时预览、取消恢复、确认持久化；
- MotionPolicy、Markdown stream holdback、active stream 与 fixed-bottom active band。

审查同时发现，原方案首页的“Phase 0-5 核心完成”掩盖了若干会直接影响用户行为或安全边界的问题。本轮已修复启动配置、语法主题、ANSI 注入、Unicode 截断、主题富预览和 ANSI-256 Diff 等高优先级缺陷。真实终端探测、PTY/ConPTY 矩阵和遗留 adapter 清理仍属于后续工作，不能视为已经验收。

## 2. 本轮修复

| 编号 | 审查发现 | 实施结果 |
| --- | --- | --- |
| F1 | 配置中持久化的 `aicli.theme.syntax` 启动时未加载 | 启动 bootstrap 现在读取 config、环境变量和 `--syntax-theme` |
| F2 | `CurrentThemeContext()` 把 syntax 硬编码为 `auto` | 改为读取当前 syntax preference，再由 ThemeContext 解析 |
| F3 | Chroma 默认实例固定 `monokai`，全局切换无效 | `DefaultTheme` 改为 `auto`，空请求跟随全局默认 |
| F4 | `syntax=auto` 被持久化/归一化为 `monokai` | `auto` 作为独立 preference 保留；light -> `github`，dark -> `monokai` |
| F5 | mode 运行时切换后，formatter 全局 syntax 不刷新 | `SetTheme`、`SetThemeMode`、`ApplyThemeSelection` 会同步 auto syntax |
| F6 | ANSI/Plain backend 信任 `Span.Text`，可能输出注入的 ESC/CSI | backend 边界统一清理控制序列和意外换行 |
| F7 | OSC 8 URL 未校验，可通过 ST/BEL 终止 payload | hyperlink payload 增加控制字符、长度、URI scheme 校验 |
| F8 | `SanitizeKeepSGR` 接受畸形 SGR/OSC 8，未闭合状态可污染提示符 | 只保留规范 SGR 与安全 OSC 8，并补 link close/SGR reset |
| F9 | `ANSIToSpans` 用 `Span.Text == "\n"` 表示换行 | 新增 `ANSIToLines`；换行只存在于 `Line` 边界 |
| F10 | CSI 参数校验包含永远不成立的条件 | 按 CSI 参数/中间字节范围修正，并增加 malformed/长 OSC 测试 |
| F11 | 工具预览按 rune 截断，中文/emoji 可能越界 | 统一改用 `render.Truncate` 终端 cell 截断 |
| F12 | `AllowANSI` 预览先转 plain 再解析，实际丢失 SGR | 直接保留 `ANSIToLines` 产生的结构化 style |
| F13 | 全屏主题 rich preview 在测宽前调用 sanitizer，颜色被剥离 | 安全 SGR 解析为 span、按可见 cell 截断并重新编码 |
| F14 | ANSI-256 Diff 分支声明支持背景但返回空颜色 | dark/light 分别使用经过固定验证的 indexed tint |
| F15 | classic palette 注释称仅 label 绿色，实际可染整段 assistant body | `RoleAssistant` 恢复默认前景，强调由 Accent/label role 承担 |
| F16 | Chroma、Goldmark、uniseg 被标成 indirect | `go mod tidy` 后归入直接依赖，清理未使用的 emoji 依赖 |
| F17 | 配置 TUI 概览只显示 palette，看不到 mode/syntax | 概览增加 `Theme mode` 与 `Syntax theme` 独立行 |
| F18 | 流从 plain 升级为 Markdown 时 collector 未包含已有前缀 | 升级时先 seed 既有 body，活动带与 finalize 均保留完整内容 |
| F19 | chat 状态先拼字符串，surface 再按关键字反推 state/role | 生产路径改为直接构建并传递 `StatusLineModel`；空闲态用 `HideState` 保持 model-first 布局，旧字符串 API 继续兼容 |
| F20 | active stream 经 plain buffer 后整行统一着色，span 语义丢失 | `BufferBackend` 保留 styled lines，fixed-bottom surface 直接消费结构化 active frame，并在边界执行控制序列清理与 cell 截断 |
| F21 | timeline supplement 的未完成迁移引用不存在的局部变量，导致 `ui` 包不能编译 | 修正为逐行尝试 typed timeline document，无法等价投影时回退 legacy formatter |
| F22 | Markdown stable prefix 在 active cell 中仍按普通文本逐行投影 | stable prefix 直接进入 Goldmark renderer；heading/list/table/inline role 与 Chroma code token style 进入 styled active frame，未闭合结构继续留在 muted holdback |
| F23 | styled active band 仍用 legacy role adapter 编码，显式 token RGB 被覆盖 | 改用 surface 已有 `TerminalDriver.ColorProfile` 构建 `ThemeContext`，统一经 Resolver/ANSI backend 做 TrueColor、256、16、NoColor 降级 |
| F24 | 活动带只显示尾部数行，但每帧仍克隆完整 Markdown document | Markdown document 按 source/width/resolved syntax theme 缓存；BufferBackend 从 block tree 尾部反向取可见行，避免长回复的全树复制 |
| F25 | active frame diff 只比较 plain text，token 颜色变化不会成为重绘信号 | 增加 structured `LinesEqual`，controller 同时比较文本和 span style/link；syntax theme 的纯样式变化可直接触发一次重绘 |
| F26 | 100 KiB fenced code 的同步 Chroma 首次渲染约 176 ms，不适合阻塞活动视口 | active preview 使用独立的 64 KiB/2000 行预算；超限静默退化为结构化 plain code，最终 transcript 仍保留原 512 KiB 高亮预算 |
| F27 | `/theme` 富预览先拼 TrueColor ANSI 字符串，再由全屏列表反向解析和截断 | 新增 `ThemePreviewDocument`；semantic swatch、Chroma code、Diff 全部保留为 block/line/span，最终才按实际或注入的 ColorProfile 编码 |
| F28 | 大 Diff 默认可同步进入 Chroma，且无法识别的多行输入被塞进单个 Span | Diff 使用独立 64 KiB/2000 行高亮预算；超限保留增删/行号语义并跳过 lexer；fallback 改为安全的逐行 IR |
| F29 | Markdown/Diff/theme picker/typed status 只有局部断言，缺少可审查的完整 buffer baseline | 新增 40/80/120 列 styled+plain golden，逐 span 固化 role、RGB/ANSI 色、modifier、link、截断和折叠结果 |
| F30 | Diff 没有大输入性能基线 | 增加 100 KiB unified parse/render benchmark；本机解析约 0.12 ms，结构化渲染约 19.6 ms，未进入超预算 Chroma |
| F31 | typed tool cell 仍固定使用 focus/dark/TrueColor，pipe、用户主题和低色深能力被绕过 | `FormatToolCall` 改用 `CurrentThemeContext`；删除六个未使用的 `fatih/color` 全局色，新增 NoColor/ANSI-16 profile 回归测试 |
| F32 | Goldmark 把源文本空行当作块分隔符消费后没人补回，Markdown 段落、列表、代码块和表格在输出里全部贴在一起 | 新增 layout 阶段 `render.ApplyBlockSpacing` + `SpacingPolicy`（`BlockSpacer` 块），markdown renderer 默认插入单空行、同类 list/quote 保持紧凑；`markdown.Options.Spacing` 可退回 legacy 稠密布局 |
| F33 | 流式活动带高度写死（buffer 8 行、surface `ActiveBandMaxRows=6`，算 8 保 6），既不随终端高度变化，也不在会话中跟随 resize | 新增 `ui.ActiveBandRows(terminalHeight)` 自适应预算（1/3 屏高，min 6 / max 14，并保留 12 行给输出与输入区）；surface 用 `ActiveBandRowBudget`/`ActiveBandViewportSize` 统一裁剪与绘制，`BottomPaneState.ActiveBandMaxRows` 随快照下发；`ActiveStreamController.SetViewport` 在每帧同步宽高且仅在变化时请求重绘 |
| F34 | session info、usage 与 table 虽已有结构化骨架，但仍存在字节宽度、map 随机顺序、窄终端溢出和 legacy role adapter | `SessionInfoDocument`/`TableDocument` 正式接入当前 ThemeContext；表格统一按 terminal cell 测宽，按 viewport budget 有界分配列宽并截断；key/config 输出稳定排序；新增 `session-info`/`info-table` 40/80/120 golden |
| F35 | input 与 shell feedback 已生成 Document，却仍通过 `renderDocWithTheme` 使用 legacy `fatih/color`，绕过 NoColor/ANSI-16/256/TrueColor profile | 新增 `ThemeContextForTheme`/`renderDocumentWithProfile` 迁移桥；prompt、InputBox、ShellFeedback、shell command/output/summary 均通过语义 palette 与真实 ColorProfile 编码，并增加 NoColor/ANSI-16 回归 |
| F36 | fullscreen rich preview 截断后固定用 TrueColor backend 重编码，低色深和 `NO_COLOR` 失效，且每帧若临时探测 profile 会放大开销 | full-screen session 进入 raw mode 前从既有 `TerminalDriver` 取得一次 ColorProfile，经 loop hooks 注入 frame，避免与按键 decoder 抢读；preformatted SGR 在截断后按该 profile 重编码，ANSI-16 量化、NoColor 去色，光标/反显控制仍由 surface 自有协议管理 |
| F37 | 100 KiB 表格单元格可能令旧 overflow 逐格缩列循环接近十万次，单 Span 截断还会重复完整测宽 | 表格改为与 viewport 宽度相关的 budget allocator；`render.Truncate` 复用行宽并为已知溢出的文本走单次 grapheme 截断扫描；补 `BenchmarkTableDocument100KiBCell` 与 `BenchmarkTruncateText100KiB` |
| F38 | 窄终端 Diff 对超宽内容直接截断，用户无法看到行尾改动；固定 gutter 还会裁掉大行号，增删背景也未利用 syntax theme 的 diff scopes | content row 改为保留 gutter/sign 的 style-aware wrap，续行使用等宽空 gutter，整份文档按最大行号动态扩宽 gutter，token style 和全部字符均保留；syntax theme 声明 `GenericInserted/GenericDeleted` 背景时优先采用，并按 ColorProfile 降级；40 列 diff golden 更新为可审查的续行布局 |
| F39 | 提示符隐藏时（流式过程中）活动带按 `outputBottomRowLocked()` 定位，被画进滚动区并覆盖尾部输出，而它在 `bottomRowsLocked` 里预留的行留在状态行上方全空——屏幕底部出现与带高相同数量的空行 | `promptBottomRowLocked` 把"活动带可见"与 popup gap/prompt reserve 同等对待，底部栈统一锚定到 `statusRow-1`；新增 24/40/60 行 × prompt 显示隐藏 × 1/2/3/6/自适应带高的布局不变量回归（带起始行必须等于 `outputBottom+1`，栈底必须紧贴状态行） |
| F40 | `Terminal.updateSize()` 探测非 TTY 时永远回落 80x24，`EnableForTest(80, 40)` 的高度会被下一次 layout 覆盖，导致高终端布局无法测试 | 新增 `Terminal.SetSizeForTest` 固定几何（非正值恢复真实探测），`EnableForTest` 与 ui 包测试 helper 统一使用；新增 `screenVT` 最小 VT100 回放（滚动区、CUP、EL、IND/RI），在 commands 包按 24/40 行重放完整流式回合，断言活动带绘满到 `statusRow-1`、提交后 transcript 与提示符之间最多一行空行 |
| F41 | system message 仍使用魔法字符串 `Role("System")`，且 message/status/separator/layout 虽有 Document，最终仍经 legacy `fatih/color` adapter，无法统一执行 palette/profile 降级 | 新增 RequiredRole `RoleSystem`，focus light/dark、classic、contrast、mono 与 custom palette fallback 全部覆盖；message/status/separator/layout/timeline 正式改用 `renderDocumentWithProfile`，主题预览加入 system swatch，并补 NoColor/ANSI-16 跨组件回归 |
| F42 | fixed-bottom 生产状态行、legacy plain active band 与 prompt notice 仍直接调用 Theme color，导致主 surface 的一部分内容绕过其已持有的 TerminalDriver ColorProfile | 状态 model/legacy line 增加 ThemeContext 渲染入口；生产 `renderStatusLocked` 复用 surface profile；plain active band 与 notice 转成 `RoleInfo`/`RoleTextMuted` Document 后和 styled band 共用同一 Resolver/backend；测试改为断言 plain projection、语义差异和 ANSI-16 降级，随后删除无生产调用的 `renderDocWithTheme`/`colorizeRoleWithTheme` 及 legacy fixed-status formatter |
| F43 | 独立 `Progress/Spinner`、旧 `StatusBar`、welcome 与 deprecated output helpers 仍直接执行 `Theme.XColor.Sprint`，并存在负进度、Spinner 停止后不可重启、Unicode 清行按字节、代码块重复 fence/language 等问题 | 全部新增/改用 Document API：`Progress.Document/Format`、`Spinner.Document/Format`、`StatusBar.Document`、`WelcomeDocument`、`FormatOutputDocument`、`CodeBlockDocument`、`KeywordDocument`；最终统一经 Resolver/Profile backend；进度值和宽度有界，Spinner 加锁且每次启动重建 stop channel，清行按 terminal cell，代码块直接使用 Chroma token spans，关键词采用最长优先非重叠匹配 |
| F44 | Theme 公开兼容方法和 commands 菜单/会话/exec 输出继续先生成 fatih ANSI 字符串，再参与 `%*s` 对齐或直接 `fmt.Printf`，导致低色深、pipe、安全和 Unicode 对齐继续分叉 | 新增 `RoleTextDocument`、`RenderRoleTextWithTheme` 与显式 profile bridge；Theme `Format*`/`Colorize*`/`Dimmed` 全部变为 Document encoder；commands 增加 typed `chatTextPart` 输出边界，选择菜单把可信本地 SGR 解析回 span 后按当前 profile 重编码，会话信息按 cell width 对齐，exec event/delta 先消毒再输出；生产代码审计已无 `.XColor.Sprint/.Sprintf` 调用 |
| F45 | canonical Markdown AST 已结构化，但 `MarkdownFormatter.Format` 字符串适配器固定 TrueColor，chat 热切换/ANSI-16/pipe 会绕过真实 ThemeContext；同文件 legacy 私有 helper 仍直接 `color.New(...).Sprint` | `MarkdownFormatter` 增加无包循环的 `ThemeContextProvider`，chat 注入 `ui.CurrentThemeContext`，`ui.FormatMarkdownWidth` 改为 `FormatDocument -> renderDocumentWithProfile`；legacy helper 也改用 render/style backend 与 Chroma，不再直接执行 fatih color；新增注入 ANSI-16 与 UI Markdown ANSI-16 回归 |
| F46 | `TerminalDriver`/`CurrentColorProfile` 传入 `DepthOverride="auto"` 时，`detectDepth` 把它当作显式值，反而跳过 `AICLI_COLOR_DEPTH`，使诊断/兼容覆盖在 `COLORTERM=truecolor` 等环境下失效 | `auto` 与空值统一继续读取 `AICLI_COLOR_DEPTH`，随后才做终端环境推断；新增 truecolor 环境下 env 强制 ANSI-16 的优先级回归 |
| F47 | `PrintStatusColored`、`Layout.UpdateStatusWithColor`、`StatusItem.Color` 与 `StatusBar.Update(..., colorFunc)` 仍允许任意字符串着色函数进入组件边界，虽然新 renderer 已忽略 callback | 删除全部 color callback API；`StatusBar.Update/UpdateWithWidth` 只提供默认语义角色，定制值改用 `UpdateRole/UpdateWithWidthRole`，Layout 对应提供 `UpdateStatusRole`；测试直接断言 Document 中的 role |
| F48 | fixed-bottom 同时持有 `statusLine string` 与 `StatusLineModel`，旧 `SetStatusLine` 还会触发 `ParseLegacyStatusLine` 关键字反推，形成双 owner | 删除字符串字段、setter、parser、legacy formatter 与 fallback 分支；surface 初始化即持有 `RunReady` model，渲染、快照、消毒和 cursor 恢复全部走 `StatusLineModel -> StatusLineDocument` |
| F49 | Theme 同时保存语义 palette 名和 18 个 `*color.Color` 字段，preset 在两套颜色系统中重复维护且可能漂移 | 删除 Theme 全部颜色对象、`color.New` 初始化、`disableColors` 和 preset overlay；Theme 只保存 mode/name、图标与边框字符，颜色唯一 owner 为 `style.Palette`；同时补齐 classic 的 meta/tool/info 语义强调，并修正 mono 复用 Style 时错误携带 `Accent/TextPrimary` role 的问题 |
| F50 | `formatter/markdown.go` 虽以 Goldmark/Chroma 为生产 owner，仍保留约 650 行私有 line scanner、table/fence/inline regex renderer 和自制 rune width/ANSI strip，测试继续绕过 canonical renderer | 删除完整 legacy scanner/helper 链；标题、引用、inline code、强调和链接测试统一通过 `FormatDocument` 检查 plain projection、role、modifier 与 link；ANSI plain 投影改用 `render.ANSIToPlain`，Markdown 现在只有 AST renderer 一个实现 |
| F51 | FrameScheduler 在突发 delta 中可能于 active viewport 只有 5 行时开始合并后续帧；若网络随后短暂停顿，14 行预算会一直显示陈旧短帧 | active viewport 未填满且内容行数继续增长时允许绕过 FPS credit，填满后仍按原 30 FPS 合并 tail 更新；新增同一时间戳 burst 填满 viewport 回归，并修正空洞测试只统计首个内容之后的内部空白，不把终端顶部未使用区域误判为 transcript/band gap |
| F52 | 新 backend 已拥有完整 NoColor/profile 降级，但生产桥和测试仍通过 `fatih/color.NoColor` 全局变量覆盖输出，形成第二个颜色决策源，也使显式 TerminalDriver profile 可能被进程全局状态否决 | 删除所有 `colorDisabled` 分支，让 `ThemeContext.Terminal` 成为唯一颜色能力来源；测试改用可恢复的 `NO_COLOR`/`FORCE_COLOR`/`AICLI_COLOR_DEPTH` profile 输入；删除未使用的 `Terminal.PrintWithColor` 任意回调入口，并从 go.mod/go.sum 移除 `fatih/color`、`go-colorable`、`go-isatty` |
| F53 | runtime event 已按事件类型生成内容，却仍只把 `chatRuntimeTimelineEvent.Line` 交给 `RenderAsyncLine`，随后再由 `LegacyTimelineParser` 反推 planning/team/task/input/tool/progress/approval 等语义 | `chatRuntimeTimelineEvent` 增加 typed `TimelineEvent`/`Document`；bridge 新增 Document writer，interaction 增加 `RenderAsyncDocument`，正常 TUI 路径直接消费 IR。planning、team、task route、subagent、mailbox、input queue、tool requested/普通 completed/denied/progress、approval、LLM retry/finished、compact、prompt preflight、reasoning 等已按源事件构建 kind/status/tag/detail spans；测试保留 plain `Line` 仅作为日志/断言投影。edited-diff tool result 暂保留专用 `FileDiff` 字符串兼容入口，避免 NoColor 布局回归 |
| F54 | `Terminal.PrintRight`、`PrintCenter`、`DrawBox` 是无调用的旧式字符串布局 API，其中 `DrawBox` 还按 byte 长度判断标题并直接 `fmt.Print` Unicode 边框，绕过 cell width、Document 和 surface writer | 删除三个无调用入口；保留的 `PrintAt` 仅作为 Layout 已使用的低级定位/串行写入 primitive，不承担颜色或组件布局 |

## 3. 启动配置契约

主题三轴继续保持独立：

```text
semantic palette: classic | focus | contrast | mono | custom-*
light/dark mode:  auto | light | dark
syntax theme:     auto | <Chroma style name>
```

启动优先级为：

```text
CLI flag > environment > config file > built-in default
```

具体入口：

| 轴 | CLI | 环境变量 | YAML |
| --- | --- | --- | --- |
| palette/mode | `--theme` | `AICLI_THEME`、`AICLI_THEME_MODE` | `aicli.theme.name/mode` |
| syntax | `--syntax-theme` | `AICLI_THEME_SYNTAX` | `aicli.theme.syntax` |

兼容保留 `AICLI_SYNTAX_THEME` 旧文档别名；两者同时存在时，`AICLI_THEME_SYNTAX` 优先。

`syntax=auto` 不再被改写为具体主题。它作为持久选择保留，渲染时按有效 mode 解析：

```text
light -> github
dark  -> monokai
```

这样 mode 从 light 切换到 dark 时，Markdown、Diff、preview 和使用全局 highlighter 的 formatter 会同步变化。

## 4. 安全与降级边界

结构化渲染现在执行两层防护：

1. 外部终端文本通过 `ANSIToLines` 解析，只保留 SGR 数据，丢弃 cursor、erase、alternate screen、OSC title、OSC 52、DCS/APC/PM 等序列。
2. ANSI/Plain backend 在最终输出边界再次清理 `Span.Text`，防止调用方绕过 parser 直接构造不合法 IR。

OSC 8 只允许无控制字符、长度不超过 4096 bytes 且 URI 可解析的链接。允许的 scheme 为 `http`、`https`、`mailto`、`file` 和相对链接；其他 scheme 不生成超链接控制序列，但可见 label 仍保留。

颜色降级规则保持确定性：

| Profile | 前景 | Diff 背景 | 超链接 |
| --- | --- | --- | --- |
| TrueColor | RGB/Indexed/ANSI | RGB tint | 能力允许时启用 |
| ANSI-256 | 量化到 indexed | indexed tint | 能力允许时启用 |
| ANSI-16 | 经典 16 色 | 禁用自定义背景 | 能力允许时启用 |
| NoColor/pipe | 无 SGR | 无背景 | 禁用 |

颜色不是唯一信息来源。Diff 始终保留 `+/-`，tool/status 始终保留 marker 和文字，plain backend 保留全部可见内容。

## 5. 验证结果

已通过：

```powershell
go test -timeout 90s ./cmd/aicli/ui/...
go test ./cmd/aicli ./cmd/aicli/formatter
go test -timeout 90s -run "<theme/stream/transcript focused tests>" ./cmd/aicli/commands
go test -count=1 -vet=off -timeout 180s ./cmd/aicli/commands
go test -race -count=1 -run "Progress|Spinner|StatusBar" ./cmd/aicli/ui
go vet ./cmd/aicli/ui/... ./cmd/aicli/formatter/... ./cmd/aicli ./cmd/aicli/commands
go mod tidy
```

覆盖的新增回归场景包括：

- syntax 启动优先级与 `--syntax-theme` root flag；
- auto syntax 在 light/dark 下的解析和运行时同步；
- Chroma 空 Theme 请求跟随全局选择；
- `Span.Text` 清屏/BEL 注入；
- OSC 8 ST/OSC 52 payload 注入、file link 正常路径；
- malformed CSI、长 OSC、换行不进入 Span；
- CJK cell 宽度截断、工具 SGR 保留；
- 全屏 rich preview 保留 SGR 且剥离清屏 CSI；
- ANSI-256 Diff indexed background；
- classic assistant 正文不被整段着色；
- typed status 空闲/审批状态、宽度折叠、单行消毒与 cursor 恢复；
- styled active frame 的语义 role、CJK cell 截断、NO_COLOR 与 CSI/OSC 注入清理；
- active Markdown heading/code 投影、open-fence holdback、Chroma token style、syntax theme/resize cache invalidation；
- active Markdown 在 40/80/120 列、最多 6 行 viewport 下的宽度基线；
- 超过 active 高亮预算的代码静默降级且保留可见尾部，不泄漏 `limit_exceeded` 技术标签；
- theme preview 的 NoColor、ANSI-16、TrueColor 编码路径，不再固定输出 TrueColor；
- typed tool formatter 跟随当前 ThemeContext，pipe/NoColor 无 ESC，ANSI-16 不输出高色深序列；
- session info/table 的 CJK cell 对齐、稳定排序、窄终端列预算与 40/80/120 golden；
- input 与 shell feedback 跟随真实 ColorProfile，NoColor 无 ESC，ANSI-16 不泄漏 256/TrueColor SGR；
- system/message/status/separator/layout 使用正式 RequiredRole 与真实 ColorProfile，跨组件 NoColor/ANSI-16 回归通过；
- fixed-bottom model-only status、plain active band 和 notice 与 styled band 共用 surface ThemeContext；
- Progress/Spinner 的值域、Unicode 消毒、ANSI-16 编码和停止后重启；
- StatusBar typed role、terminal-cell padding/truncate，任意 `Color` callback API 已删除；
- output/code/keyword/welcome 的 Document 投影、Chroma token、最长非重叠匹配和 metadata cell 对齐；
- commands provider/model/theme/reasoning picker、session preamble 与 exec events 不再预着色后参与字符串宽度计算；
- MarkdownFormatter 注入 live ThemeContext，chat/theme 热切换和 ANSI-16 不泄漏 256/TrueColor SGR；
- Markdown 私有 line scanner/table/fence/inline renderer 已删除，相关测试统一覆盖 Goldmark/Chroma Document；
- Theme 不再持有 `*color.Color`，classic/mono 的 RequiredRole 身份和完整性由 semantic palette 测试覆盖；
- `color.NoColor` 测试已迁到 `NO_COLOR`/`FORCE_COLOR`/`AICLI_COLOR_DEPTH`，全仓 Go module 不再包含 `fatih/color`；
- runtime timeline 的 plain 投影保持稳定，同时 planning/team/task/input/tool/progress/approval/LLM/compact/reasoning 直接携带 `TimelineEvent`/`Document` 并绕过 legacy line writer；
- active burst 在 viewport 未填满时可继续增长，填满后恢复 FPS 合并；内部空洞检测忽略终端顶部天然空白；
- fullscreen rich preview 使用 session 注入的 ColorProfile，NoColor 去色且 ANSI-16 保留安全 SGR；
- 窄终端 Diff 续行保持 gutter 对齐、token style 与完整字符，并验证 syntax theme diff scope 背景回退；
- 大 Diff 超预算时不调用 Chroma，但继续保留 `+/-`、行号和 add/delete semantic role；
- Markdown、Diff、theme preview（含 System role）、typed status、session info、info table 的 40/80/120 styled/plain buffer golden。

新增 benchmark 在本地 Windows/AMD Ryzen 7 5800H 的一次观测值如下，仅用于建立量级，不作为跨机器硬门禁：

| 场景 | 本地观测 |
| --- | --- |
| 100 KiB cached active Markdown repaint | 约 `0.1-0.2 ms/op`，`11 KiB/op` |
| 100 KiB code 的 active 首次预览（预算降级） | 约 `18 ms/op`，`1.7 MiB/op` |
| 100 KiB Markdown 首次 AST render | 约 `30 ms/op` |
| 100 KiB Go fenced code + Chroma | 约 `176 ms/op` |
| 100 KiB unified Diff parse | 约 `0.12 ms/op`，`376 KiB/op` |
| 100 KiB Diff structured render（预算降级） | 约 `19.6 ms/op`，`2.6 MiB/op` |
| 100 KiB 单文本 Span 截断 | 约 `15.3 ms/op`，`857 B/op`，`10 allocs/op` |
| 含 100 KiB 单元格的 table Document | 约 `30.0 ms/op`，`6.4 KiB/op`，`46 allocs/op` |

活动帧已通过 document cache 和反向 tail layout 避免重复解析及全树 line clone。首次 code highlight 仍是最重路径，后续需要在真实 CI/终端样本上确定预算，并评估超长单块的后台或分段高亮。

完整 `go test ./cmd/aicli/commands` 曾在
`TestBuildSharedChatPromptPreflightCompactor_RetainsFrozenTools` 上挂起：
mock 返回非 SSE JSON，而 `LocalAdapter.streamSummaryResponse` 走 Stream；
当 session config 为 nil 时 `ProviderMaxRetriesFromAgentConfig` 得到 `-1`（无限重试），
`forwardStreamWithRetry` 在错误流上反复退避，直至 package timeout。
已修复：compact 请求设置 `disable_retries`、auto-compact runtime 将 `-1` 收敛为 `0`、
测试 mock 改为 SSE，并新增 non-SSE 快速 fallback 回归。

并行开发中的 site-account 自动探测一度访问 ConfigTUI 测试 mock 未声明的
`/api/v1/status`、`/setup/status`、`/health` 等路径；mock 改为允许 best-effort probe 后，
本轮最新 `go test -count=1 -vet=off -timeout 180s ./cmd/aicli/commands`
已在约 38 秒内通过。本轮新增的 mid-stream active-band 回归、UI/formatter/main 全量测试、
golden、race 与 vet 同样通过。

F52-F54 完成后的最新复验中，UI/formatter 全量、golden、race 均直接通过；commands
全量在约 39 秒内通过。复验期间并行开发新增但尚未接完的
`internal/api/skills/siteaccount_handlers.go` 与
`internal/runtimeserver/siteaccount_service.go` 会使正常 package load 失败，因此 commands/main/vet
复验使用 Go overlay 仅排除这两个未跟踪文件。去掉 overlay 后的失败只包含
`Handler.siteAccountService` 等并行 site-account 编译错误，和本轮渲染改动无关；正式发布门禁仍必须在
该并行功能完成后以无 overlay 命令再跑一次。

`go test -run "^$" ./...` 已成功编译仓库中的正式 package，最后仅在被
gitignore 的本地 `backend/tmp` 调试目录失败：`md_debug.go` 与
`md_debug2.go` 同时声明 `main`。这些文件不是本轮改动，审查未删除或覆盖；
发布检查应排除本地临时目录，或将调试程序拆为独立 package/build tag。

## 6. 剩余工作与优先级

### P0：发布前终端验证

- 建立 Windows Terminal/ConPTY、VS Code terminal、xterm、tmux、dumb/pipe smoke；
- 验证 resize、退出时 cursor/scroll region/mode 恢复；
- 验证 `NO_COLOR`、ANSI-16、ANSI-256、TrueColor 的真实输出；
- 验证 typed status 与 styled active band 在 40x12、80x24、120x40 下的真实终端布局；
- commands compactor stream 超时已隔离修复（`disable_retries` + 有限 MaxRetries + SSE mock / non-SSE 快速 fallback 回归）；site-account probe mock 已允许 best-effort 探测，最新完整 commands 在约 38 秒内通过，发布流水线应继续保留该全量门禁。

### P1：终端能力与 surface 深化

- 已落地离线 `ParseOSCColorReply` + `ColorProfile.WithDefaults`，并经 `DetectColorProfile` 应用 `AICLI_OSC_FG`/`AICLI_OSC_BG` 与 injectable `DefaultFG`/`DefaultBG`；
- 已落地有界 live OSC 10/11 探测：`style.ProbeOSCDefaultColors`（可注入 Writer/Reader、默认 50ms deadline、拒绝无 deadline 的阻塞 reader、读字节上限）、`DetectOptions.OSCProbe` 仅补齐缺口、不覆盖 injectable/env；`AICLI_DISABLE_OSC_PROBE` 同时跳过 env 与 live；`TerminalDriver.ColorProfile` / `CurrentColorProfile` 在 interactive+ANSI 下挂载 process-once `LiveOSCProbe`（能力 refresh 不探 stdin）；与 raw editor 的长期协调仍可在统一 input decoder 阶段再收紧；
- active band 已让流式 Markdown stable prefix 产出 AST/Chroma spans；后续重点转为真实终端视觉验收与超长单块增量解析策略；
- styled active stream 与 Markdown/Diff/theme picker/typed status 已覆盖 40/80/120 列；后续补 light、ANSI-256、ANSI-16、NoColor 的完整视觉快照矩阵；
- 已增加 Markdown、100 KiB code、100 KiB active repaint 和 100 KiB Diff parse/render benchmark；待 CI 基线稳定后再设置非抖动预算门禁；
- Diff 超预算渲染仍会逐行构建完整 Document；若真实 transcript 出现更大补丁，再评估 viewport/tail projection 或分页策略。

### P2：遗留 adapter 清理

- 已删除 `FixedBottomSurface.SetStatusLine`/`ParseLegacyStatusLine`、`PrintStatusColored`、`Layout.UpdateStatusWithColor`、`StatusItem.Color`、StatusBar color callback、Theme `*color.Color` 以及 Markdown 私有旧 renderer；这些入口不再属于兼容面。
- 已删除 `color.NoColor` 全局兼容信号与 `Terminal.PrintWithColor`；NoColor、强制颜色和色深测试只通过 profile 输入驱动，生产和测试均不再依赖 `fatih/color`。
- Theme `Format*`/`Colorize*`/`Dimmed` 等字符串返回方法仍保留公开调用契约，但内部均先创建 Document，再按实际 profile 编码；它们是新渲染边界的便捷 adapter，不是第二套着色实现。
- runtime event bridge 已直接传递 `TimelineEvent`/`Document`，不再通过字符串标签恢复语义；`theme_render.go` 的 `LegacyTimelineParser` 仅服务其他历史 supplement 调用和 edited-diff tool result 兼容入口，后续不再扩展字符串规则。
- 全仓 Go 代码对 `fatih/color`、`.XColor.Sprint/.Sprintf`、`color.New(...).Sprint`、旧 status/color callback 和 Markdown legacy helper 的审计均为零结果。

因此 P2 剩余范围已缩小为：迁移非 runtime bridge 的 unknown supplement 上游，以及让 edited-diff tool result 直接携带 `FileDiff`/`Document`。生产视觉内容和颜色能力决策已经统一使用 `Document -> Resolver -> ColorProfile backend`。

## 7. 验收判断

本轮可以判定为：**渲染架构核心可用，高优先级逻辑和安全缺陷已收口，聚焦测试通过**。

暂时不能判定为：**全部 Phase 已完成发布验收**。发布级完成仍取决于真实 PTY/ConPTY 矩阵、长流增量解析策略，以及 unknown timeline 字符串兼容边界的退出。
