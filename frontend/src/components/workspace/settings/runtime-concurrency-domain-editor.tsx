import { GaugeIcon, Trash2Icon } from "lucide-react";
import { useMemo, useState } from "react";

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
        title={<span className="text-base">Concurrency 配置</span>}
        icon={
          <SettingsPanelIcon>
            <GaugeIcon size={16} />
          </SettingsPanelIcon>
        }
        description="维护全局并发上限、队列参数，以及 provider 级别的并发限制。"
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>{config.enabled ? "并发控制开" : "并发控制关"}</Badge>
            <Badge>{`provider 限制 ${providerLimits.length} 条`}</Badge>
            <Badge>{`limit 汇总 ${totalProviderLimit}`}</Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)_minmax(0,1fr)]">
          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="text-sm font-semibold text-[var(--foreground)]">
              concurrency.enabled
            </div>
            <div className="mt-1 text-xs leading-5 text-[var(--muted-foreground)]">
              关闭后当前阈值仍会保留在配置里。
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>{config.enabled ? "已启用" : "已关闭"}</span>
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
        title="Per Provider Limits"
        titleIcon={GaugeIcon}
        description="按 provider 名称维护独立并发上限，常用于区分不同上游容量。"
        items={providerLimits}
        getRowKey={(item) => item.id}
        emptyState="当前没有 provider 级别的并发限制。"
        summary={
          <>
            <ConfigDomainSummaryBadge>{`${providerLimits.length} 条限制`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {config.maxConcurrentRequests
                ? `全局 ${config.maxConcurrentRequests}`
                : "未设全局阈值"}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton size="sm" label="新建 provider 限制" onClick={openCreateDialog} />
        }
        columns={[
          {
            header: "Provider",
            cell: (item) => <div className="font-semibold">{item.provider}</div>,
          },
          {
            header: "Limit",
            cell: (item) => <div>{item.limit || "--"}</div>,
          },
          {
            header: "操作",
            cell: (item) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="secondary"
                  label="编辑"
                  onClick={() => openEditDialog(item)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label="删除"
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
        title={editingProvider ? `编辑 ${editingProvider}` : "新建 provider 并发限制"}
        description="维护 per_provider_limits 里的单条 provider 并发阈值。"
        footer={
          <SettingsDialogFooter
            confirmLabel="保存草稿"
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
        widthClassName="max-w-2xl"
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning">
              {dialogError}
            </SettingsNoticeCard>
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
