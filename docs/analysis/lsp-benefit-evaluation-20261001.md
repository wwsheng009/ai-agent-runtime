# LSP 集成收益评估（2026-10-01）

> 定位：评估报告（analysis），不是设计文档；不改任何 schema、不改代码行为。
> 数据窗口：2026-09-29T22:50:31Z → 2026-10-01T01:53:39Z（脚本 14 天窗口，事件实际始于 LSP 埋点上线）。
> 事实源：`lsp.request.finished` / `lsp.server.state` / `tool.completed` 的 A 通道落盘事件
> （`~/.aicli/chat-logs/**/runtime-events.jsonl`），共 1829 个事件文件 / 583,757 行。
> 口径锚点：`docs/plan/lsp-observability-and-analysis-plan-20260929.md` §3.3/§4.3、
> `docs/knowledge_Layer/adr/0003-exploration-attribution-metrics.md`（阈值不得写死）。
> 复现：`py -3 scripts/analyze-lsp-baseline.py --days 14 --out <out.md> --json <out.json>`；
> 分段口径见本报告 §8（构建代际切分、闭环、分桶统计）。

---

## 0. 结论摘要（TL;DR）

| # | 判定 | 关键证据（窗口内） |
| --- | --- | --- |
| 1 | **直接收益已可量化，但当前体量小**：诊断注入 40 次 / 83 条，全部来自 gopls；命中率 40/40 = 1.0 | §3.2；888 次请求中 injected 40（4.5%） |
| 2 | **闭环有效**：被注入的诊断在下一次同文件编辑后消失（转 clean）2/3，闭环时延 2.1s / 2.9s；第 3 条尚无后续编辑 | §3.3；另有历史回填 2/2、实机轮 1/1（样本重叠，不可重复计数） |
| 3 | **收益以「零额外工具轮次」到达**：887/888 次触发来自编辑回执的内联追加，显式 `lsp_diagnostics` 仅 1 次；注入等待 P50 165ms / P95 643ms | §3.1/§3.4；`manager.go:176-194` |
| 4 | **覆盖面受「默认关闭 + 缺二进制」限制**：133 个有工具事件的会话中仅 12 个（9.0%）产生 LSP 请求；14 天窗口的 5708 次编辑中仅 978 次（17.1%）落在 LSP 活跃会话内（5708 含 LSP 上线前的编辑，比值偏保守）。pyright / typescript 贡献 112 次请求、0 条诊断 | §3.1/§4.3；`spec.go:315-318` 默认 `enabled=false` |
| 5 | **成本主要在等待与协议噪声**：全窗口等待 313.5s（旧构建 286.5s / 新构建 27.0s）；旧构建 277 次打满 ~1s 预算（clean 145 + no_fresh 132）；追加 125,078B 中仅 10.8% 是诊断正文，89.2% 是提示/空结果文本（去重后新构建显著下降） | §4.1/§4.2 |
| 6 | **间接收益最确定：观测→优化闭环已回本**：clean 等待 P50 1000→269ms、starting 1000→250ms、降级提示带字节比例 100%→33%、空结果块 168→130B/次；崩溃归因与重启窗口落地（窗口内 gopls 崩溃 16 次，根因为外部内存耗尽） | §5；部分数字来自 `plan §5.8` 实机轮 |
| 7 | **净判定**：直接收益「小而真」，间接收益「确定且已回本」。放大直接收益的前置条件：补 pyright/ts 二进制、压低冷视图 no_fresh 的 1s 等待、评估默认开启策略（阈值待积累，不写死） | §6 |

**一句话**：LSP 在「编辑 → 诊断 → 修复」这条链路上已被证明可用且便宜（热态 P50 165ms、闭环 2–3s），
但当前真实收益规模被 opt-in 默认关闭、缺失语言服务器与冷启动等待压住；本轮最确定的收益来自观测本身
——它把等待、噪声、崩溃从「不可见」变成「可复算、可优化」。

---

## 1. 评估框架与方法

### 1.1 收益的四个维度

