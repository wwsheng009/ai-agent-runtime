import { describe, expect, it } from "vitest";

import {
  collectAttentionSessionIds,
  isSessionWaitingOnUser,
  shouldOfferSessionStop,
} from "./session-attention";

describe("isSessionWaitingOnUser（等待类信号）", () => {
  it("审批 / 计划 / 回答任一命中即为「在等我」", () => {
    expect(isSessionWaitingOnUser({ pendingApprovals: 1 })).toBe(true);
    expect(isSessionWaitingOnUser({ planPending: true })).toBe(true);
    expect(isSessionWaitingOnUser({ waitingAnswer: true })).toBe(true);
  });

  it("只在跑、或没有信号，都不算「在等我」", () => {
    expect(isSessionWaitingOnUser({ running: true })).toBe(false);
    expect(isSessionWaitingOnUser({ runningAgents: 2 })).toBe(false);
    expect(isSessionWaitingOnUser(undefined)).toBe(false);
  });
});

describe("collectAttentionSessionIds（待办聚合入口）", () => {
  it("按投影顺序收集等待会话，跳过运行中与空键", () => {
    expect(
      collectAttentionSessionIds({
        "session-b": { running: true },
        "session-a": { pendingApprovals: 1 },
        "  ": { waitingAnswer: true },
        "session-c": { planPending: true, running: true },
      }),
    ).toEqual(["session-a", "session-c"]);
  });

  it("缺省活动投影返回空数组（不渲染入口）", () => {
    expect(collectAttentionSessionIds(undefined)).toEqual([]);
    expect(collectAttentionSessionIds({})).toEqual([]);
  });
});

describe("shouldOfferSessionStop（就地停止入口）", () => {
  it("运行中 / 子代理 / 等待类都提供停止入口", () => {
    expect(shouldOfferSessionStop({ running: true })).toBe(true);
    expect(shouldOfferSessionStop({ runningAgents: 1 })).toBe(true);
    expect(shouldOfferSessionStop({ waitingAnswer: true })).toBe(true);
    expect(shouldOfferSessionStop({ pendingApprovals: 2 })).toBe(true);
  });

  it("空闲会话不提供停止入口", () => {
    expect(shouldOfferSessionStop(undefined)).toBe(false);
    expect(shouldOfferSessionStop({})).toBe(false);
  });
});
