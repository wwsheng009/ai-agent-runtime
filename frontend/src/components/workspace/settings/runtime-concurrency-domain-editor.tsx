import { GaugeIcon, Trash2Icon } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import { ConfigDomainDialog } from "./config-domain-dialog";
import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "./config-domain-table";
import { ConfigFormField } from "./config-form-field";
import {
  editorControlClassName,
  editorToggleRowClassName,
} from "./editor-control-class";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "./settings-action-group";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsNoticeCard } from "./settings-notice-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { SettingsPanelCard } from "./settings-panel-card";
import {
  type RuntimeConcurrencyConfigSummary,
  type RuntimeConcurrencyProviderLimitSummary,
} from "./runtime-concurrency-domain-utils";

export type ConcurrencyProviderLimitDraftInput = {
  limit: string;
  provider: string;
};

type RuntimeConcurrencyDomainEditorProps = {
  config: RuntimeConcurrencyConfigSummary;
  onChange: (next: RuntimeConcurrencyConfigSummary) => void;
  onDeleteProviderLimit: (provider: string) => void;
  onSaveProviderLimit: (
    draft: ConcurrencyProviderLimitDraftInput,
    previousProvider: string | null,
  ) => string | null;
  providerLimits: RuntimeConcurrencyProviderLimitSummary[];
};

export function RuntimeConcurrencyDomainEditor({
  config,
  onChange,
  onDeleteProviderLimit,
  onSaveProviderLimit,
  providerLimits,
}: RuntimeConcurrencyDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const [draft, setDraft] = useState<ConcurrencyProviderLimitDraftInput>({
    provider: "",
    limit: "",
  });

  const totalProviderLimit = useMemo(
    () =>
      providerLimits.reduce((sum, item) => sum + (Number(item.limit) || 0), 0),
    [providerLimits],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingProvider(null);
    setDraft({ provider: "", limit: "" });
    setDialogOpen(true);
  }

  function openEditDialog(item: RuntimeConcurrencyProviderLimitSummary) {
    setDialogError(null);
    setEditingProvider(item.provider);
    setDraft({
      provider: item.provider,
      limit: item.limit,
    });
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveProviderLimit(draft, editingProvider);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.concurrency.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <GaugeIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.concurrency.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.concurrency.badges.enabledOn")
                : t("editor.concurrency.badges.enabledOff")}
            </Badge>
            <Badge>
              {t("editor.concurrency.badges.providerLimitCount", {
                count: providerLimits.length,
              })}
            </Badge>
            <Badge>
              {t("editor.concurrency.badges.limitTotal", {
                total: String(totalProviderLimit),
              })}
            </Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)_minmax(0,1fr)]">
          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="text-sm font-semibold text-[var(--foreground)]">
              concurrency.enabled
            </div>
            <div className="mt-1 text-xs leading-5 text-[var(--muted-foreground)]">
              {t("editor.concurrency.enabledHint")}
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>
                {config.enabled
                  ? t("editor.concurrency.enabled")
                  : t("editor.concurrency.disabled")}
              </span>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.enabled}
                onChange={(event) =>
                  onChange({ ...config, enabled: event.target.checked })
                }
              />
            </label>
          </div>

          <ConfigFormField label="max_concurrent_requests">
            <input
              className={editorControlClassName}
              value={config.maxConcurrentRequests}
              onChange={(event) =>
                onChange({
                  ...config,
                  maxConcurrentRequests: event.target.value,
                })
              }
              placeholder="200"
            />
          </ConfigFormField>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="queue_size">
              <input
                className={editorControlClassName}
                value={config.queueSize}
                onChange={(event) =>
                  onChange({
                    ...config,
                    queueSize: event.target.value,
                  })
                }
                placeholder="500"
              />
            </ConfigFormField>
            <ConfigFormField label="queue_timeout">
              <input
                className={editorControlClassName}
                value={config.queueTimeout}
                onChange={(event) =>
                  onChange({
                    ...config,
                    queueTimeout: event.target.value,
                  })
                }
                placeholder="5s"
              />
            </ConfigFormField>
          </div>
        </div>
      </SettingsPanelCard>

      <ConfigDomainTable
        title={t("editor.concurrency.table.title")}
        titleIcon={GaugeIcon}
        description={t("editor.concurrency.table.description")}
        items={providerLimits}
        getRowKey={(item) => item.id}
        emptyState={t("editor.concurrency.table.empty")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.concurrency.table.limitCount", {
                count: providerLimits.length,
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {config.maxConcurrentRequests
                ? t("editor.concurrency.table.globalLimit", {
                    value: config.maxConcurrentRequests,
                  })
                : t("editor.concurrency.table.noGlobalLimit")}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.concurrency.actions.create")}
            onClick={openCreateDialog}
          />
        }
        columns={[
          {
            header: t("editor.concurrency.table.columns.provider"),
            cell: (item) => <div className="font-semibold">{item.provider}</div>,
          },
          {
            header: t("editor.concurrency.table.columns.limit"),
            cell: (item) => <div>{item.limit || "--"}</div>,
          },
          {
            header: t("editor.concurrency.table.columns.actions"),
            cell: (item) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.concurrency.actions.edit")}
                  onClick={() => openEditDialog(item)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.concurrency.actions.delete")}
                  onClick={() => onDeleteProviderLimit(item.provider)}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[14rem]",
          },
        ]}
      />

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingProvider
            ? t("editor.concurrency.dialog.editTitle", { name: editingProvider })
            : t("editor.concurrency.dialog.createTitle")
        }
        description={t("editor.concurrency.dialog.description")}
        footer={
          <SettingsDialogFooter
            confirmLabel={t("editor.concurrency.dialog.save")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
        widthClassName="max-w-2xl"
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning">{dialogError}</SettingsNoticeCard>
          ) : null}
          <div className="grid gap-3 md:grid-cols-2">
            <ConfigFormField label="provider">
              <input
                className={editorControlClassName}
                value={draft.provider}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    provider: event.target.value,
                  }))
                }
                placeholder="nvidia"
              />
            </ConfigFormField>
            <ConfigFormField label="limit">
              <input
                className={editorControlClassName}
                value={draft.limit}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    limit: event.target.value,
                  }))
                }
                placeholder="100"
              />
            </ConfigFormField>
          </div>
        </div>
      </ConfigDomainDialog>
    </div>
  );
}
