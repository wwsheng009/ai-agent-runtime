import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  compactSessionContext,
  normalizeSessionCompactOutcome,
  normalizeSessionCompactResult,
  normalizeSessionCompactStatus,
  resolveSessionCompactMode,
} from "@/api/runtime/session-compact";

function buildFullResponse() {
  return {
    ok: true,
    status: {
      mode: "local",
      phase: "pre_turn",
      reason: "",
      provider: "opencode.ai",
      model: "deepseek-v4.1-flash",
      trigger_token_limit: 60_000,
      max_context_tokens: 100_000,
      token_before: 80_000,
    },
    result: {
      mode: "local",
      phase: "pre_turn",
      provider: "opencode.ai",
      model: "deepseek-v4.1-flash",
      trigger_token_limit: 60_000,
      max_context_tokens: 100_000,
      token_before: 80_000,
      token_after: 22_000,
      compacted_messages: 8,
      checkpoint_ids: ["ckpt-1", "ckpt-2"],
      usage_source: "provider",
    },
  };
}

describe("compactSessionContext（手动 compact 契约）", () => {
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
    respondWith(buildFullResponse());
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("把 compact 命令连同模式投递到 runtime/commands", async () => {
    const outcome = await compactSessionContext("session/1", { mode: "remote" });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain(
      "/api/runtime/sessions/session%2F1/runtime/commands",
    );
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.headers).toMatchObject({
      "Content-Type": "application/json",
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      type: "compact",
      mode: "remote",
    });
    expect(outcome.status.tokenBefore).toBe(80_000);
    expect(outcome.result).toMatchObject({
      tokenAfter: 22_000,
      compactedMessages: 8,
      checkpointIds: ["ckpt-1", "ckpt-2"],
      usageSource: "provider",
    });
  });

  it("不传模式时省略 mode 字段（由运行时按会话能力解析）", async () => {
    await compactSessionContext("session-1");

    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ type: "compact" });
  });

  it("非 2xx 会抛出后端错误信息", async () => {
    respondWith({ error: "session busy" }, 500);

    await expect(compactSessionContext("session-1")).rejects.toThrow();
  });
});

describe("normalizeSessionCompactOutcome", () => {
  it("解析完整响应", () => {
    const outcome = normalizeSessionCompactOutcome(buildFullResponse());

    expect(outcome.status).toEqual({
      mode: "local",
      phase: "pre_turn",
      reason: "",
      provider: "opencode.ai",
      model: "deepseek-v4.1-flash",
      triggerTokenLimit: 60_000,
      maxContextTokens: 100_000,
      tokenBefore: 80_000,
    });
    expect(outcome.result?.tokenAfter).toBe(22_000);
  });

  it("容忍 200 {} 之类的残缺响应：缺字段给空值/0，不抛错", () => {
    const outcome = normalizeSessionCompactOutcome({});

    expect(outcome.result).toBeNull();
    expect(outcome.status).toEqual({
      mode: "",
      phase: "",
      reason: "",
      provider: "",
      model: "",
      triggerTokenLimit: 0,
      maxContextTokens: 0,
      tokenBefore: 0,
    });
    expect(normalizeSessionCompactOutcome(null).result).toBeNull();
  });

  it("skipped（result=null）保留 status 里的原因", () => {
    const outcome = normalizeSessionCompactOutcome({
      ok: true,
      status: { mode: "auto", reason: "below_threshold", token_before: 12_000 },
      result: null,
    });

    expect(outcome.result).toBeNull();
    expect(outcome.status.reason).toBe("below_threshold");
    expect(outcome.status.tokenBefore).toBe(12_000);
  });

  it("result 非法时按 null 处理，checkpoint_ids 过滤非字符串", () => {
    expect(normalizeSessionCompactResult("bogus")).toBeNull();
    expect(normalizeSessionCompactResult([])).toBeNull();
    expect(
      normalizeSessionCompactResult({
        checkpoint_ids: ["ckpt-1", 42, null],
        token_after: Number.NaN,
      }),
    ).toMatchObject({ checkpointIds: ["ckpt-1"], tokenAfter: 0 });
  });

  it("未知 mode 一律回落到空串", () => {
    expect(resolveSessionCompactMode(" AUTO ")).toBe("auto");
    expect(resolveSessionCompactMode("remote")).toBe("remote");
    expect(resolveSessionCompactMode("bogus")).toBe("");
    expect(resolveSessionCompactMode(undefined)).toBe("");
    expect(normalizeSessionCompactStatus({ mode: "bogus" }).mode).toBe("");
  });
});
