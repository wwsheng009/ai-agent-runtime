# 非数据库错误深度分析与优化方案（2026-09-27）

> 关联：本文件是 2026-09-27 使用分析页错误榜的第二轮分析。
> **范围约定**：按用户要求，所有 SQLite / WAL / 在线数据库文件操作类问题
> **不在本方案优化范围内**（历史根因与离线处理见
> `docs/analysis/sqlite-wal-corruption-and-offline-compaction-20260926.md`）。
> 本文只分析其余错误，并给出分阶段优化方案。
>
> 证据基线：统计窗口截至 **2026-09-27 18:25 CST**；原始九项错误合计 1279 条，
> 其中可直接判定为数据库 / artifact SQLite 存储类 **112 条**（`TOOL_BROKER_FAILURE`
> 94 + `artifact_read` SQLite 18），**非数据库记录 1167 条**。

## 0. 摘要

1. **上游非法工具参数（`upstream_invalid_response` 178）不是网络断流或输出预算不足。**
   在完整落盘的 63 份 HTTP 响应中，91 次工具调用里有 **66 次参数非法**；其中
   **65 次是同一种形态：裸 duration 超时值**（`"timeout": 60s`、`"timeout": 120s`、
   `"timeout": 8m` …，未加 JSON 引号）。这些响应 HTTP 200、SSE 正常 `[DONE]`、
   `finish_reason=tool_calls`、帧解析零错误，且 `completion_tokens` 仅 98～313，
   远低于 32000 / 64000 的输出预算。
2. **失败后的重试策略对这类语法错误无效且放大成本。** 同一逻辑请求（示例
   `llm_req_9b6a15af…`）连续 3 次尝试返回**同一段非法参数（SHA-256 前 16 位
   `990a9cb4b4a7166e`）**，而每次重试都把 `max_tokens` 从 32000 翻倍到 64000——
   但错误与预算无关（代码 `provider_retry.go:274-319` 只按 reason 扩容，未检查
   `MalformedToolCallError.Truncated`）。
3. **`STALE_CONTEXT` 139 与“模型上下文超限”无关**：全部是 `apply_patch`（119）
   和 `edit`（20）的旧内容/`@@` 失配，是编辑协议问题；分析页把它显示成
   “上下文超限”属分类映射缺陷（`failure_category.go:86` 的 `CONTEXT` 子串兜底）。
4. **`TOOL_EXECUTION` 381 里只有 18 条是数据库问题，其余 363 条是“泛码桶”**：
   非法正则 104、未执行的非法 JSON 参数 86、路径不存在 55（grep）、策略拒绝 19、
   子进程管道等待 17、`spawn_subagents` 枚举 12 等。**86 条 shell 类记录并不是
   shell 真的执行失败**，而是模型非法参数被拒后投影成工具事件（`not_executed=true`）。
5. **`TOOL_PATH_NOT_FOUND` 192 中有 12 条是真实缺陷**：`take_screenshot`(10) +
   `take_snapshot`(2) 的输出 `filePath`（新文件）被通用路径预检当成**必须存在的
   输入**而拒绝。其余是路径猜测/工作目录类问题，但预检把 `os.Stat` 的一切错误
   压成“不存在”，也丢失了权限与 I/O 的区分。
6. **`AGENT_READ_ONLY` 54 全部来自 shell，且并非都是误伤**：明确需要写权限的
   `go build/test` 等属预期拦截；但 `Get-ChildItem … | Select-Object -First N`
   等只读形态仍因后段不在白名单（`grants.go:275-291`）被拒。**不应以“让该码归零”
   为目标，更不得放宽硬边界。**
7. **`user_cancelled` 91 无法从现有落盘数据完成归因**：43 条 `duration_ms=0`；
   只有 9 条能按 turn/trace 严格匹配到 `session_end`，其余 82 条所在会话的
   `runtime-events.jsonl` 已按保留策略清理（184 个会话中 146 个缺失）。
   需要把 `cancel_source/cause/reason` 随请求记录一并落库。
8. **`TOOL_TIMEOUT` 56 中 shell 占 47**，主要是 go test/build、递归列目录、git、
   node/python 等长任务；`timeout_source` 元数据缺失 32/56，无法离线区分
   “工具超时 / turn deadline / 父 context”。

一句话：**需要优先修的是「工具参数契约面（超时用整数字段）+ 失败后的重试与
诊断策略 + 分析分类映射 + 路径角色预检」，而不是继续加大 token 预算或重试次数。**

---

## 1. 分析方法、数据来源与边界

### 1.1 数据来源

| 来源 | 用途 | 访问方式 |
|---|---|---|
| `~/.aicli/sessions/runtime/usage_analytics.sqlite` | 错误榜计数、按工具/来源/模型/时长聚合、`record_json` 内的原始错误文本 | `node:sqlite` **readOnly=true**，只做 SELECT；无 checkpoint/vacuum/migration |
| `~/.aicli/chat-logs/<yyyy>/<mm>/<dd>/<session>/events/runtime-events.jsonl` | `llm.request.finished`、`llm.retry`、`session_end` 的原始载荷 | 只读文件流 |
| `~/.aicli/chat-logs/.../http/*_request_provider_wrapper.json` 等 | 出站请求与 SSE 原始响应（含截断标记）、失败参数原文、attempt/预算 | 只读文件流（`body_text` 截断到 256 KiB 首尾） |
| 源码 + 测试 | 机制定位与防御边界 | 只读 `grep`/`view` |

### 1.2 复现材料

- 统计脚本（可重跑、只读）：`.aicli/tmp/nondb-usage-audit-20260927.mjs`
- 脱敏证据快照（含聚合、参数 SHA-256 前 16 位、结构摘要，不含密钥）：`.aicli/tmp/nondb-usage-evidence-20260927.json`
- 两个独立代码审查子代理（只读、plan 模式）：路径/权限/正则方向、上游协议方向。

### 1.3 方法限制（写方案前必须承认）

- 归类到“原因”的映射基于**原始错误文本的正则**，是可复核的启发式，不是二进制精确标签。
- 对照样本（同一 session/provider/model 最近的“成功请求”）是**观察性对照**，不是随机 A/B，
  不能据此断言 provider 或模型的因果责任比例。
- 日志对应的是**当时运行的二进制版本**；部分历史缺陷可能已修复（例如只读分类器已支持
  逐段复合命令与部分 PowerShell 只读命令），需要按当前版本回归验证。
- HTTP 捕获上限 256 KiB（`http_debug.go:70,160-174`），且按 256 文件/64 MiB 清理
  （`chat_http_artifacts.go:16-22,93`）；事件日志按保留策略清理 **146/184** 个会话文件缺失。
  因此部分结论“不可回放”，只能标注为待验证。
---

## 2. 证据总览

### 2.1 非数据库部分构成（窗口内）

| 错误码 / 来源 | 数量 | 其中数据库类 | 非数据库 | 主要归属 |
|---|---:|---:|---:|---|
| `TOOL_EXECUTION`（tools） | 381 | 18（artifact SQLite） | 363 | 正则/参数/路径/策略/子进程 |
| `TOOL_PATH_NOT_FOUND`（tools） | 192 | 0 | 192 | grep 93、view 71、ls 11、截图/快照 12、glob 5 |
| `upstream_invalid_response`（requests） | 178 | 0 | 178 | 非法工具参数（已回放 146 条全部为 `invalid_tool_arguments`） |
| `STALE_CONTEXT`（tools） | 139 | 0 | 139 | apply_patch 119、edit 20 |
| `TOOL_BROKER_FAILURE`（tools） | 123 | 94 | 29 | SQLite 94；状态冲突 14、越权 6、取消 6 等 |
| `user_cancelled`（requests） | 91 | 0 | 91 | context.Canceled，归因受限 |
| `TOOL_INVALID_ARGS`（tools） | 65 | 0 | 65 | 参数缺字段/不支持参数/补丁语法 |
| `TOOL_TIMEOUT`（tools） | 56 | 0 | 56 | shell 47，其余 MCP/网络 |
| `AGENT_READ_ONLY`（tools） | 54 | 0 | 54 | 全部 shell |
| **合计** | **1279** | **112** | **1167** | |

### 2.2 按 provider/model 的请求错误率（观察性）

| provider | model | 总请求 | 该类错误 | 比例 |
|---|---|---:|---:|---:|
| `commandgo` | `deepseek/deepseek-v4.1-flash` | 14,642 | 173 | **1.18%** |
| `opencode.ai` | `deepseek-v4.1-flash` | 20,146 | 5 | 0.025% |
| `hanhe` | `deepseek-v4.1-flash` | 89 | 0 | 0% |

两者相差约 47 倍，指向“网关/模型输出 + 我方 schema 形态”的组合问题；但
**不是同输入对照实验**，不能直接归因。可复核的硬事实是：在同一失败会话里，
请求体发送的 bash 工具 schema 确实包含 `timeout`（string）与 `timeout_sec`（integer），
且包含“必须带引号、裸写会破坏 JSON”的描述（示例：
`session_20260927160529_pIgkVfY5/http/306_request_provider_wrapper.json` 的
捕获尾部），模型仍产出了裸 duration。

### 2.3 HTTP 回放结果（失败请求）

对 28 个失败逻辑请求成功关联到 82 份响应工件（去重后）：

| 指标 | 数值 |
|---|---:|
| HTTP 状态 | 200（82/82） |
| `finish_reason` | `tool_calls`（82/82） |
| 捕获完整（`body_bytes == captured`） | 63 份 / 25 个逻辑请求 |
| 捕获被截断（256 KiB 上限） | 19 份 |
| 完整响应中的工具调用 | 69 |
| 完整响应中的非法参数 | **66** |
| 其中“裸 duration” | **65** |
| 其中“`$env:…` 表达式未加引号” | 1 |
| SSE 帧解析失败 | 0 |
| `[DONE]` 终止 | 63/63 |

示例（同 `llm_request_id`，attempt 1→3，非法参数哈希完全相同）：

| 工件 | attempt | max_tokens | completion_tokens | 参数哈希 |
|---|---:|---:|---:|---|
| `session_20260927123719_qv6Y4it8/http/052_response_provider_wrapper.json` | 1 | 32000 | 98 | `990a9cb4b4a7166e` |
| `…/055_response_provider_wrapper.json` | 2 | 32000→64000 | 98 | `990a9cb4b4a7166e` |
| `…/057_response_provider_wrapper.json` | 3 | 64000 | 259 | `990a9cb4b4a7166e` |

> 结论：这是**可复现的退化样本**（同请求同参数），扩容预算不会改变它；
> 需要改变的是“让模型不必写字符串形式的超时”与“重试决策不要被语法错误触发扩容”。

### 2.4 其他关键聚合

- **非法参数投影**：`TOOL_EXECUTION` 中 86 条 `not_executed=true` 的 shell 记录，
  错误文本均为“tool call arguments were not valid JSON”，与 2.3 是同一批模型退化。
- **STALE_CONTEXT 恢复观察**：139 条中 128 条的后续 10 次调用内出现过 `view/grep`；
  113 条后续 10 次内同工具成功（**不保证同一文件**，只能视为恢复信号上界）。
