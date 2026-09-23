// 「路由」区块的档位表格：level × provider/model/effort/source 四列 + 启用/关闭态。
//
// 表格形态的理由（§7.2「档位表格」+ 288px 侧栏硬约束）：provider/model/effort
// 三列是**可编辑输入**，逐档一行让「同一档位的三个字段」在视觉上对齐；
// 行下方再挂该档位的来源提示与后端校验错误（按字段回显）。
//
// 语义纪律（I-6）：输入框初值直接取投影里的生效值；表格不判断某字段「应该」是
// 什么，也不推断来源——来源徽标就是投影的 `source`。

import { Fragment } from "react";
import { useTranslation } from "react-i18next";

import {
  SESSION_DETAIL_CHIP_CLASS,
  SESSION_DETAIL_SECTION_LABEL_CLASS,
} from "@/components/workspace/session-detail-panel-shared";
import { cn } from "@/lib/utils";
import type {
  RoutingLevelSummary,
  SessionRoutingEditableField,
  SessionRoutingTargetLayer,
} from "@/types/runtime";
import { SESSION_ROUTING_EDITABLE_FIELDS } from "@/types/runtime";

import {
  type RoutingErrorEcho,
  type RoutingLevelDraftMap,
  type RoutingSourceKey,
  resolveRoutingSourceKey,
} from "./session-detail-routing-shared";

const FIELD_LABEL_KEY = {
  provider: "panels.sessionDetail.routing.levels.provider",
  model: "panels.sessionDetail.routing.levels.model",
  reasoning_effort: "panels.sessionDetail.routing.levels.effort",
} as const;

const SOURCE_LABEL_KEY: Record<
  RoutingSourceKey,
  | "panels.sessionDetail.routing.source.session"
  | "panels.sessionDetail.routing.source.workspace"
  | "panels.sessionDetail.routing.source.config"
  | "panels.sessionDetail.routing.source.default"
  | "panels.sessionDetail.routing.source.derived"
> = {
  session: "panels.sessionDetail.routing.source.session",
  workspace: "panels.sessionDetail.routing.source.workspace",
  config: "panels.sessionDetail.routing.source.config",
  default: "panels.sessionDetail.routing.source.default",
  derived: "panels.sessionDetail.routing.source.derived",
};

const TH_CLASS = cn(
  SESSION_DETAIL_SECTION_LABEL_CLASS,
  "px-1 py-0.5 text-left font-normal",
);

const FIELD_INPUT_CLASS =
  "h-6 w-full min-w-0 rounded-card border border-border bg-surface-soft px-1 app-text-10 text-foreground outline-none focus:border-accent-primary/40 disabled:cursor-not-allowed disabled:opacity-60";

export type SessionDetailRoutingLevelsProps = {
  levels: readonly RoutingLevelSummary[];
  drafts: RoutingLevelDraftMap;
  layer: SessionRoutingTargetLayer;
  errorEcho: RoutingErrorEcho;
  /** 后端错误原文（不改写、不翻译），仅在 errorEcho 命中行时展示。 */
  errorMessage: string;
  /** true=该层不可写或正在保存：输入框置灰（§7.2「子会话只读」同口径）。 */
  disabled: boolean;
  onDraftChange: (
    level: string,
    field: SessionRoutingEditableField,
    value: string,
  ) => void;
};

