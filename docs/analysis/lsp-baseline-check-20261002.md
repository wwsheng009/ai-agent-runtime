# LSP 基线检查与判读（2026-10-02）

> 口径锚点：`docs/plan/lsp-observability-and-analysis-plan-20260929.md` §4.3（基线登记表）；
> 数据源：`scripts/analyze-lsp-baseline.py` 全窗口复算（`~/.aicli/chat-logs`，2299 个
> runtime-events.jsonl / 734706 行 / 0 损坏）；
> 判读准则：§4.1 优化决策矩阵、§7.2 预期效果（A6 / cold probe 判据）、ADR-0003 D4
> （阈值只在基线产出后固化，本轮结论为**建议阈值**，待确认后固化）。

## 0. 结论一览

九项指标：**4 达标 / 2 判据落地 / 3 未达标或关注**。

| 指标 | 基线值 | 判读 | 建议阈值 |
| --- | --- | --- | --- |
| `lsp_edit_coverage_ratio` | 0.9728 | 达标 | ≥0.95 |
| `lsp_diag_hit_ratio` | 1.0000 | 达标（构造性上限） | =1.0 |
| `lsp_diag_new_ratio` | 0.7798 | A6 判据不触发 → 维持 `scope=all` | ≥50% 维持 all |
| `lsp_fallback_ratio` | 0.6050 | 未达标（较上轮改善 0.127） | <0.5（attempted） |
| `lsp_wait_latency_p95` | 1097 ms | 全窗口达标；按日恶化需关注 | ≤1500 ms；P50=0 |
| `lsp_append_bytes_ratio` | 0.1765 | 达标 | ≤0.20 |
| `lsp_closure_ratio` | 0.5244 | 达标贴线（样本 ×20 后回归真实水平） | ≥0.5 |
| `lsp_cold_first_publish_p95` | 14363 ms | 未达标，当前最大单点延迟 | 候选 ≤10 s |
| `lsp_cold_first_probe_ratio` | 0.9608 | **触发既定判据** → 引入 `cold_probe_ms` | ≥50% 引入（既定） |

## 1. 复算核验

- **提交读数**：2115 请求 / 37 会话 / 119 injected / 925 degraded / P95 1097 ms。
- **复算**（2026-10-02 全窗口）：2119 请求 / 38 会话——与提交读数仅差窗口端点
  （提交运行之后新增 4 请求 / 1 会话）；**9 项指标值全部吻合**
  （coverage 0.9729 vs 0.9728、append 0.1763 vs 0.1765 为端点四舍五入末位差）。
- **内部自洽**：119 injected + 485 clean + 925 degraded = 1529 attempted；
  + 586 no_server = 2115 总请求 ✓；attempted 口径（O3）端到端生效。
- **仪器健康**：`--selftest` 通过；Go/Python 基线包 fixture 互锁（既有单测覆盖）。

## 2. 与上轮（2026-10-01T02:54Z，n=1078）对比

| 指标 | 上轮 | 本轮 | 变化 |
| --- | --- | --- | --- |
| `lsp_edit_coverage_ratio` | 0.9284 | 0.9728 | ↑ +0.044 |
| `lsp_diag_hit_ratio` | 1.0000 | 1.0000 | 持平 |
| `lsp_diag_new_ratio` | n/a | 0.7798 | 新采集（O9） |
| `lsp_fallback_ratio` | 0.7321 | 0.6050 | ↓ -0.127（改善） |
| `lsp_wait_latency_p95` | 1000 ms | 1097 ms | ↑ +97 ms（按日恶化见 §3.5） |
| `lsp_append_bytes_ratio` | 0.1828 | 0.1765 | ↓ -0.006 |
| `lsp_closure_ratio` | 0.7500（n=4） | 0.5244（n=82） | 样本 ×20，回落至上轮小样本乐观偏差之下 |
| `lsp_cold_first_publish_p95` | 14363 ms（n=3） | 14363 ms（n=25） | 样本 ×8，P95 持平（同一重尾） |
| `lsp_cold_first_probe_ratio` | n/a | 0.9608 | 新采集（O4/O7/O11） |

## 3. 逐项判读

