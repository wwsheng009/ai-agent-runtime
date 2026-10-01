import { describe, expect, it } from "vitest";

import type { LspObserveEvent } from "@/api/runtime/lsp";

import {
  coldFastFailCount,
  eventDurationLabel,
  eventOutcomeLabel,
  eventSessionId,
  eventSessionLabel,
  latestServerStates,
  recentLspEvents,
  requestOutcomeCounts,
  requestReasonCounts,
  shortEventType,
} from "./lsp-observability-shared";

function event(partial: Partial<LspObserveEvent> & { observation_seq: number; type: string }): LspObserveEvent {
  return {
    timestamp: "2026-10-01T00:00:00Z",
    ...partial,
  };
}

const FIXTURE: LspObserveEvent[] = [
  event({
    observation_seq: 9,
    type: "lsp.server.state",
    correlation: { session_id: "s1" },
    payload: { server: "gopls", state: "ready", pid: 42, first_publish_ms: 7156 },
  }),
  event({
    observation_seq: 8,
    type: "lsp.request.finished",
    correlation: { session_id: "s1" },
    payload: { outcome: "degraded_no_fresh", reason_category: "no_publish", duration_ms: 1008, cold_fast_fail: true },
  }),
  event({
    observation_seq: 7,
    type: "lsp.request.finished",
    correlation: { session_id: "s1" },
    payload: { outcome: "clean", duration_ms: 169 },
  }),
  event({
    observation_seq: 6,
    type: "lsp.request.finished",
    correlation: { session_id: "s2" },
    payload: { outcome: "clean", duration_ms: 320 },
  }),
  event({
    observation_seq: 5,
    type: "lsp.server.state",
    correlation: { session_id: "s1" },
    payload: { server: "gopls", state: "starting", reason_category: "other" },
  }),
  event({
    observation_seq: 4,
    type: "lsp.server.state",
    correlation: { session_id: "s2" },
    payload: { server: "pyright", state: "unavailable", reason_category: "binary_missing" },
  }),
];

describe("lsp-observability-shared", () => {
  it("eventSessionId 兼容 correlation 与 payload 两种落点", () => {
    expect(eventSessionId(FIXTURE[0]!)).toBe("s1");
    expect(eventSessionId(event({ observation_seq: 1, type: "x", payload: { session_id: "s9" } }))).toBe("s9");
    expect(eventSessionId(event({ observation_seq: 2, type: "x" }))).toBe("");
  });

  it("latestServerStates 每个（会话, 服务器）只取最新一条", () => {
    const rows = latestServerStates(FIXTURE);
    const byKey = new Map(rows.map((row) => [row.key, row]));
    expect(rows).toHaveLength(2);
    // s1/gopls 取 seq=9（ready + first_publish_ms），而不是 seq=5（starting）。
    expect(byKey.get("s1::gopls")?.state).toBe("ready");
    expect(byKey.get("s1::gopls")?.firstPublishMs).toBe(7156);
    expect(byKey.get("s2::pyright")?.reasonCategory).toBe("binary_missing");
    expect(byKey.get("s2::pyright")?.firstPublishMs).toBeNull();
  });

  it("计数只统计窗口内事实：outcome / reason / cold_fast_fail", () => {
    expect(requestOutcomeCounts(FIXTURE)).toEqual([
      { key: "clean", count: 2 },
      { key: "degraded_no_fresh", count: 1 },
    ]);
    expect(requestReasonCounts(FIXTURE)).toEqual([{ key: "no_publish", count: 1 }]);
    expect(coldFastFailCount(FIXTURE)).toBe(1);
  });

  it("recentLspEvents 仅截断，不改顺序", () => {
    expect(recentLspEvents(FIXTURE, 2).map((item) => item.observation_seq)).toEqual([9, 8]);
    expect(recentLspEvents(FIXTURE, 0)).toEqual([]);
  });

  it("行标签：outcome/state 回退与缺失占位", () => {
    expect(eventOutcomeLabel(FIXTURE[1]!)).toBe("degraded_no_fresh");
    expect(eventOutcomeLabel(FIXTURE[0]!)).toBe("ready");
    expect(eventOutcomeLabel(event({ observation_seq: 3, type: "x" }))).toBe("—");
    expect(eventDurationLabel(FIXTURE[1]!)).toBe("1008 ms");
    expect(eventDurationLabel(FIXTURE[0]!)).toBe("—");
    expect(eventSessionLabel(FIXTURE[4]!)).toBe("s1");
    expect(eventSessionLabel(event({ observation_seq: 3, type: "x" }))).toBe("—");
  });

  it("shortEventType 去掉 lsp. 前缀", () => {
    expect(shortEventType("lsp.request.finished")).toBe("request.finished");
    expect(shortEventType("other")).toBe("other");
  });
});
