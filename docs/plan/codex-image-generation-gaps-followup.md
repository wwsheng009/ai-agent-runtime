# Codex 图片生成 – 缺口补齐实施方案

状态: **completed**
前置文档: `docs/plan/codex-image-generation-runtime-server-and-frontend.md`
相关能力链路: `docs/codex/image-generation-capability-flow.md`

## 0. 背景

端到端主链路（runtime-server 暴露 `GET /api/runtime/sessions/{id}/generated-images/{name}` + 前端 image segment/artifact）已经落地并全绿（见前置文档的 `# 已完成` 部分）。

本文档收敛主方案里尚未处理的 4 个缺口：

1. **流式中间态**: `image_generation_call` 的 `in_progress` / `partial_image` 事件目前不会进入前端，用户在图片完成前看不到任何反馈。
2. **Range / 大图**: 当前 handler 使用 `io.Copy` 一次性写出，不支持 HTTP Range / 断点续传 / ETag 复验。
3. **缓存可配置化**: `Cache-Control: max-age=3600` 写死在 handler 里，公网部署或强一致场景下缺乏调节手段。
4. **端到端验证 runbook**: 缺少一份“从启 runtime-server 到在浏览器看到生成图”的可复现手动验证步骤。

## 1. 目标 / 非目标

### 1.1 目标

- 流式路径上，assistant 消息在图片返回前就能呈现“生成中”占位，完成后原地切换为真实图片。
- `/generated-images/{name}` 端点满足现代浏览器对 Range / ETag / If-None-Match 的常规期待，大图不再一次性占用后端内存/带宽。
- 图片端点的缓存策略通过 `runtime.yaml` 配置，热重载后立即生效，无需发版。
- 提供一份可复制粘贴的 runbook，让别人无需看代码也能验收图片链路。

### 1.2 非目标

- 不引入图片压缩 / 缩略图生成。
- 不把图片升级为 artifact store 第一公民（留给后续 artifact store 重构）。
- 不为 aicli 的 CLI 渲染做任何变更。
- 不做多租户隔离增强（短期沿用现有 session scope）。

## 2. 设计总览

```
┌──────────────────────────────┐   (SSE)   ┌───────────────────────────┐
│ internal/llm stream adapters │──────────▶│ internal/agent runtime    │──┐
│  emit image_generation_call  │           │ forward as runtime events │  │
│  progress & partial_image    │           └───────────────────────────┘  │
└──────────────────────────────┘                                          │
                                                                          ▼
┌──────────────────────────────┐   HTTP   ┌─────────────────────────────┐
│ internal/api/skills          │◀────────▶│ frontend                     │
│ generated_image_handlers     │  Range   │ image placeholder segment   │
│ ServeContent + ETag + cache  │          │ upgrades to final <img>     │
└──────────────────────────────┘          └─────────────────────────────┘
```

## 3. 缺口 1 – 流式中间态

### 3.1 现状

- `internal/llm/provider.go` 与 `gateway_client.go` 的 SSE 解析只识别 `EventTypeText` / `EventTypeReasoning` / `EventTypeDone` / `EventTypeError`。
- Codex Responses SSE 会先发 `response.output_item.added` (`type: image_generation_call`, `status: in_progress`)，中间可能发 `response.image_generation_call.partial_image`（低频率），最后在 `response.output_item.done` 里把 `result` (base64) 给出。
- 这些事件目前都只会在响应汇总时由 `ProcessCodexAssistantImageGeneration` 一次性解读，不会以增量方式传到上层。

### 3.2 改动点

#### 3.2.1 `internal/llm/runtime.go` 新增事件类型

```go
const (
    ...
    EventTypeImage     StreamEventType = "image"
)
```

`StreamChunk` 复用现有 `Metadata` 字段传递图片进度，结构约定：

```go
// Metadata keys
// "phase"          -> "started" | "partial" | "completed" | "failed"
// "image_id"       -> string (codex item id, e.g. "img:1")
// "sanitized_id"   -> string (sanitize(":"->"_"))
// "revised_prompt" -> string (optional)
// "session_id"     -> string (copy of req.Metadata["session_id"])
// "progress"       -> float64 0..1 (optional, only with partial)
// "error"          -> string (only with failed)
```

#### 3.2.2 Codex SSE 适配器

在 `internal/llm/codex_stream_adapter.go`（若已存在则改此文件；否则与现有 SSE 解析同文件内新增 case）解析：

