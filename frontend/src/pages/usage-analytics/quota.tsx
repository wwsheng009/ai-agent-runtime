// P2-1A：用量 / 配额面板（`/usage/stats|ledger|policy`）。
//
// 口径与边界：
// - 后端账本无货币价格字段，本面板只呈现 token / 请求计数，不推算任何费用；
// - 结构不满足（API 层抛 `invalid usage * payload`）或 403 / 503 均如实呈现，
//   不做「假装正常」的空态；
//   某一段失败不影响其余段（stats / policy / ledger 独立呈现）。

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import {
  isUsageQuotaAdminForbidden,
  isUsageQuotaLedgerDisabled,
  isUsageQuotaLedgerUnavailable,
  useUsageQuota,
  type UsageQuotaSectionError,
} from "@/hooks/use-usage-quota";
import { cn } from "@/lib/utils";
import type { UsageQuotaEntry, UsageScope } from "@/types/runtime";
import { RefreshCwIcon } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatNumber, formatTimestamp } from "./format";
import { PolicyBadge, QuotaBar, StatRow } from "./quota-atoms";
import { FilterInput, FilterSelect } from "./primitives";
import {
  clampLedgerLimit,
  defaultLedgerLimit,
  displayModel,
  levelKey,
  ledgerLimitOptions,
  metaText,
  resolvedFromKey,
  type AppliedLedgerFilters,
  type SectionErrorEntry,
} from "./quota-shared";

