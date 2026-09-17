import { describe, expect, it } from "vitest";

import { mergeSessionActivity, projectSessionActivity } from "./activity";
import type { SessionRuntimeEntrySnapshot } from "./types";

function snapshot(
  overrides: Partial<SessionRuntimeEntrySnapshot> & { sessionId: string },
): SessionRuntimeEntrySnapshot {
  return {
    mode: "poll",
    status: "online",
    lastSeq: 0,
    activeTurn: null,
    detached: false,
    pending: { approvals: 0, questions: 0, planPending: false },
    runningAgents: 0,
    lastEventAt: null,
    lastError: null,
    ...overrides,
  };
}

describe("projectSessionActivity（注册表条目 → 侧栏活动）", () => {
  it("无任何信号的会话不生成条目（不伪造状态）", () => {
    expect(projectSessionActivity([snapshot({ sessionId: "a" })])).toEqual({});
  });

  it("待交互与运行中按会话投影，优先级字段齐备", () => {
    const activity = projectSessionActivity([
      snapshot({
        sessionId: "session-a",
        mode: "live",
        activeTurn: { turn_id: "turn-1", status: "running" } as never,
        pending: { approvals: 2, questions: 1, planPending: true },
        runningAgents: 3,
      }),
    ]);

    expect(activity["session-a"]).toEqual({
      pendingApprovals: 2,
      planPending: true,
      waitingAnswer: true,
      running: true,
      runningAgents: 3,
    });
  });

  it("本地回合仍在跑（无服务端在途回合）也算运行中", () => {
    const activity = projectSessionActivity(
      [snapshot({ sessionId: "session-a" })],
      { runningSessionIds: ["session-a"] },
    );

    expect(activity["session-a"]).toEqual({ running: true });
  });

  it("本地在途但注册表还没条目 → 仍显示运行中（切走不闪回空闲）", () => {
    const activity = projectSessionActivity([], {
      runningSessionIds: ["session-a"],
    });

    expect(activity["session-a"]).toEqual({ running: true });
  });

  it("会话身份按 normalizeSessionId 对齐（大小写 / 空白）", () => {
    const activity = projectSessionActivity(
      [snapshot({ sessionId: " Session-A " })],
      { runningSessionIds: ["session-a"] },
    );

    expect(Object.keys(activity)).toEqual(["session-a"]);
  });
});

describe("mergeSessionActivity", () => {
  it("后者覆盖同名字段，其余字段保留", () => {
    expect(
      mergeSessionActivity(
        { a: { running: true, runningAgents: 2 }, b: { planPending: true } },
        { a: { running: false }, c: { waitingAnswer: true } },
      ),
    ).toEqual({
      a: { running: false, runningAgents: 2 },
      b: { planPending: true },
      c: { waitingAnswer: true },
    });
  });

  it("缺省入参被忽略", () => {
    expect(mergeSessionActivity(undefined, { a: { running: true } })).toEqual({
      a: { running: true },
    });
  });
});
