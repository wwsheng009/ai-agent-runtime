import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  interruptSessionTurn,
  requestSessionTurnInterrupt,
} from "@/api/runtime/session-turn-control";

describe("interruptSessionTurn（建议 3 cancel 契约）", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ url: String(input), init });
        return new Response(JSON.stringify(body), {
          status,
          headers: { "Content-Type": "application/json" },
        });
      },
    ) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
    respondWith({
      ok: true,
      cancelled: true,
      reason: "cancelled",
      channel: "active_turn",
      turn_id: "turn-9",
      cancel_source: "user_interrupt",
    });
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("把 interrupt 连同回合身份投递到 runtime/commands", async () => {
    const response = await interruptSessionTurn("session/1", {
      turnId: "turn-9",
    });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain(
      "/api/runtime/sessions/session%2F1/runtime/commands",
    );
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.headers).toMatchObject({
      "Content-Type": "application/json",
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "interrupt",
      turn_id: "turn-9",
    });
    expect(response).toMatchObject({
      cancelled: true,
      reason: "cancelled",
      channel: "active_turn",
      cancel_source: "user_interrupt",
    });
  });

  it("没有回合身份时省略 turn_id（后端语义 = 取消当前在途回合）", async () => {
    await interruptSessionTurn("session-1");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "interrupt",
    });

    calls = [];
    await interruptSessionTurn("session-1", { turnId: "   " });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "interrupt",
    });
  });

  it("signal 透传到 fetch（停止时按需中止投递）", async () => {
    const controller = new AbortController();
    await interruptSessionTurn("session-1", {
      turnId: "turn-9",
      signal: controller.signal,
    });
    expect(calls[0].init?.signal).toBe(controller.signal);
  });

  it("409 turn_mismatch 按错误抛出（调用方据此判断是否需要重发）", async () => {
    respondWith(
      { error: "turn_id does not match the active turn (turn-10)" },
      409,
    );

    await expect(
      interruptSessionTurn("session-1", { turnId: "turn-9" }),
    ).rejects.toThrow(/turn_id does not match the active turn/);
  });
});

describe("requestSessionTurnInterrupt（停止按钮 best-effort）", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ url: String(input), init });
        return new Response(JSON.stringify(body), {
          status,
          headers: { "Content-Type": "application/json" },
        });
      },
    ) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("成功投递时返回后端结果（重复 stop 也算成功语义）", async () => {
    respondWith({
      ok: true,
      cancelled: false,
      reason: "already_cancelled",
      channel: "active_turn",
      turn_id: "turn-9",
    });

    const result = await requestSessionTurnInterrupt("session-1", "turn-9");

    expect(result).toMatchObject({
      cancelled: false,
      reason: "already_cancelled",
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "interrupt",
      turn_id: "turn-9",
    });
  });

  it("409（回合已换代）不向调用方抛错，返回 null", async () => {
    respondWith({ error: "turn_id does not match the active turn" }, 409);

    await expect(
      requestSessionTurnInterrupt("session-1", "turn-9"),
    ).resolves.toBeNull();
  });

  it("网络失败不向调用方抛错，返回 null", async () => {
    globalThis.fetch = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    }) as typeof fetch;

    await expect(
      requestSessionTurnInterrupt("session-1", "turn-9"),
    ).resolves.toBeNull();
  });

  it("草稿会话（空 id）不发请求", async () => {
    respondWith({ ok: true });

    await expect(requestSessionTurnInterrupt(null)).resolves.toBeNull();
    await expect(requestSessionTurnInterrupt("   ")).resolves.toBeNull();
    expect(calls).toHaveLength(0);
  });

  it("空 turn_id 退化为「取消当前在途回合」", async () => {
    respondWith({
      ok: true,
      cancelled: true,
      reason: "cancelled",
      channel: "session_actor",
    });

    await requestSessionTurnInterrupt("session-1", "  ");

    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "interrupt",
    });
  });
});
