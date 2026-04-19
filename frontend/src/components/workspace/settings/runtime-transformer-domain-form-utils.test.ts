import { describe, expect, it } from "vitest";

import { buildTransformerModifierRecordFromDraft } from "./runtime-transformer-domain-form-utils";

describe("runtime-transformer-domain-form-utils", () => {
  it("builds a transformer modifier record from draft input", () => {
    expect(
      buildTransformerModifierRecordFromDraft(
        {
          type: "disable_params",
          enabled: true,
          modelsText: "claude-*\n gpt-*",
          paramsJson: '{\n  "params": ["temperature", "top_p"]\n}',
          extraJson: '{\n  "owner": "platform"\n}',
        },
        "request",
      ),
    ).toEqual({
      error: null,
      record: {
        owner: "platform",
        type: "disable_params",
        enabled: true,
        models: ["claude-*", "gpt-*"],
        params: {
          params: ["temperature", "top_p"],
        },
      },
    });
  });

  it("rejects invalid params json", () => {
    expect(
      buildTransformerModifierRecordFromDraft(
        {
          type: "override_params",
          enabled: true,
          modelsText: "",
          paramsJson: "[]",
          extraJson: "",
        },
        "response",
      ),
    ).toEqual({
      error: "`params JSON` 必须是 JSON 对象。",
      record: null,
    });
  });
});
