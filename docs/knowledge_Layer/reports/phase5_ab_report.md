# Phase 5 on-mode A/B 报告（M5 早期读数：knowledge off vs on）

- **日期**：2026-10-01
- **工作区**：`%TEMP%\p6_ab\ws`（仓库副本，内含 `.aicli\runtime.yaml` + `.aicli\knowledge\knowledge.db`，最终 279 MB / 776 exploration_nodes）
- **模型**：`opencode.ai / space-bunny-free`（两臂同 provider / 同 model / 同任务集 / 同 prompt 后缀）
- **驱动**：`POST /web/api/invoke`（同步远程调用）+ `GET /web/api/turn`（turn 台账 + 空闲门）+ `POST /web/api/input {"type":"interrupt"}`（卡死回收）+ 直读 `knowledge.db`（只读 SQLite）
- **任务集**：`.tmp\ab_set.json`（20 个真实会话任务，逐任务同序）
- **分析器**：`.tmp\ab_analyze.py` → `.tmp\ab_report.json`（per_task 明细 + 同任务配对比较 + integrity 闸门）

## 1. 结论摘要

| # | 断言 | 结果 |
|---|---|---|
| A1 | **机制闸门：on 档真的注入、off 档真的不注入** | ✅ 成立。`context_snapshots` 在 off 臂运行窗口（18:37–18:47）**0 条**；on 臂（18:55 起）每个任务都落快照，`budget_json.mode=signals`、`token_budget=800`、`injected` 1–44 条/任务、`budget_filtered=0`。`context_items` 共 683 行 |
| A2 | **M5 门槛：prompt token ↓≥25% 且成功率不降**（`06_implementation_index_and_guidance.md` §7.2） | ❌ **未达标，且当前样本不足以判定**。配对 n=8：prompt token 平均 **-3.03%**，中位 -3.22%，95%CI **[-15.57%, +21.62%]**（3 降 / 5 升），与 0 不可区分，距 25% 门槛差两个数量级 |
| A3 | 成功率不降 | ✅ 未观察到下降：配对 8 个任务两臂 `turn_success` 均 8/8；on 臂 14 个任务里 9 个完成（5 个因 turn 卡死被预算回收，非知识层故障） |
| A4 | 附带信号（同一批配对，仅作方向性参考，不作验收） | completion token **-35.8%**（8/8 全部下降，CI [11.25%, 60.35%]）；tool_calls **-14.6%**（3 降/2 升/3 平，CI [-12.3%, 41.5%]）；墙钟 **+76%**（on 更慢，CI 明显 >0） |
| A5 | **测量链路本身可用性**（本轮最大产出） | ✅ 修掉 3 个会让整臂报废的驱动缺陷：invoke 返回 ≠ turn 结束（空闲门）、交互工具永久挂死（禁令 + 单任务预算）、假成功（正文非空判据） |

> **口径声明**：本轮是 **n=8 配对的早期读数，不构成 M5 验收**。M5 要求 ≥20 个配对任务；本轮 20 任务中 off 臂只完成 9 个、on 臂只完成 8 个（重叠 8 个），样本损失来自**任务集里 3 个会触发交互/构建的重任务**（t09/t10/t12 两臂都卡死）与 on 臂额外损失 t11。下一轮应把任务集收敛为"只读理解型"20 条后重跑。

## 2. 方法

### 2.1 两臂 + 预热

| 臂 | `knowledge.mode` | 作用 | 任务数 | 完成 | joined usage |
|---|---|---|---|---|---|
| `warm`（预热/播种） | `on` | 把探索节点写进共享 `knowledge.db`，让 `on` 臂起点就有可复用节点 | 11 | 8 | 10 |
| `off`（基线） | `off` | 对照 | 12 | 9 | 9（75.0%） |
| `on`（处理） | `on` | 被测 | 14 | 9 | 9（64.3%） |

