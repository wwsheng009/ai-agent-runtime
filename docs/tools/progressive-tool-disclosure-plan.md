# 渐进式工具披露（Progressive Tool Disclosure）实施方案（v2）

> 状态：设计提案 v2（2026-10-15 修订；v1 原文存档：`progressive-tool-disclosure-plan.v1-20261014.md`）
> 前置定稿：`docs/architecture/unified-tool-surface-registry.md`（工具面收敛 canonical 文档，本方案是其后续线）
> 参考项目：T-Auto/dsh-ops（Windows DSH 性能插件；2026-10-15 本地核查 `lib/presentation.js`、`lib/policy.js`、`lib/tools.js`、`docs/schema-measurement.md`、`docs/native-ptc.md`）。其实测为**组件门控 -28.2% schema 字节、描述压缩 -47.5%**，并明确声明"不代表整任务 provider token"；v1 引用的"97%"无出处，已删除
> 适用范围：aicli（CLI 宿主）与 runtime-server（服务宿主）共用的工具装配与 prompt 组装链路
> 关联代码：`internal/toolsurface`、`internal/toolschema`、`internal/prompt`、`internal/toolbroker`、`internal/agent`（loop.go / tool_surface_binding.go / tool_surface_context.go / tool_list.go）、`internal/chat`（turn_tool_surface_snapshot.go）、`internal/compactruntime`
> 修订说明：v2 全部关键论断经 2026-10-15 代码核查（行号为核查时点）；v1→v2 变更清单见 §9

---

## 1. 问题陈述

每次请求把当前授权面内全部工具的完整 JSON Schema（含 description）放入请求前缀（system 消息之后的 tools 数组）发给模型：

- 工具定义占用大量前缀 token，每次对话重复支付；
- 工具越多，模型决策越分散：该走文件工具时去跑 shell、报错后乱换工具；
- 大量低频工具（mesh、team、schedule、lsp…）在多数会话中从未被调用，却全程占位。

dsh-ops 的实测结论（本地核查）：固定 schema 成本可被"**组件门控 + 描述压缩**"显著压低（-28.2% / -47.5%），但其文档同时声明——"实际请求数、缓存定价、工具输出与历史重复**可能超过**固定 schema 节省；没有任何百分比能预测整任务 provider token"。本项目具备宿主级控制权，可在装配源头收敛（§8）。

> 事实基线（2026-10-14 实测，registry 文档 §2.2/§15.3）：`/runtime/tools` 全量面 53 工具（orchestration=31 / core=20 / mcp:aicli_goal=2）；带 chrome-devtools MCP 的会话 83 工具。收敛空间主要在 orchestration 家族与 MCP 组。

## 2. 目标与非目标

### 目标
1. 请求前缀中的工具信息从"全量 schema"收敛为"紧凑路由表 + 按需披露"；
2. 工具集在一个会话内**字节级稳定**：沿用并加固既有冻结/缓存代际语义（§5），不新建平行机制；
3. 能力按组件渐进启用：未启用的组件，其工具**不进入模型可见面**（不是注册后隐藏）；
4. 权限语义不变（sandboxPolicy / approval / 执行期拦截）；同时保持既有原则"**模型可见面 = 可执行面**"。

### 非目标
- 不改变权限判定与执行期拦截的语义——可用面按组件收敛是预期行为（v1 "不改变任何执行行为"表述有歧义，已修正）；
- 不引入新的 MCP server；
- 第一期不做动态运行时装卸（按会话/回合粒度静态装配）；
- 不新建第二套启停判定：分组是既有策略裁剪机制（profile / ToolExecutionPolicy / ShouldList）的配置化语法糖（§4.1）。

### 2.1 收益从哪来（三杠杆 + 一个可选，按落地顺序）

| 杠杆 | 机制 | 生效阶段 | 收益性质 |
| --- | --- | --- | --- |
| ① 组启停（层一） | 未启用族的**完整 schema 不进前缀**（每请求都省） | 阶段 3 | 直接省 token（减法） |
| ② schema 瘦身（层二） | 同集合、更小字节（静态短语表 + 参数子集） | 阶段 2 | 直接省 token（压缩） |
| ③ 缓存加固（§5） | 前缀字节稳定 → provider 前缀缓存持续命中 | 阶段 1 | **省钱不省 token**：避免缓存失效后按全价重付 |
| ④ 按需加载（层四） | schema 首次用到才下发 | 第二期（可选） | 大幅省 token，但多一次往返 + 一次断代 |

关键澄清（避免误读为"第一期只发摘要"）：

- **第一期不发"摘要代替 schema"**：两个轴别混——**集合轴**（组启停决定谁进 tools 数组）与**内容轴**（进数组的工具给多详细）。第一期数组里的是**可调用 schema**（阶段 2 起为精简版，非摘要）；路由表是**叠加**的认知地图，本身也花 token（数百量级），它换来的是"能安全地少发 schema + 少走弯路"；
- **省 token 要乘上请求数**：工具 schema 是**每请求重复**的固定前缀，省一个工具等于省 N 次；路由表是**一次性**（会话内固定、可缓存），所以"一张表换掉 M 个常驻 schema"在 M 稍大时即净赚；
- **省 token ≠ 省成本**：前缀缓存命中良好时，常驻前缀的边际成本已被折扣覆盖。真实收益来自 (a) 缓存未命中/首轮的全价，(b) **减少缓存失效次数**（§5 守则的全部意义），(c) 决策质量（少调错工具 = 少整轮请求）；
- **收益随会话形态分化**：orchestration 是最大单一族（registry §2.2：31 个），多代理会话收益最大；普通单代理会话可关的主要是 mesh / lsp / io 与部分 orchestration——这正是目标分档 40% / 20% 的原因，且**必须先补基线实测**（§6 度量）。
- **参考项目量级（本地核查）**：dsh-ops 组件门控后默认面 5 工具 / 7,685 B / ≈1,922 tok（全量 10 工具 / 10,700 B），描述压缩使 file 类描述 12,807 → 6,724 B（-47.5%）。注意其工具基数只有 4~10 个，**本项目是 51~86 个**，百分比不可直接外推。

## 3. 现状盘点（v2：逐项经代码核查）

