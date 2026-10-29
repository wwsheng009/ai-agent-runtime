# envd `view` 的行窗口方案（远端按行造窗 + 单一事实入口）

日期：2026-10-28　作者：Qoder（seam 工作线）
关联：`docs/plan/tool-layer-io-seam-design-20261028.md`（§4.2 `RandomAccessor`、§4.3 降级矩阵、§6 Phase 2.5b 第 4 项）、`docs/architecture/envd-capabilities.md` §3.5、`docs/architecture/envd-client-offset-extension.md`（E0，读侧已落地）

---

## 0. 结论先行

1. **`GET /files` + `Range` 不能单独解决深行偏移。** `view offset=N` 是**行**偏移，`Range` 只回答**字节**。行号要么由前缀扫描得出（今天 8 MiB 天花板 + 倍增取前缀的代价来源），要么由**目标机按行造窗**得出。seam 方案 §6 Phase 2.5b 第 4 项把"`RandomAccessor` 走单区间 `Range`"当作行窗口的答案，这一句**前提不足**，照它施工会造出假元数据（见 §1.3）。
2. **方案主体**：新增一次"目标机按行造窗"的取数路径（`awk` 单进程：stdout 出行窗口、stderr 出总行数），并给共享核一个**唯一的新事实入口** `viewcore.SourceFacts{TotalLines, TotalLinesKnown}`。核仍**只有一套窗口实现**，`eof`/`total_lines`/越尾提示全部由"窗口内容 + 一个总行数"推导，不允许适配层直接宣布 `eof`。
3. **能力必须由探测得出**：探测用纯 stdin 的一次 spawn（不写盘、无副作用），逐字节验证"NUL 存活"与"末行未终止计数"两条；测不过 → 回到今天的路径。**这条改动只会增加能力，不会削减能力**（回滚性质，见 §7）。
4. **本轮只交方案，不交代码**：M0 是一次带成本测量的 spike，先把"到底省多少"钉死再动核（这条约束是上一轮我自己写的："需先拿到成本数据再动"）。

---

## 1. 问题

### 1.1 今天的实现（事实，非评价）

`backend/internal/remotetools/envd_backend.go:1061 readViewWindow` 的循环：

```go
content, stats, readErr := viewcore.ReadTextFromBytes(data, request, viewcore.Options{ByteBudget: budget})
if complete || stats.HasMore { return content, stats, ... }   // 服务
if want >= envdViewFetchCeiling || want >= size { return refusal(CodeOutputLimit, ...) }  // 诚实拒绝
want *= 2
```

- 不变式：**只有在能确定前缀已覆盖请求窗口时才服务**（`complete`，或核自己因预算/行上限停下并报 `HasMore`）。
- 天花板 `envdViewFetchCeiling = 8<<20`，刻意等于 `viewcore.MaxDecodeBytes`（远端不得比本地更愿意在内存里堆字节）。
- 拒绝文案已经在教用户自己上手：`"...or read it with shell (for example sed -n '<N>p')"`。
- BOM/UTF-16 只整份服务（前缀切在码点中间会改变文本）。

### 1.2 代价

行偏移越深，传输量越大，且**传输量与前缀成正比而非与窗口成正比**。设预算 `budget`、文件 `size`、第 `offset` 行的起始字节约为 `offset × size / total_lines`，倍增序列的传输总量是该值的 ~2 倍、往返是 `log2` 次。§5 给出实测口径与 M0 要填的表。

### 1.3 为什么"直接接 `ReadRange`"是错的（这是本方案存在的原因）

核的 `Stats.EOF` 语义在代码里写死了："`eof` 表示我看见了你给我的东西的结尾"（`envd_backend.go:1045-1049` 的注释、`WindowMetadata` 的 `"eof": stats.EOF`、`DeliveredWindow`）。若适配层用 `ReadRange` 只取窗口附近字节交给 `ReadTextFromBytes`，核会因为它"读到了数据末尾"而报 `eof = true`，于是发布**一个它从未见过的结尾**。这正是 §8.9 那一轮确立的铁律：

> **给错的元数据比拒绝更坏。**

所以任何"预造窗"都必须先把**事实**补齐，而不是让适配层替核宣布结论。本方案的关键就是：需要补的事实只有**一个**（源文件总行数），其余全部可推导。

---

