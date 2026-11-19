import { describe, expect, it } from "vitest";

import type { RuntimeScheduleTask } from "@/types/runtime";

import { resolveScheduleTaskSession } from "./use-schedule-tasks";

function task(overrides: Partial<RuntimeScheduleTask> = {}): RuntimeScheduleTask {
  return {
    id: "task-1",
    kind: "after",
    title: "Task",
    prompt: "prompt",
    scheduledAt: "2026-10-11T12:05:00Z",
    sessionId: "",
    status: "blocked",
    ...overrides,
  };
}

describe("resolveScheduleTaskSession", () => {
  it("headless 任务没有会话时按任务 id 管理", () => {
    expect(
      resolveScheduleTaskSession(
        task({ targetKind: "headless" }),
        "",
      ),
    ).toEqual({ sessionId: "", requiresSession: false });
  });

  it("会话型任务缺少会话时明确要求会话", () => {
    expect(
      resolveScheduleTaskSession(
        task({ targetKind: "session", sessionId: "", createdBySessionId: "" }),
        "",
      ),
    ).toEqual({ sessionId: "", requiresSession: true });
  });

  it("绑定会话或创建者会话均可用于管理", () => {
    expect(
      resolveScheduleTaskSession(
        task({ targetKind: "session", sessionId: "session-1" }),
        "",
      ),
    ).toEqual({ sessionId: "session-1", requiresSession: false });
    expect(
      resolveScheduleTaskSession(
        task({
          targetKind: "headless",
          createdBySessionId: "creator-1",
        }),
        "",
      ),
    ).toEqual({ sessionId: "creator-1", requiresSession: false });
  });
});
