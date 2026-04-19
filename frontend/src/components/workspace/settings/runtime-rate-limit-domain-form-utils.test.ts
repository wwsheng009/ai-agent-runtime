import { describe, expect, it } from "vitest";

import {
  buildRateLimitApiKeyRecordFromDraft,
  buildRateLimitPathRecordFromDraft,
  type RateLimitApiKeyDraftInput,
  type RateLimitPathDraftInput,
} from "./runtime-rate-limit-domain-form-utils";

function createApiKeyDraft(
  overrides?: Partial<RateLimitApiKeyDraftInput>,
): RateLimitApiKeyDraftInput {
  return {
    apiKeyPattern: "sk-",
    qps: "10",
    qpd: "100000",
    qpm: "600",
    blockDuration: "60s",
    extraJson: JSON.stringify({ custom: true }),
    ...overrides,
  };
}

function createPathDraft(
  overrides?: Partial<RateLimitPathDraftInput>,
): RateLimitPathDraftInput {
  return {
    path: "/v1/responses",
    requestsPerMinute: "60",
    burst: "15",
    extraJson: JSON.stringify({ custom: true }),
    ...overrides,
  };
}

describe("runtime-rate-limit-domain-form-utils", () => {
  it("builds an api key limit record", () => {
    const result = buildRateLimitApiKeyRecordFromDraft(createApiKeyDraft());

    expect(result.error).toBeNull();
    expect(result.record).toMatchObject({
      api_key_pattern: "sk-",
      qps: 10,
      qpd: 100000,
      qpm: 600,
      block_duration: "60s",
      custom: true,
    });
  });

  it("builds a path limit record", () => {
    const result = buildRateLimitPathRecordFromDraft(createPathDraft());

    expect(result.error).toBeNull();
    expect(result.path).toBe("/v1/responses");
    expect(result.record).toMatchObject({
      requests_per_minute: 60,
      burst: 15,
      custom: true,
    });
  });

  it("rejects empty api key pattern or path", () => {
    expect(
      buildRateLimitApiKeyRecordFromDraft(
        createApiKeyDraft({
          apiKeyPattern: "   ",
        }),
      ).error,
    ).toContain("api_key_pattern");

    expect(
      buildRateLimitPathRecordFromDraft(
        createPathDraft({
          path: "   ",
        }),
      ).error,
    ).toContain("路径");
  });
});
