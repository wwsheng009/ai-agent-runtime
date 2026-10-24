# grep 参数收敛审计（2026-10-24）

## 方法

枚举本地 `grep` schema 声明的**全部**参数（运行时从 `Parameters()` 读取，不靠人工清点），
与 `grep_core_switch.go` 的 `coreSearchParams` 逐项比对，得到「已进 core」与「仍在 legacy」
两个集合，再对后者逐条判定性质。

结果：**96 个声明参数，52 个已进 core，44 个仍在 legacy**。

## 44 项的归类

### A. 语义缺口 —— 已在本轮修复（3 项）

这三项不是「展示风格」，它们改变结果集，remote 完全没有，属于必须收敛的：

| 参数 | 语义 | 处理 |
|---|---|---|
| `count_matches` | count 模式统计**匹配次数**而非匹配行数 | 折进 `OnlyMatching`（同一套算术），并让该 flag 本身选择 count 模式 |
| `glob_case_insensitive` | `glob`/`include`/`exclude` 按大小写不敏感匹配 | 过滤层携带 case 模式；rg 侧转发 `--glob-case-insensitive` |
| `ignore_file_case_insensitive` | 发现的 ignore 文件按大小写不敏感匹配 | 规则级 `caseInsensitive` 标志；rg 侧转发 |

### B. rg-only 透传（能力依赖二进制）—— 保持本地

`pcre2`、`multiline`、`multiline_dotall`、`replace`、`passthru`、`crlf`、
`auto_hybrid_regex`、`engine`、`type_add`、`type_clear`、`follow`。

这些参数的共同点：**离开 rg 就没有实现**，本地路径的处理方式是「没有 rg 就明确报错」。
remote 侧不声明它们是诚实的——那里根本没有这个二进制。

### C. 输出格式透传 —— 保持本地

`json`、`json_output`。

`--json` 的语义是「原样透传 rg 的 JSON Lines 事件流」，本质是把 rg 的输出当作 API。
它和本项目「规范化输出」的契约是互斥的两种格式，不是同一工具的两个参数。

### D. 纯展示开关 —— 保持「接受但不生效」

`heading`、`no_heading`、`color`、`pretty`、`line_number`、`no_line_number`、
`with_filename`、`no_filename`、`field_context_separator`、`path_separator`、
`context_separator`、`no_context_separator`、`null`、`null_data`、
`line_buffered`、`no_line_buffered`、`block_buffered`、`no_block_buffered`。

它们对模型没有意义：颜色码污染 payload、标题分组破坏稳定骨架、缓冲策略是进程行为。
本地一直是「接受这个写法，但不改变输出骨架」，**目的是让熟悉 rg 的调用者不必改写**。
remote 不声明它们。

### E. 本地遍历/预算机制 —— 保持本地

`text`、`binary`、`no_config`、`head_limit`、`sort`、`sortr`、`sort_reverse`、
`sort_files`、`no_sort_files`。

`text`/`binary` 是「如何判定二进制」的旧入口，已由 `unrestricted_level` 统一表达。
`head_limit`/`sort*` 属于分页与顺序策略，core 已有 `limit`/`offset` 与固定 path 排序。

### F. 逃生舱 —— 设计上只属于本地

`rg_args`。

它的存在意义就是「结构化参数覆盖不到时，直接给 rg 传参」。remote 若声明它，
等于把宿主机的命令行暴露给远端调用者。

## 本轮顺带发现并修复的真实缺陷

1. **`no_ignore_files` 没有抑制显式 ignore_file 的转发**
   core 无条件把 `--ignore-file` 传给 rg，而 `--no-ignore-files` 的语义是丢弃这份清单。
   结果：调用方说「不要用这些规则」，规则却生效了。

2. **`ignore_files` 键在两个面上类型不同**
   remote/core 是 `bool`（「ignore 处理是否开启」），本地是 `[]string`（路径清单）。
   同名字段两种类型，任何消费者按一种形状断言都会在另一条路径上失败。
   本轮不改这个既存分歧（改动会波及既有读取方），而是在本地 surface adapter 里
   把 `ignore_files_list` 映射回本地形状，并在此文档登记该分歧。

3. **`ignore_file_case_insensitive` 的作用域被理解反了**
   本地原实现与测试都认为它作用于**显式** ignore_file。
   实测 rg 14：`--help` 写的是 "Process ignore files (.gitignore, .ignore, etc.)
   case insensitively"，且显式 `--ignore-file` 保持大小写敏感。已按 rg 修正，
   并改掉那条断言相反的测试。

   实测还发现一个容易误判的细节：**在非 git 目录里 `.gitignore` 根本不被读取**，
   所以该 flag 看起来「没效果」；本项目 core 默认带 `--no-require-git`，
   两引擎因此都能一致地让该 flag 生效。

## 剩余判断

44 项中，**真正需要继续收敛的语义缺口为 0**。
剩余三类（rg-only 透传、json 透传、展示开关与本地机制）都有明确的「为什么它可以留在本地」，
且都不是「忘了做」。

下一步若要继续，应当转向 remote 侧的 schema 一致性检查
（确保 remote 声明的每个参数都有真实实现），而不是继续搬运本地参数。

---

# 反向审计：remote 声明的参数是否都有实现

## 方法

两路取证：

1. 通过 MCP `tools/list` 取 remote grep 的实际 schema（客户端视角，不是源码视角）；
2. 用 `go/ast` 静态扫描 `searchcore` 包，收集所有传给 `stringArg`/`boolArg`/
   `intArg`/`intArgOK`/`stringArgAny`/`GrepGlobListFromArgs`/`GrepPathListFromArgs`/
   `GrepPatternsFromArgs` 的字符串字面量，即「实现真正读取的键」。

两者求差，得到「声明但没实现」和「实现了但没声明」。

## 结果

初始：remote 声明 58 个，静态扫描到 55 个读取点。

**声明但未读取 4 个**，逐一核实后：

| 参数 | 结论 |
|---|---|
| `mode` | 经 `grepModeFromArgs` 直接索引 `args["mode"]`，静态扫描看不到 |
| `max_filesize` | 经 `grepMaxFileSizeFromArgs` 直接索引，同样看不到 |
| `patterns` / `regexp` | 经 `GrepPatternsFromArgs` 读取，该函数接收整个 map |

三者都是**扫描器盲区，不是缺失实现**，已登记在测试的 `indirectArgKeys` 中并注明原因。

**实现了但未声明 1 个**：`files_without_matches`（`files_without_match` 的复数别名，
handler 接受但 remote 未声明）。已补上。

**最终：声明 59 个，未实现 0 个。**

## 固化为回归测试

`internal/remotetools/grep_schema_audit_test.go`：

- `TestRemoteGrepSchemaEveryParamIsRead` —— 声明但没人读 = 失败。
  一个调用方看得见、运行时却静默忽略的参数，是工具契约最糟的形态。
- `TestRemoteGrepSchemaMatchesLocalCoreSet` —— handler 读的参数必须对客户端可见。
- `TestRemoteGrepSchemaAliasesAreDeclared` —— handler 接受的别名必须可发现。

**这些测试被验证过确实能抓到问题**：临时注入一个不存在的参数名，
审计立即报告「flagged 1 unread param」。测试不是摆设。
