# grep/rg 输出守卫（读取侧字节闸门 + 单行截断）

> 状态：已实现（backend/internal/toolkit/tools/grep.go 的 `runGrepCommand` /
> `captureLimitedOutput` / `clampGrepContentLine`，测试见 grep_output_guard_test.go）

## 背景

2026-10-12 事故：一次 `grep pattern=gateway path=C:/Users/vince/.aicli glob=*.json`
让 ripgrep 产出了约 **1.97 GB** stdout。旧实现的问题链：

1. `runGrepCommand` 用 `bytes.Buffer` **全量接收** stdout（无字节上限）；
2. `normalizeRipgrepOutputWithStats` 对整段输出做 `ReplaceAll → Split → 逐行 regex → fmt.Sprintf`，
   峰值同时驻留多份近似全量副本；
3. 收集上限（`grepCollectHardCap=5000` 行）与模型可见预算（`grepOutputBudgetBytes=32 KiB`）
   **都在归一化之后**才生效，闸门来得太晚。

结果：进程私有内存涨到 ~5.7 GB、单次调用耗时 286 s。同类风险对任何
「宽路径 + 大文件/大 glob」的搜索都存在，与具体目录无关（不做任何目录黑名单）。

## 机制

1. **读取侧硬上限（默认 16 MiB）**：`runGrepCommand` 改为 `StdoutPipe` +
   `captureLimitedOutputByLines(stdout, budget, lineBudget)`，只多读 1 字节即可判定
   溢出；内存占用与上限成正比，**绝不随输出量增长**。闸门发生在任何归一化/收集之前。
2. **超限即停**：命中任一闸门立即 `Kill` rg 并丢弃管道残余，再 `Wait` 回收。既避免
   「停止读取导致子进程写管道阻塞」的死锁，也避免让失控搜索继续跑完（事故里 286 s 的主因）。
   字节上限返回哨兵 `errGrepOutputLimit`，行预算返回 `errGrepLineBudget`。
3. **流式逐行收集、收满页即停（长尾止损）**：`searchWithRipgrep` 按「本次还需要收集的
   行数 + `grepStreamLineProbe`(=8)」通过 ctx 下发行预算（`withGrepStreamLineBudget`），
   捕获层数换行符、读满即终止 rg——不再为已经够用的结果扫描整棵树。
   - 行预算经 **context** 传递而非改 `runCommand` 签名：runner 是测试注入点（30+ 处桩
     实现），签名变更会波及大量无关测试；未设置预算时行为与旧版逐字一致。
   - 该停止是**正常分页语义**（`errGrepLineBudget` → `truncated=true`），不是 partial、
     不产生 next_action；rg 在页满之前自然结束则仍按原路径判定完整性。
   - `--stats` / `--json` 需要尾部汇总行，**不设行预算**（保持读满，仍受字节上限保护）；
     多 scope 在收满页后不再启动后续 rg，并按截断收尾。
4. **部分证据语义**：`searchWithRipgrep` 对 `errGrepOutputLimit` 走既有的 partial 分支——
   保留已收集匹配、`partial=true`、附 `next_action`（收窄 pattern/paths/glob 或减小
   `max_count`），并在正文给出「单次 rg 原始输出超过上限 N MiB（已提前停止读取）」。
   不把截断伪装成超时，也不丢弃已到手的证据。
5. **单行截断（默认 4 KiB，归一化前）**：`clampGrepContentLine` 在逐行 regex 之前截断
   超长行（保持 UTF-8 边界，附 `…[+N bytes truncated]` 标记）。`path:line:col:` 前缀保留在
   行首，后续匹配行 regex 仍可解析。避免数百 KB 的 minified JSON/日志行放大 regex 与
    `Sprintf` 开销、挤掉其他匹配。JSON 输出模式（`--json`）不做此改写以保持 JSON 语义。
6. **stderr 头部限额（32 KiB）**：`headLimitedBuffer` 只保留前 32 KiB 诊断并在截断时标注；
   `Write` 始终返回 `len(p)`，子进程不会因管道写满而死锁。
7. **附带修复**：`hasOutput` 判断从 `strings.TrimSpace(string(output))`（整段复制）改为
   `bytes.TrimSpace(output)`，省掉一份可达上限大小的内存副本。

## 元数据口径（结果不完整时）

提前停止会让 `match_count` 不再是查询总数，因此结果统一加了口径标注（键定义在
`internal/toolresult`，供工具与事件摘要共用）：

| 场景 | 元数据 |
| --- | --- |
| 完整结果 | 无 `match_count_scope` / `truncation_reason`，`match_count` 就是本次匹配数 |
| 收满页提前停止（`errGrepLineBudget`） | `truncated=true`、`has_more=true`、`next_offset=N`、`truncation_reason="page_stop"`、`match_count_scope="observed_before_stop"` |
| 字节闸门命中（`errGrepOutputLimit`） | `partial=true`、`match_count_scope="observed_before_stop"`，`truncation_reason` 保持交付侧原因 `"byte_budget"`（若同时发生） |

`match_count_scope="observed_before_stop"` 表示该数值只是「停止前观察到的下界」；
运行事件摘要（`internal/agent/tool_runtime_events.go`）据此渲染为 `"108+ matches, truncated"`，
避免把观测值当成查询总数复述。

## 与既有闸门的关系

| 层 | 位置 | 阈值 | 作用 |
| --- | --- | --- | --- |
| 读取上限（新） | `runGrepCommand` | 16 MiB（env 可调） | 内存/时长的第一道闸，超限即停 |
| 行预算（新） | `withGrepStreamLineBudget` + 捕获层 | 页大小 + 8 行 | 收满页即停，压缩长尾耗时 |
| 单行截断（新） | `normalizeRipgrepContentLines` | 4 KiB（env 可调） | 防止单行病态放大 |
| 收集上限 | `grepCollectLimit` | `maxMatches=100` / `head_limit`，硬顶 5000 行 | 分页与续读 |
| 模型可见预算 | `buildGrepResult` | `grepOutputBudgetBytes=32 KiB` + 归档 | 交付窗口与 `artifact_read` 续读 |

默认值是四层里最大的一道（16 MiB ≈ 5000 行 × 3 KiB），既保证一次分页抓取能填满，
又把峰值内存钉在几十 MB 以内（事故现场低两个数量级）。

## 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `AICLI_GREP_MAX_OUTPUT_BYTES` | `16777216`（16 MiB） | 单次 rg stdout 字节上限；`0`/`off`/`unlimited` 关闭（不推荐），非法值回退默认 |
| `AICLI_GREP_MAX_LINE_BYTES` | `4096`（4 KiB） | 归一化前单行字节上限；同关闭语义 |

配置解析与 `AICLI_SHELL_*` 保持同一风格：未设置/非法值回退默认，不会因手滑值静默收紧；
两者都是运行时读取（`os.Getenv`），无需重启即可通过设置进程环境覆盖。

## 适用范围说明

`runGrepCommand` 同时是 `GlobTool`（`--files` 类扫描）的默认 runner，读取上限同样生效；
其输出形态天然很小，16 MiB 不会误伤。内置 walker 引擎（rg 不可用时的回退）使用
`bufio.Scanner`（64 KiB 单行上限），不在本次改动范围内。
