import { describe, expect, it } from "vitest";

import {
  getRuntimeRetryConfig,
  listRuntimeRetryRules,
  normalizeRetryMatchList,
} from "./runtime-retry-domain-utils";

describe("runtime-retry-domain-utils", () => {
  it("reads retry summary for the dedicated form editor", () => {
    expect(
      getRuntimeRetryConfig({
        retry: {
          enabled: true,
          default_max_retries: 3,
          default_retry_delay_ms: 1000,
          default_backoff_multiplier: 2,
          invalid_encrypted_content_recovery: {
            strip_client_state_once: true,
          },
          enhanced_strategy: {
            enabled: true,
            secondary_threshold: 2,
            fallback_threshold: 4,
            primary_min_score: 40,
            secondary_excluded_score: 10,
          },
        },
      }),
    ).toEqual({
      enabled: true,
      defaultMaxRetries: "3",
      defaultRetryDelayMs: "1000",
      defaultBackoffMultiplier: "2",
      invalidEncryptedContentStripClientStateOnce: true,
      enhancedStrategyEnabled: true,
      enhancedStrategySecondaryThreshold: "2",
      enhancedStrategyFallbackThreshold: "4",
      enhancedStrategyPrimaryMinScore: "40",
      enhancedStrategySecondaryExcludedScore: "10",
    });
  });

  it("lists retry rules with matcher summaries", () => {
    expect(
      listRuntimeRetryRules({
        retry: {
          rules: [
            {
              name: "rate_limit_retry",
              description: "retry on 429",
              enabled: true,
              max_retries: 10,
              retry_delay_ms: 2000,
              backoff_multiplier: 2,
              error_code: {
                codes: ["1302", "slow_down"],
                pattern: "^13.*",
              },
              keyword: {
                values: ["rate limit", "限流"],
                patterns: ["(?i)too.?many.?requests"],
                case_sensitive: false,
              },
              status_code: {
                range: "429",
              },
              owner: "ops",
            },
          ],
        },
      }),
    ).toEqual([
      {
        id: "0:rate_limit_retry",
        index: 0,
        raw: expect.any(Object),
        name: "rate_limit_retry",
        description: "retry on 429",
        enabled: true,
        maxRetries: "10",
        retryDelayMs: "2000",
        backoffMultiplier: "2",
        errorCodeCodesText: "1302\nslow_down",
        errorCodePattern: "^13.*",
        keywordValuesText: "rate limit\n限流",
        keywordPatternsText: "(?i)too.?many.?requests",
        keywordCaseSensitive: false,
        statusCodeRange: "429",
        extraFieldCount: 1,
      },
    ]);
  });

  it("normalizes retry matcher list input", () => {
    expect(normalizeRetryMatchList("rate limit\n slow_down, 429")).toEqual([
      "rate limit",
      "slow_down",
      "429",
    ]);
  });
});
