# Provider 响应标记剥离方案（preset 配置 + 模型级匹配）

- 日期：2026-09-09
- 范围：`backend/internal/agentconfig`、`backend/internal/llm`（`gateway_client.go`、`provider.go`、`providercompat`、adapter）、presets 机制
- 背景：`session_20260909075710_7kjQXObC` 执行失败分析（证据：`~/.aicli/chat-logs/2026/09/09/20260909_075709_938_478f9524.http/`、`session_history_replica.sqlite`）
- 状态：设计与实施建议；本文不包含代码改动

## 1. 结论摘要

> **通过 presets.yaml / config.yaml 声明式配置“响应标记剥离规则”，规则按 provider + 模型 glob（如 `*minimax*`）匹配；流式在 providercompat 的 `NormalizeStreamChunk` 单点、非流式在两处 body 剥离，均在 adapter 解析之前完成过滤。**

推荐：

- 新增 provider 级配置 `response_marker_rules`：规则列表，每条规则由 `models`（glob，可省略表示全模型）与 `markers`（字面量标记列表）组成；
- 规则通过既有 presets 深合并机制下发，内置 preset 默认覆盖已知问题模型族（`*minimax*`）；
- **剥离点在 providercompat 单点**：流式走 `Chain.NormalizeStreamChunk`（两个客户端 gateway_client / provider_wrapper 的流式入口都已汇聚于此），非流式在两处 `ReadAll` 之后对 body 做字节剥离（gateway_client.go:639、provider.go:736）；
- 匹配语义复用 `path.Match`（大小写不敏感），与 `internal/modelcard/match.go` 既有模式一致。

不建议：

- 在 adapter 的 `HandleResponse` 之后再清洗 `assistantMsg`——此时 `<tool_call>` markup 解析已经失败（工具名已变成 `]<]minimax[>[<invoke name="grep">...`），无法修复；
- 把剥离逻辑写进 `parseToolCallMarkupContent` / `parseToolCallMarkupBlock` 的全局 if 分支；
- 只覆盖 `gateway_client.go` 一处客户端——本次事故的流量 source 是 `provider_wrapper`（ProviderWrapper），两个客户端都要生效；
- 用正则表达式作为默认标记语法（`]<]minimax[>[` 本身含 `[]<>` 元字符，字面量更安全）；正则作为后续扩展项。

## 2. 问题与失败机理（背景）

`session_20260909075710_7kjQXObC` 的第 5 轮起，opencode.ai 网关 → minimax-m3 的 SSE 流内出现字面量标记 `]<]minimax[>[`，且被插进 `<tool_call>` 块内每个 token 之间：

```
:]<]minimax[>[<tool_call>
]<]minimax[>[<invoke name="grep">]<]minimax[>[<path>C:\Users\vince\.aicli\chat-logs]<]minimax[>[</path>...
```

后果链：

1. `parseToolCallMarkupBlock`（`internal/llm/adapter/openai.go:371`）把第一个 `<arg_key>` 之前的全部文本当工具名 → 工具名为 `]<]minimax[>[<invoke name="grep">...`；
2. 工具查表失败 → `TOOL_NOT_FOUND`（`retryable=false`），该会话累计 9 次；
3. 污染内容写入历史并随后续请求回传 → 模型持续模仿该格式 → 失败级联。

**关键事实：该会话请求捕获文件（`*.http/00X_request_provider_wrapper.json`）的 `source` 为 `provider_wrapper`，即流量走 `ProviderWrapper`（`internal/llm/provider.go`），不是 GatewayClient。剥离逻辑必须对两个客户端同时生效。**

标记仅出现在部分模型（minimax 系），同一 provider 的其他模型不受影响，因此需要**模型级匹配**而非只按 provider 一刀切。

## 3. 配置模型

### 3.1 Provider 配置结构

`internal/agentconfig/config.go` 的 `Provider` 新增：

```go
// ResponseMarkerRule 描述一条响应标记剥离规则：models 为模型 glob
// 匹配（path.Match 风格，大小写不敏感，省略表示匹配全部模型），
// markers 为该类模型响应流中需要剥离的字面量标记。
type ResponseMarkerRule struct {
    Models  []string `yaml:"models" mapstructure:"models" json:"models"`
    Markers []string `yaml:"markers" mapstructure:"markers" json:"markers"`
}

// Provider 内新增字段
ResponseMarkerRules []ResponseMarkerRule `yaml:"response_marker_rules" mapstructure:"response_marker_rules" json:"response_marker_rules"`
```

