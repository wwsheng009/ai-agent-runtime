import { describe, expect, it } from "vitest";

import {
  DEFAULT_REASONING_EFFORT_VALUE,
  findModelCapability,
  isSupportedReasoningEffort,
  readSessionReasoningEffort,
  resolveDefaultReasoningEffort,
  resolveEffectiveReasoningEffort,
  resolveModelReasoningEffortOptions,
} from "@/lib/reasoning-effort";
import type { RuntimeModelProviderRecord } from "@/types/runtime";

function providerWithCapabilities(
  model_capabilities: RuntimeModelProviderRecord["model_capabilities"],
): RuntimeModelProviderRecord {
  return {
    name: "demo",
    models: ["reasoner", "plain"],
    model_capabilities,
  };
}

describe("readSessionReasoningEffort", () => {
  it("reads a trimmed session-level value", () => {
    expect(readSessionReasoningEffort({ reasoning_effort: " high " })).toBe(
      "high",
    );
  });

  it("returns empty string for missing or invalid values", () => {
    expect(readSessionReasoningEffort(undefined)).toBe("");
    expect(readSessionReasoningEffort({})).toBe("");
    expect(readSessionReasoningEffort({ reasoning_effort: 3 })).toBe("");
  });
});

describe("findModelCapability", () => {
  it("prefers the exact model entry over the wildcard", () => {
    const capability = findModelCapability(
      providerWithCapabilities({
        reasoner: { reasoning_efforts: ["low", "high"] },
        "*": { reasoning_efforts: ["minimal"] },
      }),
      "reasoner",
    );
    expect(capability?.reasoning_efforts).toEqual(["low", "high"]);
  });

  it("falls back to the wildcard capability", () => {
    const capability = findModelCapability(
      providerWithCapabilities({ "*": { reasoning_model: true } }),
      "plain",
    );
    expect(capability?.reasoning_model).toBe(true);
  });
});

describe("resolveModelReasoningEffortOptions", () => {
  it("filters to the declared capability efforts", () => {
    expect(
      resolveModelReasoningEffortOptions(
        providerWithCapabilities({
          reasoner: { reasoning_model: true, reasoning_efforts: ["low", "medium"] },
        }),
        "reasoner",
      ),
    ).toEqual(["low", "medium"]);
  });

  it("deduplicates declared efforts while preserving order", () => {
    expect(
      resolveModelReasoningEffortOptions(
        providerWithCapabilities({
          reasoner: { reasoning_efforts: ["Low", "low", " high "] },
        }),
        "reasoner",
      ),
    ).toEqual(["Low", "high"]);
  });

  it("falls back to the preset ladder for reasoning models without explicit efforts", () => {
    expect(
      resolveModelReasoningEffortOptions(
        providerWithCapabilities({ reasoner: { reasoning_model: true } }),
        "reasoner",
      ),
    ).toEqual(["minimal", "low", "medium", "high"]);
  });

  it("hides the ladder when the capability marks a non-reasoning model", () => {
    expect(
      resolveModelReasoningEffortOptions(
        providerWithCapabilities({ plain: { reasoning_model: false } }),
        "plain",
      ),
    ).toEqual([]);
  });

  it("keeps the preset ladder when the provider exposes no capabilities", () => {
    expect(
      resolveModelReasoningEffortOptions(
        { name: "demo", models: ["plain"] },
        "plain",
      ),
    ).toEqual(["minimal", "low", "medium", "high"]);
  });
});

describe("resolveEffectiveReasoningEffort", () => {
  it("keeps a supported selection", () => {
    expect(resolveEffectiveReasoningEffort("high", ["low", "high"])).toBe("high");
  });

  it("falls back to default when the selection is unsupported", () => {
    expect(resolveEffectiveReasoningEffort("xhigh", ["low", "high"])).toBe(
      DEFAULT_REASONING_EFFORT_VALUE,
    );
  });

  it("falls back to default when the model exposes no options", () => {
    expect(resolveEffectiveReasoningEffort("high", [])).toBe(
      DEFAULT_REASONING_EFFORT_VALUE,
    );
  });
});

describe("resolveDefaultReasoningEffort", () => {
  it("prefers the model capability default", () => {
    expect(
      resolveDefaultReasoningEffort(
        providerWithCapabilities({
          reasoner: { default_reasoning_effort: "low" },
        }),
        "reasoner",
        "medium",
      ),
    ).toBe("low");
  });

  it("falls back to the config default", () => {
    expect(
      resolveDefaultReasoningEffort(
        providerWithCapabilities({ reasoner: {} }),
        "reasoner",
        " medium ",
      ),
    ).toBe("medium");
  });
});

describe("isSupportedReasoningEffort", () => {
  it("matches case-insensitively", () => {
    expect(isSupportedReasoningEffort("HIGH", ["low", "high"])).toBe(true);
    expect(isSupportedReasoningEffort("xhigh", ["low", "high"])).toBe(false);
  });
});