| 维度 | 问题 | 指标（口径见 §8） |
| --- | --- | --- |
| ① 可达性 | 编辑时是否真的触达了 LSP？ | `lsp_edit_coverage_ratio`、触发方式分布、会话覆盖 |
| ② 有效性 | 触达后是否产出诊断、诊断是否被消除？ | `lsp_diag_hit_ratio`、`lsp_closure_ratio`、注入明细 |
| ③ 成本 | 等待、字节、降级噪声、可靠性代价是多少？ | 等待分位与总等待、打满预算次数、追加字节构成、fallback、server 状态 |
| ④ 间接收益 | 观测是否产生了可复算的优化闭环？ | 优化轮前后对比（构建代际切分） |

### 1.2 数据与样本

| 项 | 值 |
| --- | --- |
| 窗口 | 2026-09-29T22:50:31Z → 2026-10-01T01:53:39Z |
| 扫描 | 1829 个 `runtime-events.jsonl` / 583,757 行 / 0 损坏 |
| LSP 请求 | 888 次（inline 887 / tool 1） |
| LSP 活跃会话 | 12 个；同期有工具事件的会话 133 个（其中 121 个无任何 LSP 请求） |
| 编辑调用 | 14 天口径 5708 次（`tool.completed` 按事件 timestamp 过滤，含 LSP 上线前）；LSP 活跃会话内 978 次 |
| 构建代际 | 旧构建 827 请求 / 新构建 61 请求（切分规则见 §8.1） |

**窗口的真实含义**：埋点 2026-09-29 晚间才落盘，因此「14 天窗口」实际覆盖约 27 小时、跨三代构建；
这不足以标定阈值，只够回答「收益是否存在、量级如何、成本在哪」。

### 1.3 纪律与反事实声明

- 未采集项一律 `n/a` + 原因，**不渲染为 0**；不做阈值告警（ADR-0003 D4）。
- **反事实不可测**：本窗口没有「同一任务关闭 LSP」的对照组，因此不能声称「LSP 让任务快了多少」；
  闭环率是过程代理（诊断消失），不是因果结论。
- 窗口跨构建、含实机验证脚本产生的流量；injected 40 与 closure eligible 3 均属小样本，只作方向性判读。

---

## 2. 收益是怎么产生的（机制）

LSP 的收益路径只有一条主链路 + 一个可选工具面：

| 路径 | 触发条件 | 行为 | 证据 |
| --- | --- | --- | --- |
| **A. 编辑内联诊断（主路径）** | 工具成功 + metadata 含 `mutated_paths` + LSP 已启用 | `Bridge.AppendToResult` → didOpen/didChange/didSave → 等待诊断 → **追加到原工具回执尾部** | `internal/tools/manager.go:176-194`、`internal/lsp/bridge.go:187-…` |
| B. `lsp_diagnostics` 工具（opt-in） | `diagnostics.toolEnabled=true`（默认 false） | 与 A 同管线，按文件显式查询 | `internal/tools/lsp_bridge.go:20-28`、`spec.go:95-97` |
| C. `lsp_servers` 工具 / `/lsp` 命令族 | 用户或模型显式调用 | 池状态/诊断/重启；不产生诊断收益 | `internal/tools/lsp_bridge.go:26-28`、`chat_lsp_command.go` |
| D. 启动期 bootstrap | aicli chat 启动时扫描工作区 | 写 `.aicli/runtime.yaml`，决定哪些 server 参与 | `chat_lsp_bootstrap.go` |

**关键设计收益**：主路径把诊断追加在编辑工具的回执里，模型在**同一次观察**中看到编译/类型问题，
不需要额外发起工具调用、不需要额外一轮 LLM 交互。窗口内 887/888 次触发为 inline、显式工具调用仅 1 次，
说明收益确实是「搭车」到达的（成本与收益同一次工具调用内结算）。

代价是编辑调用需要等待（有界预算），以及回执字节增加——这两项在 §4 量化。

---

## 3. 收益读数

### 3.1 可达性：触达与覆盖

