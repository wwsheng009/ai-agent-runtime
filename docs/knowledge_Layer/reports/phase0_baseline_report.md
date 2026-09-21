# Phase 0 基线报告（索引侧已实测 / 任务侧待跑）

> 模板：`04` §7.7 ｜ 交付项：`06` §4 Phase 0 交付 3、附录 A 步骤 7
> 数据面：`backend/internal/knowledge/baseline.go`（`BaselineReport.Summarize` / `Percentiles`）
> 状态：**部分完成**。索引侧（§2、§3）已实测且可复算，含 2 个外部仓库样本（§2.4）；任务侧 5–10 个代表任务需要真实跑 agent 才能产出，见 §6。

---

## 1. 样本与配置

```text
Phase: 0
样本:  本仓库 E:\projects\ai\ai-agent-runtime（3860 个候选文件）
       外部仓库 gin-gonic/gin          master @ 3b08cd72（99 个候选文件）
       外部仓库 prometheus/prometheus  main   @ 1d6fe378（1010 个候选文件）
时间:  2026-09-20
配置:  knowledge.mode = shadow（只建索引，绝不注入提示词）
       adapter = regex_builtin（AdapterVersion = builtin/3）
       embedding = 关闭；无出网
范围:  排除 .git / .aicli / node_modules / dist / build / vendor / target / __pycache__ 等
       （内置 ignoreDirs，见 indexer.go）；另外整目录跳过所有以 "." 开头的目录
索引口径: 轻索引 = 文件 + **顶层符号** + imports（`04` §2 Lazy 原则）。
       函数体内的局部 var/const/let 不索引（builtin/3 起，见 §4.4）
```

复算命令（PowerShell）：

```powershell
cd backend
$env:KNOWLEDGE_BASELINE_REPO = (Resolve-Path ..).Path
go test ./internal/knowledge/ -run TestBaselineFullIndex -v -count=1
```

该测试默认 `t.Skip`，只在显式给 `KNOWLEDGE_BASELINE_REPO` 时测量；输出 Scanned / Indexed /
Skipped / Symbols / Refs / 首次全量耗时 / 二次增量耗时 / DB 字节 / `adapter_conflict` 计数。

---

## 2. 索引侧结果（实测）

### 2.1 首次全量（冷启动，空库）

| 指标 | 值 |
|---|---|
| Scanned / Indexed / Skipped / Errors | 3860 / 3860 / 0 / **0** |
| Symbols（解析产出） | 43587 |
| Refs（解析产出） | 380657 |
| 首次全量耗时 | **146.9s**（2m26.85s） |
| DB 字节 | 259543040 = **247.5 MiB** |

### 2.2 二次运行（无文件变更）

| 指标 | 值 |
|---|---|
| Scanned / Indexed / Skipped | 3860 / **0** / 3860 |
| 耗时 | **1.47s**（0.38ms/文件，整轮扫描口径） |

`Indexed=0` 是增量判据（`files.content_hash` + mtime/size 预筛）生效的直接证据：
无变更时一行都不重写。该测试还会对两轮之间的文件元数据做 `diffFileMeta`，
若 `Indexed>0` 会打印出具体是哪个文件变了，避免"增量失效"被误判成"测试抖动"。

### 2.3 身份与覆盖

| 指标 | 值 |
|---|---|
| `symbols` 表行数 | 42855 |
| 解析产出 − 落库 = 被合并的身份 | 732（1.7%），涉及 229 个文件（229 条 `adapter_conflict` 事件） |
| `refs` 表行数 | 380657（与解析产出一致，引用不合并） |
| 覆盖率判据 | `Errors=0` 且 `Indexed=Scanned` |

### 2.4 外部仓库对照（交付 3 的"1 个外部 Go 仓库"）

两个外部样本用与 §1 **完全相同**的命令测量，仓库以 codeload tarball 取到临时目录
（`git clone` 在本机网络下只有 ~13 KB/s，7 分钟只拉到 5 MB，故改用 tarball + `tar -xzf`）：

