import { describe, expect, it } from "vitest";

import {
  buildForkSessionRequest,
  buildForkSessionTitle,
  resolveSelectionAfterSessionDelete,
} from "./session-row-actions";

describe("sidebar session row actions", () => {
  it("builds fork titles with suffix and id fallback", () => {
    expect(buildForkSessionTitle(" 规划会话 ", "s-1", "（分支）")).toBe(
      "规划会话（分支）",
    );
    expect(buildForkSessionTitle("   ", "s-2", "（分支）")).toBe("s-2（分支）");
  });

  it("inherits title, user and workspace path for forks", () => {
    const request = buildForkSessionRequest({
      session: {
        id: "s-9",
        metadata: { context: { workspace_path: "E:\\projects\\demo\\" } },
      },
      sourceTitle: "主线",
      userId: " user-a ",
      branchSuffix: "（分支）",
    });

    expect(request).toEqual({
      title: "主线（分支）",
      user_id: "user-a",
      workspace_path: "E:/projects/demo",
    });
  });

  it("omits user and workspace when unknown so forks stay ungrouped", () => {
    const request = buildForkSessionRequest({
      session: { id: "s-9" },
      sourceTitle: "",
      userId: "   ",
      branchSuffix: " (branch)",
    });

    expect(request).toEqual({ title: "s-9 (branch)" });
  });

  it("routes selection away only when the deleted session is selected", () => {
    expect(resolveSelectionAfterSessionDelete("s-1", "s-1")).toBeNull();
    expect(resolveSelectionAfterSessionDelete("s-2", "s-1")).toBe("s-2");
  });
});
