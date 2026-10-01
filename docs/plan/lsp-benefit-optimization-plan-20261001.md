# LSP 收益优化与修复方案（2026-10-01）

> 定位：实施计划（plan）。依据：`docs/analysis/lsp-benefit-evaluation-20261001.md`（14 天窗口，
> 888 请求 / 12 个 LSP 活跃会话）与 `docs/plan/lsp-observability-and-analysis-plan-20260929.md` §5.8。
> 纪律：事件字段保持低敏（无路径/正文/自由文本）；口径单源；阈值不写死（ADR-0003 D4）；
> inline 追加保持 append-only 与不变式 I1/I2（docs/lsp 03）。
> 实施顺序：O3 → O2 → O4 → O5 → O6 → O1 → 文档回填（O1 改动面最大，放最后）。

---

## 1. 背景与依据（本轮要解决的问题）

| 问题（评估读数） | 目标 | 对应工作项 |
| --- | --- | --- |
| pyright / typescript 缺二进制贡献 112 次请求、0 条诊断（12.6% 请求量，纯降级噪声） | 池级预检跳过缺失成员；状态面保留可行动原因 | O1 |
| `appended_diag/note/empty_bytes` 在 metrics 有、事件投影缺失（0/888 样本），字节构成只能反推 | 事件载荷补齐三字段，观测链闭合 | O2 |
| `lsp_fallback_ratio` 两套分母：基线 requests（0.5923） vs 运行时 attempted（0.7215） | 统一为 attempted（plan §3.3/§5.8 定义） | O3 |
| 路径级冷快速失败（a6a98435）已落地，但事件无归因字段，效果无法复算 | 请求事件新增 `cold_fast_fail` | O4 |
| 多成员顺序预算可能叠加（Go+TS 混合工作区潜在 2×等待），且 `server` 只记首个成员 | 新增 `attempted_members` 观测（行为不变） | O5 |
| clean 空结果块 ~130B/次（窗口 163 次），其中 scope/servers 属性是固定冗余 | 新增 compact 模式（默认），可配置回 full | O6 |

---

## 2. 范围与非目标

### 2.1 本轮实施（O1–O6，全部为代码级、可单测、可回滚）

- **O1** 缺二进制池级预检：成员在预检失败时被路由跳过，状态面（`lsp_servers` / `/lsp status` / web）保留
  `unavailable + binary_missing` 原因；手动 `StartServer` / `Restart` 可强制重试（用户可能刚装好）。
- **O2** 事件投影补 `appended_diag_bytes` / `appended_note_bytes` / `appended_empty_bytes`。
- **O3** `lsp_fallback_ratio` 分母统一为 attempted（Go 基线包、Python 脚本、自验样例同步）。
- **O4** 请求事件新增 `cold_fast_fail`（低敏布尔）：命中路径级冷快速失败时为 true。
- **O5** 请求事件新增 `attempted_members`（仅 >1 时落盘）：多成员叠加的观测前提。
- **O6** 空结果块 compact：`<lsp_diagnostics file="…" count="0"/>`；配置 `diagnostics.emptyStyle: full|compact`（默认 compact）。

### 2.2 非目标（本轮不做，另行评估）

- **默认开启 LSP**（收益规模最大杠杆，但需资源护栏：本窗口 16 次 gopls 崩溃为整机内存耗尽的外部终止）。
- **多成员并行等待**：先由 O5 积累观测，再决定并行化或全局预算，避免破坏"慢 server 不饿死同批"的既有语义。
- **A/B 反事实协议**（同任务集 LSP on/off）与 **mutated_paths 覆盖审计**（覆盖率缺口 9%）。
- 任何阈值固化/告警（ADR-0003 D4）。

---

## 3. 工作项设计

### O1 缺二进制池级预检

**现状**：`Registry.ServersForPath` 对每个匹配成员调用 `ensureStarted`（`registry.go:130-143`），
缺二进制在启动时才失败 → 每个请求都产生 `degraded_binary_missing`（窗口 112 次、0 诊断）。

**设计**：

- `RegistryOptions` 新增 `LookPath func(string) (string, error)` 接缝；优先级：
  显式 `LookPath` > `Dial != nil`（测试/自定义传输一律视为可用，避免破坏现有 fake dial 测试）> `exec.LookPath`。
- `registryEntry` 新增一次性的可用性缓存（`sync.Once` + `available` + `availabilityErr`），
  错误文案与现有启动失败一致（`lsp: start <name>: executable "<cmd>" not found`），保证
  `ReasonCategory` 仍折叠为 `binary_missing`。
