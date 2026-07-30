import {
  ArrowDownIcon,
  ArrowUpIcon,
  Settings2Icon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import { ConfigDomainDialog } from "./config-domain-dialog";
import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "./config-domain-table";
import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "./settings-action-group";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsNoticeCard } from "./settings-notice-card";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { SettingsMiniToggleCard } from "./settings-mini-toggle-card";
import {
  type TransformerModifierDraftInput,
} from "./runtime-transformer-domain-form-utils";
import { isConfigRecord } from "./runtime-provider-config-utils";
import {
  type RuntimeTransformerConfigSummary,
  type RuntimeTransformerModifierSummary,
  type TransformerModifierScope,
} from "./runtime-transformer-domain-utils";

const KNOWN_TRANSFORMER_MODIFIER_KEYS = new Set([
  "type",
  "enabled",
  "models",
  "params",
]);

type RuntimeTransformerDomainEditorProps = {
  config: RuntimeTransformerConfigSummary;
  onChangeConfig: (next: RuntimeTransformerConfigSummary) => void;
  onDeleteModifier: (scope: TransformerModifierScope, index: number) => void;
  onMoveModifier: (
    scope: TransformerModifierScope,
    index: number,
    direction: "up" | "down",
  ) => void;
  onSaveModifier: (
    scope: TransformerModifierScope,
    draft: TransformerModifierDraftInput,
    editingIndex: number | null,
  ) => string | null;
  requestModifiers: RuntimeTransformerModifierSummary[];
  responseModifiers: RuntimeTransformerModifierSummary[];
};

