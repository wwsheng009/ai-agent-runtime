import { describe, expect, it } from "vitest";

import {
  buildProviderModelCapabilitiesRecord,
  emptyProviderModelDraft,
  formatProviderModelIDsText,
  formatReasoningEffortBudgetsText,
  mergeProviderModelDraft,
  mergeProviderModelDraftsWithCapabilities,
  mergeProviderModelDraftsWithMetadata,
  normalizeProviderModelIDs,
  parseProviderModelIDsText,
  parseReasoningEffortBudgetsText,
  parseReasoningEffortsText,
  providerModelDraftFromCapabilityFields,
  providerModelDraftIsEmpty,
  providerModelDraftToSpec,
  providerModelDraftsFromRecord,
  splitProviderModelIDInput,
} from "./model-capability-draft";
import {
  formatTokenCount,
  summarizeProviderModelDraft,
  toggleProviderModality,
} from "./model-capability-summary";

describe("providerModelDraftFromCapabilityFields", () => {
  it("maps a full capability spec into a draft", () => {
    const draft = providerModelDraftFromCapabilityFields({
      reasoning_model: true,
      replay_reasoning_content: false,
      input_modalities: ["text", "image"],
      reasoning_efforts: ["low", "high"],
      reasoning_effort_budgets: { high: 32768, low: 2048 },
      default_reasoning_effort: "high",
      compact_reasoning_effort: "low",
      max_context_tokens: 128000,
      max_tokens: 8192,
      auto_compact_ratio: 0.75,
      auto_compact_token_limit: 100000,
      auto_compact_mode: "local",
      supports_remote_compact: true,
      native_tools: { image_generation: true, images_generations_api: true },
    });

    expect(draft).toMatchObject({
      reasoningModel: true,
      replayReasoningContent: false,
      inputModalities: ["text", "image"],
      reasoningEffortsText: "low, high",
      reasoningEffortBudgetsText: "high: 32768\nlow: 2048",
      defaultReasoningEffort: "high",
      compactReasoningEffort: "low",
      maxContextTokensText: "128000",
      maxTokensText: "8192",
      autoCompactRatioText: "0.75",
      autoCompactTokenLimitText: "100000",
      autoCompactMode: "local",
      supportsRemoteCompact: true,
      imageGeneration: true,
      imagesGenerationsApi: true,
    });
  });

  it("keeps unknown capability fields for round-tripping", () => {
    const draft = providerModelDraftFromCapabilityFields({
      reasoning_model: true,
      future_capability: { nested: 1 },
    });
    expect(draft.extraFields).toEqual({ future_capability: { nested: 1 } });
    expect(providerModelDraftToSpec(draft)).toEqual({
      reasoning_model: true,
      future_capability: { nested: 1 },
    });
  });

  it("treats replay_reasoning_content as tri-state", () => {
    expect(
      providerModelDraftFromCapabilityFields({}).replayReasoningContent,
    ).toBeNull();
    expect(
      providerModelDraftFromCapabilityFields({
        replay_reasoning_content: true,
      }).replayReasoningContent,
    ).toBe(true);
    expect(
      providerModelDraftFromCapabilityFields({
        replay_reasoning_content: false,
      }).replayReasoningContent,
    ).toBe(false);
  });
});

describe("providerModelDraftToSpec", () => {
  it("omits empty fields and returns null for an empty draft", () => {
    expect(providerModelDraftToSpec(emptyProviderModelDraft())).toBeNull();
    expect(providerModelDraftIsEmpty(emptyProviderModelDraft())).toBe(true);
  });

  it("writes false booleans as removal, not as explicit values", () => {
    const draft = {
      ...emptyProviderModelDraft(),
      reasoningModel: false,
      supportsRemoteCompact: false,
      imageGeneration: false,
    };
    expect(providerModelDraftToSpec(draft)).toBeNull();
  });

  it("round-trips a configured draft through the spec record", () => {
    const draft = {
      ...emptyProviderModelDraft(),
      reasoningModel: true,
      reasoningEffortsText: "low, medium, high",
      maxContextTokensText: "200000",
      imageGeneration: true,
    };
    expect(providerModelDraftToSpec(draft)).toEqual({
      reasoning_model: true,
      reasoning_efforts: ["low", "medium", "high"],
      max_context_tokens: 200000,
      native_tools: { image_generation: true },
    });
  });
});

describe("mergeProviderModelDraft", () => {
  it("only lets non-blank incoming fields overwrite", () => {
    const base = {
      ...emptyProviderModelDraft(),
      maxTokensText: "4096",
      imageGeneration: true,
      replayReasoningContent: true as boolean | null,
    };
    const incoming = providerModelDraftFromCapabilityFields({
      reasoning_model: false,
      max_tokens: 0,
      input_modalities: null,
      native_tools: { image_generation: false },
      max_context_tokens: 128000,
    });
    const merged = mergeProviderModelDraft(base, incoming);
    expect(merged).toMatchObject({
      maxTokensText: "4096",
      imageGeneration: true,
      replayReasoningContent: true,
      maxContextTokensText: "128000",
      reasoningModel: false,
    });
  });
});