- **重复失败**：同一 session + 工具 + 错误码 + 完全相同的 `attempted_args`，
  10 分钟内重复出现的“额外失败调用”共 16 条（stale 7、execution 6、path 2、read-only 1）。
- **超时元数据**：56 条中 `timeout_source` 缺失 32 条；已带来源的为
  tool_argument 15、parent_context_deadline 5、tool_default 4；p50=30s、p95=600s。
- **只读拒绝构成**：`not_allowlisted` 25、`compound` 24、`dynamic_syntax` 5；
  命令族以 PowerShell 列目录/查询（23）、其他 shell（11）、git（9）为主。
- **取消构成**：91 条中 `duration_ms=0` 43 条；严格匹配到 `session_end` 仅 9 条
  （3 条带明确 cause），82 条因事件文件被清理无法归因。
---

## 3. 逐项深入分析

> 每项格式：现象 → 原始证据 → 机制与代码定位 → 判断 → 优化方向（详细方案见 §5）。
> 代码路径除特殊说明外均相对 `backend/internal/`。

### 3.1 上游非法工具参数（`upstream_invalid_response` 178 + 86 条投影）

**现象**：178 条请求级错误集中在 `commandgo + deepseek/deepseek-v4.1-flash`（173/14642）。
请求级原文没有 error 文本，需要靠 `llm_request_id` 回查事件与 HTTP 工件。

**原始证据**

- 146/178 条按 `llm_request_id` 找回 `llm.request.finished`，**全部为
  `invalid_tool_arguments`**；146 条日志文本统一指向 shell 的
  “incomplete or non-object JSON arguments”。
- 完整落盘的 63 份响应里，66 个非法参数中 **65 个是裸 duration**
  （`timeout` 值写作 `60s`/`120s`/`8m`…，未加引号），1 个是 `$env:…` 表达式未加引号。
- 所有响应 HTTP 200、帧解析零错误、`finish_reason=tool_calls`、`[DONE]` 正常。
- 同参数哈希在 3 次 attempt 中完全一致（见 §2.3 表）。
- 请求体（wire）里 bash schema 明确写了“值必须是带引号的 JSON 字符串……裸写 30s 会让
  整个 arguments 变成非法 JSON”，说明**文字警告不足以约束该模型的 JSON 生成**。

**机制与代码定位**

1. 参数聚合到校验的主链：`llm/provider.go:1660-1668` → `providercompat/response.go:110-126`
   → `adapter/sse.go:23-97` → `adapter/openai.go:515-526, 848-877, 949-964, 1043-1054`
   → 严格对象校验 `adapter/openai.go:700-716`。
2. 校验失败把“未闭合 / 非法转义 / 裸字面量 / 顶层非对象”合并为同一条文案
   （`adapter/openai.go:705-712`），偏移与顶层类型未保存；
   `adapter/malformed_tool_call.go:89-111` 仅按 `finish_reason` 设置 `Truncated`。
3. 扩容判据只按 reason：`llm/provider_retry.go:274-281` 把 `invalid_tool_arguments`
   列入扩容原因，`provider_retry.go:295-319` 直接翻倍 max_tokens，**不检查是否真的被
   预算截断**；测试甚至固化了“无截断证据也扩容”（`retry_policy_test.go:992-995`）。
4. 反馈回注：`agent/loop.go:1417,1433-1454` 每工具名最多 2 次；`agent/loop.go:1474-1528`
   与 `1587-1601` 把非法调用投影成 `tool.requested/tool.completed`（`not_executed=true`），
   并把坏参数原文回填给模型。
5. 正常防线**不能动**：`adapter/openai.go:535-536` 在执行前拒绝；
   `toolargs/normalize.go:23-41,120-166` 的补括号容错位于执行器之前的另一条入口，
   **不允许**前移到 OpenAI 严格校验之前去“猜命令”。

**判断**

- 直接原因是模型（或网关链路）生成非法 JSON；
- 放大原因是重试/扩容策略把“语法错误”当成“预算截断”；
- 契约面原因是模型可见 schema 提供了字符串型 `timeout`，而该模型高频写坏它。
- 次要但确定的缺口（未在本批样本中出现）：聚合只追加、不做身份冲突检查
  （`adapter/openai.go:957-961,1049-1053`）；`choices[0]` 硬编码（`:854`）；
  modern+legacy 字段可能写同一槽（`:876-877,1012-1023`）；
  逐行兼容层与逐帧解析层不一致（`providercompat/response.go:114-123` 只转单行）。

**优化方向**：P0-1（分类映射）、P0-2（结构化证据）、
P0-3（禁止无证据扩容/同 hash 重发）、P1-1（超时字段模型面整数化）、
P1-2（结构化反馈）、P1-6（聚合身份一致性断言）。

---

### 3.2 `STALE_CONTEXT` 139（编辑上下文失配，不是模型窗口超限）

**现象**：apply_patch 119、edit 20；分析页显示“上下文超限”。

**原始证据**

- 工具 `record_json` 自带 `error_code=STALE_CONTEXT`、`failure_class=stale_context`、
  `current_snippet`、`suggested_view_offset/limit`。
- 失败后 128/139 在 10 次调用内出现 view/grep；113/139 在 10 次内同工具成功（恢复信号）。
- 10 分钟窗口内用**完全相同的 attempted_args** 再次失败的额外调用仅 7 条，
  说明大多数失败被后续重读修正，而非原地重试。

**机制与代码定位**

- 语义定义：`errors/codes.go:36-39`；匹配失败与恢复信息：`toolkit/tools/edit.go:269-295`；
  补丁 hunk 定位失败与 stale/语法区分：`toolkit/tools/apply_patch.go:1412-1445`。
- **分类错误**：`llm/failure_category.go:79-99` 先用子串兜底，`strings.Contains(normalized,"CONTEXT")`
  命中 `STALE_CONTEXT` → `context_overflow`。模型窗口超限走的是另一条链
  （`llm/provider_retry.go:102-123` → `retry_policy.go:1019-1020` 的
  `CONTEXT_BUDGET_EXCEEDED`），两者语义完全不同。

**判断**：这是**编辑协议问题**（模型复用旧片段、长上下文 hunk 漂移、并发/连续编辑），
不是需要压缩对话或扩大上下文窗口的问题。分类映射需要修正。

**优化方向**：P0-3（映射修正）；P2-1（恢复体验与失败模板）。

---

### 3.3 `TOOL_EXECUTION` 363（非数据库部分：泛码桶）

按原始错误文本归组：

| 原因 | 数量 | 说明 |
|---|---:|---|
| grep 正则表达式无效 | 104 | 模型把代码片段当正则（含 lookaround / 非法字符类） |
| shell 参数非法 JSON（未执行） | 86 | §3.1 的投影，非 shell 故障 |
| grep 搜索路径不存在 | 55 | 泛码，应收敛为路径类错误 |
| 策略拒绝（sandbox/plan 等） | 19 | 含 path outside allowlist / mode:plan |
| 子进程 `WaitDelay expired before I/O complete` | 17 | 输出管道等待缺陷 |
| `spawn_subagents` 不支持 `execution_mode` | 12 | 工具契约变更后的旧参数 |
| artifact 引用不存在 | 11 | 引用过期/伪造 id |
| 其他（fetch 404、evaluate_script、download 等） | 59 | 长尾 |

**机制与代码定位**

- 泛码定义与二次诊断：`agent/tool_execution_result_helpers.go:153-185`；
  `toolresult/diagnostic.go:128-155` 可再细分，但依赖文本规则。
- grep 没有结构化错误输出：`toolkit/tools/grep.go:702-708,731-737,777-782`；
  中文错误文本未命中 `diagnostic.go:1999-2004,2047-2065` 的规则。
- 策略类错误（sandbox/plan）没有专属稳定码，退化进 `TOOL_EXECUTION`。

**判断**：这个桶里 **18% 是数据库（已排除）**，47% 是“正则 + 未执行参数 + 路径”三种
本可精确分类的问题，真正无法解释的长尾不足 20%。**优化价值在“精确分类 + 源头修复”，
而不是做一个更大的兜底桶。**

**优化方向**：P1-3（grep 引擎与结构化诊断）、P0-3（分类）、P1-5（管道等待）、P1-6（策略码）。

---

### 3.4 `TOOL_PATH_NOT_FOUND` 192

| 工具 | 数量 | 判定 |
|---|---:|---|
| grep | 93 | 路径猜测/工作目录类（应回归精确码） |
| view | 71 | 同上 |
| ls | 11 | 同上 |
| `take_screenshot` | 10 | **缺陷**：输出 filePath 被当输入做存在性检查 |
| glob | 5 | 路径猜测 |
| `take_snapshot` | 2 | **缺陷**：同截图 |

**原始证据（缺陷部分）**：错误文本形如
`path not found: E:\...\tmp\shot-mobile-360.png (candidates: …\tmp, …\aicli-web-verify.exe, …)`
——`tmp` 目录存在、png 是**待生成的新文件**，候选里甚至列出了无关文件。

**机制与代码定位**

- 预检按“工具级 retry_class + 是否含 mutation 参数”决定是否检查路径
  （`toolexec/preflight.go:1343-1368`），不区分参数角色；
- `isPathLikeKey` 把 `filePath → filepath` 一律视作内容输入（`preflight.go:1586-1594`），
  随后按工作区解析并调用本机 `os.Stat`（`preflight.go:1299-1340,1625-1682`）；
- `os.Stat` 的任何错误都被压成“不存在”（`preflight.go:1664-1682`），丢失权限/IO 区分；
- 权限层按**工具名**统一选 `OpRead/OpWrite`（`policy/tool_policy.go:379-393`），
  无法表达“读页面、写截图文件”这种一次调用的双角色；
- 现有豁免是工具级 `path_preflight=false`（`toolexec/preflight_test.go:390-421`），
  属临时手段，非通用角色契约。

**判断**：12 条是可复现的实现缺陷；其余 180 条主要是使用侧路径错误，
但“存在性检查把一切错误归为不存在”会让真正的权限/IO 问题被误诊。

**优化方向**：P1-4（路径角色契约 + 远端/输出路径处理）。

---

### 3.5 `TOOL_INVALID_ARGS` 65

| 工具 | 数量 | 典型错误 |
|---|---:|---|
| apply_patch | 17 | 缺 `patch`、hunk 语法、必须以 `*** Begin Patch` 开始 |
| edit | 16 | 15 条缺 `file_path`，1 条多重命中 |
| spawn_agent | 13 | `tools_whitelist` / `execution_mode` / `timeout` / `budget_tokens` 不支持 |
| write | 8 | 7 条缺 `file_path`，1 条缺 `content` |
| shell | 3 | `output_bytes_cap` 无效、批次内单条失败 |
| append_write / 其他 | 8 | 缺 `file_path`、枚举错误等 |

**机制与代码定位**

- 校验与明确报错：`toolexec/preflight.go:72-127`（归一化、必填、类型）；
  `toolresult/diagnostic.go:2047-2065`（中文参数/补丁文本兜底）；
- `spawn_agent` 明确拒绝不支持参数并列出支持集（`toolbroker` broker 参数校验，
  已在错误文本中体现），但模型仍反复传入旧参数，说明**工具面与说明/示例不一致**
  或模型沿用了其它委派工具的 schema（`spawn_subagents` 等）。