- `ServersForPath` 跳过不可用成员；`StartAll` 跳过；`Status()` 对未启动且不可用的成员返回
  `StateUnavailable + Reason`（修复现在会误显示 `starting` 的问题）。
- 手动 `StartServer` / `Restart` 先失效预检缓存再启动（显式重试语义，对应"用户刚装好二进制"）。

**改动点**：`internal/lsp/registry.go`、`internal/lsp/bridge.go`（`BridgeOptions.LookPath` 透传）。
**测试**：新增 `TestRegistrySkipsUnavailableMembers`（注入 LookPath 失败 → `ServersForPath` 为空、
Status 为 unavailable + binary_missing；注入成功 → 正常返回）；跑既有 lsp 全包回归。
**验收**：缺二进制成员不再产生请求事件；`lsp_servers` 仍能看到该成员与原因；Dial 注入的既有测试全绿。
**回滚**：移除预检分支即恢复原行为（单文件回滚）。

### O2 事件投影补字节拆分

**现状**：`bridge.observeRequest` 已计算三字段（`bridge.go:522`），`metrics.RequestRecord` 已带（`metrics.go:42-44`），
`runtimeobserve` 白名单已有（`projector.go:155-157`），但 `eventbridge/observer.go` 的请求载荷没有。

**设计**：`lsp.Event` 增加三个 int 字段；`observeRequest` 赋值；`eventbridge` 在值 >0 时写入载荷。
**测试**：扩展 `eventbridge/observer_join_test.go` 断言三字段透传。
**验收**：新事件可直接复算"诊断正文 vs 协议噪声"；旧事件保持 n/a（不渲染为 0）。

### O3 fallback 分母统一为 attempted

**现状**：`baseline_report.go:35-37` 与 `scripts/analyze-lsp-baseline.py`（`build_rows`）用
`degraded / requests`；`metrics.go:172` 用 `degraded / (requests - no_server)`。plan §3.3/§5.8 定义的是 attempted。

**设计**：两端统一 `degraded / attempted`，样本文案改为 `degraded X / attempted Y（no_server Z、clean W）`；
Python `--selftest` 增加 fallback 断言（fixture：degraded 1 / attempted 3）。
**验收**：`--selftest` 通过；重跑基线后 `lsp_fallback_ratio` 显示 0.7215（旧口径 0.5923 仅作历史注记）。
**回滚**：单点公式回滚。

### O4 `cold_fast_fail` 归因

**设计**：`Outcome.ColdFastFail`（内部字段，不进入模型文本）；`Diagnose` 在命中
`client.ColdWait(path)` 走 reduced budget 时置 true；`RequestRecord` / `Event` / eventbridge 载荷新增
`cold_fast_fail`（仅 true 时落盘）；`runtimeobserve` 白名单同步。
**测试**：扩展现有冷路径测试（`TestColdRetryAfterGraceTimeout`）断言第二次请求 `ColdFastFail=true`。
**验收**：新窗口可复算"路径级快速失败"的次数与节省等待。

### O5 `attempted_members` 观测

**设计**：`RequestRecord` / `Event` 新增 `AttemptedMembers int`（= `len(outcome.Servers)`）；
eventbridge 仅 >1 时落盘（单成员请求载荷保持现状）；白名单同步。
**测试**：metrics/observer 用例覆盖多成员。
**验收**：多成员叠加是否发生、发生频率可直接查询；行为零变化。

### O6 空结果块 compact

**设计**：`DiagnosticsConfig.EmptyStyle`（`emptyStyle: full|compact`，空值/非法值 → compact）；
`RenderOptions.EmptyCompact bool`（零值 = full，保证直接调用者行为不变）；
`format.go` 空结果分支按 compact 输出 `<lsp_diagnostics file="…" count="0"/>`；`bridge` 从配置传入。
**测试**：`format_test` 增加 compact/full 两例；断言 compact 不含 `scope=`/`servers=`，full 保持原格式。
**验收**：新窗口 clean 平均追加字节从 ~130B 降到 ~60B；模型仍能区分"已检查无问题"。
**回滚**：配置回 `full` 即可，无需改码。

---

## 4. 验收与验证计划

1. 定向测试：`go test ./internal/lsp/... ./internal/lsp/eventbridge/... ./internal/lsp/baseline/... -count=1`；
   涉及透传时补 `./cmd/aicli/commands/...`（bootstrap 相关）。
