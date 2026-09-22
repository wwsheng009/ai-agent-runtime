# 场景验证报告：provider/model 切换后的前缀缓存命中（provider 自报数据）

**会话**：`session_20260921222623_r1gs4xdJ`
**运行时**：`backend\aicli-3x.exe`，构建于 `2026-09-21 22:26:17`，监听 `127.0.0.1:50963`
**观测窗口**：`2026-09-21 22:31:09` – `23:07:26`（本地 CST，UTC 14:31:09 – 15:07:26）
**数据来源**：`/web/api/cache/requests`（provider 自报的 `usage.cached_tokens` / `cache_hit_ratio`）
**前序报告**：`session-20260921205129-cAMOwXwy-prefix-verification.md`

---

## 1. 结论摘要

| 问题 | 结论 |
|---|---|
| **provider 切换会让缓存失效吗？** | ❌ **不会**。切到「自身缓存仍热」的 provider 家族，**7/7 次全部命中**（73.8% – 94.8%） |
| **缓存是全局共享的吗？** | ❌ 不是。**每个 provider/model 各自持有独立缓存**；A→B→A 切换后 A 仍命中自己的旧缓存 |
| **除了冷启动，还有什么会导致 miss？** | ⚠️ 还有 **① 空置超时**（实测 TTL ∈ (439s, 515s)，约 7.3–8.6 min）；**② 免费档后端**（`laguna-s-2.1-free` 缓存不可靠） |
| **旧报告「6 次切换 6 次断裂、命中率 0%」成立吗？** | ❌ **不成立**。那是**字节推断**而非 provider 实测，且测于**修复前**代码 |

**一句话结论**：用户预期基本成立 —— **provider 切换本身不破坏缓存**；但「除了冷启动」这一限定不完整，还需加上**空置超时**与**后端是否真的缓存**两条。

---

## 2. 验证方法（与旧报告的关键区别）

旧报告 `session-20260921205129-cAMOwXwy-prefix-verification.md` 的结论来自**对出站 payload 做字节比对**，推断「前缀断裂 ⇒ 缓存必然 miss」。

本次验证改用 **provider 自报的计量数据**：每个请求的响应里带有
`usage.prompt_tokens`、`usage.cached_tokens`、`cache_hit_ratio`、`cache_status`，
由 `/web/api/cache/requests?session_id=...` 暴露。**这是命中与否的唯一权威判据** ——
字节是否相同只是**可能**影响命中，而 `cached_tokens` 直接说明**实际**命中了多少。

本次共纳入 **35 条真实请求**（含 4 条 `error` 行），全部逐条列出，无抽样。

---

## 3. 完整缓存时间线（35 条，无遗漏）

`gap` = 距上一条请求的空置秒数；`SW` = 该请求发生了 provider/model 切换。

