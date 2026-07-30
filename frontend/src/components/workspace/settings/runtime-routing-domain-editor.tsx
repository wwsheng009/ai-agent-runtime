import {
  ArrowDownIcon,
  ArrowUpIcon,
  GitBranchPlusIcon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";

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
import {
  createDefaultRuntimeRoute,
  type RuntimeRouteSummary,
  type RuntimeRoutingConfigSummary,
} from "./runtime-routing-domain-utils";
import { type RouteDraftInput } from "./runtime-routing-domain-form-utils";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsNoticeCard } from "./settings-notice-card";
import { SettingsPanelIcon } from "./settings-panel-icon";

const KNOWN_ROUTE_KEYS = new Set([
  "match_path",
  "match_type",
  "group",
  "pipeline",
  "protocol",
  "priority",
  "match_models",
  "match_model_regexes",
  "exclude_models",
  "exclude_model_regexes",
]);

const routeMatchTypeOptions = [
  { value: "prefix", label: "prefix" },
  { value: "exact", label: "exact" },
  { value: "regex", label: "regex" },
] as const;

type RuntimeRoutingDomainEditorProps = {
  availableGroups: string[];
  onChangeConfig: (next: RuntimeRoutingConfigSummary) => void;
  onDeleteRoute: (index: number) => void;
  onMoveRoute: (index: number, direction: "up" | "down") => void;
  onSaveRoute: (
    draft: RouteDraftInput,
    editingIndex: number | null,
  ) => string | null;
  routeConfig: RuntimeRoutingConfigSummary;
  routes: RuntimeRouteSummary[];
};