2. 全包回归：`go test ./...`（backend）。
3. Python：`py -3 scripts/analyze-lsp-baseline.py --selftest`；重跑 14 天基线写入 `.tmp/lsp-benefit-baseline.md`。
4. 新窗口复算（预期）：
   - O1：pyright/ts 请求 112 → 0（转 no_server，attempted 分母同步缩小）；fallback(attempted) 0.7215 → ~0.671；
   - O4：`cold_fast_fail` 计数 ≈ 路径级冷请求数（修复前 11/20 的 no_fresh 场景）；
   - O6：clean 平均追加 ~130B → ~60B；
   - O2/O5：字段存在性 100%（新构建样本）。
5. 文档回填：本方案 §4 结果、评估报告 §4.3/§6.2/§7、umbrella plan §4.3（fallback 行）、
   docs/lsp 配置文档（`emptyStyle` 与预检语义）。
6. 独立核验：改动完成后由只读子代理对照本方案审计 diff（文件/行/测试覆盖）。

---

## 5. 风险与回滚

| 风险 | 缓解 | 回滚 |
| --- | --- | --- |
| 预检误伤（PATH 临时缺失导致成员被跳过） | 手动 `StartServer`/`Restart` 失效缓存重试；状态面显示原因 | 移除预检分支 |
| compact 默认值改变模型可见文本 | 可配置 `emptyStyle: full`；仅空结果分支变化，诊断块不动 | 配置回 full |
| 新增事件字段影响旧消费者 | 全部为可选字段（omitempty/仅 >0 落盘）；projector 白名单先行 | 移除字段 |
| 预检与 Dial 接缝冲突 | 优先级规则：显式 LookPath > Dial!=nil 视为可用 > exec.LookPath；既有 fake dial 测试不受影响 | 单测覆盖 |
| 分母统一改变历史对比 | 报告保留旧口径注记（0.5923 为 requests 口径历史值） | 公式回滚 |

## 6. 完成定义（DoD）

- O1–O6 全部落地且有对应单测；`go test ./...` 与 `--selftest` 全绿；
- 事件载荷可复算 O2/O4/O5 字段；基线 `lsp_fallback_ratio` 为 attempted 口径；
- 评估报告与 umbrella plan §4.3 完成回填；本方案标注每项"已实施/证据"。

---

## 7. 实施结果（2026-10-01）

**状态：O1–O6 全部落地。**

| 项 | 状态 | 改动点 | 测试证据 |
| --- | --- | --- | --- |
| O1 | ✅ | `registry.go`（预检缓存/路由跳过/状态/手动重试）、`bridge.go`（LookPath 透传） | `TestRegistrySkipsUnavailableMembers`；lsp 全包回归 |
| O2 | ✅ | `observer.go`、`metrics.go`、`bridge.go`、`eventbridge/observer.go`、`projector.go` | `observer_join_test`（三字段透传、0 值不落盘） |
| O3 | ✅ | `baseline_report.go`、`scripts/analyze-lsp-baseline.py`（Go/Python 自验同步） | `baseline_test`（0.3333=1/3）、`--selftest`（fallback 1/3） |
| O4 | ✅ | 冷分支置位 → `RequestRecord`/`Event` → eventbridge → 白名单 | `TestColdRetryAfterGraceTimeout`（恰好 1 次 `cold_fast_fail`） |
| O5 | ✅ | `AttemptedMembers = len(outcome.Servers)`，仅 >1 落盘 | `observer_join_test` |
| O6 | ✅ | `spec.go`（`emptyStyle` 默认 compact）、`format.go`、`bridge.go`；顺带把空块的字面 `\n` 修为真实换行 | `TestRenderDiagnosticsEmptyStyle`（compact/full 两态） |
| O7 | ✅ | 分析侧消费新字段：Go 基线包 + Python 脚本同构新增 `lsp_cold_first_probe_ratio` 行、追加字节拆分、多成员明细；降级文案改报**实际预算**（原恒报配置 `wait_ms`） | `TestColdProbeRowStates` / `TestColdProbeIgnoresUnclassifiedNoFresh` / `--selftest` / `TestColdRetryAfterGraceTimeout` |
| O8 | ✅ | 删除/移走的路径（apply_patch delete/move）不再进入内联诊断：文件已不存在时静默跳过，不再产生 `read file` 降级提示与无谓请求；目录同样跳过 | `TestAppendToResultSkipsDeletedPaths` |
| O9 | ✅ | 请求事件新增 `total_diag_count`/`new_diag_count`（scope 过滤前全量与其中新增条数，仅全量>0 时落盘）：解锁 A6（scope 默认值）决策；基线新增 `lsp_diag_new_ratio` 行与明细 | `TestDiagnoseCountsNewVsTotalDiagnostics` / eventbridge 载荷断言 / baseline fixture / `--selftest` |