| # | 既有资产 | 位置（行号为核查时点） | 对方案的意义 |
| --- | --- | --- | --- |
| 1 | 统一工具面装配（按 name 排序、幂等去重、surface_source 标注） | `toolsurface.go:82-114`；来源汇总 `:119-139` | 观测面与注册面的单一分类/排序实现点 |
| 2 | 请求装配路径排序**已实现** | `loop.go:4340`（computeAvailableTools 尾部）、`tool_surface_binding.go:168` | v1"排序推广"已完成；剩余是叠层顺序定义（§5.1） |
| 3 | 回合冻结 + 会话稳定面 + 缓存代际 | `tool_surface_context.go:97-111`；`chat/turn_tool_surface_snapshot.go:28-42,83-114`；`loop.go:814-822,2270-2356`（promptCacheEpoch/Key） | v1"冻结快照"已存在；方案改为复用并补交互约束（§5） |
| 4 | 工具面指纹与 eligibility binding | `tool_surface_binding.go:17-49,67-96,101-170` | golden 断言与"为何换面"审计直接复用 |
| 5 | 模型面 schema 瘦身（已部分落地） | `loop.go:4568-4621`（shell/grep 别名收口、grep 描述改写）、`:4672-4702`（grep 参数压表：保留 context、其余走 rg_args）、`:4705-4765`（预算触发：描述截 160 字、剥注解） | 层二起点：剩余范围与顺序收口见 §4.2 |
| 6 | 工具 token 计量与事件 | `loop.go:5765-5774`（estimateToolDefinitionTokens，经 llmRuntime.CountTokens；`tokenestimate` 为独立包）、`:2264`（toolSchemaTokens）、`:2216-2235`（tool_schema.compacted/frozen 事件） | 收益量化基建已有；观测端点补前后对比即可 |
| 7 | 请求级来源账本 | `loop.go:2321-2329`（SummarizeSources → req.Metadata，不推高 token） | 观测/审计沿用同一机制 |
| 8 | 既有启停机制（三层在场） | profile 工具策略双落点（registry §16.2）；`ToolExecutionPolicy`（ReadOnly/Allowlist/CapabilityScope）；`toolkit.ShouldList/list_when` + `tool_list.go:26-68` | 层一改为映射复用，避免第四套并行体系（§4.1） |
| 9 | 分层 prompt 片段 | `prompt/layers.go:33-45`（Fragment）、`:143-201`（按插入序合并） | 路由表可作新 Fragment；"补 order 字段"需先盘点生产者（§4.3） |
| 10 | 回合级工具叠加（pin / routing） | `turn_context.go:38-47,65-104`；`loop.go:2237-2245`；顺序定格测试 `turn_pin_overlay_test.go:13-43` | 前缀含 sanctioned 回合叠层，v1 硬约束需修正（§5.1） |
| 11 | simple-goal 投影（一次性路径） | `loop.go:4218-4225,4312-4332` | 层一/层三按宿主路径分治（§4.0） |
| 12 | 权限过滤与只读面 | `loop.go:4288-4301`（策略拦截）、`:4302-4307`（只读 shell 描述增强） | 可见面按策略裁剪是既有设计（"模型看得见就会调"） |
| 13 | broker 编排面判定 | `toolbroker.IsBrokerTool`（job/subagent/team/schedule/plan/question） | 分组键之一；但 Classify 粒度粗（core 单桶），需第二键（§4.1） |
| 14 | 独立能力描述包（已占用） | `internal/capability/capability.go`（Descriptor/Provider：skill/tool/workflow/agent） | **命名避让**：分组声明不放 `internal/capability` |
| 15 | 编排工具逐族审计矩阵 | registry 文档 §8（31 编排工具） | 分组归属审计的现成输入 |

## 4. 方案设计（四个渐进层）

### 4.0 先决：宿主路径矩阵（v2 新增）

工具的可见集合与冻结语义按入口不同，方案必须逐路径明确（v1 把"会话"当作同质对象）：

| 路径 | 装配点 | 冻结语义 | goal 投影 | 层一/层三行为 |
| --- | --- | --- | --- | --- |
| aicli exec（一次性） | `computeAvailableTools`；内存快照 | 无跨回合稳定面；回合=整个 run | 开（可收缩目录） | 组启停=本次运行静态配置；路由表按投影后工具集生成 |
| aicli TUI chat / runtime-server inline | 同上 + `runtimeTurnToolSurfaceSnapshot` | 会话稳定面 + 回合 freeze-once；目录变化经 `InvalidateStableToolSurface` 在回合边界生效 | 关（稳定面禁投影） | 组启停=会话级；路由表随会话装配一次 |
| compact / 子代理 | `compactruntime`（保留 tools 前缀、tool_choice=none）；scheduler 只读面 | 继承父面或按子策略另建面 | 按路径 | 不得破坏"前缀保留"约定 |

任何实现动工前，先为三条路径各配一组 golden 断言（§5.6）。

### 4.1 层一：组件分组 = 既有策略裁剪的配置化（v2 修订）

v1 主张"新建分组体系 + 复用 Classify 做分组键"，与现状冲突：Classify 是粗分类（core 为单桶），且三套启停机制已在场。修订为：

1. **组是配置语法糖，不是新判定**：一张静态分组表把"组"翻译到既有机制——组 → profile 工具策略条目 / allowlist 名单 / capability scope / ShouldList 条件；运行时仍由既有链路判定（与 profile 的分工、冲突优先级见 §4.5）；
2. **分组键补充**：内置工具需要一张静态 `name → family` 表（file/shell/search/…），落点建议 `configs/` 或 `internal/toolgroups`（避开 `internal/capability` 撞名）；MCP 工具按 `mcp:<server>` 成组；orchestration 沿用 IsBrokerTool；
3. **分组表（修订版，消除 v1 内部矛盾）**：

   | 组 | 成员 | 默认 |
   | --- | --- | --- |
   | core.file | view / write / edit / append_write / multiedit / glob / grep / ls | 开 |
   | core.shell | shell 家族（模型面已收口为一个主 surface，`loop.go:4568`） | 开 |
   | core.search | web_search / fetch / sourcegraph | 开 |
   | orchestration | broker 编排面全家（job_* / subagent / team / schedule / plan / question） | 会话相关 |
   | io | download / image / artifacts | 按需 |
   | mesh / lsp | mesh 族 / lsp 族（归属待按 registry §8 审计核实） | 仅对应会话 |

   （v1 曾把 background job 同列 core.shell 与 orchestration——已修正：job_* 属编排面。）
4. **"不注册"与观测一致性**：未启用组不进入模型可见面，但观测端点要能解释原因——定义解释词表：`group_disabled / policy_denied / should_list / read_only / budget_compacted`；
5. **守卫**：不得破坏 `disable_tools` 例外（目录冻结、只翻 tool_choice，`loop.go:4293-4295,2334-2339`）。

### 4.2 层二：schema 瘦身（确定性压缩，范围收敛）

既有事实（v1 未盘点）：shell/grep 别名收口、grep 描述改写与参数压表**已实现**（`loop.go:4568-4702`）；预算触发的描述截断/剥注解也已存在（`loop.go:4705-4765`）。修订后的范围：

1. 覆盖剩余 token 大户：`spawn_subagents`、job 家族、`schedule`/`wait_team`、mesh 类 MCP——以实测 token 排序推进；
2. 压缩方式沿用既有范式（表驱动静态短语 + 参数 expose 子集）；**修正 v1 举例**：现实现是**保留 `context`、隐藏其余高级项（经 `rg_args` 透传）**，不是删除 context；
3. **与预算压缩的顺序定死**：Compact 在回合冻结前一次成型；预算截断（`compactToolSurfaceToBudget`）只在超限时兜底触发、且发生在冻结前；另有 `compactToolSurfaceForPrompt`（按步余量计算，当前仅测试引用、未接生产路径）——若未来启用，必须先解决"逐 step 变化"与前缀稳定的矛盾（epoch 断代或改冻结前一次成型）；
4. 红线不变：同一构建版本下 compact 输出逐字节相同；warning 进 metadata 不改变输出字节；静态短语表为唯一文案来源。

