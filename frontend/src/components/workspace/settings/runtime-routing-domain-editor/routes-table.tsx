// 由 components/workspace/settings/runtime-routing-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { ArrowDownIcon, ArrowUpIcon, GitBranchPlusIcon, Trash2Icon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import {
  type RuntimeRoutingConfigSummary,
  type RuntimeRouteSummary,
} from "../runtime-routing-domain-utils";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";

export function RuntimeRoutingRoutesTable({
  availableGroups,
  onDeleteRoute,
  onMoveRoute,
  openCreateDialog,
  openEditDialog,
  routeConfig,
  routes,
  t,
}: {
  availableGroups: string[];
  onDeleteRoute: (index: number) => void;
  onMoveRoute: (index: number, direction: "up" | "down") => void;
  openCreateDialog: () => void;
  openEditDialog: (route: RuntimeRouteSummary) => void;
  routeConfig: RuntimeRoutingConfigSummary;
  routes: RuntimeRouteSummary[];
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <ConfigDomainTable
        title={t("editor.routing.routes.tableTitle")}
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
                  <div className="font-semibold text-foreground">
                    {route.matchPath || "--"}
                  </div>
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
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
                <div className="mt-1 text-xs text-muted-foreground">
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
                <div className="mt-1 text-xs text-muted-foreground">
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
                <div className="mt-1 flex flex-wrap gap-2 text-xs text-muted-foreground">
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
  );
}
