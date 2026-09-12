# frontend 前端功能与样式优化方案（参考 deepseek-harness）

状态：**草案（待评审）**；实施记录见 §9.3。

日期：2026-09-11

负责范围：`frontend/`（样式体系、工作台交互、设置域、i18n、测试基建）

参考材料：

- 目标项目特性分析：`E:\projects\ai\deepseek-harness\packages\client\*`（22 个 UI 包 + `apps/web` 入口）
- 目标项目设计体系 / 外壳 / 设置 / i18n / 质量保障分析（只读调研）
- 当前项目盘点：`frontend/`（只读调研，行号基于 2026-09-11 工作区快照）

---

## 1. 背景

当前 `frontend/` 已经具备可用的工作台闭环：会话列表 + 消息流（Markdown / 工具行 / 推理行 / 流式）+ artifact / checkpoint / plan 面板 + 轨迹视图 + runtime teams + 15 个后端配置域 + harness 设置。但整体「粗糙」体现在三个层面：

1. **功能域缺件**：缺少工具审批交互、运行时队列、附件与多模态输入、斜杠命令与 `@` 引用、deliverables 行、jobs 弹层、子代理目录、会话搜索、模型采样参数等 Agent UI 的常规能力（当前盘点 §6.3 已逐条确认「无 API、无 UI」）。
2. **样式体系未成型**：`globals.css` 是暗色优先的 690 行变量堆叠（220 条自定义属性），消费侧大量 `text-[var(--foreground)]` 式任意值；`landing.css` 是第二套独立 token 体系；设置卡片家族存在约 16 个近重复组件；硬编码圆角/间距散落。
3. **工程结构老化**：`backend-config-settings-page.tsx`(3932 行)、`workspace-sidebar.tsx`(1560 行)、`lib/workspace-thread-state.ts`(1815 行)、`types/runtime.ts`(1692 行)、i18n 双语资源各 2.5k 行手工同步；`src/**/.backups/` 残留；测试分布不均，无 a11y / 视觉 / 移动端验证，SSE 无自动重连。

`deepseek-harness` 的 Web 客户端（`packages/client/*`）在**同类问题上给出了成体系且可机械校验的答案**：三层设计 token、事件投影式消息管线、折叠谓词化、虚拟化与分页解耦、keyed 插槽式工具视图、schema 化设置与 staging 卡片、类型化 i18n 字典 + AST 硬编码门禁、以语义几何断言为主的 Playwright 质量基建。本方案的目标是把其中**高价值、低耦合**的部分移植到当前项目，而不是照搬其 cordis 插件架构。

---

## 2. 目标与非目标

### 2.1 目标

1. 建立**三层设计 token 体系**（primitive → semantic → 组合/皮肤），让主题/强调色/字号切换只依赖属性翻转与变量覆盖，消灭 `landing.css` 第二套体系与任意值扩散。
2. 把**消息渲染管线**从「行组件里散落 UI 状态」升级为「事件 → 中性节点 → 视图快照」的投影模型，落地可折叠 transcript、折叠谓词、system prompt 行、turn usage 行。
3. 补齐**输入面能力**：附件（拖放 / 粘贴 / 草稿轨 / 灯箱）、斜杠命令、`@` 引用、busy 队列坞与 steering 语义。
4. 补齐**阻塞式交互**：工具审批面板（含取消信号）与提问/计划评审卡，统一走可取消 pending 生命周期。
5. 对**设置域做结构治理**：超长页面拆分为注册表 + 按域懒加载；卡片家族收敛为统一 Card/Panel；引入「暂存 / 丢弃 / 保存」的草稿模型。
6. 建立**可机械执行的样式与 i18n 纪律**：语义 token 白名单、硬编码文案 AST 扫描、双语字典类型对齐。
7. 升级**质量基建**：dist fail-fast、空闲端口探测、失败截图、固定 locale/timezone、语义锚点 + 容差几何断言、golden 往返校验。
8. 以最小 API 增量补齐后端能力对接：**优先「前端接已有端点」**（就绪度矩阵见 §6.3 P2-1）；仅「队列/steering、附件上传、MCP 目录、Artifacts 统一接口、货币成本」五项保留后端前置依赖，其余能力不得以后端缺失为由延后。

### 2.2 非目标

- 不引入 cordis / 插件热插拔 / `ctx.slots` 体系，不复制 boot manifest、`__DSH_BOOT__`、profile 依赖镜像等宿主机制。
- 不引入 `ui-dockkit` 的 split-tree / float / dock 全量操作代数；右栏按当前需求做轻量停靠/全屏。
- 不重写数据层与轨迹 reducer（其乱序缓冲、幂等、终态冻结、序列化回放已有强测试，属于优质资产）。
- 不改变 Runtime HTTP/SSE 协议语义；新增 API 以前端消费为准，服务端缺失的能力进入「依赖项」而非强行前端模拟。
- 不回退、不重复建设既有资产：SSE 常驻重连/seq 续传、桌面通知、会话重命名/侧栏搜索、外观字体字号、轨迹搜索与导出、密度模式等已在库能力（清单见 §5.5）只允许增强或复用，禁止另起一套。
- 不引入账号体系、遥测上报、多标签状态广播、PWA 安装、自研 HMR 帧协议、全局命令面板、通用虚拟滚动基建与本地全文检索引擎（理由见 §8、§6.4）。
- 不照抄平台特定实现：`corner-shape: superellipse`、`0.5px` 发丝在 Chromium 的绘制行为、`::-webkit-scrollbar` 互斥门控需按目标浏览器重新测量。
- 不引入双语文档配对门禁（成本高、收益仅覆盖文档读者），只保留 UI 文案的 i18n 门禁。

---

## 3. 分析方法与证据口径

| 材料 | 范围 | 证据口径 |
|---|---|---|
| 目标项目功能域分析 | `packages/client/*` 22 个包 + 7 条优先交互管线 | 源码直读（标注路径:行），README 契约标注 `[README]` |
| 目标项目设计体系分析 | `ui-theme` / `ui-layout` / `ui-sidebar*` / `ui-dockkit` / `ui-settings*` / `locale` / `apps/web/tests` | 源码与 README 直读；含 e2e 契约清单 |
| 当前项目盘点 | `frontend/src/**`（排除 `.backups/`、`dist/`） | 源码直读；83 个 Vitest 文件 + 5 个 Playwright spec 统计 |
| 当前项目复核（Round 2） | 方案 §5/§6 的全部量化与定性断言 + 既有资产清单 | 逐项 grep/view 复核，产出 `.tmp/frontend-opt-analysis/round2-current-verification.md`；该报告第六节列出未取证项，未取证内容不得当作结论 |
| 目标项目功能点穷举（Round 2） | `apps/web` + `packages/client/*`：启动链路 / 布局侧栏 / 会话列表 / 设置 / 功能包 / 横向交互 | 源码直读 + 负向 grep，产出 `.tmp/frontend-opt-analysis/round2-target-shell-features.md`（含 TOP15、不采纳清单、证据缺口） |
| 后端就绪度审计（Round 2） | 路由全集（`handler.go:637-873`）× 事件常量 × 前端消费面 | 静态审计，产出 `.tmp/frontend-opt-analysis/round2-backend-readiness.md`；14 项能力逐条判定，未做运行时验证 |
| 证据缺口复核（Round 3） | Round 2 报告 C 节 C-1~C-7：侧栏装配 / 设置壳 / 负向结论 / `/export` / `apps/web` 构建 / 全局通知位 / 审批与运行时状态 | 源码直读 + 目录级 grep，产出 `.tmp/frontend-opt-analysis/round3-gap-a.md`、`round3-gap-b.md`；结论与未核验子项见 §11.6 |

> 行数与行号口径：本方案所有「N 行」均按含空行的编辑器口径（等价 `ReadAllLines`/`wc -l`），与 `view` 工具显示行号一致；用 `Measure-Object -Line` 等跳过空行的口径会偏小。

### 3.1 Round 2 复核修正记录（本版相对初版的实质变化）

| # | 初版断言 | 复核结论 | 本版处置 |
|---|---|---|---|
| R1 | P1-8「SSE 自动重连」需从零实现退避与游标回放 | 会话流**已有**常驻重连 + `seq` 游标续传 + 阈值退避；日志流亦有重连状态机 | 改写为 P1-8「连接状态统一与断线恢复收口」，禁止第二套退避（资产 A1/A2，§5.5） |
| R2 | 文件树 / 终端面板一并「不采纳」 | `fs/*` 端点**已就绪**且前端零消费；终端确无 PTY 接口 | 文件树改为采纳（新增 P2-10，只读 + 预览）；终端仍在 §8 不采纳 |
| R3 | 超长文件拆分清单仅列 6 个文件 | 实测 **41 个文件 > 500 行**；最大者 `backend-config-settings-page.tsx`(3932) 与 `workspace-sidebar.tsx`(1560) 未入清单，原「每文件 < 500」验收不可达 | P0-2 重写：首批 15 项 + 四批滚动 + 测试随源；§11.4 给出 41 项完整清单与批次归属 |
| R4 | 后端能力「大面积缺失」（审批/搜索/用量等） | 审批闭环、SSE 重放、子代理控制面、Skills 全族、会话元数据搜索、用量、文件读写**均已就绪**；硬前置收窄为 5 项 | §6.3 P2-1 重写为 A/B/C/D 四档就绪度矩阵；§10 风险表首行同步更新 |
| R5 | 卡片枚举数量与 P0-3 列举不一致 | 以源码枚举为准 | P0-3 固定为 16 个具名枚举 |
| R6 | 侧栏「无会话搜索、无重命名」 | 实为**已有**（会话名搜索 + 重命名） | 记为资产 A13；P1-9/P2-6 明确为「扩展」而非新建 |
| R7 | 「消息全量平铺」等定性描述 | 部分准确（context 卡已有折叠） | 表述收敛为「无 Turn 级折叠谓词」，避免以偏概全 |

> 未取证项（11 项，如 `maxSteps≤20` 具体 clamp、桌面通知触发条件等）仍以 `round2-current-verification.md` 第六节为准，未取证内容不得作为结论引用。

> 目标项目侧证据缺口：**Round 3 已 7/7 复核完毕**（不影响任务划分，仅影响实现细节取值）。关键修正：C-1 侧栏几何实际归 `ui-layout`（`columns.ts:13-19`），列表入口在 `ui-workspace` `WorkspaceBrowser`；C-2 设置 SHELL 属 `ui-settings-general`，契约槽位实为 8 个（非六类）；C-3 目录级复核确认多选/未读/置顶均不存在；C-4 `/export` 执行在 `session-log-export` 包（含子会话与附件、无内容级脱敏）；C-5 `apps/web` 为 `base:'./'`、无 proxy/HMR 配置、PWA 静态元数据齐备；C-6 全局通知位仅槽位声明、**零实现**；C-7 审批卡与 pending 注册表已核验，目标项目无审批超时态。完整证据与未核验子项见 **§11.6**。

---

## 4. 目标项目剖析（deepseek-harness）

### 4.1 架构分层（可作为「边界思想」参考）

- **22 个 UI 包按功能域切分**：`ui-chat`（transcript 投影 + 滚动）、`ui-conversation`（输入机、队列、装配管线）、`ui-trajectory`、`ui-tool`、`ui-primitives`、`ui-commands`、`ui-input-trigger`、`ui-attachment`、`ui-user-questions`、`ui-approval`、`ui-goal`、`ui-jobs`、`ui-subagent`、`ui-model-selection`、`ui-agent-preset`、`ui-skill`、`ui-message-feedback`、`ui-deliverables`、`ui-directory-picker-{browse,native}`、`ui-workflow-run`、`store`/`resources`。
- **契约层独立**：`src/client/contract/*.ts` 承担声明合并 map、Session Controller 类型派生（如 `QueueItemId`）、slots 契约；实现集中在 `{conversation,input,queue,skeleton}`。
- **权威边界原则**：Runtime/Host/Agent 拥有配对、生命周期、delivery window；客户端只做投影与乐观显示，权威记录到达时**原子替换**（本地输入立即显示、权威到达消失）。
- **不采纳部分**：`ctx.slots.register/provide/inject` 四 shares 体系、`ctx` 不进组件、业务包零 React context 等约束，仅在「多插件动态装配 + HMR」场景才划算；当前项目用路由 + 组合式组件 + 常规状态库即可。

### 4.2 核心交互管线（本方案主要移植来源）

**① 消息渲染（ui-chat / ui-conversation）**

- 位置层级 `ConversationLocation = session | turn | step | unresolved`，`Turn/Step.status ∈ open|closed|unknown`，各带 `start/end` 事件。
- Definition 把事件匹配成中性节点 `ConversationViewNode {key, kind, id, target, data}`，Chat/Trajectory 各自取快照。
- **折叠策略**（Compact 默认）：开启的 Turn 内 Context injection / reasoning / Assistant / Tool / Retry 保持展开，`turn/end` 后折叠；final-answer 边界 = 最新 Step 含非空文本/图片且**不含工具调用**；无 final answer 的 closed Turn 保全过程证据可见；折叠控件汇总 Turn 级持久计数（工具、带回复消息、子代理，零值省略、工具与子代理互斥），全零显示 `Thought for a while`。
- `system/message` 永不渲染为普通消息，而是请求前折叠的 `System prompt` 行；Turn token usage 行不完整即整行隐藏。
- **滚动所有权**：跨 prepend/remount 恢复语义锚点；贴底时 `ResizeObserver` 跟随新底线；离开底部后流高变化保顶、以 reading-line 几何选 active Turn。

**② Markdown 增量渲染（ui-primitives）**

- 前缀冻结：除尾部 `UNSTABLE_TAIL_BLOCKS = 2` 块外全部冻结，切割点取最后一个冻结块的 end offset，切片逐字一致；未闭合顶层 fence 走「最后完整行 + partial 行」第二前沿。
- 渲染 key = 块在全文中的绝对起始 offset（跨冻结边界稳定，React reconcile 而非 remount）；非追加输入使 `generation` 递增，调用方按代丢弃缓存。
- 引用式链接/脚注有已知偏差，最终 settled 全量解析自愈——**把偏差与自愈路径写进注释**是该项目的工程习惯。
- 高亮：单例 `IntersectionObserver` 文档级复用，元素首次相交即激活并永久离开观察集；`supportsHighlighting(lang)` 前置门。
- 安全默认：GFM + KaTeX，禁用 raw HTML、相对链接与不安全协议，仅绝对 HTTP(S) 图片直出。

**③ 工具行与工具专属视图（ui-tool）**

- `tool.call.toolview` 按 wire tool 名 keyed 注册；业务包只注册「工具名 → 组件」，**不配对事件、不重建 transcript、不拥有 root/subcall 拓扑**。
- 行状态机：`expandable = bodyRaw != null || outputText !== null || card !== null`；失败态**替换**摘要而非追加（`errorSummary`）；diff 折叠行携带 `+A -R`；失败时禁用文件链接。
- 嵌套可交互元素（文件链接）必须处理点击与键盘冒泡，否则行级展开吞掉链接；行根带 `data-variant/data-tool/data-state`，并以 visually-hidden 文本播报状态。

**④ Composer：触发器 / 命令 / 附件（ui-input-trigger / ui-commands / ui-attachment）**

- 光标处 `/`、`@` 打开分组候选菜单，支持键盘与指针、drill-down、单组 launcher；`aria-activedescendant` 高亮；Tab 作用于补全（`drill` vs `pick`），无高亮时原样放行保原生焦点遍历。
- 命令解析显式四分类：`leadingInput | popupSelect | action | execute`，**命令行永不静默降级为普通 prompt**；客户端与宿主命令同名「fail loudly」。
- 附件：有序草稿轨 + 全视口拖放邀请 + 消息图片按数量定尺寸 + 工具画廊 + 原图灯箱；数据/上传状态全部由 slot owner 提供，组件只渲染。

**⑤ 队列与 Steering（ui-conversation / ui-chat）**

- `resolveSubmitMode(preferred, running, gesture, steeringAvailable)`：非运行或不支持 steering → queue；否则 plain Enter/主发送按钮走用户偏好，Cmd/Ctrl chord 取反。
- steering 身份从事件溯源重建：重放 `agent/inbox/spliced`，只有 `next-step` 且 `source.kind === 'user'` 的 `user/message` 才算持久人类 steering。
- 队列帧是 wire 数据，**无 ref 的 image block 直接跳过**；缩略图加载失败保留空占位（读错误由耐久 transcript 呈现）。

**⑥ 审批 / 提问（ui-approval / ui-user-questions）**

- 统一生命周期：Remote Event waterfall 消费 → 注册 pending interaction → `await pending.result`；`PendingApproval(sessionId, {toolName, callId?, reason?, signal?})`，`delegate-on-remove` 保证会话取消/断开时安全收敛。
- 提问采用「单 entry 两 shape」：声明 `presentation intent` 的请求走自身 surface（如 `plan-review` 计划决策卡），其余走 generic question flow——刻意避免多 entry 竞争同一 carrier。

**⑦ 轨迹（ui-trajectory）**

- 纯投影：自有 Definition 从共享 Session 窗口组装业务记录，**既不读也不改 Chat 快照**。
- 分页 + 虚拟化解耦：初始取「挂载时 tail 结尾的 50 个目标 Node」，后续扩窗不驱逐前缀；虚拟化只挂载可见行 + 小 overscan；request-only 分隔符共享下一个可测量虚拟项；语义 key 与 ARIA index 跨 prepend 存活。
- 时间线总览：固定 Overview 投影真实 start/duration，Assistant span 拆 TTFT/decoding，hover 显示精确时钟，拖拽选中区间聚焦账本，wheel 缩放、右键清除/平移。

**⑧ 其余功能域速览（完整清单见目标项目分析报告 §2）**

| 域 | 关键契约 |
|---|---|
| ui-goal | composer 上下文栈第二卡；armed→pause、paused→resume；每个耐久 `/goal` run 渲染为右对齐命令气泡，重载可由 run 重建 |
| ui-jobs | 会话头动作 + 弹层；live 行按 `startedAt` 升序、settled 按 `finishedAt` 降序；elapsed 每秒 tick；只读投影、零 RPC |
| ui-subagent | 会话头 lineage 面包屑 + 后代目录；`{parentSessionId, childSessionId, mode}` 精确地址；独立 Stop；损坏行可读但 disabled |
| ui-model-selection | `/model` 弹窗与 composer 常驻座位共享同一「按 provider 分组」的每会话目录 |
| ui-agent-preset | 预设「创建即固定」，选择/默认只影响后续会话；broken 行保留定位/删除用于修复 |
| ui-skill | `/` 技能源与宿主命令冲突时宿主优先；落定 `Instructions` 卡内容不随目录漂移 |
| ui-message-feedback | Like/Dislike 为 log-only Session 事件，永不进入模型上下文；每 Session 单实例反馈面 |
| ui-deliverables | 回合结束 Files changed 行来自**成功文件变更工具结果**而非模型散文；CSS 容器查询实现响应式，无 JS 布局观测 |
| ui-workflow-run | run→phase→member 逐级展开；层级展开默认值按状态区分；可导航子会话需多重前置校验，宁 disabled 不误开 |
| store / resources | 无 React 快照存储；`dsh-resource://` 地址化读取，使「谁拥有数据」与「谁展示数据」分离 |