### 4.3 层三：系统 prompt 路由表（"认知地图"）

新增 Fragment（source: `tooling_route`），内容 = 紧凑路由表。修订要点：

1. **定义为确定性函数**：`body = f(本次装配的已注册工具集, 组启用状态)`；f 为纯函数，行按工具名固定排序（禁止依赖 map 迭代序），文案来自静态短语表——同一句否定 v1"静态常量"与"程序化生成"的矛盾；
2. **never advertise absent（路由表口径）**：缺席工具一行不出。注意与既有语义差异：环境能力段（environment_capabilities）**有意**展示 broken/unavailable 工具供规划——两者定位不同，需显式声明、避免后续互相"修正"；
3. **落位先于 order 字段**：先列出现有 Fragment 生产者与目标相对顺序（project instructions / env capabilities / guidance / skills catalog…），再决定是否引入 `order`（`layers.go:143-201` 现为插入序合并，引入 order 需定义排序时机与生产者迁移策略）；
4. **归并替代，不是叠加（硬要求）**：现状 system 前缀里**已有多处**工具使用指引——`chat_session.go:2622-2634`（shell / 文件编辑 / 并行 / 难度路由 / 多代理五段）、`agent_context.go:54,62`、`instruction_messages.go:178,182`、`agent/prompt_builder.go:57`、`skill/executor.go:403,1116-1121`、`prompt/environment_capabilities.go:568`（环境能力段）。路由表上线时必须**同时收敛/删除与之重复的既有指引**，按"净 token 变化"考核；只叠加不归并 → 直接否决；
5. **可以不加（启用判据）**：若层一已把暴露面裁到很小（经验阈值 ≤15 个工具）且无别名 / 近义族 → 路由表边际价值低，跳过；存在别名族（shell/bash/execute_shell_command、view/read、grep/rg）或近义工具（search_tool vs grep vs web_search）时启用，收益主要来自减少误选与整轮浪费；
6. **权威性排序**：tools 数组是**权威定义**（模型只能调用其中声明过的工具），路由表只是同一集合的"索引 + 偏好"，**不得包含数组外的工具**；CI 断言两者同源（§7 风险表）。
7. **参考实现核对（dsh-ops `lib/policy.js` `renderToolingPolicy`）**：与层三同构——`TOOLING_SECTION='dsh-ops:repository-tooling'` + `SECTION_ORDERS={[TOOLING_SECTION]:3250}`（固定 order）、"A single compact table; absent tools never get advertised"、行由已发布集合程序化生成、表头随可用组件切换；且 **prompt 段与组件同生共死**（`configuration-reference.md` §Prompt registration：shell OFF 撤 bash/pwsh 路由段、file OFF 撤文件指引段、background OFF 撤后台工具段；file 498 B / shell 492 B / background 269 B）。→ 层三"归并替代"在参考项目里的粒度就是**组件**：工具集与它的 prompt 段一起进、一起出。

示例（行内容为静态短语，行序按工具名）：

```text
# 工具路由
| 任务 | 工具 |
| 读文件/批量区间 | view (files[] 批量) |
| 搜索内容 | grep (先 patterns 批量) |
| 找路径 | glob |
| 执行命令 | shell |
- 报错先修参数重试，勿换工具绕行。
```

### 4.4 层四：低频工具技能化与 schema 后补（第二期，可选）

**先澄清（易误读）**：工具的"集合"与"内容"是两个轴——**集合轴**由组启停决定（谁进 tools 数组），**内容轴**决定每个进数组的工具给多详细。第一期 tools 数组 = **已启用组的可调用 schema**（足以让模型正确调用，不是摘要；阶段 2 起为精简版），路由表（层三）是**叠加**在 system 前缀上的认知地图，不替代 schema。"只发摘要、schema 按需下发"属于本层（第二期）能力，需 schema 后补协议，第一期明确不做。

**核查结论（2026-10-15，参考项目本地核查）**：dsh-ops **不做运行时按需加载**。它的答案是：

1. **组件门控到发布源头**——未启用组件连 MCP 服务器都不请求那些工具（`lib/tools.js` `serverArgs(enableShellTools)` → `['serve']` / `['serve','--enable-shell']`；`configuration-reference.md`："enabling it still requires session `danger-full-access` to publish the four background tools. **The file component never publishes command tools.**"）；
2. **模型拿不到能力时的唯一出路 = 用已发布替代工具，或请 operator 改策略**（`lib/policy.js` `hostShellRefusal`："Use an available file tool for file operations; otherwise **request an operator policy change**."）；
3. **要"调用时"省 schema，走 PTC（SDK 化）而非 schema 后补**：PTC-only 呈现只给**一个** wire schema，宿主由同一批工具 schema 生成 **SDK 声明**，模型写代码调 `tools.ops_x()`（`lib/ptc/contract.js`：单一 contract 驱动 host schemas + SDK types + callable names；`native-ptc.md`："The host generates SDK declarations from the same tool schemas; no duplicate guide or dynamic plan IDs are added to the system prefix."），并用 `ops_batch(plan→resume)` 预检 + `ops_result` 有界引用。

**因此本方案不引入下列 A/A′/B 按需加载路线**（保留为探索记录，供将来有强需求时评估；理由：参考项目实测未采用，且成本结构不利——见下）：

**按需加载的候选流程**（若第二期落地）：

1. 常驻一个极简**发现入口**（meta 工具）：优先复用既有 `search_tool`（`tool_list.go:70-96` 的 `InMemoryToolSearchIndex`；宿主显式注册时才可见；`fullCatalogForSearch` 已保证隐藏工具可被搜到）；
2. 模型调用发现入口 → 运行时返回命中工具的条目（含 `Parameters`）；
3. 运行时把该定义**追加**进本回合 tools 数组（复用 `overlayTurnPinnedTools`（`turn_context.go:65-104`）同型的 append-only 叠层）；
4. 追加会改写本回合 tools 前缀 → 触发 **cache epoch 断代**（`loop.go:2250-2254` 的 `prompt_cache_epoch_break`），并在请求 metadata 记录提升原因；
5. 冻结语义从"回合 freeze-once"放宽为"回合内 **append-only 提升**"（不重排、不删除、只追加）——这是同时保住前缀缓存与按需加载的唯一折中；
6. 执行期仍过 `AllowToolCallWithContext`：**后补不放宽授权**（组关闭 / deny 的工具即使被"发现"也不能执行）。

**代价**：每次按需加载多一次 LLM 往返 + 一次前缀断代（缓存 miss）；只对低频高价值工具使用，并观测"提升命中率 / 断代成本"。

**两种加载时机（第二期的关键设计，成本差一个量级）**：

