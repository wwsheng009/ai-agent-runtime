import { describe, expect, it } from "vitest";

import type { TrajectoryItem, TrajectoryItemStatus } from "./types";
import {
  MIN_SPAN_RATIO,
  bucketTrajectoryItems,
  buildTrajectoryTimelineAxis,
  clampTrajectoryWindow,
  dominantTrajectoryStatus,
  formatTrajectoryAxisTick,
  formatTrajectoryDuration,
  fullTrajectoryWindow,
  isFullTrajectoryWindow,
  panTrajectoryWindow,
  trailingTrajectoryWindow,
  trajectoryStatusCounts,
  trajectoryTimelineTicks,
  trajectoryWindowSpan,
  zoomTrajectoryWindow,
  zoomTrajectoryWindowAtCenter,
} from "./timeline-window";

function item(
  index: number,
  overrides: Partial<TrajectoryItem> = {},
): TrajectoryItem {
  return {
    id: `item-${index}`,
    seq: index + 1,
    kind: "assistant",
    causeId: "",
    status: "completed",
    head: { kind: "text", content: `content ${index}` },
    createdAt: index + 1,
    updatedAt: index + 1,
    ...overrides,
  };
}

/** 651 条真实规模数据（时间递增 100ms，第 400 条失败）。 */
function denseItems(): TrajectoryItem[] {
  return Array.from({ length: 651 }, (_, index) =>
    item(index, {
      at: 1_700_000_000_000 + index * 100,
      status: index === 400 ? "failed" : "completed",
      kind: index % 3 === 0 ? "tool" : "assistant",
    }),
  );
}

describe("buildTrajectoryTimelineAxis", () => {
  it("无时间戳时退化为序号轴", () => {
    const items = [item(0), item(1), item(2)];
    const axis = buildTrajectoryTimelineAxis(items);

    expect(axis.kind).toBe("ordinal");
    expect(axis.hasTime).toBe(false);
    expect(axis.positions).toEqual([0, 1, 2]);
    expect(axis.max).toBe(2);
  });

  it("时间戳充足时按时间轴分布（不再等距）", () => {
    const items = [
      item(0, { at: 1000 }),
      item(1, { at: 2000 }),
      item(2, { at: 10_000 }),
    ];
    const axis = buildTrajectoryTimelineAxis(items);

    expect(axis.kind).toBe("time");
    expect(axis.hasTime).toBe(true);
    expect(axis.min).toBe(1000);
    expect(axis.max).toBe(10_000);
    // 前两条事件贴近轴起点（时间上确实靠在一起）。
    const ratioOfThird = (axis.positions[2] - axis.min) / (axis.max - axis.min);
    expect(ratioOfThird).toBe(1);
    expect((axis.positions[1] - axis.min) / (axis.max - axis.min)).toBeLessThan(0.2);
  });

  it("个别 item 缺时间时按相邻已知时间插值（不堆到起点）", () => {
    const items = [
      item(0, { at: 1000 }),
      item(1),
      item(2, { at: 3000 }),
    ];
    const axis = buildTrajectoryTimelineAxis(items);

    expect(axis.positions).toEqual([1000, 2000, 3000]);
  });

  it("头部/尾部缺时间时沿用最近已知时间", () => {
    const items = [item(0), item(1, { at: 1000 }), item(2, { at: 2000 }), item(3)];
    const axis = buildTrajectoryTimelineAxis(items);

    expect(axis.positions).toEqual([1000, 1000, 2000, 2000]);
    expect(fullTrajectoryWindow(axis)).toEqual({ start: 1000, end: 2000 });
  });

  it("只有一个已知时间时退化为序号轴（避免所有条挤在同一点）", () => {
    const axis = buildTrajectoryTimelineAxis([item(0, { at: 42 })]);

    expect(axis.kind).toBe("ordinal");
    expect(axis.hasTime).toBe(false);
    expect(axis.positions).toEqual([0]);
    expect(fullTrajectoryWindow(axis)).toEqual({ start: 0, end: 1 });
  });

  it("所有时间相同时退化为序号轴", () => {
    const axis = buildTrajectoryTimelineAxis([
      item(0, { at: 5000 }),
      item(1, { at: 5000 }),
    ]);

    expect(axis.kind).toBe("ordinal");
    expect(axis.positions).toEqual([0, 1]);
  });

  it("空列表返回占位轴", () => {
    const axis = buildTrajectoryTimelineAxis([]);
    expect(axis.positions).toEqual([]);
    expect(axis.max).toBeGreaterThan(axis.min);
  });
});

