// 由 components/workspace/settings/runtime-routing-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { GitBranchPlusIcon } from "lucide-react";

import {
  ConfigDomainSummaryBadge,
} from "../config-domain-table";
import { ConfigFormField } from "../config-form-field";
import {
  editorControlClassName,
  editorToggleRowClassName,
} from "../editor-control-class";
import {
  type RuntimeRoutingConfigSummary,
  type RuntimeRouteSummary,
} from "../runtime-routing-domain-utils";
import { SettingsBadgeList } from "../settings-badge-list";
import { SettingsPanelIcon } from "../settings-panel-icon";

export function RuntimeRoutingOverviewSection({
  onChangeConfig,
  protocolCount,
  routeConfig,
  routes,
  t,
}: {
  onChangeConfig: (next: RuntimeRoutingConfigSummary) => void;
  protocolCount: number;
  routeConfig: RuntimeRoutingConfigSummary;
  routes: RuntimeRouteSummary[];
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <div className="rounded-panel border border-border bg-surface-softer p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <SettingsPanelIcon>
              <GitBranchPlusIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-foreground">
                {t("editor.routing.title")}
              </div>
              <div className="mt-1 text-sm text-muted-foreground">
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
            label={t("editor.routing.fields.strategy")}
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
              placeholder={t("editor.routing.fields.strategyPlaceholder")}
            />
          </ConfigFormField>

          <div className="rounded-card border border-border bg-surface-softer p-3">
            <div className="app-text-13 font-semibold text-foreground">
              {t("editor.routing.fields.failover")}
            </div>
            <div className="mt-1 text-xs leading-5 text-muted-foreground">
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
                className="h-4 w-4 accent-accent-primary"
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
  );
}