| 指标 | 值 | 样本 |
| --- | --- | --- |
| `lsp_edit_coverage_ratio` | **0.9070** | inline 887 / LSP 活跃会话内 edit 978 |
| 触发方式 | inline 887（99.9%） / tool 1 | 888 |
| LSP 活跃会话 | 12 / 133 个有工具事件的会话 = **9.0%** | 121 个会话有工具活动但零 LSP 请求 |
| 14 天全部编辑中的 LSP 触发面 | 978 / 5708 = **17.1%** | 5708 含 LSP 上线前（09-29 前）的编辑，比值偏保守 |

解读：
- 在**已启用** LSP 的会话里，编辑覆盖率 90.7%，主链路触达基本完整（缺口来自幂等/失败编辑，见 plan §5.8 第七轮审计）。
- 但 LSP 是 **opt-in 默认关闭**（`spec.go:315-318`），全窗口只有 9% 的会话、17% 的编辑进入收益面——
  这是当前「实际收益规模」的第一约束。

### 3.2 有效性：命中与注入

| 指标 | 值 | 样本 |
| --- | --- | --- |
| `lsp_diag_hit_ratio` | **1.0000** | injected 且 diag_count>0：40 / 40 |
| 注入次数 / 诊断条数 | 40 次 / **83 条** | 全部来自 gopls |
| 注入等待 | P50 **165ms** / P95 **643ms** | n=40（全窗口） |
| 注入追加字节 | 13,560B | 平均 339B/次 |
| 服务分布（请求） | gopls 617 / pyright 106 / typescript 6 | pyright 与 ts 的 112 次请求 **0 诊断**（全部降级） |

**命中率的诚实解读**：1.0 是「injected 必有诊断」的条件概率，不是「编辑必有诊断」的无条件概率。
无条件的诊断产出率是 40/888 = 4.5%；其中 gopls 为 40/617 = 6.5%。分母被大量 no_fresh / 降级稀释。

注入样例（窗口内 40 条明细见 §8 复现产物）：单次注入 1–7 条诊断、193–919B、等待 4–980ms；
同一会话（cxHK8mPD）在 09-30 白天有连续 26 次注入，说明热态 gopls 上诊断产出是稳定的。

### 3.3 闭环：诊断是否被消除

闭环口径：同会话 + 同 `path_fingerprint`，injected（带 `diag_fingerprint`）之后的下一次同文件编辑 outcome=clean。

| # | 会话 | 注入时间(UTC) | 诊断数 | 等待 | 下一次同文件编辑 | 闭环 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | QJDrIzkp | 09-30 23:33:59 | 1 | 221ms | clean（+2.1s） | ✅ |
| 2 | QJDrIzkp | 10-01 00:16:58 | 1 | 4ms | clean（+2.9s） | ✅ |
| 3 | tYkHLf8u | 10-01 01:25:21 | 3 | 429ms | 无后续编辑 | ⏳ |

- `lsp_closure_ratio` = **2/3 = 0.6667**（eligible 3，closed 2）。
- 历史样本：首次回填（窗口至 10-01T00:48Z）为 2/2；实机第一轮 1/1。**这些样本与本窗口的 2 条 closed 重叠
  （同为 QJDrIzkp），不可相加**；当前去重后的累计证据是 2 条 closed、1 条 open。
- 闭环时延 2.1s / 2.9s：即「编辑 → 看到诊断 → 下一次编辑修复」在秒级完成，链路是活的。
- 边界：诊断正文不落盘（隐私设计），因此无法从事件层判断「模型是否理解了诊断」；
  closure 只证明问题在后续编辑中消失。

---

## 4. 成本读数

### 4.1 等待成本

| 代际 | 请求 | 总等待 | 打满预算(≥990ms) | clean P50 | no_fresh P50 | injected P50 |
| --- | --- | --- | --- | --- | --- | --- |
| 旧构建 | 827 | **286,467ms** | **277 次（33.5%）** | 1000ms（n=146） | 1000ms（n=132） | 165ms（n=37） |
| 新构建 | 61 | **26,998ms** | 20 次（32.8%） | **269ms**（n=17） | 1001ms（n=20） | 221ms（n=3） |

- 旧构建的 277 次打满预算 = clean 145 次 × ~1s（确认「无问题」）+ no_fresh 132 次 × ~1s（**纯损耗**）。
- 新构建按路径优化生效：clean 1000→269ms、starting 1000→250ms、binary_missing 0–122ms；
  但 **no_fresh 仍 20 次 × ~1000ms**，占新构建总等待的 74%——这是剩余的最大单点成本。
