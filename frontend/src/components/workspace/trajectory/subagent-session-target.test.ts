// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  stubResizeObserver,
  subagentItem,
} from "@/components/workspace/trajectory/subagent-session-dialog.test-helpers";
import { subagentSessionTarget } from "@/components/workspace/trajectory/subagent-session-target";

beforeEach(() => {
  stubResizeObserver();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("subagentSessionTarget", () => {
  it("从 chat SSE subagent 载荷解析子会话 ID 与 role", () => {
    const target = subagentSessionTarget(
      subagentItem({ session_id: "child-9", role: "researcher" }),
    );
    expect(target).toEqual({
      sessionId: "child-9",
      agentId: undefined,
      role: "researcher",
      status: undefined,
    });
  });

  it("终态镜像用 agent_id 兜底并把 status 带出", () => {
    const target = subagentSessionTarget(
      subagentItem({ agent_id: "child-9", session_id: "child-9", status: "completed" }),
    );
    expect(target?.sessionId).toBe("child-9");
    expect(target?.agentId).toBe("child-9");
    expect(target?.status).toBe("completed");
  });

  it("缺少 session 标识或非 subagent item 时返回 null", () => {
    expect(subagentSessionTarget(subagentItem({ role: "researcher" }))).toBeNull();
    expect(subagentSessionTarget(null)).toBeNull();
    expect(
      subagentSessionTarget({
        ...subagentItem({ session_id: "child-9" }),
        kind: "tool",
      }),
    ).toBeNull();
  });
});