两臂**共用同一个 `knowledge.db`**：off 臂跑完后的库状态即 on 臂的起点 —— 这正是"跨任务复用"要验证的形态。integrity 三档闸门判两臂均为 `PARTIAL`（joined < total），故结论按"部分样本"标注。

### 2.2 每任务新会话（`-SessionPerTask`）

单会话连打 20 任务时上下文单调增长：warm 臂第 9 个任务 `in_tok` 已 118952，历史更冲到 998000（模型 1M 窗口），随后任务全是超时噪声，token 无法按任务比较。改为**每任务一个干净会话**后：每任务 prompt token 从小上下文起算，两臂可逐任务配对；知识层跨任务复用走 per-workspace 的 `knowledge.db`（不依赖会话），被测机制仍完整生效；会话切换实测 ~0.8s。数据侧证据：两臂 `distinct_sessions` 等于任务数，无单会话串场。

### 2.3 空闲门（收口轮八最关键的驱动修复）

warm 臂实测踩到的坑：**模型在一个 turn 里跑起 14 分钟不返回的 shell**（屏幕 `Running shell (13m 55s)`，内容是一次 `Remove-Item`；同 turn reasoning 里留着 `Build OOM (errno 1455)`）。后果链条：

1. `/web/api/invoke` 在 **225s** 就带 `status=requires_approval` 返回，而服务端 turn 仍 `running`；
2. 后续任务被排进会话 pending 队列（`GET /web/api/turn` 显示 `pending=5`），**300ms 内返回 `requires_approval` + 0 token**；
3. `POST /web/api/sessions/new` 的切换指令也卡在队列里，t10 的 `session` 与 t09 相同 —— "每任务新会话"的隔离性静默失效。

修法（`.tmp\ab_driver.ps1`）：

- **每个任务 invoke 前过空闲门**：`GET /web/api/turn` 的 `current.busy` 必须为 false；
- busy > 20s 即 `POST /web/api/input {"type":"interrupt","discard_pending":true}` 强杀 turn 并等 `busy=false`；
- invoke 返回 `requires_approval`/`queued` 或正文为空 → 强杀 + 换干净会话重试（≤3 次），仍失败才如实记一条失败；
- 单任务 `timeout_ms` 240s → **90s**（实测正常任务 7–76s）。

### 2.4 交互禁令 + 单任务预算

off 臂修完空闲门后又暴露第二个阻塞源：**模型调 `ask_user_question` / `enter_plan_mode`**，无头驱动没有用户可答，工具阻塞 6m17s 且 invoke 不返回。修法：

- 两臂同口径追加 prompt 后缀：只读分析、不改文件、不跑 `go build/go test`、不提问、不用交互/派发工具；
- 单任务总预算 `-TaskBudgetSec 120~150`（跨全部 attempt）：超预算如实记 `budget_exceeded`，不再拖垮整臂。

修复后 t09/t10/t12 从"整臂报废"降级为"单任务失败（各 ~186s）"，两臂其余任务全部正常完成。

## 3. 口径

| 指标 | 口径 |
|---|---|
| 任务级成功 | turn 级：`/web/api/turn` 的 turn 成功且 usage 台账能 join（`turn_success`）。`expect_any` 工具命中口径在本任务集上**两臂均为 0**，不可用，故不以它下结论 |
| 防假成功 | 必须 `assistant.content` 非空。warm 臂教训：provider 配额耗尽时 invoke 仍返回 `status=completed` 且 `llm_observed=true`，60/60"成功"实为 `UPSTREAM_QUOTA_EXHAUSTED`；驱动已加"预检探针正文非空才放行整臂" |
| prompt / completion / cache_read token | 驱动直取 invoke 响应 `usage`；配对比较用分析器 `paired_analysis(base=off, treat=on)`，只统计两臂都 joined 的任务 |
| tool_calls | 同 turn 的 `usage_tool_calls` |
| 墙钟 | 驱动侧 `Stopwatch`（含 HTTP 开销） |
| integrity | `INVALID`（ok==0 / joined==0 / success==0）不出结论；`PARTIAL`（joined<total）出结论但标注样本损失；`OK` 正常 |

