import { describe, expect, it, vi } from "vitest";

import {
  consumeSseResponse,
  parseSsePayload,
  SseIdleTimeoutError,
} from "@/api/runtime/sse";

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

  // 故障现场：半开连接不会让 fetch 报错，`reader.read()` 可以永远挂着
  // （CDP 网络层只到达 13 字节、页面侧消费 21 字节，UI 永久停在
  // "Streaming output…"）。看门狗必须把「连着但没数据」变成可处理的错误。
  it("静默超过 idleTimeoutMs：取消底层连接并抛 SseIdleTimeoutError", async () => {
    vi.useFakeTimers();
    try {
      const encoder = new TextEncoder();
      let cancelled = false;
      const response = new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(encoder.encode(": open\n\n"));
            // 之后不再写入任何字节：模拟半开连接（不报错、不结束）。
          },
          cancel() {
            cancelled = true;
          },
        }),
        { headers: { "Content-Type": "text/event-stream" }, status: 200 },
      );
      const onEvent = vi.fn();

      const consumed = consumeSseResponse(response, {
        idleTimeoutMs: 45_000,
        onEvent,
      });
      const rejection = expect(consumed).rejects.toBeInstanceOf(
        SseIdleTimeoutError,
      );
      await vi.advanceTimersByTimeAsync(45_000);

      await rejection;
      expect(cancelled).toBe(true);
      expect(onEvent).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  // 反向约束：注释帧（服务端每 15s 一行 `: keepalive`）同样是存活证据，
  // 空闲会话（after == latest_seq）只有 keepalive，不能被误判成死连接。
  it("keepalive 注释帧重置看门狗：空闲长连接不被误判", async () => {
    vi.useFakeTimers();
    try {
      const encoder = new TextEncoder();
      let ticks = 0;
      const response = new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(encoder.encode(": open\n\n"));
            const timer = setInterval(() => {
              ticks += 1;
              controller.enqueue(encoder.encode(": keepalive\n\n"));
              if (ticks >= 5) {
                clearInterval(timer);
                controller.close();
              }
            }, 10_000);
          },
        }),
        { headers: { "Content-Type": "text/event-stream" }, status: 200 },
      );
      const onEvent = vi.fn();

      const consumed = consumeSseResponse(response, {
        idleTimeoutMs: 45_000,
        onEvent,
      });
      await vi.advanceTimersByTimeAsync(50_000);

      await expect(consumed).resolves.toBeUndefined();
      expect(onEvent).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});