## 2. 实测事实（2026-10-28，目标机 172.18.3.238，envd 0.2.11，用户 n9e）

工具链：`/usr/bin/awk → GNU Awk 5.2.1`、`sed (GNU sed) 4.9`。**这些数字来自真机，不是文档推断**；它们决定了谁是合法真值源。

| # | 现场 | 测得 | 对方案的意义 |
|---|---|---|---|
| F1 | `printf 'x\ny'`（末行无换行） | `awk END{print NR}` = **2**；`wc -l` = **1**；`sed -n '1,5p'` 输出 `x\ny` | 核的规则与 **awk 的 `NR` 一致**，与 `wc -l` **不一致**。总行数**不得**取 `wc -l`，否则末行无换写的文件会少算一行，进而把 `eof`/越尾提示算错 |
| F2 | `printf 'a\000b\nsecond\n'` | `awk NR==1{length}` = **3**；`sed -n '1,1p'` 与 `awk NR<=1` 都回 **4 字节**（`a \0 b \n`），文件 11 字节 | NUL 在两条造窗通道里**字节级存活**，与 E0 的 `/files` 结论一致；二进制/含 NUL 的窗口不需要因此被拒 |
| F3 | `awk -v a=2 -v b=3 'NR>=a&&NR<=b{print} END{print "TOTAL=" NR > "/dev/stderr"}'` | stdout = `two\nthree\n`，stderr = `TOTAL=3` | **一次 spawn 同时拿到窗口与总行数**，两通道分离，不需要第二次往返 |
| F4 | 1 MB 单行文件 | `awk NR==1{length}` = 1000000；`sed -n '2p'` = 5 字节 | 超长行不是造窗通道的边界；长行夹取仍归核（`ReaderLineCapBytes`），不在远端做 |
| F5 | 核的行规则（代码事实） | `viewcore/view.go:63`：**"A final unterminated line is returned with EOF=true; a subsequent call returns HasBytes=false"** | 与 F1 互相印证：核计数末行 → awk 计数末行 → 两者可对齐；`wc -l` 不可 |
| F6 | 核的事实出口（代码事实） | `WindowMetadata`：`total_lines` 仅在 `stats.TotalLinesKnown` 时发布；`DeliveredWindow.FullRead = req.Offset==0 && !HasMore && !ByteBudgetApplied && LongLinesTruncated==0` | **省略 `total_lines` 字段是合法的"不知道"**，不是撒谎；深偏移窗口天然 `FullRead=false`，不会污染读账本/去重缓存 |

### 2.1 一处 argv 陷阱（写下来，因为它是静默错误）

awk 把形如 `name=value` 的位置参数当作**赋值**而非文件名。若目标路径含 `=`（如 `x=1.txt`），`awk 'prog' x=1.txt` 会去读 **stdin**，得到一个空窗口——错内容且不报错。

本设计的调用路径满足两个条件，因此陷阱不触发，但**必须有测试钉住**：

1. jail 解析后的 `target` 恒为**绝对路径**（首字符 `/` 使 `name` 不是合法标识符 → awk 视作文件名）；
2. M0 用例集里必须包含一个名字含 `=` 的文件（含 `a=b/c.txt` 与 `=x.txt` 两种形态）。

若某次实现想用相对路径喂 awk，则必须先把它变成 `./` 前缀或走 `cat` 通道。

---

## 3. 设计

### 3.1 唯一的新概念：`SourceFacts`（一个事实，不是一个开关）

```go
// backend/internal/toolcore/viewcore/read.go
type SourceFacts struct {
    // TotalLines is the line count of the WHOLE source under the same rule the
    // core uses to count lines: a final line without a newline still counts.
    TotalLines      int
    TotalLinesKnown bool
}

type Options struct {
    ByteBudget int
    Truncated  func(reason TruncReason)
    Access     toolcore.Access
    // Facts describes the source when the caller could not hand over the whole
    // file. It is the ONLY extra input the window rule needs, and it is the only
    // place where an off-machine source may state a fact about itself.
    Facts *SourceFacts
}
```

核内的改动只有一处判断（其余一律不动）：