### 4.3 设计体系（ui-theme / ui-layout）

**三层 token**

| 层 | 内容 | 说明 |
|---|---|---|
| L0 基座 | `--dsw-font-family`、`--ds-font-family-code`、缓动与三档时长 | 组合型字体变量需要可解析；代码字体栈刻意不带裸 `monospace` 尾巴，避免 Windows CJK 回退 SimSun |
| L1 static 原色阶 | `--dsw-static-*`（amber/blue/deepseek/green/neutral/neutral-bluish/red，50–1000 步进） | 与主题无关；暗色段重复声明属导出噪音 |
| L2 semantic alias | `--dsw-alias-*`（bg/label/border/interactive/brand/button/state/markdown/code/tooltip/specific-*） | 亮色与暗色各一份；feature 组件**只允许消费这一层** |
| L3 组合与皮肤 | 字体五元组与简写、`--dsw-shadow-lv*`、`--dsw-elevation-*`、滚动条、高亮 | 每个字体角色同时给 `font-family/weight/line-height/font-size` 与 `font` 简写；字号与行高成对 |

**关键机制**

- **主题解析与应用分离**：解析发生在 `ui-theme`（light/dark/system → 不可变 `ThemeSnapshot`）；应用在 `ThemePresenter`：`colorScheme` → `documentElement.style.colorScheme`、`dark` → `body[data-ds-dark-theme]`、字号 → `body` 的 `--dsh-content-font-size`，其余 token 逐个写成 body 内联变量，并且**只回收自己写过的变量名**，`dispose()` 逆向撤销。绝不 `body.className = ...` 式覆写。
- **首屏防闪烁**：host 把已注册主题设置嵌进 index 响应，loading 页渲染前浏览器即设置 `color-scheme` / 深色属性 / 字号。
- **消费纪律**：feature 组件禁止复制 static 值、禁止写死颜色、禁止在 feature CSS 写主题选择器；新增共享 token 必须回到 owner sheet，且「primitive 步进 + semantic 别名」同批落地。
- **运行期约束**：内置 token 只覆盖一个白名单集合，超过即报错训练消费纪律；feature 样式表禁止 import token 实现文件。

**组件表现契约（可直接写进规范与 lint）**

- 浮层（菜单/popover/modal/panel/浮动按钮/composer）一律 `border:0` + 单条 elevation shadow；0.5px 发丝是 shadow 第一层，可用 `--dsw-elevation-stroke-color` 每表面重绑或抑制；**禁止** alias-border 与 elevation shadow 并用；状态色边框（如 warn 面板）保留真 border。
- 中性 border 一律 0.5px 发丝；满圆（50%/100%/pill）必须配对 `corner-shape: round`。
- 链接：`font-weight:500`、静止无下划线、hover 点状下划线。
- 滚动条全局单点拥有，暴露 `--scrollbar-thumb*` 变量供提升表面重绑；若同时支持 `scrollbar-width/color` 与 `::-webkit-scrollbar`，必须互斥门控。
- 外壳：`sidebar | center | rightbar` 三轨 grid；右栏是**轨道（track）不是盒子**，占位者上报 `shown/track/fullscreen`；列宽求解顺序为「右栏先缩 → 丢掉整条右轨 → 中间列才可跌破 400px」，左栏永不退让（由响应式折叠的有效偏好决定）；拖拽手柄用 pointer capture + 按帧合并 dx。

### 4.4 设置、表单与 i18n

- **schema 化设置服务**：`rehydrate()/validate()/nodeAtPath()/getPath()` + 不可变草稿克隆；设置 UI 本身也是槽位体系的一部分。
- **卡片模型（staging / discard / save）**：编辑先 staging，只有 save 写文档；discard 不触碰文档；reset 回落组合默认值；非数字草稿拒绝保存；错误在写入前给出。
- **Onboarding**：key 只写不读；configured 状态无需重启即可观测；已配置的重载绝不再画 takeover chrome。
- **i18n**：中文 key 集是**真源**，其它语言 `satisfies Record<Key,string>` 编译期对齐；namespace 按 feature 归属并做类型合并；`verify-client-ui-i18n.ts` 用 TS AST 扫描 JSX 文本与 copy 型属性（`alt/aria-*/label/placeholder/title/...`）拒绝硬编码，并设最小扫描文件数防止口径缩水；内部匹配用 discriminant / 稳定 id，绝不用本地化文本。

### 4.5 质量保障（apps/web/tests，约 108 个 e2e 文件）

- **组织形式**：vitest 组织 + Playwright(chromium) **直打构建产物**；`DIST_INDEX` 缺失即 fail-fast 提示先 build；`probeFreePort()` 申请空闲端口给真实服务；`scaffold.ts` 负责 seed session / host 配置 / 页面启动。
- **断言方法**：优先 `getByRole` + accessible name；状态锚点用 data 属性；几何断言带容差（原生 scroll 距离/锚点 top ±2px，响应式回流 ±32px）；断言「语义行位置、底部归属、真实滚动宿主」而非 DOM 基数。
- **golden 往返**：几何/可见性写成 `expected/**/*.expected.md`；测试同时校验「运行读取的 fixture 集合 == 提交的 fixture 集合」。
- **失败证据与环境控制**：失败写全页截图（gitignored `.artifacts/`）；固定视口、locale、`timezoneId`；页面已死也不能吞掉真正的断言错误。
- **契约清单覆盖面**：设置 chrome/主题/字号/语言持久化、右栏与 dock（含指针链、全屏、per-session 表面隔离）、轨迹虚拟化、长会话滚动契约（分页/流式/恢复/非滚轮/fling）、composer 几何与草稿滚动、模型设置、插件配置、Onboarding。

### 4.6 外壳与功能包（Round 2 补充，为本版新增任务的直接参照）

§4.1–§4.5 覆盖核心交互与设计/设置/质量体系；以下链路是本版新增任务（P1-9/P1-10、P2-6~P2-11）的参照，证据取自 `round2-target-shell-features.md`（TOP15 + 功能点表）。

| 领域 | 目标项目实现（证据） | 可移植要点 | 本方案任务 |
|---|---|---|---|
| 启动链路 | `web/src/boot-page.ts:17-45,68-70`（框架无关加载页 + 失败报告）、`boot-client.ts:41-60`（逐插件加载状态 + 完整性审计） | 失败可见性不依赖框架；「未完整加载不得进入可用态」 | P1-10 |
| 错误边界 | `ui-renderer/src/client/scoped-slots.tsx:325-333,672-682`（槽位级边界） | 全局/路由/面板三层；单面板崩溃不拖垮外壳 | P1-10 |
| 会话列表 | `ui-workspace/src/client/tree.ts:108,173,231`、`rows/WorkspaceBrowser.tsx:379-424,557-562`、`locales.ts:43-102`（状态/归档/删除/Fork/双排序/拖拽写回/搜索降级） | 排序模式正交切换；乐观更新 + 失败恢复；等待态指示 | P1-9、P2-6 |
| 命令系统 | `ui-commands/src/client/{PopupSelectView.tsx:50,service.ts:411-412}`（输入触发 + 命令服务 + 错误隔离） | 命令是「输入触发」而非全局面板；单命令失败不影响输入 | P2-7 |
| 交付物 | `ui-deliverables/src/client/locales.ts:7-45`（宿主动作 + 能力降级 + opening/error/retry） | 浏览器能力与宿主能力分层；无宿主则隐藏而非报错 | P2-8 |
| 任务与子代理 | `ui-jobs/src/client/locales.ts:4-8`（状态条）、`ui-subagent/src/client/locales.ts:7-60`（会话树 + 只读态解释） | 只读投影；「为何不能继续输入」必须说清 | P2-9 |
| 会话目标 | `ui-goal/src/client/locales.ts:4-16`（四相：进行中/暂停/完成/受限） | 状态机四相 + 目标文本；暂停/恢复/完成入口 | P2-9（后端暴露见 §6.3 P2-1B） |
| 文件树 | `ui-sidebar-files/src/client/locales.ts:22-50`（错误分类/截断/noWorkspace/reload） | 错误分类各有可行动文案；截断可见 | P2-10 |
| 右栏与资源 | `ui-sidebar-right/src/client/index.ts:17-21,41-44`（两段式注册）、`resources/src/client/contract.ts:35-70`（资源模型） | 标签注册表 + 统一「打开资源」 | P2-11 |
| 三栏几何 | `ui-layout/src/client/columns.ts:11-29,43-44`（1024 自动折叠 + 手动覆盖双状态） | 自动与手动两态分离、可记忆 | P2-3 |

> 未读/置顶/多选、全局命令面板、多窗口、桌面原生账号与遥测等按 §8 处理；目标项目侧证据缺口见 §3.1 注。

---

## 5. 当前项目 frontend 现状与差距

### 5.1 现状摘要（详见当前项目盘点报告）

- **技术栈**：React 19.2 + react-router-dom 7.13 + TS 5.9 + Vite 8 + Tailwind v4（`@tailwindcss/vite`）；Vitest 4.1 + jsdom；Playwright 1.62；react-markdown 10 + remark-gfm/breaks；PrismJS；recharts；i18next 26。
- **路由**：`/`（落地页）、`/logs`、`/usage*`、`/analytics`、`/runtime/config`、`/workspace/*`，全部 lazy + Suspense。
- **工作台**：`pages/workspace-page.tsx`(327) 纯组合层，向 `WorkspaceShell` 传 **68 个 props**（唯一属性名去重后仍为 68）；会话/线程/回溯/流式/轨迹恢复/团队/目录各有独立 hook。
- **数据层**：`api/runtime/*`（sessions/teams/config/agent-chat/harness/analytics/cache/logs/siteaccount）+ `types/runtime.ts`(1692)；SSE `consumeSseResponse` 带 AbortController 与 15s 软超时；错误统一 `RuntimeApiError` 与租约冲突映射；轨迹 `trajectory-reducer.ts`(831) 有乱序缓冲/幂等/终态冻结/seq 空洞推进与 golden 对拍。
- **样式**：`globals.css`(690 行、220 变量、暗色优先) + `landing.css`(26 变量、独立动效) 双体系；`src/components/ui/*` 基础件（cva/clsx/tailwind-merge）；设置卡片家族约 16 个近重复组件。
- **测试**：Vitest 83 个文件（设置 27、workspace 组件 13、hooks 12、lib/trajectory 8、lib 5、pages 5、api 3、其余 4）；Playwright 仅 5 个 spec / 17 个 test。
- **已具备、但此前方案未计入的资产**（Round 2 复核新增）：桌面通知（`use-workspace-agent-chat-turn.ts:674,1054`）、会话重命名与会话名搜索（`workspace-sidebar.tsx:620,724`）、外观字体/字号（`appearance-settings-page.tsx:495-519`）、轨迹搜索（`lib/trajectory/trajectory-search-index.ts`）、会话运行时流常驻重连（`hooks/workspace/use-session-runtime-stream.ts`）、日志流连接状态机（`hooks/use-runtime-logs.ts`）。保留要求见 §5.5。

### 5.2 功能域差距矩阵

| 功能域 | 当前 frontend | 参考做法（deepseek-harness） | 优先级 |
|---|---|---|---|
| 工具审批（approve/deny） | 无 UI；`approval_requested/resolved` 仅作轨迹单行文本（后端应答通道 `runtime/commands{approve_tool}` 与 pending 查询已就绪，见 §6.3） | 审批面板经 Remote waterfall 注册 pending interaction，带 `signal` 与 delegate-on-remove | P1 |
| 提问 / 计划评审 | 仅 plan mode 三选一面板（approve/request_changes/quit） | 单 entry 两 shape：`plan-review` 计划决策卡 + generic question flow，草稿存储与租约类型 | P1 |
| 消息折叠 | Turn 级无折叠（仅 context 卡内部可折叠）；无折叠谓词、无折叠摘要、无 System prompt 行、无 turn usage 行 | 终局边界谓词 + 计数摘要 + 稳定 seat；`system/message` 折叠行 | P1 |
| 流式 Markdown | `splitStreamingMarkdown` / 打字机逐字显示；每 chunk 语义未冻结 | 前缀冻结 + 尾块不稳 + fence 双前沿 + 绝对 offset key + 视口懒高亮 | P1 |
| 滚动所有权 | `useTypewriter` + 容器滚动；无锚点恢复契约 | 语义锚点恢复 + ResizeObserver 跟随 + reading-line 选 active Turn | P1 |
| Composer 输入面 | textarea + provider/model/reasoning 选择；无附件、无命令、无引用 | 输入机 + 块/装饰模型 + `/`、`@` 触发器 + 四分类命令派发 + 附件轨 | P1 |
| 队列 / steering | 无（后端亦无排队/插话实现，需新接口） | QueueDock + busy Enter 偏好 + 事件溯源重建 steering 身份 | P1 |
| 工具行 | `message-tool-row.tsx` 单组件；无工具专属卡、无失败替换摘要规则 | keyed `tool.call.toolview` + 状态机 + diff 统计 + visually-hidden 播报 | P1 |
| 轨迹 | 已有虚拟行/搜索/详情/reducer（较厚） | 纯投影 + tail 50 节点分页 + 扩窗不驱逐 + 时间线总览双向联动 | P2 |
| Artifacts | 面板数据来自消息富内容与 checkpoint diff；无 `artifacts` API（后端无 `/artifacts` 路由；MVP 可由 checkpoint list/files/preview + `fs/read-file` + metadata 聚合） | deliverables 行来自成功变更工具结果；正文内联文件链接 | P2 |
| 后台任务 | 无（后端 `/background/jobs` 五端点 + 四类事件已就绪，前端零消费） | 会话头动作 + jobs 弹层（只读投影、零 RPC） | P2 |
| 子代理 | runtime-teams 有队友/任务视图（后端 agents spawn/wait/events/input/close/resume + mailbox 已就绪） | 会话头 lineage 面包屑 + 后代目录 + 独立 Stop + `@` 引用源 | P2 |
| 技能 / MCP | 无（后端 skills 全族路由已就绪；MCP 仅 reload，无目录/schema 接口） | `/` 技能源；插件配置卡片 + 花名册 | P2（技能市场在 P2-1A 内交付；MCP 目录属 P2-1C） |
| 消息反馈 | 无 | Like/Dislike → 反馈弹窗；log-only 事件；本期只做命令入口（P2-7），不做独立反馈面板 | P2（收敛） |
| 模型选择 | provider/model/reasoning-effort 选择器 | `/model` 弹窗 + composer 常驻座位共享同一目录 | P2 |
| 模型采样参数 | 仅有 reasoningEffort + maxSteps（0=不限、clamp ≤20）；temperature/max_tokens 存在于配置文档但 `/models` 不暴露，`top_p` 后端无字段 | schema 化设置 + 暂存/校验后再写 | P2 |
| 会话搜索 / 分享 | 侧栏有会话名搜索 + 重命名；轨迹有增量倒排搜索与 JSONL/golden 导出；无跨会话内容检索（后端 `/sessions/search` 仅元数据过滤） | 选择/搜索覆盖可见窗口（轨迹面） | P2 |
| 成本 / 预算 | analytics 页有 token/缓存统计；后端 `/usage/*` 未消费；账本无 cost/价格字段 | turn usage 行 + inspector 用时/用量 | P2 |
| 文件树 / 终端 | workspace 目录注册/增删已具备（含目录/会话对话框）；后端 `fs/read-file|write-file|append-file` 未消费；无终端 | 目标项目有文件树（含错误分类/截断提示）与文档预览（PDF），无终端 | 文件树 P2（`fs/*` 已就绪）；终端 不采纳 |
| 键盘命令面板 | 无（目标项目亦无全局面板） | `/` 命令 + `@` 触发；内置 `/export` 等命令可作首批 | P2 |
| 连接状态 / 断线恢复 | 会话运行时流与日志流各自内建重连（§5.4），**缺**统一状态呈现、手动重试、直连 `/api/agent/chat` 流恢复 | 连接状态呈现件 + 重试入口 + 恢复幂等 | P1 |
| 会话列表组织与状态 | 侧栏有目录分组/重命名/会话名搜索；**无**运行/等待状态指示、无排序模式切换、无拖拽重排/跨组、无归档与 Fork | 行状态指示 / 归档 / 非破坏删除 / Fork → **P1-9**；分组视图 / 双排序 / 拖拽写回 / 组内折叠 → **P2-6** | P1-9 + P2-6 |
| 全局错误边界与启动失败面 | 无 ErrorBoundary/`componentDidCatch`；lazy chunk 失败白屏；`#root` 缺失无提示 | 全局 + 路由级 + 面板级三层边界；boot 失败报告 | P1 |
| 交付物（deliverables）呈现 | 无独立视图；变更文件信息散落在工具行（且行级字段未进事件白名单） | 回合结束 Files changed 行 + 宿主能力降级（预览/下载，无宿主则隐藏原生动作） | P2 |
| 可观测增补（子代理树 / 任务状态条 / traces） | runtime-teams 已有队友/任务视图；后台任务与 traces/diagnostics 端点前端零消费 | 子代理树 + jobs 状态条与弹层 + （可选）traces 查看器 | P2 |
| 会话目标（goal） | 无 UI；后端已有 goal 运行时工具（`aicli_goal` Get/Update）与四相状态（active/paused/complete/budget-limited），但未暴露 REST/快照；CLI 有 `/goal` | 目标指示（四相 + 目标文本）与暂停/恢复/完成入口；快照字段就绪前以轨迹只读派生 | P2 |
| 右栏面板架构 | 散装面板（artifact/checkpoint/详情直接挂载，资产 A7）；无标签注册表、无统一「打开资源」入口 | 标签注册表（显示/切换/全屏/tab 记忆）+ 统一资源打开入口 | P2 |

### 5.3 样式与设计系统差距

