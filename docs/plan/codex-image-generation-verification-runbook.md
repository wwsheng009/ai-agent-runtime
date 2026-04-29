# Codex 图片生成端到端验证 Runbook

状态: **ready**
对应方案: `docs/plan/codex-image-generation-gaps-followup.md`

## 1. 目标

验证 runtime-server 图片生成链路是否完整，包括：

- assistant 消息里的图片占位是否先出现；
- 最终图片是否能原地替换占位；
- `/generated-images/{name}` 是否支持 `ETag`、`If-None-Match`、`Range`；
- 浏览器前端是否能通过 `VITE_API_BASE_URL` 访问同一个 runtime-server。

## 2. 前置条件

- `backend/configs/config.yaml` 里 `codex_fox` 或等价 Codex provider 已启用图片能力。
- `backend/configs/runtime.yaml` 存在 `images.cacheMaxAge`，默认示例为 `1h`。
- 前端环境变量使用 `VITE_API_BASE_URL`，不是 `VITE_RUNTIME_BASE_URL`。
- 运行目录下有可写的 `backend/data`、`chat-logs` 或等价会话落盘目录。

## 3. 启动服务

启动 runtime-server：

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go run ./cmd/runtime-server serve --config .\configs\runtime.yaml
```

另开一个终端启动前端：

```powershell
cd E:\projects\ai\ai-agent-runtime\frontend
pnpm dev
```

如果前端没有直连 runtime-server，而是通过 `.env` 配置访问，确认：

```env
VITE_API_BASE_URL=http://127.0.0.1:8101
```

## 4. 触发图片生成

1. 打开前端 workspace。
2. 选择支持 Codex 图片能力的 provider / model，例如 `codex_fox` + `gpt-5.4-mini`。
3. 发送 prompt：

```text
draw me a red square, 256x256
```

4. 观察消息流：
   - 图片完成前，应先看到 `image-placeholder` 占位卡片。
   - 生成完成后，占位应被真实图片替换。

## 5. HTTP 验证

拿到会话 ID 和生成图的 `metadata.generated_images[].id` 后，先把 `:` 清洗成 `_`，再请求：

```powershell
curl -i "http://127.0.0.1:8101/api/runtime/sessions/<sid>/generated-images/<name>"
```

期望结果：

- `200 OK`
- `Content-Type: image/png`
- `ETag: "<...>"`
- `Accept-Ranges: bytes`
- `Cache-Control: private, max-age=3600`，或者按 runtime 配置变化

再做条件请求：

```powershell
curl -i -H "If-None-Match: <etag>" "http://127.0.0.1:8101/api/runtime/sessions/<sid>/generated-images/<name>"
```

期望结果：

- `304 Not Modified`
- 响应体为空

再做 Range 请求：

```powershell
curl -i -H "Range: bytes=0-15" "http://127.0.0.1:8101/api/runtime/sessions/<sid>/generated-images/<name>"
```

期望结果：

- `206 Partial Content`
- `Content-Range` 正确
- body 长度为 16 字节

## 6. 前端验收

打开消息列表后确认：

- 图片生成过程中，assistant 消息里有进度占位；
- 图片完成后，占位消失，真实图片出现在同一位置；
- 点击图片后，artifact 面板能看到对应 image artifact；
- artifact 详情里能看到 `sha256`、`byteCount`、`revised_prompt`。

## 7. 失败演练

1. 删除 `metadata.generated_images[].saved_path` 指向的文件，再请求图片端点，期望 `404`。
2. 请求路径包含 `..` 或路径分隔符，例如：

```powershell
curl -i "http://127.0.0.1:8101/api/runtime/sessions/<sid>/generated-images/..%2fpasswd"
```

期望 `400 Bad Request`。

## 8. aicli 零回归

运行同样的 prompt 到 aicli：

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go run ./cmd/aicli chat
```

确认会话目录下仍能落盘到 `generated-images`，行为与修改前一致。
