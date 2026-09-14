# 消息渲染对齐 deepseek-harness 优化方案（页面样式 + 渲染样式与逻辑）

状态：**已实施（批次 A–F 全部落地；§10.2 门禁四件套全绿，实施记录见 §12；§13.3 取证缺口已补测收口；§12.1.2 终审轮修正后全量复跑 73/73 e2e 全绿）**——原「草案（待评审）」于 2026-09-14 实施完毕后终止。

日期：2026-09-14

负责范围：`frontend/`（会话页骨架、消息流渲染、Markdown 排版、工具/推理行、turn 尾部）

参考对象：

- 线上参考：`http://127.0.0.1:3080/`（deepseek-harness Web 客户端，Dark 主题，实测会话「重试路径限流错误检查」）
- 参考源码：`E:\projects\ai\deepseek-harness`（`packages/client/*` 22 个 UI 包；样式规范 `docs/web-styling.md`）
- 目标页面：`http://localhost:5193/workspace/sessions/session_20260914150629_KGcKeL3a`
- 对象代码：`frontend/src/components/workspace/**`、`frontend/src/lib/chat-view/**`、`frontend/src/styles/globals/**`

上游计划：`docs/plan/frontend-deepseek-harness-optimization-plan.md`（P1-1 投影模型 / P1-2 增量 Markdown / P1-3 滚动契约 / P1-6 工具行状态机 已完成；本方案是**渲染层视觉与交互对齐**的后续专项，不改写上述内核）。

---

## 1. 背景与结论摘要

现状：当前会话页把**每条消息渲染成一张独立卡片**——`rounded-[1rem]` + 双向渐变填充 + `border 1px` + `shadow-[0_16px_40px_rgba(0,0,0,0.12)]`，卡片内再放「头像 chip + 作者名 + label 徽标 + 竖直渐变细线 + `space-y-4` 段列表」。整个流是 `max-w-[52rem] gap-6` 的卡片堆叠。

参考站：消息是**扁平 flow item**（无卡片背景、无边框、无阴影、无头像行），在**自适应单列**（`clamp(680px, 列宽×64%, 920px)`）里以 16px 间距排列；过程信息（Think / 工具调用 / 上下文注入）压成 **24px 单行**「图标 + 标题 + 2px 分隔点 + 摘要」；正文是 Markdown（**字号走用户设置轴**，行高与字号成对派生），块间距 16px；只有**用户消息**有气泡（右对齐、单层底色、`padding 10px 16px`、`border-radius 22px`、`max-width: min(内容宽×0.702, 82%)`）。

> ⚠️ **本节为二审后的口径**（2026-09-14）。一审在默认设置 + dpr=1 下测得的 `748px` / `16/28` / `14/24` / 「无发丝」等**均为解析值**，源码事实与本节的 5 处纠正见 **§13 纠正台账**（C1 宽度轴、C2 回合级折叠、C3 发丝、C4 字号轴、C5 缺失 kind）。方案已按 §13 全面修订，实施时**不得**回退到一审写死的常量。

核心结论（按优先级）：

| 级别 | 结论 | 一句话依据 |
|---|---|---|
| P0 | 消息容器应从「卡片」改为「无容器扁平行」 | 参考站 5 类取样节点实测 `background=transparent`、`border=0`、`box-shadow=none`；特性组件禁止自造容器色 |
| P0 | 过程信息（Think/工具/上下文）应压成 24px 单行，展开才占高度 | 参考站工具行 `rect.h=24`、Think 行 `rect.h=24`（折叠态）。**注意折叠是两层**：回合级统计折叠行 + 行级 24px（§13 C2） |
| P0 | 消息列宽与列间距走**宽度轴**：内容列 = `clamp(680px, 列宽×64%, 920px)`（用户可拖拽替换）、列间距 16px，页面横向留白由滚动区 padding + 自适应外边距承担 | `.Md3f7G_scroll{padding:16px 32px}` + `.Md3f7G_column` 的 clamp 实现；一审记的 `748px / margin 0 170px` 是解析值（§13 C1） |
| P1 | Markdown 排版需按参考站标定（**字号轴 + 行高成对**、块间距 16px、行内 code 14/22 无边框、代码块 13/22） | 源码 `--dsh-content-font-size` + `--dsh-content-font-delta` 与全量 `calc()` 派生；实测 `p{font:16px/28px}` 是该用户 delta=+2 的解析值（§13 C4） |
| P1 | 打字机（`useTypewriter`）应移除或降级为「仅未收到首块时的占位」 | 参考站无逐字打字；且打字机与 P1-2 的前缀冻结/绝对 offset key 存在语义冲突（显示的 content 被截断会反复触发非追加判定） |
| P1 | 需要统一 turn 尾行（动作区 + 时间/耗时/token 统计），且时间戳默认 `opacity: 0`、hover 显现 | 参考站 `.p-xYUq_timeStart/timeEnd{opacity:0}`、`.p-xYUq_action{28×28}`、tail 高 28 |
| P2 | 用户气泡需右对齐、22px 圆角、`min(525px, 82%)` 限宽；编辑/回溯入口收进 hover 动作区 | 参考站 `.gdEzaW_bubble` 实测 |
| P2 | 中性分隔/边框**取 1px 等价**：参考站源码确有 17 处 `0.5px`，但 dpr=1 下解析并绘制为 1px，本仓 `0.5px/0.25px` 同样被向上取整为 1px；本方案不追 dpr≥2 的发丝观感（唯一例外：回合折叠行，§13 C3 / 批次 F3） | 源码标注 + 像素级实测（§10.4、§13.1） |

> 本方案同时回答用户的两个问题：**页面样式**（§4）与**消息渲染的样式与逻辑**（§5、§6），并给出本地落地口径（§8）、批次排期（§9，含二审新增的批次 F）、验收门禁（§10）、**二审纠正台账与新增范围**（§13）。

---

## 2. 目标与非目标

### 2.1 目标

1. **页面骨架对齐**：会话滚动区 → 单列内容列（**宽度轴** `--app-chat-content-width` = `clamp(680px, 列宽×64%, 920px)` / `gap 16px`）→ 扁平 flow item 三层结构，横向留白随视口自适应；去掉消息级卡片容器。
2. **消息渲染样式对齐**：用户气泡、上下文行、Think 行、工具行、turn 尾行五类节点各有稳定规格（字号/行高/颜色/间距/圆角/交互态），全部落到语义 token，不写字面色值；**并按 §13.2 补齐 `turn-process`（回合统计折叠行）、`system-prompt`、`steering`、`notice`（错误/截断/重试单行）与 `fallback` 的规格**。
3. **消息渲染逻辑对齐**：引入**扁平 flow 节点导出**（`ChatMessage → FlowItem[]` 纯函数投影），把「一条消息 = 一张卡」改为「一条消息 = 若干 flow item」，使折叠、悬停动作、时间统计、锚点跳转都有统一挂载点。
4. **过程信息降噪（两层折叠，§13 C2）**：回合级统计折叠行控制「过程行是否渲染」，展开后过程证据才以单行 + 摘要形式出现，再点行展开详情；流式期不折叠。保持既有 P1-1 谓词语义不变（只换表现层与统计形制）。
5. **可回归**：既有测试（`message-list.test.tsx` / `message-markdown.test.tsx` / `message-tool-row.test.tsx` / `collapse-rendering.test.tsx`）语义不变；新增语义锚点（`data-chat-flow-kind` 等）供 e2e 断言。

### 2.2 非目标

- 不引入 CSS Modules 迁移（本仓是 Tailwind v4 + `@theme` 语义 token；参考站禁用 Tailwind 是其自身约束，见 §8.1）。
- 不改写 `lib/trajectory/**` 事件归约内核、不改写 SSE 协议、不改 `lib/chat-view` 的折叠谓词语义（只在渲染层扩 `kind`）。
- 不引入虚拟滚动、不做消息树/分支可视化、不做 Markdown 定制插件（沿用 react-markdown + remark）。
- 不照搬参考站 cordis 插件与插槽体系、不照搬 `corner-shape: superellipse`（实测不可用，§10.4）。
- 不做亮色主题改造（token 已双主题；本方案只保证两主题都可读，不重新标定亮色）。
- 不新增「用户可调字号/宽度」的设置入口：本方案只建立**字号轴与宽度轴**（`calc(基准 + delta)` / `clamp`），把设置面板留给后续独立需求（§13 C1/C4）。
- 不实现本地协议无对应事件的参考站 kind（`inbox` / `request-prompt` / `command` / `compaction`）与页面区域（`ContextMeter` / `TodoPanel`）；只保留 `fallback` 可读降级原则（§13.2、§13.3）。
- 不追 dpr≥2 的 0.5px 发丝观感：全局维持 1px 等价（唯一例外是回合折叠行，§13 C3）。

---

## 3. 取证方法与证据口径

本方案的所有尺寸/颜色/结构断言都来自**真实浏览器计算样式**，不是源码推断。口径如下：

| 材料 | 方法 | 证据位置 | 可信度 |
|---|---|---|---|
| 参考站样式 | Playwright（本机已安装 Chromium）打开 `http://127.0.0.1:3080/`，点击侧栏进入会话「重试路径限流错误检查」，对 `[data-slot="conversation.view"]` 子树按 `user / context / assistant-step / tool-call / turn-tail / flow-column` 六类取样，读 `getComputedStyle` 关键属性 + `getBoundingClientRect` | `frontend/.tmp/style-report-ref.md`（317 个 CSS 变量 + 每类节点逐层计算样式） | 高（实测） |
| 参考站令牌 | 同一页面 `document.documentElement` 上的 `--dsw-*` 变量全集（317 条） | `frontend/.tmp/style-report-ref.md` §令牌段 | 高（实测） |
| 本地页面样式 | 同一脚本对 `http://localhost:5193/workspace/sessions/session_20260914150629_KGcKeL3a` 取样（419 个变量，`thread / article-0 / article-1 / thread-host` 四类锚点） | `frontend/.tmp/style-report-local.md` | 高（实测） |
| 本地渲染逻辑 | 源码直读（`message-list.tsx`、`message-list/*`、`lib/chat-view/*`、`hooks/workspace/use-typewriter.ts`） | 见 §7 行号 | 高（源码） |
| 参考站样式纪律 | `E:\projects\ai\deepseek-harness\docs\web-styling.md` | 引文标注 `[web-styling]` | 中（规范文本，非实测） |
| 规范文本的可实现性 | Playwright 注入对照元素 + **截图像素分析**（页面内 canvas `getImageData`，dpr=1/2 双跑）：`border-width` 取 1/0.5/0.25px、`box-shadow` ring 取 0.5/1px，量测真实绘制厚度；`corner-shape` 取 `superellipse`/`bevel` 对照 | `frontend/.tmp/hairline-render-output.json`、`hairline-probe-output.json` | 高（实测，像素级） |

取证脚本：`frontend/.tmp/style-probe.mjs`、`style-probe2.mjs`、`hairline-probe.mjs`、`hairline-render-probe.mjs`、`pseudo-probe.mjs`（临时件，不入库；如需要可作为 e2e 断言的雏形）。

> 口径声明：凡标 `[web-styling]` 的条目来自参考站规范文档，**默认按「规范文本、非实测」对待**，须以本仓实测为准。
> - 已实测收口（2026-09-14，§10.4）：0.5px 发丝（**口径已按 §13 C3 修正：源码有 0.5px 声明，dpr=1 下与 1px 等价**）、`corner-shape: superellipse`（不可用）、以及「抬升面用阴影」的**前半句**（消息 flow item 确无阴影）。
> - 仍属未取证的剩余条目：§6.4 的「link 无下划线 + hover 虚线下划线」规则、以及「抬升面**不用边框**」的后半句（实测中带阴影的卡片同时仍有 1px 边框，与规范表述不完全一致）。
> - 二审新增证据源：`E:\projects\ai\deepseek-harness\packages\client\**` 源码直读（不再只是计算样式），见 §13.4 与附录 A 增补行。

---

## 4. 页面样式基线（参考站实测）

### 4.1 三层结构与尺寸

```
[data-slot="conversation.session"]
└─ .wSkVaW_viewArea                            ← 会话区（滚动宿主 .wSkVaW_scrollBody 宽 1160）
   └─ [data-slot="conversation.view"]
      └─ .Md3f7G_root            display:flex  width:1152
         └─ .Md3f7G_scroll       display:block padding:16px 32px  width:1088
            └─ .Md3f7G_column     display:flex(column)  width:748  max-width:748
                                  margin:0 170px  gap:16px   [data-chat-flow]
               ├─ .Md3f7G_flowItem  width:748   ← 每个消息节点一行
               └─ ...
```

关键点：

- **列宽是自适应量，不是定值**（2026-09-14 二审修正，源码 `ui-conversation/.../ConversationRoot.module.css:28-34`）：
  `--dsh-chat-content-width: var(--dsh-chat-user-width, clamp(680px, calc(var(--dsh-conversation-column-width) * 0.64), 920px))`
  ——下限 680px、上限 920px、按会话列宽的 64% 自适应，且**用户拖拽偏好会整体替换该值**。实测到的 `748px` + `margin:0 170px` 是「该视口 + 该用户设置」下的**解析结果**，不是可照抄常量（748 是 figma 值，源码注释明确说明实现刻意比 figma 低一档）。
- **一条共享宽度轴决定三处宽度**：转录列 = `W`；停靠卡（todo / goal / queue）= `W − 32px`（卡内 4×8px 内缩）；**输入卡 = `W + 32px`**（`--dsh-composer-card-max-width`）。滚动宿主左右 padding = `calc(var(--dsh-composer-side-clearance) + 16px)`，所以「转录列恰好比输入卡窄 32px」在任意视口宽度下都成立（`ChatView.module.css:13-15`）。
- **列间距不是 flex `gap`，而是相邻兄弟上的 `margin-top`**：`.column > :not([hidden]):not(.flowItem:empty) ~ :not([hidden]):not(.flowItem:empty) { margin-top: var(--dsh-chat-flow-gap, 16px) }`；隐藏行（`hidden="until-found"`）与被渲染器主动放弃的空行**都不占间距**；折叠态下**回答行**把该变量收成 `8px`，让「摘要行 + 回答」贴紧（`ChatView.module.css:49-64`）。
- **滚动宿主 padding 上下 16px、左右 `clearance + 16px`**：实测 `16px 32px` 同样是解析值（该环境 clearance = 16px）。
- 滚动条样式走语义变量：`--dsw-alias-scrollbar-bg-l1: #3c3c3d`、hover `#545557`（l2: `#545557` / hover `#65676b`），滚动条是页面唯一允许的「重色」细件。

### 4.2 色彩令牌（实测摘录，Dark）

| 语义 | 变量 | 值 | 用途 |
|---|---|---|---|
| 页面底色 | `--dsw-alias-bg-base` | `#151517` | 会话背景 |
| 层 1 / 2 / 3 | `--dsw-alias-bg-layer-1/2/3` | `#232324` / `#2c2c2e` / `#353638` | 抬升面、气泡、菜单 |
| 主文本 | `--dsw-alias-label-primary*` | `#f9fafb` | 正文 |
| 次文本 | `--dsw-alias-label-secondary` | `#cfd3d6` | 行标题（Think / Grep / 上下文注入） |
| 三级文本 | `--dsw-alias-label-tertiary` | `#adb2b8` | 行摘要、时间戳、动作图标 |
| 说明文本 | `--dsw-alias-label-caption` | `#81858c` | label/badge |
| 描边 | `--dsw-alias-border-l1/l2/l3` | `#ffffff0f` / `#ffffff1f` / `#ffffff29` | 分隔线、控件边框 |
| 用户气泡 | `--dsw-specific-bubble` | `#2c2c2e` | 用户消息底 |
| 行内 code | `--dsw-alias-markdown-inline-code` | `#2c2c2e` | inline code 底 |
| 代码块 | `--dsw-alias-markdown-code-block` | `#1b1b1c` | 代码块底 |
| 代码块标题条 | `--dsw-alias-markdown-code-block-banner` | `#2c2c2e` | 语言/复制条 |
| 状态 | `--dsw-alias-state-{success,warn,error,business}-primary` | `#22c55e` / `#f59e0b` / `#f25a5a` / `#679efe` | 成功/警告/错误/业务 |
| 强调渐变 | `--dsw-linear-gradient-think` | `linear-gradient(180deg,#151517 20.19%,#15151700 100%)` | 推理区顶部渐隐 |
| 阴影 | `--dsw-shadow-lv1/lv2/lv3` | `0 2px 4px #0000000d` / `0 4px 12px #00000005,0 2px 8px #0000000a` / `0 0 1px #0003,0 0 4px #00000005,0 12px 32px #00000014` | 浮层 |

