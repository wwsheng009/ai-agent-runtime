// 通道动作载荷的展示单测：kind=button 动作结果优先展示 message（QQ「授权
// 校验」等），微信扫码的 status/login_id 组合与 raw 兜底保持原语义。

import { describe, expect, it } from "vitest";

import { normalizeConnectorActionPayload } from "@/api/runtime/connectors";

import { summarizeActionPayload } from "./action-payload";

describe("summarizeActionPayload（kind=button 动作结果）", () => {
  it("message 优先展示（QQ 授权校验）", () => {
    const payload = normalizeConnectorActionPayload({
      ok: true,
      action: "check_credentials",
      message: "校验通过：测试机器人（AppID app-1）",
      app_id: "app-1",
      bot: { id: "bot-1", username: "测试机器人" },
    });
    expect(summarizeActionPayload(payload)).toBe("校验通过：测试机器人（AppID app-1）");
  });

  it("微信扫码返回保持 status/login_id 组合", () => {
    const payload = normalizeConnectorActionPayload({ status: "wait", login_id: "login-1" });
    expect(summarizeActionPayload(payload)).toBe("status=wait · login_id=login-1");
  });

  it("未识别载荷退化为 raw JSON", () => {
    const payload = normalizeConnectorActionPayload({ foo: "bar" });
    expect(summarizeActionPayload(payload)).toBe('{"foo":"bar"}');
  });
});