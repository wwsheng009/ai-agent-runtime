// 「路由」区块的档位卡片：level/启用态/来源 + provider/model/effort 三字段。
//
// 卡片形态适配右栏 288px 硬约束：每个档位独立成组，字段在窄栏纵向堆叠、
// 宽栏横向展开；来源提示与后端校验错误挂在同一卡片下方（按字段回显）。
//
// 语义纪律（I-6）：输入框初值直接取投影里的生效值；卡片不判断某字段「应该」是
// 什么，也不推断来源——来源徽标就是投影的 `source`。

import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";

import { SESSION_DETAIL_CHIP_CLASS } from "@/components/workspace/session-detail-panel-shared";
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
    <div className="grid gap-2" data-testid="routing-level-table">
      {levels.map((level) => {
        const draft = drafts[level.level];
        const inherited = level.source !== "" && level.source !== layer;
        const rowError = errorEcho.level === level.level ? errorEcho : null;
        return (
          <div
            key={level.level}
            className="rounded-field border border-border/70 bg-surface-soft/40 p-2"
            data-testid={`routing-level-row-${level.level}`}
          >
            <div className="flex flex-wrap items-start justify-between gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-1.5">
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
              <span
                className={cn(
                  SESSION_DETAIL_CHIP_CLASS,
                  "border-border bg-surface-soft text-muted-foreground",
                )}
                data-testid={`routing-level-source-${level.level}`}
              >
                {sourceLabel(level.source)}
              </span>
            </div>

            <div className="mt-2 grid gap-1.5 sm:grid-cols-3">
              {SESSION_ROUTING_EDITABLE_FIELDS.map((field) => (
                <label className="block min-w-0" key={field}>
                  <span className="mb-1 block app-text-10 text-muted-foreground">
                    {t(FIELD_LABEL_KEY[field])}
                  </span>
                  <Input
                    aria-label={`${level.level} ${t(FIELD_LABEL_KEY[field])}`}
                    size="sm"
                    surface="soft"
                    className="min-w-0"
                    data-testid={`routing-field-${level.level}-${field}`}
                    disabled={disabled}
                    onChange={(event) =>
                      onDraftChange(level.level, field, event.target.value)
                    }
                    spellCheck={false}
                    value={draft?.[field] ?? ""}
                  />
                </label>
              ))}
            </div>

            {inherited || rowError ? (
              <div
                className="mt-1.5 grid gap-0.5"
                data-testid={`routing-level-notes-${level.level}`}
              >
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
            ) : null}
          </div>
        );
      })}
    </div>
  );
}
