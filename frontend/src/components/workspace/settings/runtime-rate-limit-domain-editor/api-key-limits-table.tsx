import { type TFunction } from "i18next";
import { ShieldEllipsisIcon, Trash2Icon } from "lucide-react";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import { type RuntimeRateLimitApiKeyLimitSummary } from "../runtime-rate-limit-domain-utils";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";

export function RuntimeRateLimitApiKeyLimitsTable({
  apiKeyLimits,
  onDeleteApiKeyLimit,
  openCreateApiDialog,
  openEditApiDialog,
  t,
}: {
  apiKeyLimits: RuntimeRateLimitApiKeyLimitSummary[];
  onDeleteApiKeyLimit: (index: number) => void;
  openCreateApiDialog: () => void;
  openEditApiDialog: (limit: RuntimeRateLimitApiKeyLimitSummary) => void;
  t: TFunction<"runtimeConfig">;
}) {
  return (
    <ConfigDomainTable
      title="API Key Limits"
      titleIcon={ShieldEllipsisIcon}
      description={t("editor.rateLimit.apiKey.tableDescription")}
      items={apiKeyLimits}
      getRowKey={(item) => item.id}
      emptyState={t("editor.rateLimit.apiKey.empty")}
      summary={
        <>
          <ConfigDomainSummaryBadge>
            {t("editor.rateLimit.summary.rules", {
              count: apiKeyLimits.length,
            })}
          </ConfigDomainSummaryBadge>
          <ConfigDomainSummaryBadge>
            {apiKeyLimits.length > 0
              ? t("editor.rateLimit.apiKey.firstPattern", {
                  pattern: apiKeyLimits[0]?.apiKeyPattern,
                })
              : t("editor.rateLimit.apiKey.notConfigured")}
          </ConfigDomainSummaryBadge>
        </>
      }
      actions={
        <SettingsAddButton
          size="sm"
          label={t("editor.rateLimit.apiKey.create")}
          onClick={openCreateApiDialog}
        />
      }
      columns={[
        {
          header: "Pattern",
          cell: (item) => (
            <div className="min-w-[12rem] font-semibold text-[var(--foreground)]">
              {item.apiKeyPattern}
            </div>
          ),
        },
        {
          header: t("editor.rateLimit.apiKey.columns.quota"),
          cell: (item) => (
            <div className="min-w-[12rem] text-sm">
              <div>{`qps ${item.qps || "--"} / qpm ${item.qpm || "--"}`}</div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                {`qpd ${item.qpd || "--"}`}
              </div>
            </div>
          ),
        },
        {
          header: t("editor.rateLimit.apiKey.columns.block"),
          cell: (item) => (
            <div className="min-w-[10rem]">
              <div>{item.blockDuration || "--"}</div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                {item.extraFieldCount > 0
                  ? t("editor.rateLimit.extraFields.count", {
                      count: item.extraFieldCount,
                    })
                  : t("editor.rateLimit.extraFields.none")}
              </div>
            </div>
          ),
        },
        {
          header: t("editor.rateLimit.columns.actions"),
          cell: (item) => (
            <SettingsActionGroup>
              <SettingsActionButton
                variant="secondary"
                label={t("editor.rateLimit.actions.edit")}
                onClick={() => openEditApiDialog(item)}
              />
              <SettingsActionButton
                variant="ghost"
                icon={<Trash2Icon size={14} />}
                label={t("editor.rateLimit.actions.delete")}
                onClick={() => onDeleteApiKeyLimit(item.index)}
              />
            </SettingsActionGroup>
          ),
          align: "right",
          className: "w-[12rem]",
        },
      ]}
    />
  );
}

