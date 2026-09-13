// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  RuntimeSessionPlanMode,
  SessionRuntimeEvent,
} from "@/lib/runtime-api";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    answerSessionQuestion: vi.fn(),
    resolveSessionToolApproval: vi.fn(),
  };
});

import {
  answerSessionQuestion,
  resolveSessionToolApproval,
} from "@/lib/runtime-api";

import { usePendingInteractions } from "./use-pending-interactions";

const mockResolveApproval = vi.mocked(resolveSessionToolApproval);
const mockAnswerQuestion = vi.mocked(answerSessionQuestion);

type HookSnapshot = ReturnType<typeof usePendingInteractions>;
type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function runtimeEvent(
  type: string,
  payload: Record<string, unknown>,
  sessionId?: string,
): SessionRuntimeEvent {
  return {
    type,
    timestamp: "2026-09-11T00:00:00Z",
    ...(sessionId ? { session_id: sessionId } : {}),
    payload,
  };
}

function approvalRequested(sessionId: string, requestId = "req-1") {
  return runtimeEvent(
    "approval_requested",
    {
      request_id: requestId,
      tool_name: "shell_exec",
      reason: "run tests",
      risk_level: "medium",
    },
    sessionId,
  );
}

function activePlan(sessionId: string): RuntimeSessionPlanMode {
  return {
    session_id: sessionId,
    active: true,
    status: "active",
    permission_mode: "plan",
    plan_path: "docs/plan/example.md",
    notes: "looks good",
    plan_content: "# plan",
    plan_content_available: true,
  };
}

function Harness({
  sessionId,
  plan,
  onSnapshot,
}: {
  sessionId?: string;
  plan?: RuntimeSessionPlanMode | null;
  onSnapshot: (snapshot: HookSnapshot) => void;
}) {
  const snapshot = usePendingInteractions({ sessionId, plan });
  onSnapshot(snapshot);
  return null;
}

