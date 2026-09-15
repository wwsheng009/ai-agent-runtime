// P2-9 子片 2：子代理树（可折叠到末级）+ 只读原因 + 记录跨度。
//
// 纪律：
//   * 层级由 `buildAgentForest` 派生（parentAgentId → agentPath 最长前缀祖先），
//     解析不到父行的身份行保留为顶层节点，不丢弃、不臆造父子；
//   * 折叠只影响渲染，不改数据；折叠按钮文案带隐藏后代数；
//   * 只读原因只展示 `agentReadOnlyReason` 能从数据证实的两类
//     （已关闭记录 / 父代理不在线），其余不猜；
//   * 记录跨度端点缺失 / 非法时不渲染该 chip（不补零、不推算），
//     且文案与 title 明说「非活跃耗时」。

import { ChevronRightIcon } from "lucide-react";
import { type TFunction } from "i18next";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  agentDisplayStatus,
  agentDisplayName,
  agentDurationSpan,
  agentMetaFacts,
  agentPathSegments,
  agentReadOnlyReason,
  agentStatusLabelKey,
  agentStatusToneClass,
  buildAgentForest,
  canResumeAgent,
  canStopAgent,
  flattenAgentTree,
  formatAgentDuration,
  formatAgentDurationExact,
  type AgentDurationLabel,
  type AgentTreeRow,
} from "@/components/workspace/session-agents-panel-shared";
import type { RuntimeAgentRecord } from "@/types/runtime";
import { cn } from "@/lib/utils";

export type SessionAgentsTreeProps = {
  actionError: unknown;
  actionErrorAgentId: string | null;
  agents: RuntimeAgentRecord[];
  onClose: (agentId: string) => void;
  onResume: (agentId: string) => void;
  pendingAgentId: string | null;
};

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** 跨度文案渲染：判别联合逐档静态展开，保持 i18n 键与插值的类型检查。 */
function durationText(t: TFunction<"workspace">, label: AgentDurationLabel): string {
  switch (label.key) {
    case "duration.seconds":
      return t("panels.agents.duration.seconds", label.values);
    case "duration.minutes":
      return t("panels.agents.duration.minutes", label.values);
    case "duration.hours":
      return t("panels.agents.duration.hours", label.values);
    case "duration.days":
      return t("panels.agents.duration.days", label.values);
    case "duration.daysHours":
      return t("panels.agents.duration.daysHours", label.values);
    case "duration.months":
      return t("panels.agents.duration.months", label.values);
    case "duration.monthsDays":
      return t("panels.agents.duration.monthsDays", label.values);
    case "duration.years":
      return t("panels.agents.duration.years", label.values);
    case "duration.yearsMonths":
      return t("panels.agents.duration.yearsMonths", label.values);
    case "duration.exactDays":
      return t("panels.agents.duration.exactDays", label.values);
  }
}

export function SessionAgentsTree({
  actionError,
  actionErrorAgentId,
  agents,
  onClose,
  onResume,
  pendingAgentId,
}: SessionAgentsTreeProps) {
  const { t } = useTranslation("workspace");
  const [collapsedIds, setCollapsedIds] = useState<ReadonlySet<string>>(() => new Set<string>());
  const forest = useMemo(() => buildAgentForest(agents), [agents]);
  const rows = useMemo(() => flattenAgentTree(forest, collapsedIds), [forest, collapsedIds]);

  const toggle = (agentId: string) => {
    setCollapsedIds((current) => {
      const next = new Set(current);
      if (next.has(agentId)) {
        next.delete(agentId);
      } else {
        next.add(agentId);
      }
      return next;
    });
  };

  return (
    <ul aria-label={t("panels.agents.treeAria")} className="mt-2 space-y-1" data-testid="agents-tree">
      {rows.map((row) => (
        <AgentTreeRowItem
          actionError={actionError}
          actionErrorAgentId={actionErrorAgentId}
          collapsed={collapsedIds.has(row.agent.agentId)}
          key={row.agent.agentId}
          onClose={onClose}
          onResume={onResume}
          onToggle={toggle}
          pendingAgentId={pendingAgentId}
          row={row}
        />
      ))}
    </ul>
  );
}