- 成本效率（摊薄）：每交付 1 条诊断的总等待，旧构建 286,467ms/78 条 ≈ **3.7s**，
  新构建 26,998ms/5 条 ≈ **5.4s**（小样本；分子被 no_fresh 等待主导，不代表诊断本身昂贵——注入本身 P50 仅 165–221ms）。

### 4.2 字节成本

| 构成 | 旧构建 | 新构建 | 说明 |
| --- | --- | --- | --- |
| 诊断正文（injected） | 12,770B / 37 次 | 790B / 3 次 | 唯一「有效载荷」 |
| 降级提示（degraded/no_fresh/starting/binary 合计） | 82,438B / 490 次（100% 带提示） | 2,322B / 36 次（33% 带提示） | `hint_once` 去重生效 |
| 空结果块（clean） | 24,552B / 146 次（168B/次） | 2,206B / 17 次（130B/次） | 告知「无诊断」的协议文本 |
| 合计 | 119,760B（145B/请求） | 5,318B（87B/请求） | 新构建 **-40%** |

- 全窗口追加 125,078B，其中诊断正文仅 **13,560B（10.8%）**，其余 **89.2% 是提示/空结果等协议文本**。
- 追加字节占 LSP 活跃会话内编辑回执可见字节的 **19.3%**（125,078 / 646,753）——编辑回执整体变大近两成，
  但其中真正的诊断信息只有约 2%。
- 结论：字节成本不是「诊断太贵」，而是**降级与空结果的固定文案**；去重与自闭合已把它压下来（新构建 87B/请求）。

### 4.3 降级与可靠性

| outcome | 次数 | 主因 | 等待特征 |
| --- | --- | --- | --- |
| `no_server` | 159 | 文件不被任何池成员认领（如忽略目录/不支持类型） | 0ms（未尝试） |
| `degraded`（旧构建未分类） | 365 | 259 gopls（崩溃/重启预算耗尽，快速失败）+ 100 pyright + 6 ts（缺二进制被折叠） | P50 0ms |
| `degraded_no_fresh` | 152 | gopls 在预算内未发布（冷视图/模块加载中） | 1000ms |
| `degraded_binary_missing` | 6 | pyright 二进制缺失（新构建已细分类） | 0–122ms |
| `degraded_starting` | 3 | 冷启动握手期 | 250ms（快速失败） |

- 池状态：gopls starting 22 / ready 22 / **crashed 16** / stopped 2；pyright starting 9 / unavailable 9；ts starting 2 / unavailable 2。
- 16 次 gopls 崩溃的根因是**整机内存耗尽导致的外部终止**（Windows `Resource-Exhaustion-Detector`），
  不是 LSP 池缺陷；缓解（重启窗口 + 崩溃归因）已落地，见 plan §5.8「崩溃根因定位」。
- pyright / typescript 合计 112 次请求、0 条诊断——缺二进制是当前**最大的可行动降级源**。
- **口径已统一（2026-10-01，O3）**：基线报告（`internal/lsp/baseline/baseline_report.go`）与
  Python 脚本改用 `degraded / attempted`（排除 no_server）= **0.7215**（526/729），与运行时读数
  （`internal/lsp/metrics.go:172`）一致；历史 requests 口径值 0.5923 仅作对比注记。

---

## 5. 间接收益：观测 → 优化闭环（本窗口最确定的收益）

埋点（M1–M4）落地后，2026-10-01 完成了 11 轮数据驱动优化。以下只列**收益可归因、证据可复核**的部分；
第 9–11 轮在本报告数据窗口（至 01:53Z）之后落地，机制已由定向测试覆盖，**收益数字待下一窗口复算**：

