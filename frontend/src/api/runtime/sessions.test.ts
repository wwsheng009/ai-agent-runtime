import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  answerSessionQuestion,
  resolveSessionToolApproval,
} from "@/api/runtime/sessions";

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

describe("answerSessionQuestion", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  beforeEach(() => {
    calls = [];
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("把回答投递到 answer_question 命令并携带 question_id/answer", async () => {
    await answerSessionQuestion("child/1", {
      questionId: "question-42",
      answer: "继续",
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
      type: "answer_question",
      question_id: "question-42",
      answer: "继续",
    });
  });

  it("允许空回答（与后端 answer_question 放行空串对齐）", async () => {
    await answerSessionQuestion("child-1", { questionId: "q-1", answer: "" });

    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "answer_question",
      question_id: "q-1",
      answer: "",
    });
  });

  it("signal 透传到 fetch（P1-7 取消路径）", async () => {
    const controller = new AbortController();
    await answerSessionQuestion(
      "child-1",
      { questionId: "q-1", answer: "ok" },
      { signal: controller.signal },
    );

    expect(calls[0].init?.signal).toBe(controller.signal);
  });
});
