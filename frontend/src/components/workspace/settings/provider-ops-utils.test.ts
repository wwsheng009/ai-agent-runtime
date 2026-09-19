import { describe, expect, it } from "vitest";

import type {
  ProviderAutoImportResult,
  ProviderModelsResult,
  ProviderProbeResult,
} from "@/types/runtime";

import {
  buildProviderOpsRequestFromDraft,
  groupProbeResultsByModel,
  isKnownProviderProtocol,
  joinProviderOpsWarnings,
  normalizeProviderModelIDs,
  parseDraftHeaders,
  parseGoDurationSeconds,
  parseSupportedModelsText,
  providerAutoImportPatch,
  providerModelsPatch,
  resolveProbeModels,
  summarizeProbeResults,
} from "./provider-ops-utils";
import { createProviderDraftInput } from "./runtime-provider-domain-editor/draft-utils";

function draftWith(overrides: Partial<ReturnType<typeof createProviderDraftInput>>) {
  return { ...createProviderDraftInput(null, "openai"), ...overrides };
}

describe("buildProviderOpsRequestFromDraft", () => {
  it("prefers the saved provider name and trims blank fields away", () => {
    const request = buildProviderOpsRequestFromDraft(
      draftWith({ name: "draft-name", baseUrl: "  https://api.example.com  " }),
      "  saved-name ",
    );
    expect(request).toEqual({
      name: "saved-name",
      base_url: "https://api.example.com",
      protocol: "openai",
      timeout_seconds: 300,
    });
  });

  it("falls back to the draft name for unsaved providers", () => {
    const request = buildProviderOpsRequestFromDraft(
      draftWith({ name: " new-provider ", baseUrl: "" }),
    );
    expect(request.name).toBe("new-provider");
    expect(request.base_url).toBeUndefined();
  });

  it("omits a blank api_key so the backend keeps the stored secret", () => {
    const request = buildProviderOpsRequestFromDraft(
      draftWith({ apiKey: "   " }),
      "saved",
    );
    expect(request.api_key).toBeUndefined();
  });

  it("carries parsed headers and duration seconds", () => {
    const request = buildProviderOpsRequestFromDraft(
      draftWith({
        headersJson: JSON.stringify({ "X-Trace": "abc", skipped: 1 }),
        timeout: "5m",
      }),
      "saved",
    );
    expect(request.headers).toEqual({ "X-Trace": "abc" });
    expect(request.timeout_seconds).toBe(300);
  });
});

describe("parseDraftHeaders", () => {
  it("drops non-string and blank entries", () => {
    expect(
      parseDraftHeaders(JSON.stringify({ a: "1", b: "", c: 2, " ": "3" })),
    ).toEqual({ a: "1" });
  });

  it("returns undefined for empty or malformed input", () => {
    expect(parseDraftHeaders("")).toBeUndefined();
    expect(parseDraftHeaders("{}")).toBeUndefined();
    expect(parseDraftHeaders("{not json")).toBeUndefined();
    expect(parseDraftHeaders("[1,2]")).toBeUndefined();
  });
});

describe("parseGoDurationSeconds", () => {
  it("parses composite Go durations", () => {
    expect(parseGoDurationSeconds("1h30m")).toBe(5400);
    expect(parseGoDurationSeconds("300s")).toBe(300);
    expect(parseGoDurationSeconds("500ms")).toBe(1);
  });

  it("treats bare numbers as seconds and rejects junk", () => {
    expect(parseGoDurationSeconds("90")).toBe(90);
    expect(parseGoDurationSeconds("0")).toBeUndefined();
    expect(parseGoDurationSeconds("5 minutes")).toBeUndefined();
    expect(parseGoDurationSeconds("m5")).toBeUndefined();
    expect(parseGoDurationSeconds("1h30")).toBeUndefined();
    expect(parseGoDurationSeconds("")).toBeUndefined();
  });
});

describe("normalizeProviderModelIDs", () => {
  it("trims, drops blanks and de-duplicates while preserving order", () => {
    expect(normalizeProviderModelIDs([" b ", "a", "b", "", "  ", "a"])).toEqual([
      "b",
      "a",
    ]);
  });

  it("tolerates undefined", () => {
    expect(normalizeProviderModelIDs(undefined)).toEqual([]);
  });
});

describe("providerModelsPatch", () => {
  it("replaces supported models with the fetched list", () => {
    const result = { model_ids: ["gpt-4o", " gpt-4o ", "o3"] } as ProviderModelsResult;
    expect(providerModelsPatch(result)).toEqual({
      supportedModelsText: "gpt-4o\no3",
    });
  });

  it("keeps the form untouched when the fetch returned nothing", () => {
    expect(
      providerModelsPatch({ model_ids: [] } as unknown as ProviderModelsResult),
    ).toBeNull();
  });
});

