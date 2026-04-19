import { describe, expect, it } from "vitest";

import {
  buildProviderCreateConfigSnippet,
  createDefaultProviderConfig,
  getRuntimeDefaultProvider,
  getRuntimeProviderRecord,
  listRuntimeProviderSummaries,
  normalizeStringArrayInput,
  summarizeDraftSections,
} from "./runtime-provider-config-utils";

describe("runtime-provider-config-utils", () => {
  it("lists provider summaries from providers.items", () => {
    const summaries = listRuntimeProviderSummaries({
      providers: {
        default_provider: "deepseek",
        items: {
          deepseek: {
            enabled: true,
            protocol: "openai",
            base_url: "https://api.deepseek.com",
            default_model: "deepseek-chat",
            supported_models: ["deepseek-chat", "deepseek-reasoner"],
            support_types: ["openai", "anthropic"],
            timeout: "300s",
            retries: 3,
          },
        },
      },
    });

    expect(summaries).toHaveLength(1);
    expect(summaries[0]).toMatchObject({
      name: "deepseek",
      enabled: true,
      protocol: "openai",
      baseUrl: "https://api.deepseek.com",
      defaultModel: "deepseek-chat",
      supportedModels: ["deepseek-chat", "deepseek-reasoner"],
      supportTypes: ["openai", "anthropic"],
      timeout: "300s",
      hasProxyOverride: false,
      extraFieldCount: 1,
    });
  });

  it("reads provider proxy summary from provider record", () => {
    const summaries = listRuntimeProviderSummaries({
      providers: {
        items: {
          proxied: {
            enabled: true,
            protocol: "openai",
            proxy: {
              enabled: true,
              http: "http://127.0.0.1:10810",
              no_proxy: "localhost",
            },
          },
        },
      },
    });

    expect(summaries[0]).toMatchObject({
      name: "proxied",
      hasProxyOverride: true,
      proxyEnabled: true,
      proxySummary: "HTTP + NO_PROXY",
    });
  });

  it("reads the current default provider and provider record", () => {
    const config = {
      providers: {
        default_provider: "nvidia",
        items: {
          nvidia: {
            protocol: "openai",
            base_url: "https://integrate.api.nvidia.com",
          },
        },
      },
    };

    expect(getRuntimeDefaultProvider(config)).toBe("nvidia");
    expect(getRuntimeProviderRecord(config, "nvidia")).toEqual({
      protocol: "openai",
      base_url: "https://integrate.api.nvidia.com",
    });
    expect(getRuntimeProviderRecord(config, "missing")).toBeNull();
  });

  it("normalizes provider textarea input into arrays", () => {
    expect(
      normalizeStringArrayInput("gpt-5.4\n gpt-5.2-codex ,\n\n gpt-5.4 "),
    ).toEqual(["gpt-5.4", "gpt-5.2-codex", "gpt-5.4"]);
  });

  it("creates a sensible default provider config", () => {
    expect(createDefaultProviderConfig("my_codex", "codex")).toMatchObject({
      enabled: true,
      protocol: "codex",
      forward_url: "/v1/responses",
      timeout: "300s",
      support_types: ["codex"],
    });
  });

  it("builds a copyable provider create snippet", () => {
    expect(
      buildProviderCreateConfigSnippet({
        name: "deepseek",
        raw: {
          enabled: true,
          protocol: "openai",
          base_url: "https://api.deepseek.com",
          headers: {
            "X-Trace-Id": "trace-1",
          },
          supported_models: ["deepseek-chat", "deepseek-reasoner"],
          model_mappings: {
            "*": "deepseek-chat",
          },
        },
        apiKey: "",
        apiPath: "",
        baseUrl: "https://api.deepseek.com",
        defaultModel: "",
        enabled: true,
        extraFieldCount: 0,
        forwardUrl: "",
        hasProxyOverride: false,
        protocol: "openai",
        proxyEnabled: false,
        proxySummary: "环境变量 / 直连",
        supportedModels: ["deepseek-chat", "deepseek-reasoner"],
        supportTypes: [],
        timeout: "",
        truncationAdapter: "",
      }),
    ).toBe(
      [
        "deepseek:",
        "  enabled: true",
        "  protocol: \"openai\"",
        "  base_url: \"https://api.deepseek.com\"",
        "  headers:",
        "    X-Trace-Id: \"trace-1\"",
        "  supported_models:",
        "    - \"deepseek-chat\"",
        "    - \"deepseek-reasoner\"",
        "  model_mappings:",
        "    \"*\": \"deepseek-chat\"",
      ].join("\n"),
    );
  });

  it("summarizes top-level sections from draft values", () => {
    expect(
      summarizeDraftSections({
        server: { host: "127.0.0.1", port: 8101 },
        provider_groups: [{ name: "default" }],
      }),
    ).toEqual([
      {
        id: "provider_groups",
        itemCount: 1,
        kind: "array",
        label: "provider_groups",
      },
      {
        id: "server",
        itemCount: 2,
        kind: "object",
        label: "server",
      },
    ]);
  });
});