### 4.3 字体阶梯（实测摘录）

| 用途 | 变量 | 值 |
|---|---|---|
| 正文 | `--dsw-font-markdown-base-*` | 字号轴解析值 `16px/28px`（默认 14/24，`§13 C4`），`weight 400`；strong `600` |
| 小字 | `--dsw-font-markdown-small-*` | `14px/24px`；small-strong `600` |
| H1 / H2 / H3 / H4 | `--dsw-font-markdown-h1..h4-*` | `24/34 w700`、`22/32 w700`、`20/30 w700`、`16/28 w600` |
| 行内 code | `--dsw-font-markdown-code` | `14px/22px`（"SF Mono","JetBrains Mono","Fira Code",Consolas,…） |
| 代码块 | `--dsw-font-markdown-code-block` | `13px/22px`；small 变体 `12px/18px` |
| 表格 | `--dsw-font-markdown-table-*` | `15px/25px`，表头 `weight 500` |
| UI 阶梯 | `--dsw-font-{xxxs-11,xxs-12,xs-13,s-14,base-16,l-20,xl-24}` | 11/12/13/14/16/20/24（行高成对定义） |

> 规矩（`[web-styling]`）：**字号必须与行高成对使用**，禁止只改 `font-size` 不管 `line-height`；这也是本方案 §8.2 要落到本地 `app-text-*` 工具类的约束。

---

## 5. 消息渲染样式规格（参考站实测）

参考站的消息流是**扁平 flow item 列表**，每个 item 带 `data-chat-flow-key` / `data-chat-anchor-key` / `data-chat-flow-kind`，`kind ∈ {user, context, assistant-step, tool-call, turn-tail}`。以下按 kind 给出实测规格。

### 5.1 flow item 通用形制

| 属性 | 实测值 |
|---|---|
| 宽度 | 占满内容列（列宽 = `--app-chat-content-width` 的解析值；实测 748px，§13 C1） |
| 背景 / 边框 / 阴影 | 无（透明、`0px none`、无阴影） |
| 竖直间距 | 由父列 `gap: 16px` 提供，节点自身 `margin: 0` |
| 行高基准 | 过程行 `24px`，正文行 `28px`，tail 行 `28px` |

### 5.2 user（用户消息）

```
.gdEzaW_userRow     display:flex  width:748  gap:6px        ← 行（右对齐由内部 stack 推齐）
└─ .gdEzaW_userStack display:flex(column)  width:525  maxWidth:min(525px, 82%)  gap:8px
   ├─ .gdEzaW_bubble display:block  bg:#2c2c2e  padding:10px 16px  radius:22px
   │                 font:16px/24px  maxWidth:100%
   └─ .p-xYUq_actions display:flex  gap:10px  margin-left:-6px
      ├─ span.p-xYUq_timeStart  14px/24  color:#adb2b8  padding-right:12px  opacity:0（hover 显现）
      └─ button.p-xYUq_action   28×28  padding:6px  radius:28px  color:#adb2b8
```

要点：

- 气泡**只有一层底色** `#2c2c2e`（`--dsw-specific-bubble`），无描边、无阴影、无渐变；圆角 22px（比卡片感的 16px 更「气泡」）。
- 行容器是**纵向** flex（`flex-direction: column; align-items: flex-end; gap: 6px`，`MessageItem.module.css:4-9`）：气泡在上、动作区在下，两者都被右对齐推齐——不是「气泡与动作同一行」。
- 限宽是**比例而非定值**：`max-width: min(calc(var(--dsh-chat-content-width, 748px) * 0.702), 82%)`（525/748 ≈ 0.702，`MessageItem.module.css:21`）。列宽被拖宽时气泡同比变宽；实测的 525px 只是默认列的解析值。气泡自身 `max-width: 100%`。
- 气泡排版走**内容字号轴**：`font-size: var(--dsh-content-font-size, 14px)`、`line-height: calc(22px + var(--dsh-content-font-delta))`，默认 **14/22**（单行气泡高 42px = 22 + 10×2）。§5.2 结构图里记的 `16px/24px` 是该用户把内容字号调到 16px 后的解析值（delta = +2），见 §13 纠正 C4。
- **元数据不上屏**：没有「头像 + 作者名 + label」行——作者身份靠位置（右侧）与气泡色表达。
- 动作区（复制/编辑/回溯）与时间戳同排：`gap: 8px`、行高 `28px`、按钮 28×28 / `border-radius: 28px` / 图标 15px / 默认色三级文本；hover 底色 `--dsw-alias-interactive-bg-hover`、图标升到二级文本（`MessageIconActions.module.css`）。
- **hover 显现的准确规则**：只在 `@media (hover: hover)` 下折叠为 `opacity: 0`，**且用 `:has(~ [data-chat-flow-kind='user'])` 排除最后一个 user/steering 行**——即「历史用户消息 hover 才显动作，最新一条常显」；无 hover 能力的设备整行常显（`MessageIconActions.module.css:36-55`）。

### 5.3 context（上下文注入 / system prompt）

```
._root_9cl6j_3   display:flex  width:748          ← 折叠行（高 24px）
└─ ._row_9cl6j_10 display:flex  width:748
   ├─ span._leading_9cl6j_23  width:16  margin-right:6px  color:#adb2b8   ← 前导位（对齐图标列）
   ├─ span._iconIdle_9cl6j_42 width:14  color:#adb2b8                     ← 图标（14px）
   ├─ span._title_9cl6j_64    14px/24  color:#cfd3d6                      ← 「上下文注入」/「系统提示词」
   ├─ span.pC0e7a_sep         2px 宽、radius 1px、bg #81858c、margin:0 8px ← 分隔点
   └─ span.pC0e7a_source      14px/24  color:#adb2b8                      ← AGENTS.md / @pkg/system-prompt
```

要点：**整行 24px 高、无背景无边框**；多段信息用「标题 + 2px 分隔点 + 摘要」串联，摘要可承载文件名、来源包名、badge。

补充规格（源码 `ContextInjectionRow.module.css`，`SystemPromptRow` 复用同一张样式表）：

- 摘要/来源用**次级字号轴**：`font-size: 13px`（`--dsh-content-font-size-secondary`）、`line-height: calc(24px + delta)`，色阶 `label-tertiary`；分隔点 `2×2px / radius 1px / margin 0 8px / bg label-caption`。
- 展开态**不是 Markdown，而是代码面板**：`padding: 10px 16px 12px 12px`、`border-radius: 8px`、`background: --dsw-alias-markdown-code-block`、`max-height: 141px` + `overflow: auto`、`margin: 4px 0 0 calc(22px + delta)`（缩进对齐标题起点）、字体 `400 11px/16px` 等宽；展开后整行 `padding-bottom: 4px`。
- 会话区实测的 `_root` 折叠高 24px、展开后读到的 `max-height: 141px` 上限即来自此处——**「上下文注入」在参考站是「一行 + 可选代码面板」，不是 Markdown 正文**。

### 5.4 assistant-step（Think / 正文）

```
.Sxvs8a_root  display:flex
└─ .Sxvs8a_body  display:flex  gap:16px
   ├─ .QWLzlG_root（折叠头，折叠态 24px 高）
   │  └─ 同上「图标 + 标题(14/24 #cfd3d6) + 分隔点 + summary(14/24 #adb2b8)」
   └─ ._markdown_1r4m5_5   display:block  font:16px/28px
```

折叠态实测样例：`Think | The user is asking me to check whether the retry mechanism a…`（单行摘要截断）；展开后正文为 Markdown：

| 元素 | 实测 |
|---|---|
| `p` | 字号/行高走内容字号轴（默认 `14/24`，实测该用户 `16/28`）；**块间距由容器 flex `gap: 16px` 承担**，段落 margin 归零（§13 C4；落地见批次 D1/F1） |
| `strong` | `weight 600`，同字号行高 |
| `code`（inline） | `14px/22px`，`background:#2c2c2e`，`padding:0 5px`，`border-radius:6px`，**无边框** |
| 列表 / 引用 / 表格 | 见 §4.3 字体阶梯；表格 `15/25`，表头 `500` |
| 超宽表格 | `.md-table-wide`（≥4 列）会**突破内容列**铺到转录区宽度：用容器查询 `100cqw` 与 `--dsh-chat-content-width` 算 `--dsh-table-spare/--dsh-table-lead`，负 margin 外扩、正 padding 抵消，使表格内容仍在原 x 起排（`AssistantMarkdown.module.css:33-43`） |
| 中断标记 | `.stopped`：11px/18px、`padding 0 6px`、`radius 6px`、底 `--dsw-alias-interactive-bg-hover`、色 `label-tertiary`，左对齐、**无动画** |
| 动作区偏移 | `.actions { margin-top: 16px; margin-left: -6px }`（光学对齐 28px 命中区，与 tail 同一规则） |

要点：正文区**没有卡片包裹**，直接铺在内容列上；Markdown 的视觉边界只靠块间距与 code 底色。

> **字号轴（2026-09-14 二审新增，源码 `ui-theme/src/styles/gradient-shadow-text.css:55` + `AssistantMarkdown.module.css`）**：参考站正文尺寸不是常量，而是**用户设置项**。
> - `--dsh-content-font-size`（默认 **14px**）与 `--dsh-content-font-delta: calc(var(--dsh-content-font-size, 14px) - 14px)`；次级为 `--dsh-content-font-size-secondary`（默认 13px，配 `--dsh-content-font-delta-secondary`）。
> - 行高、图标尺寸、行高盒、气泡行高**全部**写成 `calc(<基准>px + delta)`，所以「字号与行高成对变化」是机制保证的，不是纪律要求。
> - 默认值：正文 `14px/24px`（`.root`），块间距由 `.body { display:flex; flex-direction:column; gap:16px }` 承担（**是 flex gap，不是 `p` 的 margin**）；行标题/摘要走次级轴 → `13/20`（`ReasoningRow .summary`）或 `13/24`（`ContextInjectionRow .source`）。
> - 本方案 §5.4 表格与 §5.2 里记录的 `16/28`、`16/24` 是**该用户把内容字号设为 16px**时的解析值；实现时**不得硬写 16/28**（见 §13 纠正 C4、批次 F1）。
> - 另有「已完成回合转录样式」设置 `TranscriptViewMode`：`normal | compact`，**默认 `compact`**（`ui-chat/src/chat-settings.ts:18`），它直接决定 §6.2 的回合级折叠是否生效——详见 §13 纠正 C2。

### 5.5 tool-call（工具调用）

```
.ztWv_q_callRow  display:block  radius:6px  width:748   ← 整行可点（高 24px，折叠态）
└─ .o3BgMG_root  display:flex
   └─ 同上「图标 + 标题(工具名，14/24 #cfd3d6) + 分隔点 + summary(14/24 #adb2b8)」
```

实测样例：`Grep | resource-manager/retry`、`Grep | rate_limits|limit_reached|plan_type`。

要点：

- 工具调用**默认就是一行 24px**——工具名 + 参数/目标摘要，不展开、不占高度、不画边框。
- 展开（点击行）才出现结果面板；面板属于「用户主动索要」的内容，不参与默认滚动的视觉噪声。
- 行 `border-radius: 6px` 说明有 hover 底色（`--dsw-alias-interactive-bg-hover` 量级），但静止无背景。

### 5.6 turn-tail（回合尾部）

```
.osXY9a_root  display:flex  gap:16px  height:28px
├─ .p-xYUq_actions  display:flex  gap:10px  margin-left:-6px   width:754
│  └─ button × N    28×28  padding:6px  radius:28px  color:#adb2b8   ← 复制/重试/赞/踩
└─ span.p-xYUq_timeEnd  14px/24  color:#adb2b8  padding-left:12px  opacity:0（hover 显现）
   └─ 文本形态：「8月25日 17:27 · 用时 5分34秒 · 首 token 2秒 · 61 tok/s」（分隔用 span.p-xYUq_runTimeDot「·」margin:0 10px）
```

要点：

- 一个回合的**统计与动作统一挂在尾部一行**，不在消息内部散落 token 用量行（本地现状是消息卡内 `turnUsage.summary` 一行）。
- 时间/耗时/tok-s 默认不可见（`opacity: 0`），hover 时淡入——**元数据不抢阅读焦点**，但需要时可查。
- 动作按钮成组（`gap:10px`），左对齐并 `margin-left:-6px` 让图标光学对齐正文左缘。

---

## 6. 消息渲染逻辑规格（参考站语义）

### 6.1 扁平 flow 模型（最关键的一条）

参考站的渲染模型不是「消息 = 组件」，而是：

```
一次会话 → flow items[]（有序、可 diff、可锚点定位）
每个 item = { flowKey, anchorKey, kind, payload }
```

- **kind 决定渲染器**：参考站注册 13 类（§13.2），一审只覆盖 5 类（`user` / `context` / `assistant-step` / `tool-call` / `turn-tail`）；本地落地扩展为 10 类（另加 `system-prompt` / `steering` / `turn-process` / `notice` / `fallback`，见 §8.4 类型定义）。渲染层用 `kind → renderer` 映射，而不是在一条消息组件里用 if/else 拼所有形态。
- **一个 assistant 回合会展开成多个 item**：Think 行、工具行、正文行是**同级兄弟**，不是「卡内子行」。这让间距、hover、锚点、折叠都退化成列表级操作。
- **锚点独立于内容**：`data-chat-anchor-key` 提供跳转/定位目标（用于回溯、搜索命中、引用跳转），内容变化不改变锚点。
- **每行自带语义摘要**：折叠态把「标题 + 摘要」直接写在行内（`Grep | resource-manager/retry`），因此**过程行一旦渲染即可读**，无需再展开；但「过程行是否渲染」由 §6.2 的回合级折叠决定。

### 6.2 折叠与展开

> **2026-09-14 二审修正**：本节初稿把参考站写成「每行都可见、只是压扁」，**与源码不符**。参考站的折叠是**两层结构**：回合级折叠开关 + 行级单行压缩。

