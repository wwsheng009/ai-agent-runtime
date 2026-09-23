// 「路由」区块纯逻辑单测（无 React、无 IO）：
// 草稿 → 补丁、错误 → 字段定位、层可写性、目标路径回显。
//
// 这些断言锁的是**语义纪律**（I-6）：投影值原样进草稿；清空只在「生效值来自本层」
// 时才生成键路径清除；不可写层与未知 source 不做兜底推断。

import { describe, expect, it } from "vitest";

import type { RoutingLevelSummary, RoutingPanelMetadata } from "@/types/runtime";

import {
  buildRoutingLevelDrafts,
  buildSessionRoutingWrite,
  isRoutingLayerWritable,
  resolveRoutingErrorEcho,
  resolveRoutingSourceKey,
  resolveRoutingTargetPath,
} from "./session-detail-routing-shared";

function levelSummary(
  overrides: Partial<RoutingLevelSummary> = {},
): RoutingLevelSummary {
  return {
    level: "hard",
    enabled: true,
    provider: "opencode.ai",
    model: "deepseek-v4.1",
    reasoning: "high",
    source: "session",
    expensive: false,
    ...overrides,
  };
}

function panelMetadata(
  overrides: Partial<RoutingPanelMetadata> = {},
): RoutingPanelMetadata {
  return {
    scope: "main",
    childSession: false,
    sessionOverride: true,
    workspaceOverride: false,
    configOverride: false,
    workspacePath: "E:/ws",
    workspacePrefsPath: "E:/ws/.aicli/chat-prefs.yaml",
    configPath: "C:/Users/vince/.aicli/config.yaml",
    configLayer: "user",
    writableLayers: ["session", "workspace", "config"],
    levels: [],
    subAgent: null,
    ...overrides,
  };
}

describe("buildRoutingLevelDrafts", () => {
  it("草稿初值直接取投影生效值，不做加工", () => {
    const drafts = buildRoutingLevelDrafts([
      levelSummary(),
      levelSummary({ level: "easy", provider: "", model: "small", reasoning: "" }),
    ]);

    expect(drafts.hard).toEqual({
      provider: "opencode.ai",
      model: "deepseek-v4.1",
      reasoning_effort: "high",
    });
    // 空字段保持空串（不填「默认」、不继承别的档位）。
    expect(drafts.easy).toEqual({
      provider: "",
      model: "small",
      reasoning_effort: "",
    });
  });
});

describe("buildSessionRoutingWrite", () => {
  it("无改动时不产生补丁，也不发请求", () => {
    const plan = buildSessionRoutingWrite({
      levels: [levelSummary()],
      drafts: { hard: { provider: "opencode.ai", model: "deepseek-v4.1", reasoning_effort: "high" } },
      enabledDraft: null,
      effectiveEnabled: true,
      layer: "session",
    });

    expect(plan.hasChanges).toBe(false);
    expect(plan.mainAgent).toEqual({});
    expect(plan.clearFields).toEqual([]);
  });

  it("改值只带该字段（字段级合并）", () => {
    const plan = buildSessionRoutingWrite({
      levels: [levelSummary()],
      drafts: { hard: { provider: "opencode.ai", model: "next-model", reasoning_effort: "high" } },
      enabledDraft: null,
      effectiveEnabled: true,
      layer: "workspace",
    });

    expect(plan.mainAgent).toEqual({
      profiles: { hard: { model: "next-model" } },
    });
    expect(plan.clearFields).toEqual([]);
    expect(plan.hasChanges).toBe(true);
  });

  it("清空本层覆盖 → 键路径清除，且不写成空串覆盖", () => {
    const plan = buildSessionRoutingWrite({
      levels: [levelSummary({ source: "session" })],
      drafts: { hard: { provider: "opencode.ai", model: "", reasoning_effort: "high" } },
      enabledDraft: null,
      effectiveEnabled: true,
      layer: "session",
    });

    expect(plan.clearFields).toEqual(["main_agent.profiles.hard.model"]);
    expect(plan.mainAgent.profiles).toBeUndefined();
    expect(plan.hasChanges).toBe(true);
  });

  it("生效值来自其它层时清空本层输入不生成清除（该字段在本层没有覆盖）", () => {
    const plan = buildSessionRoutingWrite({
      levels: [levelSummary({ source: "config" })],
      drafts: { hard: { provider: "", model: "deepseek-v4.1", reasoning_effort: "high" } },
      enabledDraft: null,
      effectiveEnabled: true,
      layer: "session",
    });

    expect(plan.clearFields).toEqual([]);
    expect(plan.mainAgent).toEqual({});
    expect(plan.hasChanges).toBe(false);
  });

  it("启用开关变化写入 main_agent.enabled", () => {
    const plan = buildSessionRoutingWrite({
      levels: [levelSummary()],
      drafts: { hard: { provider: "opencode.ai", model: "deepseek-v4.1", reasoning_effort: "high" } },
      enabledDraft: false,
      effectiveEnabled: true,
      layer: "session",
    });

    expect(plan.mainAgent).toEqual({ enabled: false });
    expect(plan.hasChanges).toBe(true);
  });
});