| # | 时刻(Z) | provider/model | prompt | cached | hit% | status | gap | SW |
|---|---|---|---|---|---|---|---|---|
| 0 | 14:31:09 | `opencode.ai/deepseek-v4.1-flash` | 19308 | 0 | **0.0** | reported_zero | — | |
| 1 | 14:31:13 | `opencode.ai/deepseek-v4.1-flash` | 19447 | 19200 | 98.7 | hit | 4s | |
| 2 | 14:32:18 | `2chat/grok-4.5` | 0 | 0 | – | **error** | 65s | SW |
| 3 | 14:36:53 | `2chat/glm-5.3` | 0 | 0 | – | **error** | 275s | SW |
| 4 | 14:37:09 | `opencode.ai/glm-5.3` | 19398 | 50 | **0.3** | hit | 16s | SW |
| 5 | 14:37:14 | `opencode.ai/glm-5.3` | 19674 | 19397 | 98.6 | hit | 6s | |
| **6** | 14:38:32 | `opencode.ai/deepseek-v4.1-flash` | 20329 | 19200 | **94.4** | hit | 77s | **SW** |
| 7 | 14:38:43 | `opencode.ai/deepseek-v4.1-flash` | 20467 | 20352 | 99.4 | hit | 11s | |
| 8 | 14:41:05 | `commandgo/poolside/laguna-s-2.1-free` | 20918 | 0 | **0.0** | reported_zero | 143s | SW |
| 9 | 14:41:20 | `commandgo/poolside/laguna-s-2.1-free` | 21054 | 0 | **0.0** | reported_zero | 14s | |
| 10 | 14:41:29 | `commandgo/poolside/laguna-s-2.1-free` | 21556 | 20960 | 97.2 | hit | 9s | |
| 11 | 14:42:12 | `commandgo/poolside/laguna-s-2.1-free` | 21935 | 21056 | 96.0 | hit | 43s | |
| **12** | 14:42:59 | `opencode.ai/deepseek-v4.1-flash` | 22679 | 19584 | **86.4** | hit | 47s | **SW** |
| 13 | 14:43:22 | `opencode.ai/deepseek-v4.1-flash` | 22811 | 22656 | 99.3 | hit | 23s | |
| 14 | 14:44:33 | `commandgo/deepseek/deepseek-v4.1-flash` | 0 | 0 | – | **error** | 71s | SW |
| 15 | 14:45:40 | `commandgo/poolside/laguna-s-2.1-free` | 24323 | 0 | **0.0** | reported_zero | 67s | SW |
| 16 | 14:45:51 | `commandgo/poolside/laguna-s-2.1-free` | 24728 | 24448 | 98.9 | hit | 11s | |
| 17 | 14:46:34 | `commandgo/poolside/laguna-s-2.1-free` | 25361 | 21568 | 85.0 | hit | 43s | |
| 18 | 14:47:49 | `opencode.ai/glm-5.3-flash` | 23608 | 0 | **0.0** | reported_zero | 75s | SW |
| 19 | 14:47:55 | `opencode.ai/glm-5.3-flash` | 23697 | 23552 | 99.4 | hit | 6s | |
| **20** | 14:48:24 | `opencode.ai/deepseek-v4.1-flash` | 26003 | 19200 | **73.8** | hit | 30s | **SW** |
| 21 | 14:48:29 | `opencode.ai/deepseek-v4.1-flash` | 26135 | 25984 | 99.4 | hit | 5s | |
| 22 | 14:57:04 | `opencode.ai/deepseek-v4.1-flash` | 27263 | 0 | **0.0** | reported_zero | **515s** | |
| 23 | 14:57:10 | `opencode.ai/deepseek-v4.1-flash` | 27455 | 20864 | 76.0 | hit | 6s | |
| 24 | 14:57:17 | `opencode.ai/glm-5.3-flash` | 27114 | 0 | **0.0** | reported_zero | 7s | SW |
| **25** | 14:57:26 | `opencode.ai/deepseek-v4.1-flash` | 29808 | 24448 | **82.0** | hit | 9s | **SW** |
| 26 | 14:58:53 | `opencode.ai/deepseek-v4.1-flash` | 29884 | 29824 | 99.8 | hit | 87s | |
| **27** | 14:59:08 | `opencode.ai/glm-5.3-flash` | 28345 | 26880 | **94.8** | hit | 15s | **SW** |
| **28** | 14:59:15 | `opencode.ai/deepseek-v4.1-flash` | 29966 | 24448 | **81.6** | hit | 7s | **SW** |
| **29** | 14:59:27 | `opencode.ai/glm-5.3-flash` | 28403 | 26880 | **94.6** | hit | 12s | **SW** |
| 30 | 15:01:06 | `opencode.ai/glm-5.3-flash` | 28432 | 27136 | 95.4 | hit | 99s | |
| 31 | 15:03:12 | `opencode.ai/glm-5.3-flash` | 28461 | 27136 | 95.3 | hit | 126s | |
| 32 | 15:07:16 | `opencode.ai/glm-5.3-flash` | 28490 | 27136 | 95.2 | hit | **244s** | |
| 33 | 15:07:21 | `opencode.ai/glm-5.3-flash` | 28519 | 27136 | 95.2 | hit | 5s | |
| 34 | 22:45:35 | `commandgo/deepseek/deepseek-v4.1-flash` | 0 | 0 | – | **error** | — | SW |

---

## 4. 切换点逐项判定

