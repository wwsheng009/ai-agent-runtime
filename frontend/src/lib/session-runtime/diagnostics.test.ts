// Batch 4（§4.6）：注册表观测投影的纯函数单测。
//
// 面板只负责显示，口径全在这里：live / poll / idle 怎么数、本会话模式怎么取、
// 预算与可见性怎么带出。与后端 `active_connections` 对账时以 `live` 为准。

import { describe, expect, it } from "vitest";

import { summarizeSessionRuntimeEntries } from "@/lib/session-runtime/diagnostics";
import type { SessionRuntimeEntrySnapshot } from "@/lib/session-runtime/types";

function entry(
  sessionId: string,
  mode: SessionRuntimeEntrySnapshot["mode"],
): SessionRuntimeEntrySnapshot {
  return {
    sessionId,
    mode,
    status: mode === "idle" ? "idle" : "online",
    lastSeq: 0,
    activeTurn: null,
    detached: false,
    pending: { approvals: 0, questions: 0, planPending: false },
    runningAgents: 0,
    lastEventAt: null,
    lastError: null,
  };
}

describe("summarizeSessionRuntimeEntries", () => {
  it("空集合：全部为 0，且不报告任何会话模式", () => {
    const summary = summarizeSessionRuntimeEntries([], { sessionId: "s1" });

    expect(summary).toMatchObject({
      total: 0,
      live: 0,
      poll: 0,
      idle: 0,
      sessionMode: null,
      pageHidden: false,
    });
  });

  it("按模式分类计数，并给出本会话的订阅强度", () => {
    const summary = summarizeSessionRuntimeEntries(
      [entry("s1", "live"), entry("s2", "poll"), entry("s3", "poll")],
      { sessionId: "s1" },
    );

    expect(summary).toMatchObject({
      total: 3,
      live: 1,
      poll: 2,
      idle: 0,
      sessionMode: "live",
    });
  });

  it("本会话不在订阅集合里时为 null（面板显示「未订阅」而不是抛错）", () => {
    const summary = summarizeSessionRuntimeEntries([entry("s1", "live")], {
      sessionId: "s9",
    });

    expect(summary.sessionMode).toBeNull();
  });

  it("缺省预算取 flags 常量（前台 1 + 后台 2），可见性缺省为前台", () => {
    const summary = summarizeSessionRuntimeEntries([entry("s1", "live")]);

    expect(summary.foregroundBudget).toBe(1);
    expect(summary.backgroundBudget).toBe(2);
    expect(summary.pageHidden).toBe(false);
  });

  it("显式传入预算与隐藏状态时原样带出（供降采样诊断）", () => {
    const summary = summarizeSessionRuntimeEntries([entry("s1", "poll")], {
      pageHidden: true,
      foregroundBudget: 0,
      backgroundBudget: 5,
    });

    expect(summary).toMatchObject({
      pageHidden: true,
      foregroundBudget: 0,
      backgroundBudget: 5,
    });
  });

  it("sessionId 为空白时不参与匹配，避免把空串会话误判为已订阅", () => {
    const withBlank = summarizeSessionRuntimeEntries(
      [entry("", "live"), entry("s1", "poll")],
      { sessionId: "   " },
    );

    expect(withBlank.sessionMode).toBeNull();
    expect(withBlank.live).toBe(1);

    const exact = summarizeSessionRuntimeEntries(
      [entry("", "live"), entry("s1", "poll")],
      { sessionId: "s1" },
    );

    expect(exact.sessionMode).toBe("poll");
  });
});
