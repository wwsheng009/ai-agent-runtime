// §4.6 plan 状态读法的边界用例。
//
// 断言纪律：只钉「状态读法」——文案与配色由组件层的渲染用例覆盖。

import { describe, expect, it } from "vitest";

import { sessionModeBannerHintLeaf } from "./session-mode-banner-shared";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

function plan(overrides: Partial<RuntimeSessionPlanMode> = {}): RuntimeSessionPlanMode {
  return {
    session_id: "session-1",
    active: false,
    status: "inactive",
    permission_mode: "default",
    plan_content: "",
    plan_content_available: false,
    ...overrides,
  };
}

describe("sessionModeBannerHintLeaf", () => {
  it("非 active 不提示（不显示 plan 上下文）", () => {
    expect(sessionModeBannerHintLeaf(null)).toBeNull();
    expect(sessionModeBannerHintLeaf(plan({ active: false }))).toBeNull();
  });

  it("模型已请求裁决优先于「已就绪」", () => {
    expect(
      sessionModeBannerHintLeaf(
        plan({ active: true, pending_exit_request: true, plan_content_available: true }),
      ),
    ).toBe("modelRequested");
  });

  it("正文可用报「已就绪」，正文缺失报「等待产出」", () => {
    expect(sessionModeBannerHintLeaf(plan({ active: true, plan_content_available: true }))).toBe(
      "ready",
    );
    expect(sessionModeBannerHintLeaf(plan({ active: true, plan_content_available: false }))).toBe(
      "waiting",
    );
  });
});