export function UsageQuotaPanel({ adminToken }: { adminToken: string }) {
  const { t } = useTranslation("usageAnalytics");
  const [selectedScope, setSelectedScope] = useState<UsageScope | null>(null);
  const [entrypointDraft, setEntrypointDraft] = useState("");
  const [skillDraft, setSkillDraft] = useState("");
  const [successDraft, setSuccessDraft] = useState("");
  const [limitDraft, setLimitDraft] = useState(String(defaultLedgerLimit));
  const [applied, setApplied] = useState<AppliedLedgerFilters>({
    entrypoint: "",
    skill: "",
    success: undefined,
    limit: defaultLedgerLimit,
  });

  // 选中项直接保存 scope 三元组（选择时一次解析），避免在渲染期从响应反查造成循环依赖。
  const scopeQuery = selectedScope
    ? {
        tenantId: selectedScope.tenant_id,
        projectId: selectedScope.project_id,
        userId: selectedScope.user_id,
      }
    : undefined;

  const { stats, policy, ledger, knownScopes, loading, statsError, policyError, ledgerError, refresh } =
    useUsageQuota({ adminToken, scope: scopeQuery, ledgerFilters: applied });

  // 作用域下拉的可选项来自最近一次全局聚合响应（hook 保留，切到具体作用域后仍可回选）。
  const scopeOptions: UsageScope[] = knownScopes;

  const scopeSelectOptions = useMemo(
    () => [
      { value: "", label: t("quota.scopeGlobal") },
      ...scopeOptions.map((item) => ({
        value: item.scope_key,
        label: item.scope_key,
      })),
    ],
    [scopeOptions, t],
  );

  const handleScopeChange = useCallback(
    (nextKey: string) => {
      if (!nextKey) {
        setSelectedScope(null);
        return;
      }
      setSelectedScope(scopeOptions.find((item) => item.scope_key === nextKey) ?? null);
    },
    [scopeOptions],
  );

  const applyFilters = useCallback(() => {
    setApplied({
      entrypoint: entrypointDraft.trim(),
      skill: skillDraft.trim(),
      success: successDraft === "" ? undefined : successDraft === "true",
      limit: clampLedgerLimit(limitDraft),
    });
  }, [entrypointDraft, limitDraft, skillDraft, successDraft]);

  const resetFilters = useCallback(() => {
    setEntrypointDraft("");
    setSkillDraft("");
    setSuccessDraft("");
    setLimitDraft(String(defaultLedgerLimit));
    setApplied({ entrypoint: "", skill: "", success: undefined, limit: defaultLedgerLimit });
  }, []);

  const filtersDirty =
    entrypointDraft.trim() !== applied.entrypoint ||
    skillDraft.trim() !== applied.skill ||
    (successDraft === "" ? undefined : successDraft === "true") !== applied.success ||
    clampLedgerLimit(limitDraft) !== applied.limit;

  const policyEntries = useMemo<UsageQuotaEntry[]>(
    () => (policy ? [...policy.tenants, ...policy.projects, ...policy.users] : []),
    [policy],
  );

  const summary = stats?.policy ?? null;
  const usage = stats?.usage ?? null;
  const quota = stats?.quota ?? null;
  const ledgerUnavailable = isUsageQuotaLedgerUnavailable(ledgerError);
  // 账本 503 分两种：配置未启用（not configured）与「已启用但初始化失败」（后端降级并回传原因）。
  // 后者不能提示成「未配置」，否则会被误判为开关没开，把 dsn / 驱动 / 目录问题带偏。
  const ledgerErrorLabel = !ledgerUnavailable
    ? t("quota.errors.ledger")
    : isUsageQuotaLedgerDisabled(ledgerError)
      ? t("quota.errors.ledgerUnavailable")
      : t("quota.errors.ledgerBroken");
  const sectionErrorCandidates: SectionErrorEntry[] = [
    { key: "stats", label: t("quota.errors.stats"), error: statsError },
    { key: "policy", label: t("quota.errors.policy"), error: policyError },
    {
      key: "ledger",
      label: ledgerErrorLabel,
      error: ledgerError,
    },
  ];
  const sectionErrors = sectionErrorCandidates.filter(
    (item): item is SectionErrorEntry & { error: UsageQuotaSectionError } =>
      item.error !== null && !isUsageQuotaAdminForbidden(item.error),
  );

  return (
    <section aria-label={t("quota.title")} className="surface-panel rounded-panel-lg p-3 sm:p-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">{t("quota.title")}</h2>
          <p className="mt-0.5 max-w-3xl text-xs leading-5 text-muted-foreground">
            {t("quota.description")}
          </p>
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <label className="min-w-[12rem] flex-1 sm:flex-none">
            <span className="mb-1 block text-xs text-muted-foreground">{t("quota.scopeLabel")}</span>
            <Select
              ariaLabel={t("quota.scopeLabel")}
              value={selectedScope?.scope_key ?? ""}
              options={scopeSelectOptions}
              onChange={handleScopeChange}
              triggerClassName="h-9 rounded-field"
            />
          </label>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={refresh}
            disabled={loading}
            aria-label={t("quota.refresh")}
            title={t("quota.refresh")}
          >
            <RefreshCwIcon size={14} className={cn(loading && "animate-spin")} />
            <span className="hidden sm:inline">{t("quota.refresh")}</span>
          </Button>
        </div>
      </div>

      {!adminToken.trim() || isUsageQuotaAdminForbidden(statsError) ? (
        <p
          role="status"
          className="mt-3 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs leading-5 text-analytics-warning"
        >
          {t("quota.adminTokenRequired")}
        </p>
      ) : null}

      {sectionErrors.length > 0 ? (
        <ul role="alert" className="mt-3 space-y-1.5">
          {sectionErrors.map((item) => (
            <li
              key={item.key}
              className="rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2 text-xs leading-5 text-analytics-danger"
            >
              {item.label}: {item.error.message}
            </li>
          ))}
        </ul>
      ) : null}

      <div className="mt-3 grid gap-2 xl:grid-cols-3">
        <article className="rounded-panel border border-border bg-surface-softer p-3">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t("quota.policy.title")}
            </h3>
            {loading ? (
              <span className="text-xs text-muted-foreground">{t("quota.loading")}</span>
            ) : null}
          </div>

          {summary ? (
            <>
              <div className="mt-2 flex flex-wrap gap-1.5">
                <PolicyBadge label={t("quota.policy.tracking")} enabled={summary.tracking_enabled} />
                <PolicyBadge label={t("quota.policy.ledger")} enabled={summary.ledger_enabled} />
                <PolicyBadge label={t("quota.policy.quota")} enabled={summary.quota_enabled} />
              </div>
              <dl className="mt-2 grid grid-cols-1 gap-1 text-xs">
                <StatRow
                  label={t("quota.policy.defaults")}
                  value={`${t("quota.policy.defaultRequests", { value: formatNumber(summary.default_max_requests) })} / ${t("quota.policy.defaultTokens", { value: formatNumber(summary.default_max_tokens) })}`}
                />
                <StatRow
                  label={t("quota.policy.configured")}
                  value={t("quota.policy.configuredCount", {
                    tenant: formatNumber(summary.tenant_quota_count),
                    project: formatNumber(summary.project_quota_count),
                    user: formatNumber(summary.user_quota_count),
                  })}
                />
              </dl>
            </>
          ) : (
            <p className="mt-2 text-xs text-muted-foreground">{t("quota.loading")}</p>
          )}

          {policyEntries.length > 0 ? (
            <div className="mt-2 max-h-56 overflow-auto rounded-panel border border-border">
              <table className="w-full text-xs">
                <thead className="sticky top-0 bg-surface-softer text-left text-muted-foreground">
                  <tr>
                    <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.policy.columns.level")}</th>
                    <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.policy.columns.key")}</th>
                    <th scope="col" className="px-2 py-1.5 text-right font-medium">{t("quota.policy.columns.requests")}</th>
                    <th scope="col" className="px-2 py-1.5 text-right font-medium">{t("quota.policy.columns.tokens")}</th>
                  </tr>
                </thead>
                <tbody>
                  {policyEntries.map((entry) => (
                    <tr key={`${entry.level}:${entry.key}`} className="border-t border-border">
                      <td className="px-2 py-1.5">{t(levelKey(entry.level))}</td>
                      <td className="max-w-[16rem] truncate px-2 py-1.5" title={entry.key}>{entry.key}</td>
                      <td className="px-2 py-1.5 text-right tabular-nums">
                        {entry.max_requests === null ? t("quota.policy.unlimited") : formatNumber(entry.max_requests)}
                      </td>
                      <td className="px-2 py-1.5 text-right tabular-nums">
                        {entry.max_tokens === null ? t("quota.policy.unlimited") : formatNumber(entry.max_tokens)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : policy && policyEntries.length === 0 ? (
            <p className="mt-2 text-xs text-muted-foreground">{t("quota.policy.entriesEmpty")}</p>
          ) : null}
        </article>

        <article className="rounded-panel border border-border bg-surface-softer p-3">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {t("quota.usage.title")}
          </h3>
          {usage ? (
            <>
              <dl className="mt-2 grid grid-cols-1 gap-1 text-xs">
                <StatRow
                  label={t("quota.usage.scope")}
                  value={stats?.scope?.scope_key ?? t("quota.scopeGlobal")}
                />
                <StatRow label={t("quota.usage.requests")} value={formatNumber(usage.request_count)} />
                <StatRow label={t("quota.usage.total")} value={formatNumber(usage.total_tokens)} />
                <StatRow
                  label={t("quota.usage.tokens")}
                  value={t("quota.usage.tokenBreakdown", {
                    prompt: formatNumber(usage.prompt_tokens),
                    completion: formatNumber(usage.completion_tokens),
                  })}
                />
                <StatRow label={t("quota.usage.success")} value={formatNumber(usage.success_count)} />
                <StatRow label={t("quota.usage.failure")} value={formatNumber(usage.failure_count)} />
                <StatRow label={t("quota.usage.execute")} value={formatNumber(usage.execute_count)} />
                <StatRow label={t("quota.usage.agentChat")} value={formatNumber(usage.agent_chat_count)} />
                {stats?.scope ? (
                  <>
                    <StatRow label={t("quota.usage.lastRequest")} value={formatTimestamp(usage.last_request_at)} />
                    <StatRow label={t("quota.usage.lastEntrypoint")} value={usage.last_entrypoint || "-"} />
                    <StatRow label={t("quota.usage.lastSkill")} value={usage.last_skill || "-"} />
                  </>
                ) : null}
              </dl>
              {stats && !stats.scope ? (
                <p className="mt-2 text-xs text-muted-foreground">
                  {t("quota.usage.scopes", { value: formatNumber(usage.scope_count) })} ·{" "}
                  {t("quota.usage.users", { value: formatNumber(usage.user_count) })}
                </p>
              ) : null}
            </>
          ) : (
            <p className="mt-2 text-xs text-muted-foreground">{t("quota.loading")}</p>
          )}
        </article>

        <article className="rounded-panel border border-border bg-surface-softer p-3">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {t("quota.limit.title")}
          </h3>
          {stats?.scope && quota ? (
            quota.enabled ? (
              <div className="mt-3 space-y-3">
                <QuotaBar
                  label={t("quota.limit.requests")}
                  remaining={quota.remaining_requests}
                  limit={quota.max_requests}
                  unlimitedLabel={t("quota.limit.unlimited")}
                />
                <QuotaBar
                  label={t("quota.limit.tokens")}
                  remaining={quota.remaining_tokens}
                  limit={quota.max_tokens}
                  unlimitedLabel={t("quota.limit.unlimited")}
                />
                <dl className="grid grid-cols-1 gap-1 text-xs">
                  <StatRow
                    label={t("quota.limit.resolvedFrom")}
                    value={t(resolvedFromKey(quota.resolved_from))}
                  />
                </dl>
              </div>
            ) : (
              <p className="mt-2 text-xs text-muted-foreground">{t("quota.limit.disabled")}</p>
            )
          ) : stats ? (
            <p className="mt-2 text-xs text-muted-foreground">
              {stats.scope ? t("quota.limit.unavailable") : t("quota.limit.selectScope")}
            </p>
          ) : (
            <p className="mt-2 text-xs text-muted-foreground">{t("quota.loading")}</p>
          )}
        </article>
      </div>

      <article className="mt-2 rounded-panel border border-border bg-surface-softer p-3">
        <div className="flex flex-col gap-2 xl:flex-row xl:items-end xl:justify-between">
          <div className="min-w-0">
            <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t("quota.ledger.title")}
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {ledger
                ? t("quota.ledger.subtitle", {
                    value: formatNumber(ledger.count),
                    limit: formatNumber(ledger.limit),
                  })
                : t("quota.loading")}
            </p>
          </div>
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
            <FilterInput
              label={t("quota.ledger.entrypointLabel")}
              value={entrypointDraft}
              placeholder={t("quota.ledger.entrypointPlaceholder")}
              onChange={setEntrypointDraft}
            />
            <FilterInput
              label={t("quota.ledger.skillLabel")}
              value={skillDraft}
              placeholder={t("quota.ledger.skillPlaceholder")}
              onChange={setSkillDraft}
            />
            <FilterSelect
              label={t("quota.ledger.successLabel")}
              value={successDraft}
              options={[
                { value: "", label: t("quota.ledger.successAll") },
                { value: "true", label: t("quota.ledger.successOnly") },
                { value: "false", label: t("quota.ledger.failureOnly") },
              ]}
              onChange={setSuccessDraft}
            />
            <FilterSelect
              label={t("quota.ledger.limitLabel")}
              value={limitDraft}
              options={ledgerLimitOptions}
              onChange={setLimitDraft}
            />
            <div className="flex items-end gap-2 sm:col-span-2 xl:col-span-4">
              <Button type="button" size="sm" onClick={applyFilters} disabled={!filtersDirty}>
                {t("quota.ledger.apply")}
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={resetFilters}>
                {t("quota.ledger.reset")}
              </Button>
            </div>
          </div>
        </div>

        {ledgerError !== null || !ledger ? (
          <p className="mt-2 text-xs text-muted-foreground">
            {ledgerError !== null ? t("quota.ledger.unavailable") : t("quota.loading")}
          </p>
        ) : ledger.records.length > 0 ? (
          <div className="mt-2 overflow-x-auto rounded-panel border border-border">
            <table className="w-full text-xs">
              <thead className="bg-surface-softer text-left text-muted-foreground">
                <tr>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.ledger.columns.time")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.ledger.columns.entrypoint")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.ledger.columns.skill")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.ledger.columns.tokens")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.ledger.columns.model")}</th>
                  <th scope="col" className="px-2 py-1.5 font-medium">{t("quota.ledger.columns.outcome")}</th>
                </tr>
              </thead>
              <tbody>
                {ledger.records.map((record) => (
                  <tr key={record.id} className="border-t border-border">
                    <td className="whitespace-nowrap px-2 py-1.5">{formatTimestamp(record.created_at)}</td>
                    <td className="px-2 py-1.5">{metaText(record, "entrypoint") || "-"}</td>
                    <td className="max-w-[14rem] truncate px-2 py-1.5" title={metaText(record, "skill")}>
                      {metaText(record, "skill") || "-"}
                    </td>
                    <td className="whitespace-nowrap px-2 py-1.5 tabular-nums">
                      {formatNumber(record.input_tokens)} / {formatNumber(record.output_tokens)} /{" "}
                      {formatNumber(record.total_tokens)}
                    </td>
                    <td className="px-2 py-1.5">{displayModel(record)}</td>
                    <td className="px-2 py-1.5">
                      <Badge
                        className={cn(
                          record.success
                            ? "border-analytics-success-border bg-analytics-success-soft text-analytics-success"
                            : "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger",
                        )}
                      >
                        {record.success ? t("quota.ledger.rowSuccess") : t("quota.ledger.rowFailure")}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="mt-2 text-xs text-muted-foreground">{t("quota.ledger.empty")}</p>
        )}
      </article>
    </section>
  );
}
