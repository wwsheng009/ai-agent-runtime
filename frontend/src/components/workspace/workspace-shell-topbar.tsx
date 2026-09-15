import {
  ChartNoAxesCombinedIcon,
  DatabaseIcon,
  FlaskConicalIcon,
  ListChecksIcon,
  MessageSquarePlusIcon,
  NetworkIcon,
  PanelLeftOpenIcon,
  PanelRightCloseIcon,
  PanelRightOpenIcon,
  RadioTowerIcon,
  RefreshCwIcon,
  Settings2Icon,
  TerminalSquareIcon,
  WifiOffIcon,
  type LucideIcon,
} from "lucide-react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { SessionGoalIndicator } from "@/components/workspace/session-goal-indicator";
import { buttonVariants } from "@/components/ui/button-variants";
import { Button } from "@/components/ui/button";
import { ConnectionStatusBadge } from "@/components/ui/connection-status-badge";
import {
  getThreadTransportKind,
  type WorkspaceThreadTransportKind,
} from "@/components/workspace/workspace-shell-shared";
import { type Thread } from "@/data/mock";
import { useConnectionStatusLabels } from "@/hooks/workspace/use-connection-status-labels";
import { type ConnectionStatus } from "@/lib/connection-status";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

/**
 * 传输通道的图标 / 配色：颜色复用 `--connection-*` 语义 token（亮暗主题都保持可读），
 * 文本一律只走 tooltip（`title`）与无障碍标签，顶栏不再为它留一行文字宽度。
 */
const TRANSPORT_ICONS: Record<WorkspaceThreadTransportKind, LucideIcon> = {
  live: RadioTowerIcon,
  error: WifiOffIcon,
  seeded: FlaskConicalIcon,
};

const TRANSPORT_TONE_CLASSNAMES: Record<WorkspaceThreadTransportKind, string> = {
  live: "text-connection-online",
  error: "text-connection-offline",
  seeded: "text-muted-foreground",
};

type WorkspaceShellTopbarProps = {
  /** P2-1A：root → 当前会话的 lineage 链（≤1 段时不渲染面包屑）。 */
  agentBreadcrumb?: { label: string; path: string | null }[];
  /** P2-1A：后代目录条数（>0 时在入口按钮上显示计数）。 */
  agentDescendantCount?: number;
  /** P1-8：会话运行时流连接状态（未提供时不渲染状态条）。 */
  connectionStatus?: ConnectionStatus | null;
  density: "comfortable" | "compact";
  isNewThread?: boolean;
  /** P2-9：常驻状态条——当前 live（pending/running）后台任务数（0 时不渲染）。 */
  liveJobsCount?: number;
  liveTeamCount: number;
  onRetryConnection?: () => void;
  /** 刷新当前会话（重新拉取权威历史、运行时状态与列表投影；未提供时不渲染入口）。 */
  onRefreshSession?: () => void;
  /** P2-1A：打开后台任务面板（未提供时不渲染入口）。 */
  onOpenJobs?: () => void;
  /** P2-1A：打开子代理控制面（未提供时不渲染入口）。 */
  onOpenAgents?: () => void;
  onOpenSidebar: () => void;
  onOpenSettings: () => void;
  /** 折叠/展开右侧栏（条目 / 计划 / 还原 / 会话用量 合并为同一面板）。 */
  onToggleRightRail: () => void;
  rightRailOpen: boolean;
  /** 刷新在途：按钮禁用并转圈，避免重复触发。 */
  sessionRefreshing?: boolean;
  selectedThread: Thread;
  threadStatusLabel: string;
  transportLabel: string;
  threadSubtitle: string;
};

