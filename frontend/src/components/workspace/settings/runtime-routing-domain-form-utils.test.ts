import { describe, expect, it } from "vitest";

import {
  buildRouteRecordFromDraft,
  type RouteDraftInput,
} from "./runtime-routing-domain-form-utils";

function createDraft(overrides?: Partial<RouteDraftInput>): RouteDraftInput {
  return {
    matchPath: "/v1/responses",
    matchType: "prefix",
    group: "codex_group",
    protocol: "codex",
    priority: "10",
    matchModelsText: "gpt-5.4",
    matchModelRegexesText: "^gpt-5\\..*$",
    excludeModelsText: "gpt-5.1",
    excludeModelRegexesText: "^o[0-9].*$",
    extraJson: JSON.stringify({ custom_flag: true }),
    ...overrides,
  };
}

describe("runtime-routing-domain-form-utils", () => {
  it("builds a route record from the modal draft", () => {
    const result = buildRouteRecordFromDraft(createDraft());

    expect(result.error).toBeNull();
    expect(result.record).toMatchObject({
      match_path: "/v1/responses",
      match_type: "prefix",
      group: "codex_group",
      protocol: "codex",
      priority: 10,
      match_models: ["gpt-5.4"],
      match_model_regexes: ["^gpt-5\\..*$"],
      exclude_models: ["gpt-5.1"],
      exclude_model_regexes: ["^o[0-9].*$"],
      custom_flag: true,
    });
  });

  it("rejects missing group", () => {
    const result = buildRouteRecordFromDraft(
      createDraft({
        group: "   ",
      }),
    );

    expect(result.record).toBeNull();
    expect(result.error).toContain("group");
  });
});
