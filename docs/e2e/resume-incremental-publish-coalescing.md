# 恢复历史的「边读边画」成本归因与发布合并（2026-09-25）

> 结论先行
> 1. 逐页读取（read+prepend）**5–32ms/页**；逐页 **数据面 reconcile 12–70ms/页**；
>    **帧请求在补齐线程上 ≈0s** —— 每次「发布」的真实成本表现为**争用**：帧会让
>    UI actor 做整份转录重规划，补齐线程随后在锁/队列上排队，实测表现为「按页发布」
>    时读取标记膨胀到 330–700ms/页。
> 2. 因此把**发布**（统一帧）按步长合并、**读取与前插**保持逐页，是本路径的正确切法：
>    同一会话 42 页 canonical 历史，首帧不变，全量历史就绪 **11.0s → 7.4s**（−33%）。
> 3. 装载收尾那一次（全量 seed + 授权式替换）实测 **1.04s**（`resume_history_complete`
>    − `resume_history_deferred`），它**不是重复劳动**：按步长合并后未发布的页只是在
>    展示历史上前插，必须由收尾这一次 reconcile 真正插入 Scene，否则用户滚不到。
> 4. 「给匹配加 item 侧归一化缓存」**经基准否决并已回退**（见 §4）。
> 5. 本机端到端单跑抖动可达 ±40–100%（同变体两跑 4.62s vs 3.17s），**小 delta 一律
>    用进程内基准**，端到端只用于正确性与量级。

## 1. 阶段表口径（`AICLI_STARTUP_TIMING=1`）

```
resume_history           同步只读最新一页（窗口化装载的首页）
ready                    首帧：最新一页 seed + 统一帧 + composer
resume_history_page      逐页「读取 + 前插展示历史」（每页一条）
resume_history_reconcile 该页数据面 reconcile：建 unit + 锚点插入 + 非授权快照
resume_history_publish   该页之后请求统一帧（异步，补齐线程上约 0s）
resume_history_deferred  补齐段结束
resume_history_complete  装载收尾：全量 seed + 授权式原生 scrollback 替换
```

`resume_history_page` 与 `resume_history_publish` 分开，是为了避免「发布合并」把
观测面读成「页变少了」；`resume_history_reconcile` 再从发布里分出数据面成本。

## 2. 发布合并 A/B（同一会话，42 页）

| 变体 | ready（首帧） | 发布帧数 | 全量历史就绪 |
|---|---|---|---|
| 逐页发布（stride=1） | 3.135s / 2.838s | 13 | 11.147s / 10.990s |
| 固定步长 3（上一版） | 3.071s / 3.348s | 4 | 7.644s / 7.660s |
| **按规模有界（现行，本例步长 8）** | **3.044s / 3.041s** | **2** | **6.759s / 6.634s** |

- **首帧不受影响**，全量历史就绪相对固定步长再提前 **~0.95s（−12%）**；
  `resume_history_complete − resume_history_deferred`（收尾）两版同为 ~1.0s——发布合并
  只是把「未发布页的插入」推到收尾，插入总量不变。
- 发布帧数少于「页数/步长」：被事件日志重放已表达过的页会 `seeded=false`（
  `renderResumeHistoryPageIncremental` 返回 false），没有可见变化就不发布。
- 代码：`resumeHistoryIncrementalPublishStride(page)`（按首页 `Total` 与观测页大小
  估算页数，把发布次数钳在 ~6 步，下限步长 3）+ `shouldPublishResumeHistoryIncrementalPage(pages, stride)`；
  首页必发，尾部由收尾的授权式快照兜住。**发布次数随历史规模有界**是这条策略的
  关键：固定步长下 400 页会画 133 帧，按规模钳位后仍是 ~6 帧。

## 3. 收尾成本（末位测量）

| 项 | 实测 |
|---|---|
| 收尾（全量 seed + 授权替换） | **1.04s**（8.70 − 7.66） |
| 其中：unit×item 匹配（3600 items 基准） | ~25–30ms |
| 其中：未发布页（约 28 页）真正插入 Scene | 主要成本 |
| 首次补齐页的 reconcile | 1.26s（与首帧规划争用）；其后各页 12/54/70ms |