**验证记录**

- `go test ./internal/lsp/... ./internal/runtimeobserve/... -count=1` → 全绿（lsp 24.2s）。
- `go test -p 2 ./internal/tools/... ./internal/runtimeserver/... ./internal/webui/... -count=1` → exit 0。
- `py -3 scripts/analyze-lsp-baseline.py --selftest` → OK（4 请求 / fallback 1/3 / closure 1.0）。
- 刷新基线（窗口至 2026-10-01T02:36Z，1049 请求）：`lsp_fallback_ratio = 0.7301`
  （degraded 595 / attempted 815），**attempted 口径端到端生效**。
- 全量 `go test ./...` 受整机内存耗尽（`VirtualAlloc ... errno=1455`，与窗口内 16 次 gopls 崩溃同源）
  影响，多个包 `[build failed]`；另 `cmd/aicli/commands` 的编排/UI 测试在并发负载下 flaky
  （隔离运行通过，与 LSP 无关）。本次改动的包与关联包均已定向验证通过。
- 只读子代理 diff 审计：见 §7.1（审计结论回填）。

### 7.1 独立审计结论

**结论：O1–O6 全部按方案落地，全链路接线完整；四条不变式（低敏字段、append-only、配置可回退、
测试真实覆盖）全部满足；未发现实现缺陷。**（只读子代理，审计范围：工作区 diff 对照本方案。）

| 审计项 | 结论 | 证据（节选） |
| --- | --- | --- |
| O1 预检分层与透传 | ✅ | `registry.go:67-94/162-170/199-206/236/262-266/308/362-369`、`bridge.go:83-84/111` |
| O2 三字段全链路 | ✅ | `observer.go:56-58`、`bridge.go:549-551`、`eventbridge/observer.go:73-82`、`projector.go:155-157` |
| O3 双端口径一致 | ✅ | `baseline_report.go:36-42/91`、`analyze-lsp-baseline.py:408-414/456`、两侧断言 |
| O4/O5 归因字段 | ✅ | `bridge.go:326-328/553-554`、`eventbridge:83-88`、`optimization_test.go:637-647` |
| O6 compact 与换行修正 | ✅ | `spec.go:84-87/138-149`、`format.go:17-20/96-107`、`bridge.go:385`；无其他调用方依赖字面 `\n` |
| 不变式 | ✅ | 新增字段全为 int/bool（无路径/正文）；非空块未动；`emptyStyle` 可配置回退 `full` |

审计发现的唯一实质缺口与处理：

1. **O1 的「`Dial != nil` 跳过预检」缺直断言** → 已补用例：`TestRegistrySkipsUnavailableMembers`
   新增 dial 注入 + LookPath 失败仍须正常路由（回归保护）。
2. samples 文案在 attempted=0 时会打印 `degraded 0 / attempted 0` → 已改为
   `no attempted requests（no_server N）`（Go/Python 同步）。
3. 低风险记录（不处理）：`Normalize()` 未显式规范化 `EmptyStyle`（`EmptyStyleValue()` 已兜底）；
   预检缓存仅会话内有效、PATH 变化靠手动重启重查（方案 §5 已声明为设计取舍）。

### 7.2 预期效果（待新窗口复算）

| 指标 | 窗口基线 | 预期 | 复算方式 |
| --- | --- | --- | --- |
| pyright/ts 请求 | 112 次 / 0 诊断 | → 0（转 no_server） | `lsp.request.finished` 按 server 过滤 |
| `lsp_fallback_ratio` | 0.7215（attempted） | → ~0.671（剔除 112 次不可尝试请求） | 基线脚本（已统一口径） |
| clean 平均追加字节 | ~130B | → ~60B | `appended_empty_bytes` 直接读数（O2 后） |
| `cold_fast_fail` | 无字段 | 可直接统计路径级快速失败次数 | 新事件字段 |
| `attempted_members` | 无字段 | 可确认多成员工作区是否出现叠加等待 | 新事件字段（仅 >1 落盘） |
| `lsp_cold_first_probe_ratio` | n/a（0 分类样本） | ≥20 样本后判读：首探针占比高 → 下一轮引入 `cold_probe_ms`（保守默认）；占比低 → 维持现状 | 基线报告新行（Go/Python 同构，旧事件缺字段不计入） |
| `lsp_diag_new_ratio` | n/a（0 诊断样本） | ≥20 诊断样本后判读：新增占比低（如 <50%）→ 评估把 `diagnostics.scope` 默认切到 `changed`；占比高 → 维持 `all`（A6） | 基线报告新行（Go/Python 同构，仅全量>0 的事件携带） |
