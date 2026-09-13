/**
 * 子会话下钻对话框（P1-5 方案 1：前端下钻）。
 *
 * - 入口：父轨迹的 `subagent` item（`session_id` / `agent_id` 即子会话 ID）；
 * - 数据源：子会话自己的 `runtime/events` + `runtime/stream`（不污染父流）；
 * - 渲染：紧凑实时列表（最近 N 条），审批/工具进度与 CLI `/agents panel`
 *   走同一 EventStore，只是展示形态不同；
 * - 隔离：子事件只进入本对话框内的独立 store（`useSubagentSession`）。
 */
import { RefreshCwIcon, XIcon } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";

import {
  useSubagentSession,
  type SubagentSessionStatus,
} from "@/hooks/workspace/use-subagent-session";
import {
  useTrajectorySnapshot,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";
import type { TrajectoryItem } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import type { SubagentSessionTarget } from "./subagent-session-target";
import {
  trajectoryItemKindKey,
  trajectoryItemSummary,
} from "./trajectory-view-shared";

const MAX_VISIBLE_ROWS = 400;

const SUBAGENT_STATUS_KEYS = {
  idle: "panels.shell.trajectory.subagentSession.statuses.idle",
  loading: "panels.shell.trajectory.subagentSession.statuses.loading",
  live: "panels.shell.trajectory.subagentSession.statuses.live",
  reconnecting: "panels.shell.trajectory.subagentSession.statuses.reconnecting",
  closed: "panels.shell.trajectory.subagentSession.statuses.closed",
  error: "panels.shell.trajectory.subagentSession.statuses.error",
} as const satisfies Record<SubagentSessionStatus, string>;

const STATUS_TONE: Record<SubagentSessionStatus, string> = {
  idle: "text-muted-foreground",
  loading: "text-muted-foreground",
  live: "text-accent-teal",
  reconnecting: "text-accent-gold",
  closed: "text-muted-foreground",
  error: "text-accent-gold",
};

const ITEM_STATUS_DOT: Record<TrajectoryItem["status"], string> = {
  pending: "bg-muted-foreground/60",
  running: "bg-accent-teal animate-pulse",
  completed: "bg-accent-teal",
  failed: "bg-accent-gold",
  canceled: "bg-muted-foreground/60",
};

function SubagentSessionRows({ store }: { store: TrajectoryStore }) {
  const snapshot = useTrajectorySnapshot(store);
  const { t } = useTranslation("workspace");
  const items = snapshot.items.slice(-MAX_VISIBLE_ROWS);

  if (items.length === 0) {
    return (
      <div
        data-subagent-session-empty
        className="flex h-full items-center justify-center px-4 text-center app-text-12 text-muted-foreground"
      >
        {t("panels.shell.trajectory.subagentSession.empty")}
      </div>
    );
  }

  return (
    <div
      data-subagent-session-rows
      className="h-full overflow-y-auto px-1 py-1"
    >
      {items.map((item) => (
        <div
          data-subagent-session-row
          className="flex items-start gap-2 rounded-md px-2 py-1.5 hover:bg-surface-soft"
          key={item.id}
        >
          <span
            aria-hidden
            className={cn(
              "mt-1.5 size-1.5 shrink-0 rounded-full",
              ITEM_STATUS_DOT[item.status],
            )}
          />
          <span className="w-8 shrink-0 pt-0.5 font-mono app-text-10 text-muted-foreground">
            #{item.seq}
          </span>
          <span className="w-20 shrink-0 pt-0.5 app-text-10 uppercase tracking-[0.1em] text-muted-foreground">
            {t(trajectoryItemKindKey(item.kind))}
          </span>
          <span className="min-w-0 flex-1 break-words app-text-12 text-foreground">
            {trajectoryItemSummary(item, 240)}
          </span>
        </div>
      ))}
    </div>
  );
}

export function SubagentSessionDialog({
  target,
  onClose,
}: {
  target: SubagentSessionTarget | null;
  onClose: () => void;
}) {
  const enabled = Boolean(target);
  const {
    store,
    status,
    error,
    pendingApproval,
    resolvingApproval,
    approvalError,
    resolveApproval,
    reconnect,
  } = useSubagentSession({
    sessionId: target?.sessionId ?? null,
    enabled,
  });
  const { t } = useTranslation("workspace");

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [enabled, onClose]);

  if (!target) {
    return null;
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      data-subagent-session-dialog
      role="presentation"
    >
      <div
        aria-label={t("panels.shell.trajectory.subagentSession.ariaLabel")}
        aria-modal="true"
        className="flex h-[min(78vh,720px)] w-[min(960px,100%)] flex-col overflow-hidden rounded-lg border border-border bg-surface-softer shadow-2xl"
        role="dialog"
      >
        <header className="flex items-center gap-2 border-b border-border px-3 py-2.5">
          <span className="min-w-0 flex-1 truncate app-text-13 font-semibold text-foreground">
            {t("panels.shell.trajectory.subagentSession.title", {
              id: target.agentId ?? target.sessionId,
            })}
            {target.role ? (
              <span className="ml-2 app-text-11 font-normal text-muted-foreground">
                {target.role}
              </span>
            ) : null}
          </span>
          {target.status ? (
            <span className="shrink-0 rounded-full border border-border bg-surface-soft px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
              {target.status}
            </span>
          ) : null}
          <span
            className={cn(
              "shrink-0 app-text-10 uppercase tracking-[0.12em]",
              STATUS_TONE[status],
            )}
            data-subagent-session-status={status}
          >
            {t(SUBAGENT_STATUS_KEYS[status])}
          </span>
          <button
            aria-label={t("panels.shell.trajectory.subagentSession.refresh")}
            className="shrink-0 rounded-md border border-border bg-surface-solid p-1.5 text-muted-foreground transition hover:text-foreground"
            onClick={reconnect}
            title={t("panels.shell.trajectory.subagentSession.refreshTitle")}
            type="button"
          >
            <RefreshCwIcon size={13} />
          </button>
          <button
            aria-label={t("panels.shell.trajectory.subagentSession.close")}
            className="shrink-0 rounded-md border border-border bg-surface-solid p-1.5 text-muted-foreground transition hover:text-foreground"
            onClick={onClose}
            type="button"
          >
            <XIcon size={13} />
          </button>
        </header>

        {/*
          P1-5 方案 4：inline 审批入口。子会话在 `approval_requested` 到达后
          阻塞等待决定；这里直接复用 actor 的 `approve_tool` 命令，不必回 CLI。
        */}
        {pendingApproval ? (
          <div
            className="flex flex-wrap items-center gap-2 border-b border-border bg-surface-soft px-3 py-2"
            data-subagent-session-approval
          >
            <span className="app-text-12 font-medium text-foreground">
              {t("panels.shell.trajectory.subagentSession.approval.title", {
                tool:
                  pendingApproval.toolName ??
                  t(
                    "panels.shell.trajectory.subagentSession.approval.unknownTool",
                  ),
              })}
            </span>
            {pendingApproval.riskLevel ? (
              <span className="shrink-0 rounded-full border border-border bg-surface-solid px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                {t("panels.shell.trajectory.subagentSession.approval.risk", {
                  level: pendingApproval.riskLevel,
                })}
              </span>
            ) : null}
            {pendingApproval.reason ? (
              <span
                className="min-w-0 flex-1 truncate app-text-11 text-muted-foreground"
                title={pendingApproval.reason}
              >
                {pendingApproval.reason}
              </span>
            ) : (
              <span className="min-w-0 flex-1" />
            )}
            <button
              className="shrink-0 rounded-md border border-accent-teal/60 bg-accent-teal/10 px-2.5 py-1 app-text-11 font-medium text-accent-teal transition hover:bg-accent-teal/20 disabled:cursor-not-allowed disabled:opacity-60"
              data-subagent-session-approval-approve
              disabled={resolvingApproval}
              onClick={() => void resolveApproval(true)}
              type="button"
            >
              {resolvingApproval
                ? t("panels.shell.trajectory.subagentSession.approval.resolving")
                : t("panels.shell.trajectory.subagentSession.approval.approve")}
            </button>
            <button
              className="shrink-0 rounded-md border border-border bg-surface-solid px-2.5 py-1 app-text-11 text-muted-foreground transition hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60"
              data-subagent-session-approval-reject
              disabled={resolvingApproval}
              onClick={() => void resolveApproval(false)}
              type="button"
            >
              {t("panels.shell.trajectory.subagentSession.approval.reject")}
            </button>
          </div>
        ) : null}

        {approvalError ? (
          <div
            className="border-b border-border px-3 py-1.5 app-text-11 text-accent-gold"
            data-subagent-session-approval-error
          >
            {approvalError}
          </div>
        ) : null}

        <div className="min-h-0 flex-1 bg-surface-solid">
          {store ? (
            <SubagentSessionRows store={store} />
          ) : (
            <div className="flex h-full items-center justify-center app-text-12 text-muted-foreground">
              {t("panels.shell.trajectory.subagentSession.subscribing")}
            </div>
          )}
        </div>

        {error ? (
          <div
            className="border-t border-border px-3 py-1.5 app-text-11 text-accent-gold"
            data-subagent-session-error
          >
            {error}
          </div>
        ) : null}
      </div>
    </div>
  );
}