export function RuntimeTransformerDomainEditor({
  config,
  onChangeConfig,
  onDeleteModifier,
  onMoveModifier,
  onSaveModifier,
  requestModifiers,
  responseModifiers,
}: RuntimeTransformerDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [dialogScope, setDialogScope] =
    useState<TransformerModifierScope>("request");
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [draft, setDraft] = useState<TransformerModifierDraftInput>(() =>
    createTransformerModifierDraft(null),
  );

  const totalModifierCount = requestModifiers.length + responseModifiers.length;
  const enabledModifierCount = useMemo(
    () =>
      [...requestModifiers, ...responseModifiers].filter((item) => item.enabled)
        .length,
    [requestModifiers, responseModifiers],
  );

  function openCreateDialog(scope: TransformerModifierScope) {
    setDialogError(null);
    setDialogScope(scope);
    setEditingIndex(null);
    setDraft(createTransformerModifierDraft(null));
    setDialogOpen(true);
  }

  function openEditDialog(item: RuntimeTransformerModifierSummary) {
    setDialogError(null);
    setDialogScope(item.scope);
    setEditingIndex(item.index);
    setDraft(createTransformerModifierDraft(item));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveModifier(dialogScope, draft, editingIndex);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  function renderModifierTable(
    scope: TransformerModifierScope,
    title: string,
    description: string,
    items: RuntimeTransformerModifierSummary[],
  ) {
    return (
      <ConfigDomainTable
        title={title}
        titleIcon={Settings2Icon}
        description={description}
        items={items}
        getRowKey={(item) => item.id}
        emptyState={
          scope === "request"
            ? t("editor.transformer.modifiers.emptyRequest")
            : t("editor.transformer.modifiers.emptyResponse")
        }
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.transformer.modifiers.count", { count: items.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {items.length > 0
                ? t("editor.transformer.modifiers.enabledCount", {
                    count: items.filter((item) => item.enabled).length,
                  })
                : t("editor.transformer.modifiers.notConfigured")}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.transformer.modifiers.create")}
            onClick={() => openCreateDialog(scope)}
          />
        }
        columns={[
          {
            header: t("editor.transformer.columns.orderType"),
            cell: (item) => (
              <div className="min-w-[14rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge>{`#${item.index + 1}`}</Badge>
                  <div className="font-semibold text-[var(--foreground)]">
                    {item.type || "--"}
                  </div>
                  <Badge>
                    {item.enabled
                      ? t("editor.transformer.badges.enabled")
                      : t("editor.transformer.badges.disabled")}
                  </Badge>
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {scope === "request"
                    ? t("editor.transformer.scope.requestHint")
                    : t("editor.transformer.scope.responseHint")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.transformer.columns.models"),
            cell: (item) => (
              <div className="min-w-[14rem]">
                <div>
                  {item.models.length > 0 ? item.models.slice(0, 2).join(", ") : "--"}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {item.models.length > 2
                    ? t("editor.transformer.models.totalMatch", {
                        count: item.models.length,
                      })
                    : item.models.length > 0
                      ? t("editor.transformer.models.match", {
                          count: item.models.length,
                        })
                      : t("editor.transformer.models.none")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.transformer.columns.paramsExtra"),
            cell: (item) => (
              <div className="min-w-[12rem]">
                <div>
                  {t("editor.transformer.params.keyCount", {
                    count: item.paramsKeyCount,
                  })}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {item.extraFieldCount > 0
                    ? t("editor.transformer.extraFields.count", {
                        count: item.extraFieldCount,
                      })
                    : t("editor.transformer.extraFields.none")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.transformer.columns.actions"),
            cell: (item) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowUpIcon size={14} />}
                  label={t("editor.transformer.actions.moveUp")}
                  onClick={() => onMoveModifier(scope, item.index, "up")}
                  disabled={item.index === 0}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowDownIcon size={14} />}
                  label={t("editor.transformer.actions.moveDown")}
                  onClick={() => onMoveModifier(scope, item.index, "down")}
                  disabled={item.index === items.length - 1}
                />
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.transformer.actions.edit")}
                  onClick={() => openEditDialog(item)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.transformer.actions.delete")}
                  onClick={() => onDeleteModifier(scope, item.index)}
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

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.transformer.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <Settings2Icon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.transformer.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.httpTransformStageEnabled
                ? t("editor.transformer.status.httpStageOn")
                : t("editor.transformer.status.httpStageOff")}
            </Badge>
            <Badge>
              {t("editor.transformer.summary.enabledModifiers", {
                enabled: String(enabledModifierCount),
                total: String(totalModifierCount),
              })}
            </Badge>
            <Badge>
              {config.highPerf
                ? t("editor.transformer.status.highPerfOn")
                : t("editor.transformer.status.highPerfOff")}
            </Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-4">
          <ToggleCard
            label="high_perf"
            description={t("editor.transformer.toggles.highPerf")}
            checked={config.highPerf}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, highPerf: checked })
            }
          />
          <ToggleCard
            label="http_transform_stage_enabled"
            description={t("editor.transformer.toggles.httpTransformStage")}
            checked={config.httpTransformStageEnabled}
            onCheckedChange={(checked) =>
              onChangeConfig({
                ...config,
                httpTransformStageEnabled: checked,
              })
            }
          />
          <ToggleCard
            label="cache_adapters"
            description={t("editor.transformer.toggles.cacheAdapters")}
            checked={config.cacheAdapters}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, cacheAdapters: checked })
            }
          />
          <ToggleCard
            label="stream_null_filter"
            description={t("editor.transformer.toggles.streamNullFilter")}
            checked={config.streamNullFilter}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, streamNullFilter: checked })
            }
          />
        </div>
      </SettingsPanelCard>

      {renderModifierTable(
        "request",
        t("editor.transformer.requestTable.title"),
        t("editor.transformer.requestTable.description"),
        requestModifiers,
      )}

      {renderModifierTable(
        "response",
        t("editor.transformer.responseTable.title"),
        t("editor.transformer.responseTable.description"),
        responseModifiers,
      )}

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingIndex == null
            ? dialogScope === "request"
              ? t("editor.transformer.dialog.createRequestTitle")
              : t("editor.transformer.dialog.createResponseTitle")
            : dialogScope === "request"
              ? t("editor.transformer.dialog.editRequestTitle", {
                  index: String(editingIndex + 1),
                })
              : t("editor.transformer.dialog.editResponseTitle", {
                  index: String(editingIndex + 1),
                })
        }
        description={t("editor.transformer.dialog.description")}
        footer={
          <SettingsDialogFooter
            confirmLabel={t("editor.transformer.actions.saveDraft")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}
          <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_11rem]">
            <ConfigFormField
              label="type"
              description={t("editor.transformer.fields.typeHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.type}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, type: event.target.value }))
                }
                placeholder="disable_params"
              />
            </ConfigFormField>
            <ToggleCard
              label="enabled"
              description={t("editor.transformer.fields.enabledHelp")}
              checked={draft.enabled}
              onCheckedChange={(checked) =>
                setDraft((current) => ({ ...current, enabled: checked }))
              }
            />
          </div>

          <ConfigFormField
            label="models"
            description={t("editor.transformer.fields.modelsHelp")}
          >
            <textarea
              className={`${editorControlClassName} min-h-24 resize-y`}
              value={draft.modelsText}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  modelsText: event.target.value,
                }))
              }
              placeholder="gpt-*\nclaude-*"
            />
          </ConfigFormField>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label="params JSON"
              description={t("editor.transformer.fields.paramsJsonHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                spellCheck={false}
                value={draft.paramsJson}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    paramsJson: event.target.value,
                  }))
                }
                placeholder={'{\n  "params": ["temperature"]\n}'}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.transformer.fields.extraJson")}
              description={t("editor.transformer.fields.extraJsonHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                spellCheck={false}
                value={draft.extraJson}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    extraJson: event.target.value,
                  }))
                }
                placeholder={'{\n  "owner": "platform"\n}'}
              />
            </ConfigFormField>
          </div>
        </div>
      </ConfigDomainDialog>
    </div>
  );
}

function ToggleCard({
  checked,
  description,
  label,
  onCheckedChange,
}: {
  checked: boolean;
  description: string;
  label: string;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <SettingsMiniToggleCard
      checked={checked}
      description={description}
      label={label}
      onCheckedChange={onCheckedChange}
    />
  );
}

function createTransformerModifierDraft(
  item: RuntimeTransformerModifierSummary | null,
): TransformerModifierDraftInput {
  if (!item) {
    return {
      type: "",
      enabled: true,
      modelsText: "",
      paramsJson: "",
      extraJson: "",
    };
  }

  const params = isConfigRecord(item.raw.params) ? item.raw.params : {};
  const extraFields = Object.fromEntries(
    Object.entries(item.raw).filter(([key]) => !KNOWN_TRANSFORMER_MODIFIER_KEYS.has(key)),
  );

  return {
    type: item.type,
    enabled: item.enabled,
    modelsText: item.models.join("\n"),
    paramsJson: stringifyJsonObject(params),
    extraJson: stringifyJsonObject(extraFields),
  };
}

function stringifyJsonObject(value: Record<string, unknown>) {
  return Object.keys(value).length > 0 ? JSON.stringify(value, null, 2) : "";
}
