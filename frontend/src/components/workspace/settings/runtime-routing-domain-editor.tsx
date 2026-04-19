import {
  ArrowDownIcon,
  ArrowUpIcon,
  GitBranchPlusIcon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";

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
                Routing 配置
              </div>
              <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                顶部维护 routing 根配置，下面用表格管理路由规则顺序和匹配条件。
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <ConfigDomainSummaryBadge>{`${routes.length} 条路由`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>{`${protocolCount} 个协议条件`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {routeConfig.failover ? "failover 开" : "failover 关"}
            </ConfigDomainSummaryBadge>
          </SettingsBadgeList>
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-[minmax(0,1fr)_13rem]">
          <ConfigFormField label="routing.strategy" description="常见值包括 health 等路由策略。">
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
              控制路由层是否允许故障转移。
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>{routeConfig.failover ? "已启用" : "已关闭"}</span>
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
        description="路由顺序会影响命中结果。可以直接在表格里上移、下移规则，再用弹窗编辑匹配条件。"
        items={routes}
        getRowKey={(route) => route.id}
        emptyState="当前还没有路由规则，可直接新建第一条 route。"
        summary={
          <>
            <ConfigDomainSummaryBadge>{`${routes.length} 条规则`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>{`${availableGroups.length} 个 group 可选`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>{`strategy ${routeConfig.strategy || "--"}`}</ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton size="sm" label="新建路由" onClick={openCreateDialog} />
        }
        columns={[
          {
            header: "顺序 / 匹配",
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
            header: "目标分组",
            cell: (route) => (
              <div className="min-w-[12rem]">
                <div>{route.group || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {route.pipeline
                    ? `pipeline ${route.pipeline}`
                    : route.protocol || "未设 protocol"}
                </div>
              </div>
            ),
          },
          {
            header: "模型条件",
            cell: (route) => (
              <div className="min-w-[14rem]">
                <div>{route.matchModels.length > 0 ? route.matchModels.join(", ") : "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {route.excludeModels.length > 0
                    ? `exclude ${route.excludeModels.join(", ")}`
                    : "无排除条件"}
                </div>
              </div>
            ),
          },
          {
            header: "优先级 / 扩展",
            cell: (route) => (
              <div className="min-w-[12rem]">
                <div>{route.priority ? `priority ${route.priority}` : "--"}</div>
                <div className="mt-1 flex flex-wrap gap-2 text-xs text-[var(--muted-foreground)]">
                  {route.pipeline ? <span>{`pipeline ${route.pipeline}`}</span> : null}
                  {route.extraFieldCount > 0 ? (
                    <span>{`${route.extraFieldCount} 个扩展字段`}</span>
                  ) : (
                    <span>无扩展字段</span>
                  )}
                </div>
              </div>
            ),
          },
          {
            header: "操作",
            cell: (route) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowUpIcon size={14} />}
                  label="上移"
                  onClick={() => onMoveRoute(route.index, "up")}
                  disabled={route.index === 0}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowDownIcon size={14} />}
                  label="下移"
                  onClick={() => onMoveRoute(route.index, "down")}
                  disabled={route.index === routes.length - 1}
                />
                <SettingsActionButton
                  variant="secondary"
                  label="编辑"
                  onClick={() => openEditDialog(route)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label="删除"
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
        title={editingIndex == null ? "新建 Route" : `编辑 Route #${editingIndex + 1}`}
        description="主匹配字段使用表单编辑，模型匹配与扩展字段使用文本区和 JSON，兼顾高频维护和配置完整性。"
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note="路由顺序也会影响命中结果，保存后可继续在表格里调整顺序。"
            confirmLabel="保存路由"
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
            <ConfigFormField label="match_path" description="如 /v1/chat、/v1/messages、/v1/responses。">
              <input
                className={editorControlClassName}
                value={draft.matchPath}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, matchPath: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField label="match_type" description="常见值为 prefix、exact、regex。">
              <Select
                ariaLabel="路由匹配类型"
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
            <ConfigFormField label="group" description="路由命中后转发到的 provider group。">
              <div className="space-y-2">
                <Select
                  ariaLabel="路由目标分组"
                  value={draft.group}
                  onChange={(value) =>
                    setDraft((current) => ({ ...current, group: value }))
                  }
                  options={[
                    { value: "", label: "请选择 group" },
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
                  placeholder="也可以直接输入 group 名称"
                />
              </div>
            </ConfigFormField>
            <ConfigFormField label="protocol" description="可选，显式限制协议，如 openai、anthropic、codex。">
              <input
                className={editorControlClassName}
                value={draft.protocol}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, protocol: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField label="pipeline" description="可选，兼容旧 routing 配置里的 pipeline 目标。">
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
            <ConfigFormField label="match_models" description="支持换行或逗号输入模型匹配列表。">
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
              description="支持换行或逗号输入模型排除列表。"
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
              description="按行填写正则规则。"
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
              description="按行填写正则排除规则。"
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
            label="扩展字段 JSON"
            description="保留未进入专用表单的 route 字段，避免丢配置。"
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