describe("usePendingInteractions", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mockResolveApproval.mockReset();
    mockAnswerQuestion.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderHook(
    sessionId: string | undefined,
    plan?: RuntimeSessionPlanMode | null,
  ) {
    let latest: HookSnapshot | null = null;
    act(() => {
      root.render(
        <Harness
          sessionId={sessionId}
          plan={plan}
          onSnapshot={(snapshot) => {
            latest = snapshot;
          }}
        />,
      );
    });
    return {
      get current(): HookSnapshot {
        if (!latest) {
          throw new Error("hook not rendered");
        }
        return latest;
      },
      update(next: {
        sessionId?: string;
        plan?: RuntimeSessionPlanMode | null;
      }) {
        act(() => {
          root.render(
            <Harness
              sessionId={next.sessionId}
              plan={next.plan}
              onSnapshot={(snapshot) => {
                latest = snapshot;
              }}
            />,
          );
        });
      },
    };
  }

  it("registers approval events and only surfaces the attached session", () => {
    const hook = renderHook("session-1");

    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-1"));
    });
    expect(hook.current.pending?.kind).toBe("approval");
    expect(hook.current.pending?.status).toBe("pending");
    expect(
      hook.current.pending?.kind === "approval"
        ? hook.current.pending.toolName
        : "",
    ).toBe("shell_exec");

    // 注册在另一会话的条目不应抢占当前会话的呈现位。
    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-2", "req-2"));
    });
    expect(hook.current.pending?.id).toBe("req-1");

    hook.update({ sessionId: "session-2" });
    expect(hook.current.pending?.id).toBe("req-2");
  });

  it("submits an approval decision with a cancellation signal and settles it", async () => {
    mockResolveApproval.mockResolvedValue({});
    const hook = renderHook("session-1");
    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-1"));
    });

    let result = false;
    await act(async () => {
      result = await hook.current.resolveApproval("req-1", true);
    });

    expect(result).toBe(true);
    expect(mockResolveApproval).toHaveBeenCalledWith(
      "session-1",
      { requestId: "req-1", allow: true },
      { signal: expect.any(AbortSignal) },
    );
    expect(hook.current.pending).toBeNull();
  });

  it("keeps the entry actionable with an error when the decision fails", async () => {
    mockResolveApproval.mockRejectedValueOnce(new Error("network down"));
    const hook = renderHook("session-1");
    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-1"));
    });

    await act(async () => {
      expect(await hook.current.resolveApproval("req-1", false)).toBe(false);
    });

    expect(hook.current.pending?.status).toBe("pending");
    expect(hook.current.pending?.error).toBe("network down");

    // 失败后可重试：回填终态后不再呈现。
    mockResolveApproval.mockResolvedValueOnce({});
    await act(async () => {
      expect(await hook.current.resolveApproval("req-1", false)).toBe(true);
    });
    expect(hook.current.pending).toBeNull();
  });

  it("answers questions through the same lifecycle", async () => {
    mockAnswerQuestion.mockResolvedValue({});
    const hook = renderHook("session-1");
    act(() => {
      hook.current.applyRuntimeEvent(
        runtimeEvent(
          "question_asked",
          {
            question_id: "q-1",
            prompt: "which branch?",
            required: true,
            suggestions: ["main", "dev"],
          },
          "session-1",
        ),
      );
    });

    expect(hook.current.pending?.kind).toBe("question");
    expect(
      hook.current.pending?.kind === "question"
        ? hook.current.pending.suggestions
        : [],
    ).toEqual(["main", "dev"]);

    await act(async () => {
      expect(await hook.current.answerQuestion("q-1", "main")).toBe(true);
    });
    expect(mockAnswerQuestion).toHaveBeenCalledWith(
      "session-1",
      { questionId: "q-1", answer: "main" },
      { signal: expect.any(AbortSignal) },
    );
    expect(hook.current.pending).toBeNull();
  });

  it("converges pending entries on session termination events", () => {
    const hook = renderHook("session-1");
    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-1"));
    });
    expect(hook.current.pending).not.toBeNull();

    act(() => {
      hook.current.applyRuntimeEvent(
        runtimeEvent("session_end", { reason: "closed" }, "session-1"),
      );
    });
    expect(hook.current.pending).toBeNull();
  });

  it("projects an active plan into the slot and yields to real interactions", () => {
    const hook = renderHook("session-1", activePlan("session-1"));

    expect(hook.current.pending?.kind).toBe("plan_review");
    expect(hook.current.pending?.id).toBe("plan-review:session-1");

    // 工具审批阻塞优先于恒存的计划评审投影。
    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-1"));
    });
    expect(hook.current.pending?.kind).toBe("approval");

    // 计划退出（投影消失）后审批仍是唯一呈现。
    act(() => {
      hook.update({ sessionId: "session-1", plan: null });
    });
    expect(hook.current.pending?.kind).toBe("approval");
  });

  it("converge() clears the slate for the given session", () => {
    const hook = renderHook("session-1");
    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-1"));
    });

    act(() => {
      hook.current.converge("stream_lost");
    });
    expect(hook.current.pending).toBeNull();
  });

  it("aborts in-flight decisions when unmounted", async () => {
    let capturedSignal: AbortSignal | undefined;
    mockResolveApproval.mockImplementation(
      (_sessionId, _request, options) =>
        new Promise<Record<string, unknown>>((_resolve, reject) => {
          capturedSignal = options?.signal;
          capturedSignal?.addEventListener("abort", () =>
            reject(new Error("aborted")),
          );
        }),
    );
    const hook = renderHook("session-1");
    act(() => {
      hook.current.applyRuntimeEvent(approvalRequested("session-1"));
    });

    let decision: Promise<boolean> | null = null;
    act(() => {
      decision = hook.current.resolveApproval("req-1", true);
    });
    act(() => {
      root.unmount();
    });

    expect(capturedSignal?.aborted).toBe(true);
    expect(await decision).toBe(false);
  });
});