## 4. 数据

### 4.1 逐任务配对（n=8，两臂都完成的任务）

`prompt/tc/wall/ok` = prompt token / tool_calls / 墙钟 ms / turn_success

| 任务 | off | on | prompt Δ |
|---|---|---|---|
| t01 | 94400/5/20709/1 | 133053/6/57305/1 | +40.9% |
| t02 | 71895/4/15604/1 | 75427/4/46503/1 | +4.9% |
| t03 | 70431/3/13952/1 | 73966/4/32403/1 | +5.0% |
| t04 | 94797/5/34953/1 | 96248/3/32403/1 | +1.5% |
| t05 | 100913/6/24153/1 | 48718/1/24453/1 | **-51.7%** |
| t06 | 67174/2/7054/1 | 70885/2/25653/1 | +5.5% |
| t07 | 50780/3/24003/1 | 48339/3/31203/1 | -4.8% |
| t08 | 285440/15/45303/1 | 212306/8/76203/1 | **-25.6%** |
| t11 | 258510/15/67806/0 | —（on 卡死） | 不可配对 |

未配对：t09/t10/t12 两臂均 `budget_exceeded`；t11 仅 off 完成（on 卡死）；t13/t14 仅 on 有记录（t13 完成、t14 卡死）——off 臂在 t12 后被时间预算截断。

### 4.2 配对统计

| 指标 | off 均值 | on 均值 | 平均变化 | 中位变化 | 95%CI（变化量） | on 更低/更高/相等 |
|---|---|---|---|---|---|---|
| prompt_tokens | 104479 | 94868 | **-3.03%** | -3.22% | [-15.57%, +21.62%] | 3/5/0 |
| total_tokens | 105613 | 95508 | -3.54% | -2.81% | [-15.07%, +22.15%] | 3/5/0 |
| completion_tokens | 1134 | 640 | **-35.80%** | -20.02% | [-60.35%, -11.25%] | 8/0/0 |
| cache_read_tokens | 81695 | 69502 | -0.70% | -5.72% | [-29.44%, +30.85%] | 2/6/0 |
| tool_calls | 5.4 | 3.9 | -14.58% | 0.0% | [-12.30%, +41.46%] | 3/2/3 |
| wall_ms | 23216 | 40766 | **+107.85%（变慢）** | +100.23% | [+38.55%, +177.15%] | 1/7/0 |

（变化量 = on 相对 off；负值表示 on 更低。CI 为正态近似，n=8 时极宽，只用于判断"是否可区分于 0"。）

读法：**只有 completion token 是 8/8 同向下降且 CI 不跨 0**；prompt token 这条 M5 主判据的 CI 宽到跨 0，n=8 下无信号；墙钟在 on 臂明显更差（CI 完全 >0），是本轮唯一"显著变差"的指标。

### 4.3 机制侧证据（knowledge.db，只读）

- off 臂窗口（18:37–18:47）：`context_snapshots` **0 条**；
- on 臂窗口（18:55–19:15）：每任务 1 条快照，`budget_json` 形如
  `{"budget_filtered":0,"dropped":{},"floor_filtered":0,"injected":3,"mode":"signals","overridden_filtered":0,"reason":"ok","stale_filtered":0,"token_budget":800,"tokens":960}`；
- on 臂单任务 `injected` 区间 1–44 条，`context_items` 683 行，`exploration_nodes` 776 行。

即：**注入链路在 on 档确实工作**（此前只从配置推断，本轮拿到运行期一手证据），因此 §4.2 的"效果不显著"是**效果本身不显著**，而不是"没注入"。

## 5. 工程教训（已落地到驱动/工具链）

