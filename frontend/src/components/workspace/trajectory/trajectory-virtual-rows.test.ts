import { describe, expect, it } from "vitest";

import { computeRowOffsets, computeVirtualWindow } from "./trajectory-virtual-rows";

describe("computeRowOffsets", () => {
  it("全部未知高度时用估算值", () => {
    const { offsets, totalHeight } = computeRowOffsets(4, new Map(), 40);
    expect(offsets).toEqual([0, 40, 80, 120]);
    expect(totalHeight).toBe(160);
  });

  it("混合已知/未知高度", () => {
    const heights = new Map<number, number>([
      [0, 100],
      [2, 20],
    ]);
    const { offsets, totalHeight } = computeRowOffsets(4, heights, 40);
    expect(offsets).toEqual([0, 100, 140, 160]);
    expect(totalHeight).toBe(200);
  });

  it("空列表", () => {
    const { offsets, totalHeight } = computeRowOffsets(0, new Map(), 40);
    expect(offsets).toEqual([]);
    expect(totalHeight).toBe(0);
  });
});

describe("computeVirtualWindow", () => {
  const heights = new Map<number, number>();
  // 100 行，每行 40px → 总高 4000
  const count = 100;

  it("空列表返回空窗口", () => {
    expect(computeVirtualWindow(0, 400, 0, heights, 40)).toEqual({
      start: 0,
      end: 0,
      totalHeight: 0,
    });
  });

  it("视口顶部：窗口从 0 开始，覆盖视口 + overscan", () => {
    const window = computeVirtualWindow(0, 400, count, heights, 40, 4);
    expect(window.start).toBe(0);
    expect(window.end).toBeGreaterThanOrEqual(10);
    expect(window.end).toBeLessThanOrEqual(14); // 10 + overscan 4
    expect(window.totalHeight).toBe(4000);
  });

  it("视口中部：窗口包含 scrollTop 所在行", () => {
    const window = computeVirtualWindow(2000, 400, count, heights, 40, 4);
    expect(window.start).toBeLessThanOrEqual(50);
    expect(window.end).toBeGreaterThanOrEqual(60);
  });

  it("视口底部：end 封顶 count", () => {
    const window = computeVirtualWindow(3900, 400, count, heights, 40, 4);
    expect(window.end).toBe(count);
    expect(window.start).toBeLessThanOrEqual(95);
  });

  it("窗口随 scrollTop 单调前进（无回跳）", () => {
    const top = computeVirtualWindow(0, 400, count, heights, 40);
    const mid = computeVirtualWindow(2000, 400, count, heights, 40);
    const bottom = computeVirtualWindow(3900, 400, count, heights, 40);
    expect(top.start).toBeLessThan(mid.start);
    expect(mid.start).toBeLessThan(bottom.start);
  });

  it("已知行高（参差）下窗口边界正确", () => {
    const varied = new Map<number, number>([
      [0, 200],
      [1, 20],
      [2, 20],
      [3, 20],
    ]);
    // offsets: 0, 200, 220, 240; total 260
    const window = computeVirtualWindow(205, 40, 4, varied, 40, 0);
    expect(window.start).toBe(1);
    expect(window.end).toBe(4); // 行2/3 在视口 205..245 内（行3 露出 5px）
  });
});