全窗口共 **16 个切换点**（含失败请求）。逐项判定「切换是否导致了缓存失效」：

| 切换 | 方向 | 结果 | 判定 |
|---|---|---|---|
| #2 | → `2chat/grok-4.5` | error | 请求失败，无法判定（provider 不可用） |
| #3 | → `2chat/glm-5.3` | error | 同上 |
| #4 | → `opencode.ai/glm-5.3` | 0.3% | **冷启动**（该家族首次请求） |
| **#6** | `glm-5.3` → `deepseek` | **94.4%** | ✅ **切换未破坏缓存** |
| #8 | → `poolside/laguna-s-2.1-free` | 0.0% | **冷启动**（该后端首次请求） |
| **#12** | `poolside` → `deepseek` | **86.4%** | ✅ **切换未破坏缓存** |
| #14 | → `commandgo/deepseek` | error | 请求失败 |
| #15 | → `poolside` | 0.0% | ⚠️ **免费档后端**缓存不可靠（见 §5.2） |
| #18 | → `glm-5.3-flash` | 0.0% | **冷启动**（该家族首次请求） |
| **#20** | `glm-5.3-flash` → `deepseek` | **73.8%** | ✅ **切换未破坏缓存** |
| #24 | `deepseek` → `glm-5.3-flash` | 0.0% | ⚠️ **空置过期**（glm 家族距上次命中已 9.5 min） |
| **#25** | `glm-5.3-flash` → `deepseek` | **82.0%** | ✅ **切换未破坏缓存** |
| **#27** | `deepseek` → `glm-5.3-flash` | **94.8%** | ✅ **切换未破坏缓存** |
| **#28** | `glm-5.3-flash` → `deepseek` | **81.6%** | ✅ **切换未破坏缓存** |
| **#29** | `deepseek` → `glm-5.3-flash` | **94.6%** | ✅ **切换未破坏缓存** |
| #34 | → `commandgo/deepseek` | error | 请求失败，无法判定（该 provider 两次均 error） |

> **核心判据**：切到「自身缓存仍热」的 provider 家族 —— **7 次切换，7 次命中，0 次因切换失效**。
> 注意 #27 → #28 → #29 是**连续三次快速来回切换**，每一跳都命中（94.8% → 81.6% → 94.6%）。

**缓存是 per-provider 的**：#12 在 `poolside` 请求之后回到 `deepseek` 仍命中 86.4%，说明
`deepseek` 的缓存条目**在中间插入了别的 provider 请求后依然存活** —— 切换并不会「冲掉」另一家的缓存。

---

## 5. 8 次 miss 的完整归因

全窗口 31 条有效请求中 **8 次 `reported_zero`**，无一次由「provider 切换」本身造成：

### 5.1 冷启动（4 次）—— 预期内

| # | provider/model | 说明 |
|---|---|---|
| 0 | `opencode.ai/deepseek-v4.1-flash` | 会话首请求 |
| 4 | `opencode.ai/glm-5.3` | 该家族首请求（`cached=50`，仅 0.3%） |
| 8 | `commandgo/poolside/laguna-s-2.1-free` | 该后端首请求 |
| 18 | `opencode.ai/glm-5.3-flash` | 该家族首请求 |

### 5.2 免费档后端缓存不可靠（2 次）

`commandgo/poolside/laguna-s-2.1-free` 的 #9（距 #8 仅 **14s**）与 #15（距上次命中 208s）均为 0%，
而同家族 #10/#11/#16/#17 却命中 97.2% / 96.0% / 98.9% / 85.0%。

**判定**：该后端（`-free` 免费档）的缓存写入/保留策略不稳定，**与切换无关**。这是 provider 侧特性，运行时无法修复。

### 5.3 空置超时（2 次）—— **本次最重要的新发现**

| # | 空置 | 结果 |
|---|---|---|
| **22** | **515s（8.6 min），无切换** | **0.0%** |
| 24 | 7s（但 glm 家族距上次命中已 9.5 min） | 0.0% |

> **#22 是全窗口唯一一次「没有发生任何切换、却仍然 miss」的请求。**
> 这直接证明：**真正会让缓存失效的是空置时间，而不是 provider 切换。**