**判断**：这是**契约面/模型习惯**问题，不是执行器缺陷；但 24 条“缺 file_path”
（edit/write/append_write）与 13 条 spawn_agent 旧参数值得用 schema/示例层修正，
而不是让模型在失败后自行摸索。

**优化方向**：P1-2（结构化反馈）、P2-4（工具面一致性巡检）。
---

### 3.6 `TOOL_BROKER_FAILURE` 的非数据库部分（29）

| 原因 | 数量 | 细节 |
|---|---:|---|
| 状态版本冲突 | 14 | `control_descendant` 9、`subagent_ack_lifecycle` 4、`subagent_control` 1；错误含 `expected/actual version` 与重读提示 |
| 状态/作用域不允许 | 6 | 如“notification allows [inspect] but acknowledge requested”“outside requested root scope” |
| context canceled | 6 | `ask_user_question` 5、`list_agents` 1 |
| batch 尚无 child session 绑定 | 2 | `wait_agent`（错误提示“retry shortly”） |
| 不支持的 section 枚举 | 1 | `subagent_inspect_task` |

**机制与代码定位**：`toolbroker/broker.go:1253-1302` 是**默认兜底分类器**——
业务约束/状态冲突只要没命中关键词（如 version conflict 只按 `sqlite3: interrupted` 等特判），
就落入 `TOOL_BROKER_FAILURE`；保留 Cause 与 `Context["tool"]`，但主消息只有
“broker tool execution failed”。

**判断**：这些不是 Broker 故障，是**并发/状态机语义**。应给 supervision 冲突专属错误码
（如 `SUPERVISION_STATE_CONFLICT` / `SUPERVISION_ACTION_NOT_ALLOWED`），并明确
`allowed_actions`；版本冲突类可自动重读一次并合并。

**优化方向**：P2-3。

---

### 3.7 `TOOL_TIMEOUT` 56

| 维度 | 分布 |
|---|---|
| 工具 | shell 47（failed 33 + partial 14）、`new_page` 5、`list_pages`/`download`/`fetch`/`grep` 各 1 |
| shell 命令族 | PowerShell 进程/脚本 9、go_test 8、其他 7、PowerShell 递归/列目录 6、git 6、go_build 4、node 3、python 2、node 测试 1、shell 搜索 1 |
| 时长 | p50 = 30.0s，p95 = 600.0s（download 600s 为参数上限） |
| `timeout_source` | 缺失 32、tool_argument 15、parent_context_deadline 5、tool_default 4 |

**机制与代码定位**

- 超时解析与优先级：`toolkit/tools/bash.go:2111-2140`（timeout_ms > timeout_sec > timeout 字符串）；
  默认值按命令族推断（`bash.go:2214-2219` 等）。
- 元数据字段已存在于 payload 拷贝清单：`agent/tool_runtime_events.go:745-749`，
  但 32/56 缺失，说明部分路径未回填。
- 子进程管道等待 `WaitDelay expired before I/O complete` 17 条（与超时同族，
  属进程收尾缺陷，见 §3.3）需要在执行器层修复，而不是加超时时间。

**判断**：主要矛盾是**长任务与执行预算/前台模型不匹配**，以及**超时来源不可见**。
样本中大量 `Get-ChildItem -Recurse`、`go test ./...`、整套前端构建属于“本应后台化或
缩小范围”的命令。

**优化方向**：P1-6（元数据全覆盖）、P2-5（命令族预算与后台化引导）。

---

### 3.8 `AGENT_READ_ONLY` 54（全部 shell）

| 维度 | 分布 |
|---|---|
| 命令族 | PowerShell 列目录/查询 23、其他 shell 11、git 9、PowerShell 进程/脚本 3、go_build 2、python 2、go_test 2、node 测试 1、shell 搜索 1 |
| 拒绝原因 | 非白名单 25、复合命令 24、动态语法/重定向 5 |

**现状（需按当前版本回归，不能凭历史计数定性）**

- 已实现：shell 工具在只读下**逐调用**判定（`policy/tool_policy.go:326-354`）；
  复合命令按段检查（`policy/grants.go:247-261`），`git status; git diff`、纯只读管道已可通过
  （`pipeline_test.go:96-109`）；`grants.go:283-290` 已含 `Get-ChildItem/gci`、`Get-Content/gc`；
  混合 commands 批次中任一项违规即拒绝（`tool_policy_test.go:236-273`）。
- 未支持：`Select-Object`/`Where-Object` 等管道后段（`grants.go:275-291`），
  因静态分类器不识别 `$`、反引号、重定向而保守拒绝（`grants.go:235-244`）。
- **正确拒绝**：`go build/test`、解释器脚本、重定向写文件——这些应继续拒绝，
  属只读硬边界（不可被审批/bypass 覆盖，`errors/codes.go:60-62`）。

**判断**：历史样本中确有只读误伤（如 `Get-ChildItem … | Select-Object -First N`），
但也混有任务与权限不匹配的预期拒绝。**优化目标是提升只读查询的识别精度与引导，
而不是降低拒绝总量。**

**优化方向**：P1-6。

---

### 3.9 `user_cancelled` 91

**原始证据**

- 43/91 `duration_ms = 0`（未产生首个 token 即被取消）。
- 只有 9 条能按 `turn_id`/`trace_id` 严格匹配到 `session_end`（其中 3 条带明确
  `cancel_cause`：`user_interrupt` 1、`parent_context(user_interrupt)` 2）。
- 82 条无法归因，原因是所在会话的 `runtime-events.jsonl` 已按保留策略清理
  （184 个采样会话中 146 个文件缺失）。
- 分类实现：`llm/retry_policy.go:1016-1017` 只要 `errors.Is(err, context.Canceled)`
  即返回 `USER_CANCELLED`，不区分取消来源。

**判断**：这个数字**既不能全部算用户主动停止，也不能全部算系统故障**。
已知宿主侧曾有“运行时刷新硬停在途回合”的事故（见
`docs/analysis/a6-host-stop-attribution-and-inflight-refresh-20260927.md`），
当前代码已有延迟重建守卫，但需要按运行版本与 `cancel_source` 才能判断新窗口。

**优化方向**：P0-3（把 `cancel_source/cause/reason` 随请求落库）；P2-6（零时长取消专项看板）。

---

## 4. 跨项根因归纳

四条主线，覆盖本轮 1167 条非数据库记录中的绝大多数：

### 主线 A：模型可见的“参数契约面”与模型实际生成能力不匹配

- 字符串型 `timeout` 让弱 JSON 模型写出裸 `60s`（证据 §3.1）；
- `spawn_agent` 的旧参数（`execution_mode` 等）与 `spawn_subagents` 混用（§3.5）；
- `edit/write` 的 `file_path` 必填被反复漏掉（§3.5）；
- 只读 agent 里模型反复尝试写类命令（§3.8），说明任务与权限的匹配提示不足。
- 结论：**契约面应优先使用“模型难以写坏”的形态（整数、枚举、扁平字段），
  并在失败反馈中给出字段路径与安全示例。**

### 主线 B：错误分类与诊断信息不足，把不同问题混进泛码

- `STALE_CONTEXT` → 上下文超限（`failure_category.go:86`）；
- `AGENT_READ_ONLY` → unknown（子串兜底无映射）；
- grep 非法正则/缺路径 → `TOOL_EXECUTION`（`grep.go:702-708` 无结构化诊断）；
- 请求来源直接拿原始 `error_code` 当 failure_category（`usageanalytics/query_v2.go:520-527`），
  与工具来源走 D5 映射（`query_v2.go:483`）不一致，导致同一错误两套展示口径。
- 结论：**分类修正与结构化诊断的收益在“可定位、可回归”，同时在减少误诊**
  （例如把编辑失配误当上下文超限会引导错误的优化方向）。

### 主线 C：失败后的重试/扩容策略对“语法类退化”无效且放大成本

- 无截断证据也翻倍预算（`provider_retry.go:274-319`）；
- 同 hash 请求重复发送（§2.3）；
- provider 内层 3 次 × 模型反馈 2 次可放大到 9 次 HTTP（`provider_retry.go:240-262`、
  `loop.go:1417,1433-1454`）；
- 失败 usage 未计入预算核算（`provider.go:1796-1797`、`loop.go:822-823`）。
- 结论：**先按“是否真的被预算截断”区分扩容，再限制同 hash 重发与共享总额度。**

### 主线 D：证据留存与保留策略限制事后归因

- 事件文件 146/184 缺失 → 82 条取消无法归因；
- `llm.request.finished` 不含参数解析偏移/原始片段哈希/finish 证据（`loop.go:2366-2416`）；
- `timeout_source` 缺失 32/56；
- HTTP 捕获 256 KiB 首尾截断 + 256 文件清理（`http_debug.go:70,160-174`、
  `chat_http_artifacts.go:16-22,93`）。
- 结论：**把关键结构化事实写进用量记录（不依赖大 body、不依赖长期保留 raw 日志）。**
---

## 5. 优化方案（按优先级分阶段）

> 每项包含：目标 / 改动点 / 安全约束 / 验收指标。
> 验收指标中的“当前值”来自本窗口证据（§2、§3）；目标值为建议上线门槛，
> 需用修复后同长度窗口（建议 7 天或等量请求）对比，而不是历史累计值。

### P0：先修“安全与证据”，不做任何行为放宽

#### P0-1 修正错误分类映射（低风险、立即生效）

- **目标**：让面板与报表语义正确，避免误导优化方向。
- **改动点**
  1. `backend/internal/llm/failure_category.go:79-99`：
     - 移除/收窄 `strings.Contains(normalized,"CONTEXT")` 的泛匹配；
     - 至少为 `STALE_CONTEXT`、`TOOL_PATH_NOT_FOUND`、`AGENT_READ_ONLY`、
       `TOOL_BROKER_FAILURE`、`STALE_*` 增加**精确前缀映射**（前缀优先级已存在于 switch 前半段）。
     - 建议类别：`STALE_CONTEXT → tool_error`（或新增 `edit_conflict` 类），
       `AGENT_READ_ONLY → tool_error`（或 `permission_denied`），
       `TOOL_BROKER_FAILURE → tool_error`（或 `broker_error`）。
  2. `backend/internal/usageanalytics/query_v2.go:520-527`：requests 来源也走
     `llm.FailureCategoryFromErrorCode`（与 tools 来源 `:483` 一致），
     避免原始大写错误码直出。
  3. 如需新增类别：同步 `frontend/src/pages/usage-analytics/error-patterns-panel.tsx`
     的 `failureCategoryKeys` 与 i18n（`observability.failureCategories.*`）。
- **安全约束**：仅改映射与展示，不改执行路径。
- **验收**：`STALE_CONTEXT` 的 failure_category 不再出现 `context_overflow`；
  `AGENT_READ_ONLY` 不再显示 unknown；请求来源与工具来源对同一码给出一致类别。

#### P0-2 参数解析证据结构化落盘（不依赖 raw body）

