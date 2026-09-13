// 由 components/workspace/settings/runtime-provider-queue-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { GaugeIcon, Trash2Icon } from "lucide-react";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";
import {
  type RuntimeProviderQueueConfigSummary,
  type RuntimeProviderQueueProviderSummary,
} from "../runtime-provider-queue-domain-utils";

export function RuntimeProviderQueueProviderTable({
  config,
  onDeleteProvider,
  openCreateDialog,
  openEditDialog,
  providers,
  t,
}: {
  config: RuntimeProviderQueueConfigSummary;
  onDeleteProvider: (provider: string) => void;
  openCreateDialog: () => void;
  openEditDialog: (item: RuntimeProviderQueueProviderSummary) => void;
  providers: RuntimeProviderQueueProviderSummary[];
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <ConfigDomainTable
        title={t("editor.providerQueue.table.title")}
        titleIcon={GaugeIcon}
        description={t("editor.providerQueue.table.description")}
        items={providers}
        getRowKey={(item) => item.id}
        emptyState={t("editor.providerQueue.table.empty")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.providerQueue.table.overrideCount", {
                count: providers.length,
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {config.defaultMaxConcurrency
                ? t("editor.providerQueue.table.defaultMax", {
                    value: config.defaultMaxConcurrency,
                  })
                : t("editor.providerQueue.table.noDefaultMax")}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.providerQueue.actions.create")}
            onClick={openCreateDialog}
          />
        }
        columns={[
          {
            header: t("editor.providerQueue.table.columns.provider"),
            cell: (item) => (
              <div className="min-w-[12rem]">
                <div className="font-semibold">{item.provider}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {item.extraFieldCount > 0
                    ? t("editor.providerQueue.table.extraFields", {
                        count: item.extraFieldCount,
                      })
                    : t("editor.providerQueue.table.noExtraFields")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providerQueue.table.columns.slot"),
            cell: (item) => (
              <div className="min-w-[14rem]">
                <div>{`max ${item.maxConcurrency || "--"} · queue ${item.queueSize || "--"}`}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {item.queueTimeout ||
                    t("editor.providerQueue.table.noQueueTimeout")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providerQueue.table.columns.actions"),
            cell: (item) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.providerQueue.actions.edit")}
                  onClick={() => openEditDialog(item)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.providerQueue.actions.delete")}
                  onClick={() => onDeleteProvider(item.provider)}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[14rem]",
          },
        ]}
      />
  );
}