| 维度 | 当前 | 目标 | 优先级 |
|---|---|---|---|
| token 分层 | 单层变量堆叠（`:root` 暗色默认 + `html[data-theme=light]` 覆盖 + accent-tone 覆盖） | L1 static / L2 semantic / L3 组合三层，feature 只消费 semantic | P0 |
| 消费方式 | Tailwind 任意值 `text-[var(--foreground)]`、`[background:var(--workspace-shell-bg)]` | 语义主题类（`@theme` 内 `--color-*` 映射）或受限语义工具类 | P0 |
| 第二套体系 | `landing.css` 26 变量 + 独立 keyframes/类名 | 并入同一 token 体系，仅保留页面级布局类 | P0 |
| 组件复用 | 16 个近重复卡片组件；artifact/checkpoint 各自实现 | 统一 Card/Panel/Section 抽象 + 变体枚举 | P0 |
| 表现契约 | 无浮层/边框/链接/滚动条规范 | elevation-only 浮层、0.5px 发丝、滚动条单点拥有 | P1 |
| 主题应用 | `applyDocumentSettings` 写 `<html>` 属性，逻辑与主题解析耦合在 bootstrap | 解析/应用分离 + 幂等写入 + retraction set + dispose 逆操作 | P1 |
| 密度模式 | 已有 comfortable/compact（composer 与 shell 已接线） | 保留密度开关，token/字号治理不得写死单一密度 | P0（治理前置） |
| 硬编码值 | `rounded-[0.7rem]`、`rounded-[0.9rem]`、字号默认散落 | 圆角/字号收敛到 token 或局部组件契约 | P0 |
| a11y | 零散 `ariaLabel`；无 focus-trap 断言；`index.html lang="en"` | visually-hidden 播报、`aria-activedescendant`、ARIA index 跨 prepend 存活、lang 与语言同步 | P1 |

### 5.4 工程与质量差距

| 维度 | 当前 | 目标 | 优先级 |
|---|---|---|---|
| 超长文件 | **41 个文件 > 500 行**；Top：3932（backend-config-settings-page）/ 2595 / 2536（i18n 双语字典）/ 1815（workspace-thread-state）/ 1692（types/runtime）/ 1560（workspace-sidebar）/ 1358（provider-groups 域编辑器）/ 1337（agent-chat-turn hook）；完整清单见 §11.4 | 按域拆分 + 注册表 + 懒加载，分批推进至单文件 < 500 行 | P0（首批）→ P1/P2（后续批次） |
| 目录卫生 | **8 个 `.backups/` 目录、20 个 `.bak` 文件**（pages 6、components/workspace 5、i18n/resources 3、api/runtime 2、src 根与 components/ui、hooks/workspace、styles 各 1） | 清理并加 `.gitignore` 规则 / 禁止提交 | P0 |
| i18n | 双语文案单文件 2.5k 行手工同步 | zh 为 key 真源 + en 编译期 `satisfies`；AST 硬编码扫描 | P0 |
| 设置域测试 | 15 个域编辑器里 14 个无组件测试 | 每域至少 1 个组件/契约测试 + staging/save 行为测试 | P1 |
| e2e 覆盖 | 5 spec / 17 test，含调试用 `diag.spec.ts`；无 a11y/视觉/移动端 | 语义锚点 + 容差几何 + golden 往返；覆盖滚动/主题/composer/审批 | P1 |
| 测试基建 | 直接 launch dev/preview；无 dist fail-fast、无空闲端口探测、无失败截图 | 三件套 + 固定 locale/timezone | P0 |
| 断线恢复 | 会话流已有常驻重连 + seq 游标续传与阈值退避（`use-session-runtime-stream.ts:91-242`）；日志流有重连状态机与状态标签；**缺**统一连接状态 UI、手动重试入口、直连 `/api/agent/chat` 流恢复 | 连接状态可见化 + 手动重试 + 恢复幂等；不重复实现退避 | P1 |
| 类型组织 | 单一 `types/runtime.ts`(1692) | 按域拆分 + barrel re-export，保持对外 import 稳定 | P0 |
| 性能预算 | 无 | 长会话滚动 e2e + 增量渲染基准（可选） | P2 |

### 5.5 既有资产保留清单（不得重复建设或回退）

> Round 2 复核结论。P0/P1/P2 任何任务若与下表重叠，必须**复用/增强**现有实现，补幂等与回归断言；禁止并行实现第二套机制。未取证项（个别设置页交互细节）不在此列，见 `round2-current-verification.md` 第六节。

| # | 资产 | 证据（当前项目） | 对方案任务的约束 |
|---|---|---|---|
| A1 | 会话运行时流常驻重连 + seq 游标续传 + 退避阈值 | `hooks/workspace/use-session-runtime-stream.ts:91-242` | P1-8 仅做状态 UI/手动重试/直连 chat 流恢复；禁止另写退避循环（幂等断言：恢复不重复渲染已消费 delta） |
| A2 | 日志流重连状态机 + 连接状态标签 | `hooks/use-runtime-logs.ts`、`pages/logs-page.tsx:132-166,301-303` | P1-8 复用同一状态呈现与文案规范 |
| A3 | 轨迹搜索：增量倒排索引、AND 语义、节流重建、与筛选叠加 | `lib/trajectory/trajectory-search-index.ts`、`trajectory-view*.tsx` | P2-1「会话搜索」限定为**侧栏会话级 + 服务端内容检索**；不重写轨迹搜索 |
| A4 | 轨迹导出 / golden 对拍基建 | `lib/trajectory/{export,golden}.ts` 及测试 | P2-5 视觉 golden 前先盘点既有往返测试，避免重复建设 |
| A5 | 会话级 reasoning effort 选择（composer + 会话 context 投影） | `lib/reasoning-effort.ts`、composer 选择器（HEAD commit 即此功能） | P2-2 采样参数草稿模型不得回退该交互 |
| A6 | provider/model 目录同步与运行时模型目录 | `hooks/workspace/use-runtime-model-catalog.ts`、`lib/runtime-model-catalog-sync.ts`、`api/runtime/models.ts` | P2-1「模型目录扩展字段」在此基础增量，不新建目录源 |
| A7 | Artifact 三 surface（artifacts/checkpoints/plan）+ 详情对话框 + 消息元数据派生 | `components/workspace/artifact-panel*.tsx`、`lib/workspace-artifacts.ts`、`lib/workspace-thread-state.ts:1623-1696` | P2-1 新接口就绪前保留派生路径为降级/兼容层 |
| A8 | 回溯（message-backtrack + checkpoint 恢复） | `hooks/workspace/use-session-backtrack.ts`、checkpoint 恢复链路 | 折叠/投影改造须在回溯后强制重算（§10 风险同源） |
| A9 | Runtime teams 派发控制台/详情/console | `components/workspace/runtime-teams*`、`use-runtime-team-dispatch.ts` | P2-1 子代理 lineage **扩展**该视图而非替换 |
| A10 | workspace 目录注册/增删对话框 | `api/runtime/workspace-directories.ts`、目录对话框组件 | P2-3 窄屏改造须覆盖这两个对话框 |
| A11 | 设置页家族：外观/聊天/本地化/通知/关于 + harness | `components/workspace/settings/*-settings-page.tsx` | P2-2 治理范围覆盖**全部设置页**，不止 15 个 runtime 域 |
| A12 | 桌面通知（会话完成/失败，含可见性判断与 tag） | `hooks/workspace/use-workspace-agent-chat-turn.ts:674,1054,1187-1205` | 通知相关改造复用现有 `settings.notification` 通道与 `maybeShowDesktopNotification` |
| A13 | 会话重命名 + 侧栏会话名搜索 | `components/workspace/workspace-sidebar.tsx:620-637,724-737` | P1-9/P2-6 复用，不另做搜索框或重命名入口 |
| A14 | 外观字体族/字号（含代码字体、预览卡） | `components/workspace/settings/appearance-settings-page.tsx:436-519` | P0-4 token 治理须保留该能力与「字号仅影响内容」的语义 |
| A15 | 密度模式（comfortable/compact） | composer 与 shell 接线、`core/settings/local.ts` | P0-4 字号/间距治理不得写死单一密度 |
| A16 | 轨迹 reducer（乱序缓冲/幂等/终态冻结/seq 空洞） | `lib/trajectory/trajectory-reducer.ts` + 8 个相关测试 | 已列为非目标；P0-2 拆分清单中 `lib/trajectory/*` 不得拆除 |
| A17 | 会话历史同步/检查点/计划模式 hook 与 UI | `hooks/workspace/use-runtime-checkpoints.ts`、`use-workspace-history-sync.ts`、plan mode 面板 | P1-7 统一 pending 生命周期须兼容现有 plan 决策入口 |
| A18 | ui 基础件体系（cva/clsx/tailwind-merge） | `components/ui/*` | P0-3 卡片收敛基于该体系扩展 Card/Panel，不另起炉灶 |

> 资产对应关系：A1/A2 ↔ P1-8；A3/A4 ↔ P2-1/P2-5；A5/A6 ↔ P2-2/P2-1；A7/A8/A9 ↔ P2-1/§7.2；A10/A11 ↔ P2-3/P2-2；A12–A15 ↔ P0-4/P1-9；A16–A18 ↔ P0-2/P0-3。

### 5.6 Round 2 功能点处置总表（完整性口径）

> 口径：`.tmp/frontend-opt-analysis/round2-target-shell-features.md` 六节共 **89 个功能点**（1-x 外壳 15、2-x 布局/侧栏 15、3-x 会话列表 19、4-x 设置 12、5-x 运行时包 14、6-x 横向交互 14），本方案逐项给出处置，完整矩阵见 **§11.5**。

| 处置 | 数量 | 说明 |
|---|---|---|
| 落地（绑定任务 ID） | 63 | 每项都有 P0/P1/P2 任务承接，见 §11.5「本方案处置」列 |
| 部分采纳 | 2 | 2-13（文本预览走 P2-10；PDF/加密失败面暂缓）、5-14（只采纳订阅者错误隔离与浅比较纪律，见 §7.9） |
| 已具备（列入 §5.5 保留清单） | 8 | HTML 壳 `#root`（1-1）、应用内目录浏览对话框（2-14）、新建会话/新增工作区（3-6/3-7）、重命名（3-8）、设置触发入口（4-4）、模型设置分区（4-8）、Composer 模型选择器（5-10）；个别行附带的 P0/P2 任务仅作增量约束或「不得回退」声明，不改变归类 |
| 暂缓（本期不做，原因明确） | 6 | 1-14 浏览器鉴权、1-15 明文 HTTP 信任提示、3-14 定时任务指示、4-5 Onboarding 槽、5-5 工作流运行视图、5-11 会话级 Agent 预设 |
| 不采纳（进 §8） | 10 | 1-2 PWA、1-10 HMR 帧协议、1-11 fixture 开关、1-12 单页单视图、1-13 多标签同步、2-15 原生目录选择器、3-19 多选/未读/置顶、4-2 设置壳独立包、6-3 全局命令面板、6-12 遥测 |

> 结论：目标项目功能点**无悬空项**。新增任务的必要性来自「处置=落地」的 63 项与 §5.4 工程差距；「暂缓/不采纳」均有理由与替代方案，防止范围蔓延。

---

## 6. 优化实施方案

### 6.0 总体路线

分三阶段推进，每阶段可独立交付、独立验收：

| 阶段 | 主题 | 目标 | 预计周期 |
|---|---|---|---|
| P0 | 治理与底座 | 目录卫生、超长文件拆分、组件收敛、三层 token、i18n 真源、测试基建 | 1–2 周 |
| P1 | 核心交互对齐 | 消息渲染（折叠/增量/滚动）、Composer（附件/命令/引用）、队列与审批、工具行状态机、连接状态与断线恢复、会话列表状态、错误边界 | 2–3 周 |
| P2 | 功能补齐与工程化 | 后端就绪能力接入（jobs/subagent/usage/fs/skills）、列表组织与命令系统、交付物呈现、可观测增补、设置 schema 化、a11y 与移动端、性能与可靠性 | 3–6 周 |

实施约束：

- 每个任务先加测试再改行为；涉及样式的任务必须在 e2e 或组件测试中留下可验证证据。
- 新样式一律只消费 L2 semantic token；新增 token 必须按「primitive 步进 + semantic 别名」成对添加。
- 新增 UI 文案必须先落 `zh-CN` 真源并同步 `en-US`，否则不允许合入。

### 6.1 P0：治理与底座

#### P0-1 目录与构建卫生

- **内容**：删除全部 **8 个 `.backups/` 目录（20 个 `.bak` 文件）**；清理 `dist/` 外的构建残留；在 `.gitignore` 增加 `*.bak`、`.backups/` 规则；移除调试用 e2e（`diag.spec.ts` 改为按需运行的 `*.manual.ts`）。
- **涉及**：`frontend/src/.backups/`、`frontend/src/{api/runtime,components/ui,components/workspace,hooks/workspace,i18n/resources,pages,styles}/.backups/`（20 个 `.bak`：pages 6、components/workspace 5、i18n/resources 3、api/runtime 2，其余 4 个目录各 1）、`frontend/.gitignore`、`frontend/e2e/`。
- **验收**：`git status` 无 `.bak` 新增；`frontend/src` 下 `*.bak` 计数为 0（`Get-ChildItem -Recurse -Force -File frontend/src -Include *.bak | Measure-Object` = 0）；CI/本地 `npm run build`、`npm run test` 全绿。

#### P0-2 超长文件拆分（按域注册表化）

- **内容**：
  - `components/workspace/settings/backend-config-settings-page.tsx`(3932) → 页面壳 + 域注册表 + 15 个域编辑器目录（每域改为 dynamic import + 独立测试入口），共享 `config-domain-table/config-form-field/config-domain-dialog`。
  - `components/workspace/workspace-sidebar.tsx`(1560) → 侧栏容器 + 列表/分组/目录对话框/会话行组件；纯逻辑继续留在 `workspace-sidebar-shared.ts` 并补齐边界测试。
  - `lib/workspace-thread-state.ts`(1815) → 按职责拆为 `thread-state/{messages,deltas,queue,projection}.ts`，对外 barrel 保持既有 import 路径。
  - `types/runtime.ts`(1692) → `types/runtime/{sessions,checkpoints,backtrack,plans,teams,analytics,cache,harness,config}.ts` + `index.ts` re-export。
  - `hooks/workspace/use-workspace-agent-chat-turn.ts`(1337) → 拆分为发送编排 / SSE 消费 / delta 协调 / 目录同步 4 个 hook，组合于原 hook。
  - `components/workspace/settings/runtime-provider-groups-domain-editor.tsx`(1358) → **（原方案漏列，必须纳入）**：若不拆，本任务「单文件 < 500 行」验收不可能达成；作为域编辑器注册表化的首个迁移样本。
  - 首批同批纳入（> 700 行，共 15 项）：`runtime-provider-domain-editor.tsx`(1187)、`pages/usage-analytics-page.tsx`(974)、`pages/logs-page.tsx`(893)、`runtime-team-dispatch-panel.tsx`(846)、`lib/trajectory/trajectory-reducer.ts`(831，仅机械整理不改语义)、`runtime-team-details-panel.tsx`(812)、`runtime-agent-routing-domain-editor.tsx`(807)、`runtime-rate-limit-domain-editor.tsx`(770)、`hooks/workspace/use-session-backtrack.ts`(744)、`use-runtime-team-dispatch.ts`(743)、`message-list.tsx`(719)。