- **目标**：让每个失败调用都能离线回答“错在哪、什么形态、是否被截断、是否执行”。
- **改动点**
  1. `backend/internal/llm/adapter/openai.go:700-716`：解析失败时记录
     `parse_class`（未闭合字符串/缺括号/裸字面量/非法转义/顶层非对象/其他）、
     错误偏移、顶层类型、参数字节长度与 SHA-256 前 16 位；
     不得记录密钥与完整命令原文到用量库。
  2. `backend/internal/llm/adapter/malformed_tool_call.go:89-111`：把
     `Truncated` 与“是否收到终结事件/`[DONE]`/finish_reason”分开记录。
  3. `backend/internal/agent/loop.go:2366-2416`（失败日志）与
     `backend/internal/agent/tool_runtime_events.go:706-760`（payload 拷贝清单）：
     新增上述字段与 `not_executed`、`tool_call_id`、`finish_reason`。
  4. `backend/internal/cacheanalytics/record_builder.go:65-80`：请求记录增加
     `cancel_source/cancel_cause/cancel_reason`、`terminal_seen`、`arg_error_class`
     （值来自 `llm.request.finished` payload，会话结束由 chat actor 补齐）。
- **安全约束**：只记录结构化元数据与哈希；不把原始命令、密钥、URL 查询串写入统计库。
- **验收**：新窗口任取一条 `upstream_invalid_response`，无需 HTTP 工件即可区分
  `bare_duration / unterminated / not_object / truncated`；
  `TOOL_TIMEOUT` 的 `timeout_source` 覆盖 100%。

#### P0-3 退化重试策略：只在“真截断”时扩容，同 hash 不重发

- **目标**：停止无效扩容与同请求重放，保持安全拒绝语义。
- **改动点**
  1. `backend/internal/llm/provider_retry.go:274-281,295-319`：
     - `invalid_tool_arguments` 不再无条件进入扩容；
     - 仅当 `MalformedToolCallError.Truncated==true` 或 `finish_reason=length`
       才允许扩容（保留 `truncated_tool_call` 的合法扩容路径）；
     - 语法类退化最多 1 次同预算重采样，之后交给模型反馈路径。
  2. 同请求 hash 去重：在 provider retry 循环内保存
     `(tool_surface_fingerprint, prompt_fingerprint, max_tokens, attempt 参数)`
     规范化 hash，命中时跳过重复发送并直接进入反馈/失败路径。
  3. 共享恢复状态：`backend/internal/agent/loop.go:1417,1433-1454` 与
     `:2275-2302` 的反馈/预算升级共享“本 run 已尝试次数与预算”，避免跨层相乘
     （当前路径上限 9～18 次）。
  4. 失败 usage 记账：`backend/internal/llm/provider.go:1796-1797` 与
     `backend/internal/agent/loop.go:822-823`，让 malformed/失败请求的 token 也进入
     用量与预算核算。
- **安全约束**：只改变“重试与预算”决策，不触碰参数补全与执行前校验
  （`adapter/openai.go:535-536` 保持不变；禁止补引号/括号/转义后执行）。
- **验收**：新窗口中 `invalid_tool_arguments` 触发的扩容 = 0；
  同一规范化请求 hash 的重复发送 = 0；
  退化样本的平均 HTTP 尝试数下降（示例场景 3 → ≤1 同预算重采样 + 1 次反馈）。

#### P0-4 保持“拒绝执行”与只读硬边界（明确不做项）

- 不自动修复命令参数后执行：`toolargs/normalize.go:23-41,120-166` 的容错仅保留在
  执行器入口既有位置，**不得前移到严格校验前**。
- 不放宽只读/审批边界：`errors/codes.go:60-62`、`policy/tool_policy.go:326-364`。
- 不以“错误码数量下降”为目标牺牲真实拦截；验收只看**误伤与误放**。

---

### P1：契约面与工具诊断

#### P1-1 shell 超时字段模型面整数化（针对 65/66 主因）

- **目标**：让模型即使生成裸数字也仍是合法 JSON，消除 `bare duration` 类。
- **改动点**
  1. `backend/internal/toolkit/tools/bash.go:86-162`（模型可见 `parameters`）：
     - 模型面**移除字符串型 `timeout`**，或用 `deprecated`/描述明确“仅字符串形式”；
       优先只暴露 `timeout_ms`、`timeout_sec`（integer）；
     - 保留执行端对 `timeout` 字符串的兼容解析（`bash.go:2111-2140`）以兼容历史会话与
       其它客户端，不改变优先级语义。
  2. 同步 `backend/internal/toolkit/tools/aicli_exec.go:392-397` 与
     `tool_surface` 精简逻辑（`agent/loop.go:4150-4197`）：
     精简不得删除 `timeout_sec/timeout_ms`，可删除字符串 `timeout`。
  3. `agent/grep_tool_surface_test.go` 同类测试补齐 shell 超时字段断言。
- **安全约束**：仅 schema/文档变更；整数超时同样受 `AICLI_SHELL_MAX_COMMAND_TIMEOUT`
  上限约束（`bash.go:67-73`）。不得静默把 `60s` 字符串“修正”为数字后执行。
- **验收**：新窗口 shell 非法 JSON 参数中 `bare duration` = 0；
  `timeout_sec/timeout_ms` 在显式超时调用中占比 ≥95%；
  历史字符串形式回归测试保持通过。

#### P1-2 失败反馈结构化与有界化

- **目标**：一次反馈即可让模型改对，减少 2 次回注与重试次数。
- **改动点**
  1. `backend/internal/agent/loop.go:1474-1528`：反馈内容以“错误类别 + 字段路径 +
     合法示例 + 原始参数**有界截断**（如 ≤2 KiB，头部+尾部）”替代回贴完整坏参数
     （当前 `:1495-1503` 无长度上限）。
  2. 使用失败请求的 schema 快照与指纹（`:1458-1470` 当前为恢复时重新解析），
     保证反馈与当次工具面一致。
  3. 恢复调用 ID 补 `turn_id/request_id`（`:1573-1575`），避免跨 turn 碰撞。
- **安全约束**：反馈只是文本；不得据此自动改参数执行。
- **验收**：同错复现率（同 tool + 同 `arg_error_class`）下降 ≥50%；
  平均反馈次数下降；恢复调用 ID 碰撞 = 0。

#### P1-3 grep 引擎、参数与结构化诊断

- **目标**：消除“非法正则 104 条”与“缺路径落泛码 55 条”。
- **改动点**
  1. `backend/internal/toolkit/tools/grep.go:775-793`：先判定实际引擎
     （rg / builtin）；显式 PCRE2（`rg_args:["-P"]`）时**不要**先用 Go `regexp` 拒绝；
     仅 builtin 路径编译 Go 正则。`1725-1758` 的编译错误返回结构化错误码。
  2. 精简工具面与提示统一：`grep.go:1754-1756` 建议的 `pcre2=true` 若不在精简
     schema（`agent/grep_tool_surface_test.go:29-40`）中，则改为 `rg_args:["-P"]`。
  3. `rg_args` 类型纠正按参数语义处理（`toolexec/preflight.go:1112-1122` 不要把所有
     array 的普通字符串包装成单元素数组）；保持危险 rg 参数拒绝。
  4. 诊断兜底修正：`toolresult/diagnostic.go:1999-2004,2047-2065` 增加窄前缀规则
     （“正则表达式无效/搜索路径不存在”），但**不得**覆盖已有可信特定码。
  5. 顺带修复：`grep.go:4154-4161` 用 `CombinedOutput()` 合并 stdout/stderr，
     配合 `:3372-3376` 的 hasOutput 分支与 `:3844-3862` 的宽松行归一化，
     stderr-only 也可能被包装成“部分成功”。应分离 stdout/stderr/退出码，
     仅真实结果 stdout 参与部分结果判断，stderr-only 必须失败（保留真实超时的
     部分结果，见 `grep_partial_test.go`）。
- **安全约束**：不得自动把非法正则改成 literal/转义（改变搜索语义）；
  不得放行危险参数；引擎不可用时显式报错，不静默退化。
- **验收**：`TOOL_EXECUTION` 中 grep 非法正则 = 0（改为精确码）；
  PCRE2 模式能实际执行；stderr 伪成功 = 0；原参数不被建议盲重试。

#### P1-4 路径角色契约（输入/输出/工作目录 + 归属）

- **目标**：修复截图/快照输出误拒（12 条），同时不引入越权。
- **改动点**
  1. 引入工具注册层的路径描述：`role=input|output|inout|workdir`、
     `fs_owner=runtime|tool_server|browser|opaque`、`must_exist`、类型/覆盖/建父目录约束。
     预检（`toolexec/preflight.go:1299-1368,1586-1620`）与权限
     （`policy/tool_policy.go:379-393`）共享该描述。
  2. 本地输出：允许叶子不存在，校验父目录存在、写权限与覆盖规则；
     本地输入：保持存在性/类型/读权限与 fail-closed。
  3. 非本地主机路径（浏览器/MCP 服务端）：不拼工作区、不做本机 `os.Stat`/候选枚举。
  4. `preflight.go:1664-1682` 区分 `os.ErrNotExist` 与权限/IO 错误（后者给独立码）。
  5. 自动纠错边界收紧：默认只给候选；自动改写限“可信声明的本地单输入、唯一且语义等价”，
     禁止改写输出/工作目录/远端路径，改写后重新执行权限校验
     （`preflight.go:208-225,1690-1759,1785-1856`）。
- **安全约束**：跳过本地存在性检查 ≠ 放行写操作；只读/plan/sandbox 限制不变；
  不接受不可信 MCP 自报的角色授权。
- **验收**：截图/快照新输出误拒 = 0；非本地路径触发本机 Stat = 0；
  输出/越权自动改写 = 0；真实缺输入仍 100% 拦截。

#### P1-5 只读分类器：提升识别精度，不降低边界

- **目标**：只读场景误伤下降，任务与权限不匹配时给出正确引导。
- **改动点**
  1. `backend/internal/policy/grants.go:275-291`：在**静态可验证**前提下，
     为常见只读流水后段增加固定 Cmdlet 白名单形态：
     `Select-Object -First/-Skip <非负字面量>`、`Sort-Object`、`Measure-Object`、
     `Where-Object`（仅字面量属性比较）等；含脚本块/计算属性/变量的一律拒绝。
  2. `tool_policy.go:326-364` 保持逐段检查与硬拒绝；错误里返回失败段位置与稳定原因。
  3. `toolresult/diagnostic.go:2176-2186`（若存在）按“未支持静态查询” vs
     “明确副作用/动态语法”给不同 next_action：前者建议专用工具/分页参数，
     后者建议换用具备写权限的执行者（reader/writer 子代理）。
- **安全约束**：不放行 `go build/test`、解释器脚本、重定向、动态调用、变量替换；
  不以 AGENT_READ_ONLY 总量下降为指标。
- **验收**：准入集合正例正确率 100%；拒绝集合误放 = 0；
  只读硬边界绕过 = 0（安全测试）。

#### P1-6 参数聚合身份一致性断言（保守加固，补充 §3.1）

- **目标**：把“可构造但尚未在样本中证实”的聚合缺陷变成 fail-closed 断言，
  避免未来出现串槽或身份覆盖导致的静默错误命令。
