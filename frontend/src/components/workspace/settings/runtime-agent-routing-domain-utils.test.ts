import { describe, expect, it } from "vitest";

import {
  analyzeRuntimeAgentRoutingConfig,
  buildRuntimeAgentRoutingRecord,
  getRuntimeAgentRoutingSettings,
  providerModelOptions,
  providerReasoningEffortOptions,
  updateRuntimeAgentRoutingConfig,
  updateRuntimeTeamRoutingInheritance,
} from "./runtime-agent-routing-domain-utils";
import { type RuntimeProviderSummary } from "./runtime-provider-config-utils";

describe("runtime agent routing domain utils", () => {
  it("uses subagent routing as the team default when no team policy exists", () => {
    const settings = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            default_difficulty: "hard",
            levels: {
              hard: { provider: "strong", model: "strong-model" },
            },
          },
        },
      },
    });

    expect(settings.teamUsesSubagentRouting).toBe(true);
    expect(settings.subagents.defaultDifficulty).toBe("hard");
    expect(settings.teams.levels.hard).toEqual({
      provider: "strong",
      model: "strong-model",
      reasoningEffort: "",
    });
  });

  it("reads an independent team routing policy", () => {
    const settings = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: { routing: { enabled: true } },
        teams: {
          routing: {
            enabled: true,
            levels: {
              expert: { provider: "team", model: "team-model" },
            },
          },
        },
      },
    });

    expect(settings.teamUsesSubagentRouting).toBe(false);
    expect(settings.teams.levels.expert.provider).toBe("team");
  });

  it("preserves advanced fields while updating the four difficulty routes", () => {
    const settings = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            roles: { verifier: { hard: { model: "audit" } } },
            levels: { hard: { timeout: "2m", thinking_effort: "high" } },
          },
        },
      },
    });
    settings.subagents.levels.hard = {
      provider: "strong",
      model: "strong-model",
      reasoningEffort: "medium",
    };

    const record = buildRuntimeAgentRoutingRecord(settings.subagents);
    const levels = record.levels as Record<string, Record<string, unknown>>;
    expect(record.roles).toEqual({ verifier: { hard: { model: "audit" } } });
    expect(levels.hard).toEqual({
      timeout: "2m",
      provider: "strong",
      model: "strong-model",
      reasoning_effort: "medium",
    });
  });

  it("builds model options from provider defaults and supported models", () => {
    const options = providerModelOptions(
      [provider("strong", "model-a", ["model-a", "model-b"])],
      "strong",
      "custom-model",
    );
    expect(options.map((option) => option.value)).toEqual([
      "custom-model",
      "model-a",
      "model-b",
    ]);
  });

  it("uses model capability reasoning efforts instead of a fixed option list", () => {
    const strong = provider("strong", "model-a", ["model-a"]);
    strong.raw.model_capabilities = {
      "model-a": {
        reasoning_model: true,
        reasoning_efforts: ["minimal", "high", "max"],
      },
    };

    const options = providerReasoningEffortOptions(
      [strong],
      "strong",
      "model-a",
      "legacy-effort",
    );

    expect(options.map((option) => option.value)).toEqual([
      "minimal",
      "high",
      "max",
      "legacy-effort",
    ]);
  });

  it("writes the selected scope without replacing unrelated aicli settings", () => {
    const initial = {
      aicli: {
        chat: { default_model: "parent-model" },
        subagents: { routing: { enabled: false } },
      },
    };
    const nextConfig = getRuntimeAgentRoutingSettings(initial).subagents;
    nextConfig.enabled = true;
    nextConfig.levels.hard = {
      provider: "strong",
      model: "strong-model",
      reasoningEffort: "high",
    };

    const updated = updateRuntimeAgentRoutingConfig(
      initial,
      "subagents",
      nextConfig,
    ) as typeof initial;

    expect(updated.aicli.chat).toEqual({ default_model: "parent-model" });
    expect(updated.aicli.subagents.routing).toMatchObject({
      enabled: true,
      levels: {
        hard: {
          provider: "strong",
          model: "strong-model",
          reasoning_effort: "high",
        },
      },
    });
  });

  it("creates and removes an independent team policy while preserving team settings", () => {
    const initial = {
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            levels: { hard: { provider: "strong", model: "strong-model" } },
          },
        },
        teams: { max_teammates: 4 },
      },
    };

    const independent = updateRuntimeTeamRoutingInheritance(initial, false);
    expect(getRuntimeAgentRoutingSettings(independent).teamUsesSubagentRouting).toBe(
      false,
    );
    expect(getRuntimeAgentRoutingSettings(independent).teams.levels.hard).toEqual({
      provider: "strong",
      model: "strong-model",
      reasoningEffort: "",
    });
    expect(
      (independent as { aicli: { teams: { max_teammates: number } } }).aicli.teams
        .max_teammates,
    ).toBe(4);

    const inherited = updateRuntimeTeamRoutingInheritance(independent, true);
    expect(getRuntimeAgentRoutingSettings(inherited).teamUsesSubagentRouting).toBe(
      true,
    );
    expect(
      (inherited as { aicli: { teams: { max_teammates: number } } }).aicli.teams
        .max_teammates,
    ).toBe(4);
  });

  it("classifies inherited, provider-default, and configured routes", () => {
    const config = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            levels: {
              normal: { provider: "fast" },
              hard: { provider: "strong", model: "strong-model" },
            },
          },
        },
      },
    }).subagents;

    const health = analyzeRuntimeAgentRoutingConfig(config, [
      provider("fast", "fast-model", ["fast-model"]),
      provider("strong", "strong-model", ["strong-model"]),
    ]);

    expect(health.routes.easy.mode).toBe("inherited");
    expect(health.routes.normal).toMatchObject({
      effectiveModel: "fast-model",
      effectiveProvider: "fast",
      mode: "providerDefault",
    });
    expect(health.routes.hard.mode).toBe("configured");
    expect(health.configuredCount).toBe(2);
    expect(health.inheritedCount).toBe(2);
  });

  it("reports provider availability and partial-route risks with fallback severity", () => {
    const config = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            levels: {
              easy: { provider: "missing", model: "model-a" },
              normal: { provider: "disabled", model: "model-b" },
              hard: { model: "parent-specific-model" },
            },
          },
        },
      },
    }).subagents;
    const disabled = provider("disabled", "model-b", ["model-b"]);
    disabled.enabled = false;

    const health = analyzeRuntimeAgentRoutingConfig(config, [disabled]);

    expect(health.routes.easy.issues).toContainEqual({
      code: "unknownProvider",
      severity: "warning",
    });
    expect(health.routes.normal.issues).toContainEqual({
      code: "disabledProvider",
      severity: "warning",
    });
    expect(health.routes.hard.issues).toContainEqual({
      code: "modelWithoutProvider",
      severity: "warning",
    });
  });

  it("warns about ambiguous provider aliases and unlisted models", () => {
    const config = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            levels: {
              easy: { provider: "shared-model", model: "shared-model" },
              hard: { provider: "strong", model: "custom-model" },
            },
          },
        },
      },
    }).subagents;

    const health = analyzeRuntimeAgentRoutingConfig(config, [
      provider("first", "shared-model", ["shared-model"]),
      provider("second", "shared-model", ["shared-model"]),
      provider("strong", "strong-model", ["strong-model"]),
    ]);

    expect(health.routes.easy.issues).toContainEqual({
      code: "ambiguousProviderAlias",
      severity: "warning",
    });
    expect(health.routes.hard.issues).toContainEqual({
      code: "modelNotListed",
      severity: "warning",
    });
  });

  it("accepts a custom route model covered by a wildcard model mapping", () => {
    const config = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            levels: {
              hard: { provider: "strong", model: "logical-model" },
            },
          },
        },
      },
    }).subagents;
    const strong = provider("strong", "strong-model", ["strong-model"]);
    strong.raw.model_mappings = { "*": "strong-model" };

    const health = analyzeRuntimeAgentRoutingConfig(config, [strong]);

    expect(health.routes.hard.issues).not.toContainEqual({
      code: "modelNotListed",
      severity: "warning",
    });
  });

  it("mirrors fail policy for a configured unsupported reasoning effort", () => {
    const config = getRuntimeAgentRoutingSettings({
      aicli: {
        subagents: {
          routing: {
            enabled: true,
            unsupported_reasoning_policy: "fail",
            levels: {
              expert: {
                provider: "strong",
                model: "strong-model",
                reasoning_effort: "xhigh",
              },
            },
          },
        },
      },
    }).subagents;
    const strong = provider("strong", "strong-model", ["strong-model"]);
    strong.raw.model_capabilities = {
      "strong-model": {
        reasoning_model: true,
        reasoning_efforts: ["low", "medium", "high"],
      },
    };

    const health = analyzeRuntimeAgentRoutingConfig(config, [strong]);

    expect(health.routes.expert.issues).toContainEqual({
      code: "reasoningUnsupported",
      severity: "error",
    });
    expect(health.errorCount).toBe(1);
  });
});

function provider(
  name: string,
  defaultModel: string,
  supportedModels: string[],
): RuntimeProviderSummary {
  return {
    account: null,
    accountAuthRef: "",
    accountSummary: "",
    apiKey: "",
    apiPath: "",
    baseUrl: "",
    defaultModel,
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
    supportedModels,
    supportTypes: ["openai"],
    timeout: "300s",
    truncationAdapter: "openai_local",
  };
}