| 优化 | 优化前（证据） | 优化后（证据） | 本窗口可复核？ |
| --- | --- | --- | --- |
| 空诊断提前采信 + 冷启动短等待 | clean 每次打满 1s（146 次 × 1000ms） | clean P50 269ms、starting 250ms | ✅ 事件对比（§4.1） |
| 降级提示去重（hint_once） | 490/490 降级族请求都追加提示（82,438B） | 36 次降级族请求仅 12 次带提示（2,322B） | ✅ 事件对比（§4.2） |
| 空结果块自闭合 | 168B/次 | 130B/次 | ✅ 事件对比（§4.2） |
| 重启预算滑动窗口 + 崩溃归因 | 预算耗尽后整场会话静默降级（rNo0YL1N 连续 106 次） | 静默期后恢复；事件新增 `reason_category`（crashed/binary_missing） | ⚠️ 机制已验证，恢复行为需新样本 |
| `tool_call_id`/`turn_id`/指纹埋点 | 请求无法与工具调用对齐；closure 不可算 | 新构建请求 100% 带 join 键；closure 可算（2/3） | ✅ 事件字段存在性 61/61 |
| 文档 LRU + didClose、增量 didChange、多 server 独立预算 | 常驻文档无上限、每次全量同步、慢 server 饿死同批 | 单测覆盖（`TestDocumentLRUEvictionSendsDidClose`、`TestIncrementalDidChangeSendsMinimalRange` 等） | ✅ 定向测试全绿（§8.1） |
| 诊断事件按集合指纹去重 | `lsp.diagnostics.updated` 1653 条 vs 请求 490 条 | 集合未变不再发布 | ⚠️ 本报告未复算该比值 |
| `first_publish_ms` 冷启动闭环 | 冷启动延迟不可得 | 事件新增字段 | ❌ 窗口内 0 样本（见 §7 数据缺口） |
| 路径级冷路径快速失败（第九轮） | 热连接上的冷路径连续 6 次各烧满 1000ms（§5.8 第九轮行） | 次编辑起只等 `cold_retry_ms`（250ms），首个发布即恢复 | ⚠️ 机制单测覆盖（`TestColdRetryAppliesPerPathOnWarmConnection`），live 待复算 |
| 冷启动延迟在单会话状态面可见（第十轮） | `first_publish_ms` 只在基线可见 | TUI 状态行 + web A 卡新增展示 | ✅ 展示面变更（`TestChatLSPServerStatusLineShowsFirstPublishLatency`） |
| 崩溃预算耗尽的结构化原因（第十一轮） | 真实崩溃原因＝原始进程错误（`exit status 0xffffffff`），不可分类、note 不可行动 | 状态/事件/note 统一为 `crashed; restart budget exhausted (restartLimit=N): <原始错误>` | ⚠️ 机制单测覆盖（`TestRestartWindowResetsBudget` 扩展），live 待复算 |

**观测红利本身的经济性**：优化前，旧构建 827 次请求花了 286.5s 等待、其中 277 次打满预算、
协议噪声 119.8KB；优化后新构建同量级操作的等待与噪声都显著下降（clean -73%、降级族提示 -97%、空块 -23%/次）。
这些数字全部由同一套事件复算得出——**可观测性把「LSP 是否值得、哪里该调」从主观判断变成了可复算问题**，
这是本轮评估中收益最确定的来源。

---

## 6. 结论与建议

### 6.1 净判定

| 判定项 | 结论 |
| --- | --- |
| 直接收益（诊断交付） | **小而真**：40 次注入 / 83 条诊断，gopls 上命中率 100%、热态 P50 165ms、闭环 2/3（2–3s）。规模受 opt-in 默认关闭与缺二进制限制 |
| 成本可控性 | 注入本身便宜（165–221ms）；主要成本是旧构建的 1s 预算等待与协议文案，新构建已削减（clean -73%、降级族提示 -97%） |
| 间接收益（观测闭环） | **确定且已回本**：8 轮优化全部由观测数据驱动，关键削减可由事件复算 |
| 当前净收益 | 在已启用且 gopls 可用的会话里为正；在 91% 的会话里 LSP 尚未参与，谈不上收益 |

### 6.2 建议（按性价比排序，阈值仍待积累）