- **改动点**
  1. `backend/internal/llm/adapter/openai.go:949-964,1043-1054`：同一 index 的
     ID/type/name 必须一致；同 ID 换 index、名称被拆片、多槽歧义一律拒绝并记录
     身份冲突原因（不按内容去重、不用整条响应共享的 chunk.id 判定重复）。
  2. `:854` 按实际 `choice.index` 隔离（不再固定取 `choices[0]`）；
     `:876-877,1012-1023` modern/legacy 同时出现时若写入同一槽则拒绝。
  3. `providercompat/response.go:114-123` 只在确认整个 SSE 事件（多行 `data:` 拼接后）
     后做兼容转换，避免多行事件绕过转换导致原生对象参数被丢弃变 `{}`。
  4. 记录 delta 片段序号与累计哈希（进 P0-2 的结构化证据），用于离线核对。
- **安全约束**：任何身份不确定都**拒绝执行**，不得猜测归并；
  断言只在“严格校验前”的聚合层生效，不改变拒绝后不执行的语义。
- **验收**：合成流矩阵中串槽/身份覆盖/静默 `{}` 变体全部 fail-closed；
  多行 `data:` 与单行 `data:` 对同一对象参数给出一致结果。

---

### P2：体验、效率与归因

#### P2-1 `STALE_CONTEXT` 恢复体验（编辑协议）

- **改动点**
  1. 失败反馈模板强化“先 view 目标文件 → 用 `current_snippet` 重建更短 hunk”
     （工具已返回 `current_snippet`/`suggested_view_offset`/`limit`），
     并在 `agent/loop.go` 的编辑类反馈中固定该顺序。
  2. `toolkit/tools/apply_patch.go:1412-1445` 保持 stale 与语法错误分离；
     对连续 stale 的同一文件，反馈中明确建议分段/缩小 `@@` 上下文。
  3. 不改“匹配失败即拒绝”的语义（不得模糊匹配后直接落盘）。
- **验收**：新窗口 STALE_CONTEXT / 编辑调用次数相比本窗口下降 ≥40%；
  失败后 2 次调用内同文件修正成功率提升（需用 file_path 维度统计）。

#### P2-2 长任务的预算与后台化引导

- **改动点**
  1. 命令族级默认提示（go test/build、npm/vitest、递归列目录、git 大范围）：
     建议缩小范围或改用后台任务；`timeout` 超出前台预算时引导 `background_task`。
  2. 修复子进程收尾：`WaitDelay expired before I/O complete` 17 条属管道等待缺陷，
     在执行器层处理（等待 I/O 排空/超时后仍保留部分输出），不要靠加超时。
  3. 保持部分输出可用（partial 结果不要因超时被丢弃）。
- **验收**：`TOOL_TIMEOUT` 中 shell 占比下降；超时后仍有部分输出的比例上升；
  子进程 WaitDelay 错误 = 0。

#### P2-3 Broker 非数据库错误的语义化

- **改动点**：`backend/internal/toolbroker/broker.go:1253-1302` 增加
  `SupervisionStateConflict`（携带 expected/actual version 与重读指引）、
  `SupervisionActionNotAllowed`（携带 `allowed_actions`）等专属码；
  版本冲突在 broker 内自动重读一次再执行；`wait_agent` 的
  “child session 尚未绑定”按提示做一次短重试（≤2s）。
- **验收**：非存储 `TOOL_BROKER_FAILURE` 下降；状态冲突二次成功率提升；
  错误消息不再出现“broker tool execution failed”这一无信息主消息。

#### P2-4 委派工具契约一致性巡检

- **改动点**：核对 `spawn_agent` / `spawn_subagents` / `followup_task` / `send_input`
  的参数集与示例；对历史高频误用参数（`tools_whitelist`、`execution_mode`、`timeout`、
  `budget_tokens`）在被拒时返回“最近替代参数”指引（已有 supported 列表，继续强化）。
- **验收**：新窗口 `TOOL_INVALID_ARGS` 中 spawn 系占比下降 ≥50%。

#### P2-5 取消归因闭环

- **改动点**：把 `session_end.cancel_source/cancel_cause/cancel_reason`
  （A6 文档已实现落盘）关联进请求用量记录（P0-2 已含字段）；
  对 `duration_ms=0` 的取消增加“取消发生在首个 SSE 之前”标记。
- **验收**：新窗口 `user_cancelled` 归因率 ≥95%；宿主刷新类取消可被单独统计。

#### P2-6 观测与回归看板

- **改动点**：错误榜增加“错误类别细分 + 版本/时间对比”；
  修复 P0-1 后，同一码在工具与请求两个来源的类别一致；
  为本文每个 P0/P1 项建立自动化回归（见 §6）。
- **验收**：上线后 7 天可给出每项指标的前后对比（含样本量）。

---

## 6. 验证与回放测试矩阵

### 6.1 上游参数与重试（对应 §3.1、P0-1/2/3、P1-1/2）

| 组 | 最小变体 | 必须断言 |
|---|---|---|
| 分片 | 每个转义/UTF-8 边界切分；LF/CRLF；单行/多行 `data:`；字符串/原生对象参数 | 语义与身份一致；不静默变 `{}` |
| JSON 类别 | 未闭合字符串、缺括号、**裸 `120s`**、非法转义、`[]/null/true`、双重字符串、`{}{}`、重复 key；各配 `tool_calls`/`length` | 分类、偏移、终结证据独立正确；拒绝项执行次数 0；非截断类扩容 0 |
| 身份/重放 | index 0/1 交错、稀疏 index、缺 index、缺双身份、多槽歧义、ID/index 冲突、名称变化、重复中间块/整对象、modern+legacy | 不串槽、不覆盖身份、不按内容猜测去重；歧义 fail-closed |
| 重试 | provider/runtime 配置 0/1/3/10；规则覆盖；`disable_retries`；能力上限不变；退化夹杂其他错误 | 精确 HTTP 次数与有效预算一致；同 hash 不重发；各层共享总额度 |
| 恢复 | 连续失败→反馈→成功；同名多调用；好坏混批；跨 turn 缺 ID | 坏批 0 执行；反馈配对；恢复 ID 不冲突；失败 usage 入账 |
| 回放 | 本文证据中的脱敏请求 + SSE（完整/超 256 KiB/仅 preview 三类） | 按生成版本走完整链路；原始 hash 可核对；缺证据样本标注“不可回放” |

**合成基线**（供实现者直接使用）：两个 delta 将 `{"command":"echo ok","timeout":60}`
分开生成（第二块同时携带 `finish_reason=tool_calls`），随后 `[DONE]`；
再接一个 `{"command":"echo ok","timeout":60s}` 变体，只接 spy executor，不执行 shell。

### 6.2 路径与权限（对应 §3.4、§3.8、P1-4/5）

- 正例：本地新输出（叶子不存在）、已存在输出覆盖、输入输出混合、目录输出。
- 反例：真实缺输入、缺父目录、越界路径、symlink/junction 越界、敏感目标。
- 角色：同名字段不同角色、嵌套数组、大小写变体（`filePath`/`file_path`）。
- 归属：浏览器/MCP 远端路径（绝对/相对）不应触发本机 `os.Stat`。
- 只读：`git status; git diff`、`Get-ChildItem`、`… | Select-Object -First N` 正例；
  `go build/test`、重定向、`$` 变量、脚本块、混合批次反例。
- 目标：新输出误拒 0；非本地 Stat 0；越权放行 0；拒绝集合误放 0。

### 6.3 grep（对应 §3.3、P1-3）

rg/builtin × {普通正则、断言、反向引用、非法括号、literal}；
另测：rg 无 PCRE2 能力、精简 schema 的恢复示例、`rg_args` 数组/JSON 数组/普通字符串、
带空格路径、stderr-only、匹配+超时。目标：静默改写 0、stderr 伪成功 0、
危险参数拒绝率 100%、exit 1 仍为合法空结果。

### 6.4 编辑与超时（对应 §3.2、§3.7、P2-1/2）

- 编辑：CRLF/LF、多处命中、stale 后重读再改、连续 stale 同文件、补丁语法错误对照。
- 超时：go test/build、npm、递归列目录、长 git、部分输出场景；
  断言 `timeout_source` 100% 存在、部分输出保留、WaitDelay 错误 0。

---

## 7. 验收指标（窗口对比用）

| 指标 | 本窗口基线 | 目标 | 数据源 |
|---|---:|---:|---|
| 完整 HTTP 响应中 bare-duration 非法参数 | 65 | 0（新窗口） | HTTP 工件回放 |
| `invalid_tool_arguments` 触发预算扩容 | 有（示例 32k→64k） | 0 | `llm.retry` + 请求记录 |
| 同规范化请求 hash 重复发送 | 示例 3 次同参 | 0 | 请求工件/新字段 |
| `STALE_CONTEXT` 映射为 context_overflow | 139 | 0 | 分析库 |
| `AGENT_READ_ONLY` 类别 unknown | 54 | 0 | 分析库 |
| 截图/快照输出路径误拒 | 12 | 0 | 工具记录 |
| `TOOL_EXECUTION` 中 grep 非法正则 | 104 | 0（改为精确码） | 工具记录 |
| `TOOL_TIMEOUT` 的 `timeout_source` 缺失 | 32/56 | 0 | 工具记录 |
| `user_cancelled` 可归因比例 | 9/91（严格匹配） | ≥95% | 请求记录 |
| 非存储 `TOOL_BROKER_FAILURE` | 29 | 下降 ≥50% 且语义化 | 工具记录 |
| 越权/只读边界误放 | 0（基线） | 0（不得回归） | 安全测试 |

> 说明：所有“新窗口”指标必须以修复后的二进制定义基线；建议同时记录
> 样本量与 provider/model 分布，避免把 provider 切换误读为修复效果。

---

## 8. 风险、不做项与前提

1. **禁止启发式修复后执行**：不补引号/括号/转义、不猜测命令、不把 `60s` 静默转成
   `60` 后执行。参数必须在执行前通过严格对象校验（`adapter/openai.go:535-536`）。
2. **不降低只读/沙箱/审批边界**：只读白名单扩展必须静态可验证、语义等价；
   审批/bypass 不能覆盖只读硬边界（`errors/codes.go:60-62`）。
3. **路径角色契约不得越权**：跳过本地存在性检查不等于放行写入；
   远端路径交所属执行器验证；自动纠错限可信本地单输入。
4. **重试收敛不得伤害合法恢复**：真截断（`finish_reason=length` /
   `Truncated=true`）仍保留扩容与恢复路径；只取消“无证据扩容”。
5. **provider 差异按证据处理**：`commandgo` 比例高是观察性事实；
   优先修我方 schema 与重试策略，不做 provider 特判硬编码；灰度与回滚可用
   工具面指纹（`tool_surface_fingerprint`）与配置开关控制。
6. **版本前提**：部分历史缺陷可能已修复（只读复合命令、部分 PowerShell 查询等）；
   本方案条目需在当前版本上先回归复现，再进入实现排期。
7. **超出本方案范围**：SQLite/WAL/在线库操作与 `artifact_read` 存储错误
   （112 条），按用户要求排除；其历史结论见
   `docs/analysis/sqlite-wal-corruption-and-offline-compaction-20260926.md`。

