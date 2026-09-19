import { describe, expect, it } from "vitest";

import {
  appendProviderModels,
  buildProviderOpsRequest,
  collectProbeSupportedModels,
} from "./provider-ops";

describe("buildProviderOpsRequest", () => {
  it("omits blank fields so the backend falls back to the saved snapshot", () => {
    expect(
      buildProviderOpsRequest({
        name: "  relay  ",
        baseUrl: "",
        apiKey: "   ",
        protocol: "openai",
        modelsPath: undefined,
        headers: {},
        timeoutSeconds: 0,
      }),
    ).toEqual({ name: "relay", protocol: "openai" });
  });

  it("trims values and keeps non-empty headers and positive timeouts", () => {
    expect(
      buildProviderOpsRequest({
        baseUrl: "https://api.example.com ",
        headers: { "X-Trace": "1" },
        timeoutSeconds: 12.7,
      }),
    ).toEqual({
      base_url: "https://api.example.com",
      headers: { "X-Trace": "1" },
      timeout_seconds: 12,
    });
  });

  it("ignores non-finite / negative timeouts", () => {
    expect(buildProviderOpsRequest({ timeoutSeconds: Number.NaN })).toEqual({});
    expect(buildProviderOpsRequest({ timeoutSeconds: -5 })).toEqual({});
  });
});

describe("appendProviderModels", () => {
  it("keeps manual rows first and appends only new IDs", () => {
    expect(
      appendProviderModels(["gpt-4o", " o3 "], ["o3", "gpt-5", "gpt-5"]),
    ).toEqual(["gpt-4o", "o3", "gpt-5"]);
  });

  it("drops blanks and duplicates already present", () => {
    expect(appendProviderModels(["a"], ["a", "  ", "b"])).toEqual(["a", "b"]);
  });

  it("tolerates empty inputs", () => {
    expect(appendProviderModels([], [])).toEqual([]);
    expect(appendProviderModels([], ["x"])).toEqual(["x"]);
  });
});

describe("collectProbeSupportedModels", () => {
  it("keeps only verified (ok) models, de-duplicated and in order", () => {
    expect(
      collectProbeSupportedModels([
        { model_id: "gpt-4o", protocol: "openai", verdict: "ok" },
        { model_id: "gpt-4o", protocol: "anthropic", verdict: "unsupported" },
        { model_id: "o3", protocol: "openai", verdict: "ok" },
        { model_id: "o3", protocol: "openai", verdict: "ok" },
        { model_id: " claude ", protocol: "anthropic", verdict: "ok" },
        { model_id: "", protocol: "openai", verdict: "ok" },
      ]),
    ).toEqual(["gpt-4o", "o3", "claude"]);
  });

  it("returns an empty list for missing results", () => {
    expect(collectProbeSupportedModels(undefined)).toEqual([]);
  });
});