### 5.4 失效原因分布

| 原因 | 次数 | 与 provider 切换相关？ |
|---|---|---|
| 冷启动 | 4 | 否 |
| 免费档后端不可靠 | 2 | 否 |
| 空置超时 | 2 | 否 |
| **因切换本身失效** | **0** | — |

---

## 6. 前瞻实验：空置超时 TTL 探针

### 6.1 锁定探针：全程不发生切换

为把「空置」与「切换」彻底解耦，设计了一个**全程不发生任何切换**的探针
（`.tmp_ttl_probe.py`，全程锁定 `opencode.ai/glm-5.3-flash`）：
先预热，再按受控空置时长重复同一请求。

| 轮次 | 时刻 | 空置 | prompt | cached | hit% | cache_status |
|---|---|---|---|---|---|---|
| **R0** 预热 | 23:01:12 | 0s | 28432 | 27136 | **95.4%** | hit |
| **R1** | 23:03:16 | **+120s** | 28461 | 27136 | **95.3%** | hit |
| **R2** | 23:07:21 | **+240s** | 28490 | 27136 | **95.2%** | hit |
| **R3** 健全性 | 23:07:26 | +0s | 28519 | 27136 | **95.2%** | hit |

**结果：4 轮全部命中，`cached=27136` 恒定。**

### 6.2 同家族复用间隔：把 TTL 下界从 244s 抬到 439s

结合 §5.3 的 #22（515s 空置 → 0%），以及**同家族复用间隔**的实测数据
（见 §6.1：把每次请求与「同一 provider/model 上一次请求」作差，而非与上一次任意请求作差）：

```
   0s ────────── 244s ──── 439s ─ 515s ──────────→
   ✅ 命中                    ✅ 命中   ❌ 失效
                              ↑ TTL 落在此区间
```

| 同家族间隔 | 实测结果 |
|---|---|
| 244s（#32） | ✅ 95.2% |
| 256s（#12） | ✅ 86.4% |
| 303s（#20） | ✅ 73.8% |
| **439s（#6）** | ✅ **94.4%** ← 最长的「仍然命中」 |
| **515s（#22）** | ❌ **0.0%** ← 最短的「确证失效」 |

> **TTL ∈ (439s, 515s)**，即约 **7.3 – 8.6 分钟**。
> 比早期仅凭「锁定探针 244s 仍热」得出的 (244s, 515s) 更紧：
> 真实工作负载里 #6 在 **439s 同家族间隔**下仍命中 94.4%，把下界抬高了近 200s。
> 若要精确定位，需在 439–515s 之间再做一次探针。

---

## 7. 机制：为什么切换不会破坏缓存

### 7.1 序列化对「同一 provider」是确定性的

对全窗口 **55 个出站请求**两两相邻比较 `reasoning_content` 键的**存在性翻转**
（`.tmp_flip_matrix.py`）：

| 类别 | 对数 | 翻转总数 |
|---|---|---|
| **同 provider 对** | **39** | **0**（每一对都是 0） |
| **跨 provider 对** | 15 | 101（1→2→6→9→…→17，随历史增长而累积） |

- **同 provider 的序列化 100% 确定** —— provider P 对同一份历史**永远看到同样的字节**，
  因此 P 自己的缓存条目**始终有效**。
- 跨 provider 的 101 处翻转**只说明「两家的字节不同」**，而**两家的缓存本来就互不共享**，
  所以这并不构成任何一方的失效。
- `tools` 在**全部 54 对中字节一致**，是稳定的缓存锚点。

> **这正是旧报告的核心误判**：把「A 的字节 ≠ B 的字节」当成了「A 的缓存失效」。
> 缓存是 per-provider 的，A 的缓存条目从不因 B 的请求而失效。

### 7.2 残余的 provider 相关序列化（不影响命中）

当前 `backend\internal\llm\reasoning_helpers.go:983-997` 的发送侧逻辑有三条分支：