export function WorkspaceShellTopbar({
  agentBreadcrumb,
  agentDescendantCount = 0,
  connectionStatus,
  density,
  isNewThread = false,
  liveJobsCount = 0,
  liveTeamCount,
  onOpenAgents,
  onOpenJobs,
  onRefreshSession,
  onRetryConnection,
  onOpenSidebar,
  onOpenSettings,
  onToggleRightRail,
  rightRailOpen,
  sessionRefreshing = false,
  selectedThread,
  threadSubtitle,
  threadStatusLabel,
  transportLabel,
}: WorkspaceShellTopbarProps) {
  const isCompact = density === "compact";
  const { t } = useTranslation("workspace");
  const { labels: connectionLabels, retryLabel } = useConnectionStatusLabels();
  const lineage = agentBreadcrumb ?? [];
  const showLineage = !isNewThread && lineage.length > 1;
  const transportKind = getThreadTransportKind(selectedThread);
  const TransportIcon = TRANSPORT_ICONS[transportKind];
  // tooltip = 状态名 + 一句解释（解释按三态分别取值，避免动态 key 绕过资源类型检查）。
  const transportHint =
    transportKind === "live"
      ? t("topbar.threadTransportHint.live")
      : transportKind === "error"
        ? t("topbar.threadTransportHint.error")
        : t("topbar.threadTransportHint.seeded");
  const transportTooltip = `${transportLabel} · ${transportHint}`;
  const refreshSessionLabel = sessionRefreshing
    ? t("topbar.refreshingSession")
    : t("topbar.refreshSession");

  return (
    <header className="absolute inset-x-0 top-0 z-30 flex justify-center px-3 pt-1.5 sm:px-4">
      <div
        className={cn(
          "flex w-full max-w-[72rem] items-center gap-1 rounded-panel border border-border bg-workspace-topbar-bg shadow-[0_8px_24px_rgba(0,0,0,0.14)] backdrop-blur-lg sm:gap-2",
          isCompact ? "h-10 px-3" : "h-11 px-3.5",
        )}
      >
        <Button
          variant="ghost"
          size="icon"
          className="shrink-0 xl:hidden"
          onClick={onOpenSidebar}
          aria-label={t("topbar.openSidebar")}
          title={t("topbar.openSidebar")}
        >
          <PanelLeftOpenIcon size={16} />
        </Button>
        {!isNewThread ? (
          <Link
            to="/workspace/chats/new"
            className={cn(
              buttonVariants({ variant: "ghost", size: "icon" }),
              "shrink-0 xl:hidden",
            )}
            aria-label={t("topbar.newChat")}
            title={t("topbar.newChat")}
          >
            <MessageSquarePlusIcon size={15} />
          </Link>
        ) : null}
        <div className="min-w-0 flex-1">
          <div className="truncate app-text-13 font-semibold tracking-[-0.02em]">
            {isNewThread ? t("topbar.newThreadTitle") : selectedThread.title}
          </div>
          {!isNewThread ? (
            <div className="hidden truncate app-text-10 text-muted-foreground sm:block">
              {threadSubtitle}
            </div>
          ) : null}
        </div>
        {showLineage ? (
          <div
            className="hidden shrink-0 items-center gap-1.5 rounded-card border border-border px-2 py-1 text-muted-foreground lg:flex"
            data-testid="topbar-agent-breadcrumb"
            title={t("topbar.agentsBreadcrumb", {
              path: lineage.map((entry) => entry.label).join(" › "),
            })}
          >
            <NetworkIcon size={13} className="shrink-0" />
            <span className="max-w-[18rem] truncate app-text-10">
              {lineage.map((entry) => entry.label).join(" › ")}
            </span>
          </div>
        ) : null}
        {!isNewThread ? (
          <div className="hidden items-center gap-2.5 md:flex">
            {/* P2-9：会话目标四相指示（只读投影；无数据不渲染）。 */}
            <SessionGoalIndicator sessionId={selectedThread.sessionId} />
            {connectionStatus && connectionStatus !== "idle" ? (
              <ConnectionStatusBadge
                status={connectionStatus}
                labels={connectionLabels}
                onRetry={onRetryConnection}
                retryLabel={retryLabel}
              />
            ) : null}
            <Badge>{threadStatusLabel}</Badge>
            {liveTeamCount > 0 ? (
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("sidebar.active", { count: liveTeamCount })}
              </div>
            ) : null}
          </div>
        ) : null}
        {/* 传输状态 + 刷新放在状态簇之外：状态簇（目标 / 连接 / 线程状态）窄屏整块隐藏，
            而这两件事任何宽度都要够得着。状态名与解释只在 tooltip / 无障碍标签里露出。 */}
        {!isNewThread ? (
          <>
            <span
              role="img"
              aria-label={transportTooltip}
              title={transportTooltip}
              data-testid="topbar-transport-status"
              data-transport-kind={transportKind}
              className={cn(
                "inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-card",
                TRANSPORT_TONE_CLASSNAMES[transportKind],
              )}
            >
              <TransportIcon size={14} aria-hidden="true" />
            </span>
            {onRefreshSession ? (
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6 shrink-0"
                data-testid="topbar-refresh-session"
                disabled={sessionRefreshing}
                onClick={onRefreshSession}
                aria-label={refreshSessionLabel}
                title={refreshSessionLabel}
              >
                <RefreshCwIcon
                  size={13}
                  aria-hidden="true"
                  className={cn(sessionRefreshing && "animate-spin")}
                />
              </Button>
            ) : null}
          </>
        ) : null}
        <Link
          to="/logs"
          className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "shrink-0")}
          aria-label={t("topbar.logs")}
          title={t("topbar.logs")}
        >
          <TerminalSquareIcon size={16} />
        </Link>
        {onOpenJobs && !isNewThread ? (
          <Button
            variant="ghost"
            size="sm"
            className="shrink-0"
            onClick={onOpenJobs}
            aria-label={t("topbar.jobs")}
            title={t("topbar.jobs")}
          >
            <ListChecksIcon size={16} />
          </Button>
        ) : null}
        {onOpenJobs && !isNewThread && liveJobsCount > 0 ? (
          <button
            type="button"
            className="hidden shrink-0 items-center rounded-card border border-border px-2 py-1 text-muted-foreground sm:flex"
            data-testid="topbar-jobs-running"
            onClick={onOpenJobs}
            title={t("topbar.jobsRunning", { count: liveJobsCount })}
          >
            <span className="app-text-10">{t("topbar.jobsRunning", { count: liveJobsCount })}</span>
          </button>
        ) : null}
        {onOpenAgents && !isNewThread ? (
          <Button
            variant="ghost"
            size="sm"
            className="shrink-0 gap-1"
            onClick={onOpenAgents}
            aria-label={t("topbar.agents")}
            data-testid="topbar-agents"
            title={t("topbar.agents")}
          >
            <NetworkIcon size={16} />
            {agentDescendantCount > 0 ? (
              <span className="app-text-10">{agentDescendantCount}</span>
            ) : null}
          </Button>
        ) : null}
        <Link
          to="/usage"
          className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "shrink-0")}
          aria-label={t("topbar.usage")}
          title={t("topbar.usage")}
        >
          <ChartNoAxesCombinedIcon size={16} />
        </Link>
        <Link
          to="/runtime/config"
          className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "shrink-0")}
          aria-label={t("topbar.runtime")}
          title={t("topbar.runtime")}
        >
          <DatabaseIcon size={16} />
        </Link>
        <Button
          variant="ghost"
          size="sm"
          className="shrink-0"
          onClick={onOpenSettings}
          aria-label={t("topbar.settings")}
          title={t("topbar.settings")}
        >
          <Settings2Icon size={16} />
        </Button>
        {!isNewThread ? (
          <Button
            variant="ghost"
            size="sm"
            className="shrink-0"
            aria-pressed={rightRailOpen}
            data-testid="topbar-toggle-right-rail"
            onClick={onToggleRightRail}
            aria-label={rightRailOpen ? t("topbar.hideRail") : t("topbar.showRail")}
            title={rightRailOpen ? t("topbar.hideRail") : t("topbar.showRail")}
          >
            {rightRailOpen ? (
              <PanelRightCloseIcon size={16} />
            ) : (
              <PanelRightOpenIcon size={16} />
            )}
          </Button>
        ) : null}
      </div>
    </header>
  );
}
