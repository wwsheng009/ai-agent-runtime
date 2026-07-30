import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { type RuntimeAgentRoutePreviewResult } from "@/types/runtime";

import { RuntimeAgentRoutingDomainEditor } from "./runtime-agent-routing-domain-editor";
import { getRuntimeAgentRoutingSettings } from "./runtime-agent-routing-domain-utils";
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