describe("窗口缩放/平移", () => {
  const axis = buildTrajectoryTimelineAxis([
    item(0, { at: 0 }),
    item(1, { at: 1000 }),
  ]);

  it("放大后跨度变小、锚点位置保持", () => {
    const full = fullTrajectoryWindow(axis);
    const zoomed = zoomTrajectoryWindow(axis, full, 4, 1);

    expect(trajectoryWindowSpan(zoomed)).toBeCloseTo(250, 6);
    // 锚点在右端（ratio=1）：末端保持不动。
    expect(zoomed.end).toBeCloseTo(1000, 6);
    expect(zoomed.start).toBeCloseTo(750, 6);
  });

  it("左端锚点放大时起点不动", () => {
    const full = fullTrajectoryWindow(axis);
    const zoomed = zoomTrajectoryWindow(axis, full, 2, 0);
    expect(zoomed.start).toBeCloseTo(0, 6);
    expect(zoomed.end).toBeCloseTo(500, 6);
  });

  it("放大有上限（MIN_SPAN_RATIO）", () => {
    let window = fullTrajectoryWindow(axis);
    for (let index = 0; index < 50; index += 1) {
      window = zoomTrajectoryWindowAtCenter(axis, window, 2);
    }
    expect(trajectoryWindowSpan(window)).toBeGreaterThanOrEqual(
      1000 * MIN_SPAN_RATIO - 1e-6,
    );
    expect(trajectoryWindowSpan(window)).toBeLessThanOrEqual(1000);
  });

  it("缩小不会超出全轴", () => {
    const full = fullTrajectoryWindow(axis);
    const zoomed = zoomTrajectoryWindowAtCenter(axis, full, 0.5);
    expect(zoomed).toEqual({ start: 0, end: 1000 });
    expect(isFullTrajectoryWindow(axis, zoomed)).toBe(true);
  });

  it("平移在轴范围内夹紧", () => {
    const zoomed = zoomTrajectoryWindowAtCenter(axis, fullTrajectoryWindow(axis), 4);
    const pannedRight = panTrajectoryWindow(axis, zoomed, 10);
    expect(pannedRight.end).toBeCloseTo(1000, 6);
    expect(pannedRight.start).toBeCloseTo(750, 6);

    const pannedLeft = panTrajectoryWindow(axis, zoomed, -10);
    expect(pannedLeft.start).toBeCloseTo(0, 6);
    expect(pannedLeft.end).toBeCloseTo(250, 6);
  });

  it("非法数值输入被夹紧而不是产生 NaN", () => {
    const clamped = clampTrajectoryWindow(axis, { start: Number.NaN, end: Number.NaN });
    expect(Number.isFinite(clamped.start)).toBe(true);
    expect(Number.isFinite(clamped.end)).toBe(true);
  });

  it("尾部窗口：锚定轴终点回溯预设跨度，超过全轴时退化为全览", () => {
    const trailing = trailingTrajectoryWindow(axis, 400);
    expect(trailing).toEqual({ start: 600, end: 1000 });
    expect(isFullTrajectoryWindow(axis, trailing)).toBe(false);

    // 预设比全轴还长 / 非法：全览（不产生空窗口）。
    expect(trailingTrajectoryWindow(axis, 5000)).toEqual({ start: 0, end: 1000 });
    expect(trailingTrajectoryWindow(axis, 0)).toEqual({ start: 0, end: 1000 });
    expect(trailingTrajectoryWindow(axis, Number.NaN)).toEqual({
      start: 0,
      end: 1000,
    });
  });
});