语义：

- `models` 为空/省略 → 匹配该 provider 下全部模型（兜底）；
- `models` 为 glob 列表 → 任一匹配即生效（OR 语义）；
- 同一 provider 下多条规则 → 全部命中规则的 markers 取并集；
- 匹配对象为 **effective model**（`model_mappings` 解析之后的模型名，即 `gateway_client.go` 中 `adapterRequest.Model` 对应的值），避免用户在 `*minimax*` 与映射后别名之间产生歧义。

### 3.2 匹配函数

新增 `internal/agentconfig`（或复用 `modelcard`）工具函数，语义对齐 `modelcard.matchPatternFold`：

```go
// MatchResponseMarkerRule 判断规则是否命中指定模型。
// 大小写不敏感；支持 path.Match glob（* ? [ ]）。
func MatchResponseMarkerRule(rule ResponseMarkerRule, model string) bool
```

`models` 为空 → true；否则对任一 pattern 做 `path.Match`（大小写归一后）。

### 3.3 presets.yaml 内置预设示例

```yaml
presets:
  - name: opencode-gateway-minimax-marker-cleanup
    enabled: true
    config:
      providers:
        items:
          opencode.ai:
            response_marker_rules:
              - models: ["*minimax*"]
                markers: ["]<]minimax[>["]
```

用户侧两种覆盖方式：

- 在 `~/.aicli/presets.yaml` 重声明同名 preset（`enabled: true/false`、追加模型族）；
- 直接在自己的 `config.yaml` 写 `providers.items.opencode.ai.response_marker_rules`（用户 config 优先级最高，覆盖 preset 层——注意 slice 是整体替换，覆盖即需重列全部条目，见 §6 合并语义）。

预设默认值建议（随时间补全）：

| provider | models | markers |
|---|---|---|
| opencode.ai | `*minimax*` | `]<]minimax[>[` |

### 3.4 可选的全局兜底层

如需跨 provider 兜底，可增加 `providers.response_marker_rules` 全局字段，与 `providers.headers` 的合并语义一致（`EffectiveProviderHeaders` 同款合并：全局为底、provider 级覆盖并集）。**一期可不做**，先用 provider 级 + 内置 preset 覆盖已知站点。

## 4. 运行时链路

```
config.yaml / presets.yaml
        │ 深合并（既有 preset 机制，无需改动）
        ▼
Provider.ResponseMarkerRules（agentconfig）
        │ 构造点复制（与 CompatibilityProfile 同模式）
        ▼
ProviderConfig.ResponseMarkerRules（internal/llm/provider.go）
        │ 进入 providercompat 前解析：ResolveResponseMarkers(rules, adapterRequest.Model) -> []string
        ▼
providercompat.Context.ResponseMarkers
        │
        ├── 流式（4 个站点汇聚）：Chain.NormalizeStreamChunk 内对
        │     delta.content / reasoning_content 做标记剥离
        │
        └── 非流式（2 处）：ReadAll 之后、HandleResponse 之前对 body 做字节剥离
        ▼
adapter（markup 解析 / 内容累积 / 回调 / 历史）全部拿到干净内容
```

### 4.1 HandleResponse 调用点全量清单（两个客户端，7 处）

| 客户端 | 文件:行 | 形态 | 覆盖方式 |
|---|---|---|---|
| GatewayClient | `gateway_client.go:639` | 非流式 | body 字节剥离（capture 后、HandleResponse 前） |
| GatewayClient | `gateway_client.go:872` | 流式（同步） | `NormalizeStreamChunk`（已汇聚） |
| GatewayClient | `gateway_client.go:1167` | 流式（channel 变体） | `NormalizeStreamChunk`（已汇聚） |
| ProviderWrapper | `provider.go:736` | 非流式 | body 字节剥离（capture 后、HandleResponse 前） |
| ProviderWrapper | `provider.go:962` | 流式 | `NormalizeStreamChunk`（已汇聚） |
| ProviderWrapper | `provider.go:1527` | 流式 | `NormalizeStreamChunk`（已汇聚） |
| ProviderWrapper | `provider.go:2026` | 通用封装 | 由内层站点覆盖 |

关键点：两个客户端的流式入口都已包 `providercompat.NormalizeStreamReadCloser`（`provider_compat_response.go:33/93`），其内部 `Chain.NormalizeStreamChunk`（`providercompat/response.go:31`）对每个 SSE 数据 JSON 做归一化——标记剥离加在这里即覆盖全部流式站点；registry 保证 openai 协议下 chain 永不空（至少命中 `openAIDefaultAdapter`）。