1. **同步 invoke 不是"超时即结束"**：`/web/api/invoke` 的返回状态与服务端 turn 生命周期不是同一件事。任何长跑驱动都必须自带 `busy` 空闲门。
2. **会话切换也会被 pending 队列拖住**：卡住的 turn 让 `POST /web/api/sessions/new` 只返回 `{"status":"queued"}`；必须轮询 `current_session_id` 变化，不能 sleep 固定时间。记录里 `session` 字段相同就是隔离失效的证据。
3. **交互工具是无头驱动的头号杀手**：`ask_user_question` / `enter_plan_mode` 在无用户环境会阻塞到天荒地老，且不受 `timeout_ms` 约束。评测 prompt 必须显式禁用。
4. **假成功**：`status=completed` 但正文为空 = 上游没真答（配额/鉴权）。预检必须要求"正文非空"。
5. **仓库副本隔离 A/B**：模型写探针文件、跑 build 都不会污染真实工作区（本轮确实写了 `zz_probe_case_test.go` 并触发 `Build OOM (errno 1455)`）。

## 6. 登记（缺口与后续）

1. **M5 未验收**：需 ≥20 个配对任务。下一轮把任务集收敛为 20 条**只读理解型**任务（不触发 build / 不触发交互），两臂各跑满 20，预期 15–20 对。
2. **prompt token 无信号需要解释**：注入预算只有 800 tokens（`token_budget=800`），而两臂 prompt 基线在 5 万–28 万量级 —— 注入量相对基数过小，理论上就难以体现 ≥25% 的下降。要么把任务集换成"大范围检索型"（注入收益占比高），要么承认 M5 的 token 判据需要按"注入命中的那部分 turn"重算，而不是全 turn 平均。
3. **墙钟回退需定位**：on 臂 +76%（CI >0）。可能来自注入带来的额外编译/检索开销，或 on 档 index 更热导致 `code_*` 工具等待。下轮要把"工具调用耗时"拆出来单独看。
4. **completion token -35.8% 与 prompt token -3% 的矛盾**：on 臂输出更短但输入没降，提示 on 档更早给出结论、但没有少读代码（tool_calls 只降 14.6% 且 CI 跨 0）。需核对 `context_items` 的 `trust/confidence` 分布是否让模型跳过了验证步骤 —— 若成立，是**正确性风险**而非纯收益，必须在验收里单列。
5. **驱动修复需回归**：空闲门 / 交互禁令 / 单任务预算三处修复在 `.tmp\ab_driver.ps1`（未入库）。若后续要把 A/B 变成常规验收脚本，应迁到 `backend/scripts/` 并补一个"注入 turn 阻塞"的回归用例。

## 7. 复现方式

```pwsh
# 1) 预热（播种 knowledge.db）
$sfx = "`n`n【A/B 驱动约束】只做只读分析；不要改文件；不要跑 go build/go test；不要提问或使用交互工具。"
pwsh -File .tmp\ab_driver.ps1 -Arm warm -Mode on  -SessionPerTask -TimeoutMs 90000 -TaskBudgetSec 150 -PromptSuffix $sfx
# 2) 两臂（顺序执行，共用同一 knowledge.db）
pwsh -File .tmp\ab_driver.ps1 -Arm off -Mode off -SessionPerTask -TimeoutMs 90000 -TaskBudgetSec 120 -PromptSuffix $sfx
pwsh -File .tmp\ab_driver.ps1 -Arm on  -Mode on  -SessionPerTask -TimeoutMs 90000 -TaskBudgetSec 120 -PromptSuffix $sfx
# 3) 分析（per_task + 同任务配对 + integrity）
$env:PYTHONIOENCODING='utf-8'; py -3 .tmp\ab_analyze.py     # → .tmp\ab_report.json
```

产物：`.tmp\ab_run_{warm,off,on}.jsonl`、`.tmp\ab_report.json`。