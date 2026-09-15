import { afterEach, describe, expect, it } from "vitest";

import {
  clampScrollTop,
  distanceFromBottom,
  forgetConversationAnchor,
  isAwayFromBottom,
  isAwayFromPinnedTop,
  maxScrollTop,
  pickConversationAnchor,
  pickReadingLineIndex,
  READING_LINE_RATIO,
  recallConversationAnchor,
  rememberConversationAnchor,
  resetConversationScrollMemory,
  resolveAnchorScrollTop,
  SCROLL_FOLLOW_THRESHOLD,
  type ReadingLineRow,
} from "./conversation-scroll";

const rows: ReadingLineRow[] = [
  { bottom: 120, id: "m1", top: 0 },
  { bottom: 360, id: "m2", top: 120 },
  { bottom: 600, id: "m3", top: 360 },
  { bottom: 720, id: "m4", top: 600 },
];

afterEach(() => {
  resetConversationScrollMemory();
});

describe("distance / bottom ownership", () => {
  it("treats the last threshold pixels as still pinned to the bottom", () => {
    const base = { clientHeight: 600, scrollHeight: 2000 };
    expect(distanceFromBottom({ ...base, scrollTop: 1400 })).toBe(0);
    expect(isAwayFromBottom({ ...base, scrollTop: 1400 })).toBe(false);
    // 恰好越过阈值即视为离开底部（与既有 120px 契约一致）。
    expect(isAwayFromBottom({ ...base, scrollTop: 1400 - SCROLL_FOLLOW_THRESHOLD })).toBe(
      false,
    );
    expect(
      isAwayFromBottom({ ...base, scrollTop: 1400 - SCROLL_FOLLOW_THRESHOLD - 1 }),
    ).toBe(true);
  });

  it("clamps scroll ranges to the real maximum", () => {
    expect(maxScrollTop({ clientHeight: 600, scrollHeight: 500, scrollTop: 0 })).toBe(0);
    expect(maxScrollTop({ clientHeight: 600, scrollHeight: 2000, scrollTop: 0 })).toBe(1400);
    expect(clampScrollTop(-40, 1400)).toBe(0);
    expect(clampScrollTop(9999, 1400)).toBe(1400);
    expect(clampScrollTop(Number.NaN, 1400)).toBe(0);
  });

  it("treats stream growth as still pinned instead of as user intent", () => {
    // 上一次钉底时 max=1400；这一帧内容长了 400px，scrollTop 还没跟上。
    const grown = { clientHeight: 600, scrollHeight: 2400, scrollTop: 1400 };
    expect(isAwayFromPinnedTop(grown, 1400)).toBe(false);
    // 同一份读数用「离底距离」判定就会误判成用户离开底部（真实回归就发生在这里）。
    expect(isAwayFromBottom(grown)).toBe(true);
  });

  it("treats browser-side clamping on shrink as still pinned", () => {
    // 内容收缩后浏览器把 scrollTop 夹到新的 max=1200：读数变小但不是用户滚动。
    const shrunk = { clientHeight: 600, scrollHeight: 1800, scrollTop: 1200 };
    expect(isAwayFromPinnedTop(shrunk, 1400)).toBe(false);
  });

  it("still detects a real scroll-up while following", () => {
    const base = { clientHeight: 600, scrollHeight: 2000 };
    expect(isAwayFromPinnedTop({ ...base, scrollTop: 1400 }, 1400)).toBe(false);
    expect(
      isAwayFromPinnedTop({ ...base, scrollTop: 1400 - SCROLL_FOLLOW_THRESHOLD }, 1400),
    ).toBe(false);
    expect(
      isAwayFromPinnedTop({ ...base, scrollTop: 1400 - SCROLL_FOLLOW_THRESHOLD - 1 }, 1400),
    ).toBe(true);
  });
});

describe("anchor resolution", () => {
  it("keeps the anchored row at its original viewport offset after a prepend", () => {
    // 视口上方插入了 800px 内容：锚点行当前偏移变成 800，需要把 scrollTop 加回 800。
    expect(
      resolveAnchorScrollTop({
        anchorOffsetTop: 300,
        currentOffsetTop: 1100,
        max: 5000,
        scrollTop: 1200,
      }),
    ).toBe(2000);
  });

  it("pins to the bottom when the anchor sits closer to the end than the viewport", () => {
    // 恢复目标超过最大滚动量：收敛到底部而不是留白。
    expect(
      resolveAnchorScrollTop({
        anchorOffsetTop: 0,
        currentOffsetTop: 120,
        max: 1400,
        scrollTop: 1400,
      }),
    ).toBe(1400);
  });
});

describe("reading line", () => {
  it("picks the last row whose top is above the reading line", () => {
    const line = 900 * READING_LINE_RATIO;
    // 基准线 300：m1(0)/m2(120) 在线上，m3(360) 已在线上方之下。
    expect(pickReadingLineIndex(rows, line)).toBe(1);
    expect(pickReadingLineIndex(rows, 200)).toBe(1);
    expect(pickReadingLineIndex(rows, 500)).toBe(2);
    expect(pickReadingLineIndex([], line)).toBe(-1);
  });

  it("falls back to the first row when every row start sits below the line", () => {
    expect(pickReadingLineIndex(rows, -50)).toBe(0);
  });

  it("derives the anchor from the reading line row", () => {
    expect(pickConversationAnchor(rows, 900)).toEqual({ messageId: "m2", offsetTop: 120 });
    // 滚动 300px 之后：各行上移，m4 顶部落在基准线（300）上并成为命中行。
    const scrolled = rows.map((row) => ({
      ...row,
      bottom: row.bottom - 300,
      top: row.top - 300,
    }));
    expect(pickConversationAnchor(scrolled, 900)).toEqual({
      messageId: "m4",
      offsetTop: 300,
    });
    expect(pickConversationAnchor([], 900)).toBeNull();
  });
});

describe("cross-mount anchor memory", () => {
  it("remembers and recalls the reading position per thread", () => {
    expect(recallConversationAnchor("session-1")).toBeNull();
    rememberConversationAnchor("session-1", { messageId: "m3", offsetTop: 64 });
    rememberConversationAnchor("session-2", { messageId: "m9", offsetTop: 12 });
    expect(recallConversationAnchor("session-1")).toEqual({
      messageId: "m3",
      offsetTop: 64,
    });
    expect(recallConversationAnchor("session-2")).toEqual({
      messageId: "m9",
      offsetTop: 12,
    });

    forgetConversationAnchor("session-1");
    expect(recallConversationAnchor("session-1")).toBeNull();
    expect(recallConversationAnchor("session-2")).not.toBeNull();
  });
});
