import { describe, expect, it } from "vitest";

import {
  buildProviderAccountConfigPatch,
  formatProviderAccountCacheLine,
  formatSiteAccountBalanceLine,
} from "./siteaccount";

describe("siteaccount api helpers", () => {
  it("formats account view balance lines", () => {
    expect(
      formatSiteAccountBalanceLine({
        balance_label: "remaining (USD)",
        balance_value: 12.34,
        display_unit: "USD",
        mode: "subscription",
        source: "sub2api",
      }),
    ).toContain("remaining (USD) 12.34 USD");

    expect(
      formatSiteAccountBalanceLine({
        source: "new-api",
        mode: "wallet",
        partial: true,
      }),
    ).toBe("account synced (source=new-api, mode=wallet, partial)");
  });

  it("formats cached provider account lines", () => {
    expect(
      formatProviderAccountCacheLine({
        quota_remaining: 8.5,
        quota_display_unit: "USD",
        mode: "quota",
        source: "new-api",
        external_user_id: "42",
        fetched_at: "2026-07-29T12:00:00Z",
      }),
    ).toBe(
      "remaining (USD) 8.5 USD (quota, source=new-api, user=42) @ 2026-07-29T12:00:00Z",
    );

    expect(
      formatProviderAccountCacheLine({
        plan_name: "Pro",
        fetched_at: "2026-07-29T12:00:00Z",
      }),
    ).toBe("plan=Pro @ 2026-07-29T12:00:00Z");
  });

  it("builds provider config patches from refresh results", () => {
    expect(
      buildProviderAccountConfigPatch({
        provider: "relay",
        site_type: "sub2api",
        site_type_confidence: "high",
        site_type_detected_at: "2026-07-29T12:00:00Z",
        site_type_scores: { sub2api: 9, "new-api": 1 },
        account_auth_ref: "providers.relay.account",
        account_cache: {
          quota_remaining: 3,
          source: "sub2api",
        },
        persisted: true,
      }),
    ).toEqual({
      site_type: "sub2api",
      site_type_confidence: "high",
      site_type_detected_at: "2026-07-29T12:00:00Z",
      site_type_scores: { sub2api: 9, "new-api": 1 },
      account_auth_ref: "providers.relay.account",
      account: {
        quota_remaining: 3,
        source: "sub2api",
      },
    });
  });
});