function AgentTreeRowItem({
  actionError,
  actionErrorAgentId,
  collapsed,
  onClose,
  onResume,
  onToggle,
  pendingAgentId,
  row,
}: {
  actionError: unknown;
  actionErrorAgentId: string | null;
  collapsed: boolean;
  onClose: (agentId: string) => void;
  onResume: (agentId: string) => void;
  onToggle: (agentId: string) => void;
  pendingAgentId: string | null;
  row: AgentTreeRow;
}) {
  const { t } = useTranslation("workspace");
  const { agent, depth, hiddenDescendantCount, parent } = row;
  const displayName = agentDisplayName(agent);
  const segments = agentPathSegments(agent.agentPath);
  const facts = agentMetaFacts(agent);
  const pending = pendingAgentId === agent.agentId;
  const failed = actionErrorAgentId === agent.agentId;
  const stopping = pending && canStopAgent(agent.status);
  const resuming = pending && canResumeAgent(agent.status);
  // 展示状态与身份状态分开：徽章看「容器在不在跑」，动作仍按身份状态授权
  // （子代理跑完后身份可能仍是 active，此时仍允许显式「停止」收口）。
  const displayStatus = agentDisplayStatus(agent);

  const span = agentDurationSpan(agent);
  const compact = span ? formatAgentDuration(span.ms) : null;
  const exact = span ? formatAgentDurationExact(span.ms) : null;

  const readOnly = agentReadOnlyReason(agent, parent);
  const readOnlyText =
    readOnly === "parent-offline"
      ? t("panels.agents.readonly.parentOffline", {
          status: t(agentStatusLabelKey(agentDisplayStatus(parent!)), {
            defaultValue: parent!.status,
          }),
        })
      : readOnly === "closed-record"
        ? t("panels.agents.readonly.closedRecord")
        : null;

  return (
    <li
      {...(row.childCount > 0 ? { "aria-expanded": !collapsed } : {})}
      aria-level={depth + 1}
      data-agent-id={agent.agentId}
      data-depth={depth}
      data-status={displayStatus}
      data-testid="agent-row"
      role="treeitem"
      style={depth > 0 ? { paddingLeft: `${depth * 14}px` } : undefined}
    >
      <div className="rounded-card border border-border bg-surface-softer px-2.5 py-2">
        <div className="flex items-start justify-between gap-2">
          <div className="flex min-w-0 items-start gap-1.5">
            {row.childCount > 0 ? (
              <Button
                aria-label={t(
                  collapsed ? "panels.agents.branchExpand" : "panels.agents.branchCollapse",
                  { count: hiddenDescendantCount, name: displayName },
                )}
                className="mt-0.5 shrink-0"
                data-testid="agent-branch-toggle"
                onClick={() => onToggle(agent.agentId)}
                size="icon"
                title={t(
                  collapsed ? "panels.agents.branchExpand" : "panels.agents.branchCollapse",
                  { count: hiddenDescendantCount, name: displayName },
                )}
                variant="ghost"
              >
                <ChevronRightIcon
                  className={cn("transition-transform", !collapsed && "rotate-90")}
                  size={14}
                />
              </Button>
            ) : null}
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="truncate app-text-13 font-medium text-foreground">
                  {displayName}
                </span>
                <Badge
                  className={cn("h-5 px-1.5 text-[10px]", agentStatusToneClass(displayStatus))}
                >
                  {t(agentStatusLabelKey(displayStatus), { defaultValue: displayStatus })}
                </Badge>
                {agent.agentType ? (
                  <span className="app-text-10 text-muted-foreground">{agent.agentType}</span>
                ) : null}
              </div>
              <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 app-text-10 text-muted-foreground">
                {segments.length > 0 ? (
                  <span className="truncate" title={agent.agentPath ?? ""}>
                    {segments.join(" / ")}
                  </span>
                ) : (
                  <span title={agent.agentId}>{agent.agentId}</span>
                )}
                {facts.map((fact) => (
                  <span key={fact.key}>
                    {t(`panels.agents.meta.${fact.key}`, { value: fact.value })}
                  </span>
                ))}
                {compact && exact ? (
                  <span
                    data-testid="agent-duration"
                    title={t("panels.agents.durationExactTitle", {
                      duration: durationText(t, exact),
                      from: span!.from,
                      to: span!.to,
                    })}
                  >
                    {t("panels.agents.durationLabel", {
                      duration: durationText(t, compact),
                    })}
                  </span>
                ) : null}
                {agent.updatedAt ? (
                  <span>{t("panels.agents.updatedAt", { time: agent.updatedAt })}</span>
                ) : null}
              </div>
            </div>
          </div>
          {canStopAgent(agent.status) ? (
            <Button
              aria-label={t("panels.agents.stopLabel", { name: displayName })}
              className="shrink-0"
              data-testid="agent-stop"
              disabled={pending}
              onClick={() => onClose(agent.agentId)}
              size="sm"
              variant="ghost"
            >
              {stopping ? t("panels.agents.stopping") : t("panels.agents.stop")}
            </Button>
          ) : null}
          {canResumeAgent(agent.status) ? (
            <Button
              aria-label={t("panels.agents.resumeLabel", { name: displayName })}
              className="shrink-0"
              data-testid="agent-resume"
              disabled={pending}
              onClick={() => onResume(agent.agentId)}
              size="sm"
              variant="ghost"
            >
              {resuming ? t("panels.agents.resuming") : t("panels.agents.resume")}
            </Button>
          ) : null}
          {!canStopAgent(agent.status) && !canResumeAgent(agent.status) ? (
            <span
              className="shrink-0 app-text-10 text-muted-foreground"
              data-testid="agent-no-action"
            >
              {t("panels.agents.unknownAction")}
            </span>
          ) : null}
        </div>
        {readOnlyText ? (
          <div
            className="mt-1 break-words app-text-11 text-muted-foreground"
            data-testid="agent-readonly"
          >
            {readOnlyText}
          </div>
        ) : null}
        {failed ? (
          <div
            className="mt-1.5 break-words app-text-11 text-analytics-danger"
            data-testid="agent-action-error"
          >
            {t("panels.agents.actionErrorTitle")}
            {" · "}
            {errorText(actionError)}
          </div>
        ) : null}
      </div>
    </li>
  );
}