1. **补装 pyright / typescript-language-server（或显式从池中移除）**：112 次请求、0 诊断、纯成本；
   新构建已能把原因细分为 `binary_missing` 并给出可行动提示。**已实施（O1，2026-10-01）**：
   池级预检跳过缺失成员，状态面保留 `unavailable + binary_missing` 原因，手动 `StartServer`/`Restart`
   可失效缓存重试。
2. **压低冷视图 no_fresh 的 1s 等待**：新构建 20/61 次请求仍打满预算（占其总等待 74%）。
   现有快速失败启发式（宽限已授予 + 连接从未发布 + 无快照）未覆盖「连接曾发布、但该路径冷」的场景；
   建议先积累 ≥1 周新构建样本，再决定是否放宽判据（避免把热连接的慢分析误判为冷）。
   （窗口外进展：提交 `a6a98435` 已把快速失败改为**路径级信号**，效果待新窗口复算。）
3. **评估默认开启策略**：当前只有 9% 会话 / 17% 编辑在收益面内。若考虑默认开启，需同时评估
   gopls 冷启动等待与内存压力（本窗口 16 次崩溃为整机内存耗尽的外部终止）。
4. **修复两处数据管道口径**——**已实施（O2/O3，2026-10-01）**：
   - `internal/lsp/eventbridge/observer.go` 请求投影已补 `appended_diag_bytes` / `appended_note_bytes` /
     `appended_empty_bytes`，`cold_fast_fail` / `attempted_members` 一并落盘；
   - `lsp_fallback_ratio` 已统一为 attempted 分母（Go 基线包 + Python 脚本 + 自验样例同步）。
5. **继续积累后再标定阈值**（ADR-0003 D4）：closure eligible 目前仅 3 条、新构建纯样本仅 61 请求；
   建议 closure eligible ≥20、新构建样本 ≥1 周后再固化告警阈值。

### 6.3 已实施（2026-10-01，方案见 `docs/plan/lsp-benefit-optimization-plan-20261001.md`）

| 项 | 内容 | 测试证据 |
| --- | --- | --- |
| O1 | 缺二进制池级预检：缺失成员不进入请求路径；状态面保留原因；手动重启失效缓存重试 | `TestRegistrySkipsUnavailableMembers` |
| O2 | 事件投影补 `appended_diag/note/empty_bytes` | `observer_join_test` |
| O3 | `lsp_fallback_ratio` 统一 attempted 分母（Go 基线包 + Python 脚本 + 自验样例） | `baseline_test` + `--selftest` |
| O4 | 请求事件新增 `cold_fast_fail` 归因 | `TestColdRetryAfterGraceTimeout` |
| O5 | 请求事件新增 `attempted_members`（多成员观测，仅 >1 落盘） | `observer_join_test` |
| O6 | 空结果块 compact（默认）+ 修正字面 `\n`；配置 `diagnostics.emptyStyle` | `TestRenderDiagnosticsEmptyStyle` |
| O7 | 分析侧消费新字段：新增 `lsp_cold_first_probe_ratio` 行、追加字节拆分与多成员明细；降级文案报告真实预算 | `TestColdProbeRowStates` / `TestColdProbeIgnoresUnclassifiedNoFresh` / `--selftest` |
| O8 | 删除/移走路径跳过内联诊断（不再把正常 delete 变成 `read file` 降级提示；目录同样跳过） | `TestAppendToResultSkipsDeletedPaths` |
| O9 | 请求事件新增 `total_diag_count`/`new_diag_count`（scope 过滤前全量与新增条数）→ 基线新增 `lsp_diag_new_ratio`；**A6（scope 默认值）从此有数据可判** | `TestDiagnoseCountsNewVsTotalDiagnostics` / eventbridge 载荷断言 / `--selftest` |
| O10 | 冷启动宽限按路径授予（真机发现：暖连接上新建文件 1.71s 发布 vs 1.0s 预算 → 白丢诊断） | `TestColdGraceCoversNewPathOnWarmClient` |

效果待新窗口复算（预期：pyright/ts 112 次请求 → 0；fallback(attempted) 0.7215 → ~0.671；
clean 空块 ~130B → ~60B；`cold_fast_fail` 可直接统计）。

### 6.4 复算工具就绪（O7，2026-10-01）