| 层级 | 参考站行为 | 证据 |
|---|---|---|
| **回合级（turn-process）** | 一个**统计式折叠控件行**：文案形如「N 个工具调用 · M 条消息 · K 个子代理」（全为 0 时显示「思考了一会儿」），右侧 16px chevron（`-90° → 0°`）。**收起时，该回合的过程行整体不渲染**（`processHidden = foldable && processMember && !open`）；展开时才出现逐行过程 | `TurnProcessNodeView.tsx`、`ChatNodeSeat.tsx:98`、`TurnProcessNodeView.module.css` |
| **行级** | 展开后每一行仍是 **24px 单行**：图标 + 名称 + 2px 分隔点 + 单行摘要（`Grep \| resource-manager/retry`），点击行展开该行详情 | §5.3–5.5 |
| **不受折叠影响的 kind** | `system-prompt` / `user` / `steering` / `turn-process` / `turn-error` / `turn-max-tokens` / `turn-tail` —— 这些行**不参与过程折叠**，始终可见 | `contract/turn-process.ts:20-33` `TURN_PROCESS_INDEPENDENT_KINDS` |
| **折叠生效条件** | 仅当 `compact` 转录模式 + 回合已关闭 + 历史完整 + 存在 `answerAnchorSeq` 时才折叠（`processWindowReady`）；`normal` 模式整段过程内联不折叠 | `ChatNodeSeat.tsx:62-81`、`chat-settings.ts:18`（默认 `compact`） |
| **折叠后的节奏** | 摘要行与最终回答之间收成 **8px**（`--dsh-chat-flow-gap: 8px`），展开时恢复 16px | `ChatView.module.css:62-64` |
| **正文** | 最终回答始终可见，不受折叠影响；折叠时其内联推理（`inlineReasoning`）也被收起，展开后恢复 | `AssistantNodeView.tsx:23-27` |
| **流式中** | 不折叠（回合未关闭），过程实时以行形式流出；回合结束后收敛为「摘要行 + 回答」 | `ChatNodeSeat.tsx:62-68` |
| **摘要摘取** | 行级摘要取该行最关键的单个参数/目标（路径、正则、命令、URL），**单行截断**而非多行摘要；回合级摘要取计数 | §5.5、`TurnProcessChatData` |

> 对照本地的差异（修正版）：本地 `ProcessCollapseRow` 的**统计式摘要方向与参考站一致**（本地 `collapse.ts` 已产出 `tools/replies/subagents` 计数，恰好对应参考站的 `toolCallCount / messageCount / subagentCount`），**不需要改成「行内摘要」**；真正的差距是：①参考站的折叠控件是一行 33px 的「统计 + chevron」并带 0.5px 底分隔线，本地是 `rounded-field` 边框按钮；②参考站折叠**只作用于过程行**，本地是「卡内藏起 `hiddenNodes`」；③参考站有「回合内出现新的用户/steering 消息时该回合不折叠」的例外（`compactAnswer`），本地无此语义。详见 §13 纠正 C2、批次 F3。

### 6.3 流式渲染

参考站实测的流式要点：

1. **直出，不重打**：新字符到达即追加渲染，没有逐字打字机。
2. **不闪回**：未见「内容回退/重排」；块级增量由框架的稳定 key 保证。
3. **进行态指示轻量**：正在执行的行用图标状态（spinner/脉冲）表达，不使用整块骨架屏。
4. 结构上允许「正文先出、工具行随后插入」——因为 item 列表可插入，而不是重排一张卡内部。

> 补充（§13 C2）：流式期间**回合级折叠恒为展开**（回合未关闭 + `streamingMessageId` 命中），过程行实时可见；回合关闭后按 `compact` 模式收敛为「统计行 + 回答」，折叠态回答行间距收 8px。

### 6.4 交互挂载点

| 交互 | 挂载位置 | 默认态 |
|---|---|---|
| 复制/重试/反馈 | turn-tail 动作区 | 图标常显（28×28） |
| 时间 / 耗时 / 首 token / tok-s | turn-tail 右侧文本区 | `opacity: 0`，hover 显现 |
| 用户消息编辑/回溯 | user 行动作区 | `opacity: 0`，hover 显现 |
| 工具详情 | 该工具行点击 | 折叠 |
| 回合过程显隐 | `turn-process` 统计行（整行可点） | 收起（`compact` 模式下回合关闭即收起） |
| 正文链接 | 正文内 | 无下划线，hover 虚线下划线（`[web-styling]`）；本地按现有 link 风格保持 |

### 6.5 滚动与阅读线

参考站滚动宿主仅做常规滚动（未见锚定 hack）；内容列走宽度轴 + `gap 16px` + 两层折叠共同降低了滚动噪声。本地已有更强的 `useConversationScroll`（贴底跟随 / 阅读保顶 / 语义锚点，P1-3 已落地），本方案**保留本地实现**，只调整列宽/间距与其阅读线采样假设（见 §9 批次 A4 的联动校验；折叠收起会**大幅改变内容高度**，A4 需重点复核保顶锚点）。

---

## 7. 本地现状与差异

### 7.1 本地实测形状（`localhost:5193` 会话页）

实测锚点（`frontend/.tmp/style-report-local.md`）：

| 锚点 | 实测 |
|---|---|
| 消息列（`thread`） | `display:flex; width:800px; max-width:800px; margin:0 28px; gap:16px` |
| 消息条目（`article`） | `display:flex; width:800px` |
| 消息卡 | `padding:14px 16px; border-radius:16px; border:1px solid oklab(0.811 -0.067 -0.005 / 0.14)`（teal 微染 1px 边框） |
| 卡片外壳 | `overflow:hidden` + `shadow`（源码 `shadow-[0_16px_40px_rgba(0,0,0,0.12)]`）+ 双向渐变背景 |
| 头像 chip | `28×28; border-radius:11.2px; bg:accent-secondary-soft; border:1px solid` |
| 标题 / 元数据 | `app-text-13`(13/19.5, 600) + `app-text-10`(10/15, 66%) |
| 系统提示词行 | `app-text-11`(11/16.5) + 「展开系统提示词」提示 |
| Markdown 正文 | `p.my-3`：`15px/25.8px`（≈1.72） |
| 行内 code | `14.25px/21.375px; bg:rgba(15,23,42,0.043); padding:2px 6px; radius:6px; border:1px solid rgba(15,23,42,0.1)` |
| 独有变量 | `--accent-primary:#f0c77b`、`--accent-teal:#8fd0c6`、`--accent-secondary-soft:rgba(143,208,198,0.1)` 等 |

### 7.2 源码结构现状

| 文件 | 现状 | 与参考站的差距 |
|---|---|---|
| `components/workspace/message-list.tsx` | 滚动宿主 + `mx-auto max-w-[52rem] gap-6` 列 + `messages.map` → `article` → 三种卡片二选一 | 列宽是**定宽 52rem（832px）**，参考站是宽度轴解析值（默认视口 748px）；无 flow kind；无 turn-tail |
| `message-list/assistant-message-card.tsx` | 卡片 + 头像 + 作者 + label + 竖直渐变线 + `space-y-4` + `ProcessCollapseRow` + usage 行 + 关联产物 | 卡片化、元数据上屏；折叠是「卡内藏起过程」，缺参考站的**回合级统计折叠行**形制与「新 steering 则不折叠」例外 |
| `message-list/user-message-bubble.tsx` | 卡片（金色渐变 + 1px 边框 + 阴影）、头像 + 作者 + label + 右侧 Badge + 内联编辑 textarea | 无气泡底色、无 22px 圆角、无 `min(525px,82%)` 限宽、动作常显 |
| `message-list/history-context-message-card.tsx` | 与 assistant 卡同形制，折叠头 + 展开面板 | 参考站是 24px 单行（`上下文注入 \| AGENTS.md`） |
| `message-reasoning-row.tsx` | 带边框 section：头按钮（Brain 图标 + `app-text-10` 标题 + 摘要 + Chevron）+ `pre` 全文（`app-text-12`，`max-h-72`） | 参考站是 24px 单行 + 展开后 Markdown；行高/字号/色阶不同 |
| `message-tool-row.tsx` + `tool-row/*` | 带边框 section：图标 + 标题 + 摘要 + 展开面板（`ToolRowPanels`） | 参考站无边框、24px、`title 14/24 #cfd3d6` + `summary 14/24 #adb2b8` |
| `message-list/process-collapse-row.tsx` | 统计式摘要按钮（`app-text-11`，`rounded-field` 边框） | **统计语义正确、形制不符**：参考站对应 `turn-process`（33px 行 + 16px chevron + 0.5px 底边 + 收起即隐藏过程行）；本地是边框按钮、无 chevron、无 `turn-process` 锚点（§13 C2） |
| `segment-components.tsx` `StreamingMarkdown` | 经 `useTypewriter` 逐字显示 | 参考站无打字机（§6.3） |
| `hooks/workspace/use-typewriter.ts` | 30ms tick、55/180 cps、代理对保护 | 与 P1-2 前缀冻结/绝对 offset key 语义冲突（截断内容反复触发「非追加」判定 → `generation` 递增 → 块 remount） |

### 7.3 差异矩阵（按处理优先级）

| # | 维度 | 参考站 | 本地 | 处置 | 级别 |
|---|---|---|---|---|---|
| D1 | 消息容器 | 无容器扁平行 | 渐变卡 + 1px 边框 + 阴影 | 去容器，改扁平行 | P0 |
| D2 | 过程信息高度 | 24px 单行 | 折叠头 32px+ / 展开面板 | 压成单行，摘要内联 | P0 |
| D3 | 列宽/间距 | **宽度轴**（`clamp(680px, 列宽×64%, 920px)`，可被用户偏好替换）/ gap 16 | 定宽 832px / gap 24 | 引入宽度轴 + 三处派生（列 / 停靠卡 W−32 / 输入卡 **W+32**），收敛间距到 16（§13 C1、批次 F2） | P0 |
| D4 | 元数据上屏 | 不上屏（hover 才显时间） | 头像+作者+label 常显 | 移入 hover 动作区 | P0 |
| D5 | 用户气泡 | `#2c2c2e` / 22px / min(525,82%) | 金色渐变卡 / 16px / 42rem | 改气泡规格 | P0 |
| D6 | 正文排版 | **字号轴**（默认 14/24，行高 `calc(基准+delta)`），块间距 = 容器 flex gap 16px | 15/25.8 定值，段落 `my-3`（12px margin） | 建立字号轴 + 段落 margin 归零改容器 gap（§13 C4、批次 F1/D1） | P1 |
| D7 | 行内 code | 14/22 无边框、`0 5px` | 14.25/21.375 带边框、`2px 6px` | 去边框、改padding/字号 | P1 |
| D8 | 打字机 | 无 | 有（30ms tick） | 移除/降级 | P1 |
| D9 | turn 尾统计 | tail 行（耗时/首 token/tok-s，hover 显现） | 卡内 usage 文本行（常显） | 移入 tail 行 | P1 |
| D10 | 回合折叠 | **回合级统计控件行**（`turn-process`：N 工具调用 · M 消息 · K 子代理 + chevron，收起即隐藏过程行）+ **行级单行摘要** | 统计式折叠按钮（`ProcessCollapseRow`）+ 行级无摘要（工具行只有标题） | **保留统计语义**，把按钮改造成 33px 统计行；同时给行级补摘要（两层都做，不是二选一）——见 §13 C2 | P1 |
| D14 | 回合级折叠控件规格 | 33px（24 文本 + 8 下内距）＋ `border-bottom: 0.5px` ＋收起时 `margin-bottom: 8px`；标签 `13~14/24` 二级色、chevron 16px 三级色 | 无此控件 | 新增 `turn-process` 等价控件（批次 F3） | P1 |
| D15 | 未覆盖 kind：`system-prompt` | 独立 kind（`data-chat-flow-kind="system-prompt"`），与 `context` 分行渲染，展开体同代码面板 | 与 context 混用同一张卡 | 拆出独立 kind 与独立渲染分支 | P1 |
| D16 | 未覆盖 kind：`steering` | 独立 kind，**与 `user` 同形制**（气泡 + 动作区共用 `:is(user, steering)` 规则） | 无对应概念/渲染 | 投影层补 `steering`，样式复用 user 气泡 | P1 |
| D17 | 未覆盖 kind：`turn-error` / `turn-max-tokens` / `retry` / `compaction` | 四类都是**单行轻量行**（非卡片）：错误行 13/20 + 红点 + 标题加粗；max-tokens 标题用 warn 色；retry 行 13/20 + 折叠三角 + 进行中 shimmer；compaction 行 24px + hover 才出现展开图标 + 代码面板体 | 部分有（中断/错误提示），无统一行规格 | 按 §13 C5 统一为「单行 + 可选面板」 | P1 |
| D18 | 未覆盖 kind：`turn-tail` 之外的动作面（`inbox` / `request-prompt` / `command` / `fallback`） | 各自有独立渲染器与 i18n 文案（inbox 通知、请求提示、通用命令卡、未知类型兜底 `JsonBlock`） | 无对应概念 | **明确列为非目标**（本地协议无这些事件），仅保留 `fallback` 思路：未知 kind 必须可读降级 | P2 |
| D11 | 动作区 | tail 行 28×28 组 | 卡片内 Button（sm, ghost, 带文字） | 收进 tail 行，图标化 | P2 |
| D12 | 卡片外壳阴影 | 无（仅浮层有阴影） | 消息卡有 40px 大阴影 | 去阴影，阴影只留浮层 | P0 |
| D13 | 边框宽度 | **源码 17 处声明 `0.5px`**（`TurnProcessNodeView` 底边、`ConversationRoot.header` 底边、`MessageItem` 卡片、`QueueDock`、`HeroShell`、`TodoPanel`、`SidebarRoot` 等）；**实测环境（dpr=1）解析并绘制为 1px** | 1px | 保持 1px 是**可接受等价**，但「参考站无发丝」的表述作废：若要在 dpr≥2 设备上与参考站一致，需显式声明 0.5px（§13 C3） | P2 |

---

## 8. 目标设计（本地落地口径）

### 8.1 落地原则与不可照搬项

| 参考站做法 | 本地口径 | 理由 |
|---|---|---|
| CSS Modules + `--dsw-*`，**禁用 Tailwind** | 保留 Tailwind v4 + `@theme` 语义 token，**新增/扩展**语义别名而不是引入第二套体系 | 本仓已完成 P0-4 三层 token（primitive → semantic → `@theme`），推翻成本远高于收益 |
| `corner-shape: superellipse` 全局圆角平滑 | **明确不引入（实测不可用）**；沿用 `rounded-*` 工具类 | 本仓 Chromium 实测：`superellipse` 关键字被拒绝且静默回落 `round`（`CSS.supports` 为 false），仅 `bevel` 等可生效（§10.4） |
| 中性边框 `0.5px` | **维持 1px，不做发丝化** | 双侧实测：参考站整页边框全为 1px；本仓 `border-width: 0.5px/0.25px` 被向上取整为 1px，border 无法产生发丝（§10.4） |
| 抬升面 `border: 0` + `--dsw-elevation-*` | 保留现有 `border-border` + 阴影策略，但**消息节点不再算抬升面**（不得有阴影） | 消息不是浮层；阴影是「浮层专属语言」 |
| 组件库/共享控件 `ui-primitives` | 继续用本仓 `components/ui/*` | 已有等价资产 |

### 8.2 Token 与排版标定（映射表）

| 用途 | 参考站 | 本地现有 | 目标（本地语义 token / 类） |
|---|---|---|---|
| 正文 | 默认 `14px/24px`（用户可调；实测环境为 16/28） | `app-chat-*`（15/25.8） | **建立单一字号轴**：`--app-chat-font-size`（默认值另定）＋ `--app-chat-font-delta = size − 基准`，行高、图标、行高盒一律 `calc(基准 + delta)`；**不要硬写 16/28**（§13 C4） |
| 块间距 | flex `gap: 16px`（`.body`），段落继承 | `p.my-3`（12px margin） | 正文块容器改 `flex flex-col gap-4`，段落 `margin` 归零；避免「gap + margin」双计 |
| 行内 code | `14/22`，`bg #2c2c2e`，`padding 0 5px`，无边框 | `14.25/21.375`，有边框，`2px 6px` | `app-inline-mono` → `14px/22px`、`padding 0 5px`、`border: none`、底色 token |
| 代码块 | `13/22`，底 `#1b1b1c`，条 `#2c2c2e` | 见 `ui/code-block.tsx` | 字号/底色对齐；标题条与底分离 |
| 小字（行标题/摘要） | **次级字号轴**（默认 `13px`，行高 `calc(基准 + delta-secondary)`；实测 `14/24`、`13/20` 是当前设置下的解析值） | `app-text-11`/`app-text-10` 混用 | 过程行统一走次级轴变量（不写死 14/24）；标题 `--label-secondary` 级、摘要 `--label-tertiary` 级（§13 C4） |
| 过程行高 | `24px` | 32px+ | 行 `h-6`（24px）+ `gap 6px` 前导位（`w-4 mr-1.5`） |
| 分隔点 | 2px、`radius 1px`、`margin 0 8px` | 无 | 新增 `.chat-row-sep`（`w-0.5 h-0.5 rounded-[1px] mx-2`） |
| 列宽 | `clamp(680px, 列宽×64%, 920px)`，可被用户宽度偏好整体替换 | `max-w-[52rem]`(832) 定宽 | 新增 `--app-chat-content-width`（clamp 同构）＋ 三处派生：转录列 = W、停靠卡 = W−32、**输入卡 = W+32**（§13 C1；批次 F2） |
| 列间距 | 兄弟 `margin-top: var(--dsh-chat-flow-gap, 16px)`；折叠后回答行 8px | `gap-6`(24) | 用 `gap-4` 起步，但需保留「折叠态回答行 8px」的收窄能力（批次 F3） |
| 用户气泡 | `#2c2c2e/22px/10px 16px/min(525,82%)` | 金色渐变卡 | `bg-[var(--surface-strong)]`-级 + `rounded-[22px]` + `px-4 py-2.5` + `max-w-[min(525px,82%)]` |
| hover 元数据 | `opacity:0 → 1` | 常显 | `opacity-0 group-hover:opacity-100 transition-opacity` |
| 动作按钮 | `28×28 / radius 28 / padding 6` | `Button size=sm` 带文字 | 图标态 `size-7 rounded-full p-1.5` |