- `response.output_item.added` 且 `item.type == "image_generation_call"` -> emit `StreamChunk{Type: EventTypeImage, Metadata: {"phase": "started", "image_id": id, "sanitized_id": sanitize(id)}}`。
- `response.image_generation_call.partial_image` -> emit `phase: "partial"`（如果 Codex 给了 index/count 就附带）。
- `response.output_item.done` 且 `item.type == "image_generation_call"` -> emit `phase: "completed"`，并把 `revised_prompt`、`sanitized_id` 都带上。

这一步不改变“何时落盘”——落盘继续由响应结束后的 `ProcessCodexAssistantImageGeneration` 做，这里只做**进度通知**。

#### 3.2.3 `internal/agent` 转发

在 `internal/agent/loop.go` 现有流式分发循环（参见 `loop.go:515-553` 的 `callCtx = llm.WithStreamReporter(...)` 段）加入 `case llm.EventTypeImage:` 分支：

```go
case llm.EventTypeImage:
    loop.agent.emitRuntimeEvent("assistant.image_progress", sessionID, "", map[string]interface{}{
        "trace_id": traceID,
        "step":     step,
        "image":    chunk.Metadata,
    })
```

`assistant.image_progress` 是一个新事件名，和现有 `assistant_delta` / `assistant.reasoning` 走同一条 runtime events 通道，无需改 SSE 协议。

#### 3.2.4 Frontend

- `frontend/src/api/runtime/sessions-stream.ts`（或对应 runtime event dispatcher）把 `assistant.image_progress` 事件 reducer 写到 `workspace-thread-state.ts`。
- 新增 `MessageSegment` 变体：

  ```ts
  | {
      type: "image-placeholder";
      imageId: string;       // sanitized_id
      phase: "started" | "partial" | "completed" | "failed";
      progress?: number;     // 0..1
      caption?: string;      // revised_prompt 如果已经出现
      errorMessage?: string; // phase=failed 时
    };
  ```

- 在 `message-rich-content.tsx` 加一个分支，`started|partial` 时渲染一个 animate-pulse 的方块（复用现有 `<Skeleton>` 风格）；`failed` 时渲染 warning callout 样式。
- 收到 `generated_images` metadata（主链路已有）后，同一个 `imageId` 上的 placeholder 会被替换为真正的 `image` segment；实现方式：在 `buildGeneratedImageAttachments` 里标记 `placeholderImageIds`，前端 reducer 用 `Map<imageId, segmentIndex>` 合并。

#### 3.2.5 测试

- `internal/llm/codex_stream_adapter_test.go`: 喂一段 SSE 流，断言 emit 出的 `StreamChunk.Type == EventTypeImage` 的 phase 顺序。
- `internal/agent/loop_image_progress_test.go`: 断言事件名 `assistant.image_progress` 出现在 runtime event ledger。
- 前端: `use-workspace-agent-chat-turn.test.ts` 扩展一个 case, 模拟进度事件 + metadata 事件，断言 segments 最终只剩一个 `type: "image"`。

### 3.3 兼容性

- Codex 非流式路径（`req.Stream == false`）不 emit 任何 `EventTypeImage`，和今天完全一致。
- 其他 provider（openai / anthropic / gemini）不会走到这个 SSE case，不触发任何新事件。
- 前端旧客户端收到未知 event 类型默认丢弃，不会 crash。

---

## 4. 缺口 2 – Range / 大图

### 4.1 现状

```go
w.Header().Set("Content-Type", mimeType)
w.Header().Set("Cache-Control", "private, max-age=3600")
w.Header().Set("Content-Disposition", ...)
w.WriteHeader(http.StatusOK)
_, _ = io.Copy(w, file)
```

（见 `backend/internal/api/skills/generated_image_handlers.go:63-75`）

- 忽略 `Range` / `If-None-Match` / `If-Modified-Since` 请求头。
- 不发 `Content-Length`，浏览器无法展示进度。
- 没有 ETag，重复请求总是走全量 body。

### 4.2 方案：切到 `http.ServeContent`

```go
// 1) 开文件后 Stat 一次，拿 ModTime / Size
info, statErr := file.Stat()
if statErr != nil { ... }

// 2) 计算 ETag
etag := `"` + generatedImageETag(entry, info) + `"`
w.Header().Set("ETag", etag)
w.Header().Set("Accept-Ranges", "bytes")

// 3) Cache-Control（见缺口 3）
w.Header().Set("Cache-Control", cacheControlHeader(h.generatedImageCacheMaxAge()))

// 4) Content-Type 需要手动 set，因为 ServeContent 会根据 name 推断，而我们的 name 参数是清洗过的 id 未必含扩展名
w.Header().Set("Content-Type", mimeType)
w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, filepath.Base(absPath)))

http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), file)
```

