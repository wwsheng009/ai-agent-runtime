import { describe, expect, it } from "vitest";

import {
  nextPendingApproval,
  type PendingSubagentApproval,
} from "@/hooks/workspace/use-subagent-session";
import type { SessionRuntimeEvent } from "@/types/runtime";

function runtimeEvent(
  type: string,
  payload: Record<string, unknown> = {},
  extra: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type,
    session_id: "child-1",
    timestamp: "2026-09-13T00:00:00Z",
    payload,
    ...extra,
  } as SessionRuntimeEvent;
}

const pending: PendingSubagentApproval = {
  requestId: "approval-42",
  toolName: "shell",
};

describe("nextPendingApproval", () => {
  it("approval_requested 建入口并映射工具名/调用/原因/风险级别", () => {
    expect(
      nextPendingApproval(
        null,
        runtimeEvent("approval_requested", {
          request_id: "approval-42",
          tool_name: "shell",
          tool_call_id: "call-7",
          reason: "writes outside workspace",
          risk_level: "high",
        }),
      ),
    ).toEqual({
      requestId: "approval-42",
      toolName: "shell",
      toolCallId: "call-7",
      reason: "writes outside workspace",
      riskLevel: "high",
    });
  });

  it("tool_name 缺失时回退事件级 tool_name，缺 request_id 时不建入口", () => {
    expect(
      nextPendingApproval(
        null,
        runtimeEvent(
          "approval_requested",
          { request_id: "approval-9" },
          { tool_name: "bash" },
        ),
      ),
    ).toEqual({
      requestId: "approval-9",
      toolName: "bash",
      toolCallId: undefined,
      reason: undefined,
      riskLevel: undefined,
    });

    // 没有 request_id 就没有可提交的目标，保持原状（不渲染不可用的入口）。
    expect(nextPendingApproval(null, runtimeEvent("approval_requested", {}))).toBeNull();
    expect(
      nextPendingApproval(pending, runtimeEvent("approval_requested", { tool_name: "bash" })),
    ).toBe(pending);
  });

  it("approval_resolved 按 request_id 精确清除，缺 id 时保守清除", () => {
    expect(
      nextPendingApproval(
        pending,
        runtimeEvent("approval_resolved", {
          request_id: "approval-other",
          allowed: true,
        }),
      ),
    ).toBe(pending);
    expect(
      nextPendingApproval(
        pending,
        runtimeEvent("approval_resolved", { request_id: "approval-42", allowed: false }),
      ),
    ).toBeNull();
    expect(
      nextPendingApproval(pending, runtimeEvent("approval_resolved", { allowed: true })),
    ).toBeNull();
    expect(
      nextPendingApproval(null, runtimeEvent("approval_resolved", { request_id: "x" })),
    ).toBeNull();
  });

  it("无关事件原样返回，避免无谓重渲染", () => {
    const event = runtimeEvent("tool.progress", { tool_call_id: "call-7" });
    expect(nextPendingApproval(pending, event)).toBe(pending);
    expect(nextPendingApproval(null, event)).toBeNull();
  });
});