| 指标 | gin（99 文件） | prometheus（1010 文件） | 本仓库（3860 文件） |
|---|---|---|---|
| Scanned / Indexed / Errors | 99 / 99 / 0 | 1010 / 1010 / 0 | 3860 / 3860 / 0 |
| 首次全量 | 2.66s | 47.3s | 146.9s |
| 每文件 | 26.9ms | 46.9ms | 38.1ms |
| refs | 9889（99.9/文件） | 131893（130.6/文件） | 380657（98.6/文件） |
| **每 ref** | **0.269ms** | **0.359ms** | **0.386ms** |
| DB | 6.6 MiB | 81.3 MiB | 247.5 MiB |
| 每文件 | 71.6 KiB | 84.3 KiB | 65.7 KiB |
| **每 ref** | **701 B** | **647 B** | **682 B** |
| 二次运行（无变更） | 0.28ms/文件 | 0.38ms/文件 | 0.38ms/文件 |
| 合并身份 | 17（1.06%） | 160（1.24%） | 732（1.68%） |

两条结论：

1. **"每文件"不是稳定口径**：26.9 / 46.9 / 38.1 ms 与仓库大小不单调，随每文件的引用密度变化。
   prometheus 的 130.6 refs/文件（含 `prompb/*.pb.go` 这类生成代码）比另两个样本高约 30%，
   单文件成本也最高。
2. **"每 ref"才是稳定口径**：0.269–0.386 ms/ref 与 647–701 B/ref，跨语言、跨仓库规模都在 ±20% 内。
   原因是写入成本几乎全部落在"每条引用一行 INSERT"上（`refs` 行数占 DB 的绝对多数）。

残余合并率三个仓库都在 1–2%（§4.5 的两类成因：同目录多编译单元、模块=文件的语言），
说明这不是本仓库的特例。

---

## 3. 门槛判定（对照 `06` §4 Phase 1 初值）

| 门槛（`06` §4 Phase 1） | 实测 | 判定 | 说明 |
|---|---|---|---|
| 首次全量 ≤ 120s（初值） | 146.9s | **Fail** | 见 §5 的校准建议 |
| DB ≤ 200MB（初值） | 247.5 MiB | **Fail** | 同上 |
| 单文件增量 < 50ms | 只能给上界 0.38ms/文件 | **不可判定** | 见 §4.7 口径 |
| `files.content_hash` 与磁盘一致率 100% | 抽样即全量（3860/3860 未变即跳过） | Pass | 见 §2.2 |

**为什么仍记录为 Fail 而不是改门槛**：`06` §4 的 Phase 1 门槛是**验收标准**，只能由 `04` §7.6
的流程（Phase 0 出基线 → 校准阈值 → 写入 config 默认值）改写，不能由本报告自行放行。
现有 3 个仓库样本（§2.4；n=3 仍不足以定 95% CI），§5 给出可执行的口径建议；
在 `04` §7.4 定稿前，本仓库对这两条门槛**保持 Fail**。

---

## 4. 发现（含反例）

### 4.1 缺陷：`stable_key` 缺 `namespace`，35% 的文件被静默降级（已修）

`04` §4.4 的公式要求 `normalize(namespace_or_package_or_module)` 参与 `stable_key`，而
`buildSymbol` 把它传成了空串。后果不是"少几个符号"，而是：

- 不同目录下的同名同签名符号（Go 仓库里最典型的是每个 `cmd/*/main.go` 的 `func main() {`、
  各处的 `type Config struct {`）算出**同一个** `stable_key`；
- `ReplaceSymbols` 的 `INSERT` 撞上 `idx_symbols_stable` 的 workspace 级唯一约束并**整份事务失败**，
  该文件的 `symbols` 与 `refs` 一起丢失（`RunIndex` 只把它计入 `Errors`，表面看像"解析失败"）；
- 实测：3860 个候选文件里 **1365 个（35%）**如此，`symbols` 少写 38551 行（22348 vs 60899）。

