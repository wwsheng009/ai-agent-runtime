import { describe, expect, it } from "vitest";

import { buildRecentSessionBuckets } from "@/lib/workspace/session-recency";
import type { RuntimeSessionRecord } from "@/types/runtime";

function session(id: string, updatedAt: string): RuntimeSessionRecord {
  return { id, updatedAt } as RuntimeSessionRecord;
}

describe("buildRecentSessionBuckets", () => {
  const now = new Date(2026, 10, 18, 12, 0, 0);

  it("把跨目录会话按今天 / 昨天分桶并按更新时间倒序", () => {
    const buckets = buildRecentSessionBuckets(
      [
        session("today-old", new Date(2026, 10, 18, 9, 0, 0).toISOString()),
        session("today-new", new Date(2026, 10, 18, 11, 0, 0).toISOString()),
        session("yesterday", new Date(2026, 10, 17, 20, 0, 0).toISOString()),
        session("older", new Date(2026, 10, 15, 20, 0, 0).toISOString()),
      ],
      { now },
    );

    expect(buckets.map((bucket) => bucket.id)).toEqual(["today", "yesterday"]);
    expect(buckets[0]?.sessions.map((item) => item.id)).toEqual([
      "today-new",
      "today-old",
    ]);
    expect(buckets[1]?.sessions.map((item) => item.id)).toEqual([
      "yesterday",
    ]);
  });

  it("每个桶最多取 limitPerBucket 条，忽略无时间戳会话", () => {
    const buckets = buildRecentSessionBuckets(
      [
        session("a", new Date(2026, 10, 18, 11, 0, 0).toISOString()),
        session("b", new Date(2026, 10, 18, 10, 0, 0).toISOString()),
        session("c", new Date(2026, 10, 18, 9, 0, 0).toISOString()),
        session("missing", ""),
      ],
      { now, limitPerBucket: 2 },
    );

    expect(buckets[0]?.sessions.map((item) => item.id)).toEqual(["a", "b"]);
  });

  it("没有今天 / 昨天的会话时返回空数组", () => {
    const buckets = buildRecentSessionBuckets(
      [session("older", new Date(2026, 10, 10, 9, 0, 0).toISOString())],
      { now },
    );

    expect(buckets).toEqual([]);
  });
});