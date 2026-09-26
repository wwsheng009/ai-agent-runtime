// §4.6 常驻模式标识：tone / 文案键 / plan 状态读法的边界用例。
//
// 断言纪律：只钉「模式判定与状态读法」——文案与配色由组件层的渲染用例覆盖。

import { describe, expect, it } from "vitest";

import {
  sessionModeBannerHintLeaf,
  sessionModeBannerLabelKey,
  sessionModeBannerTone,
  sessionModeBannerToneClass,
} from "./session-mode-banner-shared";
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

describe("sessionModeBannerTone", () => {
  it("plan 走强调档、跳权走告警档、其余中性", () => {
    expect(sessionModeBannerTone("plan")).toBe("plan");
    expect(sessionModeBannerTone("bypass_permissions")).toBe("danger");
    expect(sessionModeBannerTone("default")).toBe("neutral");
    expect(sessionModeBannerTone("accept_edits")).toBe("neutral");
    expect(sessionModeBannerTone("future_mode")).toBe("neutral");
  });

  it("已知模式映射词典键，未知模式交回调用方回落后端原文", () => {
    expect(sessionModeBannerLabelKey("plan")).toBe("composer.permission.mode.plan.label");
    expect(sessionModeBannerLabelKey("bypass_permissions")).toBe(
      "composer.permission.mode.bypass_permissions.label",
    );
    expect(sessionModeBannerLabelKey("future_mode")).toBeNull();
  });

  it("三档 tone 各自有配色类，未知档回中性", () => {
    expect(sessionModeBannerToneClass("plan")).toContain("accent-teal");
    expect(sessionModeBannerToneClass("danger")).toContain("accent-gold");
    expect(sessionModeBannerToneClass("neutral")).toContain("muted-foreground");
  });
});

describe("sessionModeBannerHintLeaf", () => {
  it("非 active 不提示（横幅只显示模式本身）", () => {
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
