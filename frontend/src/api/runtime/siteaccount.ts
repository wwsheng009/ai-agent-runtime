import type {
  ProviderAccountCache,
  SiteAccountDetectRequest,
  SiteAccountDetectResult,
  SiteAccountFetchRequest,
  SiteAccountFetchResult,
  SiteAccountRefreshRequest,
  SiteAccountRefreshResult,
  SiteAccountView,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

const siteAccountDetectUrl = buildRuntimeUrl("/api/runtime/siteaccount/detect");
const siteAccountFetchUrl = buildRuntimeUrl("/api/runtime/siteaccount/fetch");

export async function detectRuntimeSiteAccount(
  request: SiteAccountDetectRequest,
): Promise<SiteAccountDetectResult> {
  return fetchRuntimeJson<SiteAccountDetectResult>(siteAccountDetectUrl, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(request),
  });
}

export async function fetchRuntimeSiteAccount(
  request: SiteAccountFetchRequest,
): Promise<SiteAccountFetchResult> {
  return fetchRuntimeJson<SiteAccountFetchResult>(siteAccountFetchUrl, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(request),
  });
}

export async function refreshRuntimeProviderAccount(
  providerName: string,
  request: SiteAccountRefreshRequest = {},
): Promise<SiteAccountRefreshResult> {
  const encodedName = encodeURIComponent(providerName.trim());
  return fetchRuntimeJson<SiteAccountRefreshResult>(
    buildRuntimeUrl(`/api/runtime/providers/${encodedName}/account/refresh`),
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

export function formatSiteAccountBalanceLine(
  view?: SiteAccountView | null,
  fallback = "",
): string {
  if (!view) {
    return fallback;
  }

  if (typeof view.balance_value === "number" && Number.isFinite(view.balance_value)) {
    const unit = firstNonEmpty(view.display_unit, view.currency, "USD");
    const mode = firstNonEmpty(view.mode, "unknown");
    const source = firstNonEmpty(view.source, "unknown");
    const label = firstNonEmpty(view.balance_label, "balance");
    return `${label} ${formatCompactNumber(view.balance_value)} ${unit} (${mode}, source=${source})`;
  }

  if (view.source) {
    const parts = [`source=${view.source}`];
    if (view.mode) {
      parts.push(`mode=${view.mode}`);
    }
    if (view.partial) {
      parts.push("partial");
    }
    return `account synced (${parts.join(", ")})`;
  }

  return fallback;
}

export function formatProviderAccountCacheLine(
  account?: ProviderAccountCache | null,
  fallback = "",
): string {
  if (!account) {
    return fallback;
  }

  const unit = firstNonEmpty(
    account.quota_display_unit,
    account.currency,
    "USD",
  );
  const value =
    pickFiniteNumber(account.quota_remaining) ??
    pickFiniteNumber(account.wallet_balance) ??
    pickFiniteNumber(account.quota_balance);
  if (value !== null) {
    const label =
      pickFiniteNumber(account.quota_remaining) !== null
        ? `remaining (${unit})`
        : pickFiniteNumber(account.wallet_balance) !== null
          ? `wallet (${unit})`
          : `quota (${unit})`;
    const mode = firstNonEmpty(account.mode, "unknown");
    const source = firstNonEmpty(account.source, "unknown");
    const fetched = account.fetched_at ? ` @ ${account.fetched_at}` : "";
    const user = firstNonEmpty(account.external_username_masked, account.external_user_id);
    const userPart = user ? `, user=${user}` : "";
    return `${label} ${formatCompactNumber(value)} ${unit} (${mode}, source=${source}${userPart})${fetched}`;
  }

  if (account.plan_name) {
    return `plan=${account.plan_name}${account.fetched_at ? ` @ ${account.fetched_at}` : ""}`;
  }

  if (account.last_error) {
    return `last error: ${account.last_error}`;
  }

  return fallback;
}

export function buildProviderAccountConfigPatch(
  result: SiteAccountRefreshResult,
): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  if (result.site_type) {
    patch.site_type = result.site_type;
  }
  if (result.site_type_confidence) {
    patch.site_type_confidence = result.site_type_confidence;
  }
  if (result.site_type_detected_at) {
    patch.site_type_detected_at = result.site_type_detected_at;
  }
  if (result.site_type_scores && Object.keys(result.site_type_scores).length > 0) {
    patch.site_type_scores = result.site_type_scores;
  }
  if (result.account_auth_ref) {
    patch.account_auth_ref = result.account_auth_ref;
  }
  if (result.account_cache) {
    patch.account = result.account_cache;
  }
  return patch;
}

function firstNonEmpty(...values: Array<string | undefined>) {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function pickFiniteNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function formatCompactNumber(value: number) {
  if (Number.isInteger(value)) {
    return String(value);
  }
  return value.toPrecision(4).replace(/\.?0+$/, "");
}