- Go 基线包（`internal/lsp/baseline`）与 Python 脚本已同构消费 O2/O4/O5 新字段：
  §4.3 新增 `lsp_cold_first_probe_ratio`（no_fresh 的首探针/重复探针拆分），
  追加字节行附"新构建拆分：诊断/提示/空块"，事实明细附多成员请求计数；
  旧构建缺 `cold_fast_fail` 字段的事件**不参与拆分**（"缺失 ≠ 首探针"）。
- 首轮复算（窗口至 2026-10-01T03:00Z，1089 请求）：`cold_fast_fail` 0 样本（运行中构建尚未包含
  O4）；`no_publish` 3 条均无该字段；多成员请求 0 次（120 个带 `tool_call_id` 的编辑调用全部单文件）。
- 已确认的新读数：**冷启动 P95 = 14363 ms（n=3，P50 11349ms）**——冷启动才是"会话首个编辑
  必然降级"的主因，路径级快速失败 + prewarm 的处置方向正确；首次 no_fresh 全预算等待的规模
  需待 ≥20 条带 `cold_fast_fail` 样本后再定（判据：`lsp_cold_first_probe_ratio`）。

---

## 7. 局限与数据缺口

1. **反事实不可测**：没有关闭 LSP 的对照组，「省了多少时间/多少返工」无法从本数据回答；
   closure 只是过程代理。
2. **小样本**：injected 40、closure eligible 3、新构建 61 请求；所有比例只作方向判读。
3. **窗口跨构建 + 实机测试流量**：旧构建事件缺 `reason_category`/指纹（365 条未分类降级），
   部分流量来自优化轮的真机验证脚本（被测场景本身），会污染比例。
4. **`appended_*_bytes` 拆分字段未落盘**——**已修复（O2，2026-10-01）**；本报告窗口内的字节构成
   仍是反推值（历史事件不回填），新窗口起可直接读数。
5. **`first_publish_ms` 窗口内 0 样本**：冷启动延迟仍 `n/a`（未采集，不是 0）。
   （窗口外进展：提交 `654822f6` 已让 `first_publish_ms` 在单会话状态面可见，事件落盘覆盖待复算。）
6. **fallback 两套分母**——**已统一（O3，2026-10-01）**为 attempted（0.7215）。
7. **诊断正文不落盘**：无法评估诊断质量（误报/漏报）、也无法判断模型是否采纳了诊断建议。

---

## 8. 复现与口径

### 8.1 数据与命令

```powershell
# 1) §4.3 基线（committed 工具，内置自验）
py -3 scripts/analyze-lsp-baseline.py --selftest
py -3 scripts/analyze-lsp-baseline.py --days 14 --out .tmp/lsp-benefit-baseline.md --json .tmp/lsp-benefit-baseline.json

# 2) 分段事实（本报告 §3–§5 的来源；一次性脚本，仓库 .tmp 已 gitignore）
py -3 .tmp/lsp_benefit_eval.py 14    # 产出 .tmp/lsp-benefit-facts.json / .md

# 3) 机制核验（代码侧，2026-10-01 本机执行：ok ... 7.730s）
cd backend
go test ./internal/lsp/ -run "TestHintOnceDedupesInlineAppend|TestEmptyPublishEarlyAcceptWithConfirmWindow|TestEmptyPublishConservativeWithoutServerOptIn|TestRestartWindowResetsBudget|TestColdRetryAfterGraceTimeout|TestColdGraceLetsLateFirstPublishLand|TestColdGraceGrantedOncePerPath|TestPrewarmOpensDocumentAfterColdStart|TestDocumentLRUEvictionSendsDidClose|TestIncrementalDidChangeSendsMinimalRange|TestDiagnosticsEventDedupedByFingerprint|TestClientStatusReportsFirstPublishMS|TestReasonCategory" -count=1
```

分段脚本的口径（如需重建，按此实现即可，全部只读）：

- **构建代际切分**：请求 payload 含 `path_fingerprint` / `diag_fingerprint` / `reason_category` /
  `tool_call_id` 任一 → new；否则 old（比按时间猜测更可靠，实机验证存在「同一会话中途换构建」）。
