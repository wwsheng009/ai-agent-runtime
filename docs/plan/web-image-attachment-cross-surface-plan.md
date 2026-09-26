# 跨端图片附件（上传）方案 —— 核心抽取 + micro web / React 两个前端接线

状态：**执行中**（S1 核心抽取、S2 micro web 后端、S4 runtime server **已交付并提交**：
`b9cb78b9`（核心 + CLI 薄封装 + micro web 上传/发送 + 方案文档）、`24e27cca`（runtime
`POST /api/runtime/uploads` + `submit_prompt.images`，含路径边界校验）；S3 micro web UI 与
S5 React 前端接线进行中）。日期：2026-09-26。负责范围：`backend/internal/imageattach`（新）、
`backend/cmd/aicli/commands/web_*.go` 与 `web/js/*`、`backend/internal/api/runtimeapi/*`、`frontend/`。

## 0. 实施进度（滚动更新）

| 阶段 | 状态 | 证据 |
|---|---|---|
| S1 共享核心 `internal/imageattach` + CLI 薄封装 | ✅ 已提交 `b9cb78b9` | 核心 10 例（含「限制内上传必须返回持久 artifact」回归）；CLI 图片链路广域全绿 |
| S2 micro web 后端 `POST /web/api/attachments` + `/web/api/input.image_paths` | ✅ 已提交 `b9cb78b9` | 8 例（上传落盘、非图片单条跳过、409/400/405、带图发送入列并去重、不可用路径给原因、纯文本契约不变） |
| S3 micro web 前端（粘贴/选择/拖拽 + 附件轨 + verify 脚本） | 🚧 进行中 | 待 `scripts/verify-micro-web-attachments.mjs` |
| S4 runtime `POST /api/runtime/uploads` + `submit_prompt.images` | ✅ 已提交 `24e27cca` | 9 例（落盘缩放、超限/非图片逐条跳过、503 降级、目录外路径拒绝含兄弟目录陷阱、附加字段透明） |
| S5 React 前端接线（上传 → 发送带 images + i18n + 测试） | 🚧 进行中 | 待 vitest/e2e |

## 1. 结论：核心**半独立**

「用户给的图片 → 模型多模态内容」这条链路里，**数据面已经在 `internal/`，编排面还在 CLI**：

| 环节 | 位置 | 是否可复用 |
|---|---|---|
| 图片压缩 / 长边与体积上限 / 内容哈希落盘 | `internal/imageprep` | ✅ 独立核心 |
| 剪贴板位图 → PNG 字节 | `internal/clipboardimage` | ✅ 独立核心 |
| 路径校验（存在 / 是图片 / 可解析） | `internal/llm`（`ValidateLocalInputImagePaths`、`ResolveLocalInputImages`） | ✅ 独立核心 |
| 路径集合 → 多模态消息 | `internal/llm.NewUserPromptMessageWithImages`（写 `MetadataKeyInputImages` + `ContentParts`） | ✅ 独立核心 |
| 生成图回传 | `assistant.image_progress` + `internal/api/runtimeapi/generated_image_handlers.go` | ✅ 已就绪 |
| **编排**：`prepareChatImageAttachment`、批量/去重、失败回落文案 | `cmd/aicli/commands/chat_attach_image.go` | ❌ CLI 专属 |
| **入口语义**：附件列表、`[Image #N]` 令牌、删令牌即弃图 | `cmd/aicli/commands/chat_image_tokens.go`、`session.ImagePaths` | ❌ CLI 专属（有意保留） |

**缺口**：两个 Web 面各自缺的**不是**压缩/校验（核心已有），而是「**上传字节 → 受校验、受上限约束的本地路径**」
这一段，以及把该路径塞进各自请求体的通道。因此本轮抽取 `internal/imageattach` 承担这一段，三端共用。

## 2. 现状证据（本轮核实）

- CLI：`session.ImagePaths []string`；`chat_send.go:307` 用 `runtimellm.NewUserPromptMessageWithImages(userMessage, session.ImagePaths)` 组消息；
  `chat_send.go:144-153` 是「临时合并图片路径 → 发送 → 还原」的现成模式。压缩在 `chat_attach_image.go:37-43`。
- micro web client（`backend/cmd/aicli/commands/web/`，无构建步骤）：`chatWebInputRequest`（`web_handlers.go:883-893`）
  只有 `type/prompt/request_id/allow/question_id/answer/discard_pending`，**无图片字段**；已有「生成图预览」（`assistant.image_progress`）。
- React `frontend/`：composer **无附件**（优化计划 §5.1）；但 P1-4 子片 2/3 已交付附件输入面
  （`lib/composer-attachments.ts`、`hooks/workspace/composer/use-composer-attachments.ts`、`composer-attachment-rail.tsx`），
  明确「上传接口未就绪前只本地预览 + 待发送，不伪造已上传态」；优化计划 §6.3 P2-1C 点名的后端接口是 **`POST /runtime/uploads`**。
- runtime server：`POST /api/runtime/sessions/{id}/runtime/commands`（`{"type":"submit_prompt","prompt":…}`，`handler.go:962`）
  是 React 发 prompt 的通道，**无附件字段**；`internal/api/runtimeapi` 无 attachments 相关代码。