| 时机 | 触发 | 机制 | 成本 | 复用点 |
| --- | --- | --- | --- | --- |
| **A. 回合边界预取**（推荐主形态） | 启发式：用户输入词表 / 上一回合的 `tool.denied`、未找到工具、报错换工具 / skill 声明 / MCP 晚到 | 在**新回合冻结前**扩展稳定面（只增不减） | **零额外往返、零断代**（新回合本来就要重新冻结） | 既有缝隙：`shouldRefreshStableToolSurface`（`loop.go:4398-4403`）+ `toolSurfaceAddsCapabilities`（`:4431-4445`）+ `CanRefreshStableToolSurface`（`turn_tool_surface_snapshot.go:32-42`；刷新窗口 = 新回合首次冻结前） |
| B. 回合内提升 | 模型显式请求（发现入口） | append-only 追加 + epoch 断代（上文六步流程） | 多一次 LLM 往返 + 一次缓存 miss | `overlayTurnPinnedTools`（`turn_context.go:65-104`）+ `prompt_cache_epoch_break`（`loop.go:2250-2254`） |

**结论：第二期主形态是 A，不是 B**——"启发式加载"落在回合边界几乎免费（回合边界本就是允许换面的点）；B 只用于"本回合立刻要用"的急迫场景。

**启发式信号（A 的输入，全部确定性）**：

1. 用户输入词表：复用 `simpleGoalToolNames`（`loop.go:4447+`）的"文本 → 工具集"思路，方向从"投影收缩"改为"预取扩展"；可扩展为 `internal/capability` 的 Trigger/Weight 匹配（`capability.Descriptor` 已带 `Triggers` + `Weight`，比硬编码词表可扩展）；
2. 上一回合执行信号：`tool.denied`（策略拒绝）、工具未找到、报错后换工具——直接说明"模型需要但没有的能力"；
3. skill 声明：技能自带 `Tools: []string`，声明即预取（同时校验可用性）；
4. MCP 晚到 / 热重载：已是现有触发源，归入同一通道；
5. 委派需求：`spawn_agent` 的 `tools_whitelist` 需求可提前反映到父面（可选）。

**守卫**：只增不减（`toolSurfaceAddsCapabilities` 已强制）；预取受预算上限约束（复用预算压缩思路）；预取命中率 / 误取率进观测（§6 度量），误取高则关闭该信号源。

**A 的触发判断靠什么（以及一个必须承认的盲点）**：

- 5 类信号里**只有第 1 类（用户输入词表）是"预测"**；第 2 类（`tool.denied` / 未找到工具 / 报错换工具）是**事后补缺口**——模型已经表达了需求，运行时只需记录并补齐；第 3~5 类是**声明式**（skill 声明 / MCP 目录 / 委派白名单），不需要推断。所以 A 的主体不是"猜模型要什么"，而是"把已经暴露的缺口在回合边界补上"；
- **LLM 在 A 里不感知、不参与**：它不需要知道"我缺工具"。运行时补齐后，模型下一回合只是"看到"了更多工具，甚至不会意识到发生过什么；
- **盲点（必须解决）**：若路由表严格"缺席工具一行不出"，模型**不知道**某能力存在 → 不会尝试调用 → 运行时观测不到缺口 → A 永远学不到该需求（死循环）。两条出路：
  1. 路由表区分两类行——**暴露面行**（可调用，附参数提示）与**可加载行**（存在但未加载，仅一行能力描述）：打破 never-advertise-absent 的绝对性，代价是表变大；
  2. **变体 A′：模型显式请求 + 回合边界生效**——常驻极简 `request_tool(需求描述)`，模型表达需求，运行时解析后在**下一回合**加载。比 B 少一次断代（回合边界本就是换面点），但仍多一次往返。
- 选型：可预测需求 → 纯 A（词表 / 声明）；不可预测但模型自知 → A′；当回合急用 → B。

**为什么 B 有成本（量化）**：

1. **多一次 LLM 往返 = 整段上下文的代价**：未声明的工具调不了，模型必须先调发现入口、拿到定义后才能调用真实工具——**最少多 1 个 step**。该 step 要重付 system + tools + 全部历史（量级通常几万 token），而省下的 schema 通常只有几百~几千 token；
2. **断代不是一次性的**：append 改变 tools 前缀 → `prompt_cache_epoch_break`（`loop.go:2250-2254`）换 generation → 本轮全价 + 后续数轮缓存重建期。时机 A 在回合边界，新 generation 本来就要建，所以差别是"额外断代 vs 自然换面"；
3. **发现入口与历史污染**：发现工具本身常驻（描述 + 参数），其返回值进入对话历史后**每一轮都随行**——比一次性前缀更持久；
4. **实现与审计成本**：append-only 约束、epoch 记账、提升后仍需过执行期判定。

**B 值得的条件**（满足其一）：该工具在同一回合内会被多次调用（摊销往返与断代）；或避免"无该工具导致整轮失败 / 乱换工具"；或提升后后续轮次长期复用该前缀。

**变体 B′（通用分发器）**：常驻 `call_tool(name, args)` 通用入口，模型不带 schema 直接传参——省掉"多一次往返"，但失去参数引导、错误率上升（可能反而多轮），且服务端必须按真实 schema 校验。列为备选，不默认启用。

- **技能化**（现状可用、无需新协议）：`/skill` 注入 = `WithTurnPinnedTools` 回合叠加（`turn_context.go:38-47`）+ 回合 system 消息（先例 `actor.go:2991-2994`）；把低频工具用法写进技能文档、按回合 pin 其定义。同样会改变该回合前缀（sanctioned overlay，§5.1 豁免），需量化技能文档 token 成本（读技能 vs 常驻 schema 的盈亏平衡点）；
- **PTC / SDK 化应作为第二期正解候选**（v2 修正：v1 曾判"不引入"）：它用"一个 wire schema + SDK 声明"替代"N 个工具 schema"，正是参考项目在"调用时"省 schema 的路径；代价是需要 PTC-only 呈现与 SDK 生成（`lib/ptc/contract.js` 单一 contract 是参考实现）；引入前需评估本项目 provider 是否支持等价的代码执行通道。
- **tools-pin 已废除（2026-10-15 实施裁决）**：对照 Codex（`codex-rs/ext/skills`：skill=user-role `<skill>` 文本片段 + developer `<skills_instructions>` 目录，零 tool 绑定；其 skills 工具是常驻的 list/read 目录浏览器，非激活时 pin），确认 skill=知识指引而非能力授权。`/skill` 不再物化 `PinnedTools`（`resolveSkillTurnPin` 只出 Guide + 收窄声明；`overlayPinnedFunctions` 只做 allowed/disallowed 减法）；`WithTurnPinnedTools` 通道保留（不填充）。护栏：`TestSkillTurnDoesNotChangeRequestToolSurface`（guide-only skill 的请求函数面与基线逐字节一致）+ `TestOverlayPinnedFunctionsOnlyNarrows`。效果：`/skill` 激活不再产生 tools 前缀 miss（guide 走回合尾部，缓存零影响）；工具型能力需求走 MCP/常驻函数面治理（§9 第 33 条）。

### 4.5 与 profile 的分工（v2 补充，2026-10-15）

结论：**profile 定"授权范围"，本方案定"暴露面"，执行期拦截不变**；二者是包含关系（授权面 ⊇ 暴露面），不是并列的"加载器"。