### 3.1 `lsp_edit_coverage_ratio` = 0.9728 — 达标

- 读数：inline 2114 / LSP 活跃会话内 edit 2173（全部 edit 6903）。
- 判读：97.3% 的 LSP 活跃会话内编辑产生了内联请求，**覆盖充分**。残余 59 次（2.7%）
  为预期缺口：O8 删除/移走路径跳过、无匹配 server 的文件、非文件类编辑。
- 注意：覆盖率指标健康，但**绝对覆盖面仅 31%**（2173/6903 编辑在 LSP 活跃会话内）——
  69% 的编辑发生在未启用/未触达 LSP 的会话，"默认关闭"是最大收益杠杆（见 §4.6）。
- 建议阈值：≥0.95。当前达标。

### 3.2 `lsp_diag_hit_ratio` = 1.0000 — 达标（构造性上限）

- 读数：hit 119 / injected 119。
- 判读：`injected` 的定义即"有诊断注入"，1.0 是构造性必然值；该指标的异常方向
  只有"低"（注入了空诊断块，应查 server 根目录 / `workspaceFolders` 能力协商 /
  sync kind）。真实有效性信号是 **injected/attempted = 119/1529 = 7.8%**——
  尝试请求中仅 7.8% 产出诊断（clean 31.7% + 降级 60.5%）。
- 建议阈值：=1.0；任何 <1.0 即触发 §4.1 的 server 根/能力协商排查。当前达标。

### 3.3 `lsp_diag_new_ratio` = 0.7798 — A6 判据不触发，维持 `scope=all`

- 读数：new 131 / all 168（全量为 scope 过滤前条数；n=168 ≥20 满足判据样本门槛）。
- 判据（§7.2）：≥20 诊断样本后，新增占比低（如 <50%）→ 评估切 `diagnostics.scope=changed`；
  占比高 → 维持 `all`。
- 判读：新增占比 **78% ≥ 50%** → **不触发**。`scope=all` 每次都在产出新信号，
  切 `changed` 将丢掉 22% 既有问题（168 条中 37 条）的可见性，收益不大。
- 建议阈值：新增占比 ≥50% 维持 all；<50% 评估 changed。当前维持 all。

### 3.4 `lsp_fallback_ratio` = 0.6050 — 未达标，主因可行动

- 读数：degraded 925 / attempted 1529（no_server 586、clean 485）；较上轮 0.7321
  改善 0.127。
- 降级构成（复算，925 = 以下之和）：
  - `degraded_no_fresh` 265（28.6%）——冷启动/晚发布现象，是冷宽限与 `cold_probe_ms`
    的作用对象；其中带 `reason_category` 的 113 条：wait_timeout 63 / no_publish 50；
  - `degraded_crashed` 61（6.6%，`restart_budget_exhausted`）——**gopls 反复崩溃
    重启耗尽预算**，与整机内存耗尽（`VirtualAlloc ... errno=1455`）同源，是最大
    **可行动可靠性**归因（见 §4.2）；
  - `degraded_binary_missing` 19、`degraded_starting` 12、`degraded_read_error` 2、
    `degraded_transport_closed` 1；
  - 未分类 `degraded` 565（61%）——旧构建无 `reason` 字段，随 14 天窗口滚动自然出清，
    不是污染（§4.3 口径限制 1 已声明）。
- 现实下限估算：即使修复崩溃（61）与 binary_missing（19）、starting（12），
  fallback 仍 ≈ (925-92)/1529 = 54.5% —— 达标 <0.5 还需 no_fresh 下降
  （冷启动 P95 修复，见 §4.4/§4.5）。
- 建议阈值：<0.5（attempted）。当前未达标。

### 3.5 `lsp_wait_latency_p95` = 1097 ms — 全窗口达标，但按日恶化

- 读数：n=2115，P50 = 0 ms（半数以上请求零等待：空发布早接受 / 快速失败 /
  即时 clean）。
