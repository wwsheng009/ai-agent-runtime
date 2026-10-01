# Phase 5 真实会话远程 E2E 报告（交付 1/3 的生产路径验证）

- **日期**：2026-10-01
- **会话**：`session_20261001102354_qsfZLi0z`（runtime-server，`http://127.0.0.1:49747`，loopback + web 写令牌）
- **工作区**：`E:\projects\ai\ai-agent-runtime`（真实仓库，5142 个索引文件 / 5.5 万符号；同期另有 4 个会话在改同一工作区——真实噪声环境）
- **配置**：`knowledge.mode: on` + `code_tools: on`；**`watch` 未开启**（走代码默认 off）
- **模型**：`opencode.ai / deepseek-v4.1-flash`（reasoning max，permission=bypass_permissions）
- **驱动方式**：`POST /web/api/invoke`（同步远程调用，7 轮）+ `GET /web/api/turn`（turn 台账）+ 直读 `.aicli/knowledge/knowledge.db`（只读 SQLite）+ 直读会话存储 `session_history.sqlite`（工具结果信封一手证据）

## 1. 结论摘要

| # | 断言（Phase 5 交付 1 / 交付 3） | 结果 |
|---|---|---|
| A1 | 只读工具面在生产会话返回 `source=index` | ✅ `code_search` / `code_inspect` 均 index（`ObserveVersion` 行号 271–298，与库内一致） |
| A2 | **edit hook**：模型写盘后无需显式 reindex 即入库，并可被 `code_search` 命中 | ✅ 写盘 → 索引自动 +1 文件/符号 → `code_search.source=index`（行号 3） |
| A3 | **变更源 2（判定点校正）**：会话外写盘在下一个 turn 边界被发现 | ✅ 首查 `source=fallback`（`degraded=true`、`reason=no_index_hit`，grep 兜底仍给出正确路径/行号）→ turn 结束后库内已入库 → 同查询翻转为 `source=index` |
| A4 | 删除传播：软删除在一个 turn 内完成，活跃查询最终 0 命中 | ✅ 删后首查 `source=index 命中=1`（≤1 turn 残留）→ 下一轮 `source=fallback 命中=0`；库内两探针 `deleted_at` 置位、`files_active` 回到 5013 |
| A5 | 索引命中回合的端到端时延 | 观测：4.1–8.3s/轮（含模型推理）；fallback 回合 **87.1s**（13×，同轮输出 token 读数亦高，见 §4 缺口②） |

## 2. 逐轮证据（模型自述 + 一手信封 + 库状态三方对齐）

| turn | 动作 | `/web/api/turn` duration_ms | 工具结果信封（`session_history.sqlite`） | 库状态（`knowledge.db` 只读） |
|---|---|---|---|---|
| T1 | `code_search ObserveVersion` + `code_inspect` | 5543 | seq4 `source:"index"`；seq5 `source:"index"`，range 271–298 | 该符号在活跃集 |
| T2 | `write e2e_probe_20261001t0247.go` | 3961 | 写盘工具成功 | 写盘后 +1 文件/符号（edit hook → 队列 → debounce 增量） |
| T3 | `code_search ProbeEditHook…` | 4066 | seq13 `source:"index"`，path=探针文件 | 符号 `ProbeEditHook20261001T0247` 行 3 |
| T4 | 外部写盘后 `code_search ProbeExternal…` | **87085** | seq17 `source:"fallback"`、`degraded:true`、`fallback.reason:"no_index_hit"`（grep 输出含正确路径与行号 4） | 搜索时**尚未入库**（`files_active` 5014 未变） |
| T5a | 同查询再来一次 | 5623 | seq21 `source:"index"` | 已入库（`files_active` 5015，符号行 4） |
| T5b | 删除探针后立即查 | 6489 | seq21→ 之后一轮：`source:"index"`、命中 1 | 尚未校正（活跃行仍在） |
| T5c | 再一轮 | 6754 | seq27 `source:"fallback"`、`degraded:true`（`未找到匹配结果`） | 已软删除（`deleted_at` 置位，`files_active` 5013） |

> 三方对齐的意义：模型自述（`source=…`）↔ 工具结果信封（一手）↔ 库内状态（独立可验证）逐轮一致——不是"模型说它对"，而是三处证据互相印证。

## 3. 本轮验证到的生产路径

1. **edit hook 全链**（工具 ctx → `MarkChanged` → 队列 → debounce → 定向增量）在真实会话成立：模型写盘后无需任何显式动作，索引即追上，且下一轮工具查询直接命中索引口径。
2. **判定点校正（git/stat）** 在 `watch=off` 时承担外部变更发现：最坏延迟一个 turn（本轮实测：T4 搜索时未追上 → T5a 已追上），期间**正确性不降级**——fallback 明示 `degraded=true` + `no_index_hit`，grep 兜底给出正确结果。
3. **删除传播** 同口径：一个 turn 内出现一次"陈旧 index 命中"（≤1 turn 残留），随后归零并软删除；`GC` 保留期内不物理清理（设计口径）。
4. **降级协议**（04 §4.6）在生产路径生效：on 档零命中补一次 grep，信封带 `degraded`/`fallback.tool/reason/output`，模型据此仍能给出正确答案。

## 4. 登记（缺口与后续）

1. **知识层状态面未经 web/observe 暴露**：本会话无法直接读取 `knowledge` 的 mode/watch/queue 状态（`StatusReport.Watch` 已在进程内可用，但没有 HTTP 面）。建议：把知识层状态并入 `/api/runtime/observe/v1/snapshot` 或加 `/web/api/knowledge`。
2. **usage 计量口径待核**：`/web/api/turn` 在含 reasoning 的回合出现"7s 墙钟 / 13k output_tokens"这类不自洽读数（T4 的 87s / 12.7k 反而自洽）；本报告只采用墙钟与库事实，token 数不作结论。
3. **`watch=on` 场景未在本轮验证**：需在 `.aicli/runtime.yaml` 写入 `watch: on` 并重启 runtime-server；预期把 A3/A4 的"一个 turn"压缩为"一次事件"（进程内已有 0.70s 内发现的用例）。
4. **on-mode A/B（≥20 任务）与端到端 p95 统计门槛未做**：本轮为单场景真实会话验证（7 轮），不构成统计验收；三项统计门槛仍以 `acceptance_phase5_test.go` 的进程内复现为准。

## 5. 复现方式

```pwsh
# 1) 读令牌
$t = (Invoke-RestMethod http://127.0.0.1:49747/web/api/token).token
# 2) 同步远程调用（示例）
jq -n --rawfile p .tmp/e2e/p1.txt '{prompt:$p, timeout_ms:600000}' > .tmp/e2e/p1.req.json
curl.exe -sS -X POST http://127.0.0.1:49747/web/api/invoke -H "X-AICLI-Token: $t" `
  -H "Content-Type: application/json" --data-binary "@.tmp/e2e/p1.req.json" | jq -c '{status, elapsed_ms, turn_id}'
# 3) turn 台账 / 库状态
curl.exe -sS http://127.0.0.1:49747/web/api/turn | jq -c '.recent[] | {turn_id, status, duration_ms, steps}'
py -3 .tmp/kb_probe.py                 # 只读探测 knowledge.db（files/symbols/探针行）
```

驱动脚本与提示词：`.tmp/e2e/`（`invoke.ps1` / `p1..p5.txt` / `r*.json` / `extract_store.py`）。