### 4.2 providercompat Context 扩展

`providercompat.Context`（`providercompat.go:12`）新增：

```go
ResponseMarkers []string  // 已按模型解析好的字面量标记（空则不剥离）
```

在 `NormalizeStreamChunk` 外层（chain 遍历前）对 `delta.content` / `delta.reasoning_content` 逐字段做 `strings.ReplaceAll`。非流式 body 剥离复用同一 `StripMarkers` 工具函数（对 `[]byte`）。

### 4.3 ProviderConfig 构造点（4 处，与 `CompatibilityProfile` 相同的复制模式）

- `cmd/aicli/commands/chat_core.go:682`
- `cmd/aicli/commands/chat_actor_host.go:1805`
- `cmd/runtime-server/main.go:1333`
- `internal/runtimeserver/provider_config_runtime.go:47`

（`cmd/aicli/commands/chat_provider_turn.go:446`、`cmd/aicli/commands/skills_integration.go:990` 走 ProviderWrapper 路径，同样需要复制；若两处最终都汇入上述 4 个构造入口则可忽略。）

### 4.4 请求期解析

在 gateway_client / provider_wrapper 拿到 `adapterRequest.Model` 后：

```go
markers := ResolveResponseMarkers(selected.Provider.ResponseMarkerRules, adapterRequest.Model)
```

`ResolveResponseMarkers` 返回命中规则的 markers 并集；`len == 0` 时跳过全部剥离，零开销。`adapterRequest.Model` 即 effective model（`resolveGatewaySelectedModel` / `p.resolveModel` 已应用 model_mappings），两个客户端一致。

### 4.5 调试捕获不受影响

- 流式：捕获 Tee 在 providercompat 之前（`newTeeReadCloser` 包在 `normalizeStreamReadCloser` 外层），`ResponseBodyPreview/Raw` 保留原始字节；
- 非流式：先 `reportHTTPDebug` 捕获原始 body，再剥离后传给 `HandleResponse`。

已核实 `validateStreamingAggregateResponse` / `validateAssistantMessageSemantics` 不要求 assistantMsg 内容为 body 的子串，剥离后校验无冲突。

## 5. 剥离实现要点

### 5.1 流式（主路径）：NormalizeStreamChunk 内 JSON 层剥离

`providercompat/response.go:31` 的 `NormalizeStreamChunk` 在 chain 遍历前执行：

```go
// 伪代码
for _, marker := range ctx.ResponseMarkers {
    if delta, ok := chunk["choices"][0]["delta"].(map[string]interface{}); ok {
        for _, key := range []string{"content", "reasoning_content", "reasoning"} {
            if c, ok := delta[key].(string); ok {
                delta[key] = strings.ReplaceAll(c, marker, "")
            }
        }
    }
}
```

| 需求 | 说明 |
|---|---|
| 无字节边界问题 | 在已解析的 JSON 字段上操作，不涉及读缓冲拆分；证据中每个标记都是完整的独立 `content` 块 |
| 跨 content chunk 拆分 | 证据中未出现；如需 100% 覆盖，可在 chain 状态里保留标记尾部（参考 adapter 的 `MarkupTail` 机制），一期可不做并记录限制 |
| 多标记 | 逐条 `ReplaceAll` 即可，列表极短（1~2 条） |
| 空标记列表 | chain 提前 return，零开销 |
| 与捕获 Tee 叠加 | 捕获在 providercompat 之外（先 tee 后归一化），调试记录保留原始字节 |
| 非 OpenAI 协议 | anthropic/codex 的 chunk 无 `choices[].delta` 结构，不做剥离（标记仅在该 openai 网关路径被观察到）；如需扩展可在对应 adapter 的 chunk 归一化里补充 |

### 5.2 非流式（2 处）：body 字节剥离

`gateway_client.go:572-639` 与 `provider.go` 对应位置：`ReadAll` → `reportHTTPDebug`（原始字节）→ `body = StripMarkersBytes(body, markers)` → `bytes.NewReader(body)`。`StripMarkersBytes` 为纯 ASCII 字节级替换，安全处理 UTF-8 多字节序列。

### 5.3 共享工具函数

```go
// agentconfig 或 llm 包
func StripMarkers(input string, markers []string) string
func StripMarkersBytes(input []byte, markers []string) []byte
func ResolveResponseMarkers(rules []ResponseMarkerRule, model string) []string
```

