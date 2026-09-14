import { describe, expect, it } from "vitest";

import {
  buildBranchSessionRequest,
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

  it("builds a whole-session branch when no anchor is given", () => {
    const request = buildBranchSessionRequest({
      sessionId: "s-9",
      sourceTitle: "主线",
      userId: " user-a ",
      branchSuffix: "（分支）",
    });

    // 锚点缺省 = 会话末尾：请求里不出现 anchor 字段，目录归属由服务端继承。
    expect(request).toEqual({
      title: "主线（分支）",
      user_id: "user-a",
    });
  });

  it("passes the anchor and includes it for message-level branches", () => {
    const request = buildBranchSessionRequest({
      sessionId: "s-9",
      sourceTitle: "",
      userId: "   ",
      branchSuffix: " (branch)",
      anchorMessageId: " msg_1 ",
    });

    expect(request).toEqual({
      anchor_message_id: "msg_1",
      include_anchor: true,
      title: "s-9 (branch)",
    });
  });

  it("routes selection away only when the deleted session is selected", () => {
    expect(resolveSelectionAfterSessionDelete("s-1", "s-1")).toBeNull();
    expect(resolveSelectionAfterSessionDelete("s-2", "s-1")).toBe("s-2");
  });
});
