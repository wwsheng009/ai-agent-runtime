import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  isEmptySessionRuntimeSnapshot,
  getSessionRuntimeState,
  normalizeSessionRuntimeSnapshot,
  normalizeSessionRuntimeState,
} from "@/api/runtime/session-runtime";

const SNAPSHOT = {
  state: {
    session_id: "sess-1",
    status: "waiting_approval",
    current_turn_id: "turn-3",
    current_checkpoint_id: "ckpt-1",
    pending_approval: {
      id: "approval-42",
      session_id: "sess-1",
      tool_name: "shell",
      reason: "rm -rf build",
      risk_level: "high",
      expires_at: "2026-09-13T12:30:00Z",
    },
    pending_question: null,
    head_offset: 17,
    active_job_ids: ["job-1", "job-2"],
    updated_at: "2026-09-13T12:00:00Z",
  },
  execution_route: { provider: "deepseek", model: "deepseek-chat" },
};

describe("normalizeSessionRuntimeState", () => {
  it("snake_case 契约解析为归一化状态", () => {
    expect(
      normalizeSessionRuntimeState(SNAPSHOT.state),
    ).toMatchObject({
      sessionId: "sess-1",
      status: "waiting_approval",
      currentTurnId: "turn-3",
      currentCheckpointId: "ckpt-1",
      pendingApproval: {
        id: "approval-42",
        toolName: "shell",
        reason: "rm -rf build",
        riskLevel: "high",
        expiresAt: "2026-09-13T12:30:00Z",
      },
      pendingQuestion: null,
      headOffset: 17,
      activeJobIds: ["job-1", "job-2"],
      updatedAt: "2026-09-13T12:00:00Z",
    });
  });

  it("camelCase 与缺省字段可解析（无未决 = null，不伪造 id）", () => {
    const state = normalizeSessionRuntimeState({
      sessionId: "sess-2",
      status: "idle",
      pendingApproval: { requestId: "approval-7", toolName: "apply_patch" },
      pendingQuestion: { question_id: "" },
      activeJobIds: ["job-9", "", 7],
    });

    expect(state).toMatchObject({
      sessionId: "sess-2",
      status: "idle",
      pendingApproval: {
        id: "approval-7",
        toolName: "apply_patch",
        reason: "",
        riskLevel: "",
      },
      pendingQuestion: null,
      headOffset: 0,
      activeJobIds: ["job-9"],
    });
    expect(state?.currentTurnId).toBeUndefined();
    expect(state?.currentCheckpointId).toBeUndefined();
    expect(state?.updatedAt).toBeUndefined();
  });

  it("缺 session_id / status 视为契约不满足（返回 null）", () => {
    expect(normalizeSessionRuntimeState({ status: "idle" })).toBeNull();
    expect(normalizeSessionRuntimeState({ session_id: "s" })).toBeNull();
    expect(normalizeSessionRuntimeState(null)).toBeNull();
  });
});

describe("normalizeSessionRuntimeSnapshot", () => {
  it("顶层 state + execution_route 透传", () => {
    expect(normalizeSessionRuntimeSnapshot(SNAPSHOT)?.executionRoute).toEqual({
      provider: "deepseek",
      model: "deepseek-chat",
    });
  });

  it("缺 state 时回落把响应体本身当作 state（仅为兼容，缺字段仍返回 null）", () => {
    expect(
      normalizeSessionRuntimeSnapshot({ session_id: "sess-3", status: "idle" }),
    ).toMatchObject({ state: { sessionId: "sess-3", status: "idle" } });
    expect(normalizeSessionRuntimeSnapshot({})).toBeNull();
  });

  it("显式空快照（state: null）不是快照，但可被 isEmptySessionRuntimeSnapshot 识别", () => {
    const empty = { session_id: "sess-4", state: null };

    expect(normalizeSessionRuntimeSnapshot(empty)).toBeNull();
    expect(isEmptySessionRuntimeSnapshot(empty)).toBe(true);
    expect(isEmptySessionRuntimeSnapshot({ session_id: "sess-4" })).toBe(false);
    expect(isEmptySessionRuntimeSnapshot({})).toBe(false);
    expect(isEmptySessionRuntimeSnapshot(null)).toBe(false);
  });
});

describe("getSessionRuntimeState", () => {
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
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("请求 /sessions/{id}/runtime 并对 id 编码", async () => {
    respondWith(SNAPSHOT);

    const result = await getSessionRuntimeState("sess/1");

    expect(calls).toHaveLength(1);
    expect(String(calls[0].url)).toContain(
      "/api/runtime/sessions/sess%2F1/runtime",
    );
    expect(result?.state?.pendingApproval?.id).toBe("approval-42");
  });

  it("显式空快照（会话存在、无 actor 状态）解析为空态且不抛错", async () => {
    respondWith({ session_id: "sess-1", state: null });

    await expect(getSessionRuntimeState("sess-1")).resolves.toBeNull();
  });

  it("空 sessionId 直接失败（不发请求）", async () => {
    respondWith(SNAPSHOT);

    await expect(getSessionRuntimeState("  ")).rejects.toThrow(
      /session id is required/,
    );
    expect(calls).toHaveLength(0);
  });

  it("结构不满足契约时抛错，不返回伪造空态", async () => {
    respondWith({ state: { status: "idle" } });

    await expect(getSessionRuntimeState("sess-1")).rejects.toThrow(
      /invalid session runtime state payload/,
    );
  });

  it("非 2xx（如 404）抛出带 status 的错误，供调用方按空态处理", async () => {
    respondWith({ error: "session runtime state not found" }, 404);

    await expect(getSessionRuntimeState("sess-1")).rejects.toMatchObject({
      status: 404,
    });
  });
});
