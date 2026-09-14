// P2-1A：会话统计摘要纯逻辑单测（chip 口径 / 错误文案）。

import { type TFunction } from "i18next";
import { describe, expect, it } from "vitest";

import type { RuntimeSessionStats } from "@/types/runtime";

import {
  buildSessionStatsChips,
  describeSessionStatsError,
} from "./session-stats-summary-shared";

const t = ((key: string, options?: { count?: number }) =>
  options?.count === undefined ? key : `${key}#${options.count}`) as unknown as TFunction<"workspace">;

function stats(overrides: Partial<RuntimeSessionStats> = {}): RuntimeSessionStats {
  return {
    total: 6,
    active: 0,
    idle: 0,
    closed: 0,
    archived: 0,
    totalMessages: 0,
    tags: {},
    ...overrides,
  };
}

describe("buildSessionStatsChips", () => {
  it("total 恒定在首，非零计数按 active→idle→closed→archived→totalMessages 追加", () => {
    expect(
      buildSessionStatsChips(
        stats({ total: 6, active: 1, idle: 2, closed: 1, archived: 1, totalMessages: 12 }),
        t,
      ),
    ).toEqual([
      { key: "total", label: "sidebar.sessionStats.total#6", value: 6 },
      { key: "active", label: "sidebar.sessionStats.active#1", value: 1 },
      { key: "idle", label: "sidebar.sessionStats.idle#2", value: 2 },
      { key: "closed", label: "sidebar.sessionStats.closed#1", value: 1 },
      { key: "archived", label: "sidebar.sessionStats.archived#1", value: 1 },
      { key: "totalMessages", label: "sidebar.sessionStats.totalMessages#12", value: 12 },
    ]);
  });

  it("零计数省略（不渲染 0 噪声），total=0 时仍保留唯一 total chip", () => {
    expect(buildSessionStatsChips(stats(), t)).toEqual([
      { key: "total", label: "sidebar.sessionStats.total#6", value: 6 },
    ]);
    expect(buildSessionStatsChips(stats({ total: 0 }), t)).toEqual([
      { key: "total", label: "sidebar.sessionStats.total#0", value: 0 },
    ]);
  });
});

describe("describeSessionStatsError", () => {
  it("Error 取 message，字符串原样，其余 String 兜底", () => {
    expect(describeSessionStatsError(new Error("boom"))).toBe("boom");
    expect(describeSessionStatsError("bad gateway")).toBe("bad gateway");
    expect(describeSessionStatsError(undefined)).toBe("undefined");
  });
});