- 判读：全窗口 P95 1097 ms 略超 1 s 基础预算（`DefaultWaitMS`），尾部由冷宽限路径
  （首探针 2.5 s 预算）与崩溃重启贡献。**按日明细显示恶化趋势**：

  | 日期 | 请求 | 注入 | 降级 | P95(ms) |
  | --- | --- | --- | --- | --- |
  | 2026-09-29 | 30 | 0 | 21 | 1000 |
  | 2026-09-30 | 616 | 38 | 318 | 1000 |
  | 2026-10-01 | 1366 | 70 | 572 | 1440 |
  | 2026-10-02 | 107 | 11 | 14 | 1681 |

  10-02 仅 107 请求（小样本，P95 ≈ 第 6 大值），但 10-01 的 1440 ms（n=1366）
  已确认尾部抬升——与 gopls 崩溃（61 次）和冷启动重尾同源。
- 建议阈值：P95 ≤1500 ms 且 P50=0；单日 P95 >1500 ms 纳入人工复查清单
  （不自动告警，ADR-0003 D4）。当前全窗口达标、趋势关注。

### 3.6 `lsp_append_bytes_ratio` = 0.1765 — 达标

- 读数：追加 249368 B / LSP 活跃会话内回执可见 1412773 B（全部 4313810 B）；
  新构建拆分：诊断 27268 / 提示 22310 / 空块 31169。
- 判读：较上轮 0.1828 微降，**稳定可控**。新构建追加中空块 31 KB 为最大构成
  （clean 确认，compact 后 ~111 B/次 ≈ 280 次 clean）；提示 22 KB 主要来自降级
  提示（`HintOnce` 已按 (server, reason) 去重）；诊断 27 KB 为有效载荷。
  **截断仅 1 次（省略 5 字符）**——`max_items`/`max_chars` 预算充足。
- 建议阈值：≤0.20 且截断请求占比 <1%。当前达标。

### 3.7 `lsp_closure_ratio` = 0.5244 — 达标贴线

- 读数：closed 43 / eligible 82（下一次同文件编辑为 clean）。
- 判读：样本从 4 增长到 82（×20）后，比率从上轮 0.7500 回落——上轮是小样本
  乐观偏差。0.52 属中等水平：约半数注入诊断在下一轮同文件编辑后消失
  （"诊断引导修复"闭环生效）；其余 48% 为持续诊断（部分修复 / 未触达诊断行 /
  新诊断）。**暂不触发** §4.1 的工具面改造（判据为"低"，建议 <0.3）。
- 注意：eligible 82 < injected 119——37 次 injected 为旧构建无 `diag_fingerprint`，
  分母被低估；随新构建普及 fingerprint 覆盖率（当前 69%）趋近全量后再判读。
- 建议阈值：≥0.5。当前达标（贴线）。

### 3.8 `lsp_cold_first_publish_p95` = 14363 ms — 未达标，最大单点延迟

- 读数：n=25（按 (session, server) 取首个发布），P50 7305 ms。
- 判读：P50 较上轮 11349 ms 改善；P95 与上轮持平（14363 ms，同一重尾——
  gopls 大型模块视图 ~85 s 极端案例已记录于 §5.8）。冷宽限（2.5 s）+ 预热
  （启动即 didOpen+didSave）已缓解**编辑路径体感**，但首个发布本身仍慢。
- 建议：模块视图懒加载/按需加载；评估 gopls `-remote=auto` 或视图分片。
- 候选阈值：≤10 s。当前未达标。

### 3.9 `lsp_cold_first_probe_ratio` = 0.9608 — 触发既定判据

- 读数：first 98 / repeat 4（n=102 ≥ 20 满足判据门槛）。
- 判据（§7.2）：首探针占比高 → 下一轮引入 `cold_probe_ms`（保守默认）；
  占比低 → 维持现状。
- 判读：首探针占比 **96%** → **触发**。路径级快速失败（`cold_retry_ms` 250 ms）
  仅覆盖 4% 的 no_fresh；96% 首探针各按完整宽限预算（WaitMS 1000 + Grace 1500 =
  2500 ms）等待，窗口累计成本最高 ~245 s。详细设计见 §4.1。
- 限定：`cold_probe_ms` 必须保守（建议 1500 ms = 现宽限值），低于此值需 A/B 验证
  injected 不回落（O10 真机案例首探针 1333 ms 发布）。

## 4. 优化建议（按优先级）