---

## 附录 A：证据与复现

- 统计脚本：`.aicli/tmp/nondb-usage-audit-20260927.mjs`（只读，可重跑）
- 脱敏证据：`.aicli/tmp/nondb-usage-evidence-20260927.json`
- 关键原始示例：
  - `~/.aicli/chat-logs/2026/09/27/session_20260927123719_qv6Y4it8/http/052|055|057_response_provider_wrapper.json`
  - `~/.aicli/chat-logs/2026/09/27/session_20260927160529_pIgkVfY5/http/306|428_request_provider_wrapper.json`
- 关联文档：`docs/analysis/a6-host-stop-attribution-and-inflight-refresh-20260927.md`、
  `docs/analysis/sqlite-wal-corruption-and-offline-compaction-20260926.md`。

## 附录 B：关键代码索引

| 主题 | 位置 |
|---|---|
| 工具参数严格校验 | `backend/internal/llm/adapter/openai.go:700-716,535-536` |
| malformed 分类 | `backend/internal/llm/adapter/malformed_tool_call.go:89-111` |
| 预算扩容判据 | `backend/internal/llm/provider_retry.go:274-281,295-319` |
| 取消分类 | `backend/internal/llm/retry_policy.go:1016-1017` |
| 错误类别映射 | `backend/internal/llm/failure_category.go:79-99` |
| malformed 恢复与投影 | `backend/internal/agent/loop.go:1417,1433-1454,1474-1528,1587-1601` |
| 工具可靠性字段 | `backend/internal/agent/tool_runtime_events.go:706-760` |
| shell 超时解析/字段 | `backend/internal/toolkit/tools/bash.go:86-162,2111-2140` |
| grep 引擎/诊断 | `backend/internal/toolkit/tools/grep.go:775-793,1725-1758,4154-4161` |
| 路径预检 | `backend/internal/toolexec/preflight.go:1299-1368,1586-1682,1690-1759` |
| 权限/只读 | `backend/internal/policy/tool_policy.go:326-393`、`backend/internal/policy/grants.go:235-291` |
| Broker 兜底分类 | `backend/internal/toolbroker/broker.go:1253-1302` |
| 用量入库/分类 | `backend/internal/usageanalytics/query_v2.go:476-486,510-531` |
| HTTP 捕获上限 | `backend/internal/llm/http_debug.go:70,160-174`；`backend/cmd/aicli/commands/chat_http_artifacts.go:16-22,93,138-152` |

---

## 附录 C：第一波实施记录（2026-09-27 晚）

按本方案 §5 的优先级，第一波只做**低风险、证据最硬**的条目；改动均已通过包级测试。

### C.1 已实施

| 条目 | 改动 | 文件 |
|---|---|---|
| P0-1 分类映射 | `STALE_CONTEXT`/`STALE_EDIT`/`AGENT_READ_ONLY`/`SESSION_LEASE_CONFLICT` 等精确码不再落 `context_overflow`/`unknown`；requests 来源与 tools 来源共用 `FailureCategoryFromErrorCode` 映射 | `backend/internal/llm/failure_category.go`、`backend/internal/usageanalytics/query_v2.go` |
| P0-3a 扩容门控 | `invalid_tool_arguments` 只有携带截断证据（`finish_reason=length/max_tokens` → `MalformedToolCallError.Truncated`）才允许翻倍 `max_tokens`；裸 duration 等语法退化不再触发扩容 | `backend/internal/llm/provider_retry.go`、`backend/internal/llm/adapter/malformed_tool_call.go` |
| P1-1 超时字段整数化 | 模型可见的 `bash` / `execute_shell_command` / legacy `shell` function 工具面移除字符串型 `timeout`，只保留 `timeout_ms`/`timeout_sec` 整数；执行端继续兼容 `timeout="2m"` 字符串（历史会话与其它客户端不受影响） | `backend/internal/toolkit/tools/bash.go`、`backend/internal/toolkit/tools/execute_shell_command.go`、`backend/cmd/aicli/functions/shell.go` |
| P1-3a grep 引擎顺序 | 显式 `rg_args:["-P"]`/PCRE2 等 rg-bound 请求先把模式原样交给 rg，不再被 Go `regexp` 预编译拒绝；恢复提示从模型面已裁剪的 `pcre2=true` 改为 `rg_args:["-P"]` | `backend/internal/toolkit/tools/grep.go` |

### C.2 新增/更新的测试

- `backend/internal/llm/failure_category_test.go`（新增）：分类映射与幂等性。
- `backend/internal/llm/retry_policy_test.go`：截断证据门控（语法退化不扩容、真截断仍扩容）。
- `backend/internal/toolkit/tools/bash_test.go`：模型面只有整数超时字段（bash + execute_shell_command + commands 批次项）。
- `backend/internal/toolkit/tools/grep_engine_selection_test.go`（新增）：PCRE 模式路由到 rg；rg 缺失时报告引擎问题而非“正则无效”；提示只引用模型面存在的 `rg_args`。
- `backend/internal/toolkit/tools/grep_test.go`：既有提示测试同步到 `rg_args` 口径。

### C.3 验证结果

```
go test ./internal/toolkit/tools/ ./internal/llm/ ./internal/llm/adapter/ ./internal/usageanalytics/ ./cmd/aicli/functions/ -count=1
ok  internal/toolkit/tools
ok  internal/llm
ok  internal/llm/adapter
ok  internal/usageanalytics
ok  cmd/aicli/functions
```

> 复核时（19:2x）`cmd/aicli/functions` 因并行会话新增的未跟踪文件
> `backend/internal/imageprep/validate.go` 无法编译而中断；该文件不属于本方案改动，
> 本方案涉及的 `shell.go` 在此前一次包含它的全量运行中已通过。

### C.4 暂缓项（下一波）

| 条目 | 原因 |
|---|---|
| P0-2 证据结构化落盘 | 需要动 `agent/loop.go`/`cacheanalytics`，当前有并行会话正在改相关文件 |
| P0-3b 同 hash 重发抑制 | 需要改三处重试循环（streaming/非流式/gateway），建议与 P0-3a 上线观察后一起做 |
| P1-2 反馈有界化 | 同上，涉及 loop 反馈模板 |
| P1-4 路径角色契约 | 涉及 `toolexec/preflight.go` 与 `policy`，安全敏感，且有并行会话正在改 executor/sandbox 相关区域，先不与其它改动交叉 |
| P1-6 聚合身份断言 | 需要合成流矩阵配套，建议独立小步提交 |
| P2 系列 | 依赖前序观测数据与专属看板 |

### C.5 上线观察指标（对比本窗口基线）

1. 新窗口 `invalid_tool_arguments` 中 bare-duration 占比 → 期望趋近 0（schema 生效）。
2. 语法类退化的 `llm.retry` 中 `widened max_tokens` 事件 → 期望为 0。
3. `STALE_CONTEXT` 在分析页的 failure_category → 期望 `tool_error`，不再是 `context_overflow`。
4. `AGENT_READ_ONLY` → 期望不再是 `unknown`。
5. PCRE 模式 grep 成功率与 `rg_args:["-P"]` 使用率（修复后可正常执行）。
6. 同参数哈希跨 attempt 重复次数（为 P0-3b 提供依据）。

---

## 附录 D：第二波实施记录（2026-09-27 深夜）

第二波继续只做**可离线验证、不触碰安全边界**的条目；全部通过包级测试。

### D.1 已实施

| 条目 | 改动 | 文件 |
|---|---|---|
| P0-2 解析证据（适配器层） | 非法参数调用带 `parse_class`（`bare_literal`/`unterminated_string`/`unterminated_container`/`not_object`/`invalid_escape`/`syntax_error`/`other`）、`parse_offset`、`arg_bytes`、`arg_sha256`（前 16 位）；错误消息只带脱敏证据，不含原始参数文本 | `backend/internal/llm/adapter/malformed_tool_call.go`、`openai.go`、`codex.go` |
| P0-2 证据落事件 | `tool.requested` / `tool.completed` / `tool.malformed_arguments.recovered` 事件新增 `argument_evidence`（tool/parse_class/arg_bytes/arg_sha256/finish_reason/truncated/not_executed/tool_call_id） | `backend/internal/agent/loop.go` |
| P0-3b 同预算重采样上限 | 语法类 `invalid_tool_arguments`（无截断证据）在 provider 层最多 1 次同预算重采样，之后交回 agent 层 re-prompt（prompt 改变 → 请求 hash 改变）；真截断仍走预算扩容，不受限 | `backend/internal/llm/provider_retry.go`（新增 `trackMalformedSyntaxResample`）、`provider.go`（非流式 + 流式）、`gateway_client.go` |
| P1-3b stderr 证据分离 | `runGrepCommand` 分离 stdout/stderr：stdout 是唯一证据，stderr 只进错误诊断，消除「0 匹配 + stderr 报错」的 partial-success 伪成功 | `backend/internal/toolkit/tools/grep.go` |
| P1-6a choice 身份 fail-closed | 流式 chunk 携带显式非 0 `choice.index`（含 `choices[1..]`）时拒绝聚合，返回 `unexpected_choice_index`；缺省 index 的旧协议形态保持兼容 | `backend/internal/llm/adapter/openai.go`（`validateOpenAIChoiceIdentity`） |

### D.2 新增/更新测试

- `adapter/malformed_tool_call_test.go`：四类 parse_class 分类、长度/哈希、消息不落原文。
- `agent/loop_malformed_tool_call_test.go`：降级事件携带 `argument_evidence` 且不含原始参数。
- `llm/retry_policy_test.go`：`trackMalformedSyntaxResample` 上限语义（语法类 1 次；截断不计入）。
- `toolkit/tools/grep_stderr_evidence_test.go`（新）：stderr 不混入 stdout 证据。
- `adapter/openai_stream_choice_test.go`（新）：非 0 choice.index 拒绝、混入第二 choice 拒绝、缺省 index 兼容。

### D.3 验证结果

```
go build ./...                                                                    # ok
go test ./internal/llm/ ./internal/llm/adapter/ -count=1                          # ok
go test ./internal/agent/ -count=1                                                # ok
go test ./internal/toolkit/tools/ ./internal/usageanalytics/ ./cmd/aicli/functions/ -count=1   # ok
```

### D.4 剩余项与建议

| 条目 | 状态 |
|---|---|
| P1-4 路径角色契约 | 未做。`toolexec`/`policy` 目前无并行改动，但需要新契约字段（role/fs_owner/must_exist）与截图类宿主工具的定义打通，建议独立一波 |
| P1-2 反馈有界化（`invalid_tool_arguments` 第二通道） | 未做；P0-3a/3b 已先把重放次数收敛，下一步补「反馈有界 + 与 P0-3 共享计数」 |
| P0-2 剩余：cacheanalytics 记录字段（`terminal_seen`/`arg_error_class`） | 未做；涉及统计库列与迁移，建议与看板需求一起排期 |
| P1-6 剩余：delta 片段序号/累计哈希、providercompat 多行 `data:` 事件 | 未做；`choices[0]` 身份问题已 fail-closed，多行事件需要事件级缓冲，改动集中在 providercompat |
| P1-5 只读分类器白名单 | 未做；`policy` 无并行改动，可独立推进 |
| P2 系列 | 依赖新窗口观测数据 |

