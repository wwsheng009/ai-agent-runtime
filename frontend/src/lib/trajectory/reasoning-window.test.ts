import { describe, expect, it } from "vitest";

import {
  displayReasoningText,
  REASONING_WINDOW_MAX_CHARS,
} from "./reasoning-window";

describe("displayReasoningText", () => {
  it("短内容原样返回（不裁剪）", () => {
    const result = displayReasoningText("short reasoning");
    expect(result.visible).toBe("short reasoning");
    expect(result.droppedChars).toBe(0);
    expect(result.totalChars).toBe(15);
  });

  it("空内容安全", () => {
    const result = displayReasoningText("");
    expect(result.visible).toBe("");
    expect(result.droppedChars).toBe(0);
  });

  it("恰好等于窗口上限时不裁剪", () => {
    const content = "a".repeat(REASONING_WINDOW_MAX_CHARS);
    const result = displayReasoningText(content);
    expect(result.droppedChars).toBe(0);
    expect(result.visible).toBe(content);
  });

  it("超长内容只保留末尾稳定窗口", () => {
    // 8400 头部 + 350 尾部；窗口 8000 落在头部中间 → 尾部完整保留。
    const content = `${"a".repeat(8400)}${"z".repeat(350)}`;
    const result = displayReasoningText(content);

    expect(result.droppedChars).toBeGreaterThan(0);
    expect(result.visible.length).toBe(REASONING_WINDOW_MAX_CHARS);
    // 尾部窗口保留原始结尾（对齐「只渲染末尾稳定窗口」语义）。
    expect(result.visible.endsWith("z".repeat(350))).toBe(true);
    expect(result.visible).toContain("a");
    expect(result.visible.startsWith("a")).toBe(true);
    expect(result.totalChars).toBe(content.length);
  });

  it("自定义窗口上限生效", () => {
    const content = "abcdefghij";
    const result = displayReasoningText(content, 6);
    expect(result.visible).toBe("efghij");
    expect(result.droppedChars).toBe(4);
  });
});