describe("bucketTrajectoryItems", () => {
  it("651 条事件聚合到有限桶内，计数守恒", () => {
    const items = denseItems();
    const axis = buildTrajectoryTimelineAxis(items);
    const buckets = bucketTrajectoryItems(
      items,
      axis.positions,
      fullTrajectoryWindow(axis),
      120,
    );

    expect(buckets.length).toBeLessThanOrEqual(120);
    expect(buckets.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(651);
    // 桶按位置升序（渲染顺序稳定）。
    for (let index = 1; index < buckets.length; index += 1) {
      expect(buckets[index].left).toBeGreaterThan(buckets[index - 1].left);
    }
  });

  it("主导状态按优先级：桶内出现 failed 即标红", () => {
    const items = [
      item(0, { at: 0, status: "completed" }),
      item(1, { at: 10, status: "completed" }),
      item(2, { at: 20, status: "failed" }),
      item(3, { at: 30, status: "running" }),
    ];
    const axis = buildTrajectoryTimelineAxis(items);
    const buckets = bucketTrajectoryItems(
      items,
      axis.positions,
      fullTrajectoryWindow(axis),
      1,
    );

    expect(buckets).toHaveLength(1);
    expect(buckets[0].status).toBe("failed");
    expect(buckets[0].statusCounts).toMatchObject({
      completed: 2,
      failed: 1,
      running: 1,
    });
    expect(buckets[0].firstItemId).toBe("item-0");
  });

  it("缩放窗口只聚合窗口内 item（窗口外不进入桶）", () => {
    const items = [
      item(0, { at: 0 }),
      item(1, { at: 500 }),
      item(2, { at: 1000 }),
    ];
    const axis = buildTrajectoryTimelineAxis(items);
    const buckets = bucketTrajectoryItems(items, axis.positions, { start: 400, end: 600 }, 40);

    expect(buckets.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(1);
    expect(buckets[0].items[0].id).toBe("item-1");
  });

  it("空桶不渲染（时间空档可见）", () => {
    const items = [item(0, { at: 0 }), item(1, { at: 10_000 })];
    const axis = buildTrajectoryTimelineAxis(items);
    const buckets = bucketTrajectoryItems(
      items,
      axis.positions,
      fullTrajectoryWindow(axis),
      10,
    );

    expect(buckets).toHaveLength(2);
    expect(buckets[0].left).toBeCloseTo(0, 6);
    expect(buckets[1].left).toBeCloseTo(0.9, 6);
  });

  it("dominantTrajectoryStatus 空集合兜底 completed", () => {
    expect(dominantTrajectoryStatus({})).toBe("completed");
    expect(dominantTrajectoryStatus({ canceled: 1 })).toBe("canceled");
  });
});

describe("刻度与图例", () => {
  it("时间轴刻度等分窗口并输出时钟文案", () => {
    const axis = buildTrajectoryTimelineAxis([
      item(0, { at: Date.UTC(2026, 0, 2, 3, 0, 0) }),
      item(1, { at: Date.UTC(2026, 0, 2, 3, 0, 4) }),
    ]);
    const ticks = trajectoryTimelineTicks(axis, fullTrajectoryWindow(axis), [], 4);

    expect(ticks).toHaveLength(5);
    expect(ticks[0].ratio).toBe(0);
    expect(ticks[4].ratio).toBe(1);
    // 本地时区不同 → 只校验格式与单调性。
    expect(ticks[0].label).toMatch(/^\d{2}:\d{2}:\d{2}$/);
    expect(ticks[4].value - ticks[0].value).toBe(4000);
  });

  it("序号轴刻度输出 #seq", () => {
    const items = [item(0, { seq: 7 }), item(1, { seq: 9 })];
    const axis = buildTrajectoryTimelineAxis(items);
    expect(formatTrajectoryAxisTick(axis, 1, items)).toBe("#9");
  });

  it("跨天时间轴刻度带日期", () => {
    const axis = buildTrajectoryTimelineAxis([
      item(0, { at: Date.UTC(2026, 0, 2, 0, 0, 0) }),
      item(1, { at: Date.UTC(2026, 0, 4, 0, 0, 0) }),
    ]);
    const label = formatTrajectoryAxisTick(axis, Date.UTC(2026, 0, 3, 5, 6, 7), []);
    expect(label).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
  });

  it("状态计数覆盖全部状态键", () => {
    const counts = trajectoryStatusCounts([
      item(0, { status: "completed" }),
      item(1, { status: "completed" }),
      item(2, { status: "failed" }),
    ]);
    const statuses: TrajectoryItemStatus[] = [
      "pending",
      "running",
      "completed",
      "failed",
      "canceled",
    ];
    for (const status of statuses) {
      expect(typeof counts[status]).toBe("number");
    }
    expect(counts.completed).toBe(2);
    expect(counts.failed).toBe(1);
    expect(counts.running).toBe(0);
  });

  it("跨度文案可读", () => {
    expect(formatTrajectoryDuration(0)).toBe("0s");
    expect(formatTrajectoryDuration(45_000)).toBe("45s");
    expect(formatTrajectoryDuration(125_000)).toBe("2m5s");
    expect(formatTrajectoryDuration(3 * 3600_000 + 600_000)).toBe("3h10m");
  });
});
