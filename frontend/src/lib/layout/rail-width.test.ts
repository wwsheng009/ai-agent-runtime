// @vitest-environment node
//
// 右栏宽度纯函数单测：覆盖 auto/manual 两条规则、主区保护上界与覆盖层降级。
// 其中「auto + 内容型面 = 288px」是 P0-2 的回归红线 ④（与改造前 18rem 逐像素一致）。

import { describe, expect, it } from "vitest";

import {
  RAIL_GRID_MIN_VIEWPORT_PX,
  RAIL_WIDTH_CONTENT_PX,
  RAIL_WIDTH_MAX_PX,
  RAIL_WIDTH_MIN_PX,
  RAIL_WIDTH_OVERLAY_MAX_RATIO,
  RAIL_WIDTH_WIDE_MAX_PX,
  RAIL_WIDTH_WIDE_MIN_PX,
  clampRailWidth,
  isRailOverlayViewport,
  resolveRailOverlayWidth,
  resolveRailWidth,
  resolveRailWidthMax,
} from "./rail-width";

describe("rail-width 常量", () => {
  it("尺寸口径与规划文档 §4.2 一致", () => {
    expect(RAIL_WIDTH_CONTENT_PX).toBe(288);
    expect(RAIL_WIDTH_MIN_PX).toBe(320);
    expect(RAIL_WIDTH_MAX_PX).toBe(832);
    expect(RAIL_WIDTH_WIDE_MIN_PX).toBe(416);
    expect(RAIL_WIDTH_WIDE_MAX_PX).toBe(672);
    expect(RAIL_WIDTH_OVERLAY_MAX_RATIO).toBe(0.92);
    expect(RAIL_GRID_MIN_VIEWPORT_PX).toBe(1280);
  });
});

describe("resolveRailWidth · auto", () => {
  it("内容型面在任何视口下都是 288px（回归红线 ④：不被 minWidth 抬到 320）", () => {
    const viewports = [1280, 1440, 1920, 2560, 1024, 900, 640];

    for (const viewportWidth of viewports) {
      expect(
        resolveRailWidth({
          mode: "auto",
          widthPx: 700,
          surface: "content",
          viewportWidth,
        }),
      ).toBe(288);
    }
  });

  it("宽内容面 = clamp(0.32 × vw, 416, 672)", () => {
    const width = (viewportWidth: number) =>
      resolveRailWidth({
        mode: "auto",
        widthPx: 0,
        surface: "wide",
        viewportWidth,
      });

    // 0.32 × 1280 = 409.6 → 下界 416
    expect(width(1280)).toBe(416);
    // 0.32 × 1600 = 512（区间内）
    expect(width(1600)).toBe(512);
    // 0.32 × 2048 = 655（区间内，四舍五入）
    expect(width(2048)).toBe(655);
    // 0.32 × 2560 = 819.2 → 上界 672
    expect(width(2560)).toBe(672);
  });

  it("宽内容面同样受主区保护上界收窄（1100px 视口只能给 332px）", () => {
    expect(
      resolveRailWidth({
        mode: "auto",
        widthPx: 0,
        surface: "wide",
        viewportWidth: 1100,
      }),
    ).toBe(332);
  });
});

describe("resolveRailWidth · manual", () => {
  it("用户宽度过 clampRailWidth，视口偏小时只做显示收窄", () => {
    const input = { mode: "manual", widthPx: 832, surface: "content" } as const;

    expect(resolveRailWidth({ ...input, viewportWidth: 1920 })).toBe(832);
    expect(resolveRailWidth({ ...input, viewportWidth: 1600 })).toBe(832);
    // 1600 − 256 − 512 = 832；1280 − 256 − 512 = 512
    expect(resolveRailWidth({ ...input, viewportWidth: 1280 })).toBe(512);
  });

  it("低于下界的持久化值被抬到 320px（不会把主区挤到不可读）", () => {
    expect(
      resolveRailWidth({
        mode: "manual",
        widthPx: 120,
        surface: "content",
        viewportWidth: 1920,
      }),
    ).toBe(320);
  });
});

describe("clampRailWidth / resolveRailWidthMax", () => {
  it("上界 = min(832, vw − 256(左栏) − 512(主区最小))", () => {
    expect(resolveRailWidthMax(1920)).toBe(832);
    expect(resolveRailWidthMax(1600)).toBe(832);
    expect(resolveRailWidthMax(1440)).toBe(672);
    expect(resolveRailWidthMax(1280)).toBe(512);
    expect(resolveRailWidthMax(1088)).toBe(320);
    // 900 − 256 − 512 = 132：上界已低于 minWidth，属于覆盖层区间
    expect(resolveRailWidthMax(900)).toBe(132);
  });

  it("极窄视口下 clamp 退化为 minWidth（覆盖层由调用方决定）", () => {
    expect(clampRailWidth(700, 900)).toBe(320);
    expect(isRailOverlayViewport(900)).toBe(true);
  });

  it("主区放不下最小右栏时判定为覆盖层模式", () => {
    expect(isRailOverlayViewport(1087)).toBe(true);
    expect(isRailOverlayViewport(1088)).toBe(false);
    expect(isRailOverlayViewport(1920)).toBe(false);
  });

  it("非法输入回落到安全值而不是 NaN", () => {
    expect(clampRailWidth(Number.NaN, 1440)).toBe(320);
    // 非有限值一律按内容型面默认宽度兜底，再走一遍下界
    expect(clampRailWidth(Number.POSITIVE_INFINITY, 1440)).toBe(320);
    // 视口非法时按 xl 断点处理：上界 = 1280 − 768 = 512
    expect(
      resolveRailWidth({
        mode: "manual",
        widthPx: 700,
        surface: "content",
        viewportWidth: Number.NaN,
      }),
    ).toBe(512);
    expect(
      resolveRailWidth({
        mode: "auto",
        widthPx: 0,
        surface: "wide",
        viewportWidth: -100,
      }),
    ).toBe(416);
  });
});

describe("resolveRailOverlayWidth", () => {
  it("覆盖层宽度 = min(计算宽度, 92vw)", () => {
    expect(resolveRailOverlayWidth(512, 1000)).toBe(512);
    expect(resolveRailOverlayWidth(832, 800)).toBe(736);
    expect(resolveRailOverlayWidth(Number.NaN, 800)).toBe(288);
  });
});