> 本地没有 `--dsw-*` 变量，上表右列是**目标映射**：颜色一律走既有语义 token（`--foreground` / `--muted-foreground` / `--border` / `--surface-*` / `--accent-*`）。若某用途缺少语义 token，先在 `styles/globals/tokens.css` 增补语义别名，**禁止在组件里写字面色值或 `oklab(...)`**（现状：`assistant-message-card.tsx:52`、`user-message-bubble.tsx:66-69`、`history-context-message-card.tsx:61` 存在内联 `rgba(...)` 渐变）。

### 8.3 页面骨架目标

```
<div class="flex-1 overflow-y-auto px-3 py-4 sm:px-4" overflowAnchor:none>   ← 滚动宿主（保留 P1-3 所有权）
  <div role="log" class="mx-auto flex w-full max-w-[46.75rem] flex-col gap-4">
     <FlowItem kind="context" />        ← 上下文注入 / 系统提示词（24px 单行）
     <FlowItem kind="user" />           ← 用户气泡（右对齐）
     <FlowItem kind="assistant-step" /> ← Think 行 / 正文
     <FlowItem kind="tool-call" />      ← 工具行（24px 单行）
     <FlowItem kind="turn-tail" />      ← 动作 + 时间/耗时/token（hover 显现）
  </div>
</div>
```

- 滚动宿主保留 `overflowAnchor: none` 与 `useConversationScroll`（P1-3 契约不破）。
- `role="log"` / `aria-live` / `aria-busy` 语义保留（无障碍不回退）。
- 回溯导航/连接提示等**非消息**内容仍作为列内子节点，但改为「无卡片」提示行（去掉 `border + bg` 大块）。

### 8.4 渲染逻辑目标：`ChatMessage → FlowItem[]`

新增纯函数投影（建议位置 `lib/chat-view/flow.ts`，与既有 `project.ts` 同域，不新建权威状态）：

```ts
export type ChatFlowItem =
  | { kind: "context";       key: string; anchorKey: string; source: string; title: string; node: ChatViewNode }
  | { kind: "system-prompt"; key: string; anchorKey: string; node: ChatViewNode }   // §13 C5：与 context 分行
  | { kind: "user";          key: string; anchorKey: string; message: ChatMessage }
  | { kind: "steering";      key: string; anchorKey: string; message: ChatMessage } // 与 user 同形制
  | { kind: "assistant-step"; key: string; anchorKey: string; node: ChatViewNode }
  | { kind: "tool-call";     key: string; anchorKey: string; node: ChatViewNode }
  | { kind: "turn-process";  key: string; anchorKey: string; message: ChatMessage;  // 回合级统计折叠行（§13 C2）
      stats: { toolCalls: number; messages: number; subagents: number } }
  | { kind: "notice";        key: string; anchorKey: string; tone: "error" | "warn" | "info"; node: ChatViewNode } // turn-error / turn-max-tokens / retry 收敛（§13 C5）
  | { kind: "turn-tail";     key: string; anchorKey: string; message: ChatMessage }
  | { kind: "fallback";      key: string; anchorKey: string; raw: unknown };        // 未知类型可读降级，禁止白屏

export function projectChatFlow(messages: ChatMessage[], opts?: {
  streamingMessageId?: string | null;
}): ChatFlowItem[];
```

规则：

1. **一条 `ChatMessage` 展开为 N 个 item**：`text` 段 → 1..n 个 `assistant-step`（正文/推理），`tool` 段 → 1 个 `tool-call`，`context` 消息 → 1 个 `context`，系统提示 → 1 个 `system-prompt`（**不再与 context 合流**），用户消息 → 1 个 `user`（若为 steering 事件则 `steering`），错误/截断类 → 1 个 `notice`，回合末尾 → 1 个 `turn-tail`。
2. `key` 稳定：沿用 `ChatViewNode.key`（P1-1 已保证「追加新段不改变既有 key」），附加 `kind` 前缀；不得使用数组下标。
3. `anchorKey` = `message.id`（或 `message.id#node.key`），供回溯/搜索/引用跳转，与内容解耦。
4. **DOM 上打语义锚点**：`data-chat-flow-kind` / `data-chat-flow-key` / `data-chat-anchor-key`（对齐参考站命名），既服务 e2e 断言，也服务「跳转到某次工具调用」。
5. 每个 item 渲染为**同级兄弟**，不再是卡内嵌卡；折叠状态由 item 自己的局部 state 承担（工具行展开、Think 展开），互不影响。
6. **折叠语义（已按 §13 C2 修正）**：既有 `projectChatView` 的折叠谓词（`collapsed` / `summary` / `finalAnswerStart`）**保留**，统计口径不变；表现层改为两层——① 回合折叠由 `turn-process` 统计行承担，**收起时该回合过程行不渲染**（与参考站 `processHidden` 一致）；② 展开后过程行再各自单行压缩/可展开。`ProcessCollapseRow` 由「藏内容」改为「统计折叠行」形制，不再另加「显示全部步骤」开关。
7. **不参与折叠的 kind**（对应参考站 `TURN_PROCESS_INDEPENDENT_KINDS`）：`system-prompt` / `user` / `steering` / `turn-process` / `notice` / `turn-tail` 始终可见；折叠谓词只作用于 `context` / `assistant-step`（推理）/ `tool-call`。
8. **折叠例外**：该回合内出现新的 `user` / `steering` 时**不折叠**（参考站 `compactAnswer` 语义）；`streamingMessageId` 指向的回合恒不折叠。
9. **未知类型**：投影函数遇到未识别事件必须产出 `fallback`（保留原始载荷的只读 JSON 视图），**不得抛错、不得吞事件**；单测以注入未知 kind 覆盖。

### 8.5 各节点目标规格（本地实现口径）

| 节点 | 结构 | 关键类（目标） |
|---|---|---|
| `user` / `steering` | 行（右对齐）→ 气泡 → hover 动作区（编辑/回溯/复制 + 时间） | `flex justify-end` / `max-w-[min(calc(var(--app-chat-content-width)*0.702),82%)] rounded-[22px] px-4 py-2.5 bg-[var(--surface-strong)]` + `text-[length:var(--app-chat-font-size)]/[calc(22px+var(--app-chat-font-delta))]` / `group` + `opacity-0 group-hover:opacity-100`（`steering` 复用同一套类，仅语义锚点不同） |
| `context` | 24px 单行：前导位 + 图标 + 标题 + 分隔点 + 来源/摘要 + 右侧展开按钮 | `flex items-center h-6 text-[14px]/[24px]`；标题 `text-[var(--...secondary)]`，摘要 `text-muted-foreground`；`cursor-pointer hover:bg-[var(--surface-soft)] rounded-md` |
| `assistant-step`（Think） | 24px 单行（折叠）→ Markdown（展开） | 行同上；展开后 `app-chat-copy` 正文块，与正文同排版；摘要单行 `truncate` |
| `assistant-step`（正文） | 纯 Markdown 块，无容器 | `app-chat-copy`（字号轴派生，**不写死 16/28**），复用 `MessageMarkdown`，不加 `prose` 外框；块间距由容器 `gap-4` 提供 |
| `system-prompt` | 24px 单行（与 `context` 同形制、**不同分支**）+ 展开代码面板 | 同 `context` 行类；面板 `rounded-lg p-[10px_16px_12px_12px] max-h-[141px] overflow-auto bg-[var(--surface-strong)] font-mono text-[11px]/[16px]` |
| `turn-process` | 33px 统计折叠行：标签 + 16px chevron；收起即隐藏过程行 | `flex h-6 items-center gap-2 pb-2 mb-2 border-b-[0.5px] text-[14px]/[24px] text-[var(--text-secondary)]` + `data-state=open \| closed`；全 0 时显示「思考了一会儿」 |
| `notice` | 13/20 单行：tone 色点/图标 + 加粗标题（+ 可选展开面板），**非卡片** | `flex items-center gap-2 text-[13px]/[20px]`；`tone=error` 用 `--state-error` 色点、`tone=warn` 标题用 warn 色；行高不随内容增长 |
| `fallback` | 未知类型兜底：只读 JSON 块 + 「未知事件类型」标题 | 沿用 `JsonBlock` 形制；**不得白屏、不得抛错**；`data-chat-flow-kind="fallback"` |
| `tool-call` | 24px 单行：图标 + 工具名 + 分隔点 + 摘要（含路径/URL/diff/exit code 富摘要） | 行同上；`font-mono` 仅用于路径/命令片段；失败态用 `--state-error` 级色 + 词缀（不整行变红底） |
| `turn-tail` | 28px 行：动作按钮组 + 统计文本（hover 显现） | `flex items-center gap-2.5 h-7 -ml-1.5`；统计 `text-[14px]/[24px] text-muted-foreground opacity-0 group-hover:opacity-100` |

失败/进行态：

- 进行中：行内图标 `animate-spin`（沿用 `LoaderCircleIcon`），**不加整行骨架**。
- 失败：行图标换 `XCircleIcon` + 摘要尾部追加错误短语；详情在展开面板（参考站一致：错误不改变行高）。

> **展开入口（§12.1.3 补，2026-09-14）**：可展开的过程行一律提供**两个同义入口**——前导图标（指针友好，不必瞄准行尾）与右侧展开按钮 / chevron（行内唯一 Tab 停靠点）。整行本身是 `button` 的形态（纯文本摘要：`context` / Think / `system-prompt`）本就整行可点；摘要含链接时（`tool-call`，整行是 `div`）由前导图标补齐指针入口，两侧 `aria-expanded` / `aria-controls` 同步。

### 8.6 打字机（`useTypewriter`）处置

结论：**默认移除**（`StreamingMarkdown` 直接用 `MessageMarkdown`）。

理由（可验证）：

1. 参考站无打字机，逐字渲染反而放大「流式卡顿」的感知；
2. 与 P1-2 的增量管线冲突：`useTypewriter` 返回的是 `content.slice(0, shown)`——当 `content` 增长而 `shown` 落后时，下一 tick 的输入是**同一前缀的延长**（append 语义成立），但当 `generation` 判定用 `stableContent.startsWith(freezeState.content)` 比较时，打字机滞后会让「上一帧显示的 stableContent」与「这一帧的 stableContent」出现**非追加关系**（因为上一帧只显示了前缀的一部分），从而误判为「内容被改写」→ `generation++` → 已冻结块 remount 重解析，直接吃掉 P1-2 的性能收益。
3. 记录在案的替代方案（若产品坚持要打字机手感）：仅在 `interrupted`/等待首 token 时显示轻量占位（脉冲点），不截断正文。

验收：移除后 `message-markdown.test.tsx` / 流式相关 e2e 通过；新增断言「流式过程中同一块不因打字机滞后而 remount」（用既有冻结计数器或 DOM 稳定性断言）。

---

## 9. 实施批次

> 批次内条目可独立提交；每批必须跑完 §10.2 门禁四件套后才算完成。文件路径均相对 `frontend/src/`。

### 批次 A（P0）页面骨架：去卡片、收敛列宽与间距

| ID | 范围 | 做法 | 验收 |
|---|---|---|---|
| A1 | `components/workspace/message-list.tsx` | 内容列改 `w-full max-w-[var(--app-chat-content-width)]` + `gap-4`（宽度轴见 §8.2 / C1，**不写死 748px**）；`article` 保留语义属性但去掉 `justify-*` 之外的视觉负担；空态/提示块改「无卡片」提示行 | 实测列宽 = `clamp(680px, 列宽×64%, 920px)` 的解析值 ±2px、gap 16±1px |
| A2 | `message-list/assistant-message-card.tsx`、`history-context-message-card.tsx` | 删除外壳 `rounded-[1rem] border shadow-[0_16px_40px_...] bg-[linear-gradient(...)]`；改为扁平行容器（无 bg/border/shadow）；删除头像 chip + 作者 + label + 竖直渐变线 | 采样节点 `background=transparent`、`border=0`、无 box-shadow |
| A3 | `message-list/user-message-bubble.tsx` | 改右对齐气泡：`max-w-[min(525px,82%)]`、`rounded-[22px]`、`px-4 py-2.5`、单层底色；内联编辑 textarea 只在编辑态出现且不撑破气泡 | 气泡实测宽 ≤525px、圆角 22px、无渐变 |
| A4 | `hooks/workspace/use-conversation-scroll.ts`（只读复核 + 若需要微调） | 列宽变化后复核阅读线采样区间与贴底阈值；必要时只调常量，不改契约 | 既有 `use-conversation-scroll` 测试通过；手工验证长流贴底/保顶 |

### 批次 B（P0）过程行压缩：context / Think / tool 统一 24px 单行

| ID | 范围 | 做法 | 验收 |
|---|---|---|---|
| B1 | 新增 `components/workspace/chat-process-row.tsx`（共享行控件：前导位 + 图标 + 标题 + 分隔点 + 摘要 + 右侧展开态） | 抽出一处实现，context / Think / tool 三处复用；**不复制第二份** | 三处节点行高均实测 24px；DOM 结构一致（同一 `data-chat-row` 锚点） |
| B2 | `message-reasoning-row.tsx` | 头行换成 B1 控件（标题「Think/推理」+ 单行摘要）；展开区改 Markdown 渲染（与正文同排版）；`pre` 仅用于无 Markdown 的兜底 | 折叠态 `rect.h = 24 + delta`；展开后正文与正文块同字号轴（不给 reasoning 单独标定） |
| B3 | `message-list/history-context-message-card.tsx` | 头行换成 B1（标题「上下文注入」+ 分隔点 + 来源文件名/包名）；去掉卡片与头像 | 折叠态 24px；来源可直接读到 |
| B4 | `message-tool-row.tsx` | 头行换成 B1（工具名 + 富摘要），失败态用图标+词缀表达；展开面板去 1px 边框改无边框面板（用底色/间距分隔） | 折叠态 24px；失败态行高不变 |
| B5 | `tool-row/tool-row-summary.tsx` | 摘要优先级排序（路径 > 命令 > 查询 > URL > diff > exit code），确保单行内展示「最有用的一项」 | 新增用例：给定 details，单行摘要长度 ≤ 截断阈值且首选路径/命令 |
| B6 | `message-list/process-collapse-row.tsx` | 语义降级为「显示全部步骤」开关（可选、默认不出现）；统计式摘要保留为次级文案 | `collapse-rendering.test.tsx` 语义更新 + 通过 |

### 批次 C（P1）交互面：用户动作区 + turn 尾行