export function SessionDetailRoutingLevels({
  levels,
  drafts,
  layer,
  errorEcho,
  errorMessage,
  disabled,
  onDraftChange,
}: SessionDetailRoutingLevelsProps) {
  const { t } = useTranslation("workspace");

  if (levels.length === 0) {
    return (
      <p
        className="app-text-11 leading-5 text-muted-foreground"
        data-testid="routing-levels-empty"
      >
        {t("panels.sessionDetail.routing.levels.empty")}
      </p>
    );
  }

  const sourceLabel = (source: string) => {
    const key = resolveRoutingSourceKey(source);
    return key ? t(SOURCE_LABEL_KEY[key]) : source;
  };

  return (
    <table
      className="w-full table-fixed border-collapse app-text-10"
      data-testid="routing-level-table"
    >
      <thead>
        <tr className="text-muted-foreground">
          <th className={cn(TH_CLASS, "w-[16%]")} scope="col">
            {t("panels.sessionDetail.routing.levels.level")}
          </th>
          <th className={cn(TH_CLASS, "w-[21%]")} scope="col">
            {t("panels.sessionDetail.routing.levels.provider")}
          </th>
          <th className={cn(TH_CLASS, "w-[26%]")} scope="col">
            {t("panels.sessionDetail.routing.levels.model")}
          </th>
          <th className={cn(TH_CLASS, "w-[17%]")} scope="col">
            {t("panels.sessionDetail.routing.levels.effort")}
          </th>
          <th className={cn(TH_CLASS, "w-[20%]")} scope="col">
            {t("panels.sessionDetail.routing.levels.source")}
          </th>
        </tr>
      </thead>
      <tbody>
        {levels.map((level) => {
          const draft = drafts[level.level];
          const inherited = level.source !== "" && level.source !== layer;
          const rowError = errorEcho.level === level.level ? errorEcho : null;
          return (
            <Fragment key={level.level}>
              <tr className="align-top" data-testid={`routing-level-row-${level.level}`}>
                <td className="px-1 py-1">
                  <div className="grid gap-0.5">
                    <span className="truncate font-medium text-foreground">
                      {level.level}
                    </span>
                    <span
                      className={cn(
                        SESSION_DETAIL_CHIP_CLASS,
                        level.enabled
                          ? "border-accent-primary/30 bg-accent-primary/10 text-accent-primary"
                          : "border-border bg-surface-soft text-muted-foreground",
                      )}
                      data-testid={`routing-level-state-${level.level}`}
                    >
                      {level.enabled
                        ? t("panels.sessionDetail.routing.levels.enabled")
                        : t("panels.sessionDetail.routing.levels.disabled")}
                    </span>
                  </div>
                </td>
                {SESSION_ROUTING_EDITABLE_FIELDS.map((field) => (
                  <td className="px-0.5 py-1" key={field}>
                    <input
                      aria-label={`${level.level} ${t(FIELD_LABEL_KEY[field])}`}
                      className={FIELD_INPUT_CLASS}
                      data-testid={`routing-field-${level.level}-${field}`}
                      disabled={disabled}
                      onChange={(event) =>
                        onDraftChange(level.level, field, event.target.value)
                      }
                      spellCheck={false}
                      value={draft?.[field] ?? ""}
                    />
                  </td>
                ))}
                <td className="px-1 py-1">
                  <div className="flex flex-col items-start gap-0.5">
                    <span
                      className={cn(
                        SESSION_DETAIL_CHIP_CLASS,
                        "border-border bg-surface-soft text-muted-foreground",
                      )}
                      data-testid={`routing-level-source-${level.level}`}
                    >
                      {sourceLabel(level.source)}
                    </span>
                    {level.expensive ? (
                      <span
                        className={cn(
                          SESSION_DETAIL_CHIP_CLASS,
                          "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
                        )}
                        data-testid={`routing-level-expensive-${level.level}`}
                      >
                        {t("panels.sessionDetail.routing.levels.expensive")}
                      </span>
                    ) : null}
                  </div>
                </td>
              </tr>
              {inherited || rowError ? (
                <tr data-testid={`routing-level-notes-${level.level}`}>
                  <td className="px-1 pb-1.5" colSpan={5}>
                    <div className="grid gap-0.5">
                      {inherited ? (
                        <p
                          className="app-text-10 leading-4 text-muted-foreground"
                          data-testid={`routing-inherited-${level.level}`}
                        >
                          {t(
                            "panels.sessionDetail.routing.levels.inheritedHint",
                            { source: sourceLabel(level.source) },
                          )}
                        </p>
                      ) : null}
                      {rowError ? (
                        <p
                          className="app-text-10 leading-4 break-words text-analytics-danger"
                          data-testid={`routing-row-error-${level.level}`}
                        >
                          {rowError.field
                            ? `${t(FIELD_LABEL_KEY[rowError.field])}: ${errorMessage}`
                            : errorMessage}
                        </p>
                      ) : null}
                    </div>
                  </td>
                </tr>
              ) : null}
            </Fragment>
          );
        })}
      </tbody>
    </table>
  );
}
