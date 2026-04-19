import { describe, expect, it } from "vitest";

import {
  buildProviderGroupRecordFromDraft,
  type ProviderGroupDraftInput,
  validateProviderGroupDraft,
} from "./runtime-provider-groups-domain-form-utils";

function createDraft(
  overrides?: Partial<ProviderGroupDraftInput>,
): ProviderGroupDraftInput {
  return {
    name: "openai_group",
    strategy: "round_robin",
    maxRetries: "3",
    retryDelay: "1s",
    failoverEnabled: true,
    failoverMode: "primary_standby",
    failoverScope: "model_key",
    truncationEnabled: false,
    truncationMaxRetries: "6",
    truncationStrategy: "percentage",
    truncationStep: "20",
    members: [
      {
        name: "deepseek",
        role: "primary",
        weight: "100",
        enabled: true,
      },
      {
        name: "nvidia",
        role: "standby",
        weight: "80",
        enabled: false,
      },
    ],
    extraJson: JSON.stringify({
      health_check: {
        unhealthy_threshold: 3,
      },
    }),
    ...overrides,
  };
}

describe("runtime-provider-groups-domain-editor", () => {
  it("builds a provider group record from the modal draft", () => {
    const result = buildProviderGroupRecordFromDraft(createDraft());

    expect(result.error).toBeNull();
    expect(result.record).toMatchObject({
      name: "openai_group",
      strategy: "round_robin",
      max_retries: 3,
      retry_delay: "1s",
      health_check: {
        unhealthy_threshold: 3,
      },
      failover: {
        enabled: true,
        mode: "primary_standby",
        scope: "model_key",
      },
      truncation: {
        enabled: false,
        max_retries: 6,
        strategy: "percentage",
        step: 20,
      },
      providers: [
        {
          name: "deepseek",
          role: "primary",
          weight: 100,
          enabled: true,
        },
        {
          name: "nvidia",
          role: "standby",
          weight: 80,
          enabled: false,
        },
      ],
    });
  });

  it("rejects groups without valid members", () => {
    const result = buildProviderGroupRecordFromDraft(
      createDraft({
        members: [],
      }),
    );

    expect(result.record).toBeNull();
    expect(result.error).toContain("至少需要一个有效成员");
  });

  it("validates retry delay and member weight before save", () => {
    const issues = validateProviderGroupDraft(
      createDraft({
        retryDelay: "soon",
        members: [
          {
            name: "deepseek",
            role: "primary",
            weight: "0",
            enabled: true,
          },
        ],
      }),
    );

    expect(issues.map((issue) => issue.field)).toEqual([
      "retryDelay",
      "memberWeight",
    ]);
    expect(issues[0]?.message).toContain("retry_delay");
    expect(issues[1]?.message).toContain("weight");
  });

  it("rejects invalid numeric and duration fields when building records", () => {
    const result = buildProviderGroupRecordFromDraft(
      createDraft({
        maxRetries: "-1",
      }),
    );

    expect(result.record).toBeNull();
    expect(result.error).toContain("max_retries");
  });

  it("rejects duplicate providers inside the same group", () => {
    const issues = validateProviderGroupDraft(
      createDraft({
        members: [
          {
            name: "deepseek",
            role: "primary",
            weight: "100",
            enabled: true,
          },
          {
            name: "deepseek",
            role: "standby",
            weight: "80",
            enabled: true,
          },
        ],
      }),
    );

    expect(issues.filter((issue) => issue.field === "memberName")).toHaveLength(2);
    expect(issues[0]?.message).toContain("不能重复引用同一 provider");
  });

  it("requires member weight when strategy is weighted", () => {
    const issues = validateProviderGroupDraft(
      createDraft({
        strategy: "weighted",
        members: [
          {
            name: "deepseek",
            role: "primary",
            weight: "",
            enabled: true,
          },
        ],
      }),
    );

    expect(issues).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          field: "memberWeight",
        }),
      ]),
    );
    expect(issues[0]?.message).toContain("weighted");
  });
});