### D.5 观察指标补充（第二波）

1. `tool.malformed_arguments.recovered` 事件中 `evidence[].parse_class` 分布（期望 bare_literal 随 P1-1 上线下降）。
2. 同一工具名同一 step 的 provider 层 attempt 数（期望语法类 ≤2：1 次原始 + 1 次重采样，之后转 re-prompt）。
3. grep 失败中 `engine` 与 `partial` 组合（期望不再出现 stderr-only 的 `partial=true, matches=0`）。
4. `unexpected_choice_index` / `invalid_choice_index` 计数（期望 0；出现即说明上游流协议异常，可用于定位 provider_error 子集）。

---

## 附录 E：第三波实施记录（2026-09-28 凌晨）

第三波聚焦 P1-2 与 P1-6：反馈有界化、恢复身份跨 turn 唯一、聚合身份 fail-closed。

### E.1 已实施

| 条目 | 改动 | 文件 |
|---|---|---|
| P1-2 反馈有界化 | 失败反馈不再全文回贴坏参数：头部 1KiB + 尾部 1KiB + `parse_offset` 附近 256B 窗口 + 省略标记，附 `parse_class/arg_bytes/offset/arg_sha256/finish_reason` 紧凑证据行 | `backend/internal/agent/loop.go`（`malformedArgumentPreview`/`malformedCallEvidenceLine`/`utf8Safe*`） |
| P1-2 恢复调用 ID 跨 turn 唯一 | `malformedToolCallID` 种子并入 `turnID`：step 每 turn 从 1 重算 + provider 固定 id 会让跨 turn 同类失败碰撞；turnID 固定且持久化，同 turn 重放仍稳定 | `backend/internal/agent/loop.go` |
| P1-2 反馈 schema 一致性 | 核查结论：恢复路径 `resolveAvailableTools` 命中的是本 turn 冻结快照（think 路径在 `freezeToolSurfaceForTurn` 之后 `SaveTurnToolSurface`），与失败请求的工具面同源，无需再引入请求级快照 | 仅记录，无代码改动 |
| P1-6 聚合身份断言 | 同 id 出现在不同 index、同 index 改 id/改 type/改 name、同 delta 或跨 chunk 的 modern+legacy 混写 → 全部 fail-closed（空占位 function_call 保持兼容） | `backend/internal/llm/adapter/openai.go`（`StreamState.ToolCallIndexByID`、`SawModernToolCalls`/`SawLegacyFunctionCall`） |
| P1-6 delta 片段证据 | `StreamToolCall.ArgFragments` 统计增量片段数，随 malformed 调用进入错误消息（`delta_fragments=N`）与运行时事件 `argument_evidence.arg_fragments` | `openai.go`、`malformed_tool_call.go`、`agent/loop.go` |
| P1-6 多行 SSE 事件 | providercompat 按「事件」缓冲 `data:` 行：空行/非 data 行/EOF 时才整体解析归一化；拼接失败且多行时退回逐行归一化（兼容省略空行的中转） | `backend/internal/llm/providercompat/response.go`（`normalizeSSEDataPayload`） |

### E.2 新增/更新测试

- `agent/loop_malformed_tool_call_test.go`：`TestMalformedToolCallIDIsTurnScoped`（同 turn 稳定 / 跨 turn 不碰撞 / 空 turn 区分）、`TestReActLoop_MalformedToolCallFeedbackIsBounded`（8.4KB 坏参数 → 反馈含头尾与省略标记且 <8KB、全文不出现）。
- `adapter/openai_stream_identity_test.go`（新）：3 片段拼接的 `delta_fragments=3` + `bare_literal`；id-index 冲突；name 变更；同 delta / 跨 chunk 的 modern+legacy 混写；空占位 function_call 兼容。
- `adapter/openai_stream_choice_test.go`：非 0 choice index 与缺省 index 兼容（第二波）。
- `providercompat/providercompat_test.go`：多行 `data:` 事件整体归一化（对象参数 → JSON 字符串、`[DONE]` 边界保留）；省略空行的连续单行事件仍逐行归一化。

### E.3 验证结果

```
go build ./...                                                              # ok
go test ./internal/llm/ ./internal/llm/adapter/ ./internal/llm/providercompat/ -count=1   # ok
go test ./internal/agent/ -count=1                                          # ok
```

### E.4 剩余项

| 条目 | 状态 |
|---|---|
| P1-4 路径角色契约 | **仍阻塞**：`backend/internal/toolexec/preflight.go` 正被并行会话修改（含新测试文件），现在动契约会冲突；待其收敛后另起一波 |
| P0-2 剩余（cacheanalytics `terminal_seen`/`arg_error_class`） | 未做；需要统计库列/迁移，建议与看板一起排期 |
| P0-3 剩余（同 hash 去重账本、跨层共享恢复状态、失败 usage 记账） | 未做。P0-3b 已先把「同 hash 重放次数」收敛到 ≤1 次同预算重采样；账本与共享计数属跨层状态改造，风险较高，建议单独一波并配 E2E |
| P1-5 只读分类器白名单 | 未做；`policy` 未被并行会话占用，可独立推进 |
| P2 系列 | 依赖新窗口观测数据 |

### E.5 观察指标补充（第三波）

1. 失败反馈的平均字节数（期望 ≤6KB，旧行为随坏参数线性增长）。
2. 跨 turn 的 `toolcall_` ID 碰撞数（期望 0；事件 `tool.requested.tool_call_id` 去重）。
3. `tool_call_id_index_conflict` / `tool_call_name_conflict` / `mixed_tool_call_payload` 计数（期望 0；非 0 即上游流协议异常，可精准定位 provider_error 子集）。
4. `delta_fragments` 分布（多片拼接与单片的恢复率对比，用于验证「分片本身是否相关」）。

---

## 附录 F：第四波实施记录（2026-09-28 凌晨）

第四波聚焦 P1-5：只读分类器的静态可验证形态与稳定引导。

### F.1 已实施

| 条目 | 改动 | 文件 |
|---|---|---|
| P1-5 只读流水后段 cmdlet | 新增固定形态白名单：`Select-Object -First/-Skip <非负整数字面量>`（含 `-First:5`）、`Sort-Object`（bare/开关/单个字面量属性名）、`Measure-Object`（bare/统计开关/单个属性名）、`Where-Object`（`-Property Name Op Literal` 与位置形式，Op 限 12 个比较符，Literal 限数字/单位/引号串/普通词元） | `backend/internal/policy/grants.go`（`isReadOnlyPipelineCmdlet` 等 7 个校验函数） |
| P1-5 失败段定位 | `ShellReadOnlyAssessment` 新增 `Segment/SegmentIndex`（1 起）；拒绝时回带失败段，命令级（未分段）拒绝保持无段信息 | `grants.go` |
| P1-5 拒绝文案 | 策略层错误信息追加 `(segment N: "...")`，段文本有界回显（≤120 rune）且不参与判定 | `backend/internal/policy/tool_policy.go`（`shellReadOnlySegmentHint`） |
| P1-5 分层 next_action | 白名单未覆盖 → 建议专用工具/收窄只读形态（含新 cmdlet 示例）或 `read_only=false` 子代理；敏感路径 → 说明超出只读范围；不可解析 → 建议单一朴素引号命令 | `backend/internal/toolresult/diagnostic.go` |

### F.2 安全边界（拒绝集合，测试锁定）

脚本块（`{ ... }`）、计算属性（`-Property @{...}`）、变量/子表达式（`$`/`$(...)`）、
类型转换、额外 token 与逻辑连接符、位置参数列表（逗号）、`-Last`、负数与带单位计数
（`-First 5s`）全部拒绝；变量类因命令级 `$` 检查归入 `dynamic_shell_syntax`，
其余归入 `command_not_allowlisted`，两类保持不同 next_action。
不新增对 `go build/test`、解释器、重定向、动态调用的任何放行。

### F.3 新增测试

- `policy/pipeline_test.go`：8 条正例（含大小写混用 `-GT`、位置属性形式、管道起点为 `rg`/`Get-Content`）+ 9 条负例（逐条断言稳定原因码）+ 失败段序号/内容断言 + 策略层拒绝文案含 `segment 3`。
- `toolresult/diagnostic_test.go`：`TestDiagnoseReadOnlyAllowlistMissNextAction`（白名单未覆盖与敏感路径给出不同 next_action，且都不可重试/不可覆盖）。

### F.4 验证结果

```
go build ./...                                                        # ok
go test ./internal/policy/ ./internal/toolresult/ ./internal/agent/   # ok
go test ./internal/toolkit/tools/ -run "ReadOnly|Readonly|readonly"   # ok
```

### F.5 剩余项

| 条目 | 状态 |
|---|---|
| P1-4 路径角色契约 | **仍阻塞**：`toolexec/preflight.go` 仍在并行会话改动中 |
| P2-3 broker 语义化专属码 + 版本冲突重读 | 未做（涉及重试语义，建议独立一波配 E2E） |
| P0-2 cacheanalytics 证据列、P0-3 去重账本/共享恢复状态/失败 usage 记账 | 未做 |
| P2-1/P2-2 体验项、P2-4 委派契约巡检、P2-5 取消归因、P2-6 看板 | 未做 |

---

## 附录 G：第五波实施记录（2026-09-28 凌晨，并行会话停止后）

第五波完成 P1-4「路径角色契约」主体，并补一条由本会话真实误拒触发的 P1-5 修复。

### G.1 P1-4 路径角色契约

| 条目 | 改动 | 文件 |
|---|---|---|
| 角色/归属描述 | 新增 metadata 契约：`path_roles`（`input/output/inout/workdir`，按参数名声明）与 `fs_owner`（`runtime/tool_server/browser/opaque`，缺省 runtime、未知值回退 opaque）；配套解析函数 `FSOwnerFromMetadata`/`PathRoleFromMetadata` | `backend/internal/types/metadata_helpers.go` |
| MCP 工具归属 | MCP 工具在适配层与定义层统一标记 `fs_owner=tool_server`（服务端自行解析路径），服务端自报 metadata 不能覆盖本机标记 | `backend/internal/skill/mcp_adapter.go`（FindTool/ListTools）、`backend/internal/skill/executor.go` |
| 预检不再本机探测远端路径 | `shouldPreflightPaths` 前置归属判定：非 runtime 归属直接跳过存在性检查与候选枚举（截图/快照输出误拒场景） | `backend/internal/toolexec/preflight.go` |
| 输出/工作目录角色 | 逐参数角色过滤：仅声明为 `input` 的路径参与存在性预检；`output/inout/workdir` 允许叶子不存在（写权限/父目录由沙箱负责）。角色过滤逐参数生效，输出角色不会顺带放过同调用的输入缺失 | `preflight.go`（`pathRoleSkipsExistencePreflight`） |
| 缺失 vs 无法探测 | 新增 `PathProbeResult`（exists/missing/indeterminate）与 `PathProbe` 注入点；只有 `fs.ErrNotExist` 记为 missing，权限/IO/ENOTDIR 记为 indeterminate，并使用独立错误码 `TOOL_PATH_ACCESS_FAILED`（不给候选、不自动改写） | `preflight.go`、`backend/internal/errors/codes.go` |
| 自动改写边界 | 改写只允许 runtime 归属的 `input` 角色；输出/工作目录/远端路径一律不改写 | `preflight.go`（`rewriteSafeForPathRole` + `rewritePathLikeArgs`） |
| 改写后重新校验 | 新增 `PathRewriteValidator`：自动补全路径后重新执行策略/沙箱校验；校验失败时**回滚参数**并按缺失路径拒绝（error 里带 auto-heal rejected by policy 与边界说明）。Agent 侧在 middleware 用 `AllowToolCallWithContext(ctx, ...)` 注入，ctx 贯穿 5 处调用点 | `preflight.go`、`backend/internal/agent/tool_exec_middleware.go`、`loop.go`、`approved_tool.go`、`tool_parallel_scheduler.go` |
| 权限层共享契约 | 非 runtime 归属跳过本机 workspace 拼接与沙箱路径校验（命令/URL 校验保留）；声明角色固定每参数操作语义（input→read，output/inout/workdir→write），未声明参数保持原有按工具名分类 | `backend/internal/policy/tool_policy.go` |
| 工具声明 | `write`/`append_write`/`download` 的 `file_path` 声明为 output，`edit`/`multiedit` 为 inout，`view` 显式声明 file_path/paths/files 为 input | `backend/internal/toolkit/tools/*.go` |