`generatedImageETag`:

- 优先使用 `metadata.generated_images[].sha256`（主链路已写入 `@backend/internal/llm/codex_image_generation.go:33`）。
- 缺失时 fallback 为 `fmt.Sprintf("%x-%x", info.ModTime().UnixNano(), info.Size())`。

### 4.3 边界条件

- `http.ServeContent` 已经处理 `If-None-Match` 命中时返回 `304 Not Modified`，不用自己写。
- `http.ServeContent` 只接受 `io.ReadSeeker`，`*os.File` 本身满足。
- `info.ModTime()` 对 Windows 文件系统精度足够（秒级）。
- 浏览器发 `Range: bytes=0-` 时，会收到 `206 Partial Content`，content-length 正确；我们的图片通常 <1MB，不会影响体验。

### 4.4 测试新增

在 `generated_image_handlers_test.go` 新增：

1. `TestGetSessionGeneratedImage_SetsETagAndAcceptRanges`: 200 响应必须带 `ETag` 与 `Accept-Ranges: bytes`。
2. `TestGetSessionGeneratedImage_UsesSha256FromMetadataAsETag`: 当 metadata 含 `sha256` 时 ETag 值以其为后缀。
3. `TestGetSessionGeneratedImage_Returns304OnIfNoneMatch`: 先拿 ETag，再带 `If-None-Match` 请求，断言 `304`，body 为空。
4. `TestGetSessionGeneratedImage_SupportsRangeRequests`: `Range: bytes=5-10` 返回 `206`，body 是期望切片。

---

## 5. 缺口 3 – 缓存可配置化

### 5.1 配置结构

扩展 `backend/internal/config/manager.go`:

```go
// ImagesConfig controls HTTP behavior for serving runtime-generated images.
type ImagesConfig struct {
    CacheMaxAge time.Duration `yaml:"cacheMaxAge" json:"cacheMaxAge"`
}
```

嵌入 `RuntimeConfig`:

```go
Images ImagesConfig `yaml:"images" json:"images"`
```

`DefaultRuntimeConfig()` 中新增：

```go
Images: ImagesConfig{
    CacheMaxAge: time.Hour,
},
```

`runtime.yaml` 示例：

```yaml
images:
  cacheMaxAge: 1h       # 默认 1h，可为 0 关闭缓存
```

### 5.2 Handler 读取

在 `internal/api/skills/handler.go` 增加 getter：

```go
func (h *Handler) generatedImageCacheMaxAge() time.Duration {
    if h == nil {
        return time.Hour
    }
    if cfg := h.runtimeConfig; cfg != nil {
        if cfg.Images.CacheMaxAge > 0 {
            return cfg.Images.CacheMaxAge
        }
        if cfg.Images.CacheMaxAge == 0 {
            // 显式 0 -> 不缓存
            return 0
        }
    }
    return time.Hour
}

func cacheControlHeader(d time.Duration) string {
    if d <= 0 {
        return "no-store"
    }
    return fmt.Sprintf("private, max-age=%d", int(d.Seconds()))
}
```

热重载: `RuntimeManager` 已经在 `internal/runtimeserver` 端点触发时会刷新 `h.runtimeConfig`，此处只读，不需要额外 hook。

### 5.3 测试

- `TestGetSessionGeneratedImage_UsesConfiguredCacheMaxAge`: 把 runtime config 设 `Images.CacheMaxAge = 0`，断言响应头为 `Cache-Control: no-store`。
- `TestGetSessionGeneratedImage_UsesCustomCacheMaxAge`: 设 10 分钟，断言 `max-age=600`。

### 5.4 兼容性

旧 `runtime.yaml` 没有 `images` 块时，YAML 解析得到零值，getter 会退回默认 `time.Hour`，行为与当前一致。

---

## 6. 缺口 4 – 端到端验证 runbook

新文档 `docs/plan/codex-image-generation-verification-runbook.md`（不由本方案直接生成，作为本方案的交付物之一）。内容大纲：

1. 准备条件
   - `backend/configs/config.yaml` 里 `codex_fox` provider 的 `model_capabilities.gpt-5.4-mini` 已启用 `input_modalities: [text, image]` 和 `native_tools.image_generation: true`。
   - `backend/configs/runtime.yaml` 里 `images.cacheMaxAge` 为默认或显式值。
   - 前端能指向同一个 runtime-server（`frontend/.env` 中的 `VITE_API_BASE_URL`）。
