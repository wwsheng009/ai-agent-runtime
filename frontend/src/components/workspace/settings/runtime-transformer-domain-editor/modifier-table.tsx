// 由 components/workspace/settings/runtime-transformer-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { ArrowDownIcon, ArrowUpIcon, Settings2Icon, Trash2Icon } from "lucide-react";

import { Badge } from "@/components/ui/badge";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import {
  type RuntimeTransformerModifierSummary,
  type TransformerModifierScope,
} from "../runtime-transformer-domain-utils";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";

export function RuntimeTransformerModifierTable({
  description,
  items,
  onDeleteModifier,
  onMoveModifier,
  openCreateDialog,
  openEditDialog,
  scope,
  t,
  title,
}: {
  description: string;
  items: RuntimeTransformerModifierSummary[];
  onDeleteModifier: (scope: TransformerModifierScope, index: number) => void;
  onMoveModifier: (scope: TransformerModifierScope, index: number, direction: "up" | "down") => void;
  openCreateDialog: (scope: TransformerModifierScope) => void;
  openEditDialog: (item: RuntimeTransformerModifierSummary) => void;
  scope: TransformerModifierScope;
  t: TFunction<"runtimeConfig">;
  title: string;
}) {
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