- **等待统计**：`duration_ms` 求和/分位；「打满预算」= `duration_ms >= 990`；
  分位口径 `ceil(p*n)` 1-based（与 Go `metrics.go` 一致）。
- **闭环**：同 `session_id` + 同 `path_fingerprint` 的请求按时间排序，对每个带 `diag_fingerprint` 的
  injected 请求，看下一条是否 `outcome=clean`；无 eligible 输出 n/a。
- **覆盖率分母**：只算「LSP 活跃会话」（窗口内出现过 `lsp.request.finished` 的会话）内的编辑类工具调用。
- **编辑计数**：`tool.completed` 带顶层 `timestamp`，按事件时间做 14 天过滤（快照 5708；数据源实时增长，
  复算时点会更高）；「LSP 活跃会话内 978」同理。分子（inline 请求）与分母在同一 14 天窗口内。
- **反模式纪律**：未采集项输出 n/a + 原因；不做阈值告警。

### 8.2 本次评估产出的冻结快照

| 文件 | 内容 |
| --- | --- |
| `.tmp/lsp-benefit-baseline.md` / `.json` | 888 请求基线报告（§4.3 七项指标 + 事实明细 + 按日；2026-10-01 起为八项，新增 `lsp_cold_first_probe_ratio`） |
| `.tmp/lsp-benefit-facts.md` / `.json` | 构建代际对比、结果分桶、闭环明细、注入明细、每会话 |

> 快照会随新事件增长而变化；报告正文中的数字均以上述窗口（截至 2026-10-01T01:53:39Z）为准。

---

## 附录 A：刷新版 §4.3 基线登记表（窗口同上）

| 指标 | 基线值 | 样本量 | 结论/阈值 |
| --- | --- | --- | --- |
| `lsp_edit_coverage_ratio` | 0.9070 | inline 887 / LSP 活跃会话内 edit 978（全部 edit 5708） | 待标定 |
| `lsp_diag_hit_ratio` | 1.0000 | hit 40 / injected 40 | 待标定 |
| `lsp_fallback_ratio` | 0.5923（requests 分母）/ 0.7215（attempted 分母） | degraded 526 / requests 888（no_server 159、clean 163） | 待标定；**分母待收敛** |
| `lsp_wait_latency_p95` | 1000 ms（P50 0 ms） | n=888 | 待标定；分桶见 §4.1 |
| `lsp_append_bytes_ratio` | 0.1934 | 追加 125,078B / 活跃会话回执 646,753B | 待标定；构成见 §4.2 |
| `lsp_closure_ratio` | 0.6667 | closed 2 / eligible 3 | 待标定（样本少） |
| `lsp_cold_first_publish_p95` | n/a（未采集） | n=0 | 待采集 |

与首次回填（窗口至 00:48Z：覆盖率 0.8807、fallback 0.5597、closure 1.0(2/2)）的差异主要来自新增的
134 次新构建请求与 1 条 open 闭环样本；两版口径相同，本表为最新。

## 附录 B：关键证据索引

| 主题 | 位置 |
| --- | --- |
| 内联诊断接线（mutated_paths → 追加） | `backend/internal/tools/manager.go:176-194` |
| 请求级读数与 fallback 分母（attempted） | `backend/internal/lsp/metrics.go:50-77/156-196` |
| 事件投影（请求载荷字段） | `backend/internal/lsp/eventbridge/observer.go:40-78` |
| observe 字段白名单（含未投影的拆分字段） | `backend/internal/runtimeobserve/projector.go:151-161` |
| 基线报告 fallback 分母（requests） | `backend/internal/lsp/baseline/baseline_report.go:35-37` |
| 默认关闭 / 配置默认值 | `backend/internal/lsp/spec.go:89-126/315-350` |
| 工具面（lsp_diagnostics / lsp_servers） | `backend/internal/tools/lsp_bridge.go:20-28` |
| 优化轮记录（8 轮 + 崩溃根因） | `docs/plan/lsp-observability-and-analysis-plan-20260929.md` §5.8 |
| 阈值纪律（不写死阈值） | `docs/knowledge_Layer/adr/0003-exploration-attribution-metrics.md` §4/§10 |
