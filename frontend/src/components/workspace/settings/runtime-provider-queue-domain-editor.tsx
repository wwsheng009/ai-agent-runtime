import { GaugeIcon, TimerIcon, Trash2Icon } from "lucide-react";
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
  type ProviderQueueProviderDraftInput,
} from "./runtime-provider-queue-domain-form-utils";
import {
  editorControlClassName,
  editorSectionToggleClassName,
} from "./editor-control-class";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "./settings-action-group";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsMiniToggleCard } from "./settings-mini-toggle-card";
import { SettingsNoticeCard } from "./settings-notice-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsSubsectionCard } from "./settings-subsection-card";
import {
  type RuntimeProviderQueueConfigSummary,
  type RuntimeProviderQueueProviderSummary,
} from "./runtime-provider-queue-domain-utils";

type RuntimeProviderQueueDomainEditorProps = {
  config: RuntimeProviderQueueConfigSummary;
  onChangeConfig: (next: RuntimeProviderQueueConfigSummary) => void;
  onDeleteProvider: (provider: string) => void;
  onSaveProvider: (
    draft: ProviderQueueProviderDraftInput,
    previousProvider: string | null,
  ) => string | null;
  providers: RuntimeProviderQueueProviderSummary[];
};

export function RuntimeProviderQueueDomainEditor({
  config,
  onChangeConfig,
  onDeleteProvider,
  onSaveProvider,
  providers,
}: RuntimeProviderQueueDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProviderQueueProviderDraftInput>({
    provider: "",
    maxConcurrency: "",
    queueSize: "",
    queueTimeout: "",
    extraJson: "",
  });

  const totalMaxConcurrency = useMemo(
    () => providers.reduce((sum, item) => sum + (Number(item.maxConcurrency) || 0), 0),
    [providers],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingProvider(null);
    setDraft({
      provider: "",
      maxConcurrency: "",
      queueSize: "",
      queueTimeout: "",
      extraJson: "",
    });
    setDialogOpen(true);
  }

  function openEditDialog(item: RuntimeProviderQueueProviderSummary) {
    setDialogError(null);
    setEditingProvider(item.provider);
    setDraft({
      provider: item.provider,
      maxConcurrency: item.maxConcurrency,
      queueSize: item.queueSize,
      queueTimeout: item.queueTimeout,
      extraJson: stringifyExtraFields(item.raw),
    });
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveProvider(draft, editingProvider);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.providerQueue.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <GaugeIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.providerQueue.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.providerQueue.badges.enabledOn")
                : t("editor.providerQueue.badges.enabledOff")}
            </Badge>
            <Badge>
              {t("editor.providerQueue.badges.providerOverrides", {
                count: providers.length,
              })}
            </Badge>
            <Badge>
              {t("editor.providerQueue.badges.maxConcurrencyTotal", {
                total: String(totalMaxConcurrency),
              })}
            </Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)]">
          <SettingsMiniToggleCard
            checked={config.enabled}
            description={t("editor.providerQueue.enabledDescription")}
            label="provider_queue.enabled"
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, enabled: checked })
            }
          />

          <SettingsSubsectionCard title="default_slot">
            <div className="grid gap-3 xl:grid-cols-4">
              <ConfigFormField label="max_concurrency">
                <input
                  className={editorControlClassName}
                  value={config.defaultMaxConcurrency}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultMaxConcurrency: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="queue_size">
                <input
                  className={editorControlClassName}
                  value={config.defaultQueueSize}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultQueueSize: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="queue_timeout">
                <input
                  className={editorControlClassName}
                  value={config.defaultQueueTimeout}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultQueueTimeout: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="overflow_strategy">
                <input
                  className={editorControlClassName}
                  value={config.defaultOverflowStrategy}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultOverflowStrategy: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>
          </SettingsSubsectionCard>
        </div>
      </SettingsPanelCard>

      <div className="grid gap-3 xl:grid-cols-2">
        <SettingsPanelCard
          title="overflow"
          icon={
            <SettingsPanelIcon>
              <GaugeIcon size={16} />
            </SettingsPanelIcon>
          }
          description={t("editor.providerQueue.overflow.description")}
          descriptionClassName="mt-1 text-xs leading-5"
          headerAside={
            <label className={editorSectionToggleClassName}>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.overflowEnabled}
                onChange={(event) =>
                  onChangeConfig({ ...config, overflowEnabled: event.target.checked })
                }
              />
              {t("editor.providerQueue.overflow.enable")}
            </label>
          }
        >
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="overflow.max_attempts">
              <input
                className={editorControlClassName}
                value={config.overflowMaxAttempts}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    overflowMaxAttempts: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label="overflow.strategy">
              <input
                className={editorControlClassName}
                value={config.overflowStrategy}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    overflowStrategy: event.target.value,
                  })
                }
              />
            </ConfigFormField>
          </div>
        </SettingsPanelCard>

        <SettingsPanelCard
          title="wait_heartbeat"
          icon={
            <SettingsPanelIcon>
              <TimerIcon size={16} />
            </SettingsPanelIcon>
          }
          description={t("editor.providerQueue.waitHeartbeat.description")}
          descriptionClassName="mt-1 text-xs leading-5"
          headerAside={
            <label className={editorSectionToggleClassName}>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.waitHeartbeatEnabled}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatEnabled: event.target.checked,
                  })
                }
              />
              {t("editor.providerQueue.waitHeartbeat.enable")}
            </label>
          }
        >
          <div className="grid gap-3 xl:grid-cols-3">
            <ConfigFormField label="wait_heartbeat.interval">
              <input
                className={editorControlClassName}
                value={config.waitHeartbeatInterval}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatInterval: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label="wait_heartbeat.comment">
              <input
                className={editorControlClassName}
                value={config.waitHeartbeatComment}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatComment: event.target.value,
                  })
                }
              />
            </ConfigFormField>
            <ConfigFormField label="wait_heartbeat.max_wait_time">
              <input
                className={editorControlClassName}
                value={config.waitHeartbeatMaxWaitTime}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    waitHeartbeatMaxWaitTime: event.target.value,
                  })
                }
              />
            </ConfigFormField>
          </div>
        </SettingsPanelCard>
      </div>

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
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
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
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
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

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingProvider
            ? t("editor.providerQueue.dialog.editTitle", { name: editingProvider })
            : t("editor.providerQueue.dialog.createTitle")
        }
        description={t("editor.providerQueue.dialog.description")}
        footer={
          <SettingsDialogFooter
            confirmLabel={t("editor.providerQueue.dialog.save")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
        widthClassName="max-w-3xl"
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
                  setDraft((current) => ({ ...current, provider: event.target.value }))
                }
                placeholder="nvidia"
              />
            </ConfigFormField>
            <ConfigFormField label="max_concurrency">
              <input
                className={editorControlClassName}
                value={draft.maxConcurrency}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    maxConcurrency: event.target.value,
                  }))
                }
                placeholder="5"
              />
            </ConfigFormField>
            <ConfigFormField label="queue_size">
              <input
                className={editorControlClassName}
                value={draft.queueSize}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    queueSize: event.target.value,
                  }))
                }
                placeholder="20"
              />
            </ConfigFormField>
            <ConfigFormField label="queue_timeout">
              <input
                className={editorControlClassName}
                value={draft.queueTimeout}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    queueTimeout: event.target.value,
                  }))
                }
                placeholder="60s"
              />
            </ConfigFormField>
          </div>

          <ConfigFormField
            label={t("editor.providerQueue.fields.extraJson")}
            description={t("editor.providerQueue.fields.extraJsonDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              spellCheck={false}
              value={draft.extraJson}
              onChange={(event) =>
                setDraft((current) => ({ ...current, extraJson: event.target.value }))
              }
            />
          </ConfigFormField>
        </div>
      </ConfigDomainDialog>
    </div>
  );
}

function stringifyExtraFields(raw: Record<string, unknown>) {
  const extra = Object.fromEntries(
    Object.entries(raw).filter(
      ([key]) =>
        key !== "max_concurrency" && key !== "queue_size" && key !== "queue_timeout",
    ),
  );
  return Object.keys(extra).length > 0 ? JSON.stringify(extra, null, 2) : "";
}
