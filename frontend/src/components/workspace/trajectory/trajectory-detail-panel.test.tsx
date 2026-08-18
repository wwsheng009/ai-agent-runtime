// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { REASONING_WINDOW_MAX_CHARS } from "@/lib/trajectory/reasoning-window";
import type { TrajectoryItem } from "@/lib/trajectory/types";

import { TrajectoryDetailPanel } from "./trajectory-detail-panel";

function reasoningItem(content: string): TrajectoryItem {
  return {
    id: "reasoning-1",
    seq: 1,
    kind: "reasoning",
    causeId: "",
    status: "running",
    createdAt: 1,
    updatedAt: 1,
    head: { kind: "reasoning", content },
  };
}

describe("TrajectoryDetailPanel reasoning window (P2-6)", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
  });

  function render(item: TrajectoryItem) {
    act(() => {
      root.render(<TrajectoryDetailPanel item={item} onClose={() => {}} />);
    });
  }

  it("万字符推理只渲染末尾稳定窗口（渲染内容有界）", () => {
    const content = `${"a".repeat(12000)}${"z".repeat(100)}`;
    render(reasoningItem(content));

    const pre = container.querySelector("pre");
    expect(pre).not.toBeNull();
    expect(pre?.textContent?.length ?? 0).toBeLessThanOrEqual(
      REASONING_WINDOW_MAX_CHARS,
    );
    // 尾部窗口保留原始结尾。
    expect(pre?.textContent?.endsWith("z".repeat(100))).toBe(true);
    // 裁剪提示可见。
    expect(container.textContent).toContain("leading chars trimmed");
  });

  it("短推理不裁剪（无提示、全文渲染）", () => {
    render(reasoningItem("short reasoning"));
    expect(container.textContent).not.toContain("trimmed");
    expect(container.querySelector("pre")?.textContent).toBe(
      "short reasoning",
    );
  });
});
