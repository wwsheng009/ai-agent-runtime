# Codex 图片生成在 runtime-server + 前端的端到端落地方案

状态: **historical implementation plan; endpoint implemented**

当前同步（2026-05-09）：当前源码已经注册 `GET /api/runtime/sessions/{id}/generated-images/{name}`，并且 runtime file transfer 路径是 `/api/runtime/fs/read-file`。本文仍保留端到端落地方案的设计上下文；文中旧 `/api/skills/runtime/...` 与 `/runtime/fs/read-file` 写法已按当前 standalone runtime 路由修正。
负责模块: `backend/internal/api/skills`、`backend/internal/agent`、`backend/internal/llm`、`frontend/src`
关联文档:

- 背景能力链路: `docs/codex/image-generation-capability-flow.md`
- 上游迁移基线: `MIGRATION.md`、`docs/roadmap.md`

## 1. 背景

`backend/cmd/aicli` 与 `backend/cmd/runtime-server` 共享同一套 LLM/agent 内核（`internal/llm`、`internal/agent`、`internal/chat` 等），仅在“接入口”上不同：

- `aicli`: CLI/REPL，进程内直接驱动 agent。
- `runtime-server`: HTTP 服务，通过 `internal/api/skills.Handler` 把 agent 暴露成 REST/SSE。

Codex `image_generation` 这类原生工具的注入与落盘在内核层完成（参见 `docs/codex/image-generation-capability-flow.md`），所以两个入口的“是否会自动注入 image_generation tool 并保存 PNG”这一行为是天然一致的。

但从“图片能不能在 UI 上看到”这个角度：

- `aicli` 把图片写到 chat-logs 会话目录里，CLI 用户用文件浏览器 / 终端直接看，不依赖前端。
- `runtime-server`+`frontend` 当前不具备图片显示能力：
  - 后端没有提供按 session 取图片的 HTTP 端点。
  - 前端的 `MessageSegment` 类型只支持 `text|code|checklist|receipt|callout`；`Artifact.kind` 只支持 `code|html|json`。`metadata.generated_images` 在前端被完全忽略。

## 2. 目标

让经由 runtime-server 的会话产出的 Codex 生成图：

1. **能被前端取回**（不暴露任意路径读取权限）。
2. **能在消息流里就地预览**（缩略图 + 标签/Prompt 摘要）。
3. **能进入 artifact 面板**做完整查看 / 下载。
4. **可观测**（结构化日志 + 错误状态在 UI 上有反馈）。
5. **零回归**：不影响 `aicli` 现有路径，不影响纯文本/代码消息渲染。

## 3. 现状速览（事实清单）

### 3.1 已就绪（共享内核）

- `backend/internal/llm/codex_image_generation.go`
  - `BuildToolDefinitionsForRequest` 在符合条件时追加 `{"type":"image_generation","output_format":"png"}`。
  - `ProcessCodexAssistantImageGeneration` 把 base64 落盘并写 `metadata.generated_images`。
- `backend/internal/llm/provider.go` 与 `gateway_client.go` 在非流式 / 流式 / Gateway 三条路径上都调用了 `ProcessCodexAssistantImageGeneration`。
- `backend/internal/agent/loop.go::generatedImageOutputDirForAgentSession` 自动把输出目录注入 `req.Metadata[MetadataKeyGeneratedImageOutputDir]`。

### 3.2 缺口

- **Backend HTTP**: 历史缺口是 `internal/api/skills/handler.go` 只暴露通用的 `POST /api/runtime/fs/read-file`（base64），没有专门、按 session scoped 的图片端点。当前已新增 session scoped generated image endpoint，避免 UI 直接依赖任意路径读权限。
- **Frontend 类型与渲染**:
  - `frontend/src/data/mock.ts` 里 `MessageSegment` 没有 `image` 变体；`Artifact.kind` 不含 `image`。
  - `frontend/src/components/workspace/message-rich-content.tsx::MessageRichSegment` 没有 image 分支。
  - `frontend/src/components/workspace/artifact-panel.tsx` 与 `artifact-detail-dialog.tsx` 没有 `<img>` 渲染分支。
  - `frontend/src/lib/workspace-thread-state.ts` 在重建 artifact 时把 `kind` 强制收敛到 `code|html|json`，会丢掉任何 image 类型。