- 既有上传设施：`internal/filebrowse/upload.go`（`POST /fs/upload/init` 家族：分片、sha256、冲突策略、`fsscope` 工作区边界）
  是**工作区文件**上传，语义与「附件」不同，不复用其协议；只复用其「字节落盘 + 边界校验」思路。

## 3. 设计

### 3.1 共享核心 `internal/imageattach`

```go
type Limits struct{ MaxDimension int; MaxBytes int64 }   // 0/负 = 用 DefaultLimits()
func DefaultLimits() Limits                              // 1568px / 32MB（与 imageprep 一致）

type Prepared struct { Path string; Note string; Skipped bool; Bytes int64; Width, Height int }

func PrepareLocal(srcPath, artifactDir string, limits Limits) (Prepared, error)        // 磁盘已有图片
func PrepareLocalAll(paths []string, artifactDir string, limits Limits) ([]Prepared, error)
func SaveUploaded(data []byte, filename, artifactDir string, limits Limits) (Prepared, error) // 上传字节 → 校验 → 压缩 → artifact
func AllowedUploadExt(name string) bool
```

语义要点：`SaveUploaded` 先按 `MaxBytes` 拒收（避免先把 100MB 写进磁盘再拒），再 `image.DecodeConfig`
判真实图片格式（不信扩展名），落临时文件后走 `imageprep.Prepare`（内容哈希命名、压缩、上限）并清理临时文件；
`PrepareLocal` 是 `imageprep.Prepare` 的薄封装（`Skipped`/`Note` 透传）。**幂等**：已在限制内的图片
`PrepareLocal` 返回原路径，因此「上传时预处理 + 发送时再校验」不会二次压缩。

### 3.2 三端接线

| 端 | 后端 | 前端 |
|---|---|---|
| CLI（行为不变） | `chat_attach_image.go` 改为调用 `imageattach`，保留 `[Image #N]` 令牌与配置解析 | 不变 |
| micro web client | ① `POST /web/api/attachments`（`multipart/form-data`，字段 `file`，可多文件）：`imageattach.SaveUploaded` → `{path,name,bytes,width,height,note}`；② `POST /web/api/input` 增加 `image_paths []string`：发送前 `PrepareLocalAll` 后按 `chat_send.go:144-153` 同一模式并入 `session.ImagePaths`（**不注入令牌文本**，Web 端附件轨即"删附件即弃图"） | composer 三入口（粘贴 / 文件选择 / 全视口拖放）+ 附件轨（缩略图、体积、移除）+ 发送携带 `image_paths`；失败按既有 `send-status` 行如实报错 |
| React frontend | `POST /api/runtime/uploads`（`multipart/form-data`，字段 `file`）：`imageattach.SaveUploaded` 落到会话/工作区 artifact 目录（`fsscope` 校验）→ `{path,name,bytes,width,height,note}`；`submit_prompt` 增加 `images []string` → `NewUserPromptMessageWithImages` 同一核心 | 把 `use-composer-attachments` 的 pending 项接真实上传（成功后换成服务端 path、失败保持 pending 并给出原因）；`submit_prompt` 带 images；i18n 双语键；vitest + e2e |

非目标：不改 Generator 侧（生成图）链路；不回填 CLI 令牌语义到 Web；`filebrowse` 上传协议不动。

## 4. 阶段与验收

| 阶段 | 交付 | 验收 |
|---|---|---|
| S1 | `internal/imageattach` + CLI 薄封装 | 新包单测（字节上传：超限拒收、坏图拒收、真图压缩落 artifact、幂等）；CLI 既有图片链路测试全绿（行为不变） |
| S2 | micro web 后端两端点 + 测试 | `web_handlers_*_test.go`：上传成功/超限/非图片/多文件、input 带 image_paths 时附件进入发送（fake 附件管线断言） |
| S3 | micro web 前端 JS + verify 脚本 | `scripts/verify-micro-web-attachments.mjs`：三入口、附件轨、发送载荷、错误行、删附件即不发 |
| S4 | runtime `POST /api/runtime/uploads` + `submit_prompt.images` + 测试 | runtimeapi 单测：上传→路径→submit_prompt 后模型消息含 `MetadataKeyInputImages`；路径越界拒绝 |
| S5 | React 前端接线 + i18n + 测试 | vitest（hook/api）+ e2e：选择/粘贴/拖放 → 上传 → 发送带 images；失败不伪造已上传 |

## 5. 风险

| 风险 | 缓解 |
|---|---|
| 路径边界：Web 客户端送任意本地路径 | 上传端只落服务端选定 artifact 目录；`submit_prompt.images` 走 `llm.ValidateLocalInputImagePaths` + 与上传目录比对（只接受本服务端产出的路径） |
| 体积/内存：multipart 大文件 | 先 `MaxBytes` 拒收 + `io.LimitReader`；压缩在落盘后 |
| 与 CLI 令牌语义冲突 | Web 端不使用令牌；`session.ImagePaths` 每回合发送后清空（既有行为） |
| 并发：同一会话多标签页 | 附件列表由各前端自持；服务端只在发送瞬间合并（与 CLI 同模式） |
| 在途改动冲突 | 只碰 `web_*`、`runtimeapi/fs|uploads`、`frontend/`、`internal/imageattach`，避开 `internal/policy/*`、`toolbroker/*` |
