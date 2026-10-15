# 缓存行为实测：system prompt guidance 变化 vs tools 变化（2026-10-15）

> 工具：`backend/cmd/cache-probe`（真实 provider 请求；复现：`go run ./cmd/cache-probe -model cn:glm-5.3-flash`，可配 `CACHE_PROBE_NONCE` 控制缓存身份）
> Provider/模型：workbuddy（本地网关 127.0.0.1:7863）→ `cn:glm-5.3-flash`（智谱 GLM 代理，块前缀缓存，usage 返回 `prompt_tokens_details.cached_tokens`）
> 方法：7 轮真实请求，控制变量——固定 1554 tok 级 system 前缀（稳定头部 + 可切换 guidance 尾段）+ 2~3 个 tools；只改目标变量。

## 实测数据（冷缓存会话，nonce 固定）

| 轮 | 内容变化 | prompt | cached | 命中率 |
| --- | --- | ---: | ---: | ---: |
| 1 冷启动 | — | 1554 | 0 | 0%（新身份，缓存空） |
| 2 相同重发 | 无 | 1554 | 1536 | **98.8%** |
| 3 换 guidance 尾段 | system 变 | 1575 | 0 | **0%（首发 miss）** |
| 3b 重发 r3 | 无 | 1575 | 1536 | 97.5% |
| 4 追加 1 个 tool | tools 变 | 1670 | 0 | **0%（首发 miss）** |
| 4b 重发 r4 | 无 | 1670 | 1664 | **99.6%** |
| 5 回到原 A 前缀 | system 变回 | 1554 | 1536 | 98.8%（旧条目存活） |

热缓存复跑（同进程第二遍）所有轮直接命中，证实缓存写入在"变化后首次请求"完成。

## 结论

1. **"tools 追加不伤缓存、system 改动会清缓存"在该 provider 上不成立**：tools 序列参与前缀缓存哈希，追加 tool 与改 system 尾段**同为一次首发 miss**，代价对称；
2. **失效是一次性的，不是持续性的**：变化后首个请求全价，紧接着的重发立即恢复 ~99% 命中（缓存写入发生在变化后的首次请求上）；
3. **旧前缀条目不互逐**（内容寻址）：A/B 两个 guidance 变体来回切换，各自的条目都在，切换回来立即命中；
4. **对本功能（guidance gating）的含义**：
   - guidance 撤段只发生在策略真正收窄时（profile 切换/子代理构建），且与 tools 数组**同步变化**——**同一次 miss**，不产生第二笔缓存失效；
   - 日常请求（内容相等短路，前缀无变化）持续 ~99% 命中；
   - 代价模型：**每次策略变更 ≈ 一次全价 input 请求**（本例 ~1.6k tok 级），与变更的"方向"（收窄/扩展/prompt/工具）无关。

## 对方案的影响

- `progressive-tool-disclosure-plan.md` §5（缓存守则）与 §4.6（时序）的判断得到实测支持：**"会话内零重注册"的红线依然正确**——避免的不是"system 不可变"，而是"频繁、无意义的前缀抖动"；策略变更驱动的同步变化（tools + system 一次完成）是可接受的；
- 无需为此功能增加额外缓存保护机制；`syncChatSystemPromptMessage` 的内容相等短路已保证稳态零改写。

## 复现

```powershell
cd backend
# 冷缓存对照（每轮变化的首发 miss 可见）
$env:CACHE_PROBE_NONCE = "my-session-id"; go run ./cmd/cache-probe -model cn:glm-5.3-flash
# 热缓存对照（全部命中）
$env:CACHE_PROBE_NONCE = "my-session-id"; go run ./cmd/cache-probe -model cn:glm-5.3-flash
```

注意：provider 侧缓存条目有存活期（本次实测跨进程 >10 分钟仍命中）；不同 provider/模型的块大小与门槛可能不同（首轮 455 tok 时全部 miss，阈值在 455~1536 tok 之间）。
