import { describe, expect, it } from "vitest";

import {
  buildProviderRecordFromDraft,
  type ProviderDraftInput,
} from "./runtime-provider-domain-form-utils";
import { emptyProviderModelDraft } from "./runtime-provider-domain-editor/model-capability-draft";

function createDraft(overrides?: Partial<ProviderDraftInput>): ProviderDraftInput {
  return {
    name: "deepseek",
    enabled: true,
    protocol: "openai",
    baseUrl: "https://api.deepseek.com",
    apiPath: "/v1/chat/completions",
    forwardUrl: "/v1/chat/completions",
    apiKey: "${DEEPSEEK_API_KEY}",
    defaultModel: "deepseek-chat",
    proxyEnabled: false,
    proxyHttp: "",
    proxyHttps: "",
    proxyNoProxy: "",
    supportedModelsText: "deepseek-chat\ndeepseek-reasoner",
    supportTypesText: "openai, codex",
    timeout: "300s",
    truncationAdapter: "openai_local",
    headersJson: JSON.stringify({ "x-org": "runtime" }),
    modelCapabilityDrafts: {},
    modelMappingsJson: JSON.stringify({ "*": "deepseek-chat" }),
    extraJson: JSON.stringify({ retries: 3 }),
    setAsDefault: true,
    siteType: "",
    siteTypeConfidence: "",
    siteTypeDetectedAt: "",
    siteTypeScores: {},
    accountAuthRef: "",
    account: null,
    systemAccessToken: "",
    subjectUserId: "",
    ...overrides,
  };
}

describe("runtime-provider-domain-editor", () => {
  it("builds a provider record from the modal draft", () => {
    const result = buildProviderRecordFromDraft(createDraft());

    expect(result.error).toBeNull();
    expect(result.record).toMatchObject({
      enabled: true,
      protocol: "openai",
      base_url: "https://api.deepseek.com",
      api_path: "/v1/chat/completions",
      forward_url: "/v1/chat/completions",
      api_key: "${DEEPSEEK_API_KEY}",
      default_model: "deepseek-chat",
      supported_models: ["deepseek-chat", "deepseek-reasoner"],
      support_types: ["openai", "codex"],
      timeout: "300s",
      truncation_adapter: "openai_local",
      headers: { "x-org": "runtime" },
      model_mappings: { "*": "deepseek-chat" },
      retries: 3,
    });
  });

  it("includes provider-level proxy override when configured", () => {
    const result = buildProviderRecordFromDraft(
      createDraft({
        proxyEnabled: true,
        proxyHttp: "http://127.0.0.1:10810",
        proxyHttps: "socks5://127.0.0.1:10811",
        proxyNoProxy: "localhost,127.0.0.1",
      }),
    );

    expect(result.error).toBeNull();
    expect(result.record).toMatchObject({
      proxy: {
        enabled: true,
        http: "http://127.0.0.1:10810",
        https: "socks5://127.0.0.1:10811",
        no_proxy: "localhost,127.0.0.1",
      },
    });
  });

  it("omits timeout key when the timeout input is cleared", () => {
    const result = buildProviderRecordFromDraft(createDraft({ timeout: "" }));

    expect(result.error).toBeNull();
    expect(result.record).not.toHaveProperty("timeout");
  });

  it("keeps raw timeout string preserving whitespace trimming", () => {
    const result = buildProviderRecordFromDraft(
      createDraft({ timeout: "  90s  " }),
    );

    expect(result.error).toBeNull();
    expect(result.record).toMatchObject({ timeout: "90s" });
  });

  it("rejects invalid provider JSON fields", () => {
    const result = buildProviderRecordFromDraft(
      createDraft({
        headersJson: "[1,2,3]",
      }),
    );

    expect(result.record).toBeNull();
    expect(result.error).toContain("headers");
  });

  it("includes site type and account cache fields from the draft", () => {
    const result = buildProviderRecordFromDraft(
      createDraft({
        siteType: "sub2api",
        siteTypeConfidence: "high",
        siteTypeDetectedAt: "2026-07-29T00:00:00Z",
        siteTypeScores: { sub2api: 3, newapi: 1 },
        accountAuthRef: "deepseek-account",
        account: {
          source: "sub2api",
          mode: "wallet",
          currency: "USD",
          wallet_balance: 12.5,
          fetched_at: "2026-07-29T00:01:00Z",
        },
      }),
    );

    expect(result.error).toBeNull();
    expect(result.record).toMatchObject({
      site_type: "sub2api",
      site_type_confidence: "high",
      site_type_detected_at: "2026-07-29T00:00:00Z",
      site_type_scores: { sub2api: 3, newapi: 1 },
      account_auth_ref: "deepseek-account",
      account: {
        source: "sub2api",
        mode: "wallet",
        currency: "USD",
        wallet_balance: 12.5,
        fetched_at: "2026-07-29T00:01:00Z",
      },
    });
  });

  it("serializes per-model capability drafts into model_capabilities", () => {
    const result = buildProviderRecordFromDraft(
      createDraft({
        modelCapabilityDrafts: {
          "deepseek-chat": {
            ...emptyProviderModelDraft(),
            maxContextTokensText: "128000",
            inputModalities: ["text", "image"],
            replayReasoningContent: true,
          },
          "deepseek-reasoner": {
            ...emptyProviderModelDraft(),
            reasoningModel: true,
            reasoningEffortsText: "low, high",
            reasoningEffortBudgetsText: "high: 32768",
          },
          unconfigured: emptyProviderModelDraft(),
        },
      }),
    );

    expect(result.error).toBeNull();
    expect(result.record?.model_capabilities).toEqual({
      "deepseek-chat": {
        max_context_tokens: 128000,
        input_modalities: ["text", "image"],
        replay_reasoning_content: true,
      },
      "deepseek-reasoner": {
        reasoning_model: true,
        reasoning_efforts: ["low", "high"],
        reasoning_effort_budgets: { high: 32768 },
      },
    });
  });
});