- **观测**: `metadata.generated_images_error` 已在 `internal/llm` 写入，但前端不会展示。

## 4. 方案总览

采用“**全量端到端 + message inline segment**”：

```
┌──────────────────────┐    ┌─────────────────────────────┐    ┌──────────────────────┐
│ internal/llm         │    │ internal/api/skills         │    │ frontend             │
│ Codex 响应处理        │ -> │ GET /api/runtime/sessions/ │ -> │ image segment + image│
│ 写 metadata.images   │    │ generated-images/{name}     │    │ artifact + <img>     │
└──────────────────────┘    └─────────────────────────────┘    └──────────────────────┘
```

### 4.1 安全模型

不引入任意路径读取。新端点只允许返回**当前 session 的 assistant message metadata 中已经登记过的图片**：

1. 路径来自服务端写入的 `metadata.generated_images[].saved_path`，**不是**客户端传入。
2. URL 里的 `{name}` 只用于在 metadata 里做 lookup，不参与文件路径拼接。
3. 路径必须存在并且是常规文件，否则 404。
4. 命中后用绝对路径直接 `os.Open` 流式返回 `image/png`。

这样既不需要在 handler 里复算 `generated-images` 目录，也不需要导出 agent 的 ArtifactStorePath，路径解析逻辑统一交给 `internal/llm` 落盘时记录。

### 4.2 路由

```
GET /api/runtime/sessions/{id}/generated-images/{name}
```

- `{id}`: 会话 ID（与现有 `/runtime/sessions/{id}/...` 路由族一致）。
- `{name}`: 文件 basename（例如 `img_1.png`），或 `metadata.generated_images[].id` 的清洗形式（`:` -> `_`），二者均可命中，便于前端兼容。

响应:

- 200 + `Content-Type: image/png` + `Cache-Control: private, max-age=3600` + `Content-Disposition: inline; filename=...`。
- 404: session 不存在 / metadata 里查不到该 image / 文件已被清理。
- 400: `{name}` 包含 `..` 或路径分隔符。
- 503: `sessionManager` 未配置。

## 5. 详细改动清单

### 5.1 Backend

#### 5.1.1 新文件 `backend/internal/api/skills/generated_image_handlers.go`

新增 handler `GetSessionGeneratedImage`：

