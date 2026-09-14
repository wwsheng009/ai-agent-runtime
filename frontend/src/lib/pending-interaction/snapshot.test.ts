import { describe, expect, it } from "vitest";

import {
  applyPendingInteractionEvent,
  markPendingInteractionResolving,
} from "@/lib/pending-interaction/events";
import {
  hydratePendingInteractions,
  pendingInteractionsFromRuntimeState,
} from "@/lib/pending-interaction/snapshot";
import {
  emptyPendingInteractionState,
  findPendingInteraction,
} from "@/lib/pending-interaction/types";
import type { RuntimeSessionState } from "@/types/runtime";

function runtimeState(
  overrides: Partial<RuntimeSessionState> = {},
): RuntimeSessionState {
  return {
    sessionId: "sess-1",
    status: "waiting_approval",
    pendingApproval: null,
    pendingQuestion: null,
    headOffset: 0,
    activeJobIds: [],
    updatedAt: "2026-09-13T12:00:00Z",
    ...overrides,
  };
}

function approvalRequested(id: string, createdAt: string, sessionId = "sess-1") {
  return {
    type: "approval_requested",
    session_id: sessionId,
    timestamp: createdAt,
    payload: { request_id: id, tool_name: "shell", reason: "rm -rf build" },
  };
}

describe("pendingInteractionsFromRuntimeState", () => {
  it("待审批 / 待提问各自建条目并保持事件口径身份", () => {
    const items = pendingInteractionsFromRuntimeState(
      runtimeState({
        pendingApproval: {
          id: "approval-42",
          sessionId: "sess-1",
          toolName: "shell",
          reason: "rm -rf build",
          riskLevel: "high",
          expiresAt: "2026-09-13T12:30:00Z",
        },
        pendingQuestion: {
          id: "question-7",
          sessionId: "sess-1",
          prompt: "选哪个分支？",
          required: true,
          suggestions: ["main", "dev"],
        },
      }),
    );

    expect(items).toHaveLength(2);
    expect(items[0]).toMatchObject({
      kind: "approval",
      id: "approval-42",
      status: "pending",
      sessionId: "sess-1",
      toolName: "shell",
      expiresAt: "2026-09-13T12:30:00Z",
    });
    expect(items[1]).toMatchObject({
      kind: "question",
      id: "question-7",
      status: "pending",
      prompt: "选哪个分支？",
      required: true,
      suggestions: ["main", "dev"],
    });
  });

  it("空快照 / 缺 id 不建入口", () => {
    expect(pendingInteractionsFromRuntimeState(null)).toEqual([]);
    expect(pendingInteractionsFromRuntimeState(runtimeState())).toEqual([]);
    expect(
      pendingInteractionsFromRuntimeState(
        runtimeState({
          pendingApproval: {
            id: "  ",
            sessionId: "sess-1",
            toolName: "shell",
            reason: "",
            riskLevel: "",
          },
        }),
      ),
    ).toEqual([]);
  });
});