| 发布字段 | 无 `Facts`（今天） | 有 `Facts` |
|---|---|---|
| `eof` | "我把数据读完了" | **`窗口最后一行的行号 >= TotalLines`**（推导，不是被告知） |
| `total_lines` | 扫描计数 | 来自 `Facts`；未知则**不发布该字段**（F6） |
| `is_truncated` | `HasMore`（核自己因预算/行上限停下的判断） | **完全不变**——造窗不会让核以为"这就是全部请求行" |
| `offset`/`lines_read`/`tail`/`empty`/`byte_budget_applied`/长行夹取 | 不变 | 不变 |
| 越尾提示（`offset >= total` 那三行文案） | 用扫描出的总数 | 用 `Facts.TotalLines`，**文案一字不改**（否则又是一次漂移） |

**自洽校验（这是防假元数据的第二道闸）**：`Facts.TotalLines` 必须与窗口自身相容——`req.Offset + linesReturned - 1 <= TotalLines`，且当 `linesReturned == 0` 时 `req.Offset < TotalLines` 不成立即视为矛盾。矛盾时**丢弃远端窗口，回落倍增前缀路径**，绝不"信一个来源、又信另一个来源"。

### 3.2 适配层的取数策略（`readViewWindow` 内，一处分支）

```
需要行窗口
├─ BOM/UTF-16                          → 整份取数（今天的规则，不动）
├─ 负 offset（取尾）                    → 今天的倍增前缀路径（本方案明确不做远端 tail，见 §9）
├─ !capability.LineWindow              → 今天的倍增前缀路径（今天的行为）
└─ capability.LineWindow && size > 阈值 → 一次 awk 造窗：stdout 窗口 + stderr 总数 → 交给核（带 Facts）
```

阈值与选择规则**必须由 M0 的实测成本决定**，不接受"看起来更聪明"的估计式（例如用平均行长猜起点）。理由：猜错的起点会让"要不要走窗口"变成一次带内容影响的决定，而现在的选择规则只依赖 `size` 与能力位这两个**与内容无关**的量。

### 3.3 一次造窗的确切形态

```sh
awk -v a=<start+1> -v b=<start+limit> \
    'NR>=a && NR<=b {print} END {print "LINECOUNT=" NR > "/dev/stderr"}' <absolute-path>
```

- `a`/`b` 是 **1-based 行号**，由核的行语义（0-based `offset`）换算，且必须是**十进制非负整数**，构造前校验（数字由我们自己生成，不存在注入面）。
- stdout 原样交给 `viewcore.ReadTextFromBytes(data, request, Options{ByteBudget, Facts})`——**窗口渲染仍只有这一套实现**。
- stderr 的 `LINECOUNT=` 解析失败/缺失 → 视为 `TotalLinesKnown=false`（不发布 `total_lines`，`eof` 退化为"不敢说"，即走自洽校验/回落）。
- 退出码非 0、或 stdout 带 awk 诊断 → 视为造窗失败，回落前缀路径（不重试到"半截窗口"）。

### 3.4 能力探测（`envdclient.ProbeCapabilities` 增加一项）

沿用已确立的纪律：**测得才算支持**，且探测**不得有副作用**。

- 探测体：纯 stdin 管道，不碰目标盘 —— 用 `printf` 造一段含 NUL、末行无换行的样本，经**同一条 awk 程序**（窗口 + `LINECOUNT=`），比对返回字节与计数是否等于已知答案（等价于 F1+F2+F3 的一次现场复演）。
- 因此 `LineWindow=true` 的含义是："**这台机器上的 awk 在这个程序的这条命令形态下，行计数与字节传递都与核一致**"，而不是"awk 存在"。
- 探测失败（mawk/busybox awk 的 NUL 差异、没有 awk、argv 行为不同）→ `LineWindow=false` → 行为与今天**逐字节相同**。
- `samplePath == ""` 一类"没测"仍报 false（E0 已确立）。

---

## 4. 备选与否决