| ID | 范围 | 做法 | 验收 |
|---|---|---|---|
| C1 | `message-list/user-message-bubble.tsx` | 编辑/回溯/复制收进气泡下动作区；时间戳默认 `opacity-0`，`group-hover` 显现；按钮改 28×28 圆图标（保留 `aria-label`） | 键盘可达（Tab 可聚焦，focus-visible 环可见）；hover 显现不改变布局 |
| C2 | 新增 `message-list/turn-tail-row.tsx` | 回合尾部行：复制/重试/反馈按钮组 + `用时 / 首 token / tok-s` 统计（默认隐藏，hover 显现）；数据来自既有 `lib/turn-usage.ts` 与消息时间戳 | 统计文本可读且不撑高（行高 28px）；无 usage 数据时整段隐藏（不显示 0） |
| C3 | `assistant-message-card.tsx` | 删除卡内 `turnUsage.summary` 文本行，改由 C2 承担 | 旧断言迁移到 C2 用例；无重复渲染 |

### 批次 D（P1）Markdown 排版标定与流式手感

| ID | 范围 | 做法 | 验收 |
|---|---|---|---|
| D1 | `styles/globals/tokens.css`（`@theme` 层） | **建立字号轴**（C4）：`--app-chat-font-size` + `--app-chat-font-delta`，`app-chat-copy` 字号/行高成对 `calc(基准 + delta)`；块间距改容器 flex `gap: 16px`（段落 margin 归零）；标题/列表/引用/表格同轴成对标定 | 实测 `p` 字号/行高满足成对 delta 关系且块间距 16px；**无任何硬编码 16/28** |
| D2 | `message-markdown/markdown-components.tsx`、`components/ui/code-block.tsx` | 行内 code 改 `14/22`、`padding 0 5px`、去边框；代码块 `13/22`、底/条分色 | 实测 code 无 border；代码块与标题条底色不同 |
| D3 | `message-list/segment-components.tsx`、`hooks/workspace/use-typewriter.ts` | 移除打字机接线（`StreamingMarkdown` 直连 `MessageMarkdown`）；hook 与测试一并删除或标注废弃 | 流式 e2e 通过；新增「块不因滞后 remount」断言（§8.6） |

### 批次 E（P2）纪律与可测性

| ID | 范围 | 做法 | 验收 |
|---|---|---|---|
| E1 | `lib/chat-view/flow.ts` + `message-list.tsx` | 落地 `projectChatFlow` 与 `data-chat-flow-kind/-key/-anchor-key` 锚点；**未知 kind 走 `fallback` 可读降级**（§13.2） | 单测覆盖 §8.4 全部 10 类 kind（含 `turn-process` / `system-prompt` / `steering` / `notice` / `fallback`）；e2e 可定位任意工具行 |
| E2 | 全局 | 清理消息相关组件内的字面色值/内联渐变（`rgba(`、`oklab(`、`#rrggbb`），改为语义 token | 新增/复用 lint 规则或 grep 门禁，命中数 = 0 |
| E3 | `styles/globals/*` | 发丝评估**已于 §10.4 实测收口（本方案取 1px 等价）**；按 C3 修正注释口径（「参考站源码确有 17 处 0.5px，dpr=1 下与 1px 等价，本仓不追」）；F3 的回合折叠行是唯一允许 `0.5px` 的候选点 | 规范注释含结论与实测依据；除 F3 外全仓无 `border` 0.5px 用法 |

### 批次 F（P1）二审新增项：字号轴 / 宽度轴 / 回合折叠 / 缺失 kind

> 来源：§13 纠正 C1–C5。**F 与 A–E 并行推进，不阻塞 A–E**；但 F2/F3 的落点与 A1、B6 同文件，需在 A/B 合并后基于同一列容器改。

| ID | 范围 | 做法 | 验收 |
|---|---|---|---|
| F1 | `styles/globals/tokens.css` + `app-chat-copy` 全链 | 建立字号轴：`--app-chat-font-size`（默认取本地现状）、`--app-chat-font-delta`、次级轴 `--app-chat-font-size-secondary`；行高/图标/行高盒一律 `calc()` 派生（C4） | 改 `--app-chat-font-size` 一处，正文/行标题/摘要/气泡行高**同步**变化且不溢出；无硬编码 16/28 |
| F2 | `message-list.tsx` + 输入卡 + 停靠卡 | 宽度轴：`--app-chat-content-width`（clamp 同构）＋ 三处派生（转录列 W、停靠卡 W−32、输入卡 W+32）（C1） | 三个宽度实测满足 W / W−32 / W+32 关系；窄视口下 ≥680px 语义正确（或按本地断点降级） |
| F3 | 新增 `message-list/turn-process-row.tsx` + `process-collapse-row.tsx` 改造 | 回合级统计折叠行（C2）：33px（24 文本 + 8 下内距）、`border-bottom: 0.5px`、收起时 `margin-bottom: 8px`、标签二级色 + 16px chevron；收起隐藏过程行、展开显示；`data-chat-flow-kind="turn-process"`；补「回合内有新 user/steering 则不折叠」例外；折叠态回答行间距收 8px | 收起时过程行 `rect.h = 0`（不渲染）、折叠行 33±1px；展开后过程行回到 24px；`collapse-rendering.test.tsx` 语义迁移完成 |
| F4 | `lib/chat-view/flow.ts` + 渲染分支 | `system-prompt` 拆出独立 kind（展开体同代码面板）；`steering` 复用 user 气泡形制；`fallback` 未知 kind 降级（C5） | 三类各有单测；`system-prompt` 与 `context` 不再共用同一渲染分支 |
| F5 | `turn-error` / `turn-max-tokens` / `retry` 相关组件 | 统一为「单行 + 可选面板」：错误 13/20 + 红点 + 加粗标题；max-tokens 标题 warn 色；retry（若本地有事件）13/20 + 折叠三角 + 进行中 shimmer（C5） | 三类行高不随错误内容增长；无新增卡片外壳 |
| F6 | §10.1 断言脚本 + §13.3 补测 | 扩展 `style-probe.mjs` 覆盖 `InputBar` 等未取证区域；把 13.2/13.3 的处置结论回填 | 补测产物落 `frontend/.tmp/` 并在 §13.3 回填数值（无推断值） |

### 批次依赖关系

```
A（骨架） ──► B（过程行） ──► C（交互面）
                  │                │
                  └──► D（排版/流式） ──► E（纪律/锚点）

F1（字号轴）─┐
F2（宽度轴）─┼──► 与 A/B 同文件，A/B 合并后立即做 ──► F3/F4/F5 ──► F6（补测回填）
            ┘
```

- A 必须先做：列宽与去容器会改变所有后续节点的可用宽度假设。
- D3（去打字机）可与 B 并行，但必须在 E1（锚点固化）之前完成，避免两条流式路径同时变更。
- F1/F2 是**全局量的轴**（字号、宽度），必须早于 F3–F5（各节点规格都引用轴）；F6 是收尾取证，可在任一时刻并行。

---

## 10. 验收与门禁

### 10.1 视觉验收（实测驱动，非目测）

复用 `frontend/.tmp/style-probe.mjs` 对改造后的页面重新取样，逐条比对：

| 断言 | 期望 | 来源 |
|---|---|---|
| 内容列宽 | = `clamp(680px, 列宽×64%, 920px)` 的解析值 ±2px（默认视口下 748） | `.Md3f7G_column`（§13 C1） |
| 宽度派生 | 停靠卡 = W−32、输入卡 = **W+32** | §8.2 / 批次 F2 |
| 列内间距 | 16 ± 1px | 同上 `gap` |
| 消息容器 | `background: transparent` / `border: 0` / 无 `box-shadow` | flow item |
| 过程行（context/Think/tool 折叠态） | `rect.h = 24 ± 1px` | §5.3–5.5 |
| 过程行标题 / 摘要 | 次级字号轴解析值（默认 13px 起，行高同轴）；色阶 = 二级 / 三级文本 | §5.4 / §13 C4 |
| 用户气泡 | `radius 22px`、`padding 10px 16px`、宽 ≤ 525px | §5.2 |
| 正文块 | 字号/行高满足成对 delta（`calc(基准 + delta)`），块间距 16px（容器 gap，段落 margin 为 0） | §5.4 / §13 C4 |
| 回合折叠行 | 展开态 33 ± 1px、收起态过程行不渲染（`rect.h = 0`） | §13 C2 / 批次 F3 |
| 行内 code | `14px/22px`、无边框、`padding 0 5px` | §5.4 |
| turn 尾行动作按钮 | 28×28、`radius 28px` | §5.6 |
| 时间/统计 | 默认 `opacity: 0` | §5.6 |

### 10.2 工程门禁

```pwsh
npm run lint          # 含 eslint + i18n 门禁 + 备份门禁 + verify-max-lines
npm run test          # vitest
npm run build         # tsc -b + vite build
npm run test:e2e      # playwright
```

要求：0 error（既有 warning 基线不增）；`verify-max-lines` 通过（本次新增文件均 < 500 非空行）；i18n `violations=0`（新增文案须双语同步）。

### 10.3 行为验收清单

1. 流式：长回复（> 5k 字 + 代码块）逐块推进，无回退闪动、无整段重排；冻结块不 remount（§8.6 断言）。
2. 工具（两层折叠都验）：回合**收起**时该回合过程行不渲染（仅一张统计折叠行）；**展开**后连续 10 个工具调用占 10×24px + 间距，点击任一展开/收起互不影响。
3. 失败：工具失败、连接断开、回答中断三种状态均只改变行内图标/词缀，不改变行高、不新增大色块。
4. 折叠：回合折叠行显示统计（N 工具调用 · M 消息 · K 子代理；全 0 显示「思考了一会儿」）；展开后 Think 的 Markdown 与正文排版一致；行级折叠后回到 `24 + delta` 单行；**回合内有新 user/steering 时该回合不折叠**；流式中不折叠。
5. 用户消息：编辑/回溯入口仅在 hover/focus 时可见，键盘可达；`Escape` 退出编辑（保留既有语义）。
6. 无障碍：`role="log"`、`aria-live`、`aria-busy`、`aria-expanded`/`aria-controls` 全部保留；对比度按现有主题 token 不降低。
7. 亮/暗主题：两类主题下所有新样式可读（不新增硬编码暗色值）。
8. 新增 kind：`system-prompt` 与 `context` 分行渲染（不共用分支）；`steering` 与 user 同形制；`notice` 三类（错误/截断/重试）行高恒定；注入未知 kind → 渲染 `fallback` 只读 JSON，**页面不白屏、控制台无 error**（§13 C5）。
9. 轴联动：拖宽内容列时气泡、输入卡、停靠卡按 W / W−32 / W+32 同步；改字号轴一处，正文/行标题/摘要/气泡行高同步变化且无溢出（§13 C1/C4）。

### 10.4 取证收口（2026-09-14 补充实测）

**（1）已实测收口 —— 结论直接约束实施，不再留给实施期临时决定**

| 项 | 结论 | 关键实测数据（脚本见附录 A） |
|---|---|---|
| 0.5px 发丝边框 | **本方案取 1px 等价（口径已按 §13 C3 修正）** | ① 源码标注：参考站**确有 17 处 `border-*.width: 0.5px` 声明**（回合折叠行底边、会话头底边、部分卡片/面板等），此前的「参考站无发丝」表述作废；② 参考站会话页整页 1452 个节点在 **dpr=1** 下计算值**全部为 `1px`**；③ 本仓 Chromium 像素级实测：`border-width: 0.5px` 与 `0.25px` 均被**向上取整为 `1px`**（dpr=1 画 1 物理像素、dpr=2 画 2 物理像素），故 border 无法产生「亚像素发丝」；结论：dpr=1 下 1px 与参考站**视觉等价**，dpr≥2 下参考站略细（本方案不追，例外见 F3） |
| 发丝的替代实现 | **存在但本方案不采纳** | `box-shadow: 0 0 0 0.5px <color>` 实测可行：dpr=1 画出单行 50% alpha（白 50% 混 #808080 = RGB `191,191,191`），dpr=2 画 1 物理像素。不采纳原因：与「消息节点不是抬升面、不得带阴影」冲突；且需要 `0.5px` 的节点用 `border` 声明即可（dpr≥2 生效），无需引入阴影 |
| `corner-shape: superellipse` | **不可实现，禁入实施** | `CSS.supports('corner-shape','superellipse') === false`；实际声明后计算值**静默回落 `round`**（整页 1452 节点 cornerShape 值分布 = 全部 `round`）；对照项 `bevel` 可生效（计算值 `bevel`）。圆角一律走 `rounded-*` |
| 卡片外壳样式的承载节点 | 承载在**元素自身**，无伪元素参与 | `div.overflow-hidden.rounded-[1rem].border`：`background-image: linear-gradient(rgba(143,208,198,0.08), rgba(143,208,198,0.02))` + `1px` 边框（`oklab(... / 0.14)`）+ `border-radius: 16px` + `box-shadow` **第 5 槽** `rgba(0,0,0,0.12) 0px 16px 40px`；`article` 自身与 `::before`/`::after` 均无背景/阴影。批次 A 可直接在该节点摘除。**注意：Tailwind v4 的 5 槽 shadow 组合前 4 槽恒为 `rgba(0,0,0,0)`，不得凭截断字符串判定「无阴影」**（取证时已踩过该坑） |
| 本地既有发丝/ring 存量 | **为 0，无需迁移** | 本地会话页整页 819 节点扫描：`ring`/发丝用法命中数 = 0；边框宽度直方图 `{1px: 372}` |
| 「抬升面用阴影不用边框」 | **前半句成立，后半句不成立** | 参考站整页仅 2 处 `box-shadow`，均为卡片/浮层（`.uV2eYG_card` 780×94、`._card_1b2ny_13._copyable_1b2ny_25` 244×96），而 6 类 flow item **全部无阴影**（支持 D12）；但其中 `.uV2eYG_card` **同时带 1px 边框**，故「抬升面不用边框」不成立。本地口径不变：消息节点去阴影，浮层保留「边框 + 阴影」 |

**（2）仍待收口**

| 项 | 状态 | 收口方式 |
|---|---|---|
| 2px 分隔点的实际高度 | 参考站只测到宽度 2px | 本方案按 2×2 圆点实现；实施时以「视觉等于参考站截图」为准 |
| `--dsw-*` 与本地 token 的逐条等价值 | **已收口（2026-09-14）** | 按「本方案实际采用项」记录于 §12.2，每行标注证据来源（实测报告行号 / §13.4 源码标注 / `--dsw-*` 令牌表）；参考站 `--dsh-*` 字号轴未进入浏览器快照，其值以源码标注为准 |
| 参考站在窄视口/长会话下的表现 | 未测 | 本方案以本地响应式规则为准（不引入参考站断点） |
| §13.3 未取证区域（`InputBar` / `EmptyHero` / `QueueDock` / `ContextMeter` / `TodoPanel`） | **已收口（2026-09-14，F6）** | 补测产物 `frontend/.tmp/style-probe-input.mjs` + `style-probe-input-output.json`（Playwright，1440×900 / dpr=1，参考站**新会话空态**）：`composerStack` 812×242、`textarea` 778×52、hero 容器 812×242 + `heroGlow` 1100×490；`QueueDock` / `ContextMeter` / `TodoPanel` 三者 `classHits=0` → 列非目标。逐项数值见 §13.3，**无推断值**；停靠态（会话内）留待复测，不阻塞 A–F |
| 参考站在**非默认设置**下的表现（字号轴非 0 delta、宽度被拖拽、`normal` 转录模式） | 未测 | 本方案按源码机制（delta / clamp / `TranscriptViewMode`）而非单一实测值落地（§13 C1/C2/C4） |

---

## 11. 风险与回归面

