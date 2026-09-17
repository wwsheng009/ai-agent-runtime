import { describe, expect, it } from "vitest";

import {
  filterOptionValues,
  isOptionFilterEmpty,
  normalizeOptionQuery,
} from "./model-panel-option-filter";

const OPTIONS = ["OpenAI", "azure-openai", "anthropic", "google"];

describe("model-panel-option-filter", () => {
  it("normalizes a query by trimming and lowercasing", () => {
    expect(normalizeOptionQuery("  OpenAI  ")).toBe("openai");
  });

  it("returns the original list untouched for blank queries", () => {
    expect(filterOptionValues(OPTIONS, "")).toBe(OPTIONS);
    expect(filterOptionValues(OPTIONS, "   ")).toBe(OPTIONS);
  });

  it("filters case-insensitively by substring while preserving order", () => {
    expect(filterOptionValues(OPTIONS, "openai")).toEqual([
      "OpenAI",
      "azure-openai",
    ]);
    expect(filterOptionValues(OPTIONS, "AN")).toEqual(["anthropic"]);
    expect(filterOptionValues(OPTIONS, "  GOO ")).toEqual(["google"]);
  });

  it("only reports the empty state for an active query with no match", () => {
    expect(isOptionFilterEmpty(OPTIONS, "zzz")).toBe(true);
    expect(isOptionFilterEmpty(OPTIONS, "")).toBe(false);
    expect(isOptionFilterEmpty(OPTIONS, "   ")).toBe(false);
    expect(isOptionFilterEmpty(OPTIONS, "openai")).toBe(false);
  });
});
