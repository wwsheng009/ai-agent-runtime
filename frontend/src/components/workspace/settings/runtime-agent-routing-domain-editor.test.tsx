import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { type RuntimeAgentRoutePreviewResult } from "@/types/runtime";

import { RuntimeAgentRoutingDomainEditor } from "./runtime-agent-routing-domain-editor";
import {
  getRuntimeAgentRoutingSettings,
  type RuntimeAgentRoutingConfigSummary,
} from "./runtime-agent-routing-domain-utils";
import { type RuntimeProviderSummary } from "./runtime-provider-config-utils";

describe("RuntimeAgentRoutingDomainEditor", () => {
  it("renders the four difficulty routes from configured providers", () => {
    const settings = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            default_difficulty: "normal",
            levels: {
              easy: { provider: "fast", model: "fast-model" },
              hard: { provider: "strong", model: "strong-model" },
            },
          },
        },
      },
    });
    const providers = [
      provider("fast", "fast-model"),
      provider("strong", "strong-model"),
    ];

    const markup = renderToStaticMarkup(
      <RuntimeAgentRoutingDomainEditor
        onChange={vi.fn()}
        onPreviewRoute={vi.fn()}
        onTeamInheritanceChange={vi.fn()}
        providers={providers}
        settings={settings}
      />,
    );

    expect(markup).toContain("子 Agent / Team 难度路由");
    expect(markup).toContain("简单");
    expect(markup).toContain("常规");
    expect(markup).toContain("困难");
    expect(markup).toContain("专家");
    expect(markup).toContain("fast-model");
    expect(markup).toContain("strong-model");
  });

  it("renders actionable health feedback for unavailable route providers", () => {
    const settings = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            levels: {
              hard: { provider: "offline", model: "offline-model" },
            },
          },
        },
      },
    });
    const offline = provider("offline", "offline-model");
    offline.enabled = false;

    const markup = renderToStaticMarkup(
      <RuntimeAgentRoutingDomainEditor
        onChange={vi.fn()}
        onPreviewRoute={vi.fn()}
        onTeamInheritanceChange={vi.fn()}
        providers={[offline]}
        settings={settings}
      />,
    );

    expect(markup).toContain("需检查");
    expect(markup).toContain("Provider “offline”已禁用");
    expect(markup).toContain("当前没有启用的 provider");
  });

  it("renders the effective route preview controls", () => {
    const settings = getRuntimeAgentRoutingSettings({});
    const markup = renderToStaticMarkup(
      <RuntimeAgentRoutingDomainEditor
        onChange={vi.fn()}
        onPreviewRoute={vi.fn()}
        onTeamInheritanceChange={vi.fn()}
        providers={[provider("fast", "fast-model")]}
        settings={settings}
      />,
    );

    expect(markup).toContain("有效路由试算");
    expect(markup).toContain("运行试算");
    expect(markup).toContain("任务角色");
  });

  it("edits task_types with the closed enum and shows legacy roles", () => {
    const settings = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            roles: {
              verifier: { hard: { provider: "strong", model: "strong-model" } },
              auditor: { normal: { model: "audit-model" } },
            },
          },
        },
      },
    });

    const markup = renderToStaticMarkup(
      <RuntimeAgentRoutingDomainEditor
        onChange={vi.fn()}
        onPreviewRoute={vi.fn()}
        onTeamInheritanceChange={vi.fn()}
        providers={[provider("strong", "strong-model")]}
        settings={settings}
      />,
    );

    expect(markup).toContain("任务类型路由");
    // verifier → verify：映射后的枚举键作为下拉当前值展示。
    expect(markup).toContain("验证");
    // 无别名的自定义 role 原样展示并标记为旧角色（保存时仍写回 roles）。
    expect(markup).toContain("auditor");
    expect(markup).toContain("旧角色");
    expect(markup).toContain("新增任务类型");
    // 试算表单支持任务类型（未指定 + 12 类枚举）。
    expect(markup).toContain("未指定");
  });

  it("adds the next free task_type entry on demand", async () => {
    const container = document.createElement("div");
    document.body.appendChild(container);
    const root = createRoot(container);
    const actEnvironment = globalThis as typeof globalThis & {
      IS_REACT_ACT_ENVIRONMENT?: boolean;
    };
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    const onChange = vi.fn();

    try {
      await act(async () => {
        root.render(
          <RuntimeAgentRoutingDomainEditor
            onChange={onChange}
            onPreviewRoute={vi.fn()}
            onTeamInheritanceChange={vi.fn()}
            providers={[provider("fast", "fast-model")]}
            settings={getRuntimeAgentRoutingSettings({})}
          />,
        );
      });

      const addButton = Array.from(container.querySelectorAll("button")).find(
        (button) => button.textContent?.includes("新增任务类型"),
      );
      expect(addButton).toBeInstanceOf(HTMLButtonElement);

      await act(async () => {
        addButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });

      expect(onChange).toHaveBeenCalledTimes(1);
      const [scope, next] = onChange.mock.calls[0] as [
        string,
        RuntimeAgentRoutingConfigSummary,
      ];
      expect(scope).toBe("subagents");
      // 新增键取自 12 类封闭枚举（config 是字典序第一个未占用的键）。
      expect(next.taskTypes.map((entry) => entry.key)).toEqual(["config"]);
      expect(next.taskTypes[0].legacy).toBe(false);
    } finally {
      act(() => root.unmount());
      container.remove();
      delete actEnvironment.IS_REACT_ACT_ENVIRONMENT;
    }
  });

  it("discards an in-flight preview when the routing draft changes", async () => {
    const container = document.createElement("div");
    document.body.appendChild(container);
    const root = createRoot(container);
    const actEnvironment = globalThis as typeof globalThis & {
      IS_REACT_ACT_ENVIRONMENT?: boolean;
    };
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
    let resolvePreview!: (result: RuntimeAgentRoutePreviewResult) => void;
    const onPreviewRoute = vi.fn(
      () =>
        new Promise<RuntimeAgentRoutePreviewResult>((resolve) => {
          resolvePreview = resolve;
        }),
    );
    const initialSettings = getRuntimeAgentRoutingSettings({});
    const changedSettings = getRuntimeAgentRoutingSettings({
      aicli: { subagents: { routing: { enabled: true } } },
    });
    const renderEditor = (
      settings: ReturnType<typeof getRuntimeAgentRoutingSettings>,
    ) => (
      <RuntimeAgentRoutingDomainEditor
        onChange={vi.fn()}
        onPreviewRoute={onPreviewRoute}
        onTeamInheritanceChange={vi.fn()}
        providers={[provider("fast", "fast-model")]}
        settings={settings}
      />
    );

    try {
      await act(async () => {
        root.render(renderEditor(initialSettings));
      });
      const previewButton = Array.from(container.querySelectorAll("button")).find(
        (button) => button.textContent?.includes("运行试算"),
      );
      expect(previewButton).toBeInstanceOf(HTMLButtonElement);

      await act(async () => {
        previewButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      expect(onPreviewRoute).toHaveBeenCalledTimes(1);

      await act(async () => {
        root.render(renderEditor(changedSettings));
      });
      await act(async () => {
        resolvePreview(routePreviewResult("stale-provider"));
        await Promise.resolve();
      });

      expect(container.textContent).not.toContain("stale-provider");
    } finally {
      act(() => root.unmount());
      container.remove();
      delete actEnvironment.IS_REACT_ACT_ENVIRONMENT;
    }
  });
});

function routePreviewResult(providerName: string): RuntimeAgentRoutePreviewResult {
  return {
    scope: "subagent",
    routing_source: "subagent",
    routing_enabled: true,
    parent: { provider: "parent", model: "parent-model" },
    decision: {
      difficulty: "normal",
      difficulty_source: "explicit",
      provider: providerName,
      model: "preview-model",
      source: "difficulty_level",
    },
  };
}

function provider(name: string, model: string): RuntimeProviderSummary {
  return {
    account: null,
    accountAuthRef: "",
    accountSummary: "",
    apiKey: "",
    apiPath: "",
    baseUrl: "",
    defaultModel: model,
    enabled: true,
    extraFieldCount: 0,
    forwardUrl: "",
    hasProxyOverride: false,
    name,
    protocol: "openai",
    proxyEnabled: false,
    proxySummary: "",
    raw: {},
    siteType: "",
    siteTypeConfidence: "",
    siteTypeDetectedAt: "",
    siteTypeScores: {},
    supportedModels: [model],
    supportTypes: ["openai"],
    timeout: "300s",
    truncationAdapter: "openai_local",
  };
}