| 风险 | 影响 | 缓解 |
|---|---|---|
| 去卡片后「消息边界」变模糊 | 用户可能难分辨两条消息 | 用 16px 列间距 + 用户气泡/过程行形态差异建立边界；如需更强分隔，加**极轻**分隔线而非卡片 |
| 过程行压扁后信息丢失 | 用户看不到工具参数 | 单行摘要必须承载最关键参数（B5 定义优先级），详情一键展开 |
| 打字机移除引发产品预期落差 | 「AI 正在输入」的手感消失 | 用进行态图标 + 阶段提示（既有 `PHASE_LABELS`）/ `aria-busy` 替代；若需手感，按 §8.6 的替代方案 |
| 现有测试大量依赖卡片 DOM | 改版即红灯 | 按批次迁移断言到语义锚点（`data-chat-flow-kind`），禁止靠 class 断言 |
| 宽度轴引入后影响右栏/窄屏 | 内容列变窄（默认解析值 748 < 现状 832），长代码更易横向滚动；三处宽度派生若漏改会错位 | 代码块维持横向滚动；窄屏下轴值只降不升（配合 `px-3 sm:px-4`）；F2 验收强制断言 W / W−32 / W+32 关系 |
| 回合级折叠改变既有交互（§13 C2） | 用户原先「一眼看到全部过程」，改后需先展开折叠行；既有 `collapse-rendering.test.tsx` 大面积依赖旧 DOM | F3 迁移测试到语义锚点；保留「展开后仍是单行 + 可展开」的两级退路；流式中恒不折叠，避免打断阅读 |
| 字号轴（`calc(基准+delta)`）引入计算链 | 行高/图标尺寸集中依赖单一变量，改错一处会全局偏移；Tailwind 任意值写法可读性下降 | F1 用 token 层集中定义、组件只引用变量；验收要求「改一处变量 → 全链同步变化」；避免在组件内写 `calc` |
| dpr≥2 设备上发丝观感与参考站不一致（§13 C3） | 高清屏细节差异（可接受） | 明确不追（§2.2）；仅 F3 折叠行允许 0.5px；若有投诉，按 C3 的替代方案评估，不扩大到消息节点 |
| 元数据 hover 化后触屏不可见 | 移动端看不到时间/统计 | 触屏（`@media (hover: none)`）下默认可见；此项写入批次 C 验收 |
| token 增补导致亮色主题失衡 | 亮色下颜色失衡 | 新增 token 一律走 primitive → semantic 两层，亮/暗双值同批提交 |

---

## 12. 实施记录（落地时回填）

> 预实施取证（2026-09-14）：§10.4 已完成 5 项实测收口（发丝口径修正为「源码有 0.5px、dpr=1 等价」/ 替代实现不采纳 / `superellipse` 不可用 / 卡片外壳承载在元素自身 / 本地发丝存量为 0），证据脚本与产物见附录 A。该轮**未改动任何源码**，故不涉及门禁。
>
> 二审（2026-09-14，源码复核）：新增 §13 纠正台账（C1–C5）与批次 F（F1–F6）。本轮同样**未改动源码**；方案文档相应章节已修订，与 §13 冲突的旧表述以 §13 为准。

| 批次 | 提交 | 日期 | 门禁结果 | 实测对比（改造前 → 改造后） |
|---|---|---|---|---|
| A | `9bd162e8` | 2026-09-14 | 四件套全绿（见下「门禁口径」） | 卡片外壳（渐变底 + `1px` 边框 + 5 槽阴影 + 头像 chip + 作者行）→ **扁平行容器**（无 bg / border / shadow）；内容列定宽 `832` → `max-w-[var(--app-chat-content-width)]` + `gap-4`（默认解析 `748`，clamp 见 C1）；用户消息 → 右对齐气泡 `min(内容宽×0.702, 82%)` / `rounded-[22px]` / `px-4 py-2.5` |
| B（工具执行渲染部分） | `9bd162e8` | 2026-09-14 | vitest 178 文件 / 1301 用例通过；`npm run lint` OK（eslint 0 error + i18n 644 文件 / 备份 / 行数 / 字面量四道脚本）；`tsc -b && vite build` OK；`e2e/workspace-chat.spec.ts` 9 通过（含 G2 折叠→展开） | 折叠态工具行：**卡片 + 结果常驻可见** → **24px 单行**（图标 + 工具名 + 分隔点 + 富摘要 + 状态词缀），结果/输入/错误收进展开面板；失败态：整行红底 → 图标 + 摘要尾缀表达，**行高不随错误内容增长** |
| C | `9bd162e8` | 2026-09-14 | 四件套全绿（见下「门禁口径」） | 卡内 `turnUsage.summary` 文本行 → 独立 `turn-tail-row`（28px 动作组 + hover 统计；无 usage 数据整行隐藏，不显示 0）；用户动作区（编辑/回溯/复制）收进气泡下方动作区，按钮 28×28 圆图标 + `aria-label`，时间戳 `group-hover` 显现（触屏 `@media (hover: none)` 默认可见，不改变布局） |
| D | `9bd162e8` | 2026-09-14 | 四件套全绿（见下「门禁口径」） | 打字机接线（`hooks/workspace/use-typewriter.ts` + 测试）→ **删除**，`StreamingMarkdown` 直连 Markdown 渲染：显示内容不再滞后截断，块不因前缀变化 remount；正文块间距 → 容器 flex `gap`（段落 margin 归零）；行内 code / 代码块按 D2 标定（去边框、底/条分色） |
| E | `9bd162e8` | 2026-09-14 | 四件套全绿 + `verify-message-tokens`（31 文件 / 0 命中）、`verify-max-lines`（892 文件，最大 499）、i18n（644 文件 / 0 违规） | `projectChatFlow` 与 `data-chat-flow-kind/-key/-anchor-key` 锚点落地（覆盖 §8.4 全部 kind，未知 kind → `fallback` 行可读降级）；消息组件字面色值 / 内联渐变**清零**（脚本门禁化）；发丝口径注释按 §13 C3 修正为「dpr=1 等价、F3 为唯一 0.5px 例外」 |
| F | `9bd162e8` | 2026-09-14 | 四件套全绿（含 e2e `design-tokens` / `G1` / `G2`） | F1 字号轴：`--app-chat-font-size: 15px` + delta，行高/行高盒一律 `calc(24px + delta)`（无硬编码 16/28）；F2 宽度轴 `clamp(680px, 64cqw, 920px)` + 三处派生（转录列 W / 停靠卡 W−32 / 输入卡 W+32）；F3 回合级统计折叠行（33px = 24 文本 + 8 下内距 + 0.5px 底边，收起时过程行不渲染、流式恒不折叠）；F4 `system-prompt` / `steering` / `fallback` 独立分支；F5 `notice` 单行收敛（turn-error / turn-max-tokens，行高不随内容增长）；F6 §13.3 补测与回填完成 |

> **门禁口径（2026-09-14 收口轮，A–F 同一工作区）**：`npm run lint` → 0 error（3 条既有 `exhaustive-deps` warning 未增）+ `i18n lint OK(scanned=644, violations=0)` + 备份 / 行数 / 字面量三道脚本 OK；`npx vitest run` → **178 文件 / 1301 用例通过**（142.8s）；`npm run build` → OK（vite 1.47s，仅既有 `INEFFECTIVE_DYNAMIC_IMPORT` 警告）；`npm run test:e2e` → **70 passed**（2.8m，含 `workspace-chat.spec.ts` 的 G1 流式推理展开与 G2 工具行折叠→展开）。

> **门禁口径（2026-09-14 终审轮，§12.1.2 两处修正后全量复跑）**：`npm run lint` → eslint 0 error（3 条既有 warning）+ `i18n lint OK(scanned=647, violations=0)` + `verify-no-backups` OK（946 文件 / 0 残留）+ `verify-max-lines` OK（896 文件，最大 499）+ `verify-message-tokens` OK（32 文件 / 0 命中）；`npx vitest run` → **179 文件 / 1322 用例通过**；`npm run build` → OK（vite 1.40s，`BUILD_EXIT=0`，仅既有 `INEFFECTIVE_DYNAMIC_IMPORT` 警告）；`npm run test:e2e`（**先 build 后跑**，见 §12.1.2 末段）→ **73 passed**（2.8m，退出码 0），覆盖 `workspace-chat` G2 / P1-3c、`trajectory` P3-1 / P3-2、新增 `tool-row-history`。§12.1.1 末行记录的「环境负载型 flake」据此收窄：`composer-commands` P2-7 `/export` 与 `session-grouping` 回滚两项在重建 dist 后 10/10 通过，属构建产物新鲜度问题，非负载 flake。

### 12.1 工具执行渲染落地明细（批次 B4/B5，2026-09-14）

| 落点 | 条款 | 改动 | 证据 |
|---|---|---|---|
| `components/workspace/chat-process-row.tsx` | §8.4 / §8.5（B1 复用） | 工具行改用共享过程行：24px 单行、hover 才有底色、`data-chat-row` / `data-chat-flow-kind="tool-call"` / `data-chat-row-state` / `data-chat-anchor-key` 锚点、独立展开按钮带 `aria-controls`/`aria-expanded` | e2e G2 用 `[data-chat-flow-kind="tool-call"]` 定位并断言折叠/展开 |
| `components/workspace/message-tool-row.tsx` | §5.5 / §8.5（B4） | 折叠态 = 图标 + 工具名 + 分隔点 + 富摘要 + 状态词缀（`data-tool-row-status`）；失败态不整行红底、错误摘要作尾缀；`fileLinkDisabled` 时不渲染文件链接；`sr-only` 播报保留 | `message-tool-row.test.tsx`；e2e G2 |
| `components/workspace/tool-row/tool-row-panels.tsx` | §5.5（B4） | 结果/错误/输入全部进展开面板（`data-tool-row-detail-panel`，未展开 `hidden`）；失败态错误块**替换**输出块（不追加）；面板去 1px 边框改「底色 + 间距」；`kind="json"` 走 `prettyJson` | 单测：折叠态面板 hidden 且内含 result/input，展开后出现 error 块 |
| `lib/tool-row/state.ts` | §5.5 / §8.5（B4/B5） | `isToolRowExpandable` 放宽为「输入 / 结果 / 错误任一非空」；新增 `orderSummaryParts` + `SUMMARY_PART_PRIORITY` / `SUMMARY_MAX_PARTS` / `SUMMARY_TEXT_LIMIT`（路径 > 文本 > URL > diff > 退出码，单行最多 2 项、文本 72 字符截断） | `state.test.ts` 新增 3 组用例 |
| `components/workspace/tool-row/tool-row-summary.tsx` | §5.5（B5） | 摘要字号改由过程行次级字号轴派生，去掉硬编码字号类 | `verify-message-tokens` / design-tokens e2e |
| i18n `panels-messages.ts`（zh-CN / en-US） | §5.5 | `expandLabel` / `collapseLabel`：「展开/折叠工具输入」→「展开/折叠工具详情」（面板已含输出与错误） | i18n lint OK（scanned=644, violations=0） |

> 边界：工具执行**内核**（状态机、事件分类、投影）属上游 P1-6，本批次只改渲染层；折叠态 24px 与「失败态行高不变」由「详情一律移入展开面板」保证。
> 门禁命令：`npm run test`、`npm run lint`、`npm run build`、`npx playwright test e2e/workspace-chat.spec.ts`。

### 12.1.1 收口补记：历史工具回执不再并入「上下文注入」（2026-09-14）

> 触发：首页消息列表里历史 `role="tool"` 回执此前一律落到 context 行（呈现为通用「上下文注入」），无法分辨读的是哪个文件、跑的是什么命令。补记的是**投影路由**这一处缺口，渲染形制仍按 §5.5 / §8.5（24px 单行 = 工具名 + 富摘要，点击展开详情）。

| 落点 | 条款 | 改动 | 证据 |
|---|---|---|---|
| `lib/chat-view/flow.ts`（新增 `isToolReceiptMessage`） | §8.4 规则 1 | 历史 `role="tool"` 回执先取其中的 `tool` 段投影为 `tool-call` 行（不再并入 context）；旧数据里没有可识别 tool 段时降级回 context 行，不丢内容 | `lib/chat-view/flow.test.ts`（14 用例，含回执路由与降级分支） |
| `e2e/tool-row-history.spec.ts`（新增） | §10.3 / B4·B5 | 注入「`read_file` 成功 + `shell` 失败」历史并刷新：断言 2 条 `[data-tool-row]`、摘要含 `notes.txt` / `exit status 1`、状态 `finished` / `error`、折叠态高度 ≤28px、展开面板含命令与错误、收起后面板 `hidden` | `npx playwright test e2e/tool-row-history.spec.ts`（通过） |

> **收口门禁（2026-09-14）**：`npx vitest run src/lib/tool-row/state.test.ts src/components/workspace/message-tool-row.test.tsx src/components/workspace/message-list.test.tsx src/lib/chat-view/flow.test.ts` → **4 文件 / 40 用例通过**；`tsc -b` 与 `npm run lint`（eslint + i18n / 备份 / 行数 / 字面量）OK；`npx playwright test` 全量 73 项跑两遍：分别 **72 / 71 通过**，失败项两遍互不相同（`pending-interaction` P1-7b、`composer-commands` P2-7 `/export`、`session-grouping` Host 回滚），三者单独重跑**全部通过** → 判定为环境负载型 flake，与本批次改动无关；`tool-row-history` / `workspace-chat` G2 / `trajectory` 工具筛选在两次全量运行中均通过。

### 12.1.2 终审轮修正：工具行标题回归 + e2e 落库竞态（2026-09-14）

> 触发：全量门禁复跑时 e2e `G2` 稳定失败（工具行内找不到工具名文本）。定位为**渲染层偏离本方案形制**：批次 B4 曾把折叠态标题从「工具名」换成人类可读动作词（`读取文件` / `Open page`…，随 `KIND_TITLE_KEY` 落地），与 §5.5 / §8.5 / §12「图标 + 工具名 + 分隔点 + 摘要」以及 e2e 既有契约（按工具名定位工具行）冲突；同一轮 `P3-1` 在相同命令下交替通过/失败，定位为**测试侧竞态**，非产品缺陷。

| 落点 | 问题 | 处置 | 证据 |
|---|---|---|---|
| `components/workspace/message-tool-row.tsx`、`lib/tool-row/state.ts`、i18n `panels-messages.ts`（zh-CN / en-US） | 折叠态标题被替换为本地化动作词，与 §5.5 / §8.5「工具名」形制相悖，按工具名定位的 e2e 断言（G2）全部失效 | 标题恒为 `segment.name`；删除已无消费者的 `KIND_TITLE_KEY` 与 `toolRow.kind.*` 词典（kind 只决定图标与摘要语义，不参与标题） | `message-tool-row.test.tsx`（已注册 kind 与未注册工具同断言：标题 = 原始工具名）、`message-list.test.tsx`；e2e `G2` / `P1-3c` 恢复通过 |
| `e2e/support.ts`（新增 `waitForAssistantHistory`）、`e2e/trajectory.spec.ts`（P3-1） | mock 与后端同口径：assistant 消息在 turn 收尾的 `done` 帧才落库，而正文 chunk 先到；D 批删除打字机后正文出现更早，刷新易落在落库窗口内，恢复出的历史只剩用户消息 | 刷新前显式轮询 `GET /api/runtime/sessions/:id/history` 直到 assistant 记录出现（非固定 sleep，不改断言语义） | `npx playwright test e2e/trajectory.spec.ts --grep "P3-1:" --repeat-each=3` → **3/3 通过** |

> **门禁执行注意（本轮实测）**：e2e 只跑 `vite preview` 的**构建产物**——`playwright.config.ts` 的 `requireDist()` 只校验 `dist/index.html` 是否存在、不校验新鲜度。故「src 改了但未重新 `npm run build`」会让用例以旧产物失败：本轮 `composer-commands` P2-7 `/export` 与 `session-grouping` 回滚两项，在重建 dist 后与本批其余用例（共 10 项）一次性全通过，**全量 e2e 之前必须先 `npm run build`**。§12.1.1 末行把这两项一并记为「环境负载型 flake」的口径据此收窄：其中至少一部分是构建产物新鲜度问题。