⇒ 收尾的压缩空间不在「匹配」，而在「把未发布页的插入提前」——但那要求每页都
reconcile（≈45ms/页 × 28 ≈ 1.26s），与省下的 1.04s 基本相抵，**当前切法是局部最优**。
真正的下一步在规划器：单次发布的整份重规划（合成语料 6720 cells 实测 602ms，
见 `resume-l2-5-deferred-replan-measurement.md`）与 `State()` 深拷贝地板
（同文档 §8 / 计划 §4.7、§4.8）。

## 4. 被否决的改动：item 侧归一化缓存（已回退）

假设：推理块匹配要对每个 (unit, item) 组合重算 `persistedReasoningBody(head)`
（含 CR 时整段复制），真实会话是 O(units×items) 次 KB 级拷贝。

实测（`BenchmarkPersistedHistoryUnitMatch`，1800 items × 1800 units，推理 3KB）：

| 语料 | uncached（旧行为） | cached |
|---|---|---|
| LF | 25.7ms / 9 allocs | 27.2ms / 26 allocs |
| CRLF | 26.9ms / 1209 allocs | 27.3ms / 1226 allocs |

结论：**匹配不是瓶颈**。`matched` 作用域让扫描短路（每个 unit 只做约 1 次真比较），
且 `strings.ReplaceAll` 无匹配时不分配、`TrimLeft` 只切片——归一化根本不复制文本。
基准保留为回归护栏（防这条内层循环将来退化成真正的二次方）。

## 5. 复跑配方

```powershell
# 端到端（启动窗口化）
pwsh -NoProfile -File backend\.tmp\probe-resume-streaming.ps1 -Windowed 1 [-ExePath <exe>]
# 会话内 /resume
pwsh -NoProfile -File backend\.tmp\probe-resume-in-session.ps1 -Windowed 1
# 匹配护栏基准
go test ./cmd/aicli/commands/ -run '^$' -bench UnitMatch -benchmem
# 正确性门禁（真实会话）
pwsh -NoProfile -File scripts/test-aicli-resume-history-e2e.ps1 -SessionId <id>
```

## 6. 否证：「最新几页内容更重」不是首次 reconcile 贵的原因

补齐段第一次 reconcile 稳定地比其后各页贵 8~25×（三次独立运行内比例一致：1.264s/54ms、2.028s/263ms、2.482s/120ms），且启动路径与会话内 `/resume` 两个流程都一样。为排除「这一页本身很重」，直接查存储里每 100 条一页的体积（只读）：

| 页（newest-first） | 1 | 2 | 3 | 4 | 5 | 6 | 7..43 均值 | 最重页 |
|---|---|---|---|---|---|---|---|---|
| 体积 | 480KB | **503KB** | 538KB | 593KB | 608KB | 446KB | 491KB | 740KB（第 26 页） |

全库 4221 条 / 21.3MB，页体积均匀。**第 2 页并不重**（正好在均值线上），因此异常点不在内容量级上，而在代码路径里。诊断脚本：`backend/.tmp/probe-db-page-sizes.py`。

## 7. 定位：贵的是**非授权快照**，不是建 unit / 匹配插入

给逐页 reconcile 加三段打点后（`resume_history_reconcile_units` / `_apply` / `resume_history_reconcile`），同一会话实测（机器有负载的一次运行）：

| 发布序 | units（锁外建 unit） | apply（锁内 Snapshot+匹配+插入） | snapshot（非授权快照） |
|---|---|---|---|
| 第 1 次（visited 1） | 103ms | 216ms | **2.809s** |
| 第 2 次（visited 8） | 7ms | 209ms | —（`seeded=false`，无变化不发布） |
| 第 3 次（visited 16） | 5ms | 104ms | —（同上） |
| 第 4 次（visited 24） | 7ms | 53ms | 135ms |
| 第 5 次（visited 32） | 11ms | 51ms | —（同上） |
| 第 6 次（visited 40） | 10ms | 58ms | —（本规则跳过） |

- 数据面总共只花 ~100-220ms/页；**贵的全是 `sessionInteractionSnapshot()`**（`postTranscriptSnapshotFromBridge`）：首次 2.3-2.8s，其后 135-767ms。
- 首次特别贵，符合「控制器/语义转录首次装配」的一次性成本；它发生在补齐段而不是 `ready` 之前，已经是正确的放置（`ready` 不受影响）。
- `seeded=false` 的页天然跳过快照（「看不见的变化不发布」），这是早先就有的正确行为。

## 8. 落地：跳过「必被收尾覆盖」的那次快照

