import { describe, expect, it } from "vitest";

import {
  mergeExtraJsonField,
  mergeModelCapabilitiesIntoExtraJson,
} from "./runtime-provider-capability-utils";

describe("mergeModelCapabilitiesIntoExtraJson", () => {
  it("writes matched capabilities into an empty extra json", () => {
    const merged = mergeModelCapabilitiesIntoExtraJson("{}", {
      "gpt-4o": { reasoning_model: true, reasoning_efforts: ["low", "high"] },
    });
    expect(JSON.parse(merged ?? "{}")).toEqual({
      model_capabilities: {
        "gpt-4o": { reasoning_model: true, reasoning_efforts: ["low", "high"] },
      },
    });
  });

  it("treats a blank extra json as an empty object", () => {
    const merged = mergeModelCapabilitiesIntoExtraJson("", {
      m: { max_context_tokens: 128000 },
    });
    expect(JSON.parse(merged ?? "{}")).toEqual({
      model_capabilities: { m: { max_context_tokens: 128000 } },
    });
  });

  it("keeps unrelated extra fields and models outside the response", () => {
    const extraJson = JSON.stringify({
      models_verified_at: "2026-09-20T00:00:00Z",
      model_capabilities: {
        keep: { max_tokens: 4096 },
        "gpt-4o": { max_tokens: 1024 },
      },
    });
    const merged = mergeModelCapabilitiesIntoExtraJson(extraJson, {
      "gpt-4o": { reasoning_model: true },
    });
    expect(JSON.parse(merged ?? "{}")).toEqual({
      models_verified_at: "2026-09-20T00:00:00Z",
      model_capabilities: {
        keep: { max_tokens: 4096 },
        "gpt-4o": { max_tokens: 1024, reasoning_model: true },
      },
    });
  });

  it("never lets zero values from the response clobber saved fields", () => {
    const extraJson = JSON.stringify({
      model_capabilities: {
        m: {
          reasoning_model: true,
          max_tokens: 8192,
          native_tools: { image_generation: true },
        },
      },
    });
    const merged = mergeModelCapabilitiesIntoExtraJson(extraJson, {
      m: {
        reasoning_model: false,
        max_tokens: 0,
        input_modalities: null,
        native_tools: { image_generation: false, images_generations_api: false },
      },
    });
    expect(merged).toBeUndefined();
  });

  it("merges nested objects field by field", () => {
    const extraJson = JSON.stringify({
      model_capabilities: { m: { native_tools: { image_generation: true } } },
    });
    const merged = mergeModelCapabilitiesIntoExtraJson(extraJson, {
      m: { native_tools: { images_generations_api: true } },
    });
    expect(JSON.parse(merged ?? "{}").model_capabilities.m.native_tools).toEqual({
      image_generation: true,
      images_generations_api: true,
    });
  });

  it("reports no change for identical capabilities, empty input and bad json", () => {
    const extraJson = JSON.stringify({
      model_capabilities: { m: { reasoning_model: true } },
    });
    expect(
      mergeModelCapabilitiesIntoExtraJson(extraJson, {
        m: { reasoning_model: true },
      }),
    ).toBeUndefined();
    expect(mergeModelCapabilitiesIntoExtraJson("{}", {})).toBeUndefined();
    expect(
      mergeModelCapabilitiesIntoExtraJson("{not json", {
        m: { reasoning_model: true },
      }),
    ).toBeUndefined();
  });
});

describe("mergeExtraJsonField", () => {
  it("writes a scalar field and reports an unchanged rewrite", () => {
    const merged = mergeExtraJsonField("{}", "max_tokens_limit", 200000);
    expect(JSON.parse(merged ?? "{}")).toEqual({ max_tokens_limit: 200000 });
    expect(
      mergeExtraJsonField(merged ?? "", "max_tokens_limit", 200000),
    ).toBeUndefined();
  });

  it("leaves bad json untouched", () => {
    expect(mergeExtraJsonField("[1,2]", "max_tokens_limit", 1)).toBeUndefined();
  });
});