describe("hydratePendingInteractions", () => {
  const now = Date.parse("2026-09-13T12:05:00Z");

  it("空注册表 + 快照 → 重建未决条目", () => {
    const next = hydratePendingInteractions(
      emptyPendingInteractionState(),
      runtimeState({
        pendingApproval: {
          id: "approval-42",
          sessionId: "sess-1",
          toolName: "shell",
          reason: "rm -rf build",
          riskLevel: "high",
        },
      }),
      { now },
    );

    expect(next.items).toHaveLength(1);
    expect(next.items[0]).toMatchObject({ id: "approval-42", status: "pending" });
  });

  it("幂等：重复水合结果稳定且保留首次注册时间", () => {
    const state = runtimeState({
      pendingQuestion: {
        id: "question-7",
        sessionId: "sess-1",
        prompt: "选哪个分支？",
        required: false,
        suggestions: [],
      },
    });

    const first = hydratePendingInteractions(
      emptyPendingInteractionState(),
      state,
      { now },
    );
    const second = hydratePendingInteractions(first, state, { now });

    expect(second.items.map((item) => item.id)).toEqual(["question-7"]);
    expect(second.items[0].createdAt).toBe(first.items[0].createdAt);
  });

  it("陈旧快照不得复活已决 / 回退 resolving 条目", () => {
    let current = applyPendingInteractionEvent(
      emptyPendingInteractionState(),
      approvalRequested("approval-42", "2026-09-13T11:59:00Z"),
    );
    current = applyPendingInteractionEvent(current, {
      type: "approval_resolved",
      session_id: "sess-1",
      timestamp: "2026-09-13T12:00:01Z",
      payload: { request_id: "approval-42", allow: false },
    });

    const hydrated = hydratePendingInteractions(
      current,
      runtimeState({
        pendingApproval: {
          id: "approval-42",
          sessionId: "sess-1",
          toolName: "shell",
          reason: "rm -rf build",
          riskLevel: "high",
        },
      }),
      { now },
    );
    expect(findPendingInteraction(hydrated, "approval-42")).toMatchObject({
      status: "resolved",
      resolution: "deny",
    });

    const resolving = markPendingInteractionResolving(
      applyPendingInteractionEvent(
        emptyPendingInteractionState(),
        approvalRequested("approval-42", "2026-09-13T11:59:00Z"),
      ),
      "approval-42",
    );
    const hydratedResolving = hydratePendingInteractions(
      resolving,
      runtimeState({
        pendingApproval: {
          id: "approval-42",
          sessionId: "sess-1",
          toolName: "shell",
          reason: "rm -rf build",
          riskLevel: "high",
        },
      }),
      { now },
    );
    expect(findPendingInteraction(hydratedResolving, "approval-42")).toMatchObject(
      { status: "resolving" },
    );
  });

  it("缺席结算：早于快照时点的本地 pending 不在快照里 → 收敛为 cancelled", () => {
    const current = applyPendingInteractionEvent(
      emptyPendingInteractionState(),
      approvalRequested("approval-1", "2026-09-13T11:00:00Z"),
    );

    const next = hydratePendingInteractions(current, runtimeState(), { now });

    expect(findPendingInteraction(next, "approval-1")).toMatchObject({
      status: "cancelled",
      resolution: "snapshot_absent",
    });
  });

  it("竞态保护：晚于快照时点新建的本地 pending 不被缺席结算", () => {
    const current = applyPendingInteractionEvent(
      emptyPendingInteractionState(),
      approvalRequested("approval-late", "2026-09-13T12:00:05Z"),
    );

    const next = hydratePendingInteractions(current, runtimeState(), { now });

    expect(findPendingInteraction(next, "approval-late")?.status).toBe("pending");
  });

  it("跨会话的本地 pending 不受本会话快照影响", () => {
    const current = applyPendingInteractionEvent(
      emptyPendingInteractionState(),
      approvalRequested("approval-other", "2026-09-13T11:00:00Z", "sess-2"),
    );

    const next = hydratePendingInteractions(current, runtimeState(), { now });

    expect(findPendingInteraction(next, "approval-other")?.status).toBe("pending");
  });

  it("超时守卫：expires_at 已过的审批置 expired（30min 终态兜底）", () => {
    const next = hydratePendingInteractions(
      emptyPendingInteractionState(),
      runtimeState({
        pendingApproval: {
          id: "approval-overdue",
          sessionId: "sess-1",
          toolName: "shell",
          reason: "",
          riskLevel: "",
          expiresAt: "2026-09-13T12:00:30Z",
        },
      }),
      { now },
    );

    expect(findPendingInteraction(next, "approval-overdue")).toMatchObject({
      status: "expired",
      resolution: "expired",
    });
  });

  it("无快照（404 / 未取到）时原样返回，不清空事件流已建条目", () => {
    const current = applyPendingInteractionEvent(
      emptyPendingInteractionState(),
      approvalRequested("approval-1", "2026-09-13T11:59:00Z"),
    );

    expect(hydratePendingInteractions(current, null, { now })).toBe(current);
  });
});