修复：`symbolNamespace(file)` 取文件所在目录（`adapter_builtin.go`），并顺手把
`AdapterVersion` 升到 `builtin/2`，让 builtin/1 写出的错误身份表自然失效。
同时按 `04` §4.4 的歧义规则加固写路径：撞键时保留身份行、改挂宿主、记 `adapter_conflict`
（`store_sqlite.go`），不再让一个坏身份毁掉整份文件的符号与引用。

回归用例：`symbol_identity_test.go`（跨目录不同身份 / 同目录移动身份不变 / 冲突不失败且留事件 / 无冲突不产生事件）。

### 4.2 反例：修复前的 65s / 100MiB 是"假基线"

第一次测量（builtin/1）得到"首次全量 65.1s、DB 100.2 MiB"，看起来轻松满足 Phase 1 的
≤120s / ≤200MB。但那份索引**少了三分之一的文件**（`indexed=2493`，`errors=1365`），
所以它测的不是"索引这个仓库要多久"，而是"索引三分之二的仓库要多久"。

这是本次基线最重要的方法论教训：**基线必须先证明覆盖率（`Errors=0`、`Indexed=Scanned`），
再看耗时与体积**；否则阈值会被"少干活"自然满足。`baseline_index_test.go` 因此把
`Errors`、`Symbols` 与 `stats.Symbols` 的差额一起打印出来。

### 4.3 索引范围口径：以 "." 开头的目录被整目录跳过

`collectIndexableFiles` 对 `d.Name()` 以 "." 开头的目录直接 `SkipDir`（比 `ignoreDirs` 更宽）。
本仓库按扩展名统计有 4969 个候选代码文件，索引器实际只收 3860 个，差额全部来自
`.github/`、`.devcontainer/` 这类点目录。这不违反任何门槛（点目录通常是配置与 CI 脚本），
但**必须写进口径**，否则"候选文件数"在工具与索引之间对不上。

### 4.4 局部变量被当成符号：5080 行身份合并的真因（builtin/3 已修）

namespace 修复后仍有 1093 个文件撞身份、5080 行被合并。当时的第一版归因（"同包内合法的重名
工具函数"）**是错的**——用一次性诊断用例（对全仓跑一遍 `Extract` 并按 `stable_key` 分组）测得：

| 维度 | 合并行数 |
|---|---|
| kind=**variable** | **4295**（84.5%） |
| kind=function / type / method / constant / interface | 455 / 197 / 101 / 28 / 4 |
| 语言 | typescript 2525、go 2260、javascript 291、python 4 |

真实成因是**适配器的取符号范围过宽**，不是身份公式有歧义：

- Go/TS 的 `var` / `const` / `let` 规则表允许行首缩进，于是**函数体内的局部变量**也被当成符号。
  典型样本：`var output bytes.Buffer` 在一份测试文件里出现两次（x92）、在同一个目录的不同测试文件里
  出现 149 次——同目录 + 同签名 + 同 owner（空）⇒ 同一个 `stable_key`。
- 这与 `04` §2 的 Lazy 原则直接冲突：**"v1 默认只索引文件 + 顶层符号 + imports；方法体、局部变量、
  类型关系不索引"**，`04` 术语表也把"局部"归入按需的 Deep Index。适配器（builtin/2）实现了 Deep
  的行为却挂在 Light 的路径上。

修复（builtin/3）：`topLevelOnly(language)` 对 go / typescript / javascript 只接受**顶格声明**
（`isIndented` 判行首空白），Python 的 `^def`/`^class` 早已是同一规则，Rust 的 impl 方法依赖缩进
作用域故不适用。回归用例：`adapter_scope_test.go`（顶层符号必索引 / 局部变量必不索引 /
缩进行里的调用点仍产出引用 / Rust impl 方法不受影响）。

实测效果（同一 workspace，builtin/3 多一个新增测试文件，故同时给每文件口径）：