describe("mergeProviderModelDraftsWithMetadata", () => {
  it("merges metadata field by field and reports no change", () => {
    const current = {
      m: { ...emptyProviderModelDraft(), maxTokensText: "4096" },
    };
    const merged = mergeProviderModelDraftsWithMetadata(current, {
      m: { id: "m", reasoning_model: true, max_context_tokens: 128000 },
    });
    expect(merged?.m).toMatchObject({
      reasoningModel: true,
      maxContextTokensText: "128000",
      maxTokensText: "4096",
    });
    expect(
      mergeProviderModelDraftsWithMetadata(merged ?? current, {
        m: { id: "m", reasoning_model: true, max_context_tokens: 128000 },
      }),
    ).toBeNull();
  });

  it("returns null for empty or missing metadata", () => {
    expect(mergeProviderModelDraftsWithMetadata({}, undefined)).toBeNull();
    expect(mergeProviderModelDraftsWithMetadata({}, {})).toBeNull();
  });
});

describe("mergeProviderModelDraftsWithCapabilities", () => {
  it("ignores zero values from the auto-import payload", () => {
    const current = {
      m: { ...emptyProviderModelDraft(), maxTokensText: "4096" },
    };
    expect(
      mergeProviderModelDraftsWithCapabilities(current, {
        m: { max_tokens: 0, input_modalities: null },
      }),
    ).toBeNull();
    const merged = mergeProviderModelDraftsWithCapabilities(current, {
      m: { reasoning_model: true, max_tokens: 0 },
    });
    expect(merged?.m).toMatchObject({
      reasoningModel: true,
      maxTokensText: "4096",
    });
  });
});

describe("text parsing helpers", () => {
  it("parses and formats reasoning efforts", () => {
    expect(parseReasoningEffortsText("low, high low")).toEqual(["low", "high"]);
    expect(parseReasoningEffortBudgetsText("low: 2048\n\nbad\nhigh: 32768")).toEqual({
      low: 2048,
      high: 32768,
    });
    expect(formatReasoningEffortBudgetsText({ high: 32768, low: 2048 })).toBe(
      "high: 32768\nlow: 2048",
    );
  });

  it("parses model lists from text and add-box input", () => {
    expect(parseProviderModelIDsText("a, b\nc a")).toEqual(["a", "b", "c"]);
    expect(splitProviderModelIDInput("a b，c")).toEqual(["a", "b", "c"]);
    expect(normalizeProviderModelIDs([" a ", "a", "", "b"])).toEqual(["a", "b"]);
    expect(formatProviderModelIDsText(["a", "b", "a"])).toBe("a\nb");
    expect(normalizeProviderModelIDs(undefined)).toEqual([]);
  });

  it("toggles modalities in vocabulary order", () => {
    expect(toggleProviderModality(["text"], "image", true)).toEqual([
      "text",
      "image",
    ]);
    expect(toggleProviderModality(["text", "image"], "text", false)).toEqual([
      "image",
    ]);
    expect(toggleProviderModality(["video"], "text", true)).toEqual([
      "text",
      "video",
    ]);
  });
});

describe("providerModelDraftsFromRecord", () => {
  it("reads valid entries and skips malformed ones", () => {
    const drafts = providerModelDraftsFromRecord({
      m1: { reasoning_model: true },
      " m2 ": { max_tokens: 4096 },
      broken: 42,
      "": { reasoning_model: true },
    });
    expect(Object.keys(drafts).sort()).toEqual(["m1", "m2"]);
    expect(drafts.m2.maxTokensText).toBe("4096");
  });
});

describe("buildProviderModelCapabilitiesRecord", () => {
  it("skips empty drafts and keeps configured ones", () => {
    const record = buildProviderModelCapabilitiesRecord({
      configured: {
        ...emptyProviderModelDraft(),
        maxContextTokensText: "128000",
      },
      blank: emptyProviderModelDraft(),
    });
    expect(record).toEqual({ configured: { max_context_tokens: 128000 } });
  });
});

describe("summarizeProviderModelDraft", () => {
  it("exposes display-friendly values", () => {
    const summary = summarizeProviderModelDraft({
      ...emptyProviderModelDraft(),
      reasoningModel: true,
      reasoningEffortsText: "low, high",
      maxContextTokensText: "128000",
      autoCompactTokenLimitText: "100000",
    });
    expect(summary).toMatchObject({
      reasoningModel: true,
      reasoningEffortCount: 2,
      maxContextTokens: 128000,
      autoCompactTokenLimit: 100000,
    });
    expect(formatTokenCount(128000)).toBe("128K");
    expect(formatTokenCount(1048576)).toBe("1M");
    expect(formatTokenCount(512)).toBe("512");
  });
});
