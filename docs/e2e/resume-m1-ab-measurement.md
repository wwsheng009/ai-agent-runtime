# M1 A/B 实测记录（L2.1 + L3）— 2026-09-25

> 结论先行：**本机环境不满足测量条件**（期间发生内存耗尽 / 页面文件不足，PowerShell 自身
> 出现 `0x800705AF 页面文件太小`，第二次 aicli 实例 `exit_during_run=2`）。
> 三次运行的同相位数字互相矛盾（同一份代码、同一会话，`resume_metadata` 在 135 ms 与
> 4035 ms 之间摆动），**因此不能用这批数据支持或否决 M1 的任何性能结论**。
> 可支持的唯一结论见 §3：**M1 不是启动回退的原因**（对照组同样慢，甚至更慢）。

## 1. 三次运行的同相位对照（同一会话 `session_20260924072950_ltYRU9tG`）

单次运行的噪声很大，因此下表只列**同相位**（`AICLI_STARTUP_TIMING` 打点，单位 ms）：

| 阶段 | 基线 `20260924-234117`（纯净，23:41） | A `m1-after-l2-1-plus-l3`（L2.1+L3，06:19:54） | B `m1-b-l21-only`（L2.1，**L3 关闭**，06:23:26） | A′ `m1-a-l21-plus-l3`（L2.1+L3，06:25:10，**中途 exit=2**） |
|---|---|---|---|---|
| persistence | — | — | 476 | 358 |
| resume_metadata | — | — | **4035** | **135** |
| eventlog_read | 102 | 489 | 327 | 261 |
| eventlog_parse | 1530 | 2714 | 2414 | 1545 |
| eventlog_apply | 1586 | 3156 | 2924 | 1933 |
| history_seed | 1192 | 2231 | 2789 | 1909 |
| ready | 299 | 629 | 696 | 445 |
| **ready 合计** | **5421** | **10720** | **14096** | **6930** |
| 收敛 | 34524 | 71306 | 57760 | n/a（崩溃） |
| first_recovery | 6567 | 12546 | 16094 | 7940 |

运行日志：`artifacts/aicli-resume-startup-perf-e2e/<artifact>/summary.json` 与 `run.log`。

## 2. 判定（P 项）

| 判定 | 基线 | A | B | A′ |
|---|---|---|---|---|
| P2 ready 预算（≤3000ms） | FAIL 5421 | FAIL 10720 | FAIL 14096 | FAIL 6930 |
| P9 首内容预算（≤8000ms） | FAIL 11222 | FAIL 19799 | FAIL 23687 | FAIL 16410 |
| P11 replay 体积 | FAIL | FAIL | FAIL | FAIL |
| P12 UI 冻结（≤2000ms） | FAIL 3041 | FAIL 3065 | FAIL 3081 | FAIL（中断） |
| P10 交付单调 | **PASS** | **FAIL**（2 次回退） | **FAIL**（2 次回退） | 中断 |
| P6 收敛（plan_stalled） | PASS | PASS | PASS | n/a |

**P10 是本次唯一「基线绿 → 变更后红」的项，必须当成未决风险**：
A/B 两次都在恢复中期出现 `acked` 回退（`2926->0@26928ms`、`2926->366@30592ms`）。
但 A/B 的 `eventlog_apply`（3156/2924）比基线（1586）慢约 85%，恢复期整体被拉长到 57–71s，
而基线只有 34.5s —— 在这种「全线慢 1.8~2 倍」的机器状态下，**超时/预算类回归无法归因到代码**。
→ 处置：**不撤回代码、也不宣称通过**；在安静机器上按 §4 复跑后再定。
若复跑仍复现 P10 回退，则 L2.1 的 deadline 重排必须回退或加开关。

## 3. 为什么可以排除「M1 导致启动回退」

1. **对照组同样慢，甚至更慢**：B 关掉了 L3（`AICLI_SQLITE_WARMUP=0`，等价基线行为），
   `ready 合计` 反而是三次里最高的 14096 ms；A 与 B 的 `eventlog_parse/apply` 落在同一区间
   （2714/3156 vs 2414/2924）。
2. **同代码、反向排序**：A′ 与 A 是同一份代码（L3 均开启），A′ 的 `eventlog_parse=1545`、
   `eventlog_apply=1933` 与**基线**（1530/1586）几乎一致；而关闭 L3 的 B 却是 2414/2924。
   差异跟着机器负载走，不跟 L3 走。
3. **受影响的相位与改动无关**：膨胀最大的是 `eventlog_parse`（纯 `json.Unmarshal` 解码 91 MB）
   与 `history_seed` —— L2.1 只改 UI 侧 deadline 顺序，L3 只把 WASM SQLite 编译挪到启动早期，
   两者都不可能让纯 JSON 解码慢 60%。
4. **机器证据**（查询时刻）：物理内存 **14,198 MB，空闲仅 3,095 MB**；
   `Memory Compression` 进程 395 MB；同机还有其它 `aicli`/`aicli-3x` 实例与 Edge、Defender；
   运行期间 PowerShell 自身报 `0x800705AF 页面文件太小`，第二次 aicli 实例 `exit_during_run=2`。
   → 三次运行是在**换页**状态下跑的；此外每次 stall 触发的 `runtime.Stack(_, true)`（本轮 1–3 次/次运行）
   会 **stop-the-world**，在内存压力下进一步放大该次运行的后续所有相位耗时。

## 4. 复跑配方（放行 M1 前必须做）

**前置条件（每次运行前逐项确认，任一不满足则本次数据作废）**

1. 空闲物理内存 ≥ 6 GB（本轮为 3.1 GB）。
2. 无其它 `aicli` / `aicli-*` / 其它 agent 的 harness 实例在跑（`Get-Process aicli*`）。
3. 关闭浏览器与杀软实时扫描（或把 repo 与 `.tmp` 加入排除项）。

**跑法**

- 每个配置 **跑 3 次**，按 `A B A B A B` 交替（抵消负载漂移），取**中位数**而非单次值。
- 每次运行后检查 `run.log`：出现 `exit_during_run=` 非空 → 该次作废重跑。
- 关注量按优先级：`ready 合计`（P2）、`eventlog_parse`、`eventlog_apply`、`first_recovery`（P9）、
  `P10 acked 回退次数`、`P12 最长冻结`。

**判定口径**

- L3 的收益应表现为 **A 相对 B 的 `persistence`/首个用到 SQLite 的相位**差值 ≈ 1.0–1.1 s
  （WASM 编译成本），而不是启动总量的大幅变化。
- L2.1 的收益应表现为 **P10 不再出现 acked 回退**，且 `plan_stalled` 保持 False。
- **若安静机器上 A 与 B 的差值仍小于运行内噪声（中位数差 < 15%），结论应记为「L3 收益不可测」，
  并考虑把 L3 变成可配置项而不是默认开启**（当前已有 `AICLI_SQLITE_WARMUP=0` 开关）。
