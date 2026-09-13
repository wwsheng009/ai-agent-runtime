import { describe, expect, it } from "vitest";

import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";
import type { SessionRuntimeEvent } from "@/types/runtime";

import {
  applyPendingInteractionEvent,
  expirePendingInteractions,
  failPendingInteractionResolve,
  markPendingInteractionResolving,
  pendingInteractionsFromRuntimeEvents,
  settlePendingInteraction,
} from "./events";
import { pendingPlanReviewFromPlan } from "./plan-review";
import {
  emptyPendingInteractionState,
  findPendingInteraction,
  selectPendingInteraction,
  type PendingInteractionState,
} from "./types";

const NOW = Date.parse("2026-09-11T10:00:00.000Z");

function runtimeEvent(
  type: string,
  payload: Record<string, unknown>,
  sessionId = "s-1",
): SessionRuntimeEvent {
  return {
    type,
    session_id: sessionId,
    payload,
    timestamp: "2026-09-11T09:59:00.000Z",
  };
}

function fold(events: SessionRuntimeEvent[]): PendingInteractionState {
  return pendingInteractionsFromRuntimeEvents(events);
}

describe("pending interaction 事件归约", () => {
  it("approval_requested 注册审批条目并读取白名单字段", () => {
    const state = fold([
      runtimeEvent("approval_requested", {
        request_id: "req-1",
        tool_name: "run_shell_command",
        reason: "needs approval",
        risk_level: "high",
        tool_call_id: "call-9",
        turn_id: "turn-3",
        expires_at: "2026-09-11T10:30:00.000Z",
      }),
    ]);

    const approval = findPendingInteraction(state, "req-1");
    expect(approval).toMatchObject({
      kind: "approval",
      status: "pending",
      sessionId: "s-1",
      toolName: "run_shell_command",
      reason: "needs approval",
      riskLevel: "high",
      toolCallId: "call-9",
      turnId: "turn-3",
      expiresAt: "2026-09-11T10:30:00.000Z",
    });
  });

  it("approval_requested 缺 request_id 不建入口（不伪造身份）", () => {
    const state = fold([runtimeEvent("approval_requested", { tool_name: "x" })]);
    expect(state.items).toHaveLength(0);
  });

  it("重复注册同一 request_id 保留注册序且只留一条", () => {
    const state = fold([
      runtimeEvent("approval_requested", { request_id: "req-1", reason: "a" }),
      runtimeEvent("question_asked", { question_id: "q-1", prompt: "?" }),
      runtimeEvent("approval_requested", { request_id: "req-1", reason: "b" }),
    ]);

    expect(state.items).toHaveLength(2);
    expect(state.items[0].id).toBe("req-1");
    expect(findPendingInteraction(state, "req-1")).toMatchObject({ reason: "b" });
  });

  it("approval_resolved 按 request_id 回填 allow / deny", () => {
    const base = fold([
      runtimeEvent("approval_requested", { request_id: "req-1" }),
    ]);
    const allowed = applyPendingInteractionEvent(
      base,
      runtimeEvent("approval_resolved", { request_id: "req-1", allow: true }),
    );
    expect(findPendingInteraction(allowed, "req-1")).toMatchObject({
      status: "resolved",
      resolution: "allow",
    });

    const denied = applyPendingInteractionEvent(
      base,
      runtimeEvent("approval_resolved", { request_id: "req-1", allow: false }),
    );
    expect(findPendingInteraction(denied, "req-1")).toMatchObject({
      status: "resolved",
      resolution: "deny",
    });
  });

  it("approval_resolved 的 expired 进入超时终态", () => {
    const state = fold([
      runtimeEvent("approval_requested", { request_id: "req-1" }),
      runtimeEvent("approval_resolved", {
        request_id: "req-1",
        allow: false,
        resolution: "expired",
      }),
    ]);
    expect(findPendingInteraction(state, "req-1")).toMatchObject({
      status: "expired",
      resolution: "expired",
    });
  });

  it("approval_resolved 缺 id 时按会话保守清除（不误清其他会话）", () => {
    const state = fold([
      runtimeEvent("approval_requested", { request_id: "req-1" }),
      runtimeEvent(
        "approval_requested",
        { request_id: "req-other" },
        "s-other",
      ),
      runtimeEvent("approval_resolved", {}),
    ]);

    expect(findPendingInteraction(state, "req-1")).toMatchObject({
      status: "resolved",
    });
    expect(findPendingInteraction(state, "req-other")).toMatchObject({
      status: "pending",
    });
  });

  it("question_asked 注册提问（required / suggestions 兜底）", () => {
    const state = fold([
      runtimeEvent("question_asked", {
        question_id: "q-1",
        prompt: "Which environment?",
        required: true,
        suggestions: ["staging", 7, "prod"],
      }),
      runtimeEvent("question_asked", {
        id: "q-2",
        prompt: "free form",
        suggestions: "single",
      }),
    ]);

    expect(findPendingInteraction(state, "q-1")).toMatchObject({
      kind: "question",
      prompt: "Which environment?",
      required: true,
      suggestions: ["staging", "prod"],
    });
    expect(findPendingInteraction(state, "q-2")).toMatchObject({
      required: false,
      suggestions: ["single"],
    });
  });

  it("question_answered 回填 answered 并携带答案摘要", () => {
    const state = fold([
      runtimeEvent("question_asked", { question_id: "q-1", prompt: "?" }),
      runtimeEvent("question_answered", {
        question_id: "q-1",
        answer: "staging",
      }),
    ]);
    expect(findPendingInteraction(state, "q-1")).toMatchObject({
      status: "resolved",
      resolution: "answered:staging",
    });
  });

  it("session_end 收敛未决条目为 cancelled 且不动已完成条目", () => {
    const state = fold([
      runtimeEvent("approval_requested", { request_id: "req-1" }),
      runtimeEvent("question_asked", { question_id: "q-1", prompt: "?" }),
      runtimeEvent("approval_resolved", { request_id: "req-1", allow: true }),
      runtimeEvent("session_end", {}),
    ]);

    expect(findPendingInteraction(state, "q-1")).toMatchObject({
      status: "cancelled",
      resolution: "session_end",
    });
    expect(findPendingInteraction(state, "req-1")).toMatchObject({
      status: "resolved",
    });
  });

  it("expirePendingInteractions 只把到期审批置为 expired", () => {
    const state = fold([
      runtimeEvent("approval_requested", {
        request_id: "req-overdue",
        expires_at: "2026-09-11T09:59:30.000Z",
      }),
      runtimeEvent("approval_requested", {
        request_id: "req-future",
        expires_at: "2026-09-11T10:30:00.000Z",
      }),
      runtimeEvent("approval_requested", { request_id: "req-none" }),
      runtimeEvent("question_asked", { question_id: "q-1", prompt: "?" }),
    ]);

    const next = expirePendingInteractions(state, NOW);
    expect(findPendingInteraction(next, "req-overdue")).toMatchObject({
      status: "expired",
    });
    expect(findPendingInteraction(next, "req-future")).toMatchObject({
      status: "pending",
    });
    expect(findPendingInteraction(next, "req-none")).toMatchObject({
      status: "pending",
    });
    expect(next.items.filter((item) => item.status === "expired")).toHaveLength(1);
  });

  it("resolving → resolved / 失败回退 pending 携带原因", () => {
    const registered = fold([
      runtimeEvent("approval_requested", { request_id: "req-1" }),
    ]);
    const resolving = markPendingInteractionResolving(registered, "req-1");
    expect(findPendingInteraction(resolving, "req-1")).toMatchObject({
      status: "resolving",
    });

    const failed = failPendingInteractionResolve(resolving, "req-1", "network");
    expect(findPendingInteraction(failed, "req-1")).toMatchObject({
      status: "pending",
      error: "network",
    });

    const settled = settlePendingInteraction(resolving, "req-1", {
      status: "resolved",
      resolution: "deny",
    });
    expect(findPendingInteraction(settled, "req-1")).toMatchObject({
      status: "resolved",
      resolution: "deny",
    });
  });

  it("selectPendingInteraction 优先 pending 并按时长会话过滤", () => {
    const base = fold([
      runtimeEvent("approval_requested", { request_id: "req-1" }),
      runtimeEvent("question_asked", { question_id: "q-1", prompt: "?" }),
    ]);
    const resolvingFirst = markPendingInteractionResolving(base, "req-1");
    expect(selectPendingInteraction(resolvingFirst, "s-1")?.id).toBe("q-1");

    const otherSession = fold([
      runtimeEvent("approval_requested", { request_id: "req-2" }, "s-2"),
    ]);
    // 会话隔离：s-2 的待交互不在 s-1 的呈现位出现。
    expect(selectPendingInteraction(otherSession, "s-1")).toBeNull();
    expect(selectPendingInteraction(otherSession, "s-2")?.id).toBe("req-2");
    expect(selectPendingInteraction(emptyPendingInteractionState(), "s-1")).toBeNull();
  });

  it("pendingInteractionsFromRuntimeEvents 全链路：注册 → 回填 → 收敛", () => {
    const state = fold([
      runtimeEvent("approval_requested", { request_id: "req-1" }),
      runtimeEvent("question_asked", { question_id: "q-1", prompt: "?" }),
      runtimeEvent("question_answered", { question_id: "q-1", answer: "yes" }),
      runtimeEvent("approval_resolved", { request_id: "req-1", allow: false }),
    ]);
    expect(state.items.map((item) => item.status)).toEqual([
      "resolved",
      "resolved",
    ]);
    expect(selectPendingInteraction(state, "s-1")).toBeNull();
  });
});

describe("计划评审条目", () => {
  const plan = {
    active: true,
    plan_path: "/tmp/plan.md",
    notes: "review me",
  } as RuntimeSessionPlanMode;

  it("active 计划投影为计划评审条目", () => {
    expect(pendingPlanReviewFromPlan(plan, "s-1")).toMatchObject({
      kind: "plan_review",
      id: "plan-review:s-1",
      status: "pending",
      sessionId: "s-1",
      planPath: "/tmp/plan.md",
      notes: "review me",
    });
  });

  it("未激活 / 无会话 / null 计划不产生条目", () => {
    expect(
      pendingPlanReviewFromPlan({ ...plan, active: false }, "s-1"),
    ).toBeNull();
    expect(pendingPlanReviewFromPlan(plan, "")).toBeNull();
    expect(pendingPlanReviewFromPlan(null, "s-1")).toBeNull();
  });
});
