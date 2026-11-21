import { MoreHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { cn } from "@/lib/utils";
import type {
  ConnectorActionSpec,
  ConnectorBindingView,
  ConnectorSchema,
  ConnectorView,
} from "@/types/runtime";

import { ConnectorBindingsList } from "./bindings-list";
import { ConnectorRuntimeSummary } from "./runtime-summary";
import { findProviderSchema } from "./shared";

type ConnectorRowProps = {
  item: ConnectorView;
  schema: ConnectorSchema | null;
  bindings: ConnectorBindingView[];
  busy: boolean;
  onRunAction: (item: ConnectorView, action: ConnectorActionSpec) => void;
  onSetLogs: (item: ConnectorView) => void;
  onToggle: (item: ConnectorView) => void;
  onEdit: (item: ConnectorView) => void;
  onSetTest: (item: ConnectorView) => void;
  onTakeover: (item: ConnectorView) => void;
  onReload: (item: ConnectorView) => void;
  onDelete: (item: ConnectorView) => void;
};

export function ConnectorRow({
  item,
  schema,
  bindings,
  busy,
  onRunAction,
  onSetLogs,
  onToggle,
  onEdit,
  onSetTest,
  onTakeover,
  onReload,
  onDelete,
}: ConnectorRowProps) {
  const { t } = useTranslation("connectors");
  const providerActions = findProviderSchema(schema, item.provider)?.actions ?? [];
  const takeoverVisible =
    item.enabled && !item.owned_by_current_host && Boolean(item.active_owner);

  return (
    <li className="flex flex-col gap-2 rounded-card border border-border bg-surface-soft px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium text-foreground">
            {item.name}
          </span>
          <Badge>{item.provider}</Badge>
          {item.mode ? <Badge>{item.mode}</Badge> : null}
          <Badge>{item.enabled ? t("page.enabled") : t("page.disabled")}</Badge>
          {item.policy.runtime_owner ? (
            <span className="text-xs text-muted-foreground">
              {t("page.owner", { owner: item.policy.runtime_owner })}
            </span>
          ) : null}
          {/* 运行归属（active_owner）：本宿主高亮，空=未运行。 */}
          <span
            className={cn(
              "text-xs",
              item.owned_by_current_host
                ? "font-medium text-accent-primary"
                : "text-muted-foreground",
            )}
          >
            {item.active_owner
              ? t("page.activeOwner", { owner: item.active_owner })
              : t("page.unclaimed")}
          </span>
        </div>
        <span className="truncate text-xs text-muted-foreground">{item.id}</span>
        {/* 运行计数与最近错误（runtime additive；缺失时不展示）。 */}
        <ConnectorRuntimeSummary runtime={item.runtime} />
        {/* 所在会话与节点（docs/bots/09 §5.3）。 */}
        <ConnectorBindingsList bindings={bindings} />
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        {providerActions.map((action) => (
          <Button
            key={action.key}
            variant="secondary"
            size="sm"
            disabled={busy || !item.enabled}
            onClick={() => void onRunAction(item, action)}
          >
            {action.label}
          </Button>
        ))}
        <Button
          variant="secondary"
          size="sm"
          disabled={busy}
          onClick={() => onSetLogs(item)}
        >
          {t("page.logs")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={busy}
          onClick={() => void onToggle(item)}
        >
          {item.enabled ? t("page.disable") : t("page.enable")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={!schema || busy}
          onClick={() => onEdit(item)}
        >
          {t("page.edit")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={busy}
          onClick={() => onSetTest(item)}
        >
          {t("page.test")}
        </Button>
        <details
          className="relative"
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.currentTarget.removeAttribute("open");
            }
          }}
        >
          <summary
            className={cn(
              buttonVariants({ variant: "secondary", size: "sm" }),
              "cursor-pointer list-none marker:hidden [&::-webkit-details-marker]:hidden",
            )}
            aria-disabled={busy}
          >
            <MoreHorizontalIcon size={14} aria-hidden="true" />
            {t("page.more")}
          </summary>
          <div className="surface-panel absolute right-0 top-full z-30 mt-1 w-44 rounded-panel p-1 shadow-[var(--elevation-panel)]">
            {takeoverVisible ? (
              <button
                type="button"
                className="flex w-full items-center rounded-control px-2 py-1.5 text-left text-sm hover:bg-surface-softer disabled:cursor-not-allowed disabled:opacity-50"
                disabled={busy}
                onClick={(event) => {
                  event.currentTarget.closest("details")?.removeAttribute("open");
                  void onTakeover(item);
                }}
              >
                {t("page.takeover")}
              </button>
            ) : null}
            <button
              type="button"
              className="flex w-full items-center rounded-control px-2 py-1.5 text-left text-sm hover:bg-surface-softer disabled:cursor-not-allowed disabled:opacity-50"
              disabled={busy}
              onClick={(event) => {
                event.currentTarget.closest("details")?.removeAttribute("open");
                void onReload(item);
              }}
            >
              {t("page.reload")}
            </button>
            <button
              type="button"
              className="mt-1 flex w-full items-center rounded-control border-t border-border px-2 pb-1.5 pt-2 text-left text-sm text-accent-orange hover:bg-accent-orange/8 disabled:cursor-not-allowed disabled:opacity-50"
              disabled={busy}
              onClick={(event) => {
                event.currentTarget.closest("details")?.removeAttribute("open");
                void onDelete(item);
              }}
            >
              {t("page.remove")}
            </button>
          </div>
        </details>
      </div>
    </li>
  );
}