| 方案 | 内容 | 否决/采纳理由 |
|---|---|---|
| **A（采纳）** | 远端 awk 造窗 + 核接受 `SourceFacts` | 传输量与窗口成正比；核仍只有一套窗口器；`eof` 是**推导**而非被告知；能力可实测 |
| B | 适配层直接用 `Client.ReadRange` 取字节窗口，自行拼 `Stats` | **否决**：这等于在核外再写一套行语义（§4.4 明令禁止的"第二套窗口器"），且必须替核宣布 `eof` → 假元数据（§1.3） |
| C | 用 `Stat` 的均行长估算起点，再 `Range` 定位 | **否决为选择规则**：估算影响"取哪段字节"，猜错会静默给错行；且仍需扫描前缀才能得到真实行号。估算只能在 M0 实测后作为"是否值得"的输入，不能进入正确性链路 |
| D | 保持拒绝，把 `sed -n` 提示写得更完整 | **部分采纳**：这是 `LineWindow=false` 时的现状，本方案不削弱它。作为独立交付它零风险但零收益 |
| E | 让目标机 `tail -n N` 服务负 offset | **推迟**：取尾还需总行号才能给出 `WindowStart`（`tail` 不给），要么两次 spawn 要么改 awk 环形缓存；收益不确定，先用现路（§9） |
| F | 在 envd 侧建行索引缓存（首次扫描记 offset→line） | **否决（本阶段）**：引入跨调用状态与失效问题，而 `Client` 的既有约定是无状态+并发安全（`client.go:91-93`）。若将来做，它属 `mountctl` 的句柄层，不属这里 |

---

## 5. 成本模型（口径先定，数字由 M0 填）

三个量分开报，避免"往返少了但字节多了"或反之：

- `roundtrips`：一次 `view` 调用向目标发起的取数次数；
- `bytes_moved`：从目标传回本机的字节总数（不含进程控制面）；
- `remote_work`：目标机为造窗额外读盘的字节数（这是**我们有意转移的成本**，必须披露而不是藏起来）。

今天（倍增前缀）在 `budget=4096`、每行 41 字节、`offset=60000`（起始 ~2.46 MB）的解析式估算：倍增序列 4 KiB→…→4 MiB→全量，`bytes_moved ≈ 13 MB`、`roundtrips ≈ 12`。方案 A：`bytes_moved ≈ 窗口大小`、`roundtrips = 1`（`/files` 不可用时的进程通道另计）。

> 上面 13 MB 是**按倍增规则算出来的**，M0 要用插桩把 `(roundtrips, bytes_moved)` 在 3 档文件大小 × 3 档深度上实测替换掉，并给出 `remote_work`。seam 方案 §7.4 的往返/字节检查可扩成第 4 检查。

**必须一起测的反例**：小文件（`size` 小于窗口预算几倍）时造窗多一次 spawn，可能比今天更慢——阈值若不含这条，就是"为了深偏移把普通读拖慢"。

---

## 6. 正确性风险与对策

| 风险 | 具体表现 | 对策 |
|---|---|---|
| 行计数规则不一致 | `wc -l` 少算末行（F1） | 真值源只允许 awk `NR`；`wc -l` 在代码评审清单里点名为禁用 |
| awk 方言差异 | mawk/busybox 的 NUL、`/dev/stderr`、`NR` 行为 | 探测实测（§3.4），测不过即 `LineWindow=false`，行为回到今天 |
| 编码文件 | UTF-16/BOM 切窗会改变文本 | 明确排除：带 BOM 一律走整份路径（今天的规则，不动） |
| 路径含 `=` | awk 把文件名当赋值 → 空窗口且无错 | 绝对路径前提 + 用例集钉死（§2.1） |
| 窗口与总数矛盾 | 远端返回比 `LINECOUNT` 更大的行号 | 自洽校验失败 → 丢弃窗口、回落前缀，不"信两次" |
| 竞态（读中途文件被改） | 窗口来自旧内容、总数来自新内容 | 一次 spawn 内两者同源；跨 spawn 才可错。文档化：不承诺强一致，但**不承诺的一律不说成承诺**（`eof` 只在自洽时发布） |
| stderr 污染 | awk 诊断混进 `LINECOUNT=` | 解析失败即视作未知（不发布 `total_lines`），诊断原文进 `note` |
| 输出上限 | 大窗口 × 长行撑爆 per-call 上限 | 预算仍在核里（`ByteBudget`/`ToolBudget`），传输侧 `--max-bytes-per-call` 语义不变；窗口请求行数上限沿用核的 `MaxLimit` |

---

## 7. 分期与回滚

