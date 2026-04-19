import {
  ArrowDownIcon,
  ArrowUpIcon,
  Settings2Icon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";

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
        emptyState={`当前没有 ${scope === "request" ? "请求" : "响应"} Body 修改器。`}
        summary={
          <>
            <ConfigDomainSummaryBadge>{`${items.length} 条修改器`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {items.length > 0 ? `启用 ${items.filter((item) => item.enabled).length} 条` : "未配置"}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label="新建修改器"
            onClick={() => openCreateDialog(scope)}
          />
        }
        columns={[
          {
            header: "顺序 / 类型",
            cell: (item) => (
              <div className="min-w-[14rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge>{`#${item.index + 1}`}</Badge>
                  <div className="font-semibold text-[var(--foreground)]">
                    {item.type || "--"}
                  </div>
                  <Badge>{item.enabled ? "启用" : "关闭"}</Badge>
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {scope === "request" ? "上游请求前处理" : "客户端响应前处理"}
                </div>
              </div>
            ),
          },
          {
            header: "模型范围",
            cell: (item) => (
              <div className="min-w-[14rem]">
                <div>
                  {item.models.length > 0 ? item.models.slice(0, 2).join(", ") : "--"}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {item.models.length > 2
                    ? `共 ${item.models.length} 个模型匹配`
                    : item.models.length > 0
                      ? `${item.models.length} 个模型匹配`
                      : "未设 models"}
                </div>
              </div>
            ),
          },
          {
            header: "参数 / 扩展",
            cell: (item) => (
              <div className="min-w-[12rem]">
                <div>{`params ${item.paramsKeyCount} 个键`}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {item.extraFieldCount > 0
                    ? `${item.extraFieldCount} 个扩展字段`
                    : "无扩展字段"}
                </div>
              </div>
            ),
          },
          {
            header: "操作",
            cell: (item) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowUpIcon size={14} />}
                  label="上移"
                  onClick={() => onMoveModifier(scope, item.index, "up")}
                  disabled={item.index === 0}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowDownIcon size={14} />}
                  label="下移"
                  onClick={() => onMoveModifier(scope, item.index, "down")}
                  disabled={item.index === items.length - 1}
                />
                <SettingsActionButton
                  variant="secondary"
                  label="编辑"
                  onClick={() => openEditDialog(item)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label="删除"
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
        title={<span className="text-base">Transformer 配置</span>}
        icon={
          <SettingsPanelIcon>
            <Settings2Icon size={16} />
          </SettingsPanelIcon>
        }
        description="维护 HTTPTransformStage 相关开关，以及 request/response Body modifier 列表。"
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>{config.httpTransformStageEnabled ? "HTTP Stage 开" : "HTTP Stage 关"}</Badge>
            <Badge>{`${enabledModifierCount}/${totalModifierCount} 条修改器启用`}</Badge>
            <Badge>{config.highPerf ? "高性能开" : "高性能关"}</Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-4">
          <ToggleCard
            label="high_perf"
            description="控制是否启用高性能转换路径。"
            checked={config.highPerf}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, highPerf: checked })
            }
          />
          <ToggleCard
            label="http_transform_stage_enabled"
            description="控制是否启用完整 HTTPTransformer 体系。"
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
            description="控制适配器实例是否缓存。"
            checked={config.cacheAdapters}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, cacheAdapters: checked })
            }
          />
          <ToggleCard
            label="stream_null_filter"
            description="控制流式响应中的 null 字段是否过滤。"
            checked={config.streamNullFilter}
            onCheckedChange={(checked) =>
              onChangeConfig({ ...config, streamNullFilter: checked })
            }
          />
        </div>
      </SettingsPanelCard>

      {renderModifierTable(
        "request",
        "Request Body Modifiers",
        "在请求发往上游前修改 Body，适合禁用参数、覆写参数或角色转换。",
        requestModifiers,
      )}

      {renderModifierTable(
        "response",
        "Response Body Modifiers",
        "在响应返回客户端前修改 Body，适合字段过滤和响应裁剪。",
        responseModifiers,
      )}

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingIndex == null
            ? `新建${dialogScope === "request" ? "请求" : "响应"}修改器`
            : `编辑${dialogScope === "request" ? "请求" : "响应"}修改器 #${editingIndex + 1}`
        }
        description="维护修改器类型、模型范围、params JSON 和未覆盖的扩展字段。"
        footer={
          <SettingsDialogFooter
            confirmLabel="保存草稿"
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
              description="例如 disable_params、override_params、response_field_filter。"
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
              description="关闭后该修改器仍保留在配置里。"
              checked={draft.enabled}
              onCheckedChange={(checked) =>
                setDraft((current) => ({ ...current, enabled: checked }))
              }
            />
          </div>

          <ConfigFormField label="models" description="支持换行或逗号分隔。">
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
              description="写入 modifier.params，必须是 JSON 对象。"
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
              label="扩展字段 JSON"
              description="保留未被专用表单覆盖的字段，必须是 JSON 对象。"
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
