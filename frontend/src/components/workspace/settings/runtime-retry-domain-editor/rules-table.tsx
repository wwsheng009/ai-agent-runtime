// 由 components/workspace/settings/runtime-retry-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { ArrowDownIcon, ArrowUpIcon, RefreshCcwIcon, Trash2Icon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import {
  type RuntimeRetryConfigSummary,
  type RuntimeRetryRuleSummary,
} from "../runtime-retry-domain-utils";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";

import { summarizeMatcher } from "./draft-utils";

export function RuntimeRetryRulesTable({
  config,
  onDeleteRule,
  onMoveRule,
  openCreateDialog,
  openEditDialog,
  rules,
  t,
}: {
  config: RuntimeRetryConfigSummary;
  onDeleteRule: (index: number) => void;
  onMoveRule: (index: number, direction: "up" | "down") => void;
  openCreateDialog: () => void;
  openEditDialog: (rule: RuntimeRetryRuleSummary) => void;
  rules: RuntimeRetryRuleSummary[];
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <ConfigDomainTable
        title={t("editor.retry.rules.tableTitle")}
        titleIcon={RefreshCcwIcon}
        description={t("editor.retry.rules.tableDescription")}
        items={rules}
        getRowKey={(rule) => rule.id}
        emptyState={t("editor.retry.rules.empty")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.retry.summary.rules", { count: rules.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.retry.summary.defaultRetries", {
                value: config.defaultMaxRetries || "--",
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {config.enhancedStrategyEnabled ? "enhanced on" : "enhanced off"}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.retry.rules.create")}
            onClick={openCreateDialog}
          />
        }
        columns={[
          {
            header: t("editor.retry.rules.columns.orderName"),
            cell: (rule) => (
              <div className="min-w-[14rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge>{`#${rule.index + 1}`}</Badge>
                  <div className="font-semibold text-foreground">
                    {rule.name || "--"}
                  </div>
                  <Badge>
                    {rule.enabled
                      ? t("editor.retry.rules.enabled")
                      : t("editor.retry.rules.disabled")}
                  </Badge>
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {rule.description || t("editor.retry.rules.noDescription")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.retry.rules.columns.matchers"),
            cell: (rule) => (
              <div className="min-w-[15rem] text-xs leading-5 text-muted-foreground">
                {summarizeMatcher(rule, t).map((line) => (
                  <div key={line}>{line}</div>
                ))}
              </div>
            ),
          },
          {
            header: t("editor.retry.rules.columns.strategy"),
            cell: (rule) => (
              <div className="min-w-[12rem]">
                <div>{`max ${rule.maxRetries || "--"} · delay ${rule.retryDelayMs || "--"}ms`}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {`backoff ${rule.backoffMultiplier || "--"}`}
                  {rule.extraFieldCount > 0
                    ? ` · ${t("editor.retry.extraFields.count", {
                        count: rule.extraFieldCount,
                      })}`
                    : ""}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.retry.columns.actions"),
            cell: (rule) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowUpIcon size={14} />}
                  label={t("editor.retry.actions.moveUp")}
                  onClick={() => onMoveRule(rule.index, "up")}
                  disabled={rule.index === 0}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowDownIcon size={14} />}
                  label={t("editor.retry.actions.moveDown")}
                  onClick={() => onMoveRule(rule.index, "down")}
                  disabled={rule.index === rules.length - 1}
                />
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.retry.actions.edit")}
                  onClick={() => openEditDialog(rule)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.retry.actions.delete")}
                  onClick={() => onDeleteRule(rule.index)}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[22rem]",
          },
        ]}
      />
  );
}