| 层 | 时间点 | 归属 | 职责 |
| --- | --- | --- | --- |
| 授权范围 | 会话/agent 构建期 | profile 解析链：`profile.Resolve`（`resolver.go:10`）→ `MergeToolPolicies`（`merge.go:153`）→ `profileinput.BuildToolExecutionPolicy`（`inputs.go:166`）；双落点 `applyAgentExecutionPolicy`（`handler.go:5568`）/ `buildLocalChatToolPolicy`（`chat_actor_host.go:2767`） | allowlist / denylist / read_only / sandbox（`ResolvedToolPolicy` 字段集） |
| 暴露面 | 装配期（回合冻结前一次成型） | 本方案层一~层三 | 组启停 / schema 瘦身 / 路由表；**只做减法，不得扩权** |
| 执行拦截 | 运行时每调用 | `ToolExecutionPolicy`（既有） | 权限/沙箱/审批语义，不变 |

要点：

1. **组配置挂在 profile 链上**：新增字段经 Resolve/Merge 融合后落到既有策略字段与 ShouldList 条件，避免第四套判定；必须同时接两个落点（`profile_tool_policy_guard_test.go` 已有等价性护栏）；字段级设计见 `profile-tool-groups-design.md`（`tools.disabled_groups` 草案）；
2. **冲突优先级（建议，阶段 3 定稿）**：profile 显式 deny > 组关闭 > allowlist/默认；组关闭永不放宽授权；
3. **换面时机**：`BuildToolSurfaceEligibilityKey` 已含 policy；profile 变更 = 新 key = 新会话/新 cache generation（§5.3）；
4. **目录变化复用既有通道**：MCP 晚到工具先例——`syncLocalChatToolPolicyAllowlist`（`chat_tool_policy_sync.go`）+ 稳定面失效（`chat_mcp_surface_invalidation.go`），回合边界生效；
5. **观测**：`policy_denied` 与 `group_disabled` 必须可区分（§4.1 词表）。

### 4.6 加载颗粒度与交互时序（v2 补充）

**颗粒度矩阵**（谁在哪一层、以什么单位生效）：

| 维度 | 单位 | 归属 | 生效时间 |
| --- | --- | --- | --- |
| 授权 | 工具名（allowlist / denylist） | profile → ToolExecutionPolicy | 会话 / agent 构建期 |
| 授权 | 能力域（capability scope） | 策略装配 | 同上 |
| 授权 | MCP 服务器（use / exclude_servers） | profile.mcp | 同上 |
| 暴露 | **工具组**（`disabled_groups`） | 本方案层一 | 装配期（回合冻结前） |
| 暴露 | 工具定义字段（description / 参数子集） | 本方案层二 | 同上 |
| 暴露 | 路由表条目 | 本方案层三 | 会话装配一次（exec 路径按运行） |
| 暴露 | 单个工具（按需 pin / 技能化） | 本方案层四（可选） | 回合边界（sanctioned 叠层） |
| 执行 | 单次调用（沙箱 / 审批 / 只读） | 既有 | 运行时每调用 |

**渐进粒度的四档**（"渐进"不是一个点，而是一条梯度）：

| 档 | 粒度 | 机制 | 状态 |
| --- | --- | --- | --- |
| 1 | 进程 / 宿主 | 内置工具注册、MCP 连接 | 现状 |
| 2 | 会话 | 组启停 + 瘦身（层一 / 层二） | 第一期 |
| 3 | **回合** | **A 回合边界预取**（第二期 A） | 雏形已存在：`shouldRefreshStableToolSurface` 的 MCP 晚到 / legacy 投影刷新分支（`loop.go:4398-4403`） |
| 4 | 调用 | B 回合内提升 / B′ 通用分发器 | 第二期 B（可选） |

**A 是否算渐进式**：算。A 满足"不是一次性给全、而是按需放出"，只是它的"按需"由**运行时启发式推断**（非模型请求）、粒度是**回合**（非调用）。第二期 A 的实质是把仓库里**已有的**"晚到工具回合边界重同步"缝隙从"补漏"推广为"预取"——不是新建机制。

**关键结论**：第一期**没有"调用级加载"**——不存在"模型请求加载某工具"的协议；能力只能通过会话 / profile 变更（新会话或回合边界）获得。

**交互时序**（会话路径）：

```text
profile 解析(Resolve/Merge) → ToolExecutionPolicy(含 disabled_groups)
      ↓
会话构建: 目录采集(CollectToolCatalogDefinitions) → eligibility key
      ↓
首个 think(): computeAvailableTools
   四源采集 → 策略过滤(AllowsDefinition: deny / 组关闭 / 只读) → ShouldList
   → optimizeModelToolSurface(瘦身) → [goal 投影] → 预算压缩 → name 排序
      ↓
   冻结: freezeToolSurfaceForTurn → SaveTurnToolSurface(会话稳定面 + 回合 freeze-once)
      ↓
请求装配: system 前缀(路由表) + tools 数组(冻结面) + 回合叠层(pin / routing 追加)
      ↓
后续 step: 命中快照(逐字节不变) → preflight / compact 保留 tools 前缀
      ↓
回合结束 → 下一回合: 稳定面复用; 目录变化(MCP 晚到)经 InvalidateStableToolSurface
   在回合边界重同步(策略白名单 sync + 稳定面失效) → 新回合重新冻结
      ↓
切换 profile: 新 eligibility key → 新会话 / 新 cache generation（不中途换面）
```

**与 LLM 的边界**：LLM 不参与装配，只消费两个产物——system 前缀中的路由表与 tools 数组。被关闭组的工具对模型**不存在**（不是"被拒绝"），因此不产生"试错 → 被拒 → 换工具"的浪费；代价是模型无法感知"能力存在但当前不可用"（除路由表显式说明）。两个真源（路由表 vs tools 数组）必须由 `f(静态输入)` 程序化生成 + CI 断言防漂移（§4.3）。

### 4.7 真实工具加载 vs 定义装配（边界声明，v2 补充）

本方案只改**模型可见面（定义装配）**，不改**真实工具加载**（可执行实体的注册与连接）——两者是独立链路：

| 来源 | 真实加载触发点 | 时机 | 环境 |
| --- | --- | --- | --- |
| 内置 toolkit | `tools.NewDefaultManagerWithRuntimeConfig` → `registerBuiltinToolkitTools`（`tools/manager.go:74-98`） | 宿主启动（`runtime-server/main.go:1758`）或会话/工作区构建（`profile_support.go:531/538/575`、`chat_setup.go:487`、`chat_mcp_session_scope.go:473`） | 宿主进程内 |
| MCP | `mcp/manager.Start/StartAsync`（`:242/:252`）→ `connectMCP`（`:381`）→ `connectClient`（`:625`）→ `loadTools`（`:632`） | 会话工作区 MCP manager 创建时（`session_workspace_mcp.go:208-216`；`runtime-server/main.go:1217-1222`） | 子进程 / 远端 server；**异步** |
| 编排（broker） | broker 构造 + handler 注册 | agent 构建期 | 宿主进程内 |
| skill 工具 | `chat/actor.go:2991-2994` `WithTurnPinnedTools` | **回合边界** | 宿主进程内（skill executor） |
| LSP | `tools/lsp_bridge.go:267-273`（`bridge.Enabled()` 时） | 宿主构建期 | 宿主进程内 |