> **终审轮全量复跑（2026-09-14 18:19，修正 A+B 后）**：`npm run build` → OK（`BUILD_EXIT=0`），紧接 `npx playwright test` 全量 → **73 passed / 0 failed / 0 flaky**（2.8m，退出码 0），含此前两项争议用例（`composer-commands` P2-7 `/export`、`session-grouping` 回滚）与 `trajectory` P3-1；同轮 `npx vitest run` 179 文件 / 1322 用例、`npm run lint` 四道脚本均 OK（见 §12 门禁口径·终审轮）。

### 12.1.3 展开入口补强：前导图标与右侧展开按钮同义（2026-09-14）

> 触发：工具行折叠态只有行尾 `>` 可点，指针用户的可点区域过于靠右（要瞄准行尾）；而 `context` / Think / `system-prompt` 三种过程行摘要不含交互元素、整行本身就是 `button`，不存在该问题。补强落在**共享过程行控件**上，一次覆盖全部过程行形态。

| 落点 | 条款 | 改动 | 证据 |
|---|---|---|---|
| `components/workspace/chat-process-row.tsx` | §8.5（展开入口补注） | 摘要含交互元素（当前仅工具行）时，前导图标升级为**独立展开按钮**：`data-chat-row-toggle="icon"`、`aria-controls` / `aria-expanded` 与右侧按钮同步、`tabIndex={-1}` 使其指针可达但**不占第二个键盘停靠点**；右侧按钮补 `data-chat-row-toggle="chevron"` 与 `cursor-pointer`；可展开行补 `cursor-pointer` | `message-tool-row.test.tsx`「展开入口：点前导图标与点右侧 chevron 同义，aria-expanded 同步」（含 `iconToggle.tabIndex=-1` / `chevronToggle.tabIndex=0`）；e2e `tool-row-history`：**点前导图标展开**、**点右侧 chevron 收起**，两入口 `aria-expanded` 全程同步 |
| `e2e/tool-row-history.spec.ts`、`e2e/workspace-chat.spec.ts`（G2） | §10.3 | 展开入口选择器由 `button[aria-controls$="-panel"]` 收窄为 `[data-chat-row-toggle="chevron"]`（多入口后旧选择器语义不再唯一） | 见下方门禁 |

> **门禁（2026-09-14）**：`npx vitest run src/components/workspace/message-tool-row.test.tsx src/components/workspace/message-list.test.tsx src/components/workspace/message-list/segment-components.test.tsx src/lib/chat-view/flow.test.ts src/lib/tool-row/state.test.ts` → **5 文件 / 43 用例通过**；`npm run lint` → eslint 0 error（3 条既有 warning）+ i18n / 备份 / 行数 / 字面量四项校验 OK；`npx tsc -b --force` 无错误、`npm run build` 重建 dist 后再跑 e2e；`npx playwright test e2e/tool-row-history.spec.ts e2e/trajectory.spec.ts e2e/workspace-chat.spec.ts` → **17 passed**（含 `G2`、`P1-3a/b/c`、`P3-1/2`、`tool-row-history`）。

> **全量 e2e 波动记录**：全量 73 项在本轮先后跑三次，第 1 跑 1 例失败（`workspace-chat` P1-3a：读数锚点漂移 1084px）、第 2 跑 1 例失败（`workspace-chat` G5：`top` 距底 > 120px），失败项同属 **P1-3 滚动所有权家族**；同产物同命令重跑时 `G5` 失败率在 3/3 与 1/3 之间摆动（`P1-3a` 单独 `--repeat-each=3` → 3/3 通过），失败断言都落在「流式 chunk 到达后立即取几何」的窗口内，判定为**时序/负载抖动，非本次改动引入**——本次改动只在「摘要含交互元素的过程行」内新增按钮，而 `script=scroll` 流程（P1-3a / G5 的脚本）**不含任何工具行**，`interactiveSummary` 全仓仅由 `message-tool-row.tsx` 传入。处置：**不放宽既有容差、不改 P1-3 用例语义**，按已知抖动记录；若后续复现稳定，再单开一轮收口。

### 12.1.4 空内容不占位：占位文案 / 复制图标 / 空行三合一收口（2026-09-14）

> 触发（用户实测反馈）：① 界面出现 `[empty message]` 占位文案，且推理过程这类「本就没有正文」的行也被塞进占位；② 复制图标在每一行都常驻（无内容时只是置灰），用户要求「按具体内容显示」；③ 消息之间出现多余空行，挤压可视区。
>
> 参考站口径（源码实证）：`AssistantMarkdown` 对「只含工具调用头 / 没有任何可见块」的节点直接 `return null`，注释写明「a node that is only those heads (or empty) would paint an empty root between tool groups — skip the shell unless something visible remains」（`packages/client/ui-chat/src/client/chat/AssistantMarkdown.tsx:60-67`）；复制 / 分支入口只在**消息级 chrome** 出现一次（`MessageIconActions`，用户气泡与助手回合尾共用，`MessageItem.tsx:157-228` 的 `UserStyleBubble` 只有当 `text !== '' || rest.length > 0` 才画气泡），**过程行（推理 / 工具）不挂复制入口**。
>
> 本地实现原则：把「有没有可见内容」收口成**单一判定源**，渲染层据此决定「产不产行 / 产不产按钮」，而不是先渲染出来再用透明、`disabled`、占位文案去遮。

| 落点 | 条款 | 改动 | 证据 |
|---|---|---|---|
| `lib/chat-view/visible-text.ts`（新增） | §5.5 / §8.5 | 新增 `hasVisibleText`（`trim().length > 0`）作为全仓唯一空内容判据；文件头记录三条使用场景（不产行、不占位、不出复制入口） | 各渲染层不再各写 `trim()`；`npm run lint` 的 i18n / 行数脚本通过 |
| `lib/chat-view/message-visibility.ts`（新增）+ `lib/chat-view/index.ts` | §12.1.4 | `segmentHasVisibleContent`（段级：空文本 / 空推理 / 空 callout / 空代码块 → 不可见；工具行、图片占位行自带状态 → 恒可见）与 `hasVisibleMessageContent`（消息级：命中关联产物或回合用量时仍保留该行） | `message-visibility.test.ts`（5 用例：空壳 / 正文 / 推理 / 工具 / 产物 / 用量 / 空 callout / 空代码块） |
| `lib/thread-state/history-mapping.ts` | §5.5 | 历史消息 `content` 为空不再降级成 `"[empty message]"` 文本段；空正文直接不产段（工具回合 / 仅推理 / 仅附件是正常协议形态） | `history-mapping.test.ts`「空 content 不得降级成 `[empty message]`」（含工具回合 / 仅推理两形态） |
| `lib/thread-state/events.ts` | §9 流式 | `buildStreamingMessageSegments` / `createStreamingAssistantMessage` 不再注入 `...` 占位文本段：首块到达前段落序列为空，首个 `assistant_delta` 由 `appendTextToMessageSegments` 直接 push 新文本段（该函数保留「读到历史 `...` 段先清零再加」的兼容分支） | `runtime-events.test.ts`「没有正文时不产占位文本段」「流式助手消息初始无 segments」；`deltas.test.ts` 首块替换用例保持通过 |
| `components/workspace/message-list/segment-rendering.tsx` | §8.4 / §12.1.4 | 空文本段 `return null`（连锚点包裹都不落 DOM）；这是「空行」的第一类来源：空行节点自身高度为 0，但仍会被父级 `gap` 计入 | `segment-rendering.test.tsx` |
| `components/workspace/message-reasoning-row.tsx` | §5.5 / §12.1.4 | 无推理正文时整行不渲染；删除 `reasoningRow.empty` 占位摘要分支（推理行的摘要只由真实内容派生） | `message-reasoning-row.test.tsx`（4 用例） |
| `components/workspace/message-list.tsx` | §4.1 / §12.1.4 | 消息级空壳门：无可见内容的**助手**消息不产出 `<article>`。根因——转录列是 `flex flex-col gap-4`，空壳 `article` 仍是 flex item，会在相邻消息间撑出一条 16px 空行（典型：回合开始到首块到达之间的流式空壳、纯工具回合）；`aria-setsize` / `aria-posinset` 改按**实际渲染集合**计数 | `message-list.test.tsx`「无可见内容的助手消息不产出 article（不占 gap 空行）」 |
| `components/workspace/message-list/turn-tail-row.tsx` | §12.1.4 | 复制图标改为**按内容渲染**：该回合没有可见回答文本时整颗图标不渲染（不再「禁用但常驻」）；无文本、无用量、无重试入口时整行不渲染（去掉每条助手消息尾部 28px 空动作行） | `turn-tail-row.test.tsx`（5 用例） |
| `components/workspace/message-list/user-message-bubble.tsx` | §12.1.4 | 空文本段在气泡内不渲染（含外层包裹 div）；动作区整行按条件产出（无复制文本且无回溯 / 选中态 → 不产 28px 行）；复制按钮同样只在有文本时渲染 | `user-message-bubble.test.tsx`（5 用例） |
| `components/workspace/message-list/history-tool-message-row.tsx`、`history-context-message-card.tsx` | §12.1.1 / §12.1.4 | 行节点为 `null` 时不再套空包裹 `<div>`——空包裹会吃掉 `gap-1` / `space-y-2` 的间距，等价于一条空行 | 上述两卡片的既有单测 + `segment-rendering.test.tsx` |
| `components/workspace/artifact-panel-shared.ts` | §12.1.4 | 检查点对话摘要跳过空正文消息，不再用 `"[empty message]"` 顶上屏；`slice(0, 4)` 改为**过滤后**截取（空消息不再占用预览名额） | `artifact-panel-shared.test.ts`「没有正文的检查点消息直接跳过」 |

> **空行根因归纳（三类，全部落在「已渲染但无内容」上）**：
> 1. **占位内容被当成真内容**：`"[empty message]"` 文本段、`...` 流式占位段都会渲染成**真实行**（有行高、有 markdown 段落间距）；
> 2. **固定动作行**：`turn-tail` 与用户气泡动作区此前无条件产出 28px 行，空内容时留下一条只有背景悬停反馈的空行；
> 3. **空 flex item 吃掉父级 gap**：转录列 `gap-4`、气泡 / 历史卡内 `gap-1` / `space-y-2` 都按「子节点个数」分配间距，空段落或空包裹 `div` 自身高度为 0 **但仍占一个 gap 槽**，视觉上就是一条空行。三类的共性处置是**在产出节点的那一层判空**，而不是靠 `hidden` / `opacity-0` / 空字符串兜底。
>
> 边界：本批次只改渲染与投影层，不动运行时事件协议；`STREAM_PLACEHOLDER_TEXT` 常量保留，仅用于**读**历史里可能残留的 `...` 段（`getAssistantMessageText` / `appendTextToMessageSegments` 的兼容分支），不再用于**写**新消息。
>
> **与参考站的差异（有意偏离，用户显式诉求）**：参考站的 `UserStyleBubble` 只对「空正文」隐藏气泡本体，动作区仍无条件渲染（`MessageItem.tsx:226` 的 `actions?.(text)` 不在文本判空之内），即空文本时复制图标会以「可点但无内容可写」的形态留在屏上。本地按要求收紧为**内容驱动**：没有可复制文本就整颗图标不渲染——同时删掉常驻的 `disabled` 态，避免「看得见按不动」的无效动作位。
>
> **门禁（2026-09-14，全链路串行一轮过，尾行 `GATE_OK`）**：`npm run lint` → eslint 0 error（3 条既有 react-hooks warning）+ i18n（651 键全命中）/ 备份（958 文件 0 处 `*.bak`·`.backups`）/ 行数（最大 499 行）/ 消息字面量四项校验 OK；`npx vitest run` → **186 文件 / 1373 用例全通过**（含本轮新增 `message-visibility.test.ts` 5 例、`message-list.test.tsx` 空壳不产 `article` 例、`turn-tail-row.test.tsx` 5 例、`user-message-bubble.test.tsx` 5 例、`history-mapping.test.ts` 空 `content` 不降级例）；`npm run build`（`tsc -b && vite build`，3280 模块）无错；`npm run test:e2e` → **73 passed**（全量，含 §12.1.3 收窄过选择器的 `tool-row-history` / `workspace-chat`）。
>
> **提交回填（2026-09-14）**：批次 A–F（含 §12.1 工具行 B4/B5、§12.1.1–§12.1.3 的代码与 e2e 选择器收窄）落在提交 **`9bd162e8`**（上表「提交」列已回填）；本轮收口（§12.1.4 全部改动 + §12.1.1–§12.1.3 的文档记录）落在提交 **`25e6c029`**（25 文件，`+1727/−977`）。**提交态独立核验**：在该提交的独立 worktree 上重跑四项脚本（备份 954 文件 0 残留 / 行数 903 文件最大 499 / i18n `scanned=649, violations=0` / 字面量 32 文件 0 命中）与目标 vitest（`src/lib/chat-view`、`src/lib/thread-state`、`message-list`、`message-reasoning-row`、`artifact-panel-shared` → **17 文件 / 113 用例通过**），未发生「工作区绿、提交态红」。

### 12.2 参考站 → 本地 token 映射（本方案实际采用项，2026-09-14）

> 对应 §10.4(2) 的「逐条等价值」承诺：只登记**本方案实际采用**的项与证据来源；参考站 `--dsh-*` 字号轴未出现在浏览器快照（快照只含 `--dsw-*` 317 条），其值以 §13.4 源码标注为准，其余以实测报告行号为准。**本地一律走 primitive → semantic → `@theme` 三层，不搬参考站 CSS Modules / `--dsw-*` 命名，也不写字面色值。**

| 本地语义 token / 形制 | 参考站对应 | 数值 / 关系来源 |
|---|---|---|
| `--app-chat-content-width: clamp(680px, 64cqw, 920px)` | 内容列宽 `clamp(680px, 会话列宽×64%, 920px)`（用户拖拽偏好会整体替换该值） | §13.4 `ChatView.module.css` / C1 源码标注 |
| `--app-chat-content-width-dock: calc(W − 32px)`、`--app-chat-content-width-composer: calc(W + 32px)` | 停靠卡 / 输入卡宽度派生 | §8.3 派生关系；落点 `workspace-shell/main-section.tsx`（F2） |
| `--app-chat-font-size: 15px` + `--app-chat-font-delta` | `--dsh-content-font-size`（默认 14px）+ `--dsh-content-font-delta: calc(size − 14px)` | §13.4 `gradient-shadow-text.css`；默认值按 C4 取本地现状 15px（新增设置面板列非目标） |
| `--app-chat-font-size-secondary`（= size − 2px，基准 13px） | `--dsh-content-font-size-secondary`（默认 13px） | §13.4 同上 |
| `--app-chat-process-row-height: calc(24px + delta)` | 过程行行高 `calc(24px + delta)`（含 `contain: size layout`） | §13.4 `ReasoningRow.module.css` |
| `--app-chat-bubble-line-height: calc(22px + delta)` | 气泡行高成对 delta（该用户 delta=+2 时解析为 `16px/24px`） | `style-report-ref.md:8`（`div.gdEzaW_bubble`）+ C4 |
| 用户气泡形制：`rounded-[22px]` / `px-4 py-2.5` / 单层底色 / 无渐变 | 实测 `div.gdEzaW_bubble`：`padding: 10px 16px`、`border-radius: 22px`、`border: 0`、`max-width: 100%` | `style-report-ref.md:8 / 115 / 136`（三处一致） |
| 气泡底色走语义层 `--surface-strong`（= `--primitive-ink-600-a960`） | `--dsw-specific-bubble: #2c2c2e`（高亮态 `#43454a` 语义位另有 token） | `style-report-ref.md:182 / 357`（`--dsw-*` 令牌表）；语义位对应，**逐值等价性未做像素校验** |
| `--app-chat-turn-process`：33px（24 文本 + 8 下内距 + `0.5px` 底边，全仓唯一特例） | `TurnProcessNodeView` 回合统计折叠行（33px、`0.5px` 底边、二级色 + 16px chevron） | §13.4 `TurnProcessNodeView.tsx` + `.module.css` / C2、C3 |
| 消息节点样式承载在**元素自身**（无伪元素、无阴影、无 1px 边框） | 实测卡片外壳 = `div.overflow-hidden.rounded-[1rem].border` 自身（渐变底 + `1px` 边框 + 阴影第 5 槽） | §10.4(1) 结论 4（`pseudo-probe` 产物） |

