# Workspace 聊天界面简化与响应式导航实施方案

状态：**实施中**

日期：2026-07-27

负责范围：`frontend/src/components/workspace`、`frontend/src/i18n/resources`

## 1. 背景

`/workspace/chats` 会进入 `/workspace/chats/new`。当前页面虽然功能完整，但主要依靠说明文字解释页面、线程和 Runtime 状态，导致核心任务入口不够突出：

- 新建页同时出现多组“新建聊天 / 新线程 / 新会话”说明。
- 页面中央是实现说明卡片，Composer 反而处于次要位置。
- 可执行的提示建议被收进 Composer 的小型下拉菜单。
- Composer 持续展示正常状态、零文件数、重复模型名和提交提示。
- 空侧栏展示多个零数据区块及 Runtime 技术说明。
- 小于 `xl` 的视口会直接隐藏侧栏，没有会话导航替代入口。
- 顶栏在窄屏上控件密集，部分图标链接缺少明确的可访问名称。

## 2. 目标

1. 让输入任务成为新建页视觉中心。
2. 通过可点击任务建议说明 Agent 能力，而不是依赖实现说明。
3. 正常状态保持安静，仅主动呈现变化、异常和可执行操作。
4. 精简空侧栏和 Runtime 信息层级。
5. 在移动端提供完整的聊天与会话导航抽屉。
6. 为全部纯图标导航补充 `aria-label` 和 `title`。
7. 保持已有会话、模型选择、发送、停止、Artifact 和 Runtime 团队功能可用。

## 3. 非目标

- 不修改 Runtime API、会话恢复协议或线程数据模型。
- 不重做消息气泡、Artifact 详情和设置面板。
- 不改变发送快捷键及 Provider / Model 的业务行为。
- 不在本次引入新的 Drawer、Popover 或状态管理依赖。

## 4. 设计原则

### 4.1 一层只表达一种职责

- 顶栏：导航、当前会话名称和必要状态。
- 新建页：任务方向与开始入口。
- Composer：输入、模型选择、发送与异常反馈。
- 侧栏：历史聊天、可恢复会话及按需展开的 Runtime 入口。

### 4.2 用控件代替说明

新建页直接展示“分析项目、实现需求、审查代码、制定计划”等任务建议。点击建议后填充 Composer，用户无需先理解 route、mock、seeded 或 session attachment 等实现概念。

### 4.3 默认隐藏零信息状态

- 文件数量为零时不显示文件状态。
- 没有本地聊天时不显示空说明卡。
- 没有会话时不构造“默认用户 · 0”条目。
- Runtime 正常且无活动内容时保持折叠。
- 模型选择器已显示模型时，不再重复输出模型名称。

## 5. 实施方案

### 5.1 新建聊天页

文件：`frontend/src/components/workspace/workspace-shell.tsx`

- 删除新建页中央的实现说明面板。
- 保留一个简短任务标题。
- 增加四个本地化任务建议按钮：
  - 分析项目
  - 实现需求
  - 审查代码
  - 制定计划
- 点击建议仅填充输入框，不自动提交，避免意外执行。
- 新建页将建议区与 Composer 组合成单一主任务区域。

### 5.2 Composer

文件：`frontend/src/components/workspace/message-composer.tsx`

- 移除常驻的 transport / session / prompt tips 元数据栏。
- 提示建议从 Composer 下拉菜单迁移到新建页。
- 文件数量仅在大于零时显示。
- Runtime 错误、模型加载失败和响应中状态继续可见。
- Provider / Model 选择器保持原行为。
- 删除模型名称、快捷键和“提交”的重复常驻文字。
- 快捷键改为发送按钮 `title` 的补充提示。
- 新建页适当增加输入区域高度，强化输入优先级。

### 5.3 顶栏

文件：`frontend/src/components/workspace/workspace-shell-topbar.tsx`

- 新增移动端侧栏菜单按钮。
- 新建页只显示单行标题，不再显示重复副标题和状态 Badge。
- 导航动作统一为紧凑图标按钮，使用 `aria-label` / `title` 自解释。
- 保留日志、用量、Runtime、设置、新聊天和 Artifact 入口。
- 使用更小的窄屏间距，避免 390px 宽度溢出。

### 5.4 侧栏与移动端 Drawer

文件：`frontend/src/components/workspace/workspace-sidebar.tsx`

- 单个 Sidebar 组件同时承担桌面栏与移动端抽屉。
- 移动端抽屉具有遮罩、关闭按钮、Escape 关闭和导航后关闭行为。
- 桌面端保持 16rem 网格栏，不改变现有主布局。
- 没有历史项目时隐藏搜索框。
- 没有本地聊天且未搜索时隐藏本地聊天区块。
- 没有真实会话数据时不构造零会话默认用户。
- Runtime 区域默认折叠，空团队说明不再常驻显示。
- Runtime 错误、同步状态、实际团队及详情入口仍可按需访问。