- **分批策略**：当前 `frontend/src` 共 **41 个文件 > 500 行**（39 非测试 + 2 测试，完整清单与批次归属见 §11.4）。本任务承诺**首批 15 项拆完并达标**；其余按四批滚动推进，每批随对应功能任务交付，避免一次性大爆炸改造：
  - **第 2 批（8 项，随 P2-2 收口）**：设置域编辑器残余（retry 715 / routing 565 / transformer 508 / provider-queue 504 / agent-routing-utils 514）+ appearance 596 / harness 568 + `backend-config-settings-page.tsx`(3932，**全仓最大文件**，按 section 拆分并复用域编辑器注册表；P0-2 期间即启动设计）。
  - **第 3 批（10 项，随 P2-7/P2-8/P2-9）**：`workspace-sidebar.tsx`(1560，第 3 批首位)、`workspace-shell.tsx`(695)、`runtime-teams.tsx`(693)、team-details(683)/shared(628)/dispatch-console(575)、`message-markdown.tsx`(625)、`artifact-panel.tsx`(618)、`artifact-panel-checkpoint-surface.tsx`(613)、`artifact-detail-dialog.tsx`(534)。
  - **第 4 批（4 项，随 P2-4/P2-10）**：`use-runtime-sessions-data.ts`(523)、`use-runtime-checkpoints.ts`(509)、`data/mock.ts`(539)、`cache-analytics-page.tsx`(518)。
  - **P0-6 承担（2 项）**：i18n 双语字典（2595/2536）按 namespace 拆分，必须与 P0-6 同批完成，不留给后期。
  - **测试随源（2 项）**：`workspace-thread-state.test.ts`(800)、`trajectory-reducer.test.ts`(537) 随对应源拆分同步切分，禁止只拆源不拆测试。
- **验收**：拆分后对外 API 不变（`tsc -b` 通过）；**首批 15 项内**每个新文件 < 500 行；相关既有测试不改断言即通过；41 项总量在 M4 前降至 ≤ 10（且余项均有排期与责任批次）。

#### P0-3 卡片/面板组件收敛

- **内容**：以现有 16 个设置卡片（`settings-choice-card / field-card / info-card / mini-card / mini-toggle-card / notice-card / panel-card / section / subsection-card / toggle-card / inline-toggle-card / empty-state / badge-list / action-group / add-button / dialog / dialog-footer / panel-icon`）为基础，抽取统一 `Card`（表面/密度/交互三轴变体）、`PanelSection`、`EmptyState`；用 codemod 或逐个替换；artifact/checkpoint surface 复用同一抽象。
- **落地要点**：变体用 cva 显式枚举，禁止透传任意 `className` 覆盖表面颜色；保留现有语义命名以免大范围改动测试快照。
- **验收**：近重复组件数量从 16 降到 ≤ 6（Card/PanelSection/EmptyState/Dialog/FieldRow/ActionGroup）；无视觉回归（截图对比或现有组件测试通过）。

#### P0-4 三层设计 token 体系

- **内容**：
  1. 在 `globals.css` 内建立三层：L1 `--primitive-*`（现有色板/hue 阶梯）、L2 `--color-*` 语义别名（bg/label/border/interactive/brand/button/state/markdown/code/tooltip/surface-*）、L3 组合（阴影 `--shadow-lv*`、浮层 `--elevation-*`、滚动条变量、字体角色 `--font-{s,xs,xxs}[-strong]`）。
  2. Tailwind v4 `@theme` 中把 L2 映射为语义颜色/圆角/字体工具类；feature 组件只允许语义类与语义工具。
  3. **`landing.css` 并入**：删除其 `:root` 26 变量与 light 覆盖，改用同层 token；仅保留页面布局类与 keyframes，动效时长引用统一 token。
  4. 收敛硬编码：`rounded-[0.7rem]`/`rounded-[0.9rem]`/字号默认值 → token 或组件内局部变量。
- **落地要点**：暗色仍可作为默认（保持现有 `applyDocumentSettings` 行为），但变量声明改为「亮色在 `:root`、暗色在 `html[data-theme="dark"]`」或反向保持一致；新增规则：feature 层禁止出现 `#hex`、`rgb()`、主题选择器。
- **验收**：`grep -c "var(--"` 的任意值用法下降 ≥ 70%；`landing.css` 变量数为 0；主题/强调色/字号切换后无颜色回归（e2e 覆盖三档主题 × 两种强调色）。

#### P0-5 主题解析/应用分离与首屏防闪烁

- **内容**：抽 `theme/{resolve.ts,present.ts}`：`resolve` 负责 light/dark/system + accentTone + 字体 + reduced-motion → 不可变 snapshot；`present` 只做 DOM 写入（`documentElement.style.colorScheme`、`html[data-theme]`、`html[data-accent-tone]`、CSS 变量内联覆盖），维护 retraction set，`dispose()` 逆操作；`index.html` 内联一段与 present 同源的启动脚本，消除首屏闪白/闪黑；`main.tsx` 改为调用 resolve+present。
- **验收**：DevTools 硬刷新（禁用缓存）无主题闪变；present 重复调用幂等；单测覆盖 resolve 矩阵与 present retraction。

#### P0-6 i18n 类型真源与硬编码门禁

- **内容**：把 `resources/zh-CN.ts` 拆为按 feature 的 namespace 模块并作为 key 真源；`en-US.ts` 用 `satisfies Record<ZhKey, string>` 编译期对齐；支持语言包按 namespace 动态 import；新增脚本 `scripts/verify-frontend-i18n.ts`（基于 TS AST + 现有 tsconfig）扫描 JSX 文本与 `aria-label/title/placeholder/alt/label/confirmLabel` 等 copy 型属性，白名单豁免 locale 文件与纯 token 常量，并设最小扫描文件数；**运行时回退链**：en 缺失键回退 zh、再回退 key 文本，dev 模式对回退发告警而非静默。
- **落地要点**：`index.html` 的 `lang` 改为与 `document.documentElement.lang` 同步（由 present 步骤写入）。
- **验收**：`npm run lint:i18n` 可执行且在存量清理后为 0 违规；删除 en 中任一 key 会导致 `tsc -b` 失败（编译期对齐生效）。

#### P0-7 测试基建三件套

- **内容**：Playwright 配置改为「构建产物 + 空闲端口」：缺失 `dist/index.html` 直接 fail-fast 提示先 `npm run build`；`probeFreePort()` 为 mock/preview 服务申请端口；失败用例写全页截图到 `.artifacts/`（gitignored）；固定 `locale`、`timezoneId`、视口种子；`mock-server.mjs` 与 `global-setup.ts` 收敛为 scaffold 工具，提供 seed 会话/主题/语言/历史的能力。
- **验收**：故意让一条 e2e 失败时产出截图与可读错误；e2e 在无 dist 时给出明确提示而不是白屏超时。

### 6.2 P1：核心交互对齐

#### P1-1 消息渲染投影模型与折叠策略

- **内容**：
  - 在 `lib/` 引入会话视图装配：把现有「消息 + delta + 工具 + 推理 + 富内容」的合并逻辑抽象为 Definition/Node 投影（`key/kind/id/target/data`），Chat 与 Trajectory 各自消费快照，**不新建第二份权威状态**。
  - 实现折叠谓词：Turn 结束后折叠过程证据；final-answer 边界 = 最新 Step 含非空文本/图片且不含工具调用；无 final answer 的 closed Turn 保全过程可见。
  - 折叠摘要行：汇总工具数/带回复消息数/子代理数，零值段省略，全零显示 `Thought for a while`。
  - `system/message` 渲染为请求前折叠的 `System prompt` 行；Turn token usage 行在用量不完整时整行隐藏。
  - 本地即时消息与权威记录**原子替换**（现有 `deltaCoordinator` 与轨迹投影的交接处需明确 ownership）。
- **涉及**：`components/workspace/message-list.tsx`、`message-*.tsx`、`lib/workspace-thread-state.ts`、`lib/trajectory/projection.ts`。
- **验收**：组件测试覆盖「工具调用后跟文本」「无 final answer」「纯工具 Turn」「重试行」四类折叠结果；同一 Turn 折叠后行数稳定，不因新消息到达重排。

#### P1-2 Markdown 增量渲染与视口懒高亮

- **内容**：以 `splitStreamingMarkdown`/`parseStreamingCodeFence` 为基础实现前缀冻结（尾 2 块不稳定）、未闭合 fence 双前沿、绝对 offset key、非追加输入 generation 递增；流式期不渲染可能过期的引用/图片 handler（settled 后再生效）；代码高亮改为 `IntersectionObserver` 懒激活（激活后停止观察），`supportsHighlighting` 前置；安全面保持「去 raw HTML、限链接、仅绝对 HTTP(S) 图片」。
- **验收**：性能用例（1 万字流式追加 N chunk）下重解析次数为常数上界（可用计数器断言）；流式长回复首屏帧率无肉眼卡顿；DOM 结构在 settled 后与全量解析一致。

#### P1-3 滚动所有权契约

- **内容**：会话滚动宿主改造为「语义锚点恢复 + 贴底跟随 + 离开底部保顶」：`ResizeObserver` 观察内容与输入座位高度（composer 悬浮时预留实时高度变量）；prepend 历史时按锚点行恢复位置；离开底部时以 reading-line 几何选 active Turn；为滚动契约写虚拟化中立的 e2e（断言语义行位置、底部归属、真实滚动宿主，±2px / ±32px 容差）。
- **验收**：e2e 覆盖「流式期间向上滚动不打断阅读」「加载历史前插后视口不跳」「工具流式增长时贴底跟随」三类场景。

#### P1-4 Composer 输入面升级

- **内容**：
  1. **附件**：file input + 粘贴 + 全视口拖放邀请；草稿附件轨（composer 下方）；消息图片按数量决定尺寸；图片灯箱（Escape/遮罩/关闭）；上传状态与数据 owner 化（hook 提供，组件只渲染）。
  2. **斜杠命令（输入面机制）**：`/` 菜单与 `+` 按钮同源；命令解析四分类（宿主 `leadingInput` / `popupSelect` / `action` / 默认 `execute`），**命令行永不静默降级为 prompt**；冲突 fail loudly。**边界**：触发/菜单/派发机制属本任务；具体内置命令清单与执行器属 P2-7，不在此重复定义。
  3. **`@` 引用**：光标处检测；分组候选（文件/会话/子代理）；键盘 + 指针 + drill-down；`aria-activedescendant`；Tab 在无高亮时原样放行。
  4. **草稿持久化**：按会话保存草稿到 localStorage；14 行封顶滚动 + 输入回焦行为对齐目标项目 e2e 契约。
- **涉及**：`components/workspace/message-composer.tsx`、新增 `composer/{menu,attachments,draft}.tsx`、`hooks/workspace/*`、`api/runtime/*`（附件上传依赖后端 `POST /runtime/uploads`，见 §6.3 P2-1C；接口就绪前仅本地预览 + 「待发送」标记，**不伪造已上传状态**）。
- **验收**：附件三段式（选择/粘贴/拖放）可用；命令不匹配时不发送原文；`/` 与 `@` 菜单键盘可达；草稿切换会话后恢复。

#### P1-5 队列与 Steering

- **内容**：`resolveSubmitMode(preferred, running, gesture, steeringAvailable)`：非运行或不支持 steering → queue；运行中 plain Enter/主发送按钮走用户偏好（设置项 `busyEnter`），Cmd/Ctrl chord 取反；队列坞位于 composer 上下文栈；队列帧无 ref 的图片直接跳过；缩略图加载失败保留空占位；steering 身份从事件流（splice → user/message）重建，刷新后可区分 queue 与 steer。
- **依赖**：需要运行时队列 API 与 `updateQueue` 能力（当前无，列入后端依赖）。
- **验收**：busy 状态下 Enter 行为可配置且持久化；队列项可删除/立即发送；断线重连后队列状态与权威一致。

#### P1-6 工具行状态机与工具专属视图

- **内容**：`message-tool-row.tsx` 重构为状态机：`expandable` 谓词、失败态替换摘要（不追加）、diff 折叠行携带 `+A -R`、失败禁用文件链接、嵌套链接处理点击与键盘冒泡、行根 `data-*` 属性 + visually-hidden 状态播报；注册表支持「工具名 → 专属卡」（read/diff/terminal/search/web/image/JSON），未注册走 generic 卡。
- **边界**：工具行内的单次 diff 统计属本任务（数据来自工具事件白名单，§6.3 P2-1B）；**回合级** Files changed 汇总行属 P2-8，两者不共用聚合逻辑。
- **验收**：工具行组件测试覆盖成功/失败/流式中/长输出四态；键盘 Tab 到文件链接可聚焦且不触发行展开。

#### P1-7 审批与提问交互

- **内容**：审批面板（approve/deny + 理由）与提问/计划评审卡统一为「pending interaction」生命周期：注册 → 可取消（`signal`）→ 结果回填；plan mode 现有三选一面板收敛为同一呈现位（计划评审 shape）；会话取消/断开时 delegate-on-remove 优雅收敛。
- **依赖**：后端**已就绪**（`approval_requested/resolved` 事件 + `runtime/commands{approve_tool}` + `GET /sessions/{id}/runtime` pending 查询，见 §6.3 P2-1A）；前端待补 UI。
- **验收**：审批卡出现/消失与会话生命周期一致；取消会话后 pending 卡不悬挂；拒绝审批后工具结果在轨迹与消息中一致呈现。

#### P1-8 连接状态统一与断线恢复收口（复用既有重连）

- **背景修正**：会话运行时流**已有**常驻重连 + `seq` 游标续传 + 阈值退避（`use-session-runtime-stream.ts:91-242`），日志流已有重连状态机与状态标签（`use-runtime-logs.ts`、`logs-page.tsx:132-166,301-303`）。原「从零实现退避」的表述作废。
- **内容**：① 抽取统一「连接状态」呈现件（connecting/reconnecting/online/offline + 手动重试），会话流与日志流共用；② 状态条接入工作台顶栏与消息流尾；③ 补齐**直连 `/api/agent/chat` 流**的失败恢复路径（本轮只核验了 thread 运行时流与日志流）；④ 手动重试与自动重连共用入口，重试前以本地 last seq 拉齐。
- **约束**：不得新增第二套退避循环；复用现有 `settings.notification` 与错误文案映射（§5.5 A1/A2）。
- **验收**：e2e/单测模拟断流 → 自动恢复；恢复期间不重复渲染已消费 delta（幂等断言）；手动重试不产生重复请求；直连 chat 流断线后可见状态并可恢复。

#### P1-9 会话列表状态与整理能力

- **内容**：侧栏会话行状态指示（运行中/子代理运行数/等待审批/计划待审/等待回答/已完成，等待类视觉最醒目）；归档与**非破坏删除**（移除引用/保留数据；删除工作区后其会话回落「未分组」而非连带删除）；归档恢复入口；会话 Fork；列表操作菜单补齐 Esc/方向键与 aria；行内相对时间（含「创建于」浮层）；空态区分「无会话/无匹配」。
- **依据**：目标项目 `ui-workspace/locales.ts:43-61`（状态/归档/删除语义、Fork）；当前侧栏已有重命名/会话名搜索/目录分组（§5.5 A13），本任务在其上扩展，不重做。
- **依赖**：状态数据源为会话快照 + 本地流状态合并；后端 `/sessions/{id}/archive|activate|close` 与 `/sessions/stats` 已就绪但未消费（§6.3）。
- **验收**：多会话并发时「哪个在等我」可一眼识别；归档后列表不出现且可恢复；Fork 后新会话命名与分组归属可预期。

#### P1-10 全局错误边界与加载失败面

- **内容**：全局 ErrorBoundary（当前 `frontend/src` 内无 ErrorBoundary/componentDidCatch，已核验）；lazy 路由 chunk 加载失败的重试面（**有次数上限与退避**）；`#root` 缺失/启动失败的可见提示（非白屏）；**启动完整性检查**——React 挂载后确认关键 provider/路由就绪，未就绪进入可见错误面而非「半加载」状态（目标项目 `boot-client.ts:54-60` 的单应用等价物）；错误统一走向 logger 出口。
- **依据**：目标项目 `web/src/boot-page.ts:68-70`（启动失败报告）、`web/src/boot-client.ts:54-60`（启动完整性审计）、`ui-renderer/src/client/scoped-slots.tsx:325-333`（槽位级错误边界）；当前项目为单应用，等价物是「全局 + 路由级 + 面板级」三层边界。
- **验收**：人为抛错不白屏且可恢复；chunk 404 显示重试而非死循环；错误边界有测试覆盖。

### 6.3 P2：功能补齐与工程化

#### P2-1 后端能力对接（就绪度驱动）

> 结论（Round 2 后端审计）：14 项能力中 **6 项直接可用、7 项需小改、4 项需新接口/新字段**（另 `top_p` 与货币成本单列）；同时有 **17 组端点**后端已暴露、前端零消费（免费收益）。原则：**先接既有端点，再谈新接口**。

**A. 直接可用（前端接线即可，优先做）**

| 能力 | 现有端点/事件（证据） | 前端交付 |
|---|---|---|
| 后台任务（Jobs） | `/background/jobs` 五端点 + `job_started/output/cancelled/finished` 事件（`background_handlers.go:15-210`；`handler.go:713-717`；输出走 REST `/output` 分页） | 会话头动作 + 弹层（live/settled、elapsed tick、Escape 回焦） |
| 子代理控制面 | `/sessions/{id}/agents`（spawn/wait/events/input/close/resume）+ mailbox（`handler.go:743-751`） | 会话头 lineage 面包屑 + 后代目录 + 独立 Stop（扩展既有 runtime-teams，§5.5 A9） |
| 审批闭环 | `approval_requested/resolved` 事件 + `runtime/commands{approve_tool}` + `GET /sessions/{id}/runtime` pending（`actor.go:3749-3825`；`session_runtime_handlers.go:770-783`） | P1-7 审批卡；处理 30min 超时的 `expired` 终态 |
| SSE 断线重放 | `runtime/stream?after=&poll_ms=` + `runtime/events?after_seq=&wait_ms=`（`session_runtime_stream.go:37-131`） | P1-8 复用；如需浏览器原生 `Last-Event-ID` 再小改 |
| Skills 全族 | list/get/create/update/delete/execute/search/stats/reload/validate/export/import/hot-reload（`handler.go:647-664,852-861`） | `/` 技能源、技能市场/热重载面板 |
| 会话元数据搜索 | `POST /sessions/search`（user/tags/state）+ `/sessions/stats`（`handler.go:725-729`） | P1-9 筛选搜索 + **空态降级与结果上限提示**（对齐 `ui-workspace/locales.ts:95-102`）；内容全文检索另议 |
| 用量 / 配额 | `/usage/stats|ledger`、`/usage/policy`、`/analytics/sessions/{id}/usage`（`:670-675,706-708`） | 成本/预算面板（token 口径，无货币成本） |
| 文件读写 | `POST /fs/read-file|write-file|append-file`（`:689-691`） | 文件树只读浏览 / Artifact 内容查看 |

**B. 需小改（后端字段/白名单，可并行排期）**

| 能力 | 缺口（证据） | 前端交付 |
|---|---|---|
| Deliverables 行级数据 | 工具事件白名单不含 `additions/removals/patch/mutated_paths/created_files/...`（`tool_runtime_events.go:478-532`，仅 `file_path`）；`apply_patch` 无行级统计 | 回合结束 Files changed 行（≤6 chip、`+N files`） |
| 插件配置 schema | 插件 DTO 无 `config_schema`（`harness_handlers.go:86-96`） | 设置花名册 + 配置卡片 |
| 模型采样参数 | `/models` 快照无采样字段；temperature/max_tokens 在配置文档内；`top_p` 无字段 | 设置域编辑器（草稿/校验/保存） |
| 设置域 schema | 无域级 typed schema（document sections 动态，`config_document_handlers.go:13-46`） | P2-2 草稿模型的前端映射层 |
| SSE 原生重连 | 无 `Last-Event-ID` 解析 | 仅当要求 EventSource 原生语义时补 |
| 会话目标（goal）暴露 | 后端有 goal 运行时工具（`aicli_goal`：Get/Update）与状态机（active/paused/complete/budget-limited，`session_goal_tools.go:23-52` + `internal/goal`），但**无 REST / runtime 快照字段** | 优先在 runtime 快照补 goal 字段；前端 MVP 可先解析轨迹中的 goal 工具结果做只读指示（P2-9） |

**C. 需新接口（保留后端前置依赖，UI 先以 pending/占位，不做假数据）**

| 能力 | 现状 | 建议接口形态 |
|---|---|---|
| 队列 / steering | 命令仅 submit/continue/approve/answer/interrupt/rewind；无排队实现 | `/runtime/queue` CRUD + `send-now` + `queue_updated` 事件 |
| 附件 / 多模态上传 | 无 upload 路由；chat messages 为 `[]map[string]string`；已有本地路径自动内联（`multimodal_input.go:150-171`） | `POST /api/runtime/uploads`（multipart）+ 消息 attachments/image block |
| MCP 目录 / schema | 仅 `POST /mcps/reload` | `GET /mcps`、`GET /mcps/{name}/schema` |
| Artifacts 一等公民 | 无 `/artifacts` 路由；MVP 可聚合（checkpoint + `fs/read-file` + metadata + generated-images） | `GET /sessions/{id}/artifacts`、`.../artifacts/{id}/content`、`artifact_created/updated` 事件 |
| 货币成本 | 账本无 cost/价格字段（`token_usage_history.go:148-159`） | 价格表 + cost 字段（或明确不做，仅 token 口径） |

**D. 免费收益（后端已暴露、前端零消费，17 组）**：`/capabilities`、`/status|health|events`、`/debug/prompt-layout`、`/traces/*`、`/mutation|auth|governance/policy`、`/sessions/search|stats|batch/*`、`/sessions/{id}/archive|activate|close`、`/runtime/tools`、`/runtime/tool-receipts`、`/mcps/reload`、`/teams/reload`、`/validate`、`/skills/*`、`/background/jobs/*`、`/fs/*`、`/agent-control/*`、`/supervision/*`、`/teams/{id}` 运维族。优先接：**Jobs、runtime/commands + runtime 状态、usage、sessions/search、fs/read-file、traces**。

**注意**：`/usage/*` 受 `authorizeUsageAdmin` 限制、`/observe/v1/*` 为条件注册，接入前先确认权限模型。

**落地拆包**：A 类各行前端交付可独立成 PR，建议顺序 **jobs → runtime 状态/commands → usage → sessions/search → fs/read-file → skills**；B 类后端小改与其行前端并行推进。本节 A/B/C 三档在全文（含 §4.6、§11.5）简记为 **P2-1A / P2-1B / P2-1C**。

#### P2-2 设置体系 schema 化

- **内容**：① 为 15 个后端配置域引入统一「staging 草稿 → 校验 → save/discard」模型（不可变草稿 + `validate()` 可读错误 + `discard` 不触文档 + `reset` 回落组合默认值）；② 设置读取收敛为**单一 describe 镜像 + 失效订阅**（文档一处缓存，保存后按域失效；禁止每页各自拉取/各自缓存）；③ 表单状态机显式化：`dirty / saving / readonly / overridden / invalid / reset` 均有可见表达（对齐目标项目 `ui-settings-plugins/src/client/locales.ts:4-39`）；④ **域编辑器注册表**：新增设置项只注册域描述符，不改设置壳（吸收目标项目槽位组思想，不照搬其槽位体系——Round 3 复核实为 8 个契约座位，见 §11.6 C-2）；⑤ 卡片基于统一 Card 抽象；每域至少一条组件/契约测试；⑥ 敏感输入（API Key/token）统一 `type="password"`，保存失败保留用户输入且不清空；General 增补语言切换行（回退链由 P0-6 提供）。
- **验收**：编辑不保存切走再回来不丢草稿或明确丢弃；非法值在写入前给出字段级错误；`runtime-config-diff` 继续可用；同一设置页多次进入只产生一次 describe 请求（失效订阅断言）；只读/被覆盖项有 readonly/overridden 表达而非静默失败。

#### P2-3 可访问性与响应式

- **内容**：对话框 focus-trap 与 Esc 语义；图标按钮补 `aria-label`/`title`；visually-hidden 状态播报；`aria-activedescendant` 菜单；`lang` 动态同步；**三栏几何双状态**：自动折叠（<1024px）与用户手动覆盖分离，手动展开不被 resize 重置且可记忆；窄屏导航抽屉（已有 responsive 测试）扩展为 sidebar/rightbar 双轨；长面板（teams/trajectory 详情）在窄屏转为全屏覆盖；触控目标 ≥ 32px。
- **验收**：新增 a11y e2e（键盘走查主流程 + focus 不逃逸对话框）；窄屏（<768px）可完成新建会话 → 发送 → 查看轨迹；手动覆盖状态在 resize 后保持（e2e 断言）。

#### P2-4 性能预算与长会话

- **内容**：长会话（≥2000 事件）滚动/切换性能基准；消息列表虚拟化或窗口化（如仍卡顿则引入）；轨迹已具备虚拟行，补齐「扩窗不驱逐 + ARIA index 跨 prepend」；Markdown 缓存命中率指标。
- **验收**：2000 事件会话下首次进入 ≤ 1.5s（本地构建产物）、滚动无 >100ms 长任务。

#### P2-5 视觉回归与 golden

- **内容**：选取 5–8 个高价值场景（工作台空态/消息流/折叠 Turn/工具行/审批卡/设置卡片）建立截图 golden 与 fixture 集合自校验。
- **验收**：golden 变更需显式更新并在 PR 中可见；fixture 集合不一致时测试失败。

#### P2-6 会话列表组织（分组 / 排序 / 拖拽）

- **内容**：分组视图（工作区分组 / 平铺）与排序双模式（手动 / 最近更新）正交切换；组内拖拽重排 + 跨组移动（先本地乐观、再 Host 写回、失败可见可恢复）；组内展开/折叠（Show more/less）；空白新会话在获得首条消息后自动提升。
- **依据**：目标项目 `ui-workspace/src/client/tree.ts:108,173,231`、`rows/WorkspaceBrowser.tsx:379-424,557-562,725,886-898`、`locales.ts:84-91`；当前侧栏仅有目录分组与固定排序（§5.2）。
- **依赖**：排序偏好本地持久化（`core/settings/local.ts`）；跨组移动复用 `workspace-directories` 已有归属字段。
- **验收**：两种排序模式切换不丢手动顺序；跨组拖拽失败回滚且提示；空态区分「无会话/无匹配」。

#### P2-7 命令系统（`/` 命令与内置动作）

- **内容**：composer 输入触发式 `/` 命令面板（焦点保持、方向键虚拟高亮、Enter 选择、Esc 关闭）；命令注册表 + 执行监听器错误隔离；首批内置命令：会话导出（复用轨迹 JSONL/golden 导出）、反馈入口、重命名会话。
- **依据**：目标项目 `ui-commands/src/client/{PopupSelectView.tsx:50,service.ts:411-412,locales.ts}`；当前无命令系统（§5.2）。
- **约束**：**不做**全局 Cmd+K 面板（§8）；与 P1-4 Composer 升级共用输入机。
- **验收**：命令面板键盘全流程可完成；单命令执行失败不影响其他命令与输入；导出产物可复现（golden 往返）。

#### P2-8 交付物呈现与能力降级

- **内容**：回合结束 Files changed 行（依赖 P2-1B 工具事件字段）；交付物卡片四态（运行中/已交付/失败/中断）；打开动作按宿主能力降级：浏览器内「预览」（走右栏/对话框）与下载；「在文件管理器中显示/默认应用打开」仅在有宿主能力时出现，否则隐藏而非报错。
- **依据**：目标项目 `ui-deliverables/src/client/locales.ts:4-45`（含 opening/opened/error/retry 三态与 nativeUnavailable 降级）；当前无 deliverables 视图（§5.2）。
- **验收**：无宿主能力时不出死按钮；打开失败可重试；`+N files` 数据来自工具结果而非猜测。

#### P2-9 可观测面板增补（子代理树 / 任务状态条 / 目标指示）

- **内容**：子代理会话树（可折叠、运行/非运行、只读态解释为「一次性记录/父会话离线」、耗时与 token 格式化统一）；后台任务常驻状态条（"{count} 个后台任务运行中"）与 jobs 弹层整合，避免与 toast 重复通知位；**会话目标（goal）指示**——四相状态（进行中/暂停/完成/预算受限）与目标文本，MVP 数据源为轨迹中的 goal 工具结果，后端快照字段就绪后切换权威源（不双写）。
- **依据**：目标项目 `ui-subagent/src/client/locales.ts:7-60`、`ui-jobs/src/client/locales.ts:4-8`、`ui-goal/src/client/locales.ts:4-16`；后端 jobs/subagent 能力已就绪（§6.3 P2-1A），goal 状态机证据 `session_goal_tools.go:23-52` + `internal/goal`（暴露方式见 §6.3 P2-1B）。
- **验收**：子代理树可展开到末级并显示只读原因；任务状态条与弹层计数一致；格式化函数单测覆盖。

#### P2-10 文件树只读浏览（基于已就绪的 `fs/*`）

- **内容**：按会话工作区浏览目录（loading/empty/truncated/noWorkspace 状态 + reload）；错误分类（不存在/越出工作区/非目录/暂不可用）各有可行动文案；文件内容预览接入右栏；路径显示统一家目录 `~` 缩写（对齐 6-14）。
- **依据**：后端 `POST /fs/read-file` 等已就绪（§6.3 P2-1A）；目标项目 `ui-sidebar-files/src/client/locales.ts:22-50`（错误分类/截断提示）。
- **约束**：**不采纳**终端面板（无 PTY 接口，§8）；目录越界属安全语义，文案不可弱化为普通失败。
- **验收**：大目录截断有提示；越界错误不可重试为普通失败；预览不阻塞侧栏。

#### P2-11 右栏标签注册表与资源打开

- **内容**：把右栏（artifact / checkpoint / 文件预览 / 交付物 / teams 详情）收敛为**标签注册表**：每个面板注册 `id / label / icon / 打开谓词 / 渲染器`，右栏壳统一处理显示、切换、全屏、「tab 记忆」与打开历史（后退/历史列表）；「打开资源」统一入口（消息内文件链接 / Files changed 行 / 文件树 → 右栏预览，必要时回退对话框）。
- **依据**：目标项目 `ui-sidebar-right/src/client/index.ts:17-21,41-44`（两段式注册）、`resources/src/client/contract.ts:35-70`（资源模型）；当前右栏为散装面板（资产 A7）。
- **约束**：不引入 dockkit 停靠代数（§8），只做注册表 + 轻量显示状态；不改变现有面板对外 props。
- **验收**：新增面板不改右栏壳代码（注册即生效）；同一资源重复打开不重复挂载；右栏宽度/折叠状态可记忆。

---

## 7. 关键设计决策（落地方案要点）

### 7.1 消息是投影，不是状态

**决策**：会话渲染的唯一权威仍是 runtime 事件与会话历史；前端只维护「投影 + 本地乐观输入」。本地 optimistic 消息与权威记录之间用**原子替换**（同 key 替换而非增量补丁），禁止双写漂移。

**理由**：目标项目在 Chat/Trajectory/Goal/Steering 四处均采用同一模式；当前项目 `deltaCoordinator` 已接近该模型，改造是收敛而非重写。

**代价与缓解**：投影表需要在 `workspace-thread-state.ts` 拆分时显式定义 ownership；新增单测覆盖「乐观消息被权威替换」「重复 delta 幂等」「乱序事件缓冲」三类。

### 7.2 折叠用谓词表达，不用组件内状态

**决策**：Turn 折叠与否由纯函数谓词决定（终局边界 + 证据计数），折叠摘要由持久计数生成，组件只渲染结果。

**理由**：当前所有消息平铺在 `message-list.tsx`，折叠若散落在行组件里会导致「折叠状态 vs 流式到达」竞态。谓词化后可单测、可复用（Chat 与 Trajectory 共享同一窗口数据）。

### 7.3 增量渲染的边界

**决策**：只对**追加式**流式文本启用前缀冻结；输入被截断/编辑（如用户回溯、重试）时递增 generation 并丢弃缓存。流式期不烘焙可能过期的 handler（引用/图片/链接），settled 后再解析。

**理由**：目标项目已把「已知偏差 + 自愈路径」写进注释；本方案要求在实现处同样写明偏差（引用式链接/脚注在冻结边界另一侧时的短暂字面渲染）。

### 7.4 滚动与输入座位

**决策**：滚动宿主唯一，composer 悬浮高度通过 CSS 变量参与滚动区域计算；贴底跟随与阅读保顶互斥切换；prepend 历史按锚点恢复。

**理由**：当前 `useTypewriter` 逐字显示 + 容器滚动在长会话下会与历史加载互相干扰；把滚动契约写成可执行 e2e 是最低成本防回归手段。

### 7.5 阻塞式交互的统一生命周期

**决策**：审批与提问共用「注册 pending → 持有取消信号 → 结果回填 → delegate-on-remove」生命周期；plan mode 评审作为提问的一种 shape 复用同一呈现位。

**理由**：当前只有 plan mode 三选一面板且与 artifact 面板耦合；统一后新增审批只需新增 shape，不新增并行机制。

### 7.6 样式唯一权威与消费纪律

**决策**：L2 语义 token 是唯一颜色/阴影/字体来源；feature 层禁止 hex/rgb、禁止主题选择器、禁止引用 L1 原色阶；新增 token 必须成对（primitive 步进 + semantic 别名）。

**理由**：当前 `globals.css` 单层变量 + 任意值消费，把「主题/强调色/落地页」耦合在三处；三层化后主题切换退化为属性翻转，落地页并入同体系。

### 7.7 组件表面契约

**决策**：浮层 elevation-only（禁用 border + shadow 并用）、中性 border 0.5px 发丝、滚动条单点拥有、满圆配对 corner-shape（若启用）。这些作为规范写入 `frontend/docs/styling.md`（新增），并尽量用样式测试或 lint 校验（如 CSS 中禁止 `border` 与 `elevation` 同时出现于同一浮层选择器）。

### 7.8 i18n 真源与门禁

**决策**：`zh-CN` 为 key 真源，其它语言编译期对齐；UI 文案必须走 `t()`；内部匹配用稳定 id/discriminant。门禁脚本随 CI 或 `npm run lint` 执行。

**理由**：当前双语文案 2.5k 行手工同步 + 无门禁，任何新增文案都可能单语漂移；类型对齐加 AST 扫描是成本最低的机械保障。

### 7.9 外部订阅契约（store 的最小纪律）

**决策**：不引入目标项目的自研 store；但任何对外暴露订阅的状态层（含 `useSyncExternalStore` 包装、事件总线）必须满足两条：① 单个订阅者回调抛错被捕获并 `console.error`，不影响其余订阅者；② 选择器结果浅比较后才通知，避免无关重渲染。

**理由**：这两条是 `store/src/index.ts:52-58,67-69` 与功能点 6-10（订阅者错误隔离）的可移植内核，成本低，可避免「一个面板抛错拖垮全局刷新」类故障。

---

## 8. 明确不采纳清单

| 项 | 原因 | 替代方案 |
|---|---|---|
| cordis `ctx.slots.register/provide/inject` 四 shares 体系 | 收益仅存在于「多插件动态装配 + HMR」；照搬会在 React 之上再造一套框架 | 路由 + 组合式组件 + 显式 props/context；仅保留「keyed 注册表」思想用于工具视图与命令 |
| `dsh.client` 清单 / boot manifest / `__DSH_BOOT__` / profile 依赖镜像 | 宿主插件体系基础设施，普通 Web 应用不需要 | 维持 Vite 构建 + 路由懒加载 |
| `ui-dockkit` split-tree/float/dock 全量操作代数 | 只有在产品真需要可停靠工作台时才划算 | 轻量右栏（显示/隐藏/全屏 + tab 记忆），吸收「纯函数 planner + settled intent 上报」思想 |
| `schema-form` 包 | 目标仓库快照仅剩 `lib/` 构建产物，无可移植源码 | 自建 `settings-draft`（不可变草稿 + validate）小模块 |
| 平台特定样式细节 | 依赖 Chromium 具体行为，需按目标浏览器重新测量 | 项目内以 token 表达，测量后定值 |
| 双语文档配对门禁 | 成本高，仅覆盖文档读者 | 只保留 UI 文案 i18n 门禁 |
| 终端面板（PTY / xterm） | 后端无 PTY 接口，目标项目 Web 端亦未提供 | 不进入本期；若未来需要，另立专项 |
| **文件树（修正）** | 原判「无后端接口」已过时：`POST /fs/read-file|write-file|append-file` 早已就绪、前端零消费（§6.3 P2-1A/§6.3 P2-10） | **改为采纳**：本期做只读浏览 + 内容预览；写入类动作不进 UI |
| 全局 Cmd+K 命令面板 | 目标项目无此设计；与 composer `/` 命令、轨迹搜索、会话搜索功能重叠 | 只做输入触发式 `/` 命令（P2-7）；全局入口留给后续独立评估 |
| 多标签 / 多窗口工作台（1-13） | 当前路由与状态模型不支撑，收益不明 | 单窗口 + 右栏切换；如需并行会话用 Fork/多会话列表 |
| 桌面原生动作（在文件管理器中显示 / 默认应用打开） | 浏览器环境无宿主能力，硬做会产出死按钮 | 按宿主能力探测降级：有则显示，无则隐藏（P2-8）；只保留预览/下载 |
| 未读标记 / 置顶 / 批量多选（3-19） | 目标项目（Round 3 目录级逐文件复核，关键词零命中，§11.6 C-3）与当前均无；需求未验证 | 不采纳（需求未验证）；P1-9 先做「等待态」这类有明确任务的指示 |
| 货币成本展示 | 后端账本无 cost/价格字段（`token_usage_history.go:148-159`） | 本期以 token/缓存口径呈现（P2-1A）；不做伪成本 |

**次级清单（Round 2 B 节及暂缓项，编号对应目标项目功能点）**

> 类别口径：本清单中**未标注类别者为不采纳**；**标注「暂缓」者为暂缓**（与 §5.6 计数、§11.5 处置一致）。

| 项 | 原因 | 替代方案 |
|---|---|---|
| PWA manifest / 可安装应用（1-2） | 本地工作台以标签常驻，安装收益低（目标项目静态元数据齐备，属产品取向取舍，§11.6 C-5） | 如未来需要离线/角标，另立专项 |
| HMR 帧协议（1-10） | 依赖目标项目自研 loader | Vite HMR |
| fixture 查询开关（1-11） | 生产误开风险高于收益 | e2e 用 route mock / seed |
| 单页单视图（1-12） | 当前已有 react-router 多路由，属能力而非负担 | 保留现有路由 |
| 多标签状态广播（1-13） | 会引入双写流与重复 SSE | 单窗口守护（见主表「多标签/多窗口」行） |
| 原生 OS 目录选择器（2-15） | 纯 Web 部署无法依赖宿主能力 | 应用内浏览对话框（已具备）为主，原生仅作可选增强 |
| 设置壳独立包（4-2） | 当前设置页结构可用，拆包无收益 | P2-2 在原结构内 schema 化与收敛 |
| 遥测 / 埋点（6-12） | 本地优先产品无接收方 | 结构化日志 + 诊断导出 |
| 浏览器鉴权 / 登录 UI（1-14，暂缓） | 本地/局域网部署无账号语义 | 仅按权限模型接入后端授权策略端点（§6.3 P2-1）；如需要只做 token/一次性链接页 |
| 明文 HTTP 信任提示（1-15，暂缓） | 属文案级增强，非阻塞 | 后续在连接设置补安全提示文案 |
| 定时任务指示（3-14，暂缓） | 后端无 scheduled task 证据 | 待核验后再定 |
| Onboarding 设置槽（4-5，暂缓） | 目标项目亦无完整流程；当前落地页已承担首屏引导 | 按留存数据再决策 |
| 工作流运行视图（5-5，暂缓） | workflowRun 阶段/成员模型后端证据不足 | 先统一状态词汇表（P2-9） |
| 会话级 Agent 预设切换（5-11，暂缓） | 当前为设置页路由配置；会话级座位缺后端预设版本 | 待 §6.3 P2-1B/C 评估 |
| 加密 PDF 预览（2-13 子项，暂缓） | 源产品自身不支持；成本高、需求低频 | 降级为下载/外部打开 |
| 全局虚拟滚动 / 本地全文索引 | 消息面未到瓶颈；本地索引有数据规模与迁移成本 | 窗口化优先（P2-4）；检索走服务端（P2-1A） |

---

## 9. 里程碑与验收标准

### 9.1 里程碑

| 里程碑 | 内容 | 验收标准 |
|---|---|---|
| M0（≈2 周） | P0-1 ~ P0-7 全部完成 | `npm run build` / `test` / `test:e2e` 全绿；无 `.bak`；`landing.css` 变量清零；`lint:i18n` 通过 |
| M1（≈4 周） | P1-1 ~ P1-4（渲染/滚动/Composer）+ P1-10（错误边界，纯外壳可并行） | 折叠谓词与滚动契约 e2e 通过；附件/命令/引用可用；增量渲染基准达标；抛错不白屏 |
| M2（≈6 周） | P1-5 ~ P1-9（队列/工具行/审批/连接状态/列表状态） | busy 队列与审批卡端到端可用；断流自动恢复且手动重试幂等；工具行四态测试通过；等待态可识别 |
| M3（≈10 周） | P2 首批：P2-1 A 类 + P2-6 / P2-7 / P2-9 | 新面板数据来自权威接口（无假数据）；Files changed 行来源为工具结果；jobs/subagent 面板可用 |
| M4（≈12 周） | P2-2 / P2-3 / P2-4 / P2-5 + P2-8 / P2-10 / P2-11 | 每域测试齐备；窄屏主流程可完成；长会话性能预算达标；右栏注册即生效；**超长文件（>500 行）降至 ≤ 10 且余项有排期** |

### 9.2 全局完成定义（DoD）

1. 新增/修改的交互均有组件测试或 e2e 覆盖，且断言写在语义层（角色/可访问名/可见文本/锚点），不依赖 DOM 数量与实现细节。
2. 新样式只消费 L2 semantic token；新增 token 有 primitive + semantic 两条。
3. 新文案有 zh/en 双份且通过类型对齐与门禁扫描。
4. 不引入新的重复卡片/重复对话框实现。
5. 相关文档（`frontend/docs/styling.md`、本方案）随实现同步更新。

### 9.3 实施记录

| 任务 | 状态 | 落地内容 | 验证证据 |
|---|---|---|---|
| P0-1 目录与构建卫生 | 已完成（2026-09-11） | 删除 `frontend/src/**/.backups/` 全部 8 个目录（20 个 `.bak`）；清理 `frontend/tmp/`（孤儿 `check-usage-page.mjs` 备份）与 `frontend/mock-out.log`/`mock-err.log` 残留；`frontend/.gitignore` 增补 `*.bak`、`.backups/`；`e2e/diag.spec.ts` → `e2e/diag.manual.ts`，新增 `playwright.manual.config.ts` 与 `npm run test:manual` 按需运行 | `frontend/src` 下 `*.bak`=0、`.backups/`=0；`git check-ignore` 命中新增规则；`npm run build` + `npm run test` 全绿（exit 0）；`npx playwright test --list` = 16 tests / 4 files（不含 diag），`npm run test:manual` 单跑 diag 通过 |
| P0-2 超长文件拆分（首批·第 1 项 `types/runtime.ts`） | 已完成（2026-09-11） | `types/runtime.ts`(1692) → `types/runtime/` 14 个域模块：`chat`149 / `sessions`102 / `checkpoints`82 / `backtrack`96 / `plans`50 / `events`10 / `logs`53 / `analytics`232 / `teams`354 / `config`99 / `service`26 / `harness`108 / `siteaccount`166 / `cache`152 + `index.ts` barrel（`export *`）。机械切片不改字段与导出名，`@/types/runtime` 对外路径不变；切片脚本留档 `.tmp/split-runtime-types.mjs` | `npx tsc -b --force` 0 错误；`npm run build` exit 0；`npm run test` 83 文件 / 459 用例全绿；14 个新模块 + barrel 全部 < 500 行（最大 `teams.ts` 354）；`frontend/src` 内 > 500 行文件数 41 → 40 |
| P0-2 超长文件拆分（首批·第 4 项 `lib/workspace-thread-state.ts` + 随源第 16 项测试） | 已完成（2026-09-11） | `workspace-thread-state.ts`(1815) → `lib/thread-state/` 10 个职责模块：`deltas`130 / `messages`111 / `generated-images`241 / `history-mapping`235 / `history-artifacts`296 / `events`411 / `tools`191 / `sessions`141 / `shared`60 / `text-utils`54 + `index.ts` barrel。跨模块复用的原私有 helper 就地补 `export` 但**不上浮**：`index.ts` 按拆分前导出名逐个 `export` / `export type`（新增对外导出 0）；`workspace-thread-state.ts` 退化为 2 行 barrel（`export * from "./thread-state"`），13 个消费方 import 路径零改动。随源测试 `workspace-thread-state.test.ts`(800) 按关注点切为 4 个同源测试：`history-projection`394 / `assistant-segments`215 / `runtime-events`99 / `deltas`85 + 共享 `test-fixtures.ts`(35)，断言零改动。模块划分与 §6.1 草拟名 `{messages,deltas,queue,projection}` 不一致——`queue` 在该文件中无对应实现，按实际职责落为上述 10 模块，验收仍按「职责拆分 + barrel 保接口」。脚本留档 `.tmp/split-thread-state.mjs`、`.tmp/split-thread-state-test.mjs` | `npx tsc -b --force` 0 错误；`npm run build` exit 0；`npm run test` 86 文件 / 459 用例全绿（切分前 83/459，仅文件数因测试切分 +3）；对外导出面对拍：拆分前 42 → 拆分后 42，missing=0 / extra=0；新模块与测试文件全部 < 500 行（最大 `events.ts` 411）；`frontend/src` 内 > 500 行文件数 40 → 38 |
| P0-2 超长文件拆分（首批·第 13 项 `lib/trajectory/trajectory-reducer.ts` + 随源第 34 项测试） | 已完成（2026-09-11） | `trajectory-reducer.ts`(831) → `lib/trajectory/trajectory-reducer/` 4 个层次模块 + `index.ts` barrel：`event-readers`230（载荷/seq/描述解析）/ `snapshot-ops`120（`TERMINAL_STATUSES` + clone/append/find/upsert）/ `apply`330（`applySequencedEvent`/`applyReasoningEvent`/`applyToolEvent`/finalize/freeze）/ `api`147（`removeItem`/`applyEvent`/`advanceSeqCursor`/`applyEvents`）。沿用「跨模块复用的原私有 helper 就地补 `export` 但不上浮」策略：`index.ts` 逐个转出拆分前 11 个公共导出（新增导出 0），`trajectory-reducer.ts` 退化为 2 行 barrel，消费方 import 路径零改动；语义零改动——乱序缓冲/幂等/终态冻结/seq 空洞推进按 A16 原样搬迁。随源测试 `trajectory-reducer.test.ts`(537) 按关注点切为 3 个同源测试：`sequence`174（基础序列/乱序缓冲/空洞续接）/ `state`212（幂等/终态/工具状态机/G7/error）/ `events`144（未知事件/批量重放/runtime 摘要）+ 共享 `trajectory-reducer.test-fixtures.ts`(29)，12 个 describe 整块搬迁、断言零改动。脚本留档 `.tmp/split-trajectory-reducer.mjs`、`.tmp/split-trajectory-reducer-test.mjs` | `npx tsc -b --force` 0 错误；`npm run build` exit 0；`npm run test` 88 文件 / 459 用例全绿（切分前 86/459，文件数 +2 因测试切分）；对外导出面对拍：拆分前 11 → 拆分后 11，missing=0 / extra=0；trajectory 目录定向 `npx vitest run src/lib/trajectory` 10 文件 / 86 用例全绿；拆分等价性校验 `.tmp/verify-trajectory-split.mjs`：28 个声明块 + 12 个 describe 块逐字节命中（仅允许 `export` 提升）→ EQUIVALENCE OK；新模块与测试文件全部 < 500 行（最大 `apply.ts` 330）；`frontend/src` 内 > 500 行文件数 38 → 36 |
| P0-2 超长文件拆分（首批·第 10 项 `pages/usage-analytics-page.tsx`） | 已完成（2026-09-11） | `usage-analytics-page.tsx`(974) → `pages/usage-analytics/` 4 个模块 + 40 行入口保留：`format`160（数字/百分比/时长/时间戳格式化、`readAdminToken`、`analyticsFilterKeys`、全部 i18n key/tone 映射器）/ `primitives`287（`emptyTotals`/`emptyCoverage`/`emptyDimensions` + `AnalyticsHeader`/`Metric`/`QualityNotice`/`FilterInput`/`FilterSelect`/`QualityBadge`/`TabButton`/`UsageViewTabs` + 图表 fallback）/ `sessions`294（`SessionTable`/`SessionDetail`/`SessionOverview`/`SessionTokens`/`Diagnostics`/`TurnTable`）/ `overview`201（`UsageOverview` + 懒加载 `UsageAnalyticsCharts`）；入口保留 `UsageAnalyticsPage` + 懒加载 `CacheAnalyticsView` + `CacheViewFallback`。沿用「跨模块复用的原私有 helper 就地补 `export` 但不上浮」：对外公共面仍是 `UsageAnalyticsPage` 单一导出（missing=0 / extra=0），3 个消费方（`App.tsx` 路由 + 2 个测试）import 路径与具名导入零改动；模块依赖单向无环（format ← primitives ← sessions ← overview ← page）。脚本留档 `.tmp/split-usage-analytics.mjs` | `npx tsc -b --force` 0 错误；`npm run build` exit 0；`npm run test` 88 文件 / 459 用例全绿；定向 `npx vitest run src/pages/{usage-analytics-page,cache-analytics-page}.test.tsx` 2 文件 / 8 用例全绿；拆分等价性校验 `.tmp/verify-split.mjs`：44 个声明块逐字节命中 → EQUIVALENCE OK；新模块全部 < 500 行（最大 `sessions.tsx` 294）；`frontend/src` 内 > 500 行文件数 36 → 35 |
| P0-2 超长文件拆分（首批·第 8 项 `hooks/workspace/use-workspace-agent-chat-turn.ts`） | 已完成（2026-09-11） | `use-workspace-agent-chat-turn.ts`(1337) → 入口 446 行 + `hooks/workspace/agent-chat-turn/` 10 个模块：`stream-handlers`394（SSE 消费：meta/chunk/reasoning/tool/planning/orchestration/route/observation/subagent/result/done/error 回调聚合为 `createAgentChatStreamHandlers`）/ `finalize-turn`249（终态收敛：文本与推理合并、最终产物生成、线程消息段写回、DEV 双跑校验、完成通知）/ `use-reasoning-effort`152（档位子 hook）/ `streaming-writers`144（工具段 upsert / 工具结束 / 阶段切换 / 流式错误落盘）/ `final-artifacts`113 / `streaming-frame`103（rAF 批处理 + 可见性监听）/ `turn-state`51（可变状态容器 `ChatTurnRuntimeState`）/ `notifications`37 / `thread-factory`29 / `shared`23。沿用「机械搬迁、语义零改动」：handler 闭包按原顺序逐字节搬迁，仅以 `turnState.X` 前缀与 deps 注入改写；入口保留发送编排（`submitPrompt`、连接超时、abort/finally 收口）与各工厂装配；跨模块复用的原私有 helper 就地补 `export` 但不上浮，对外导出面 3 → 3（missing=0 / extra=0），`workspace-page` 等消费方 import 路径零改动。脚本留档 `.tmp/split-agent-chat-turn-{4,5,6}.mjs`；等价性校验 `.tmp/verify-agent-chat-turn-step{4,5,6}.mjs`：回调块逐字节（14/14）、入口非 import 行逐行（changed=0）、字符串字面量多重集（missing=0 / extra=0）→ 三步 EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npm run build` exit 0；`npm run test` 88 文件 / 459 用例全绿；定向 `npx vitest run src/hooks/workspace/use-workspace-agent-chat-turn.test.ts src/pages/workspace-page.test.tsx` 全绿；新模块全部 < 500 行（最大 `stream-handlers.ts` 394），入口 1337 → 446；`frontend/src` 内 > 500 行文件数 35 → 34 |
| P0-2 超长文件拆分（首批·第 7 项 `components/workspace/settings/runtime-provider-groups-domain-editor.tsx`） | 已完成（2026-09-11） | `runtime-provider-groups-domain-editor.tsx`(1358) → 入口 149 行 + `runtime-provider-groups-domain-editor/` 6 个模块：`provider-group-members-section`423（members 区块：memberSummary/missingRoleCount/weightedMissingWeightCount 记忆化 + 自动补角色/权重 + 成员表）/ `draft-utils`309（KNOWN keys、5 组 option 常量 + 草稿构造/校验定位/选项构建/成员汇总 13 个纯函数）/ `provider-group-basic-fields`271（基础字段网格 + failover/truncation 面板）/ `provider-groups-table`267（域表：摘要徽标、成员引用徽标、行内复制/编辑/删除）/ `provider-group-dialog`127（对话框外壳 + 校验提示 + 组合）/ `field-parts`48（ProviderReferenceBadge / FieldIssueText）。沿用「机械搬迁 + props 化外壳」：15 个区间按行搬迁（含 state/memo/handler），入口仅保留 state、2 个 memo、4 个 handler 与两个子组件装配；跨模块复用的原私有 helper 就地补 `export` 但不上浮（入口对外仍只导出 `RuntimeProviderGroupsDomainEditor`，before=after）；表格块 3 个回调名（handleCopyGroup/openEditDialog/openCreateDialog）随 props 改名，dialog 外壳 4 处（open/onClose×2/onConfirm）props 化；`setDraft` 直接下传以保证原 JSX 字节不动。脚本留档 `.tmp/split-provider-groups-1.mjs`；等价性校验 `.tmp/verify-provider-groups-split.mjs`：15 个区间逐行命中（仅允许 `export` 提升与上述改名）→ EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npm run build` exit 0（2841 modules transformed）；`npm run test` 88 文件 / 459 用例全绿；定向 `npx vitest run src/components/workspace/settings/runtime-provider-groups-domain-editor.test.ts` 6 用例全绿；新模块全部 < 500 行（最大 `provider-group-members-section.tsx` 423），入口 1358 → 149；`frontend/src` 内 > 500 行文件数 34 → 33 |
| P0-2 超长文件拆分（首批·第 9 项 `components/workspace/settings/runtime-provider-domain-editor.tsx`） | 已完成（2026-09-11） | `runtime-provider-domain-editor.tsx`(1187) → 入口 349 行 + `runtime-provider-domain-editor/` 6 个模块：`provider-table`243（域表：摘要徽标 + 6 列（名称/协议/默认模型/站点余额/状态/行操作）+ 搜索与分页）/ `provider-account-section`235（账号面板：site_type/account_auth_ref/凭据字段 + 探测/抓取/刷新按钮 + 结果提示）/ `draft-utils`164（KNOWN keys、`AccountAction`、协议选项、搜索文本、草稿构造、错误描述）/ `provider-basic-fields`162（基础字段网格 + supported_models/support_types + api_key）/ `provider-dialog`141（对话框外壳 + 错误提示 + 三个区块组合 + extraJson + enable/setAsDefault 开关）/ `provider-network-fields`133（headers/model_mappings + proxy 面板）。沿用「机械搬迁 + props 化外壳」：14 个区间按行搬迁；跨模块复用的原私有 helper 就地补 `export` 但不上浮（入口对外仍只导出 `RuntimeProviderDomainEditor`，before=after）；表格 4 个回调名（openCreateDialog/openEditDialog/handleCopyProvider/handleRefreshProviderAccount）与账号区块 3 个回调名随 props 改名，dialog 外壳 4 处（open/onClose×2/onConfirm）props 化；`draft`/`setDraft` 直接下传以保证原 JSX 字节不动。脚本留档 `.tmp/split-provider-domain-1.mjs`；等价性校验 `.tmp/verify-provider-split.mjs`：14 个区间逐行命中（仅允许 `export` 提升与上述改名）→ EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npm run build` exit 0（813ms）；`npm run test` 88 文件 / 459 用例全绿；定向 `npx vitest run src/components/workspace/settings/runtime-provider-domain-editor.test.ts` 6 用例全绿；新模块全部 < 500 行（最大 `provider-table.tsx` 243），入口 1187 → 349；`frontend/src` 内 > 500 行文件数 33 → 32 |
| P0-2 超长文件拆分（首批·第 11 项 `pages/logs-page.tsx`） | 已完成（2026-09-11） | `logs-page.tsx`(893) → 入口 365 行 + `pages/logs-page/` 5 个模块：`logs-header`277（页头：状态徽标/导航/搜索+级别+口令过滤栏/刷新/复制视图链接/跟随开关/文件状态行/活动 chips）/ `logs-list-panel`179（左列表面板：级别统计筛选徽标 + 列头 + loading/error/empty/列表四态）/ `format`136（时间戳、详情值、级别色调与短标签、条目副标题/元信息/上下文、详情行等 9 个纯函数）/ `primitives`54（`LogHeaderBadge` / `CopyActionButton` / `LogsPageDetailPanelFallback`，拆出以满足 `react-refresh/only-export-components`）/ `connection`49（`connectionTone`：连接态徽标与图标）。沿用「机械搬迁 + props 化外壳」：原 49-268 行 helper 按纯函数/组件/连接态三类原样落位（仅补 `export`）；header 块（原 543-727）去 4 空格缩进、9 处回调 props 化（setQuery/setLevel/setAdminToken/setFollow/refresh/clearActiveChip/clearAllActiveState/handleCopy(view_link)/copiedSection）；列表面板块（原 730-853）去 6 空格缩进、2 处回调 props 化（setLevel/setSelectedCursor）；`subtitleLabels` 与 `levelFilterOptions` 在子组件内以同一 t 键重建，入口 `buildEntrySubtitle` 复用等值三键的 `detailLabels`；入口保留状态编排、URL 同步、详情面板 labels 与装配。等价性校验 `.tmp/verify-logs-split.mjs`：helper 三类区间逐行、header/列表面板块逐行（仅允许上述改名与缩进）、入口保留区间逐行、被移出符号零残留 → EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npx eslint src/pages/logs-page.tsx src/pages/logs-page` 0 问题；`npm run build` exit 0（807ms，2852 modules transformed）；`npm run test` 88 文件 / 459 用例全绿；定向 `npx vitest run src/pages/logs-page-shared.test.ts src/pages/logs-page-detail-panel.test.ts` 9 用例全绿；新模块全部 < 500 行（最大 `logs-header.tsx` 277），入口 893 → 365；`frontend/src` 内 > 500 行文件数 32 → 31 |
| P0-2 超长文件拆分（首批·第 12 项 `components/workspace/runtime-teams/runtime-team-dispatch-panel.tsx`） | 已完成（2026-09-11） | `runtime-team-dispatch-panel.tsx`(846) → 入口 180 行 + `runtime-teams/runtime-team-dispatch-panel/` 8 个模块：`provision-section`181（创建可运行团队表单 + fan-out 模板切换）/ `task-composer`177（任务表单：标题/目标/输入/交付物/优先级 + 派发按钮 + 错误 + 结果列表）/ `monitor-outcome-compare`177（结果对比：覆盖率卡片 + 对比行 + 终态摘要/缺口列表）/ `monitor-summary`138（批次统计 10 卡片 + 状态 pill + 自动刷新/最新更新/监控计数）/ `team-select-list`96（团队复选列表 + readiness 徽标）/ `monitor-entries`91（监控条目：摘要/邮箱预览/错误）/ `monitor-panel`84（监控面板外壳：刷新 + 错误 + 摘要/对比网格 + 条目）/ `format`11（4 个类常量）。沿用「机械搬迁 + props 化外壳」：原 154-284（provision + 模板）、286-349（团队列表）、352-477（任务表单，仅 `selectedDispatchTeamIds.length` → `selectedTeamCount` 1 处替换）整块搬迁；监控区 480-841 拆为骨架 + 3 子组件，`comparisonRows`/`batchSummary` 计算下沉到 `monitor-panel`，`terminalRowsWithSummary`/`terminalRowsMissingSummary` 下沉到对比组件；入口仅保留 props 类型、页头与装配。等价性校验 `.tmp/verify-dispatch-split.mjs`：8 个区间逐行命中（含缩进与 1 处替换）+ 入口保留区间逐行 + 被移出内容零残留 → EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npx eslint src/components/workspace/runtime-teams.tsx src/components/workspace/runtime-teams/runtime-team-dispatch-panel.tsx src/components/workspace/runtime-teams/runtime-team-dispatch-panel` 0 问题；`npm run build` exit 0（1.04s，`runtime-team-dispatch-panel` chunk 25.99 kB）；`npm run test` 88 文件 / 459 用例全绿；定向 `npx vitest run src/components/workspace/runtime-teams` 2 文件 / 10 用例全绿；新模块全部 < 500 行（最大 `provision-section.tsx` 181），入口 846 → 180；`frontend/src` 内 > 500 行文件数 31 → 30 |
| P0-2 超长文件拆分（首批·第 14 项 `components/workspace/runtime-teams/runtime-team-details-panel.tsx`） | 已完成（2026-09-11） | `runtime-team-details-panel.tsx`(812) → 入口 147 行 + `runtime-teams/runtime-team-details-panel/` 10 个模块：`mailbox-section`229（信箱撰写表单 + 消息列表 + ack）/ `path-claims-section`203（冲突检查表单 + 结果卡 + 租约列表）/ `team-snapshot`164（团队页头 + meta pills + Tasks/Teammates/Task Graph 三卡 + detailsError）/ `roster-section`83（成员列表 + 能力徽标）/ `task-queue-section`75（任务队列）/ `types`69（props 与 section 类型）/ `timeline-section`66（事件时间线）/ `primitives`52（`TeamDetailsSection` 折叠外壳）/ `final-summary-section`38（终态总结）/ `format`24（4 个类常量 + `createInitialOpenSections`）。沿用「机械搬迁 + props 化外壳」：原 209-330 / 332-383 / 385-431 / 433-595 / 597-746 / 748-787 / 789-805 七块按 section 整块搬迁（仅 `open={openSections.x}` → `open={open}`、`onToggle={() => toggleSection("x")}` → `onToggle={onToggle}` 两处 props 化替换），section 内其余 JSX 与 `details.*` 引用逐行零改动；`primitives` 因 `react-refresh/only-export-components` 再拆出 `format`（常量/纯函数与组件分离）；入口仅保留 props 解构、`openSections` 状态、装配与导出壳。等价性校验 `.tmp/verify-team-details-split.mjs`：3 个类型/常量区间 + 7 个 section 区间逐行命中（含缩进）+ 入口 3 个保留区间逐行 + 被移出内容零残留 → EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npx eslint src/components/workspace/runtime-teams.tsx src/components/workspace/runtime-teams/runtime-team-details-panel.tsx src/components/workspace/runtime-teams/runtime-team-details-panel` 0 问题；`npm run build` exit 0（1.01s，`runtime-teams` chunk 22.75 kB）；`npm run test` 88 文件 / 459 用例全绿（JSON reporter：suites 217 / tests 459 / passed 459 / failed 0）；定向 `npx vitest run src/components/workspace/runtime-teams` 2 文件 / 10 用例全绿；新模块全部 < 500 行（最大 `mailbox-section.tsx` 229），入口 812 → 147；`frontend/src` 内 > 500 行文件数 30 → 29 |
| P0-2 超长文件拆分（首批·第 15 项 `components/workspace/settings/runtime-agent-routing-domain-editor.tsx`） | 已完成（2026-09-11） | `runtime-agent-routing-domain-editor.tsx`(807) → 入口 195 行 + `settings/runtime-agent-routing-domain-editor/` 9 个模块：`routing-difficulty-table`163（表头 + 四档路由行）/ `routing-toggles-section`122（继承开关 + 四宫格 + 提示）/ `routing-preview-section`106（预览表单 + 结果挂载）/ `routing-header`93（页头卡片 + 健康徽标 + scope 切换）/ `route-preview-result`92（预览结果卡片）/ `primitives`81（`PreviewValue` / `HealthBadge` / `ScopeButton` / `LabeledCell` 纯组件）/ `health-badges`81（`RouteHealthBadge` + `RouteHealthIssueText`）/ `routing-limits-section`66（并发 + 推理策略）/ `format`65（`reasoningPolicyValues` + `difficultyOptions` / `difficultyLabel` / `cloneRoutingConfig` / `previewTranslation` / `routeHealthIssueMessage`）。沿用「机械搬迁 + props 化外壳」：原 161-223 / 225-301 / 303-419 / 421-458 / 460-520 / 525-608 六块按 section 整块搬迁；原 56 / 610-619 / 621-629 / 631-699 / 701-713 / 715-778 / 780-792 / 794-807 八组 helper 收拢为 `format` / `primitives` / `health-badges`（跨模块复用的原私有 helper 就地补 `export` 但不上浮）；入口仅保留 props 解构、scope/health/预览状态与副作用、`updateConfig` / `updateProfile` / `runRoutePreview` 与装配壳，各 section 经显式 props 注入所需状态与回调，消费方 `backend-config-settings-page.tsx`（lazy import）零改动。等价性校验 `.tmp/verify-agent-routing-split.mjs`：10 个搬迁/保留区间逐行命中（允许补 `export` 与缩进归一）+ 入口 6 类搬迁残留零命中 + 行数门禁 → EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npx eslint <入口 + 新目录 + 入口测试 + utils 及其测试 + 消费方>` 0 问题；`npm run build` exit 0；`npm run test` 88 文件 / 459 用例全绿；定向 `npx vitest run runtime-agent-routing-domain-editor.test.tsx runtime-agent-routing-domain-utils.test.ts` 2 文件 / 16 用例全绿；新模块全部 < 500 行（最大 `routing-difficulty-table.tsx` 163），入口 807 → 195；`frontend/src` 内 > 500 行文件数 29 → 28 |
| P0-2 超长文件拆分（首批·第 17 项 `components/workspace/settings/runtime-rate-limit-domain-editor.tsx`） | 已完成（2026-09-11） | `runtime-rate-limit-domain-editor.tsx`(770) → 入口 152 行 + `settings/runtime-rate-limit-domain-editor/` 7 个模块：`overview-section`259（页头 + enabled 开关 + basicConfig + summary + default_limits + global_limits 三栏卡）/ `api-key-limit-dialog`127（API Key 规则对话框：错误提示 + 5 字段 + extraJson）/ `api-key-limits-table`118（API Key Limits 域表：摘要徽标 + 4 列 + 新建/编辑/删除）/ `path-limits-table`111（Path Limits 域表：摘要徽标 + requests_per_minute/burst 列 + burst 合计）/ `path-limit-dialog`105（Path 规则对话框：path/requests_per_minute/burst + extraJson）/ `format`86（`KNOWN_API_KEY_LIMIT_KEYS` / `KNOWN_PATH_LIMIT_KEYS` + `createApiKeyDraftInput` / `createPathDraftInput` / `stringifyEditableValue`）/ `types`27（`RuntimeRateLimitDomainEditorProps`）。沿用「机械搬迁 + props 化外壳」：原 144-367 / 369-455 / 457-534 / 536-629 / 631-702 五块按 section 整块搬迁（仅去 2 空格缩进）；原 38-46 常量、48-63 props 类型、707-770 草稿工厂收拢为 `format` / `types`（跨模块复用的原私有函数就地补 `export` 但不上浮）；props 名沿用原局部标识符（`t` / `apiKeyLimits` / `openCreateApiDialog` / `setApiDraft` 等），使搬迁区间逐行一致；入口仅保留 state、`totalPathBurst` memo、4 个 open 回调、2 个 save 回调与 5 个子组件装配，消费方 `backend-config-settings-page.tsx`（lazy import）零改动。脚本留档 `.tmp/split-rate-limit-editor.mjs`；等价性校验 `.tmp/verify-rate-limit-split.mjs`：8 个搬迁区间逐行命中 + 入口保留区间逐行 + 入口 15 类符号零残留 + 导出面不变 + 行数门禁 → EQUIVALENCE OK | `npx tsc -b --force` 0 错误；`npx eslint <入口 + 新目录 + 消费方>` 0 问题；`npm run build` exit 0（1.01s，`runtime-rate-limit-domain-editor` chunk 14.18 kB）；`npm run test` 88 文件 / 459 用例全绿（JSON reporter：suites 217 / tests 459 / passed 459 / failed 0）；定向 `npx vitest run runtime-rate-limit-domain-utils.test.ts runtime-rate-limit-domain-form-utils.test.ts` 2 文件 / 6 用例全绿；新模块全部 < 500 行（最大 `overview-section.tsx` 259），入口 770 → 152；`frontend/src` 内 > 500 行文件数 28 → 27 |

---

## 10. 风险与依赖

| 风险/依赖 | 说明 | 缓解 |
|---|---|---|
| 后端接口缺口（Round 2 已收窄） | 硬前置仅剩 **队列/steering、附件上传、MCP 目录/schema、Artifacts 统一接口、货币成本**；审批、SSE 重放、子代理、Skills、搜索、用量、文件读写**均已就绪** | 按 §6.3 P2-1 三档推进：A 类先接、B 类并行小改、C 类 UI 以 `pending/占位` 呈现，不做假数据 |
| 折叠策略与现有回溯/恢复冲突 | 回溯（backtrack）会改写消息历史，折叠谓词需对「改写后的 Turn」重算 | 谓词纯函数化 + 回溯后强制重算投影；补回溯场景测试 |
| 样式重构视觉回归 | 三层 token 迁移可能造成颜色/间距偏差 | 先建立 golden 截图（P2-5 提前做最小集），迁移按页面分批 |
| 超长文件拆分影响既有 import | 大量模块可能直接深链 | 拆分时保留原路径 barrel re-export；一次性 codemod 更新 |
| SSE 重连与租约冲突 | 多人/多进程共享 runtime 时重连可能触发租约冲突 | 复用现有租约冲突标题映射；重连退避 + 会话级串行化 |
| i18n 门禁误报 | AST 扫描对非文案字符串可能误报 | 白名单 + 自然语言判定 + 最小扫描面；先以 warn 模式跑通再转 fail |
| 性能改造范围蔓延 | 虚拟化引入可能牵动消息列表整体结构 | 先度量（P2-4 基准），仅在不达标时引入，保持「先窗口化、后虚拟化」 |
| 大文件拆分与功能改造并发冲突 | 首批拆分的 `workspace-thread-state` / `use-workspace-agent-chat-turn` / `message-list` / 域编辑器，正是 P1-1~P1-4、P2-2 的改动对象；并行开工会造成大面积冲突 | 「先拆后改」固定顺序：命中批次的文件先做机械拆分（不改行为、barrel 保接口），再进行功能改造；功能任务开工前检查目标文件是否在拆分批次内 |
| 命令系统范围蔓延（P2-7） | `/` 命令易被扩展为全局面板或插件命令体系 | 明确非目标（§8）；首批限 3 条内置命令；与 P1-4 共用输入机而非另建一套 |
| 错误边界重试死循环（P1-10） | chunk 加载失败后无限重试或循环刷新 | 重试上限 + 退避 + 显式用户动作；测试必须包含「二次失败」用例 |
| 通知位重复（P2-9） | jobs 常驻状态条与既有 toast/桌面通知（A12）重复提示同一事件 | 单一通知位：状态条负责计数与入口，toast/通知只负责一次性完成/失败；同源事件去重 |
| 侧栏文件热点（P1-9 / P2-6） | `workspace-sidebar.tsx`(1560) 同时承载搜索/重命名/分组，且是第 3 批拆分对象 | 先按第 3 批拆出容器/行/菜单/搜索四块（行为不变），再在拆后结构上实现状态指示与组织功能 |

---

## 11. 附录

### 11.1 关键参考路径（目标项目）

- 消息与滚动：`packages/client/ui-chat/README.md`、`ui-conversation/src/client/contract/conversation.ts`、`ui-conversation/src/client/{conversation,input,queue,skeleton}/*`
- 增量 Markdown：`ui-primitives/src/markdown/{incremental.ts,MarkdownText.tsx,render.tsx,useViewportHighlighting.ts}`
- 工具行：`ui-tool/src/client/tool/components/ToolRow.tsx`
- 轨迹：`ui-trajectory/src/client/trajectory-virtual-rows.ts`、`ui-trajectory/README.md`
- 审批/提问/队列/steering：`ui-approval/src/client/index.ts`、`ui-user-questions/src/client/index.ts`、`input/submission-policy.ts`、`ui-chat/src/client/model/steering-history.ts`
- 设计体系：`ui-theme/src/styles/{base.css,design-platform.css,gradient-shadow-text.css,scrollbar.css,corner-shape.css}`、`ui-layout/src/client/{AppFrame.tsx,columns.ts,theme-presenter.ts}`、`docs/web-styling.md`
- 设置与 i18n：`ui-settings/src/client/schema.ts`、`ui-settings-plugins/src/client/{fields.tsx,card-form.ts}`、`locale/src/client/index.ts`、`scripts/verify-client-ui-i18n.ts`
- 质量保障：`apps/web/tests/{support.ts,scaffold.ts,chat-scroll-contract.e2e.ts,settings-chrome.e2e.ts,sidebar-right.e2e.ts}`

### 11.2 关键参考路径（当前项目，改造对象）

- 样式：`frontend/src/styles/{globals.css,landing.css}`、`frontend/src/core/settings/local.ts`
- 工作台：`frontend/src/components/workspace/{workspace-shell.tsx,workspace-sidebar.tsx,message-list.tsx,message-composer.tsx,artifact-panel*.tsx}`
- 轨迹：`frontend/src/components/workspace/trajectory/*`、`frontend/src/lib/trajectory/*`
- 数据层：`frontend/src/api/runtime/*`、`frontend/src/hooks/workspace/*`、`frontend/src/types/runtime.ts`
- 设置：`frontend/src/components/workspace/settings/*`（含 15 个 `runtime-*-domain-editor.tsx`）
- i18n：`frontend/src/i18n/resources/{zh-CN.ts,en-US.ts}`
- 测试：`frontend/src/**/*.test.ts(x)`、`frontend/e2e/*`

### 11.3 任务编号速查

| 阶段 | 任务 |
|---|---|
| P0 | P0-1 目录卫生 / P0-2 超长文件拆分 / P0-3 组件收敛 / P0-4 三层 token / P0-5 主题解析应用分离 / P0-6 i18n 真源与门禁 / P0-7 测试基建 |
| P1 | P1-1 消息投影与折叠 / P1-2 增量 Markdown / P1-3 滚动契约 / P1-4 Composer 升级 / P1-5 队列与 Steering / P1-6 工具行状态机 / P1-7 审批与提问 / P1-8 连接状态与断线恢复 / P1-9 会话列表状态与整理 / P1-10 错误边界与加载失败面 |
| P2 | P2-1 后端能力对接（就绪度驱动；子包 P2-1A/B/C 见 §6.3）/ P2-2 设置 schema 化 / P2-3 a11y 与响应式 / P2-4 性能预算 / P2-5 视觉回归 golden / P2-6 会话列表组织 / P2-7 命令系统 / P2-8 交付物呈现与降级 / P2-9 可观测面板增补 / P2-10 文件树只读浏览 / P2-11 右栏标签注册表 |

### 11.4 超长文件完整清单（> 500 行，41 项）

> 口径：`frontend/src/**`（排除 `.backups/`、`dist/`），行数 = 含空行编辑器口径（`@(Get-Content).Count`），2026-09-11 采集。39 个非测试 + 2 个测试。归属批次见 §6.1 P0-2「分批策略」。

| # | 行数 | 路径（相对仓库根） | 归属批次 |
|---|---|---|---|
| 1 | 3932 | `frontend/src/components/workspace/settings/backend-config-settings-page.tsx` | 第 2 批（设置域，按 section + 注册表） |
| 2 | 2595 | `frontend/src/i18n/resources/en-US.ts` | P0-6（按 namespace 拆分） |
| 3 | 2536 | `frontend/src/i18n/resources/zh-CN.ts` | P0-6（zh 为 key 真源） |
| 4 | 1815 | `frontend/src/lib/workspace-thread-state.ts` | 首批 |
| 5 | 1692 | `frontend/src/types/runtime.ts` | 首批 |
| 6 | 1560 | `frontend/src/components/workspace/workspace-sidebar.tsx` | 第 3 批（workspace 组件） |
| 7 | 1358 | `frontend/src/components/workspace/settings/runtime-provider-groups-domain-editor.tsx` | 首批 |
| 8 | 1337 | `frontend/src/hooks/workspace/use-workspace-agent-chat-turn.ts` | 首批 |
| 9 | 1187 | `frontend/src/components/workspace/settings/runtime-provider-domain-editor.tsx` | 首批 |
| 10 | 974 | `frontend/src/pages/usage-analytics-page.tsx` | 首批 |
| 11 | 893 | `frontend/src/pages/logs-page.tsx` | 首批 |
| 12 | 846 | `frontend/src/components/workspace/runtime-teams/runtime-team-dispatch-panel.tsx` | 首批 |
| 13 | 831 | `frontend/src/lib/trajectory/trajectory-reducer.ts` | 首批（仅机械整理，语义不动，见 A16） |
| 14 | 812 | `frontend/src/components/workspace/runtime-teams/runtime-team-details-panel.tsx` | 首批 |
| 15 | 807 | `frontend/src/components/workspace/settings/runtime-agent-routing-domain-editor.tsx` | 首批 |
| 16 | 800 | `frontend/src/lib/workspace-thread-state.test.ts` | 随第 4 项源文件同步拆分 |
| 17 | 770 | `frontend/src/components/workspace/settings/runtime-rate-limit-domain-editor.tsx` | 首批 |
| 18 | 744 | `frontend/src/hooks/workspace/use-session-backtrack.ts` | 首批 |
| 19 | 743 | `frontend/src/components/workspace/runtime-teams/use-runtime-team-dispatch.ts` | 首批 |
| 20 | 719 | `frontend/src/components/workspace/message-list.tsx` | 首批 |
| 21 | 715 | `frontend/src/components/workspace/settings/runtime-retry-domain-editor.tsx` | 第 2 批 |
| 22 | 695 | `frontend/src/components/workspace/workspace-shell.tsx` | 第 3 批 |
| 23 | 693 | `frontend/src/components/workspace/runtime-teams.tsx` | 第 3 批 |
| 24 | 683 | `frontend/src/components/workspace/runtime-teams/team-details-panel.tsx` | 第 3 批 |
| 25 | 628 | `frontend/src/components/workspace/runtime-teams/shared.ts` | 第 3 批 |
| 26 | 625 | `frontend/src/components/workspace/message-markdown.tsx` | 第 3 批 |
| 27 | 618 | `frontend/src/components/workspace/artifact-panel.tsx` | 第 3 批 |
| 28 | 613 | `frontend/src/components/workspace/artifact-panel-checkpoint-surface.tsx` | 第 3 批 |
| 29 | 596 | `frontend/src/components/workspace/settings/appearance-settings-page.tsx` | 第 2 批 |
| 30 | 575 | `frontend/src/components/workspace/runtime-teams/dispatch-console.tsx` | 第 3 批 |
| 31 | 568 | `frontend/src/components/workspace/settings/harness-settings-page.tsx` | 第 2 批 |
| 32 | 565 | `frontend/src/components/workspace/settings/runtime-routing-domain-editor.tsx` | 第 2 批 |
| 33 | 539 | `frontend/src/data/mock.ts` | 第 4 批（pages/data） |
| 34 | 537 | `frontend/src/lib/trajectory/trajectory-reducer.test.ts` | 随第 13 项机械整理同步拆分 |
| 35 | 534 | `frontend/src/components/workspace/artifact-detail-dialog.tsx` | 第 3 批 |
| 36 | 523 | `frontend/src/hooks/workspace/use-runtime-sessions-data.ts` | 第 4 批 |
| 37 | 518 | `frontend/src/pages/cache-analytics-page.tsx` | 第 4 批 |
| 38 | 514 | `frontend/src/components/workspace/settings/runtime-agent-routing-domain-utils.ts` | 第 2 批 |
| 39 | 509 | `frontend/src/hooks/workspace/use-runtime-checkpoints.ts` | 第 4 批 |
| 40 | 508 | `frontend/src/components/workspace/settings/runtime-transformer-domain-editor.tsx` | 第 2 批 |
| 41 | 504 | `frontend/src/components/workspace/settings/runtime-provider-queue-domain-editor.tsx` | 第 2 批 |

**批次合计**：首批 15（+1 源测试随拆）、第 2 批 8、第 3 批 10、第 4 批 4、P0-6 承担 2、测试随源 1 = 41。

> **拆分进度（2026-09-11）**：第 5 项 `types/runtime.ts`(1692)、第 4 项 `lib/workspace-thread-state.ts`(1815) + 随源第 16 项测试(800)、第 13 项 `lib/trajectory/trajectory-reducer.ts`(831) + 随源第 34 项测试(537)、第 10 项 `pages/usage-analytics-page.tsx`(974)、第 8 项 `hooks/workspace/use-workspace-agent-chat-turn.ts`(1337)、第 7 项 `components/workspace/settings/runtime-provider-groups-domain-editor.tsx`(1358)、第 9 项 `components/workspace/settings/runtime-provider-domain-editor.tsx`(1187)、第 11 项 `pages/logs-page.tsx`(893)、第 12 项 `components/workspace/runtime-teams/runtime-team-dispatch-panel.tsx`(846)、第 14 项 `components/workspace/runtime-teams/runtime-team-details-panel.tsx`(812)、第 15 项 `components/workspace/settings/runtime-agent-routing-domain-editor.tsx`(807)、第 17 项 `components/workspace/settings/runtime-rate-limit-domain-editor.tsx`(770) 已完成拆分（→ `types/runtime/` 14 个域模块；`lib/thread-state/` 10 个职责模块 + 4 个同源测试 + 共享夹具；`lib/trajectory/trajectory-reducer/` 4 个层次模块 + 3 个同源测试 + 共享夹具；`pages/usage-analytics/` 4 个模块；`hooks/workspace/agent-chat-turn/` 10 个模块；`components/workspace/settings/runtime-provider-domain-editor/` 6 个模块；`pages/logs-page/` 5 个模块；`components/workspace/runtime-teams/runtime-team-dispatch-panel/` 8 个模块；`components/workspace/runtime-teams/runtime-team-details-panel/` 10 个模块；`components/workspace/settings/runtime-agent-routing-domain-editor/` 9 个模块；`components/workspace/settings/runtime-rate-limit-domain-editor/` 7 个模块，均见 §9.3 实施记录）；表中行数为拆分前快照，其余 27 项待推进（首批余 3 项）。

### 11.5 Round 2 功能点处置矩阵（89 项逐条）

> 编号沿用 `.tmp/frontend-opt-analysis/round2-target-shell-features.md`；任务 ID 见 §6，保留资产见 §5.5，不采纳/暂缓见 §8，设计决策见 §7。本表与 §5.6 汇总口径一致。

#### 1-x 外壳与启动（15）

| 编号 | 功能点 | 本方案处置 |
|---|---|---|
| 1-1 | 单页 HTML 骨架与固定挂载点 | 已具备（`#root`；P0-1 保持） |
| 1-2 | PWA manifest 引用 | §8 不采纳 |
| 1-3 | HTML `lang` 静态 en | P0-6（运行时按 locale 回写 `document.lang`） |
| 1-4 | 入口硬失败保护 | P1-10 |
| 1-5 | AppWebEntry 组合入口 | P1-10（bootstrap 顺序化，不引入插件体系） |
| 1-6 | 框架无关启动页与进度弧 | P1-10（首屏骨架 + 失败面） |
| 1-7 | 逐插件加载状态投影 | P1-10（映射为路由/chunk 加载状态） |
| 1-8 | 启动失败报告面 | P1-10 |
| 1-9 | 启动完整性审计 | P1-10（构建资源/路由完整性校验） |
| 1-10 | 客户端 HMR（开发期） | §8 不采纳 |
| 1-11 | fixture 查询开关 | §8 不采纳（e2e 用 route mock） |
| 1-12 | 无客户端路由（单页单视图） | 不采纳（保留 react-router 多路由） |
| 1-13 | 多窗口/多标签同步 | §8 不采纳（单窗口；并行会话走 Fork/多会话列表） |
| 1-14 | 浏览器鉴权与登录入口 | §8 暂缓（按权限模型接后端授权端点） |
| 1-15 | 明文 HTTP 读取信任提示 | §8 暂缓（P3，连接设置文案） |

#### 2-x 布局与侧栏（15）

| 编号 | 功能点 | 本方案处置 |
|---|---|---|
| 2-1 | 三栏几何解算 | P2-3（纯函数 + 边界单测） |
| 2-2 | 侧栏宽度拖拽 clamp | P2-3（持久化偏好） |
| 2-3 | 窄屏自动折叠（1024px） | P2-3（auto 与 user 两态分离） |
| 2-4 | 右栏比例布局 | P2-3 + P2-11 |
| 2-5 | 主题呈现器（DOM 应用层） | P0-5（单一 presenter 所有权 + 可回收） |
| 2-6 | 框架级通知/状态位 | P2-9（单一通知位约束见 §10；目标项目该位仅声明未实现，见 §11.6 C-6） |
| 2-7 | 侧栏壳与全局面板导航 | P1-9/P2-11 扩展（现状部分具备；不引入 slot 体系） |
| 2-8 | 右栏面板 + 头部展开按钮 | P2-11 |
| 2-9 | 右栏标签类型注册（两段式） | P2-11（「注册即生效」为验收项） |
| 2-10 | 右栏历史与资源打开 | P2-11 |
| 2-11 | 工作区文件树浏览 | P2-10 |
| 2-12 | 文件树错误分类 | P2-10（越界为安全语义） |
| 2-13 | 文档预览（含 PDF 失败面） | 部分：文本预览 P2-10；PDF/加密失败面 §8 暂缓 |
| 2-14 | 目录选择：应用内浏览对话框 | 已具备（§5.5 A10） |
| 2-15 | 目录选择：原生 OS 选择器 | §8 不采纳（可选增强） |

#### 3-x 会话列表与工作区（19）

| 编号 | 功能点 | 本方案处置 |
|---|---|---|
| 3-1 | 分组视图（工作区/平铺） | P2-6 |
| 3-2 | 双排序（手动/最近更新） | P2-6 |
| 3-3 | 拖拽排序（组内 + 跨组） | P2-6（乐观更新 + 失败恢复） |
| 3-4 | 组内展开/折叠（Show N more） | P2-6 |
| 3-5 | 空态（无会话/无匹配） | P2-6 |
| 3-6 | 新建会话 | 已具备 |
| 3-7 | 新增工作区 | 已具备（A10） |
| 3-8 | 重命名工作区/会话 | 已具备（A13） |
| 3-9 | 删除工作区（非破坏 + pending） | P1-9（复核非破坏语义与 pending） |
| 3-10 | 归档会话 | P1-9（后端 archive/activate 就绪） |
| 3-11 | 会话 Fork | P1-9 |
| 3-12 | 操作菜单与 aria | P1-9 + P2-3 |
| 3-13 | 会话状态指示（7 态） | P1-9 |
| 3-14 | 定时任务指示 | §8 暂缓（后端证据不足） |
| 3-15 | 相对时间与创建时间浮层 | P1-9 |
| 3-16 | 会话搜索（名称+内容，降级提示） | P2-1A（受 A3 约束：不重写轨迹搜索） |
| 3-17 | 可见性过滤与记账槽 | P2-6（列表正确性） |
| 3-18 | 空白会话自动提升排序 | P2-6（后置项） |
| 3-19 | 多选/批量、未读、置顶（未发现项） | §8 不采纳（需求未验证；Round 3 目录级复核零命中，§11.6 C-3；P1-9 先做等待态指示） |

#### 4-x 设置体系（12）

| 编号 | 功能点 | 本方案处置 |
|---|---|---|
| 4-1 | 设置域基础服务与单一读取 | P2-2 |
| 4-2 | 设置壳独立包 | §8 不采纳（保留现有设置页结构） |
| 4-3 | 设置扩展槽位组 | P2-2（schema + 注册表） |
| 4-4 | 设置触发入口 | 已具备 |
| 4-5 | Onboarding 设置槽 | §8 暂缓 |
| 4-6 | 设置 schema 服务 | P2-2 |
| 4-7 | 设置作用域绑定器 | P2-2（scope 优先级与不可变快照） |
| 4-8 | 模型设置分区 | 已具备（A6）+ P2-2 增量 |
| 4-9 | 插件配置分区表单状态机 | P2-2（dirty/saving/error/readonly） |
| 4-10 | 插件配置项（Shell/loop/Web search/Subagent） | P2-2 首批三项（Shell 超时/并行度/Web search key） |
| 4-11 | 插件清单页 | P2-1A（花名册）+ P2-2 |
| 4-12 | 语言切换入口 + 回退链 | P0-6 + P2-2（General 语言行） |

#### 5-x 运行时功能包（14）

| 编号 | 功能点 | 本方案处置 |
|---|---|---|
| 5-1 | 目标（Goal）生命周期控件 | P2-9（四相指示；权威源见 P2-1B） |
| 5-2 | 子代理会话树查看器 | P2-9 |
| 5-3 | 子代理元信息格式化 | P2-9（共享纯函数 + i18n 单位） |
| 5-4 | 后台任务（Jobs）状态条 | P2-9（与 toast 去重） |
| 5-5 | 工作流运行视图 | §8 暂缓（状态词汇表先并入 P2-9） |
| 5-6 | 技能（Skill）工具行 | P1-6（ToolRow 状态机）+ P2-1A（技能面板） |
| 5-7 | 交付物呈现与宿主动作 | P2-8（能力降级 + 三态） |
| 5-8 | 交付物打开深链 | P2-8（后置；与路由 query 设计同行） |
| 5-9 | 消息反馈（Feedback） | P2-7（收敛为 `/feedback` 命令入口） |
| 5-10 | 模型选择器（Composer 内） | 已具备（A5/A6；P2-2 不得回退） |
| 5-11 | Agent 预设切换（会话级） | §8 暂缓（待后端预设版本） |
| 5-12 | 命令系统（输入触发式） | P2-7 |
| 5-13 | 资源模型（统一资源打开） | P2-11 |
| 5-14 | 轻量状态引擎（store）契约 | 部分采纳：§7.9 两条纪律，不引入自研 store |

#### 6-x 横向交互（14）

| 编号 | 功能点 | 本方案处置 |
|---|---|---|
| 6-1 | Composer 提交键位（Ctrl/Cmd+Enter） | P1-4 |
| 6-2 | 组件级 keydown 监听生命周期 | P1-4（挂载/卸载配对，并入 §7.9 纪律） |
| 6-3 | 无全局快捷键体系/命令面板 | §8 不采纳 |
| 6-4 | aria 标签覆盖 | P2-3 |
| 6-5 | 状态提示可访问性（role=status） | P2-3 + P2-6（拖拽宣告） |
| 6-6 | 虚拟高亮下拉（输入内） | P2-7 |
| 6-7 | 骨架屏/占位行 | P1-10 + P2-4 |
| 6-8 | 槽位级错误边界 | P1-10（面板层边界） |
| 6-9 | 错误重试模式（显式 retry） | P1-10 + P2-1C（各面板 retry 三态） |
| 6-10 | 订阅者/监听器错误隔离 | §7.9 + P1-10 |
| 6-11 | 离线/在线与连接恢复 | P1-8 |
| 6-12 | 遥测/埋点 | §8 不采纳 |
| 6-13 | 密钥与敏感输入（password） | P2-2（表单规范） |
| 6-14 | 路径隐私与显示缩写（`~`） | P2-10（文件树）+ P1-6（工具行路径） |

**统计核对**：落地 63 项 / 部分采纳 2 项 / 已具备 8 项 / 暂缓 6 项 / 不采纳 10 项 = **89 项**，与 §5.6 汇总一致；无悬空功能点。

---

### 11.6 目标项目证据缺口复核记录（Round 3）

> 来源：`.tmp/frontend-opt-analysis/round3-gap-a.md`（C-1/C-3/C-6）与 `round3-gap-b.md`（C-2/C-4/C-5/C-7），方法为对 deepseek-harness 源码直读 + 目录级 grep。证据路径除注明 `apps/web` 外均相对 `packages/client/`；标「未核验」的子项不得作为定稿依据。

| 缺口 | 复核结论 | 关键证据（deepseek-harness） | 对方案的影响 / 未核验子项 |
|---|---|---|---|
| C-1 侧栏主体与会话列表装配 | 侧栏无独立几何：列宽常量、断点、三列求解均在 `ui-layout`；折叠为 150ms slide+crossfade 并冻结展开宽度；会话列表入口为 `ui-workspace` 的 `WorkspaceBrowser` | `ui-layout/src/client/columns.ts:13-19,20-23,50-56`；`ui-layout/src/client/AppFrame.tsx:160-164,193-196,206-209`；`ui-sidebar/src/client/SidebarRoot.tsx:30-31,100-119,166-179`、`SidebarRoot.module.css:45-86,418-429`（reduced-motion 全关）；`ui-workspace/src/client/index.ts:141-150`、`rows/WorkspaceBrowser.tsx:839,1256-1326` | 2-1/2-2/2-3 的几何参数与 2-7 职责划分有据可依；P1-9 扩展点落在 `WorkspaceBrowser` 拆分后的结构上。未核验：`SidebarRoot` 密度/主题变量细节 |
| C-2 设置壳槽位与导航 | 设置 SHELL 确在 `ui-settings-general`；`sidebar.settings` 注册时一次声明 6 个子槽；导航不是独立数据表，而是 `settings.section` 槽条目按 order 投影；4-4 触发入口与 4-5 Onboarding 槽均真实存在 | `ui-settings-general/src/client/index.ts:100-125,146-182`；`SettingsRoot.tsx:47-101,138-144,182-193,217-221`；`ui-settings/src/client/contract/slots.ts:14-90,104-136` | P2-2 注册表模型与 4-3/4-5 对照以该包为准；**更正**：契约槽位共 8 个（6 个 owner props 类型 + action/close 复用 Header props），P2-2 措辞已同步。未核验：`navIcon(id)` 映射、`shell-contract.ts` 字段全集、实际注册 onboarding 的功能包清单 |
| C-3 多选/未读/置顶负向结论 | 两目录逐文件复核确认：多选/批量、未读标记、置顶均不存在；唯一行隐藏机制是 archive | `ui-workspace/src/client`（13 文件）与 `ui-sidebar/src/client`（5 文件）全量关键词 grep 零命中（`selectedIds` 仅视图选项菜单 `WorkspaceBrowser.tsx:187`；`pin*` 均为菜单布局/固定类注释） | 3-19 不采纳与 §8 行升级为「目录级复核」证据；P1-9 明确不引入多选/未读。未核验：两包 tests/、`Menu.selectedIds` 的多选语义 |
| C-4 `/export` 命令 | 词条与 builtin 映射在 `ui-commands`；执行落在 `session-log-export` 包（GET/HEAD `/api/session.export`），范围=根会话+子会话+附件，fflate 流式 Zip（默认级别 6）；已读范围未见内容级脱敏 | `ui-commands/src/client/locales.ts:17,23,29`、`resolution.ts:12`；`session-log-export/src/index.ts:42-43,79-101,113-166`、`archive.ts:1-15,16-18,24,100-136,186-196` | 命令系统（5-12/6-6）实现参照明确；若未来做导出，脱敏需自定。未核验：`archive.ts` 中段、`controller.ts` 内部控制流 |
| C-5 `apps/web` 构建与静态资源 | `base:'./'`；无 server/proxy/HMR 配置（独立 serve 被插件显式拒绝）；PWA 可安装静态元数据链完整且被 e2e 锁定；fixture 查询开关仅连接与上传两处 | `apps/web/vite.config.ts:31-38,146-150`；`apps/web/index.html:6-7`、`apps/web/public/manifest.webmanifest:1-16`、`apps/web/tests/pwa-manifest.e2e.ts:8-35`；`connection/src/client/index.ts:190-194`、`file-upload/src/client/runtime.ts:314-319` | 1-2 维持不采纳（属产品取向取舍，非能力缺失）；1-11 维持不采纳（开关面窄、生产误开风险）。未核验：Service Worker/离线运行时、fixture 差异行为 |
| C-6 全局通知位 | `shell.overlay` 仅为 ui-layout 声明的通用 list 槽（click-through），全仓无任何包注册 toast/status 条目；无 stacking、优先级、去重、自动消失、`role=status`/`aria-live` 实现 | `ui-layout/src/client/index.ts:81-91,151-155`；`ui-layout/src/client/AppFrame.tsx:200,230-232`、`AppFrame.module.css:90-99`；跨包 grep 仅注释与示例命中，该位静态为空 | P2-9「单一通知位」为本项目自建能力（无可移植实现），不得引用目标实现作参照；2-6 维持 P2-9。未核验：slot registry 内部排序规则 |
| C-7 审批与会话运行时状态 | 审批卡渲染/提交/abort、会话级 pending 注册表、运行态词汇与工具行状态渲染均已核验；**目标项目无审批超时实现**（取消经 AbortSignal） | `ui-approval/src/client/ApprovalPanel.tsx:12-54`、`contract/slots.ts:68-159`、`index.ts:35-92`；`ui-session/src/client/index.ts:69-102,304-323,366-386`；`ui-conversation/src/client/locales.ts:98-103,145,162-168`、`ui-tool/src/client/tool/components/ToolRow.tsx:90-109,197` | P1-7 交互与状态表达对齐有据；`expired` 30min 超时为本项目新增能力（后端已就绪，§6.3 P2-1A）。未核验：ui-chat 上层是否另有运行态入口、completed 显式分支 |

**复核后仍存的未核验子项**（均不影响任务划分与排期）：`navIcon` 映射、slot registry 内部排序、`apps/web` 的 SW/离线运行时、fixture 差异行为、`archive.ts`/`controller.ts` 中段控制流、ui-chat 上层运行态入口。本轮未修改 deepseek-harness 任何文件；两份报告含完整文件清单与关键词 grep 记录，可复查。
