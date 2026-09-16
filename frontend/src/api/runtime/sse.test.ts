import { describe, expect, it, vi } from "vitest";

import { consumeSseResponse, parseSsePayload } from "@/api/runtime/sse";

function createSseResponse(chunks: string[]) {
  const encoder = new TextEncoder();
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) {
          controller.enqueue(encoder.encode(chunk));
        }
        controller.close();
      },
    }),
    {
      headers: {
        "Content-Type": "text/event-stream",
      },
      status: 200,
    },
  );
}

describe("runtime sse helpers", () => {
  it("parses multiline JSON payloads and falls back to raw text", () => {
    expect(parseSsePayload(['{"ok":true}', '"extra"'])).toEqual({
      raw: '{"ok":true}\n"extra"',
    });
    expect(parseSsePayload(["plain-text"])).toEqual({
      raw: "plain-text",
    });
    expect(parseSsePayload([])).toBeNull();
  });

  it("consumes chunked SSE messages and emits parsed events", async () => {
    const onEvent = vi.fn<(eventName: string, payload: Record<string, unknown>) => void>();
    const onOpen = vi.fn();
    const onClose = vi.fn();

    await consumeSseResponse(
      createSseResponse([
        'event: meta\ndata: {"session_id":"s-1"}\n\n',
        'event: chunk\ndata: {"type":"text","content":"hel',
        'lo"}\n\n',
        ": keep-alive\n",
        'event: error\ndata: {"error":"boom"}\n\n',
      ]),
      {
        onClose,
        onEvent,
        onOpen,
      },
    );

    expect(onOpen).toHaveBeenCalledOnce();
    expect(onClose).toHaveBeenCalledOnce();
    expect(onEvent.mock.calls).toEqual([
      ["meta", { session_id: "s-1" }],
      ["chunk", { type: "text", content: "hello" }],
      ["error", { error: "boom" }],
    ]);
  });

  // Batch 1 起服务端在持久化帧首写 `id: <seq>`（真实持久化游标；wire-only 帧
  // 不写，见 `handler.go` 的 writeSSEEventFrame），Batch 3/4 还可能出现 `retry:`。
  // 兼容契约：解析器只认 `:`/`event:`/`data:`，其余行静默忽略——既不报错，也不
  // 产生多余事件（未升级的前端可以安全接住新服务端）。
  it("忽略 `id:`/`retry:` 行：不报错、不产生多余事件", async () => {
    const onEvent = vi.fn<
      (eventName: string, payload: Record<string, unknown>) => void
    >();

    await consumeSseResponse(
      createSseResponse([
        'id: 41\nevent: meta\ndata: {"session_id":"s-1"}\n\n',
        "retry: 3000\n: keep-alive\n\n",
        'id: 42\nevent: chunk\ndata: {"type":"text","content":"hi"}\n\n',
      ]),
      { onEvent },
    );

    expect(onEvent.mock.calls).toEqual([
      ["meta", { session_id: "s-1" }],
      ["chunk", { type: "text", content: "hi" }],
    ]);
  });
});