### 5.5 i18n

文件：

- `frontend/src/i18n/resources/zh-CN.ts`
- `frontend/src/i18n/resources/en-US.ts`

处理内容：

- 删除 route、mock、seeded preview、状态附着等面向实现的首屏文案。
- 新增任务建议、移动导航、关闭导航及发送快捷键提示。
- 保留异常信息和有实际操作价值的状态文案。

## 6. 响应式行为

### 390px

- 顶栏显示侧栏入口、当前标题与紧凑图标动作。
- 侧栏以左侧模态抽屉出现，可由遮罩、关闭按钮或 Escape 关闭。
- 建议按钮使用双列网格，Composer 保持完整宽度。
- Provider / Model 工具栏允许换行，不横向溢出。

### 1024px

- 仍使用移动端抽屉，主内容独占可用宽度。
- 新建页建议区与 Composer 保持居中最大宽度。

### 1440px 及以上

- 左侧栏固定显示。
- 移动端菜单按钮隐藏。
- Artifact rail 的原有开关和三栏布局保持不变。

## 7. 可访问性要求

- 移动抽屉使用 `role="dialog"`、`aria-modal="true"` 和明确标签。
- 遮罩和关闭按钮均具有可访问名称。
- 所有只显示图标的顶栏链接 / 按钮具有 `aria-label` 与 `title`。
- 建议按钮使用真实 `button`，支持键盘聚焦与触发。
- 关闭的移动侧栏使用不可见状态，避免焦点进入屏幕外控件。

## 8. 验收标准

1. 新建页不出现 route、mock、seeded 或“附着运行时状态”等实现说明。
2. 首屏仅保留一个任务标题，Composer 是主要视觉控件。
3. 至少四个任务建议直接可见且点击后会填充输入框。
4. Composer 不再常驻显示“0 个文件”、新会话、重复模型名和提交文字。
5. Provider / Model、发送、停止和快捷键行为不变。
6. 空工作区不再显示本地聊天空说明或零会话默认用户。
7. Runtime 入口默认折叠，错误及实际活动信息仍可访问。
8. 390px、1024px、1440px 下均可访问聊天、会话、日志、用量、Runtime、设置和 Artifact 功能。
9. 所有纯图标动作都有可访问名称。
10. 前端测试、TypeScript 构建与 Vite 构建通过。

## 9. 验证计划

- 运行 `npm test`。
- 运行 `npm run build`。
- 运行 `npm run lint`，区分本次新增问题与仓库既有问题。
- 在 390×844、1024×768、1440×900 视口检查：
  - 新建页层级与无横向溢出。
  - 移动侧栏打开、关闭和选择导航。
  - 建议填充 Composer。
  - Provider / Model 和发送按钮可用。
  - 已有会话的消息列表与 Artifact rail 不回归。

## 10. 实施记录

### 2026-07-27

- 已完成新建聊天首屏、Composer、Topbar 和 Sidebar 的降噪；增加四个任务建议，并保留 Provider、Model、发送、停止及错误状态等实际操作能力。
- 已完成响应式导航：小于 `xl` 的视口使用模态抽屉，桌面视口继续显示固定侧栏；补充 Escape、导航后关闭、遮罩关闭和纯图标动作的可访问名称。
- 已补充中英文文案，以及 Composer、Topbar、移动 Sidebar 共 7 个针对性组件测试。
- `npm run build` 通过，TypeScript 与 Vite 生产构建成功。
- 针对性 Vitest 通过：3 个测试文件、7 个测试全部成功。
- 使用 Edge/Playwright 在 390×844、1024×768、1440×900 三个视口完成浏览器验收：标题与四个建议可见，建议可填充 Composer，无横向溢出，移动抽屉可打开并通过导航或 Escape 关闭，桌面固定侧栏可见，且未捕获页面或控制台错误。
- 全量 `npm test` 当前仍有 1 个与本次简化无关的既有失败：`message-markdown.test.tsx` 对流式未闭合 `ts` fence 的 Prism token 高亮断言未满足；其余测试通过。
- 全量 `npm run lint` 仍受仓库既有问题阻塞。对本次涉及文件单独运行 ESLint 后，仅剩两个原有 `react-hooks/set-state-in-effect` 报错，位于 `workspace-shell.tsx` 的 Artifact rail 设置同步和 `workspace-sidebar.tsx` 的会话目录状态同步；本次新增代码未产生新的 lint 报错。