> 未登记项说明：`--dsw-*` 其余令牌（颜色 / 圆角 / 阴影等）**不需要**逐条等价——本地已有一套三层 token 体系，映射只在「本方案引用了参考站机制」处登记，避免制造伪映射表。

---

## 13. 二审纠正台账与新增范围（2026-09-14 源码复核）

> **本节地位**：一审结论以「真实浏览器计算样式」为口径，但取样环境是**默认设置 + dpr=1**；二审补做了 `E:\projects\ai\deepseek-harness\packages\client\**` 源码标注，因此发现 5 处结论需要纠正、8 类 flow kind 未被覆盖。**与前文冲突时以本节为准**（前文相关位置已就地加注并回指本节）。

### 13.1 纠正 C1–C5

| ID | 一审结论（已作废） | 源码事实 | 影响与处置 |
|---|---|---|---|
| C1 | 「内容列固定 **748px**，`margin: 0 170px` 居中」 | 宽度是 `clamp(680px, 会话列宽 × 64%, 920px)`，且**用户拖拽偏好会整体替换该值**；748 是该视口下的解析值（Figma 值，源码注释说明实现刻意比 Figma 低一档） | §4.1/§8.2 已改；落地按 §8.2 新增 `--app-chat-content-width` 并派生三处宽度：转录列 = W、停靠卡 = W−32、输入卡 = **W+32**（批次 F2）。**任何位置不得硬写 748** |
| C2 | 「过程信息默认单行可见、只是压扁；本地统计式折叠是偏差方向」 | 参考站存在**回合级统计折叠控件** `turn-process`（「N 个工具调用 · M 条消息 · K 个子代理」，全 0 时「思考了一会儿」+ chevron），`processHidden = foldable && processMember && !open` 时**过程行整体不渲染**；行级 24px 压缩是展开后的形态。折叠需 `compact` 模式（默认）+ 回合已关闭 + `processWindowReady` | §6.2 已整节重写、D10 判断反向已修正。**本地统计语义本就正确**（`collapse.ts` 的 tools/replies/subagents ≈ 参考站三个计数），批次 F3 只换形制：33px 统计行 + 0.5px 底边 + `turn-process` 锚点，并补「回合内有新 user/steering 则不折叠」的例外 |
| C3 | 「参考站整页边框全为 1px，**无发丝**」 | 源码 **17 处**声明 `border-*.width: 0.5px`（`TurnProcessNodeView` 底边、`ConversationRoot.header` 底边、`MessageItem`、`QueueDock`、`HeroShell`、`TodoPanel`、`SidebarRoot` 等）；dpr=1 环境实测解析并绘制为 1px | 「1px 等价」在 **dpr=1 成立、dpr≥2 不成立**（0.5px 会画成 1 物理像素，视觉更细）。本方案**仍选 1px**（理由：本仓消息节点是文档流、非抬升面，且 dpr=1 下无差异），但表述改为「实测等价」；若后续要追 dpr≥2 观感，只允许在 `TurnProcessNodeView` 等价节点上用 `0.5px`，不动消息卡 |
| C4 | 「正文 `16px/28px`、气泡 `16px/24px`、行 `14/24`」 | 参考站正文尺寸是**用户设置项**：`--dsh-content-font-size`（默认 **14px**）+ `--dsh-content-font-delta: calc(size − 14px)`，次级轴默认 13px（`--dsh-content-font-size-secondary`）；行高/图标/行高盒/气泡行高**全部**写成 `calc(<基准> + delta)`；实测 `16/28`、`16/24` 是该用户 delta = +2 的解析值 | §8.2 改为「单一字号轴」；**实施禁止硬编码 16/28**（批次 F1 建立 `--app-chat-font-size`/`-delta`，默认值取本地现状 15/25.8 或 16/28 由 UX 定，但必须走 delta 机制）；不新增用户字号设置面板（列 §2.2 非目标） |
| C5 | 「flow 只有 5 类 kind」 | 参考站 `contract` 注册 **13 类**：`inbox` / `message` / `request-prompt` / `assistant` / `turn-process` / `tool` / `command` / `compaction` / `retry` / `turn-error` / `turn-max-tokens` / `turn-tail` / `fallback`；其中 `TURN_PROCESS_INDEPENDENT_KINDS` 明确排除 7 类不参与过程折叠 | §13.2 逐类给出处置；批次 F3–F5 覆盖可落地部分，本地无协议对应的 4 类**显式列为非目标**但保留 `fallback` 降级原则 |

### 13.2 未覆盖 kind 的逐类处置（13 类全景）

| 参考站 kind | 形制（源码） | 本地对应语义 | 处置 | 批次 |
|---|---|---|---|---|
| `message`（user） | 右对齐气泡 + hover 动作区 | 有（用户消息） | 已覆盖（§5.2/§8.5） | A3/C1 |
| `assistant` | Markdown 正文 + 内联推理 | 有 | 已覆盖（§5.4） | B2/D1 |
| `tool` | 24px 单行 + 富摘要 + 面板 | 有 | 已覆盖（§5.5） | B4/B5 |
| `turn-tail` | 28px 动作组 + hover 统计 | 有（`turn-usage`） | 已覆盖（§5.6） | C2 |
| `turn-process` | **回合统计折叠行**（33px） | 有语义、无控件 | **新增**（C2 纠正） | F3 |
| `system-prompt` | 独立行 kind，展开体同代码面板 | 与 context 混用 | **拆出独立 kind** | F4 |
| `steering` | 独立 kind，与 user 同形制（`:is(user, steering)` 共用气泡/动作区规则） | 无 | **投影层补 `steering`**（若协议无该事件则留空实现 + 单测） | F4 |
| `turn-error` | 13/20 单行 + 红点 + 加粗标题，非卡片 | 有（错误提示） | **收敛为单行** | F5 |
| `turn-max-tokens` | 单行，标题用 warn 色 | 有（截断提示） | **收敛为单行** | F5 |
| `retry` | 13/20 单行 + 折叠三角 + 进行中 shimmer | 无（重试是动作不产生行） | **可选项**：若本地重试产生过程事件则补行，否则只保留动效规范 | F5（可选） |
| `compaction` | 24px 单行 + hover 才出现展开图标 + 代码面板体 | 无（本地无压缩事件） | **非目标**（协议无对应；不预埋） | — |
| `inbox` / `request-prompt` / `command` | 各自独立渲染器 + i18n 文案 | 无 | **非目标**（本地协议无这些事件） | — |
| `fallback` | 未知类型兜底渲染（JsonBlock） | 无 | **只取原则**：未知 kind 必须可读降级、不得白屏（写入 §8.4 投影函数契约） | F4 |

### 13.3 未覆盖页面区域（组件已存在，规格未取证）

以下参考站组件在二审中被确认存在，但一审未取样、本方案**目前未给出规格**。处置原则：**先标记、后按需补测**，不在方案里凭类名推断数值。

| 区域 | 参考站组件 | 与本地关系 | 本次处置 |
|---|---|---|---|
| 输入区 | `InputBar` | 本地有输入卡（§8.3 已含宽度派生 W+32） | **已补测（2026-09-14）**：hero 态 `composerSeat` **1152×242** → `composerStack` **812×242**（`padding-bottom: 32px`、`gap: 8px`、无 bg / border / shadow）→ `textarea` **778×52**（`padding: 4px 12px 0 16px`、`16px/24px`、无边框）；与 §8.3「无卡片」一致，宽度关系随 F2（W+32）。**停靠态（会话内）未取样**：参考站侧栏仅有「新会话」、无历史会话可进入 → 记为待复测项，不阻塞 A–F |
| 空态 | `EmptyHero` / `HeroShell` | 本地有空态提示 | **后置（P2）维持**；已补测：hero 容器 **812×242** + `heroGlow` svg **1100×490**（纯装饰、无边框）+ `heroWorkspaceRow` **812×28**（`padding-left: 20px`、`gap: 2px`）；`HeroShell` 的 `0.5px` 底边归 C3 口径 |
| 输入队列 | `QueueDock` | 本地无排队输入的可见 UI | **非目标**：补测该状态下 **classHits=0**（未渲染）；本地复查 `frontend/src` 仅剩事件归约内部队列 / `queueMicrotask` / runtime-teams 任务队列（另一域），**消息区无排队语义**，故不预埋 |
| 上下文计量 | `ContextMeter` | 本地无 | **非目标**（补测 classHits=0；不在本次消息渲染范围） |
| 待办面板 | `TodoPanel` | 本地无 | **非目标**（补测 classHits=0；属工具面板域，另开方案） |

> 取证补测方式（**已于 2026-09-14 执行，F6**）：沿用 §3 的 Playwright 计算样式口径，新增 `frontend/.tmp/style-probe-input.mjs`（`style-probe.mjs` 本体未改，选择器扩展为上述五个区域；viewport 1440×900 / dpr=1），产物 `frontend/.tmp/style-probe-input-output.json`。**取样状态**：参考站 `http://127.0.0.1:3080/` 的「新会话」空态（侧栏无历史会话；`consoleErrors=0`）——上表数值仅代表该状态，会话内停靠态未取样（见输入区行）。未命中一律记 `classHits=0`，**不做类名推断**；本节已同步 §10.4。

### 13.4 二审证据索引（源码直读，均在 `E:\projects\ai\deepseek-harness\` 下）

| 文件 | 支撑的结论 |
|---|---|
| `packages/client/ui-chat/src/client/chat/TurnProcessNodeView.tsx` + `.module.css` | C2 回合级统计折叠行（文案/33px/chevron/0.5px 底边）、C3 发丝 |
| `packages/client/ui-chat/src/client/chat/ChatNodeSeat.tsx` | C2 `processHidden` / `processWindowReady` / 流式不折叠 |
| `packages/client/ui-chat/src/client/contract/turn-process.ts` | C5 `TURN_PROCESS_INDEPENDENT_KINDS`（不参与折叠的 7 类 kind） |
| `packages/client/ui-chat/src/chat-settings.ts` | C2 `TranscriptViewMode`（`normal \| compact`，默认 `compact`） |
| `packages/client/ui-chat/src/client/chat/ChatView.module.css` | C2 折叠态回答行间距收 8px（`--dsh-chat-flow-gap`） |
| `packages/client/ui-chat/src/client/chat/AssistantMarkdown.module.css` + `.tsx` | C4 正文排版/块间距；`.md-table-wide` 超宽表格、`.stopped`、`.actions` 偏移 |
| `packages/client/ui-chat/src/client/chat/MessageItem.module.css` | C1/C4 气泡比例限宽 `内容宽×0.702`、气泡行高走 delta |
| `packages/client/ui-chat/src/client/chat/ContextInjectionRow.module.css` | §5.3 补充规格（次级字号轴、展开代码面板 `max-height: 141px`） |
| `packages/client/ui-chat/src/client/chat/ReasoningRow.module.css` | §5.4 行高 `calc(24px + delta)`、`contain: size layout` |
| `packages/client/ui-theme/src/styles/gradient-shadow-text.css` | C4 字号轴定义（`--dsh-content-font-size` / `-delta` / 次级轴） |

> 说明：以上为**源码标注**（可信度：高，源码直读）；与一审的「浏览器计算样式」（可信度：高，但受默认设置/dpr 限制）冲突时，源码为准，并在 §13.1 记录差异原因。

## 附录 A：证据索引

| 文件 | 内容 |
|---|---|
| `frontend/.tmp/style-report-ref.md` | 参考站 `http://127.0.0.1:3080/` 逐节点计算样式（user / context / assistant-step / tool-call / turn-tail / flow-column）+ 317 条 `--dsw-*` 令牌 |
| `frontend/.tmp/style-report-local.md` | 本地会话页 `thread / article-0 / article-1 / thread-host` 计算样式 + 419 条本地变量 |
| `frontend/.tmp/style-probe.mjs`、`style-probe2.mjs` | 取证脚本（Playwright；可改造为 §10.1 的断言脚本） |
| `frontend/.tmp/style-probe-input.mjs` + `style-probe-input-output.json` | F6 §13.3 未取证区域补测（输入区 / 空态 / 排队 / 计量 / 待办；1440×900 / dpr=1，参考站新会话空态；含 `classHits` 命中统计与逐节点计算样式） |
| `frontend/.tmp/hairline-probe.mjs` + `hairline-probe-output.json` | 整页扫描「发丝边框 / `corner-shape` / 阴影分布 / 边框宽度直方图」（ref 1452 节点、local 819 节点，§10.4 结论 1、3、5 的数据来源） |
| `frontend/.tmp/hairline-render-probe.mjs` + `hairline-render-output.json` | **像素级**实测 `border: 1/0.5/0.25px` 与 `box-shadow` ring `0.5/1px` 的真实绘制厚度（dpr=1/2；截图经页面内 canvas `getImageData` 取像素，非目测） |
| `frontend/.tmp/pseudo-probe.mjs` + `pseudo-probe-output.json` | 卡片外壳承载节点定位（`article` 自身 + `::before`/`::after` 逐项比对）与整页 ring/发丝存量扫描（§10.4 结论 4、5） |
| `E:\projects\ai\deepseek-harness\packages\client\**`（源码直读，清单见 §13.4） | **二审证据源**：宽度轴、回合级折叠、字号轴、13 类 kind、0.5px 声明等（§13.1 C1–C5）；与计算样式冲突时以此为准 |

## 附录 B：参考站关键类名（便于复测定位）

| kind | 参考站类名 |
|---|---|
| 列 | `.Md3f7G_root` / `.Md3f7G_scroll` / `.Md3f7G_column` / `.Md3f7G_flowItem` |
| 行控件 | `._root_9cl6j_3` / `._row_9cl6j_10` / `._leading_9cl6j_23` / `._iconIdle_9cl6j_42` / `._title_9cl6j_64` |
| user | `.gdEzaW_userRow` / `.gdEzaW_userStack` / `.gdEzaW_bubble` / `._text_1pfhk_1` |
| 动作区 | `.p-xYUq_actions` / `.p-xYUq_action` / `.p-xYUq_timeStart` / `.p-xYUq_timeEnd` / `.p-xYUq_runTimeDot` |
| context | `.pC0e7a_sep` / `.pC0e7a_source` |
| assistant-step | `.Sxvs8a_root` / `.Sxvs8a_body` / `.QWLzlG_root` / `.QWLzlG_separator` / `.QWLzlG_summary` / `._markdown_1r4m5_5` |
| tool-call | `.ztWv_q_callRow` / `.o3BgMG_root` / `.o3BgMG_sep` / `.o3BgMG_summary` |
| turn-tail | `.osXY9a_root` |
| 会话槽位 | `[data-slot="conversation.session"]` / `[data-slot="conversation.view"]` / `[data-chat-flow]` |
| 二审新增 kind | `turn-process`（`TurnProcessNodeView.module.css`）/ `system-prompt` / `steering` / `turn-error` / `turn-max-tokens` / `retry` / `compaction` / `fallback` —— **哈希类名待 F6 补测回填**（本轮只做了语义与规格取证，未逐一采集类名；禁止按文件名推断哈希） |
