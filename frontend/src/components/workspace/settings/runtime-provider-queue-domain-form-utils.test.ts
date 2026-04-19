import { describe, expect, it } from "vitest";

import { buildProviderQueueProviderRecordFromDraft } from "./runtime-provider-queue-domain-form-utils";

describe("runtime-provider-queue-domain-form-utils", () => {
  it("builds provider queue provider override record from draft input", () => {
    expect(
      buildProviderQueueProviderRecordFromDraft({
        provider: "nvidia",
        maxConcurrency: "5",
        queueSize: "20",
        queueTimeout: "60s",
        extraJson: '{\n  "note": "burst"\n}',
      }),
    ).toEqual({
      error: null,
      provider: "nvidia",
      record: {
        note: "burst",
        max_concurrency: 5,
        queue_size: 20,
        queue_timeout: "60s",
      },
    });
  });
});