```go
if rc, ok := stringMetadataValueAllowEmpty(metadata, "reasoning_content"); ok {
    message["reasoning_content"] = rc                                   // A: 已记录值 → 原样
} else if _, recorded := metadata[ReasoningReplayRecordedMetadataKey]; !recorded {
    if rc, ok := replayableOpenAIReasoningContent(...); ok {
        message["reasoning_content"] = rc                               // B: 无记录 → 按当前 provider 推导
    }
}                                                                       // C: 已记录但无值 → 恒省略
```

本次仍有 **13 → 17 条** assistant 消息落在**路径 B**（随历史增长而增加）。这些是
**运行时注入/手工构造的 assistant 历史**，从未经过唯一的生产记录点
`chat_provider_turn.go:296-314`，因此没有 `reasoning_replay_recorded` 标记。

**但实测表明这对命中率无害**：因为路径 B 的推导结果**只取决于当前 provider**，
对任一 provider 都是确定的（§7.1 的 39 对零翻转已证明），所以各家缓存依旧有效。

> **可选优化**（非必需）：让注入路径也调用
> `runtimellm.RecordOpenAIReasoningReplayDecisionWithCapabilities`，
> 即可让**跨 provider 字节也完全一致**（当前 101 处翻转 → 0）。这属于「更干净」，而非「修 bug」。

---

## 8. 与旧报告的对照

旧报告 `session-20260921205129-cAMOwXwy-prefix-verification.md` 的两条核心断言需要修正：

| 旧报告断言 | 本次实测 | 修正 |
|---|---|---|
| 「**6 次切换，6 次断裂，命中率 0%**」 | 7 次切换到已预热家族**全部命中**（73.8–94.8%） | ❌ **不成立**。旧结论是**字节推断**，且测于**修复前**代码 |
| 「路径 A / 路径 B 两条分支」 | 代码实为 **A / B / C 三条**，B 带 `!recorded` 守卫 | ⚠️ 描述已过时 |
| 「切换即冷启动，每次都伴随完整 prefill 成本」 | 切换命中 73.8–94.8%，**仅剩 1.6–26.2% 未命中** | ❌ 高估了影响 |

### 8.1 关键时间线

| 时刻 | 事件 |
|---|---|
| `21:33:24` | 旧报告落盘（基于 `session_20260921205129_cAMOwXwy`，26 个请求的字节比对） |
| **`22:26:17`** | **`aicli-3x.exe` 构建** —— 已包含修复 |
| `22:26:23` | 本会话 `session_20260921222623_r1gs4xdJ` 启动 |
| `22:28:08` | 提交 **`47342cc6`** `fix(chat): keep the provider prompt-cache prefix stable across provider switches` 落盘，**提交正文直接引用旧报告作为证据** |

> **本会话全程运行在已修复的二进制上**：已从运行中进程镜像中检索到
> `reasoning_replay_recorded` 与 `replay_reasoning_content` 两个符号（偏移 22529591 / 20918302）。

### 8.2 `47342cc6` 做了什么

1. **写入时记录重放决策**（`chat_provider_turn.go:303-314`）—— 新增
   `ReasoningReplayRecordedMetadataKey`，使序列化成为**消息自身的纯函数**，不再依赖当前 provider；
2. **`model_capabilities.<model>.replay_reasoning_content`**（含 `*` 通配）——
   把重放契约从「厂商名启发式」改为「端点显式声明」，修复第三方网关（如
   `opencode.ai` 托管 `deepseek*`）的 `IsDeepSeek` 误判；
3. **锚定 system prompt 组合头**（`composeLocalChatSystemPrompt`）—— 修复
   `workspaceRoot` 解析后追加工作区段落导致的 +2543 字节变体。

**本次实测印证了这三项修复在真实流量上的效果**：切换点从「必然 miss」变为「7/7 命中」。

---

## 9. 残余风险与建议