## 6. 边界与安全

1. **匹配的是 effective model**：`adapterRequest.Model`（model_mappings 解析后），两个客户端（gateway / provider_wrapper）一致。
2. **preset 合并语义——slice 整体替换，不是追加**：`MergeConfigYAML`（`preset.go:366`）规定 overlay 的 slices 替换 base 值。用户 config.yaml 写 `providers.items.opencode.ai.response_marker_rules` 会**整体替换**内置 preset 的同名条目，而不是追加。因此：
   - 文档必须写明：覆盖即重列全部条目；
   - 建议内置 preset 按模型族拆成独立条目（一条 preset 一个 `models` 族），用户通过 `enabled: true/false` 开关组合，而不是覆盖列表。
3. **marker 出现在非 ASCII 文本中间**：字节/JSON 层移除 ASCII 序列天然安全。
4. **标记缺失/顺序变化**：规则是字面量列表，上游改标记格式时需要追加新条目；可通过注释与内置 preset 版本演进。
5. **误删**：字面量标记是极低概率出现在正常文本中的 token（`]<]provider[>[` 形态），可接受；如担心，可只对含 `<tool_call>` 或 reasoning 段的窗口剥离（一期不做，留作扩展）。
6. **安全**：配置只影响出站内容清洗，不涉及密钥/权限；剥离发生在响应路径，不触碰请求体。

## 7. 测试计划

### 7.1 单元测试

- `response_marker_rules` 匹配：`*minimax*` 命中 `minimax-m3` / `MiniMax-M3`（大小写）；空 models 全命中；不命中其他模型。
- preset 深合并：内置 preset 把 `response_marker_rules` 落到 `opencode.ai` provider（参照 `preset_test.go:246`）；用户 config.yaml 覆盖。
- `newStripMarkersReader`：
  - 标记单块命中/多块命中/与正常文本交错；
  - 标记被 Read 边界切开（1 字节 1 次读）；
  - 多个标记、无标记、空列表透传；
  - 大体积流（如 10MB）内存与正确性。

### 7.2 回归测试

- adapter 级：流内容含 `]<]minimax[>[<tool_call>...<invoke name="grep">...` 时能正确解析出工具调用（参照现有 `openai_toolcall_markup_test.go`）；
- `NormalizeStreamChunk` 级：delta.content / reasoning_content 含标记时剥离正确；不含标记时原样；
- ProviderWrapper / GatewayClient 级：非流式 body 剥离后 `assistantMsg` 干净、调试捕获保留原始字节；
- 会话级验证：`*minimax*` 规则开启后，重放 `20260909_075709_938_478f9524.http` 第 5 轮响应（走 provider_wrapper 路径），不应再出现 `TOOL_NOT_FOUND`。

## 8. 实施步骤

1. `agentconfig`：新增 `ResponseMarkerRule`、`Provider.ResponseMarkerRules`、`ResolveResponseMarkers` + `MatchResponseMarkerRule`（含单测）。
2. `llm`：`ProviderConfig` 增加字段，4 个构造点复制（含 `chat_provider_turn.go` / `skills_integration.go` 若走 ProviderWrapper 路径）。
3. `providercompat`：`Context.ResponseMarkers` 字段 + `NormalizeStreamChunk` 前置剥离（流式全部站点）+ `StripMarkers` / `StripMarkersBytes` 工具。
4. 非流式：`gateway_client.go:639` 与 `provider.go:736` 两处在 capture 后对 body 剥离。
5. `presets.yaml`：内置 `opencode-gateway-minimax-marker-cleanup` 预设；`EnsureUserPresetsFile` 首次写入会自动带上（仅对新装用户，存量用户需手动合并或文档说明）。
6. 测试：单元 + 回归 + 会话重放验证。
7. 文档：`docs/plan` 实施记录与 `docs/user-guide` 配置说明（含 slice 替换语义提示）。

## 9. 遗留问题 / 后续扩展

- 正则标记支持（`response_marker_patterns`），显式 opt-in；
- 全局 `providers.response_marker_rules` 兜底层；
- 仅对含 `<tool_call>` 或 reasoning 段的窗口剥离（减少误删面）；
- 跨 content chunk 拆分的标记兜底（NormalizeStreamChunk 状态内保留标记尾部）；
- 剥离事件可观测：在 debug 捕获中记录剥离次数/字节数（`[llm-debug] marker_strip` 事件）。