执行触发点：`policy.AllowToolCallWithContext`（`policy/tool_policy.go:277-285`）→ `tools.Manager` 分发（内置 toolkit / MCP `CallTool`（`mcp/manager/manager.go:765`）/ broker handler / skill executor）。

两个必须记住的交互点：

1. **MCP 异步加载完成 = 目录变化**：晚到工具在**回合边界**经"策略白名单重同步 + 稳定面失效"进入可见面（§4.5 要点 4）；
2. **`code.*` 分组是动态的**：`tools/manager.go:117-119` 每次 `ListTools` 前 `codeGate.sync` 按最新陈旧度评估分组（ADR-0004），属"会话内动态生效"的真实工具面变化——披露方案的组启停必须与它正交（不要用 `disabled_groups` 表达 `code.*` 的可用性）。

## 5. 缓存稳定性守则（硬约束，v2 修订）

> **实测校准（2026-10-15，workbuddy cn:glm-5.3-flash，工具 `cache-probe` + 结果 `cache-behavior-probe-20261015.md`）**：块前缀缓存把 tools 序列纳入哈希——追加 tool 与改 system 尾段**同为一次首发 miss**（对称，无"追加免费"层）；失效一次性（重发即 ~99% 命中）；旧前缀条目按内容寻址存活、不互逐。据此得出操作规则：
>
> 1. **回合中途 tools 与 system 前缀一律冻结**（freeze-once；唯一例外 `/skill` pin——sanctioned 但按次计价 ≈ 一次全价 input）；
> 2. **回合边界允许变化，但 tools 与 system 必须同步、一次性完成**（共享同一次 miss；本方案的 guidance gating 天然满足——撤段与策略收窄同刻发生）；
> 3. **变化价值门槛**：能力解锁 / 策略收窄 / MCP 补齐 → 值得；装饰性微调（按 turn 改 description、频繁增删工具）→ 不做（miss 成本 > 收益）；
> 4. **不必为"复用旧缓存"回避切换**：A↔B 前缀来回切各有条目，代价只是各自首次 miss。

1. **确定性排序（修订）**：基础面 name 排序已实现（`loop.go:4340`、`tool_surface_binding.go:168`）；回合叠层（turn pin / main-agent routing，`loop.go:2237-2245`）追加在冻结面之后，**豁免 name 排序**，但须满足：同回合内"定义内容 + 追加顺序"逐字节稳定（测试定格：`turn_pin_overlay_test.go:13-43`）。新叠层不得插队 base 面内部；
2. **前缀只依赖静态因子**：审查范围 = **整个 system 消息前缀 + tools 数组**（v1 只盯路由表）；禁止时间戳/配额余量/动态计数混入，含对既有片段（环境能力探测等）的字节稳定性复审；
3. **会话内零重注册（改为沿用既有冻结语义）**：回合 freeze-once（`tool_surface_context.go:97-111`、`chat/turn_tool_surface_snapshot.go:83-114`）；策略变化由 eligibility binding 记录 + 执行期拦截，不改写已冻结前缀；目录变化（MCP 异步建连/热重载）经 `InvalidateStableToolSurface` 在**回合边界**生效；
4. **disable_tools 例外**：目录保持冻结，只翻 `tool_choice=none`（`loop.go:2334-2339,4293-4295`）——任何组启停/瘦身逻辑走同一守卫；
5. **旁路一致性**：compact 请求保留 tools 前缀（`compactruntime/local.go:507-528`）；子代理只读面按既有策略另建，不做中途改写；
6. **验证手段（强化）**：golden 字节测试——同配置双装配的 tools JSON + system 前缀 diff 为空；复用 `ToolDefinitionsFingerprint`；覆盖 4 类路径（exec / chat actor / runtime-server inline / compact）+ pin 叠层 + 同进程二次装配（捕获 map 序不稳定）。

## 6. 实施计划（v2 重排）

### 阶段 1（1~2 天）：确定性加固与路由表原型
- [ ] golden 字节测试：4 类宿主路径 + pin 叠层（删除 v1"排序推广"——已完成）；
- [ ] Fragment 顺序决策：先产出"生产者 + 相对顺序"清单，再定是否引入 order；
- [ ] 路由表原型：单组开关下验证 body 确定性（双装配 diff 空 + 行序断言）。

### 阶段 2（2~3 天）：schema 瘦身扩展
- [ ] Compact 覆盖剩余 token 大户（spawn_subagents、job 家族、schedule、mesh 类 MCP）；
- [ ] 收口与预算压缩的先后（冻结前一次成型）；
- [ ] 观测端点输出 compact 前后对比（复用 tool_schema 事件与 toolSchemaTokens 元数据）。

### 阶段 3（3~5 天）：组配置化
- [ ] 组字段设计草案定稿（`profile-tool-groups-design.md`：`tools.disabled_groups` + 组注册表 + 校验规则）；
- [ ] 分组表 + 映射到 profile / ToolExecutionPolicy / ShouldList（不新建判定）；
- [ ] 回合边界刷新语义接线（复用 InvalidateStableToolSurface）；
- [ ] 观测解释词表上线（group_disabled / policy_denied / …）。

### 阶段 4（可选）：技能化（/skill 注入通道）+ 调用时 schema 后补调研。

### 度量
- 基线口径：请求级 `tool_schema_tokens`（已有）+ registry §2.2 结构数据（86=25+30+31）；
- 目标：多代理会话 ≥40% 降幅、普通会话 ≥20%（保留 v1 目标，实施前先补基线实测）；
- 辅助：缓存命中（网关侧可观测）、工具误用率（报错后换工具绕行抽样）。

## 7. 风险与对策（v2 修订）

| 风险 | 对策 |
| --- | --- |
| 瘦身后模型漏看关键约束导致误调用 | 短语表保留安全关键句；golden 含关键词断言 |
| 分组过激导致能力缺失投诉 | 默认保守（只关 mesh/lsp 等低频组）；观测解释词表（§4.1） |
| 路由表与实际工具集漂移 | 行=f(静态输入) 程序化生成 + 行序固定；CI 断言一致性 |
| 回合叠层（pin/skill/routing）被误判为"前缀破坏" | §5.1 显式豁免 + 现有测试定格；新叠层走同一评审口径 |
| golden 覆盖不全（新增） | 断言范围 = 整个 system 前缀 + tools 数组；覆盖 4 类路径与同进程二次装配 |
| 缓存仍因 provider 侧因素失效 | 装配层只保证我方前缀确定；配合 usageanalytics 观测 provider 命中 |
| v1 引用的 dsh-ops 97% 数据不可复核（新增） | 仅作方向参考；收益以本仓库 tool_schema 实测为准 |

## 8. 与 dsh-ops 的差异说明（v2 修正）