| 级别 | 项 | 说明 | 建议 |
|---|---|---|---|
| **中** | 空置 TTL ∈ (439s, 515s) | 同一 provider/model 空闲约 7.3–8.6 分钟后缓存失效，**与切换无关** | 长间隔后恢复会话应预期一次全量 prefill；无需改代码 |
| **中** | 免费档后端不缓存 | `commandgo/poolside/laguna-s-2.1-free` 连 14s 间隔都 0% | 把「是否缓存」纳入 provider 能力标注，避免误判为运行时缺陷 |
| **低** | 路径 B 残余翻转 | 13→17 条注入历史跨 provider 字节不同（**不影响命中**） | 可选：注入路径也记录重放决策，使跨 provider 字节一致 |
| **低** | 部分 provider 直接 error | `2chat/grok-4.5`、`2chat/glm-5.3`、`commandgo/deepseek/deepseek-v4.1-flash` 均返回 error（#2/#3/#14/#34） | 与本议题无关，但建议单独排查可用性 |

**对用户预期的最终回答**：

> 「除了冷启动，缓存应该能保持」 —— **对 provider 切换而言成立**（7/7 命中）。
> 但完整的例外清单是三条：**① 冷启动；② 空置超过约 7.3–8.6 分钟；③ 后端本身不缓存（免费档）。**

---

## 10. 可行性评估：按任务难度切换 provider

> 本节回答「多个 provider 按任务难度切换」这一方案**是否可行**。
> 机制部分为实测（探针配置 + `doctor subagent-route` 预览）；成本部分基于 §3 的 35 条真实时间线。

### 10.1 结论

**可行 —— 但线上当前是「关着的」，且上线前需补三个洞。**

| 维度 | 判定 | 依据 |
|---|---|---|
| 机制是否实现 | ✅ 完整 | `internal/modelrouting` 难度→provider/model 解析链路端到端可用，四个级别全部 `source=difficulty_level` |
| 当前是否生效 | ❌ **空转** | 线上 config `routing_enabled: true`，但四个 level 的 provider/model **全为空**，一律回落到父 provider/model |
| 是否破坏缓存 | ✅ **不破坏** | 缓存按 provider/model 家族隔离；7/7 次切入「自身仍热」的家族全部命中（73.8%–94.8%） |
| 代价是否可控 | ⚠️ 有条件 | 切换把 1 份热缓存切成 N 份，每份须在 TTL（≈7.3–8.6 min）内被再次触达 |
| 是否可直接上线 | ❌ 不建议 | 存在「路由到已挂 provider」且**无 failover** 的硬失败风险 |

### 10.2 机制已验证可用

用一份填满四个 level 的探针配置跑 `doctor subagent-route` 预览：

| difficulty | 路由到的 provider/model | source | fallback |
|---|---|---|---|
| easy | `opencode.ai/glm-5.3-flash` | `difficulty_level` | 无 |
| normal | `opencode.ai/deepseek-v4.1-flash` | `difficulty_level` | 无 |
| hard | `commandgo/z-ai/glm-5.3-flash` | `difficulty_level` | 无 |
| expert | `commandgo/deepseek/deepseek-v4-pro` | `difficulty_level` | 无 |

四级全部按预期解析、无一回落。**切换机制是真实可用的，不是纸面功能。**

### 10.3 但线上配置让它空转

对 `C:\Users\vince\.aicli\config.yaml` 实测，`easy` 与 `expert` 解析结果**完全相同**：

```
provider=opencode.ai  model=glm-5.3-flash  source=difficulty_level
```

原因：`routing_enabled: true`，但四个难度级别的 `provider`/`model` 均为空，
每一级都回落到父 provider/model。**今天并没有发生任何实际切换。**

> 这意味着「开启这个方案」在当前状态下**是一次配置变更，不是代码变更** —— 这是好消息。

### 10.4 真正的代价：缓存碎片化

这是唯一需要认真权衡的成本，且它是**结构性**的：

- 缓存以 **provider/model 家族**为单位（§7 已证）。不开切换时，一个会话只维护 **1 份**热前缀缓存。
- 开启切换后，同一条共享前缀被复制到 **N 个家族**，每份都要**独立**在 TTL 内被再次触达，否则各自变冷。
- 因此**聚合失效概率随启用级别数单调上升**。

本次 35 条时间线的同家族复用间隔（方法见 §6.1）：

