import { type TFunction } from "i18next";
import { GaugeIcon, Trash2Icon } from "lucide-react";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import { type RuntimeRateLimitPathLimitSummary } from "../runtime-rate-limit-domain-utils";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";

export function RuntimeRateLimitPathLimitsTable({
  onDeletePathLimit,
  openCreatePathDialog,
  openEditPathDialog,
  pathLimits,
  t,
  totalPathBurst,
}: {
  onDeletePathLimit: (path: string) => void;
  openCreatePathDialog: () => void;
  openEditPathDialog: (limit: RuntimeRateLimitPathLimitSummary) => void;
  pathLimits: RuntimeRateLimitPathLimitSummary[];
  t: TFunction<"runtimeConfig">;
  totalPathBurst: number;
}) {
  return (
    <ConfigDomainTable
      title="Path Limits"
      titleIcon={GaugeIcon}
      description={t("editor.rateLimit.path.tableDescription")}
      items={pathLimits}
      getRowKey={(item) => item.path}
      emptyState={t("editor.rateLimit.path.empty")}
      summary={
        <>
          <ConfigDomainSummaryBadge>
            {t("editor.rateLimit.summary.pathRules", {
              count: pathLimits.length,
            })}
          </ConfigDomainSummaryBadge>
          <ConfigDomainSummaryBadge>
            {t("editor.rateLimit.summary.burstTotal", {
              total: String(totalPathBurst),
            })}
          </ConfigDomainSummaryBadge>
        </>
      }
      actions={
        <SettingsAddButton
          size="sm"
          label={t("editor.rateLimit.path.create")}
          onClick={openCreatePathDialog}
        />
      }
      columns={[
        {
          header: t("editor.rateLimit.path.columns.path"),
          cell: (item) => (
            <div className="min-w-[14rem] font-semibold text-foreground">
              {item.path}
            </div>
          ),
        },
        {
          header: "requests_per_minute",
          cell: (item) => <div>{item.requestsPerMinute || "--"}</div>,
        },
        {
          header: "burst",
          cell: (item) => (
            <div>
              <div>{item.burst || "--"}</div>
              <div className="mt-1 text-xs text-muted-foreground">
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
                onClick={() => openEditPathDialog(item)}
              />
              <SettingsActionButton
                variant="ghost"
                icon={<Trash2Icon size={14} />}
                label={t("editor.rateLimit.actions.delete")}
                onClick={() => onDeletePathLimit(item.path)}
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