```go
package skills

import (
    "io"
    "mime"
    "net/http"
    "os"
    "path/filepath"
    "strings"

    "github.com/gorilla/mux"
    "github.com/wwsheng009/ai-agent-runtime/internal/errors"
    runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
    "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func (h *Handler) GetSessionGeneratedImage(w http.ResponseWriter, r *http.Request) {
    if h.sessionManager == nil {
        h.writeError(w, http.StatusServiceUnavailable,
            errors.New(errors.ErrConfigInvalid, "session manager not configured"))
        return
    }
    vars := mux.Vars(r)
    sessionID := strings.TrimSpace(vars["id"])
    name := strings.TrimSpace(vars["name"])
    if sessionID == "" || name == "" {
        h.writeError(w, http.StatusBadRequest,
            errors.New(errors.ErrValidationFailed, "session id and image name are required"))
        return
    }
    if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
        h.writeError(w, http.StatusBadRequest,
            errors.New(errors.ErrValidationFailed, "invalid image name"))
        return
    }

    session, err := h.sessionManager.GetSession(r.Context(), sessionID)
    if err != nil {
        h.writeError(w, http.StatusNotFound, err)
        return
    }

    absPath, mimeType, ok := lookupGeneratedImagePath(session.GetMessages(), name)
    if !ok {
        h.writeError(w, http.StatusNotFound,
            errors.New(errors.ErrAPINotFound, "generated image not found"))
        return
    }
    info, statErr := os.Stat(absPath)
    if statErr != nil || info.IsDir() {
        h.writeError(w, http.StatusNotFound,
            errors.New(errors.ErrAPINotFound, "generated image file missing"))
        return
    }
    file, openErr := os.Open(absPath)
    if openErr != nil {
        h.writeError(w, http.StatusInternalServerError, openErr)
        return
    }
    defer file.Close()

    if mimeType == "" {
        if guess := mime.TypeByExtension(strings.ToLower(filepath.Ext(absPath))); guess != "" {
            mimeType = guess
        } else {
            mimeType = "image/png"
        }
    }
    w.Header().Set("Content-Type", mimeType)
    w.Header().Set("Cache-Control", "private, max-age=3600")
    w.Header().Set("Content-Disposition",
        "inline; filename="+filepath.Base(absPath))
    w.WriteHeader(http.StatusOK)
    _, _ = io.Copy(w, file)
}

func lookupGeneratedImagePath(messages []types.Message, name string) (string, string, bool) {
    if len(messages) == 0 || name == "" {
        return "", "", false
    }
    for i := len(messages) - 1; i >= 0; i-- {
        msg := messages[i]
        if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
            continue
        }
        raw, ok := msg.Metadata[runtimellm.MetadataKeyGeneratedImages]
        if !ok || raw == nil {
            continue
        }
        items := normalizeGeneratedImageEntries(raw)
        for _, entry := range items {
            savedPath := strings.TrimSpace(stringFromAny(entry["saved_path"]))
            if savedPath == "" {
                continue
            }
            entryID := strings.TrimSpace(stringFromAny(entry["id"]))
            sanitizedID := strings.ReplaceAll(entryID, ":", "_")
            base := filepath.Base(savedPath)
            if name == base || name == entryID || name == sanitizedID {
                return savedPath, strings.TrimSpace(stringFromAny(entry["mime_type"])), true
            }
        }
    }
    return "", "", false
}

func normalizeGeneratedImageEntries(raw interface{}) []map[string]interface{} {
    switch v := raw.(type) {
    case []map[string]interface{}:
        return v
    case []interface{}:
        out := make([]map[string]interface{}, 0, len(v))
        for _, item := range v {
            if m, ok := item.(map[string]interface{}); ok {
                out = append(out, m)
            }
        }
        return out
    default:
        return nil
    }
}

func stringFromAny(v interface{}) string {
    if s, ok := v.(string); ok {
        return s
    }
    return ""
}
```

#### 5.1.2 在 `handler.go::RegisterRoutes` 注册路由

在现有 `runtimeRouter.HandleFunc("/sessions/{id}/checkpoints", ...)` 附近补一行：

```go
runtimeRouter.HandleFunc(
    "/sessions/{id}/generated-images/{name}",
    h.GetSessionGeneratedImage,
).Methods(http.MethodGet)
```

#### 5.1.3 单测 `generated_image_handlers_test.go`

至少覆盖：

1. session metadata 中存在条目 + 文件存在 -> 200，body == 文件内容，`Content-Type: image/png`。
2. session 存在但 metadata 里没有该 name -> 404。
3. metadata 存在但磁盘文件不存在 -> 404。
4. `name` 含 `..` 或 `/` -> 400。
5. `sessionManager` 未配置 -> 503。
6. 用 `metadata.generated_images[].id`（含 `:`）和清洗后 id 都能命中。

复用 `chat.SessionManager` 的 in-memory storage（参见 `handler_test.go` / `checkpoint_handlers_test.go` 中现有的初始化模板）。

### 5.2 Frontend

> 命名约定: 在 `Artifact.kind` 中统一使用 `"image"`；`MessageSegment` 中新增 `type: "image"`。

#### 5.2.1 类型扩展 `frontend/src/data/mock.ts`

```ts
export type MessageSegment =
  | { type: "text"; content: string }
  | { type: "code"; language: "bash" | "json" | "tsx" | "ts" | "html"; code: string; title?: string }
  | { type: "checklist"; title: string; items: string[] }
  | { type: "receipt"; title: string; items: Array<{ label: string; value: string; tone?: "accent" | "warning" | "muted" }> }
  | { type: "callout"; title: string; content: string; tone?: "info" | "warning" | "success" }
  | {
      type: "image";
      src: string;                 // 由 runtime adapter 拼接的图片 URL
      alt?: string;
      caption?: string;            // 一般来自 revised_prompt
      width?: number;
      height?: number;
      artifactId?: string;         // 与 artifact 面板对应
    };

export type Artifact = {
  id: string;
  name: string;
  path: string;
  summary: string;
  kind: "code" | "html" | "json" | "image";
  language?: "json" | "tsx" | "ts" | "html"; // image 时可缺省
  content: string;             // 对 image 是绝对/相对 URL，不再是源码
  previewHtml?: string;
  mimeType?: string;           // 新增，image artifact 必填
  byteCount?: number;          // 可选
  sha256?: string;             // 可选
  revisedPrompt?: string;      // 可选，给 alt/caption 用
};
```