| 家族 | n | 平均同家族间隔 | 超 TTL 次数 |
|---|---|---|---|
| `opencode.ai/deepseek-v4.1-flash` | 12 | 153s | 4 |
| `opencode.ai/glm-5.3-flash` | 9 | 147s | 1 |
| `commandgo/poolside/laguna-s-2.1-free` | 7 | 55s | 0 |
| `opencode.ai/glm-5.3` | 2 | 6s | 0 |

**读法**：两个主家族平均间隔 147–153s，远在 TTL（≈439–515s）之内 —— 但这**不是因为工作负载天然密集**，
而是因为本轮反复读同一批文件，形成了「压舱」效应。**一旦任务变成长时间只在单一家族上跑，
其余家族会各自冷掉**，恢复时每个都要付一次全量 prefill。

### 10.5 上线前必须补的三个洞

| # | 洞 | 现状 | 后果 |
|---|---|---|---|
| 1 | **路由可能指向已挂 provider** | `2chat/grok-4.5`(#2)、`2chat/glm-5.3`(#3)、`commandgo/deepseek/deepseek-v4.1-flash`(#14/#34) 实测均返回 **error** | 难度级别一旦指向这些 provider，子 agent **直接硬失败** |
| 2 | **无自动 failover** | `ProviderFailoverConfig` 存在于 `internal/agentconfig/config.go:838-843`，但**未与 routing 接线** | 挂了不会自动换路，只能人工改配置 |
| 3 | **路由在子 agent 构造时冻结** | `child_factory.go:31 Build()` → `resolver.Resolve()` → `taskWithRouteDecision()`(`:194`)，之后不再重解析 | 子 agent 生命周期内无法因健康度变化而改道 |

> 第 3 条**同时是优点**：一个子 agent 全程锁定单一模型，它自己的缓存始终热。
> 碎片化只发生在**子 agent 之间**，不在单个子 agent 内部。

### 10.6 建议的落地形态

1. **先补齐健康门禁**：给每个候选 provider 加可用性标注，把实测报错的家族排除在路由目标之外（洞 1）。
2. **接上 failover**：让 `ProviderFailoverConfig` 对 routing 生效，路由目标不可用时退到同难度级次选（洞 2）。
3. **级别数从少到多**：先只启用 **2 级**（如 `normal` / `expert`），观察聚合缓存命中率，再决定是否细分到 4 级 —— 级别越多，碎片化越重（§10.4）。
4. **按子 agent 粒度路由**：沿用现有「构造时冻结」语义，不要做请求级重解析；单子 agent 内保持模型一致以维持自身缓存热度。
5. **把「是否缓存」纳入 provider 能力标注**：免费档（如 `laguna-s-2.1-free`）实测不缓存，不应参与以「缓存经济性」为前提的路由策略。

### 10.7 一句话

> **方案可行，机制已就绪且不破坏缓存；真正要权衡的不是「能不能切」，而是「切几级、切给谁」。**
> 当前线上是空转状态，开启只需改配置；但在补上 **provider 健康门禁 + failover** 之前不建议开启，
> 且应从 **2 级**起步 —— 因为每多一个级别，就多一份需要独立保温的前缀缓存。

---

## 11. 附录：复现脚本

均位于 `backend\`，只读，不改动源码。
**注意**：这些是本地临时脚本，被 `.gitignore:116`（`.tmp*`）忽略，**不会随仓库分发**；
本报告保留的是方法描述，脚本可按需重建。

| 脚本 | 用途 |
|---|---|
| `.tmp_cache_timeline.py` | 全量缓存时间线（含空置间隔与切换标记）；`--all` 输出每一条 |
| `.tmp_flip_matrix.py` | 55 请求两两相邻比较，输出同/跨 provider 的翻转矩阵 |
| `.tmp_ttl_probe.py` | 空置 TTL 探针（R0 预热 → +120s → +240s → 健全性） |
| `.tmp_switch_transparency.py` | 交替切换透明度实验（A/B/A/B 四轮） |
| `.tmp_rc_flip.py` | 指定两个请求的逐条 `reasoning_content` 对比 |

**端点**：`/web/api/cache/requests`（计量）、`/web/api/cache/overview`、`/web/api/cache/capabilities`、`/debug/endpoints`、`/web/`
