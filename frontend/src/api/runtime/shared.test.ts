import { afterEach, describe, expect, it, vi } from "vitest";

import {
  buildErrorMessage,
  fetchRuntimeJson,
  getSessionLeaseConflictTitle,
  isRuntimeApiErrorCode,
  RuntimeApiError,
} from "@/api/runtime/shared";

describe("runtime shared helpers", () => {
  it("appends the request id to backend errors", () => {
    expect(
      buildErrorMessage(503, {
        error: 'HTTP 503: {"error":{"message":"Service temporarily unavailable","type":"api_error"}}',
        request_id: "trace_123",
      }),
    ).toBe(
      'HTTP 503: {"error":{"message":"Service temporarily unavailable","type":"api_error"}} (request_id: trace_123)',
    );
  });

  it("uses the request id even when the backend omits an explicit error message", () => {
    expect(
      buildErrorMessage(500, {
        request_id: "trace_456",
      }),
    ).toBe("runtime request failed with status 500 (request_id: trace_456)");
  });

  it("explains a CLI (aicli) lease owner and how to recover", () => {
    expect(
      buildErrorMessage(409, {
        error: "[SESSION_LEASE_CONFLICT] session runtime lease conflict",
        code: "SESSION_LEASE_CONFLICT",
        context: {
          lease: {
            owner_id: "aicli-actor:host-a:22344:session-1",
            owner_kind: "aicli-actor",
            pid: 22344,
            hostname: "host-a",
            expires_at: "2026-07-16T11:06:49Z",
          },
          suggested_action:
            "continue in the owning aicli process, exit it before retrying here, or launch aicli with --runtime-server auto",
        },
        request_id: "trace_lease",
      }),
    ).toBe(
      "This session is currently active in CLI (aicli) (PID 22344, host host-a). Continue in the owning aicli process, exit it before retrying here, or launch aicli with --runtime-server auto. Current lease expiry: 2026-07-16T11:06:49Z. (request_id: trace_lease)",
    );
  });

  it("labels a web (agent chat) owner and includes the remaining lease time", () => {
    const future = new Date(Date.now() + 60_000).toISOString();
    const message = buildErrorMessage(409, {
      error: "[SESSION_LEASE_CONFLICT] session runtime lease conflict",
      code: "SESSION_LEASE_CONFLICT",
      context: {
        lease: {
          owner_id: "runtime-server-agent-chat:host-a:30008:req-1",
          owner_kind: "runtime-server-agent-chat",
          pid: 30008,
          hostname: "host-a",
          expires_at: future,
        },
        suggested_action:
          "wait for the current session owner to release the lease, then retry",
      },
    });
    expect(message).toContain("web (agent chat) (PID 30008, host host-a)");
    expect(message).toMatch(/Current lease expires in about \d+s \(at .+\)\./);
  });

  it("derives short owner titles for lease conflicts", () => {
    const cliError = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: "aicli-actor" } },
    });
    const webError = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: "runtime-server-agent-chat" } },
    });
    const runtimeError = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: "runtime-server-actor" } },
    });
    const otherError = new Error("unrelated");

    expect(getSessionLeaseConflictTitle(cliError)).toBe("This session is in use by a CLI session");
    expect(getSessionLeaseConflictTitle(webError)).toBe("This session is in use by the web app");
    expect(getSessionLeaseConflictTitle(runtimeError)).toBe("This session is in use by the runtime");
    expect(getSessionLeaseConflictTitle(otherError)).toBeUndefined();
  });

  it("preserves a structured runtime error code for callers", () => {
    const error = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      error: "lease conflict",
    });

    expect(error.status).toBe(409);
    expect(isRuntimeApiErrorCode(error, "SESSION_LEASE_CONFLICT")).toBe(true);
    expect(isRuntimeApiErrorCode(error, "VALIDATION_FAILED")).toBe(false);
  });
});

describe("fetchRuntimeJson 在途请求合并", () => {
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  function jsonResponse(body: unknown) {
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }

  /** 挂起 fetch，直到测试显式 resolve；用于制造「并发在途」窗口。 */
  function pendingFetch() {
    const pending: Array<(response: Response) => void> = [];
    const mock = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          pending.push(resolve);
        }),
    );
    globalThis.fetch = mock as unknown as typeof fetch;
    return {
      mock,
      settle(body: unknown) {
        const resolvers = pending.splice(0);
        for (const resolve of resolvers) {
          resolve(jsonResponse(body));
        }
      },
    };
  }

  it("同一 URL 的并发 GET 只发一次请求，双方共享同一结果", async () => {
    const { mock, settle } = pendingFetch();

    const first = fetchRuntimeJson<{ value: number }>("/api/runtime/models");
    const second = fetchRuntimeJson<{ value: number }>("/api/runtime/models");

    expect(mock).toHaveBeenCalledTimes(1);
    settle({ value: 1 });
    await expect(Promise.all([first, second])).resolves.toEqual([{ value: 1 }, { value: 1 }]);
  });

  it("不同 URL 不合并", async () => {
    const { mock, settle } = pendingFetch();

    const first = fetchRuntimeJson<{ value: number }>("/api/runtime/models");
    const second = fetchRuntimeJson<{ value: number }>("/api/runtime/sessions");

    expect(mock).toHaveBeenCalledTimes(2);
    settle({ value: 2 });
    await expect(Promise.all([first, second])).resolves.toEqual([{ value: 2 }, { value: 2 }]);
  });

  it("调用方自带 signal 时不合并，保留各自的取消语义", async () => {
    const { mock, settle } = pendingFetch();
    const controller = new AbortController();

    const first = fetchRuntimeJson("/api/runtime/logs", { signal: controller.signal });
    const second = fetchRuntimeJson("/api/runtime/logs", { signal: controller.signal });

    expect(mock).toHaveBeenCalledTimes(2);
    settle({ ok: true });
    await Promise.all([first, second]);
  });

  it("请求结束后不再合并：顺序调用各自发请求", async () => {
    const mock = vi.fn(async () => jsonResponse({ value: 3 }));
    globalThis.fetch = mock as unknown as typeof fetch;

    await fetchRuntimeJson("/api/runtime/sessions");
    await fetchRuntimeJson("/api/runtime/sessions");

    expect(mock).toHaveBeenCalledTimes(2);
  });

  it("失败的请求会被清出在途表，后续调用可重试", async () => {
    let calls = 0;
    globalThis.fetch = vi.fn(async () => {
      calls += 1;
      if (calls === 1) {
        return new Response("boom", { status: 500 });
      }
      return jsonResponse({ value: 4 });
    }) as unknown as typeof fetch;

    await expect(fetchRuntimeJson("/api/runtime/agents")).rejects.toBeInstanceOf(RuntimeApiError);
    await expect(fetchRuntimeJson("/api/runtime/agents")).resolves.toEqual({ value: 4 });
    expect(calls).toBe(2);
  });

  it("非 GET 请求不合并", async () => {
    const { mock, settle } = pendingFetch();

    const first = fetchRuntimeJson("/api/runtime/agents", { method: "POST", body: "{}" });
    const second = fetchRuntimeJson("/api/runtime/agents", { method: "POST", body: "{}" });

    expect(mock).toHaveBeenCalledTimes(2);
    settle({ ok: true });
    await Promise.all([first, second]);
  });
});