- dsh-ops 是插件，只能在注册点之上做披露控制；本项目是宿主，可直接在装配源头控制。本项目**已有**装配期策略过滤（`filterPolicyBlockedToolDefinitions`）+ 执行期拦截双层——本方案**复用**该双层而非新建遮罩；dsh-ops 的 reconcile 补偿也不需要——冻结/失效机制已存在（§5.3）；
- **修正（2026-10-15 核查）**：dsh-ops 确实有 restrict/deny 遮罩（`shellPolicy: deny-host-shell` + `deniedHostTools`，`lib/policy.js` `hostShellRefusal`），只是本项目不需要新建；其"未启用组件"是**发布源头不产生**（服务器级开关），比"注册后过滤"更彻底——本项目在 MCP 侧可用 `mcp.use_servers` 达到同等效果，内置工具侧需要组门控（§4.1）；
- **PTC 判断修正**：v1 写"暂不引入"，但 PTC 是参考项目解决"调用时 schema 成本"的正解（一个 wire schema + SDK 声明 + plan/resume 预检 + 有界引用），应作为第二期候选重新评估（§4.4）。

## 9. 修订记录（v1 → v2，2026-10-15）

核查方式：v1 全部关键论断逐条对照代码（行号为核查时点）与 registry 文档。v1 原文存档：`progressive-tool-disclosure-plan.v1-20261014.md`。

| # | v1 表述 | v2 修正 | 依据 |
| --- | --- | --- | --- |
| 1 | "需把排序推广到请求装配路径" | 已完成，该项删除 | `loop.go:4340`、`tool_surface_binding.go:168` |
| 2 | 未盘点冻结/缓存代际/稳定面 | 补入 §3，方案改为复用 | `tool_surface_context.go:97-111`、`chat/turn_tool_surface_snapshot.go`、`loop.go:814-822` |
| 3 | 未盘点模型面既有瘦身 | 补入 §3；层二范围收敛为剩余大户 | `loop.go:4568-4805` |
| 4 | "grep 非对称 context 可删除" | 方向相反：现实现保留 context | `loop.go:4672-4702` |
| 5 | "会话冻结首帧装配快照"（新建） | 改指既有 StableToolSurface / freeze-once | §5.3 |
| 6 | tools 数组"必须按 name 排序" | 改为"确定性排序 + 叠层豁免" | `loop.go:2237-2245`、`turn_pin_overlay_test.go` |
| 7 | 分组"复用 Classify 即可" | Classify 粒度粗，需第二键；映射既有三套机制 | §4.1、`tool_list.go:26-68` |
| 8 | "internal/capability 内声明分组" | 命名已占用，建议 toolgroups/configs | `internal/capability/capability.go` |
| 9 | background job 同列 core.shell 与 orchestration | 归 orchestration | `toolbroker.IsBrokerTool` |
| 10 | "静态字符串常量"与"程序化生成"矛盾 | 定义为 f(静态输入)，行序固定 | §4.3 |
| 11 | 技能化未指名机制 | 指名 /skill → WithTurnPinnedTools 回合叠加 | `turn_context.go:38-47` |
| 12 | "权限在执行期判定，而非注册期" | 可见面也按策略裁剪（既有原则） | `loop.go:4288-4307` |
| 13 | dsh-ops 97% 降幅引用 | 标注"外部自测、不可复核" | — |
| 14 | "复用 tokenestimate" | 工具 token 走 llmRuntime.CountTokens | `loop.go:5765` |
| 15 | 非目标"不改变执行行为"歧义 | 改为"权限语义不变；可用面收敛是预期变化" | §2 |
| 16 | 缺宿主路径分治 | 新增 §4.0 路径矩阵 | — |
| 17 | （v2 补充）未说明 profile 与本方案的分工 | 新增 §4.5：profile 定授权范围、本方案定暴露面、执行拦截不变 | `profile/merge.go:153`、`profileinput/inputs.go:166`、`profile_tool_policy_guard_test.go` |
| 18 | （v2 补充）组字段无设计输入 | 新增阶段 3 前置草案 `profile-tool-groups-design.md`（`tools.disabled_groups`） | `profile/spec.go:79-85`、`policy/specifier.go:53-97` |
| 19 | （v2 补充）缺颗粒度 / 时序说明 | 新增 §4.6：颗粒度矩阵 + 交互时序 + LLM 边界；草案 v0.2 定稿 6 项决策 | `profile-tool-groups-design.md` §8 |
| 20 | （v2 补充）未区分真实工具加载与定义装配 | 新增 §4.7 边界声明：五类来源的加载触发点 + 执行触发点 + 两个交互点 | `tools/manager.go:74-98,117-119`、`mcp/manager/manager.go:242/381/625/632/765`、`chat/actor.go:2991-2994` |
| 21 | （v2 补充）"按需加载"缺落地流程 | 层四补：第一期不替代 schema 的澄清 + 第二期候选流程（发现入口 → append-only 提升 → epoch 断代 → 执行期仍判定） | `tool_list.go:70-96`、`turn_context.go:65-104`、`loop.go:2250-2254` |
| 22 | （v2 补充）未说明"如何省 token"（易被误读为第一期只发摘要） | 新增 §2.1：三杠杆 + 可选按需加载 + "省 token ≠ 省成本"/收益随会话形态分化 | `docs/architecture/unified-tool-surface-registry.md` §2.2 |
| 23 | （v2 补充）路由表被当作"净新增内容" | §4.3 补：现状已有 6 处 guidance 注入（归并对象）+ 启用判据（可跳过）+ 权威性排序 | `chat_session.go:2622-2634`、`agent_context.go:54,62`、`instruction_messages.go:178,182`、`environment_capabilities.go:568` |
| 24 | （v2 补充）第二期只有"回合内提升"一种形态 | §4.4 补：时机 A 回合边界预取（零往返零断代，复用既有刷新缝隙）+ 5 类确定性启发信号 + 守卫 | `loop.go:4398-4445,4447+`、`turn_tool_surface_snapshot.go:32-42` |
| 25 | （v2 补充）B 的"成本"未量化、未给适用条件 | §4.4 补：四项成本来源（整段上下文往返 / 非一次性断代 / 历史污染 / 实现审计）+ 值得条件 + 变体 B′ 通用分发器 | `loop.go:2250-2254,2270-2280` |
| 26 | （v2 补充）未界定"A 是否算渐进式" | §4.6 补：渐进粒度四档（进程 / 会话 / 回合 / 调用）+ A 的性质（启发式推断、回合粒度、雏形已存在） | `loop.go:4398-4403` |
| 27 | （v2 补充）未说明 A 的触发判断实质与盲点 | §4.4 补：5 类信号中仅 1 类为预测（其余为补缺口/声明式）+ LLM 不感知 + 盲点（absent 不广告→学不到需求）+ 变体 A′（request_tool + 回合边界生效） | `loop.go:4398-4403`、`simpleGoalToolNames`（`:4447+`） |
| 28 | 引用 dsh-ops"减少 97% token" | 删除；改为核查数据：组件门控 -28.2%、描述压缩 -47.5%，且参考文档明确"不代表整任务 token" | dsh-ops `docs/schema-measurement.md` |
| 29 | 第二期设计为运行时按需加载（A/A′/B） | 降级为探索记录：参考项目不采用，其出路是"替代工具或请 operator 改策略"；正解候选改为 PTC / SDK 化 | dsh-ops `lib/policy.js`、`lib/tools.js`、`lib/ptc/contract.js`、`docs/native-ptc.md` |
| 30 | 层三"归并替代"缺参考实现 | §4.3 补参考实现：组件所有权 prompt 段同生共死 + `SECTION_ORDERS:3250` + absent 不广告 + 表头随组件 | dsh-ops `lib/policy.js` `renderToolingPolicy`、`docs/configuration-reference.md` §Prompt registration |
| 31 | 未评估"profile / policy 是否已覆盖" | 新增 §10：覆盖度对照 + 裁决（层一降级为复用 `Capability` 域、层三升为第一优先、层四不做） | `policy/capability.go`、`policy/taxonomy.go`、`profile/resolved.go`、`agentdef/builtin.go` |
| 32 | 层一新建组体系（`internal/toolgroups` + 组词汇） | 取消；改为复用既有 `Capability` 8 域 / `ToolTaxonomy.Kind`，最多把域暴露到 profile | `policy/capability_scope.go`、`policy/taxonomy.go:100-111` |
| 33 | `/skill` 激活时 pin skill 函数与程序进请求 tools | **废除**（对照 Codex：skill=纯文本指引，无 tool 绑定）；`/skill` 只注入 guide（回合尾部，缓存零影响）+ allowed/disallowed 收窄；通道保留不填充 | `chat_skill_turn.go`、`chat_actor_executor.go`、`chat_skill_pin_abolition_test.go` |