### 4.1 【P0，判据触发】O12：引入 `diagnostics.coldProbeMs`（首探针预算，保守默认 1500 ms）

**依据**：`lsp_cold_first_probe_ratio` 0.9608 触发 §7.2 既定判据（n=102 ≥20，
首探针 96% ≥50%）。

**现状机制**（`internal/lsp/bridge.go:394-417`）：

- 首探针（路径无快照 + 本连接未对该路径用过宽限）：
  `remaining = WaitMS(1000) + ColdStartGraceMS(1500) = 2500 ms`；
  超时且无快照 → `MarkColdWait(path)`（第九轮修正条件：路径无快照 + 本次等待超过
  快速失败预算）。
- 重复探针（路径已标记已知冷）：`remaining = min(ColdRetryMS(250), remaining)`，
  `ColdFastFail=true`。
- 基线读数：首探针 98 / 重复 4 → 96% 的 no_fresh 成本集中在首探针。

**设计**（2026-10-02 已实施，含一项实施期修正）：

- `DiagnosticsConfig.ColdProbeMS`（`coldProbeMs`；0/unset = 默认 1500，
  负值 = 关闭并恢复完整 2500 ms 预算）——与 `ColdStartGraceMS` 的
  0/unset/负值 三态模式一致。
- 宽限分支（`bridge.go`）在 `remaining += grace` 后追加封顶：
  `probe = max(cold_probe_ms, wait_ms)`；`if probe > 0 && remaining > probe
  { remaining = probe }`。**封顶不低于 `wait_ms`**——实施期由
  `TestRealRustAnalyzerRoundTrip`（真实 rust-analyzer 往返，配置
  `wait_ms=60s` 容忍慢速首分析）回归暴露：简单封顶会把显式调大
  `wait_ms` 的配置静默截断到 1.5s，故加 `wait_ms` 下限
  （`TestColdProbeFloorRespectsWaitMS` 钉住）。
- 标记语义不变：首探针超时且无快照仍 `MarkColdWait`（`remaining(1500) >
  coldBudget(250)` 条件不变），后续编辑仍走 250 ms 快失败；路径首个发布清除标记。
- 事件/观测：`cold_fast_fail` 语义不变（仅重复探针为 true）；首探针预算变化可由
  `duration_ms` 分布复算，无需新字段。

**保守性论证**：

- 默认 1500 ms = 现有 `ColdStartGraceMS` 值：宽限本为"中等冷启动"（gopls 模块
  视图数秒内发布）设计；完整 2500 ms 覆盖的极端尾部（~85 s 案例）本来就不会在
  预算内落地——那正是路径被标记已知冷的原因。
- O10 真机案例：暖连接新文件首探针 1333 ms 发布 → 1500 ms 仍覆盖（余量 167 ms）。
- 低于 1500 ms 需 A/B 验证 injected 不回落（(1500, 2500] ms 区间发布的首探针
  会转为 no_fresh）。

**预期收益**：98 次首探针中超时者每次节省 ~1000 ms（2500→1500），窗口累计最高
~98 s 等待；P95 尾部（冷宽限路径）相应收窄。

**验收**（已通过）：

- `TestColdProbeCapsFirstProbeBudget`（首探针等待 ≤ ColdProbeMS + ε）；
- `TestColdProbeFloorRespectsWaitMS`（封顶不低于 `wait_ms`）；
- `TestColdProbeNegativeRestoresFullBudget`（负值恢复 2500 ms）；
- `TestColdProbeConfigNormalization`（三态默认）；
- `TestColdGrace*` / `TestColdRetryAfterGraceTimeout` /
  `TestColdRetryAppliesPerPathOnWarmConnection` 不回归；
- `TestRealRustAnalyzerRoundTrip`（真实 rust-analyzer 往返，`wait_ms=60s`
  长预算不被截断）——该用例在实施期回归暴露了下限缺失，已修复；
- 真机：O10 场景（暖连接新文件 1333 ms 发布）仍 `injected`；
- 基线：首探针占比不变、首探针 P95 等待下降。

**风险与回滚**：cut 太低丢晚发布诊断（回退 O10 收益）→ 默认 1500 ms + 配置可
回退（负值）；A/B 后再考虑下调。