describe("providerAutoImportPatch", () => {
  it("maps editable fields and ignores unknown protocols", () => {
    const result = {
      name: "site",
      protocol: "anthropic",
      base_url: "https://api.example.com",
      api_path: "/v1/messages",
      forward_url: "",
      default_model: "claude-3-7-sonnet",
      supported_models: ["claude-3-7-sonnet", " claude-3-5-haiku "],
      support_types: ["chat"],
      site_type: "newapi",
      site_type_confidence: "high",
      site_type_scores: { newapi: 0.9 },
      account: null,
      warnings: ["w1"],
    } as unknown as ProviderAutoImportResult;
    expect(providerAutoImportPatch(result)).toEqual({
      protocol: "anthropic",
      baseUrl: "https://api.example.com",
      apiPath: "/v1/messages",
      defaultModel: "claude-3-7-sonnet",
      supportedModelsText: "claude-3-7-sonnet\nclaude-3-5-haiku",
      supportTypesText: "chat",
      siteType: "newapi",
      siteTypeConfidence: "high",
      siteTypeScores: { newapi: 0.9 },
    });
  });

  it("does not guess an unknown protocol", () => {
    const result = {
      name: "site",
      protocol: "mystery",
      base_url: "",
    } as unknown as ProviderAutoImportResult;
    expect(providerAutoImportPatch(result).protocol).toBeUndefined();
  });

  it("treats an empty site_type_scores map as no change", () => {
    const result = {
      name: "site",
      protocol: "openai",
      base_url: "",
      site_type_scores: {},
    } as unknown as ProviderAutoImportResult;
    expect(providerAutoImportPatch(result).siteTypeScores).toBeUndefined();
  });
});

describe("isKnownProviderProtocol", () => {
  it("accepts catalog values and rejects unknown ones", () => {
    expect(isKnownProviderProtocol("openai")).toBe(true);
    expect(isKnownProviderProtocol("codex")).toBe(true);
    expect(isKnownProviderProtocol("mystery")).toBe(false);
  });
});

describe("parseSupportedModelsText", () => {
  it("splits comma and newline separated ids", () => {
    expect(parseSupportedModelsText("a, b\nc")).toEqual(["a", "b", "c"]);
  });
});

describe("summarizeProbeResults", () => {
  it("counts verdicts, folding unknown verdicts into error", () => {
    const result = {
      results: [
        { model_id: "a", protocol: "openai", verdict: "ok" },
        { model_id: "b", protocol: "openai", verdict: "unsupported" },
        { model_id: "c", protocol: "openai", verdict: "invalid" },
      ],
    } as ProviderProbeResult;
    expect(summarizeProbeResults(result)).toEqual({
      total: 3,
      ok: 1,
      unsupported: 1,
      error: 1,
    });
  });

  it("handles a missing result", () => {
    expect(summarizeProbeResults(null)).toEqual({
      total: 0,
      ok: 0,
      unsupported: 0,
      error: 0,
    });
  });
});

describe("groupProbeResultsByModel", () => {
  it("groups multiple protocol probes under one model row", () => {
    const result = {
      results: [
        { model_id: "a", protocol: "openai", verdict: "ok" },
        { model_id: "b", protocol: "openai", verdict: "ok" },
        { model_id: "a", protocol: "anthropic", verdict: "unsupported" },
      ],
    } as ProviderProbeResult;
    const rows = groupProbeResultsByModel(result);
    expect(rows.map((row) => row.modelId)).toEqual(["a", "b"]);
    expect(rows[0].probes.map((probe) => probe.protocol)).toEqual([
      "openai",
      "anthropic",
    ]);
  });

  it("skips entries without a model id", () => {
    const result = {
      results: [{ model_id: "  ", protocol: "openai", verdict: "ok" }],
    } as ProviderProbeResult;
    expect(groupProbeResultsByModel(result)).toEqual([]);
  });
});

describe("resolveProbeModels", () => {
  it("prefers fetched candidates over the textarea", () => {
    const draft = draftWith({ supportedModelsText: "from-form" });
    expect(resolveProbeModels([" from-fetch "], draft)).toEqual(["from-fetch"]);
  });

  it("falls back to the supported-models textarea", () => {
    const draft = draftWith({ supportedModelsText: "a\nb" });
    expect(resolveProbeModels([], draft)).toEqual(["a", "b"]);
  });
});

describe("joinProviderOpsWarnings", () => {
  it("joins non-blank warnings and tolerates missing input", () => {
    expect(joinProviderOpsWarnings([" w1 ", "", "w2"])).toBe("w1; w2");
    expect(joinProviderOpsWarnings(undefined)).toBe("");
  });
});
