import { describe, expect, it } from "vitest";

import {
  COMPOSER_FALLBACK_FONT_SIZE_PX,
  COMPOSER_MAX_VISIBLE_LINES,
  resolveComposerTextareaLayout,
  resolveComposerTextareaLineHeight,
  type ComposerTextareaMetrics,
} from "./composer-textarea";

const baseMetrics: ComposerTextareaMetrics = {
  scrollHeight: 40,
  lineHeight: 20,
  fontSize: 13,
  paddingTop: 8,
  paddingBottom: 8,
};

describe("resolveComposerTextareaLineHeight", () => {
  it("uses the parsed line-height when available", () => {
    expect(resolveComposerTextareaLineHeight({ lineHeight: 24, fontSize: 12 })).toBe(24);
  });

  it("falls back to fontSize * ratio, then to the fixed default", () => {
    expect(
      resolveComposerTextareaLineHeight({
        lineHeight: null,
        fontSize: COMPOSER_FALLBACK_FONT_SIZE_PX,
      }),
    ).toBeCloseTo(19.5);
    expect(resolveComposerTextareaLineHeight({ lineHeight: 0, fontSize: null })).toBe(20);
  });
});

describe("resolveComposerTextareaLayout", () => {
  it("grows with the content while under the cap", () => {
    const layout = resolveComposerTextareaLayout({
      ...baseMetrics,
      scrollHeight: 120,
    });

    expect(layout.capped).toBe(false);
    expect(layout.height).toBe(120);
    expect(layout.overflowY).toBe("hidden");
  });

  it("caps at 14 lines plus padding and scrolls inside", () => {
    const layout = resolveComposerTextareaLayout({
      ...baseMetrics,
      scrollHeight: 900,
    });

    expect(layout.capped).toBe(true);
    expect(layout.maxHeight).toBe(20 * COMPOSER_MAX_VISIBLE_LINES + 16);
    expect(layout.height).toBe(layout.maxHeight);
    expect(layout.overflowY).toBe("auto");
  });

  it("keeps content height above the natural minimum and accounts padding", () => {
    // scrollHeight 不含上下 padding 的极端取值：不下探到 lineHeight 以下。
    const layout = resolveComposerTextareaLayout({
      ...baseMetrics,
      scrollHeight: 0,
    });

    expect(layout.height).toBe(20 + 16);
  });

  it("honours a custom line budget and ignores negative metrics", () => {
    const layout = resolveComposerTextareaLayout(
      { ...baseMetrics, scrollHeight: -10, paddingTop: -4, paddingBottom: -4 },
      3,
    );

    expect(layout.maxHeight).toBe(60);
    expect(layout.height).toBe(20);
    expect(layout.overflowY).toBe("hidden");
  });
});