### 4.2 【P1】gopls 崩溃治理（`restart_budget_exhausted` 61 次）

- 证据：61 次 = 分类降级中第二大桶（no_fresh 265 之后）、最大可行动可靠性归因；
  与评估报告"16 次 gopls 崩溃为整机内存耗尽外部终止"及本次构建日志
  `VirtualAlloc ... errno=1455` 同源。
- 建议：① ADR-0005 Job Object 内存/生命周期护栏落地（限制单 server 峰值，崩溃即
  回收）；② gopls 侧内存配置评估；③ 重启预算耗尽后的降级提示改为可行动文案
  （"gopls 反复崩溃：建议重启会话或释放内存"）。
- 验收：`restart_budget_exhausted` 计数下降；fallback 向 <0.5 收敛。

### 4.3 【P1】binary_missing 残余 19 次

- O1 预检后缺二进制应转 no_server（不产生请求事件）；残余 19 次可能来自预检缓存
  会话内失效或 PATH 变化（方案 §5 已声明为设计取舍）。
- 建议：`/lsp status` 对 binary_missing 给安装指引；评估预检缓存 TTL / 按需重查
  （手动 `Restart` 已可强制重试）。

### 4.4 【P2】冷启动 P95 14.4 s

- n=25，P50 7305 ms（较上轮改善），P95 持平（gopls 模块视图 ~85 s 重尾）。
- 建议：模块视图懒加载/按需加载；评估 gopls `-remote=auto` 或视图分片；
  预热已落地（启动即 didOpen+didSave），但首个发布本身仍慢。
- 候选阈值：≤10 s。

### 4.5 【P2】P95 按日恶化监控

- by_day P95：1000 → 1000 → 1440 → 1681 ms（10-02 仅 107 请求，小样本）。
- 建议：把"单日 P95 > 1500 ms"纳入人工复查清单（不自动告警，ADR-0003 D4）；
  随崩溃治理（§4.2）与 `cold_probe_ms`（§4.1）回落。

### 4.6 【P3】绝对覆盖面 31%

- 6903 次编辑中仅 2173 次在 LSP 活跃会话内（31%）；覆盖率指标本身健康（0.9728），
  但绝对覆盖面是最大收益杠杆。
- 建议：默认开启评估（评估报告已列为非目标，需资源护栏——与 §4.2 联动：
  先落地内存护栏再评估默认开启）。

### 4.7 【P3】closure 0.5244 贴线

- 暂不触发工具面改造；新构建普及后 fingerprint 覆盖趋近 injected 全量
  （当前 82/119 = 69%），再判读。

### 4.8 数据口径（无需行动）

- 未分类降级 565（旧构建无 `reason`）随 14 天窗口滚动自然出清；
- 多成员请求 17 次（最多尝试 2 个成员）——叠加等待存在但少，暂不需并行化
  （方案 §2.2 一致）；
- 服务分布：gopls 1334（63%）为主力，pyright 146 / typescript 30 / gopls-a 17。

## 5. 口径与限制

- 覆盖率分母是「编辑类工具调用次数」，一次调用可能覆盖多文件（多行 request 事件），
  该比值是近似口径，需与 `mutated_paths` 覆盖缺口一起判读。
- `lsp_closure_ratio` 基于 path/diag fingerprint；旧构建事件无 fingerprint，
  分母被低估（见 §3.7）。
- `lsp_append_bytes_ratio` 依赖编辑回执的 `output_model_visible_bytes`；回执未携带时
  标记 n/a（不猜分母）。
- `lsp_cold_first_publish_p95` 依赖首个发布时补发的 `first_publish_ms`
  （新构建落盘后开始采集；旧事件标记 n/a，不把未采集渲染成 0）。
- 本报告只汇总 aicli/runtime-server 会话事件；runtime-server 的事件目录可用
  `--root` 指向其 chat-logs/事件根。
- 复算快照（2026-10-02 全窗口）与提交读数（2115/37）的差异为窗口端点漂移
  （+4 请求/+1 会话），不影响任何结论；登记表记录提交读数，本报告记录复算快照。
