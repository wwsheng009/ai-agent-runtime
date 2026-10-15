// 会话 goal 管理 API 客户端单测：路径编码、信封归一化、POST 请求体与 applied/message
// 透传（不触网；fetch 由 vi.stubGlobal 注入）。

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  buildSessionGoalPath,
  fetchSessionGoal,
  updateSessionGoal,
} from "./goal";
import { RuntimeApiError } from "./shared";

const activeGoal = {
  goal_id: "goal_1",
  session_id: "session-1",
  objective: "接通控制台 goal 管理",
  status: "active",
  token_budget: 20000,
  tokens_used: 1200,
  updated_at: "2026-10-15T10:30:00+08:00",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubFetch(response: Response | (() => Response)) {
  // 泛型显式声明调用签名：实现体不需要形参，`mock.calls[0]` 仍有 [input, init] 类型。
  const mock = vi.fn<(input: string, init?: RequestInit) => Promise<Response>>(
    async () => (typeof response === "function" ? response() : response),
  );
  vi.stubGlobal("fetch", mock);
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("buildSessionGoalPath", () => {
  it("按会话拼端点，session id 逐段编码", () => {
    expect(buildSessionGoalPath("session_20261015_x")).toBe(
      "/api/runtime/sessions/session_20261015_x/goal",
    );
    expect(buildSessionGoalPath(" a/b ")).toBe(
      "/api/runtime/sessions/a%2Fb/goal",
    );
  });
});

describe("fetchSessionGoal", () => {
  it("GET 信封 → goal 观测（含 remaining_tokens）", async () => {
    const fetchMock = stubFetch(
      jsonResponse({ session_id: "s", goal: activeGoal, remaining_tokens: 18800 }),
    );

    const observation = await fetchSessionGoal("s");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/runtime/sessions/s/goal");
    expect(observation).toEqual({
      kind: "goal",
      goal: expect.objectContaining({
        phase: "active",
        objective: "接通控制台 goal 管理",
        remainingTokens: 18800,
      }),
    });
  });

  it("goal: null → absent（后端 200 + null 表示此刻没有目标）", async () => {
    stubFetch(jsonResponse({ session_id: "s", goal: null }));
    expect(await fetchSessionGoal("s")).toEqual({ kind: "absent" });
  });

  it("非 2xx → RuntimeApiError（含状态码，供 UI 如实提示）", async () => {
    stubFetch(
      jsonResponse(
        { error: { code: "VALIDATION_FAILED", message: "session id is required" } },
        404,
      ),
    );
    await expect(fetchSessionGoal("s")).rejects.toBeInstanceOf(RuntimeApiError);
  });
});

describe("updateSessionGoal", () => {
  it("POST 请求体使用后端 snake_case 字段名", async () => {
    const fetchMock = stubFetch(
      jsonResponse({
        session_id: "s",
        action: "set",
        applied: true,
        message: "goal 已设置",
        goal: activeGoal,
        remaining_tokens: 20000,
      }),
    );

    const result = await updateSessionGoal("s", {
      action: "set",
      objective: "接通控制台 goal 管理",
      tokenBudget: 20000,
    });

    const init = fetchMock.mock.calls[0][1] ?? {};
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      action: "set",
      objective: "接通控制台 goal 管理",
      token_budget: 20000,
    });
    expect(result.applied).toBe(true);
    expect(result.message).toBe("goal 已设置");
    expect(result.observation.kind).toBe("goal");
  });

  it("applied=false 如实透出守卫原因（不当成成功）", async () => {
    stubFetch(
      jsonResponse({
        session_id: "s",
        action: "resume",
        applied: false,
        message: "未应用：预算未提高到超过已用，budget_limited 保持",
        goal: { ...activeGoal, status: "budget_limited" },
      }),
    );

    const result = await updateSessionGoal("s", { action: "resume" });

    expect(result.applied).toBe(false);
    expect(result.message).toContain("budget_limited");
    expect(result.observation).toEqual({
      kind: "goal",
      goal: expect.objectContaining({ phase: "budget_limited" }),
    });
  });

  it("clear 的 goal:null → absent", async () => {
    stubFetch(
      jsonResponse({
        session_id: "s",
        action: "clear",
        applied: true,
        message: "goal 已清除",
        goal: null,
      }),
    );

    const result = await updateSessionGoal("s", { action: "clear" });
    expect(result.applied).toBe(true);
    expect(result.observation).toEqual({ kind: "absent" });
  });
});
