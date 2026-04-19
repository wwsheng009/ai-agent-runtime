import { describe, expect, it } from "vitest";

import {
  getRuntimeProviderQueueConfig,
  listRuntimeProviderQueueProviders,
} from "./runtime-provider-queue-domain-utils";

describe("runtime-provider-queue-domain-utils", () => {
  it("reads provider queue summary for the dedicated form editor", () => {
    expect(
      getRuntimeProviderQueueConfig({
        provider_queue: {
          enabled: true,
          default_slot: {
            max_concurrency: 3,
            queue_size: 10,
            queue_timeout: "30s",
            overflow_strategy: "overflow",
          },
          overflow: {
            enabled: true,
            max_attempts: 3,
            strategy: "prefer_empty",
          },
          wait_heartbeat: {
            enabled: true,
            interval: "5s",
            comment: ": queue heartbeat",
            max_wait_time: "120s",
          },
        },
      }),
    ).toEqual({
      enabled: true,
      defaultMaxConcurrency: "3",
      defaultQueueSize: "10",
      defaultQueueTimeout: "30s",
      defaultOverflowStrategy: "overflow",
      overflowEnabled: true,
      overflowMaxAttempts: "3",
      overflowStrategy: "prefer_empty",
      waitHeartbeatEnabled: true,
      waitHeartbeatInterval: "5s",
      waitHeartbeatComment: ": queue heartbeat",
      waitHeartbeatMaxWaitTime: "120s",
    });
  });

  it("lists provider queue provider overrides", () => {
    expect(
      listRuntimeProviderQueueProviders({
        provider_queue: {
          providers: {
            nvidia: {
              max_concurrency: 5,
              queue_size: 20,
              queue_timeout: "60s",
            },
            deepseek: {
              max_concurrency: 5,
              queue_size: 15,
              queue_timeout: "30s",
              note: "burst",
            },
          },
        },
      }),
    ).toEqual([
      {
        id: "deepseek",
        provider: "deepseek",
        raw: expect.any(Object),
        maxConcurrency: "5",
        queueSize: "15",
        queueTimeout: "30s",
        extraFieldCount: 1,
      },
      {
        id: "nvidia",
        provider: "nvidia",
        raw: expect.any(Object),
        maxConcurrency: "5",
        queueSize: "20",
        queueTimeout: "60s",
        extraFieldCount: 0,
      },
    ]);
  });
});
