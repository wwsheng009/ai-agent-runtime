import { beforeEach, describe, expect, it } from "vitest";

import {
  MAX_SESSION_RUNTIME_NOTICES,
  clearSessionRuntimeNotices,
  diffSessionRuntimeNotices,
  dismissSessionRuntimeNotice,
  dismissSessionRuntimeNoticesForSession,
  getSessionRuntimeNoticesSnapshot,
  pushSessionRuntimeNotices,
  subscribeSessionRuntimeNotices,
  type SessionRuntimeNotice,
} from "./notices";
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

function previous(
  snapshots: readonly SessionRuntimeEntrySnapshot[],
): Map<string, SessionRuntimeEntrySnapshot> {
  return new Map(snapshots.map((item) => [item.sessionId, item]));
}

/** 到终态（activeTurn 清空）的后台回合：上一次有在途回合、本次没有。 */
const running = snapshot({
  sessionId: "session-a",
  mode: "live",
  activeTurn: { turn_id: "turn-1", status: "running" } as never,
});

describe("diffSessionRuntimeNotices（条目变化 → 应提示的通知）", () => {
  it("后台回合收尾 → turn_finished；选中会话不提示", () => {
    const finished = snapshot({ sessionId: "session-a" });

    expect(
      diffSessionRuntimeNotices(previous([running]), [finished], { now: 1 }),
    ).toEqual([
      {
        id: "session-a#turn_finished",
        sessionId: "session-a",
        kind: "turn_finished",
        createdAt: 1,
      },
    ]);

    expect(
      diffSessionRuntimeNotices(previous([running]), [finished], {
        selectedSessionId: "session-a",
        now: 1,
      }),
    ).toEqual([]);
  });

  it("待审批 / 待回答 / 计划评审从无到有才提示，数量增长不重复", () => {
    const first = snapshot({
      sessionId: "session-a",
      pending: { approvals: 1, questions: 1, planPending: true },
    });
    const grown = snapshot({
      sessionId: "session-a",
      pending: { approvals: 3, questions: 2, planPending: true },
    });

    expect(
      diffSessionRuntimeNotices(new Map(), [first], { now: 5 }).map(
        (notice) => notice.kind,
      ),
    ).toEqual(["approval", "plan_review", "question"]);

    expect(
      diffSessionRuntimeNotices(previous([first]), [grown], { now: 6 }),
    ).toEqual([]);
  });

  it("上一份快照缺失时按零基线比较：新出现的等待类照常提示", () => {
    const pendingApproval = snapshot({
      // 归一化口径与后端 `chat.NormalizeSessionID` 一致：去空白 + 取路径末段（大小写保留）。
      sessionId: " E:/work/session-b/ ",
      pending: { approvals: 1, questions: 0, planPending: false },
    });

    expect(diffSessionRuntimeNotices(new Map(), [pendingApproval])).toEqual([
      {
        id: "session-b#approval",
        sessionId: "session-b",
        kind: "approval",
        createdAt: expect.any(Number),
      },
    ]);
  });

  it("空 sessionId 不产通知（不伪造身份）", () => {
    expect(
      diffSessionRuntimeNotices(new Map(), [
        snapshot({ sessionId: "   ", pending: { approvals: 1, questions: 0, planPending: false } }),
      ]),
    ).toEqual([]);
  });
});

describe("会话运行时通知 store", () => {
  beforeEach(() => {
    clearSessionRuntimeNotices();
  });

  function notice(
    sessionId: string,
    kind: SessionRuntimeNotice["kind"],
  ): SessionRuntimeNotice {
    return {
      id: `${sessionId}#${kind}`,
      sessionId,
      kind,
      createdAt: 0,
    };
  }

  it("同会话同类去重置顶，总量按上限截断", () => {
    pushSessionRuntimeNotices([
      notice("session-a", "turn_finished"),
      notice("session-b", "approval"),
      notice("session-c", "question"),
      notice("session-d", "plan_review"),
    ]);

    const snapshot = getSessionRuntimeNoticesSnapshot();
    expect(snapshot).toHaveLength(MAX_SESSION_RUNTIME_NOTICES);
    expect(snapshot.map((item) => item.sessionId)).toEqual([
      "session-a",
      "session-b",
      "session-c",
    ]);

    // 重复推送同一 (会话, 类型) 不再新增，也不改变顺序。
    pushSessionRuntimeNotices([notice("session-a", "turn_finished")]);
    expect(getSessionRuntimeNoticesSnapshot()).toHaveLength(
      MAX_SESSION_RUNTIME_NOTICES,
    );
  });

  it("订阅者在 push / dismiss 时收到通知，快照引用随之变化", () => {
    const before = getSessionRuntimeNoticesSnapshot();
    let calls = 0;
    const unsubscribe = subscribeSessionRuntimeNotices(() => {
      calls += 1;
    });

    pushSessionRuntimeNotices([notice("session-a", "approval")]);
    const afterPush = getSessionRuntimeNoticesSnapshot();
    expect(calls).toBe(1);
    expect(afterPush).not.toBe(before);

    dismissSessionRuntimeNotice("session-a#approval");
    expect(calls).toBe(2);
    expect(getSessionRuntimeNoticesSnapshot()).toEqual([]);

    unsubscribe();
    pushSessionRuntimeNotices([notice("session-b", "question")]);
    expect(calls).toBe(2);
  });

  it("切回会话清掉该会话的通知（其它会话保留）", () => {
    pushSessionRuntimeNotices([
      notice("session-a", "approval"),
      notice("session-b", "turn_finished"),
    ]);

    // 切回入口传进来的是各种字符串变体（URL 参数 / 路径形态），归一化后再比对。
    dismissSessionRuntimeNoticesForSession(" dir/session-a ");
    expect(
      getSessionRuntimeNoticesSnapshot().map((item) => item.sessionId),
    ).toEqual(["session-b"]);
  });
});
