/**
 * 轨迹视图主视图（P2-1）：工具栏（搜索/筛选/时间线开关）+ 时间线概览 + 虚拟滚动明细 + 详情面板。
 *
 * - 数据源：TrajectoryStore 快照（与 chat 视图同一 reducer 双投影，P2-4）；
 * - 明细行与详情面板共用同一 Item 对象（验收②）；行 = Item（P2-3，行身份 key=Item.id）；
 * - 流式期间事件逐条出现并自动跟随底部（验收①，用户上滚后暂停跟随）。
 */
import {
  BrainCircuitIcon,
  DownloadIcon,
  GitBranchIcon,
  InfoIcon,
  ListTreeIcon,
  MessageSquareTextIcon,
  SearchIcon,
  ShieldCheckIcon,
  TimerIcon,
  WrenchIcon,
} from "lucide-react";
import {
  memo,
  useMemo,
  useRef,
  useState,
  type ComponentType,
} from "react";
import { useTranslation } from "react-i18next";

import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { useTrajectorySnapshot } from "@/hooks/workspace/use-trajectory-snapshot";
import { exportSessionTrajectoryJsonl } from "@/lib/trajectory/export-session";
import type { TrajectoryItem } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { TrajectoryDetailPanel } from "./trajectory-detail-panel";
import { SubagentSessionDialog } from "./subagent-session-dialog";
import type { SubagentSessionTarget } from "./subagent-session-target";
import { TrajectoryTimeline } from "./trajectory-timeline";
import {
  TRAJECTORY_VIEW_FILTERS,
  trajectoryItemKindKey,
  trajectoryItemMatches,
  trajectoryItemPassesFilter,
  trajectoryItemSummary,
  type TrajectoryViewFilter,
} from "./trajectory-view-shared";
import {
  searchTrajectoryIndex,
  trajectorySearchSignature,
  useTrajectorySearchIndex,
} from "./trajectory-search-index";
import { useVirtualRows } from "./trajectory-virtual-rows";

const KIND_ICONS: Record<TrajectoryItem["kind"], ComponentType<{ size?: number; className?: string }>> = {
  assistant: MessageSquareTextIcon,
  reasoning: BrainCircuitIcon,
  tool: WrenchIcon,
  planning: ListTreeIcon,
  orchestration: ListTreeIcon,
  route: GitBranchIcon,
  observation: GitBranchIcon,
  subagent: GitBranchIcon,
  result: ListTreeIcon,
  system: InfoIcon,
};

const KIND_TEXT_COLORS: Record<TrajectoryItem["kind"], string> = {
  assistant: "text-[#6ea8fe]",
  reasoning: "text-accent-teal",
  tool: "text-accent-gold",
  planning: "text-[#a78bfa]",
  orchestration: "text-[#a78bfa]",
  route: "text-[#a78bfa]",
  observation: "text-[#a78bfa]",
  subagent: "text-[#a78bfa]",
  result: "text-[#a78bfa]",
  system: "text-muted-foreground",
};

const ESTIMATE_ROW_HEIGHT = 34;
const OVERSCAN = 8;

const TrajectoryRow = memo(function TrajectoryRow({
  item,
  selected,
  measure,
  onSelect,
}: {
  item: TrajectoryItem;
  selected: boolean;
  measure: (element: HTMLButtonElement | null) => void;
  onSelect: (itemId: string) => void;
}) {
  const { t } = useTranslation("workspace");
  const Icon = KIND_ICONS[item.kind];
  return (
    <button
      data-trajectory-row="true"
      ref={measure}
      aria-selected={selected}
      className={cn(
        "flex h-full w-full items-center gap-2.5 border-l-2 px-3 text-left transition hover:bg-surface-soft",
        selected
          ? "border-accent-teal bg-surface-soft"
          : "border-transparent",
      )}
      onClick={() => onSelect(item.id)}
      type="button"
    >
      <Icon size={13} className={cn("shrink-0", KIND_TEXT_COLORS[item.kind])} />
      <span className="shrink-0 font-mono app-text-10 text-muted-foreground">
        #{item.seq}
      </span>
      <span className="w-20 shrink-0 app-text-10 uppercase tracking-[0.1em] text-muted-foreground">
        {t(trajectoryItemKindKey(item.kind))}
      </span>
      <span
        className={cn(
          "min-w-0 flex-1 truncate app-text-12 text-foreground",
          item.status === "running" && "text-accent-teal",
        )}
      >
        {trajectoryItemSummary(item)}
      </span>
      {item.status === "running" ? (
        <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-accent-teal" />
      ) : null}
    </button>
  );
});