| 指标 | builtin/2 | builtin/3 | 变化 |
|---|---|---|---|
| 解析产出 symbols | 60904（15.8/文件） | 43587（11.3/文件） | **−28.4%** |
| 解析产出 refs | 372286（96.5/文件） | 380657（98.6/文件） | +2.2%（见 §4.6） |
| 首次全量 | 175.6s（45.5ms/文件） | 146.9s（38.1ms/文件） | **−16%** |
| DB | 252.8 MiB（67.1 KiB/文件） | 247.5 MiB（65.7 KiB/文件） | −2.1% |
| 合并身份 | 5080（1.4 文件/事件，1093 事件） | **732（229 事件）** | **−85.6%** |

### 4.5 残余合并（732 行）的归因与处置

builtin/3 之后剩下的 732 行合并不是"局部变量"这类噪声，而是**同一目录下合法的多个编译单元**：

- `backend/scripts/*.go`：20 个独立 `main` 程序共用目录（`//go:build ignore` 隔离），
  `func main()`、`type Message struct {` 因此同名同签名（各 x20 / x18）；
- Go 的 `package foo` 与 `package foo_test` 可在同一目录共存（`_test.go` 常另立包）；
- TS/JS 每个文件是独立模块，但同目录的两个测试文件可以各自定义
  `type ReactActEnvironmentGlobal = …`（x38）、`function respondWith(…)`（x19）。

即：**namespace=目录 对"包=目录"的语言（Go 主路径）是对的，对"模块=文件"的语言（TS/JS/Python）
过粗**。当前处置：合并为一行（last-writer-wins）+ 记 `adapter_conflict` 事件，**不做**置信度降级
（v1 DDL 的 `symbols` 表没有 `confidence` 列，见 `CHANGELOG.md` 的 Deviations）。
收敛路径留给 Phase 1：把 namespace 改为"Go=目录、TS/JS/Python=文件（模块）"，并给 build-tag
隔离的文件加文件名分量；届时需再升一次 `AdapterVersion` 并重跑本报告。

### 4.6 口径变化：refs +8371 是修复的副产物，不是回归

builtin/2 时，缩进的 `var x = f(...)` 行会被声明规则匹配，随后 `continue` 跳过调用点扫描——
**行内的真实调用 `f(...)` 因此丢失**。builtin/3 不再把这些行当声明，它们落进调用点扫描，
于是 refs 反而增加 2.2%。即 refs 的上升与 symbols 的下降同源：都在把"轻索引"从错误的作用域
收回到正确的作用域。

### 4.7 增量口径

"单文件增量 < 50ms"（`04` §7.4）在本测试里只能测到**全库无变更时的整轮扫描成本**
（§2.2 的 0.38ms/文件），不是"改一个文件后重新索引该文件"的耗时——后者要等
`index_jobs` 明细（Phase 1 交付 5）落地才能按 p95 统计。当前数字只能作为上界参考。

---

## 5. 结论

| 项 | 判定 |
|---|---|
| 基线可复算（同一份数据两次结果一致） | **Pass**（`baseline_test.go` 的确定性复算用例 + 本报告的命令） |
| `mode=off` 行为不变 | **Pass**（知识层全部为新增包/新增列，默认 `off`；`usageledger` 用幂等补列兼容旧库） |
| 索引覆盖率（`Errors=0`、`Indexed=Scanned`） | **Pass**（修复 4.1 后） |
| 身份唯一性（`adapter_conflict` 占比） | 三仓库 1.06% / 1.24% / 1.68%（builtin/2 时本仓库为 8.3%），残余成因已归因（§4.5） |
| Phase 1 门槛：首次全量 ≤ 120s | **Fail**（146.9s） |
| Phase 1 门槛：DB ≤ 200MB | **Fail**（247.5 MiB） |
| 任务侧基线（token 构成 / 工具调用 / 重复读取 / p95） | **未完成**（见 §6） |

**阈值校准建议（供 `04` §7.6 使用；已含 3 个仓库样本，定稿仍需 CI 复测）**：

