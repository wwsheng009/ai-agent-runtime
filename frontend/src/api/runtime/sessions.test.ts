import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { resolveSessionToolApproval } from "@/api/runtime/sessions";

describe("resolveSessionToolApproval", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
    respondWith({ ok: true });
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("把决定投递到 approve_tool 命令并携带 request_id/allow", async () => {
    await resolveSessionToolApproval("child/1", {
      requestId: "approval-42",
      allow: false,
    });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain(
      "/api/runtime/sessions/child%2F1/runtime/commands",
    );
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.headers).toMatchObject({
      "Content-Type": "application/json",
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "approve_tool",
      request_id: "approval-42",
      allow: false,
    });
  });

  it("patchedArgs 仅在提供时映射为 patched_args", async () => {
    await resolveSessionToolApproval("child-1", {
      requestId: "approval-7",
      allow: true,
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "approve_tool",
      request_id: "approval-7",
      allow: true,
    });

    calls = [];
    await resolveSessionToolApproval("child-1", {
      requestId: "approval-7",
      allow: true,
      patchedArgs: { command: "ls" },
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "approve_tool",
      request_id: "approval-7",
      allow: true,
      patched_args: { command: "ls" },
    });
  });

  it("非 2xx 时抛出错误（含状态码与后端 message），供 UI 展示", async () => {
    // 后端错误体与 `RuntimeErrorPayload` 对齐：error 是字符串、可带 request_id。
    respondWith({ error: "approval expired", request_id: "trace_9" }, 409);

    await expect(
      resolveSessionToolApproval("child-1", {
        requestId: "approval-42",
        allow: true,
      }),
    ).rejects.toThrow(/approval expired \(request_id: trace_9\)/);
  });
});