describe("resolveRoutingErrorEcho", () => {
  it("从键路径定位档位与字段", () => {
    expect(
      resolveRoutingErrorEcho(
        "invalid routing override: main_agent.profiles.hard.model must not be empty",
        ["hard", "easy"],
      ),
    ).toEqual({ level: "hard", field: "model" });
  });

  it("命中档位但字段不可辨时只定位到档位", () => {
    expect(
      resolveRoutingErrorEcho("main_agent.profiles.easy is not allowed here", [
        "hard",
        "easy",
      ]),
    ).toEqual({ level: "easy", field: "" });
  });

  it("按标识符边界匹配，避免 hard 命中 extra_hard", () => {
    expect(
      resolveRoutingErrorEcho("extra_hard.model is invalid", ["hard"]),
    ).toEqual({ level: "", field: "" });
  });

  it("无法定位时返回空回显（只在表单级展示原文）", () => {
    expect(resolveRoutingErrorEcho("routing override rejected", ["hard"])).toEqual(
      { level: "", field: "" },
    );
    expect(resolveRoutingErrorEcho("   ", ["hard"])).toEqual({
      level: "",
      field: "",
    });
  });
});

describe("isRoutingLayerWritable", () => {
  it("只认后端白名单", () => {
    const panel = panelMetadata({ writableLayers: ["session", "config"] });

    expect(isRoutingLayerWritable(panel, "session")).toBe(true);
    expect(isRoutingLayerWritable(panel, "config")).toBe(true);
    expect(isRoutingLayerWritable(panel, "workspace")).toBe(false);
  });

  it("缺面板或缺白名单即不可写（不自造入口）", () => {
    expect(isRoutingLayerWritable(null, "session")).toBe(false);
    expect(
      isRoutingLayerWritable(panelMetadata({ writableLayers: [] }), "session"),
    ).toBe(false);
  });
});

describe("resolveRoutingTargetPath", () => {
  it("优先回显本次 PATCH 返回的 target_path", () => {
    expect(
      resolveRoutingTargetPath({
        panel: panelMetadata(),
        layer: "workspace",
        serverTargetLayer: "workspace",
        serverTargetPath: "E:/other/chat-prefs.yaml",
      }),
    ).toBe("E:/other/chat-prefs.yaml");
  });

  it("服务端层与当前层不一致时回落到面板路径", () => {
    expect(
      resolveRoutingTargetPath({
        panel: panelMetadata(),
        layer: "config",
        serverTargetLayer: "workspace",
        serverTargetPath: "E:/other/chat-prefs.yaml",
      }),
    ).toBe("C:/Users/vince/.aicli/config.yaml");
  });

  it("session 层没有文件路径，返回空串由文案说明", () => {
    expect(
      resolveRoutingTargetPath({
        panel: panelMetadata(),
        layer: "session",
        serverTargetLayer: "",
        serverTargetPath: "",
      }),
    ).toBe("");
  });
});

describe("resolveRoutingSourceKey", () => {
  it("已知 source 归一化为 i18n 子键", () => {
    expect(resolveRoutingSourceKey("session")).toBe("session");
    expect(resolveRoutingSourceKey("Workspace")).toBe("workspace");
    expect(resolveRoutingSourceKey("derived")).toBe("derived");
  });

  it("未知 source 返回空串（调用方原样展示，不改写）", () => {
    expect(resolveRoutingSourceKey("provider_fallback")).toBe("");
  });
});