`resumeHistoryIncrementalStepIsLast(visited, stride, estimatedPages)` —— 剩余估算页数已不足一个步长时，本次发布之后紧跟的就是装载收尾的授权式替换，非授权快照必被整份覆盖，因此只做数据面、不铸造快照：

- 阈值必须用「剩余页数 < 步长」而不是 `!page.HasMore`：42 页 + 步长 8 时最后一次发布在第 40 页，其后仍有 41/42 页，`!HasMore` 永远不命中（这正是第一版规则实测无效的原因）。
- 页数未知（`Total<=0`）时保守返回 false：宁可多花一次快照，也不让补齐段退化成「最后一步看不见」。
- 实测命中：visited=40 时 `resume_history_reconcile` 打点消失（即跳过），该次原本要花 ~0.25-0.77s。
- 门禁：harness **7 passed / 0 failed**（`cells=4560 acked=87191 plan_incomplete=false plan_stalled=false`，exit 0）→ `artifacts/aicli-resume-history-e2e/skip-last-snapshot`。

## 9. 遗留：首次快照的 2.3-2.8s

它是「语义转录首次装配」的一次性成本，落在补齐段的第一次发布上（不影响 `ready`）。要再压需要**增量快照**（只把新增的头部单元格推给语义转录，而不是整份重装）——属规划器/控制器工作流（L2.6/L2.7 邻域），不在装载机制内；本文件的量测可作为验收基线：首次快照 < 300ms、其后 < 50ms。

## 10. 否证「增量快照」方向：贵的是投递，不是构造

把快照发布拆成 `transcript_snapshot_build`（生产侧把整份 Scene 拷成不可变快照）与
`transcript_snapshot_post`（投进 UI actor）后，同一会话实测：

| 发布 | build | post |
|---|---|---|
| 逐页补齐第 1 次（首步） | 123-134ms | **1.082-1.120s** |
| 逐页补齐其后各次 | 0-2ms | 0-645ms（被非阻塞 ingress 吸收时为 0s） |
| 装载收尾的授权式替换 | 1.8-6.3s | 0-483ms |

**结论：原计划的「只推新增头部单元格」收益上限只有那 0-134ms，而占大头的 1.1-2.8s 在
投递侧**——`postUIAction` 在首帧投递期间阻塞（`sceneSnapshot()` 构造本来就便宜）。
因此 §9 的验收基线改口径：不再以「增量载荷」为目标，改为「后台步子不得让生产者停等」。

## 11. 落地：逐页发布的快照三档策略

`resumeHistorySnapshotModeForStep(visited, stride, estimatedPages, hasMore)`：

| 档 | 场景 | 行为 |
|---|---|---|
| `Await` | 首步（visited==1） | 阻塞投递，保证「边读边画」第一次可见 |
| `Try` | 中间步子 | `tryPostUIAction` 非阻塞：邮箱满即**放弃这次中间态**，绝不把 actor 的忙碌回压到补齐线程 |
| `Skip` | 末步 / 最后一页 | 完全跳过（收尾的授权式替换必整份覆盖它） |

- 新增 `tryPostTranscriptSnapshotFromBridge`（生产侧入口）与
  `sessionInteractionSnapshotNonBlocking`（bridge 侧入口）；`Try` 的语义与流式运行事件
  用的非阻塞 ingress 一致：**放弃是安全的**——数据面（锚点插入）已完成，内容留在 Scene，
  下一次发布或收尾的授权式替换会重新发布完整 generation。
- 代价与边界：`Try` 在 actor 持续繁忙时会让某个中间态不出现在语义转录里（该页仍会出现在
  收尾的全量发布中）。若更看重「每一步都可见」而不是补齐速度，把中间步子改回 `Await`
  即可（一行），实测代价是每步 0.25-0.65s。
- 验证：门禁 **7 passed / 0 failed**（`artifacts/aicli-resume-history-e2e/try-post-final`）；
  聚焦测试 `-run 'ResumeHistory|Resume|Load|Snapshot'` 通过；`Try` 档实测 `post=+0s`，
  首步仍保留阻塞投递。

## 12. 遗留（下一次可做的「真增量」）

收尾那次**授权式替换**的快照构造在重载下要 1.8-6.3s（full-Scene 拷贝）。这里才是增量
真正有价值的位置：收尾需要全量语义，但可以复用上一份快照 + 只补增量单元格，属
规划器/Scene 层（L2.6/L2.7 邻域）。注意它与本节结论不矛盾：逐页补齐不需要增量载荷，
收尾需要。
