// 「会话详情」纯派生口径单测：状态映射与时间戳格式化。

import { describe, expect, it } from "vitest";

import {
  formatSessionDetailTimestamp,
  normalizeSessionDetailTags,
  resolveSessionDetailState,
} from "./session-detail-panel-shared";

describe("session-detail-panel-shared", () => {
  it("状态大小写与空白归一化后映射，未知状态回落 unknown", () => {
    expect(resolveSessionDetailState(" Closed ").labelKey).toBe(
      "panels.sessionDetail.states.closed",
    );
    expect(resolveSessionDetailState("ARCHIVED").labelKey).toBe(
      "panels.sessionDetail.states.archived",
    );
    expect(resolveSessionDetailState(undefined).labelKey).toBe(
      "panels.sessionDetail.states.unknown",
    );
    expect(resolveSessionDetailState("mystery").labelKey).toBe(
      "panels.sessionDetail.states.unknown",
    );
  });

  it("时间戳缺失 / 非法返回 null，合法值给出本地时间", () => {
    expect(formatSessionDetailTimestamp(undefined)).toBeNull();
    expect(formatSessionDetailTimestamp("   ")).toBeNull();
    expect(formatSessionDetailTimestamp("not-a-date")).toBeNull();
    expect(formatSessionDetailTimestamp("2026-09-16T03:00:00.000Z")).toBe(
      new Date("2026-09-16T03:00:00.000Z").toLocaleString(),
    );
  });

  it("标签去空去重后保持原序", () => {
    expect(normalizeSessionDetailTags(undefined)).toEqual([]);
    expect(normalizeSessionDetailTags(["a", " a ", "", "b", "b"])).toEqual([
      "a",
      "b",
    ]);
  });
});