2. 启动顺序
   ```powershell
   cd e:\projects\ai\ai-agent-runtime\backend
   go run ./cmd/runtime-server serve --config .\configs\runtime.yaml
   ```
   另开终端：
   ```powershell
   cd e:\projects\ai\ai-agent-runtime\frontend
   pnpm dev
   ```
3. 触发图片生成
   - 在前端 workspace 页面选 codex_fox / gpt-5.4-mini。
   - 发送 prompt: `draw me a red square, 256x256`。
4. 后端检查（curl / 浏览器）
   - 等待会话结束后，找到 assistant 消息 `metadata.generated_images[].id`，sanitize。
   - `curl -i http://127.0.0.1:8101/api/runtime/sessions/<sid>/generated-images/<name>` 应返回 200 + `Content-Type: image/png` + `ETag` + `Accept-Ranges: bytes`。
   - 重发带 `If-None-Match` 的同请求，断言 304。
   - 带 `Range: bytes=0-15` 的请求，断言 206，body 16 字节且以 PNG magic `\x89PNG` 开头。
5. 前端检查
   - 消息列表里图片出现前应先看到 animate-pulse 占位（缺口 1）。
   - 图片出现后点击进入 artifact 详情，查看 `sha256` / `byteCount` / `revised_prompt`。
6. aicli 零回归
   ```powershell
   cd e:\projects\ai\ai-agent-runtime\backend
   go run ./cmd/aicli chat
   ```
   发相同 prompt，`chat-logs/<ts>/<session>.artifacts/generated-images/` 下应生成 PNG。
7. 失败情形演练
   - 手动删除 `metadata.generated_images[].saved_path` 指向的文件，再次请求端点，断言 404。
   - `/generated-images/..%2fetc%2fpasswd` 请求，断言 400。

---

## 7. 推进顺序 / 拆分

按风险从低到高：

1. **缺口 3**（纯配置 + handler 读取）。单 commit 可合。
2. **缺口 2**（handler 切 `ServeContent` + 测试）。单 commit 可合，向前可叠加缺口 3 的 `cacheControlHeader`。
3. **缺口 4**（runbook 文档）。纯文档，可并行。
4. **缺口 1**（流式中间态）。跨 backend + frontend，工作量最大；拆为：
   1. backend: `EventTypeImage` + SSE 适配器 + 事件转发 + 测试；
   2. frontend: placeholder segment 类型 + reducer + 渲染 + 测试；
   3. 端到端: 在 runbook 中补充“观察占位动画出现再消失”的一步。

## 8. 风险与缓解

| 风险 | 级别 | 缓解 |
| --- | --- | --- |
| Codex 真实 SSE 的 image 事件名 / 负载与推测不符 | 中 | 缺口 1 的单测用 fixture，上线前用 `backend/chat-logs/**/runtime-http` 真实录制回放一遍 |
| 老的 `runtime.yaml` 解析不出 `images` 块导致 panic | 低 | YAML 零值默认走 `time.Hour` 已覆盖 |
| `http.ServeContent` 对 `Content-Type` 可能二次推断 | 低 | 手动 `w.Header().Set` 在调用前已经写入，`ServeContent` 会尊重已有值 |
| 前端 placeholder 合并逻辑在并发多图场景出错 | 中 | reducer 使用 `imageId` 作为唯一 key；测试覆盖“两张图同时生成”的场景 |
| 热重载时 cacheMaxAge 变更对浏览器已缓存响应无效 | 低 | 文档中说明：新值只对后续请求生效，历史缓存等 TTL 到期 |

## 9. 验收标准

- `go test ./internal/api/skills/... ./internal/llm/... ./internal/agent/...` 全绿，且新用例覆盖 Range / ETag / 304 / cache_max_age / image progress 事件。
- 前端 `vitest run` 全绿，至少覆盖 placeholder -> image 合并与 failed 占位两条路径。
- runbook 能被团队成员在无额外问询的前提下执行并完成所有断言。
- 回归: aicli 的 chat 图片保存行为与主链路前状态一致。

## 10. 开放问题

- 是否需要给 placeholder 段显示“生成已花费时间”倒计时？本方案默认**不做**，等有实际用户反馈再迭代。
- 是否应该把 ETag 公开在 `metadata.generated_images[].etag` 里以便前端预先发 `If-None-Match`？短期不做，浏览器会在后续请求自动带上。
- 是否允许多 image 结果在同一个 assistant 消息里保证顺序稳定？目前依赖 Codex 返回顺序，不额外排序。