---

## 10. 实施必要性裁决（2026-10-15，对照 profile / policy 现有能力）

**问题**：参考项目验证过的几件事，本项目是否已有等价物？还有多少需要实施？

### 10.1 覆盖度对照

| 参考项目手段 | 本项目等价物 | 状态 |
| --- | --- | --- |
| 组件开关（发布源头不产生） | `mcp.use_servers` / `exclude_servers`（MCP 服务器级：不连即不发布） | 已有 |
| 逐工具门控 | `profile.tools.allowlist` / `denylist` | 已有 |
| 只读裁剪 | `profile.tools.read_only` + 只读子代理面（写工具不注册） | 已有 |
| 角色预设名单 | `agentdef` 内置角色（explore / plan / general 各带 Tools + DisallowedTools） | 已有 |
| 工具清单单一来源 + 校验 | `policy.LookupToolTaxonomy` / `KnownToolTaxonomyNames`（`profile validate` 消费） | 已有 |
| 能力域（族级）门控 | `policy.Capability`（8 域）+ `ToolExecutionPolicy.CapabilityScopeEnabled` / `AllowedCapabilities` + `IntersectAllowedCapabilities` | 机制已有，但 **profile 不可声明**（`ResolvedToolPolicy` 仅 4 字段） |
| 工具分类表（Kind） | `policy.ToolTaxonomy.Kind`（read / search / network / edit / exec / control）+ ReadOnly / MutatesFS / RequiresNet | 已有（目前未被门控使用） |
| **prompt 段与工具集同生共死** | 无 | **缺**（6 处 guidance 无条件注入） |
| 描述压缩（短语表） | `optimizeModelToolSurface` 仅覆盖 shell/grep 别名与 grep 参数表 | 部分 |
| 缓存 / 冻结 / 账本 | turn freeze + 会话稳定面 + eligibility key + promptCacheEpoch | 已有（超出参考项目） |
| PTC / SDK 化 | 无 | 缺（第二期候选） |

### 10.2 裁决

| 层 | 裁决 | 理由 |
| --- | --- | --- |
| 层一 `disabled_groups` + `internal/toolgroups` | **降级**：改为"把已有 `Capability` 域暴露到 profile"（1 字段，复用 `CapabilityResolver` / `AllowedCapabilities`）；**取消新建组词汇与注册表** | 族级门控的机制与数据表都已存在（Capability 8 域 / Kind 6 类），再建第三套是重复；仅"配置便利性"不足以支撑新体系 |
| 层二 短语表压缩 | **保留，优先** | 参考实测 -47.5%（描述类）；本项目工具基数大，绝对收益更大；风险最低 |
| 层三 prompt 段所有权 | **保留，升为第一优先** | 这是**当前的真实缺陷**：工具被关 / 被裁剪后，指引仍无条件注入，模型被指引去调用不存在的工具 |
| 层四 / PTC | **不做**（第二期候选） | 参考项目不采用按需加载；PTC 需 provider 代码执行通道，先验证 |
| 缓存加固（§5） | **不做**（已具备） | 现有冻结 / epoch / binding 已覆盖；方案只做"别破坏"的护栏 |

### 10.3 最终实施范围（缩水后）

1. **前置 → 已完成（2026-10-15）**：基线实测落地为 `backend/cmd/tool-schema-measure` + 结果文档 `tool-schema-baseline-20261015.md`。核心数字：原始面 31 工具 / 87.2KB / ≈21.8k tok；**别名收口（已上线）单独贡献 -32.1%**（grep+rg 占原始面 47.4%）；orchestration 关停 11.6%；network off 仅 1.7%；
2. **第一优先**：prompt 段与工具集 / 组件同生共死（现有 6 处 guidance 的注入条件改为"该能力域已启用"，关闭时一并撤段）；
3. **第二优先 → 已实施（2026-10-15）**：`aicli_exec`(28→11 参数) / `shell`(12→8) / `view`(6→5) / `todos`(描述短语化) 白名单压缩落地于 `optimizeModelToolSurface`（共享实现 `compactToolPropertiesForModel`）；golden 断言（白名单集 + wire 字节上限）见 `compact_tool_wire_test.go`。生产面估算：收口+压缩后 **~36.8KB（相对原始面 -57.9%）**；`job_start`/`edit` 待实测余量决定（收益 <1.5KB，暂缓）；
4. **可选 → 已实施（2026-10-15）**：`tools.disabled_capabilities` 落地——profile 声明层（`ToolPolicySpec`/`ResolvedToolPolicy` 双镜像 + Merge 并集归一化 + validate 域名校验）→ `profileinput.BuildToolExecutionPolicy`（未知域报错；翻译为 `SetCapabilityScope(全集−disabled)`，`read_only`/`exec_shell` 为底座域不可禁用）→ 双落点（**顺带修复真实缺陷**：`applyAgentExecutionPolicy` 此前丢弃 capability scope，现已传播；TUI `session.ToolPolicy` 走 `Clone()` 天然保留）。护栏：`TestProfileToolPolicyDisabledCapabilitiesEquivalence`；e2e：真实 taxonomy 工具（web_search/job_start 被拒，view/shell/write 通过，子代理能力交集不得放宽）。eligibility key 已含 `allowed_capabilities`（`tool_surface_binding.go:86`）→ 换配置自动换 cache generation，缓存侧零新工作；
5. 不做：新建组注册表、运行时按需加载、PTC。
6. **目标修订（依据基线）**：普通单代理会话的 20% 降幅可能不达（别名收口后剩余空间有限），以真实 `tool_schema_tokens` 元数据复核后定档；多代理场景（MCP + scheduler 在场）40% 目标有望由"已上线收口 + orchestration 门控 + 短语压缩"叠加达成。

*本方案与 `docs/architecture/unified-tool-surface-registry.md` 的"剩余主线"（§15.5）保持同线；实施时以该文档的收敛状态为前置。*
