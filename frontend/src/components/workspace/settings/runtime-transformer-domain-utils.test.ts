import { describe, expect, it } from "vitest";

import {
  getRuntimeTransformerConfig,
  listRuntimeTransformerModifierSummaries,
} from "./runtime-transformer-domain-utils";

describe("runtime-transformer-domain-utils", () => {
  it("reads transformer summary for the dedicated form editor", () => {
    expect(
      getRuntimeTransformerConfig({
        transformer: {
          high_perf: true,
          http_transform_stage_enabled: false,
          cache_adapters: true,
          stream_null_filter: true,
        },
      }),
    ).toEqual({
      highPerf: true,
      httpTransformStageEnabled: false,
      cacheAdapters: true,
      streamNullFilter: true,
    });
  });

  it("lists request and response body modifiers", () => {
    const value = {
      transformer: {
        body_modifiers: {
          request: [
            {
              type: "disable_params",
              enabled: true,
              models: ["claude-*", "gpt-*"],
              params: {
                params: ["temperature", "top_p"],
              },
              owner: "platform",
            },
          ],
          response: [
            {
              type: "response_field_filter",
              enabled: false,
              models: ["openai-*"],
              params: {
                fields: ["id", "system_fingerprint"],
              },
            },
          ],
        },
      },
    };

    expect(listRuntimeTransformerModifierSummaries(value, "request")).toEqual([
      {
        id: "request:0:disable_params",
        scope: "request",
        index: 0,
        raw: expect.any(Object),
        type: "disable_params",
        enabled: true,
        models: ["claude-*", "gpt-*"],
        modelCount: 2,
        paramsKeyCount: 1,
        extraFieldCount: 1,
      },
    ]);

    expect(listRuntimeTransformerModifierSummaries(value, "response")).toEqual([
      {
        id: "response:0:response_field_filter",
        scope: "response",
        index: 0,
        raw: expect.any(Object),
        type: "response_field_filter",
        enabled: false,
        models: ["openai-*"],
        modelCount: 1,
        paramsKeyCount: 1,
        extraFieldCount: 0,
      },
    ]);
  });
});