| 阶段 | 交付 | 回滚点 | 通过条件 |
|---|---|---|---|
| **M0 spike（不改生产代码）** | 插桩测出 `(roundtrips, bytes_moved, remote_work)`；awk 造窗与核窗口在 7 类样本上**逐字节对拍**；含 `=` 文件名、NUL、末行无换行、超长行、1 MB/4.9 MB/12.3 MB 三档 | 全部是脚本 | 对拍不一致的样本若不能排除到"整份路径"，方案 A 作废，回到 D |
| **M1 核侧** | `SourceFacts` + 一处判断 + 自洽校验；`viewcore` 单测（无 Facts 路径**一字不改**通过，即"搬家未改语义"的同款证据） | 删掉 `Facts` 字段即回原状 | `driftguard` 三项（os.* / walker / capability 调用点）不放宽；工具面 6/13/19 与档位表零变化 |
| **M2 适配层** | `ProbeCapabilities` 增 `LineWindow`；`readViewWindow` 增加造窗分支与阈值；`--no-envd-line-window` kill switch（与既有 `--no-envd-files` 同形） | 关掉开关 = M1 前行为 | 真机：readonly 6/6、standard 13/13、ops 19/19；深偏移用例新增 |
| **M3 收敛（可选）** | 把行窗口能力并入 2.5b 的 `envdAccess`（`RandomAccessor.RandomRead` 服务**字节**需求；行窗口走 `ExecStreamer` 语义），使"字节读"和"行窗口"各归其位 | 独立提交，可单撤 | seam 方案 §13 更新落地记录 |

**回滚性质（重要）**：本改动只在 `LineWindow=true` 且 `size > 阈值` 时改变取数方式；探测不过、开关关闭、自洽校验失败三种情况都落回今天的代码路径。因此它**不可能让一个今天能答的请求变成不能答**——这是 §7 全部风险表的兜底。

---

## 8. 验收门（与既有纪律逐条对齐）

1. **反向验证**：把核里 `Facts` 的 `eof` 推导改成"照旧认为读完了" → 深偏移用例必须红；把 awk 的 `END` 计数改成 `wc -l` 语义 → 末行无换行用例必须红。
2. **差分对拍**：期望值由"整份读取 + 核窗口"现算（与 `ls_live_verify.go` 同构），不得手写常量。
3. **真机**：172.18.3.238 / envd 0.2.11，交叉编译后**在目标机原地**跑；`view` 深偏移、越尾、NUL、超长行、含 `=` 文件名逐类给出 PASS；闸门与残留检查同上一轮。
4. **不变量守卫**：`TestSharedCoresNeverBranchOnBackendNames` 保持 0（核内不出现 `"envd"` 字面量）；`TestCapabilityDecisionsStayCentralized` 上限仍 0——`Facts` 是数据不是能力查询，若某步需要 `Capabilities()`，必须命名那个 resolver 再抬上限。
5. **不新造错误码/档位词汇**：拒绝继续用 `CodeOutputLimit`，不支持继续用 `UNSUPPORTED`；`envdExtensionTiers` 唯一。
6. **gofmt 只作用本轮具体文件**（目录级误伤已发生两次，见事实台账）。

---

## 9. 明确不做

- 不做负 offset（取尾）的远端窗口（理由见 §4-E）。
- 不做多区间 `Range`（客户端已定单区间，多区间会返回 `multipart/byteranges`）。
- 不在 `envdclient` 里放行句柄/写策略（属 `mountctl`，`envd-client-offset-extension.md` 的边界）。
- 不改 `view` 的 schema、文案、工具面与档位表；不改 local 的任何行为。
- 不动他人文件（`docs/user-guide/aicli-remote-tools.md`、`internal/gateway/**`、`remotetools/{gateway,config,audit,metrics,platform,roots}.go` 等）。

---

## 10. 需要拍板的两点

1. **阈值口径**：`size > 4 × budget` 这类"与内容无关"的简单门（可测、可解释），还是允许引入平均行长估算（更快但需要更多用例锁死）？我倾向前者——把估算留在 M0 报告里作为数据，不放进正确性链路。
2. **总行数是否值得一次 stderr 旁路**：只靠"窗口 + 一行前瞻"也能定 `eof`（不必知总数），但会丢掉 local 那句"offset 等于/超过总行数"的提示文案（F6 允许不发布 `total_lines`，但那三行提示会变）。带上 `LINECOUNT=` 保文案一致，代价是同一次 spawn 读完整个文件（`remote_work`）。我倾向带上——省往返不该以"核少一句话"为代价。