export function RuntimeRoutingDomainEditor({
  availableGroups,
  onChangeConfig,
  onDeleteRoute,
  onMoveRoute,
  onSaveRoute,
  routeConfig,
  routes,
}: RuntimeRoutingDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [draft, setDraft] = useState<RouteDraftInput>(() =>
    createRouteDraftInput(null),
  );

  const protocolCount = useMemo(
    () =>
      new Set(routes.map((route) => route.protocol).filter(Boolean)).size,
    [routes],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingIndex(null);
    setDraft(createRouteDraftInput(null));
    setDialogOpen(true);
  }

  function openEditDialog(route: RuntimeRouteSummary) {
    setDialogError(null);
    setEditingIndex(route.index);
    setDraft(createRouteDraftInput(route));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveRoute(draft, editingIndex);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <SettingsPanelIcon>
              <GitBranchPlusIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-[var(--foreground)]">
                {t("editor.routing.title")}
              </div>
              <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                {t("editor.routing.description")}
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <ConfigDomainSummaryBadge>
              {t("editor.routing.summary.routes", { count: routes.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.routing.summary.protocols", { count: protocolCount })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {routeConfig.failover
                ? t("editor.routing.status.failoverOn")
                : t("editor.routing.status.failoverOff")}
            </ConfigDomainSummaryBadge>
          </SettingsBadgeList>
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-[minmax(0,1fr)_13rem]">
          <ConfigFormField
            label="routing.strategy"
            description={t("editor.routing.strategyHelp")}
          >
            <input
              className={editorControlClassName}
              value={routeConfig.strategy}
              onChange={(event) =>
                onChangeConfig({
                  ...routeConfig,
                  strategy: event.target.value,
                })
              }
              placeholder="health"
            />
          </ConfigFormField>

          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="text-[13px] font-semibold text-[var(--foreground)]">routing.failover</div>
            <div className="mt-1 text-xs leading-5 text-[var(--muted-foreground)]">
              {t("editor.routing.failoverHelp")}
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>
                {routeConfig.failover
                  ? t("editor.routing.toggle.enabled")
                  : t("editor.routing.toggle.disabled")}
              </span>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={routeConfig.failover}
                onChange={(event) =>
                  onChangeConfig({
                    ...routeConfig,
                    failover: event.target.checked,
                  })
                }
              />
            </label>
          </div>
        </div>
      </div>

      <ConfigDomainTable
        title="Routes"
        titleIcon={GitBranchPlusIcon}
        description={t("editor.routing.routes.tableDescription")}
        items={routes}
        getRowKey={(route) => route.id}
        emptyState={t("editor.routing.routes.empty")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.routing.summary.rules", { count: routes.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.routing.summary.availableGroups", {
                count: availableGroups.length,
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>{`strategy ${routeConfig.strategy || "--"}`}</ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.routing.routes.create")}
            onClick={openCreateDialog}
          />
        }
        columns={[
          {
            header: t("editor.routing.routes.columns.orderMatch"),
            cell: (route) => (
              <div className="min-w-[14rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge>{`#${route.index + 1}`}</Badge>
                  <div className="font-semibold text-[var(--foreground)]">
                    {route.matchPath || "--"}
                  </div>
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {route.matchType || "prefix"}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.routing.routes.columns.targetGroup"),
            cell: (route) => (
              <div className="min-w-[12rem]">
                <div>{route.group || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {route.pipeline
                    ? `pipeline ${route.pipeline}`
                    : route.protocol || t("editor.routing.routes.noProtocol")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.routing.routes.columns.modelConditions"),
            cell: (route) => (
              <div className="min-w-[14rem]">
                <div>{route.matchModels.length > 0 ? route.matchModels.join(", ") : "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {route.excludeModels.length > 0
                    ? `exclude ${route.excludeModels.join(", ")}`
                    : t("editor.routing.routes.noExclusions")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.routing.routes.columns.priorityExtra"),
            cell: (route) => (
              <div className="min-w-[12rem]">
                <div>{route.priority ? `priority ${route.priority}` : "--"}</div>
                <div className="mt-1 flex flex-wrap gap-2 text-xs text-[var(--muted-foreground)]">
                  {route.pipeline ? <span>{`pipeline ${route.pipeline}`}</span> : null}
                  {route.extraFieldCount > 0 ? (
                    <span>
                      {t("editor.routing.extraFields.count", {
                        count: route.extraFieldCount,
                      })}
                    </span>
                  ) : (
                    <span>{t("editor.routing.extraFields.none")}</span>
                  )}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.routing.columns.actions"),
            cell: (route) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowUpIcon size={14} />}
                  label={t("editor.routing.actions.moveUp")}
                  onClick={() => onMoveRoute(route.index, "up")}
                  disabled={route.index === 0}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowDownIcon size={14} />}
                  label={t("editor.routing.actions.moveDown")}
                  onClick={() => onMoveRoute(route.index, "down")}
                  disabled={route.index === routes.length - 1}
                />
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.routing.actions.edit")}
                  onClick={() => openEditDialog(route)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.routing.actions.delete")}
                  onClick={() => onDeleteRoute(route.index)}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[20rem]",
          },
        ]}
      />

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingIndex == null
            ? t("editor.routing.dialog.createTitle")
            : t("editor.routing.dialog.editTitle", {
                index: String(editingIndex + 1),
              })
        }
        description={t("editor.routing.dialog.description")}
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note={t("editor.routing.dialog.footerNote")}
            confirmLabel={t("editor.routing.actions.saveRoute")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label="match_path"
              description={t("editor.routing.fields.matchPathHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.matchPath}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, matchPath: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="match_type"
              description={t("editor.routing.fields.matchTypeHelp")}
            >
              <Select
                ariaLabel={t("editor.routing.fields.matchTypeAria")}
                value={draft.matchType}
                onChange={(value) =>
                  setDraft((current) => ({ ...current, matchType: value }))
                }
                options={routeMatchTypeOptions}
                className="w-full"
                triggerClassName={editorControlClassName}
                optionClassName="text-sm"
              />
            </ConfigFormField>
            <ConfigFormField
              label="group"
              description={t("editor.routing.fields.groupHelp")}
            >
              <div className="space-y-2">
                <Select
                  ariaLabel={t("editor.routing.fields.groupAria")}
                  value={draft.group}
                  onChange={(value) =>
                    setDraft((current) => ({ ...current, group: value }))
                  }
                  options={[
                    { value: "", label: t("editor.routing.fields.groupPlaceholder") },
                    ...availableGroups.map((groupName) => ({
                      value: groupName,
                      label: groupName,
                    })),
                  ]}
                  className="w-full"
                  triggerClassName={editorControlClassName}
                  optionClassName="text-sm"
                />
                <input
                  className={editorControlClassName}
                  value={draft.group}
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, group: event.target.value }))
                  }
                  placeholder={t("editor.routing.fields.groupInputPlaceholder")}
                />
              </div>
            </ConfigFormField>
            <ConfigFormField
              label="protocol"
              description={t("editor.routing.fields.protocolHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.protocol}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, protocol: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="pipeline"
              description={t("editor.routing.fields.pipelineHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.pipeline}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, pipeline: event.target.value }))
                }
                placeholder="chat-completions"
              />
            </ConfigFormField>
            <ConfigFormField label="priority">
              <input
                className={editorControlClassName}
                value={draft.priority}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, priority: event.target.value }))
                }
                placeholder="10"
              />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label="match_models"
              description={t("editor.routing.fields.matchModelsHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.matchModelsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    matchModelsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="exclude_models"
              description={t("editor.routing.fields.excludeModelsHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.excludeModelsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    excludeModelsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="match_model_regexes"
              description={t("editor.routing.fields.matchModelRegexesHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.matchModelRegexesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    matchModelRegexesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="exclude_model_regexes"
              description={t("editor.routing.fields.excludeModelRegexesHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-32 resize-y font-mono`}
                value={draft.excludeModelRegexesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    excludeModelRegexesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <ConfigFormField
            label={t("editor.routing.fields.extraJson")}
            description={t("editor.routing.fields.extraJsonHelp")}
          >
            <textarea
              className={`${editorControlClassName} min-h-40 resize-y font-mono`}
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

function createRouteDraftInput(route: RuntimeRouteSummary | null): RouteDraftInput {
  if (!route) {
    const defaults = createDefaultRuntimeRoute();
    return {
      matchPath:
        typeof defaults.match_path === "string" ? defaults.match_path : "/v1/chat",
      matchType:
        typeof defaults.match_type === "string" ? defaults.match_type : "prefix",
      group: typeof defaults.group === "string" ? defaults.group : "",
      pipeline: "",
      protocol: "",
      priority: "",
      matchModelsText: "",
      matchModelRegexesText: "",
      excludeModelsText: "",
      excludeModelRegexesText: "",
      extraJson: "{}",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(route.raw).filter(([key]) => !KNOWN_ROUTE_KEYS.has(key)),
  );

  return {
    matchPath: route.matchPath,
    matchType: route.matchType || "prefix",
    group: route.group,
    pipeline: route.pipeline,
    protocol: route.protocol,
    priority: route.priority,
    matchModelsText: route.matchModels.join("\n"),
    matchModelRegexesText: route.matchModelRegexes.join("\n"),
    excludeModelsText: route.excludeModels.join("\n"),
    excludeModelRegexesText: route.excludeModelRegexes.join("\n"),
    extraJson: JSON.stringify(extraFields, null, 2),
  };
}