- 首次全量：三仓库实测 **0.269–0.386 ms/ref** ⇒ 建议写成 **"≤ 0.6ms × refs，且绝对值 ≤ 300s"**，
  而不是写死 120s 或 60ms/文件——每文件成本随引用密度浮动 ±40%（§2.4），每 ref 成本才是稳定量。
- DB 体积：三仓库实测 **647–701 B/ref** ⇒ 建议 **"≤ 1 KiB × refs，且 ≤ 300MB"**；
  若要坚持 200MB 的绝对值，需要在 Phase 1 做 refs 瘦身（只留解析成功的引用 / 去重），
  本报告不预设结论。
- 两条门槛都应附 `code.status` 的 `(files, refs, bytes)` 三元组，否则无法判断是否达标。

按 `06` §4 的排期铁律②"前一 Phase 验收未通过，不得进入下一 Phase"，Phase 1 **可以继续做**
（索引 MVP 本身是 Phase 1 的交付物），但 Phase 1 的验收门槛必须先用本报告校准——
`04` §7.6 的流程本就是"Phase 0 出基线 → 用中位数 + 95% CI 校准阈值 → 写入 config 默认值"。

---

## 6. 未完成部分（任务侧基线）与执行步骤

`06` §4 交付 3 要求"本仓库 + 1 个外部 Go 仓库，跑 5–10 个代表任务，记录 token 构成、
工具调用数、重复读取次数、p95 延迟"。**索引侧已全部完成**（3 个仓库，见 §2.4）；
**任务侧没有做**，因为它需要真实任务集（`04` §7.5 要求样本来自真实日志，不能构造）。
数据面已就绪（`BaselineReport.Summarize` 直接吃 `[]*entity.TokenUsageHistory`），缺的只是样本：

1. ✅ **前置开关**：`skills_runtime.usage_ledger_enabled` 已开启。两处：
   - `backend/configs/config.yaml` / `config.runtime.snapshot.yaml` 的 env 默认 `:-false`→`:-true`
     （仍可 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED=false` 关）；
   - 运行时 `~/.aicli/.env` 追加 `SKILLS_RUNTIME_USAGE_LEDGER_ENABLED=true`（对
     `~/.aicli/config.yaml:13595` 的 `${...:-false}` 订阅优先，重启 aicli 后生效）。
   bench 证据（`sqlite_store_bench_test.go`）：单写 **3.09ms/op**、并发 0 丢失、444 bytes/记录
   （100k ≈ 42 MiB）；3 ms ≪ 一次 LLM 轮次，故默认开启不增可感知延迟。
2. **任务集**：从 `usageledger` 采样 5–10 个真实任务（`04` §7.5 要求任务集来自真实日志，
   不能只用构造任务）；口径：同一仓库、任务内至少 1 次工具调用、token 记录非空。
3. **A/B**：同一任务集分别以 `knowledge.mode=off` 与 `shadow` 各跑一遍。预期结论是
   "两者对 token 构成无差异"——这正是 Phase 1 的护栏（shadow 不改变模型可见行为）；
   若出现差异，说明 shadow 已经漏进了模型可见路径，属缺陷。
4. **取数**：`BaselineReport.Summarize(records)` 产出 token 构成、`tool_calls_per_task`、
   `repeated_read_per_task`、`exploration_token_share`，以及 `Percentiles` 的 p50/p95。
5. ✅ **外部 Go 仓库**：已完成（gin 99 文件、prometheus 1010 文件，见 §2.4）。注意 `git clone`
   在本机网络下只有 ~13 KB/s，改用 codeload tarball + `tar -xzf`；测量命令与 §1 完全相同。
6. **回填**：把结果按 `04` §7.7 模板补进本文件，并据此校准 `04` §7.4 与
   `backend/internal/knowledge/config.go` 的默认阈值。

> 相邻计划与 schema 事实源的交叉核对（`06` §4 Phase 0 交付 5）单独成文：
> [`phase0_cross_review.md`](phase0_cross_review.md)。