注意 `language` 改为可选，避免对 image 强行赋值。所有现存 mock 数据已显式提供 `language`，类型变窄但向后兼容。

#### 5.2.2 渲染 `frontend/src/components/workspace/message-rich-content.tsx`

在 `MessageRichSegment` 现有 if-else 链顶部增加一个 `image` 分支：

```tsx
if (segment.type === "image") {
  return (
    <figure className="mt-2 overflow-hidden rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)]">
      <button
        type="button"
        className="block w-full text-left"
        onClick={() => segment.artifactId && onSelectArtifact?.(segment.artifactId)}
      >
        <img
          src={segment.src}
          alt={segment.alt ?? segment.caption ?? "Generated image"}
          loading="lazy"
          className="block h-auto w-full max-h-[18rem] object-contain bg-black/40"
        />
      </button>
      {segment.caption ? (
        <figcaption className="px-3 py-2 app-text-11 text-[var(--muted-foreground)]">
          {segment.caption}
        </figcaption>
      ) : null}
    </figure>
  );
}
```

`MessageRichSegment` 当前不接收 `onSelectArtifact`。在调用方 `message-list.tsx` 中把 `onSelectArtifact` 透传到 `MessageRichSegment` 即可（`MessageRelatedArtifacts` 已经在使用同一个 prop）。需要保持 prop 是 optional 以兼容仅文本场景。

#### 5.2.3 Artifact 面板 `frontend/src/components/workspace/artifact-panel.tsx` + `artifact-detail-dialog.tsx`

在 “根据 `kind` 选择渲染分支” 的位置增加 `image`：

- 列表态: 显示缩略图 + 文件名 + 大小（如果有 `byteCount`）。
- 详情态: `<img>` 全尺寸，居中放置；下方显示 `revisedPrompt`、`sha256`、`mimeType`。
- 不支持的 image MIME (`!mimeType.startsWith("image/")`) 退回到 generic file 视图（仅显示文件名 + 下载按钮）。

#### 5.2.4 状态映射 `frontend/src/lib/workspace-thread-state.ts`

新增 helper `extractGeneratedImagesFromAssistantMessage(rawMessage, sessionId)`，输入是 backend 返回的 assistant message（含 `metadata.generated_images`），输出是：

```ts
{
  artifacts: Artifact[];      // 每张图一个 image artifact
  segments: MessageSegment[]; // 每张图一条 image segment（顺序与 metadata 一致）
}
```

实现要点：

- 仅在 `metadata.generated_images` 非空时运行。
- `id` 字段用 `metadata.generated_images[].id` 的 sanitized 形式（`:` -> `_`）。
- `src` 拼接规则:
  ```ts
  `${runtimeBaseUrl}/api/runtime/sessions/${encodeURIComponent(sessionId)}/generated-images/${encodeURIComponent(sanitizedId || basename(savedPath))}`
  ```
- `caption` / `alt` 从 `revised_prompt` 取，缺失时使用 `Generated image` fallback。
- `mimeType` 从 metadata 透传，缺省 `image/png`。

再在现有 `kind` 判定（约 `workspace-thread-state.ts:565-572`）放宽：

```ts
kind: rawKind === "code" || rawKind === "html" || rawKind === "image"
  ? rawKind
  : "json"
```

并将 image artifact 的 `content` 直接存放图片 URL（不要塞 `JSON.stringify`）。

#### 5.2.5 错误反馈

在 `metadata.generated_images_error` 存在时，追加一条 `MessageSegment` 类型 `callout` (`tone: "warning"`)，提示“1 张图片保存失败”，避免静默丢图。

#### 5.2.6 单测

新增/补充：