export function TrajectoryView({
  store,
  isLive = false,
  sessionId,
  className,
}: {
  store: TrajectoryStore;
  isLive?: boolean;
  /** 会话 ID：导出 JSONL 时从 EventStore 拉取事件（P3-2）。 */
  sessionId?: string;
  className?: string;
}) {
  const snapshot = useTrajectorySnapshot(store);
  const { t } = useTranslation("workspace");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<TrajectoryViewFilter>("all");
  const [timelineOpen, setTimelineOpen] = useState(true);
  const [selectedItemId, setSelectedItemId] = useState<string | null>(null);
  const [subagentTarget, setSubagentTarget] = useState<SubagentSessionTarget | null>(null);
  const [exporting, setExporting] = useState(false);
  const [redactExport, setRedactExport] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const searchIndex = useTrajectorySearchIndex(snapshot.items);

  const items = useMemo(
    () => {
      const filtered = snapshot.items.filter((item) =>
        trajectoryItemPassesFilter(item, filter),
      );
      const needle = query.trim().toLowerCase();
      if (!needle) {
        return filtered;
      }
      // 索引新鲜时走 terms AND（O(命中)）；节流窗口内索引滞后 →
      // 线性回退保证结果正确（索引仅为性能优化，P2-7）。
      const fresh =
        searchIndex.signature === trajectorySearchSignature(snapshot.items);
      const hits = fresh ? searchTrajectoryIndex(searchIndex, query) : null;
      if (hits === null) {
        return filtered.filter((item) => trajectoryItemMatches(item, query));
      }
      return filtered.filter((item) => hits.has(item.id));
    },
    [snapshot.items, filter, query, searchIndex],
  );

  const getKey = useMemo(() => (item: TrajectoryItem) => item.id, []);

  const virtual = useVirtualRows({
    items,
    getKey,
    estimateHeight: ESTIMATE_ROW_HEIGHT,
    overscan: OVERSCAN,
    containerRef,
  });

  const selectedItem = useMemo(
    () => snapshot.items.find((item) => item.id === selectedItemId) ?? null,
    [snapshot.items, selectedItemId],
  );

  const handleSelect = (itemId: string) => {
    setSelectedItemId((current) => (current === itemId ? null : itemId));
  };

  const handleExport = async () => {
    if (!sessionId || exporting) {
      return;
    }
    setExporting(true);
    try {
      // P2-7：分页拉取 + JSONL + 下载抽到 `lib/trajectory/export-session`，
      // 与 composer `/export` 命令共用同一实现（本函数只负责按钮的 busy 态）。
      await exportSessionTrajectoryJsonl(sessionId, { redact: redactExport });
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className={cn("flex h-full min-h-0 flex-col", className)}>
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface-softer px-3 py-2">
        <div className="relative min-w-0 flex-1 basis-48">
          <SearchIcon
            size={13}
            className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground"
          />
          <input
            aria-label={t("panels.shell.trajectory.search")}
            className="w-full rounded-md border border-border bg-surface-solid py-1.5 pl-7 pr-2.5 app-text-12 text-foreground outline-none transition placeholder:text-muted-foreground focus:border-accent-teal/45"
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("panels.shell.trajectory.searchPlaceholder")}
            value={query}
          />
        </div>
        <div className="flex items-center gap-1">
          {TRAJECTORY_VIEW_FILTERS.map((option) => (
            <button
              key={option.id}
              aria-pressed={filter === option.id}
              className={cn(
                "rounded-md border px-2.5 py-1 app-text-11 transition",
                filter === option.id
                  ? "border-accent-teal/30 bg-accent-teal/10 text-accent-teal"
                  : "border-border bg-surface-solid text-muted-foreground hover:text-foreground",
              )}
              onClick={() => setFilter(option.id)}
              type="button"
            >
              {t(option.labelKey)}
            </button>
          ))}
        </div>
        <button
          aria-label={t("panels.shell.trajectory.toggleTimeline")}
          aria-pressed={timelineOpen}
          className={cn(
            "rounded-md border p-1.5 transition",
            timelineOpen
              ? "border-accent-teal/30 bg-accent-teal/10 text-accent-teal"
              : "border-border bg-surface-solid text-muted-foreground hover:text-foreground",
          )}
          onClick={() => setTimelineOpen((current) => !current)}
          title={
            timelineOpen
              ? t("panels.shell.trajectory.hideTimeline")
              : t("panels.shell.trajectory.showTimeline")
          }
          type="button"
        >
          <TimerIcon size={13} />
        </button>
        <button
          aria-label={t("panels.shell.trajectory.toggleRedaction")}
          aria-pressed={redactExport}
          className={cn(
            "rounded-md border p-1.5 transition",
            redactExport
              ? "border-accent-teal/30 bg-accent-teal/10 text-accent-teal"
              : "border-border bg-surface-solid text-muted-foreground hover:text-foreground",
          )}
          onClick={() => setRedactExport((current) => !current)}
          title={
            redactExport
              ? t("panels.shell.trajectory.redactionOn")
              : t("panels.shell.trajectory.redactionOff")
          }
          type="button"
        >
          <ShieldCheckIcon size={13} />
        </button>
        <button
          aria-label={t("panels.shell.trajectory.export")}
          className={cn(
            "rounded-md border p-1.5 transition",
            sessionId && !exporting
              ? "border-border bg-surface-solid text-muted-foreground hover:text-foreground"
              : "cursor-not-allowed border-border bg-surface-solid text-muted-foreground",
          )}
          disabled={!sessionId || exporting}
          onClick={handleExport}
          title={
            sessionId
              ? exporting
                ? t("panels.shell.trajectory.exporting")
                : t("panels.shell.trajectory.exportJsonl")
              : t("panels.shell.trajectory.noSessionToExport")
          }
          type="button"
        >
          <DownloadIcon className={exporting ? "animate-pulse" : undefined} size={13} />
        </button>
      </div>

      {timelineOpen && snapshot.items.length > 0 ? (
        <TrajectoryTimeline
          className="px-3 pt-2"
          items={snapshot.items}
          onJumpToItem={(itemId) => {
            virtual.scrollToKey(itemId);
            setSelectedItemId(itemId);
          }}
        />
      ) : null}

      <div className="flex min-h-0 flex-1">
        <div className="relative min-w-0 flex-1 overflow-hidden">
          {items.length === 0 ? (
            <div className="flex h-full items-center justify-center px-4 text-center app-text-12 text-muted-foreground">
              {snapshot.items.length === 0
                ? t("panels.shell.trajectory.empty")
                : t("panels.shell.trajectory.noMatches")}
            </div>
          ) : (
            <div
              data-trajectory-list="true"
              ref={containerRef}
              className="h-full overflow-y-auto"
              onScroll={virtual.handleScroll}
            >
              <div className="relative w-full" style={{ height: virtual.totalHeight }}>
                {virtual.rows.map(({ item, index, offset, height }) => (
                  <div
                    className="absolute left-0 right-0 overflow-hidden"
                    key={item.id}
                    style={{ height, top: offset }}
                  >
                    <TrajectoryRow
                      item={item}
                      measure={(element) => {
                        if (element) {
                          virtual.updateHeights(index, element.offsetHeight);
                        }
                      }}
                      onSelect={handleSelect}
                      selected={item.id === selectedItemId}
                    />
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
        <TrajectoryDetailPanel
          item={selectedItem}
          onClose={() => setSelectedItemId(null)}
          onOpenSubagentSession={setSubagentTarget}
        />
      </div>

      {isLive ? (
        <div className="flex items-center gap-1.5 border-t border-border bg-surface-softer px-3 py-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
          <span className="size-1.5 animate-pulse rounded-full bg-accent-teal" />
          {t("panels.shell.trajectory.streaming")}
        </div>
      ) : null}

      <SubagentSessionDialog
        target={subagentTarget}
        onClose={() => setSubagentTarget(null)}
      />
    </div>
  );
}
