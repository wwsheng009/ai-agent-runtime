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
});