- `lib/workspace-thread-state.test.ts`: 给定包含 `metadata.generated_images` 的 mock assistant message，断言生成 1 个 image artifact + 1 个 image segment + URL 形态。
- `components/workspace/message-rich-content.test.tsx`: 渲染 image segment，断言 `<img alt>`、`<figcaption>` 出现。
- `components/workspace/artifact-detail-dialog.test.tsx`: image artifact 时渲染 `<img src>`。

## 6. 推进顺序

按依赖关系拆 PR / commit，每一步本身可独立合并：

1. **Backend handler + 路由 + 单测**（最小独立 PR）。
   - 先合：前端无感知，可通过 curl 验证。
   - 验证: `go test ./internal/api/skills/...`。
2. **Frontend 类型放宽**（`Artifact.kind`、`MessageSegment` 加 image 变体）。
   - 此步不要求实际渲染，只让类型系统接受 image。
   - 验证: `pnpm test --filter frontend`（或 Vitest）+ `pnpm build`。
3. **Frontend 状态映射** (`workspace-thread-state.ts`)。
   - 让 `metadata.generated_images` 进入 thread state，但渲染层暂时落到“unknown kind 走默认分支”，UI 不报错。
4. **Frontend 渲染层**（`message-rich-content.tsx` + artifact 面板/详情）。
   - 真正显示图片。
5. **错误回执 + i18n / 兜底文案**。
6. **可选**: 后续接入 SSE 流式 image_generation_call 的进度展示（status=`in_progress`）。

## 7. 兼容性 / 回滚

- 后端: 新端点是只读 GET，不写入任何状态；可以随时下线，前端遇到 404/网络错误时只丢图不崩。
- 前端: 类型扩展是“加新成员”，旧消息全部走原分支；当 `metadata.generated_images` 不存在时新增逻辑不会运行。
- `aicli` 完全不受影响，依旧把图片落到 chat-logs 会话目录。
- runtime-server 现有 `/api/runtime/fs/read-file` 不动，依然可作为运维兜底。

## 8. 风险与权衡

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| 文件被清理 | session 关闭后 `os.TempDir()` 下的目录可能被系统清理，URL 仍然有效但 404 | 详情页对 404 显示“图片已过期”占位，并记录 `metadata` 上原始 saved_path 供运维查 |
| 多租户 | metadata 中的 `saved_path` 是绝对路径，跨租户/跨进程不一定可读 | 端点不暴露绝对路径，只用 metadata lookup；本机访问失败按 404 处理 |
| 大图阻塞 | `io.Copy` 流式输出，不会一次性占用内存；没有总量限制 | 后续可加 `Range` 支持与单图大小阈值（>10MB 警告） |
| 浏览器缓存 | `Cache-Control: private, max-age=3600` 适合单租户；公开网关下需要降级 | 通过配置项 `runtime.images.cache_max_age`（后续）控制 |
| Content-Type 漂移 | 当前 native tool 固定 `output_format: png`；未来支持其它 format 时 mime 可能不准 | 优先读 `metadata.generated_images[].mime_type`，再 fallback `mime.TypeByExtension`，最后 `image/png` |

## 9. 开放问题

1. 是否需要把图片同步成 “artifact store 第一公民”（写到 `artifact_store.db`），让 checkpoint 流程也能引用？短期不做，待后续 artifact 相关迁移时一起重构。
2. 是否需要给前端一个 “session 范围内列出全部生成图” 的列表 API？短期不需要，因为 message stream 已经能携带完整 metadata。
3. 是否同时支持 `image_generation_call` 的 streaming 中间态（`in_progress`）？此次方案不实现，列入后续 roadmap。

## 10. 验收标准

- 通过 `runtime-server` 启动、跑一次会触发 `image_generation_call` 的 chat（如 docs/codex 示例配置 `gpt-5.4-mini`），前端能在 assistant 消息中看到内联缩略图，点击后 artifact 面板展示原图、`revised_prompt`、`sha256`。
- 关闭再打开会话，仍能看到图片（路径仍有效时）。
- `aicli` 同模型同会话仍能在 `chat-logs/<session>.artifacts/generated-images` 看到 PNG，行为零回归。
- `go test ./...`（至少 `./internal/api/skills/...`）与前端 `vitest` 全绿。
