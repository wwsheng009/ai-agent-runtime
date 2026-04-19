import { describe, expect, it } from "vitest";

import {
  buildProviderGroupCreateConfigSnippet,
  createDefaultProviderGroup,
  getRuntimeAuthConfig,
  listRuntimeProviderGroupSummaries,
} from "./runtime-config-domain-utils";

describe("runtime-config-domain-utils", () => {
  it("lists provider group summaries from provider_groups", () => {
    const summaries = listRuntimeProviderGroupSummaries({
      provider_groups: [
        {
          name: "openai_group",
          strategy: "round_robin",
          max_retries: 3,
          retry_delay: "1s",
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
            { name: "nvidia", weight: 100, enabled: true },
            { name: "deepseek", role: "standby", weight: 80, enabled: false },
          ],
        },
      ],
    });

    expect(summaries).toHaveLength(1);
    expect(summaries[0]).toMatchObject({
      name: "openai_group",
      strategy: "round_robin",
      maxRetries: "3",
      retryDelay: "1s",
      failoverEnabled: true,
      failoverMode: "primary_standby",
      truncationEnabled: false,
      truncationMaxRetries: "6",
      truncationStrategy: "percentage",
      truncationStep: "20",
      providerCount: 2,
      providers: [
        { name: "nvidia", weight: "100", enabled: true, role: "" },
        { name: "deepseek", weight: "80", enabled: false, role: "standby" },
      ],
    });
  });

  it("reads auth summary for the dedicated auth editor", () => {
    expect(
      getRuntimeAuthConfig({
        auth: {
          jwt_secret: "${AUTH_JWT_SECRET:-secret}",
          access_key_secret: "${AUTH_ACCESS_KEY_SECRET:-access}",
          jwt_expire: "24h",
          session_timeout: "30m",
          max_api_create_times: 100,
          admin_auth_enabled: true,
          admin_token: "token",
          access_auth: {
            enabled: false,
            allow_anonymous: true,
          },
        },
      }),
    ).toEqual({
      jwtSecret: "${AUTH_JWT_SECRET:-secret}",
      accessKeySecret: "${AUTH_ACCESS_KEY_SECRET:-access}",
      jwtExpire: "24h",
      sessionTimeout: "30m",
      maxApiCreateTimes: "100",
      adminAuthEnabled: true,
      adminToken: "token",
      accessAuthEnabled: false,
      accessAuthAllowAnonymous: true,
    });
  });

  it("creates a default provider group config", () => {
    expect(createDefaultProviderGroup("codex_group")).toMatchObject({
      name: "codex_group",
      strategy: "round_robin",
      max_retries: 3,
      retry_delay: "1s",
      failover: {
        enabled: true,
        mode: "primary_standby",
        scope: "model_key",
      },
      providers: [],
    });
  });

  it("builds a copyable provider group snippet", () => {
    const summaries = listRuntimeProviderGroupSummaries({
      provider_groups: [
        {
          name: "openai_group",
          strategy: "round_robin",
          max_retries: 3,
          retry_delay: "1s",
          failover: {
            enabled: true,
            mode: "primary_standby",
          },
          providers: [
            {
              name: "nvidia",
              weight: 100,
              enabled: true,
            },
          ],
          health_check: {
            unhealthy_threshold: 3,
          },
        },
      ],
    });

    expect(buildProviderGroupCreateConfigSnippet(summaries[0])).toBe(
      [
        "-",
        "  name: \"openai_group\"",
        "  strategy: \"round_robin\"",
        "  max_retries: 3",
        "  retry_delay: \"1s\"",
        "  failover:",
        "    enabled: true",
        "    mode: \"primary_standby\"",
        "  providers:",
        "    -",
        "      name: \"nvidia\"",
        "      weight: 100",
        "      enabled: true",
        "  health_check:",
        "    unhealthy_threshold: 3",
      ].join("\n"),
    );
  });
});