### G.2 P1-5 追加修复（由本会话真实误拒触发）

本会话的一个只读子代理执行 `git -C . status --short | Select-Object -First 20` 时被只读策略拒绝——
`git -C <dir>` 属于合法只读形态但当时不在白名单。修复：只读 git 分类器接受前导全局选项
`-C <dir>` 与 `--no-pager`/`--no-optional-locks`；`-c key=value` 与 `--config-env` 仍然拒绝
（可能借 pager/hook 在只读子命令下执行代码）。`backend/internal/policy/grants.go`。

### G.3 新增测试

- `toolexec/preflight_path_roles_test.go`：远端归属零探测、output/inout 叶子缺失放行且不改写、
  角色逐参数隔离、input fail-closed、权限失败独立码与无候选、`statPathProbe` 分类、
  改写拒绝非 input/远端角色、改写后校验通过/失败（回滚 + 拒绝）。
- `types/metadata_helpers_fsowner_test.go`：归属与角色解析矩阵（缺省/大小写/未知值/非字符串）。
- `policy/tool_policy_test.go`：远端归属跳过本地路径校验（runtime 同名参数仍拦截）、
  声明 output 角色按写校验（只读子目录内读放行写拦截）。
- `policy/pipeline_test.go`：`git -C . status --short`、`git --no-pager log` 正例；
  `git -c core.pager=cat log`、`git -c core.hooksPath=... status`、`git --config-env=... log` 负例。
- `skill/mcp_adapter_test.go`：MCP 工具 `fs_owner=tool_server` 标记且保留既有 metadata。

### G.4 验证结果

```
go build ./...                                   # ok
go test ./internal/... -count=1                  # 仅 internal/knowledge 失败
```

`internal/knowledge` 的失败与本波无关且未被本波改动（工作区无改动、最近提交为知识库 Phase 0 功能）：
本机 sqlite3 构建缺少 FTS5 模块（`no such module: fts5`），属于环境/驱动限制。
其余全部包通过（含 policy、toolexec、agent、toolbroker、skill、toolkit/tools、api/runtimeapi、
chat、executor、docread、ipynb、imageprep、types、toolresult、llm 等）。

### G.5 剩余项

| 条目 | 状态 |
|---|---|
| P0-2 cacheanalytics 证据列（`terminal_seen`/`arg_error_class`） | 未做（需统计库列/迁移） |
| P0-3 去重账本、跨层共享恢复状态、失败 usage 记账 | 未做（跨层状态改造，建议独立一波配 E2E） |
| P2-3 broker 语义化专属码 + 版本冲突重读 | 未做 |
| P2-1/P2-2 体验项、P2-4 委派契约巡检、P2-5 取消归因、P2-6 看板 | 未做 |
| knowledge 测试的 FTS5 环境依赖 | 与本计划无关，建议在 CI/开发环境启用 FTS5 构建或加构建标签跳过 |

---

## 附录 H：第六波实施记录（2026-09-28 凌晨）

第六波收尾 P0-3 剩余项（跨层共享恢复状态、失败 usage 记账），并补上 P0-2 的证据列
（`terminal_seen` / `arg_error_class` / 取消归因）。

### H.1 P0-3 item 3：跨层共享退化恢复配额

| 条目 | 改动 | 文件 |
|---|---|---|
| 共享配额本体 | 新增 `DegenerateRecoveryBudget`（并发安全、`Allow/Used/Max/Remaining`）、ctx 挂载 `WithDegenerateRecoveryBudget`/`FromContext`、分类器 `isDegenerateRecoveryReason` 与 `consumeDegenerateRecoveryBudget`；默认上限 3（`DefaultDegenerateRecoveryBudget`） | `backend/internal/llm/recovery_budget.go` |
| provider 层接入 | 非流式/流式重试循环在既有门控（`trackMalformedSyntaxResample`/`trackDegenerateOutputReply`）之后消耗共享配额，用尽即 `markRetryExhausted` 交回上层 | `backend/internal/llm/provider.go` |
| 网关层接入 | 网关重试循环同一处消耗共享配额，三层不再各自重放 | `backend/internal/llm/gateway_client.go` |
| agent 层接入 | `ReActLoop` 新增 run 级 `degenerateRecoveryBudget` 与 `recoveryBudget()`；`think()` 把同一实例挂到 LLM 调用 ctx；malformed 反馈与 reasoning-only 反馈两个通道都先消耗配额，用尽则发 `*_guardrail_hit`（`reason=shared_recovery_budget_exhausted`）并停止回注 | `backend/internal/agent/loop.go` |

分类边界（测试锁定）：只有「同 prompt 重放无法改变样本」的类别计费——语法类
`invalid_tool_arguments`（无截断证据）、`reasoning_only_empty_reply`、`empty_reply`。
真截断（有截断证据）继续走预算扩容路径，transport/5xx/429 等真实重试不受约束；
未挂载配额时保持旧行为（向后兼容其它调用方）。

### H.2 P0-3 item 4：失败/重放尝试的 usage 记账

| 条目 | 改动 | 文件 |
|---|---|---|
| 账本本体 | 新增 `AttemptUsageTracker`（ctx 挂载、累加、副本读取）与两个记录入口：`RecordDiscardedAttemptUsage`（流式响应体，`extractUsageFromResponseBody` 只取 provider 上报值）、`RecordDiscardedChatUsage`（非流式已解码 wire usage，全 0 不写） | `backend/internal/llm/attempt_usage.go` |
| provider 层接入 | 非流式重试循环每次失败先记录已解码 usage；流式聚合失败（含参数非法、聚合校验失败、reasoning-only）从响应体提取 usage 记账 | `backend/internal/llm/provider.go` |
| agent 层接入 | `think()` 建账本并挂 ctx；失败路径把账本用量写进 `llm.request.finished`（`usage_*` + `usage_discarded_attempts` + `usage_scope=discarded_attempts`）并作为第三返回值返回；成功路径以 `usage_discarded_*` 明细单列、返回值合并进最终 usage；`run()` 错误分支把该用量计入 `totalUsage` 与 `remainingBudget` | `backend/internal/agent/loop.go` |

口径：缓存命中率仍以最终成功那次的 `usage_*` 为准（`usage_discarded_*` 不参与
比值），run 用量/预算按「最终 + 被丢弃」的真实总消耗扣减；失败请求（无最终
usage）时 `usage_*` 直接取账本值，用量台账不再把烧掉的 token 记成 0。

### H.3 P0-2 item 4：证据列（生产者 + 记录层）

| 条目 | 改动 | 文件 |
|---|---|---|
| 记录字段 | `CacheRequestRecord` 新增 `terminal_seen`、`arg_error_class`、`cancel_source/cancel_cause/cancel_reason`（JSON 契约向后兼容，`RecordPayload` 自动带出） | `backend/internal/cacheanalytics/types.go` |
| 记录装配 | `BuildTerminalRecord` 从 `llm.request.finished` 载荷读取五个字段（缺省零值=未观测，不从状态/错误码推断） | `backend/internal/cacheanalytics/record_builder.go` |
| 生产者 | 失败路径写 `terminal_seen`（含 `terminal_finish_reason`）与 `arg_error_class`（取首个非法调用的 `parse_class`）；reasoning-only 失败标 `terminal_seen=true`；成功路径 `terminal_seen=true` | `backend/internal/agent/loop.go` |

取消归因（`cancel_*`）的记录层与载荷读取已就绪：中断兜底路径/会话结束写入
payload 后即可自动落列；chat actor 侧补齐仍是后续小步（不影响本波验收）。

### H.4 新增测试

- `llm/recovery_budget_test.go`：配额消耗/耗尽/默认值/nil 语义、ctx 往返、分类边界
  （语法类计费、真截断与 transport 类不计费、未挂载配额保持旧行为）。
- `llm/attempt_usage_test.go`：账本累加/副本/空值语义、SSE 响应体提取、无 usage
  不回退估算、非流式 wire usage 记录与全 0 跳过。
- `agent/recovery_budget_test.go`：共享配额耗尽后 malformed 反馈停止且 per-tool
  计数不推进；配额 run 级稳定复用与默认上限。
- `cacheanalytics/record_builder_context_test.go`：证据列落记录、缺失时保持未观测。

### H.5 验证结果

```
go build ./...                      # ok
go test ./internal/agent/ ./internal/llm/... ./internal/cacheanalytics/ \
       ./internal/usageanalytics/ ./internal/usageledger/ ./internal/chat/ \
       ./internal/runtimeobserve/ ./internal/toolresult/ -count=1    # 全部 ok
```

### H.6 剩余项

| 条目 | 状态 |
|---|---|
| P0-3 item 2「同请求 hash 去重账本」 | **以共享配额 + P0-3b 语义收敛**：语法类同预算重采样本身已限 1 次，跨层相乘由共享配额消除；未再引入按请求 hash 的独立账本（对 transport 类重试是负优化，按类去重已覆盖验收口径）。如需严格「同 hash 同预算重发 = 0」，可在 provider 层把 `trackMalformedSyntaxResample` 的标量换成指纹账本（改动小、风险低，但当前无观测依据支持进一步收紧）。 |
| P0-2 item 4 剩余：chat actor 侧 `cancel_*` 补齐 | 记录层与载荷读取已就绪；actor 在会话结束时把 cancel 归因写进 `llm.request.finished`（或中断兜底输入）即可闭环。 |
| P2-3 broker 语义化专属码 + 版本冲突重读 | 未做（涉及重试语义，建议独立一波配 E2E）。 |
| P2-1/P2-2 体验项、P2-4 委派契约巡检、P2-5 取消归因看板、P2-6 看板 | 未做。 |
| `internal/knowledge` 测试 | 与本计划无关：本机 sqlite3 缺 FTS5（`no such module: fts5`），建议 CI/开发环境启用或加构建标签跳过。 |
