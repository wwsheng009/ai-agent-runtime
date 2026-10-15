// S2b（docs/bots/13 §6.1）：events 合并进提交体 policy——保留既有键与 D6 路由，
// 关闭时显式 enabled:false（后端 policyFrom 为整体替换语义）。

import { describe, expect, it } from "vitest";

import type { ConnectorSubmitPayload, ConnectorView } from "@/types/runtime";

import { mergeEventsIntoPayload } from "./shared";

const EXISTING: ConnectorView = {
  id: "c1",
  provider: "telegram",
  name: "bot",
  enabled: true,
  mode: "polling",
  settings: {},
  secret_keys: [],
  policy: {
    runtime_owner: "aicli",
    route: { provider: "workbuddy", model: "m1", reasoning_effort: "medium" },
  },
};

const PAYLOAD: ConnectorSubmitPayload = { settings: {}, secrets: {}, mode: "polling" };

describe("mergeEventsIntoPayload", () => {
  it("启用：写入订阅并保留既有 policy 键与路由", () => {
    const merged = mergeEventsIntoPayload(
      PAYLOAD,
      {
        enabled: true,
        subscriptions: [
          { id: "ops", sessions: { mode: "ids", ids: ["s1"] }, types: ["turn.*"], deliver: "notice" },
        ],
      },
      EXISTING,
    );
    expect(merged.policy?.runtime_owner).toBe("aicli");
    expect(merged.policy?.route).toEqual(EXISTING.policy.route);
    expect(merged.policy?.events).toEqual({
      enabled: true,
      subscriptions: [
        { id: "ops", sessions: { mode: "ids", ids: ["s1"] }, types: ["turn.*"], deliver: "notice" },
      ],
    });
  });

  it("关闭：显式 enabled:false（清空订阅）且路由保持", () => {
    const first = mergeEventsIntoPayload(PAYLOAD, { enabled: true, subscriptions: [] }, EXISTING);
    const disabled = mergeEventsIntoPayload(first, { enabled: false, subscriptions: [] }, EXISTING);
    expect(disabled.policy?.events).toEqual({ enabled: false });
    expect(disabled.policy?.route).toEqual(EXISTING.policy.route);
  });

  it("无既有连接器（防御）：返回含 events 的 policy", () => {
    const merged = mergeEventsIntoPayload(PAYLOAD, { enabled: false, subscriptions: [] }, null);
    expect(merged.policy?.events).toEqual({ enabled: false });
  });
});
