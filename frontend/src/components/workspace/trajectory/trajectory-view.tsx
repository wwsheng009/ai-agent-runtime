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
  UserIcon,
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

import { useEarlierEntryVisibility } from "@/components/workspace/earlier-entry-visibility";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { useTrajectorySnapshot } from "@/hooks/workspace/use-trajectory-snapshot";
import { exportSessionTrajectoryJsonl } from "@/lib/trajectory/export-session";
import { isFullTrajectoryWindow, trajectoryItemsInWindow } from "@/lib/trajectory/timeline-window";
import type { TrajectoryItem, TrajectoryItemKind } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { TrajectoryDetailPanel } from "./trajectory-detail-panel";
import { SubagentSessionDialog } from "./subagent-session-dialog";
import type { SubagentSessionTarget } from "./subagent-session-target";
import {
  TrajectoryTimeline,
  type TrajectoryTimelineViewport,
} from "./trajectory-timeline";
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
import { TrajectoryLoadEarlierRow } from "./trajectory-load-earlier-row";
import { useTrajectoryListAnchor } from "./use-trajectory-list-anchor";
import { useVirtualRows } from "./trajectory-virtual-rows";

const KIND_ICONS: Record<TrajectoryItem["kind"], ComponentType<{ size?: number; className?: string }>> = {
  user: UserIcon,
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
  user: "text-[#4ade80]",
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
  hasEarlier = false,
  loadingEarlier = false,
  onLoadEarlier,
}: {
  store: TrajectoryStore;
  isLive?: boolean;
  /** 会话 ID：导出 JSONL 时从 EventStore 拉取事件（P3-2）。 */
  sessionId?: string;
  className?: string;
  /**
   * 尾部优先（tail-first）：已回放窗口之前还有更早的事件可加载
   * （首屏只回放最近一页，见 hooks/workspace/use-trajectory-recovery.ts）。
   */
  hasEarlier?: boolean;
  /** 「加载更早」在途：禁用入口，避免并发重复翻页。 */
  loadingEarlier?: boolean;
  /** 加载更早一页（前插到窗口之前；滚动到顶端时也会自动触发）。 */
  onLoadEarlier?: () => void;
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
  // 图表 → 列表联动：时间线窗口（缩放/框选/预设）与消息类型筛选都在图表上操作。
  const [chartKind, setChartKind] = useState<TrajectoryItemKind | null>(null);
  const [chartViewport, setChartViewport] = useState<TrajectoryTimelineViewport | null>(null);
  const [timelineResetToken, setTimelineResetToken] = useState(0);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const searchIndex = useTrajectorySearchIndex(snapshot.items);

  const searchedItems = useMemo(
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

  // 与图表内部同源的数据集（工具栏筛选 + 搜索 + 图表类型筛选）；
  // 时间线收到的是未按类型过滤的 searchedItems，由它用 activeKind 自行收敛。
  const kindFilteredItems = useMemo(
    () => (chartKind ? searchedItems.filter((item) => item.kind === chartKind) : searchedItems),
    [chartKind, searchedItems],
  );
  // 时间线隐藏 / 列表已空时组件会回调 null 清空视窗，避免残留过期区间。
  const viewport = timelineOpen ? chartViewport : null;
  // 视窗轴必须与当前数据集同长才可用（类型/筛选切换的过渡帧跳过过滤，避免错位）。
  const viewportMatchesItems =
    viewport !== null && viewport.axis.positions.length === kindFilteredItems.length;
  const windowFiltered =
    viewportMatchesItems && !isFullTrajectoryWindow(viewport.axis, viewport.window);
  const items = useMemo(
    () =>
      viewport && viewportMatchesItems && windowFiltered
        ? trajectoryItemsInWindow(kindFilteredItems, viewport.axis, viewport.window)
        : kindFilteredItems,
    [kindFilteredItems, viewport, viewportMatchesItems, windowFiltered],
  );

  const handleToggleChartKind = (kind: TrajectoryItemKind) => {
    setChartKind((current) => (current === kind ? null : kind));
  };

  const getKey = useMemo(() => (item: TrajectoryItem) => item.id, []);

  const virtual = useVirtualRows({
    items,
    getKey,
    estimateHeight: ESTIMATE_ROW_HEIGHT,
    overscan: OVERSCAN,
    containerRef,
  });

  // 尾部优先：前插更早页的视口锚定 + 滚到顶端自动续页（实现见同目录 hook）。
  const handleListScroll = useTrajectoryListAnchor({
    containerRef,
    firstItemKey: items.length > 0 ? getKey(items[0]) : null,
    itemCount: items.length,
    followLive: virtual.followLive,
    syncScroll: virtual.handleScroll,
    hasEarlier,
    loadingEarlier,
    onLoadEarlier,
  });

  // 入口可见性：与对话面同一条判定（贴底读最新事件时不常驻遮挡列表，见共享模块）；
  // 轨迹列表容器按行数条件挂载，故把「行非空」作为挂载键，容器后挂上时补挂监听。
  const showEarlierEntry = useEarlierEntryVisibility({
    containerRef,
    enabled: hasEarlier || loadingEarlier,
    containerKey: items.length > 0,
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
        /* 时间轴区域独立成块：`shrink-0` + 高度上限 + 自身滚动（`overscroll-contain`
           防止滚轮串到列表），底边框与浅底把「三条泳道」和下方消息列表分开；
           窗口变矮时被压缩的是本区域的滚动视口，而不是列表可视高度。 */
        <section
          aria-label={t("panels.shell.trajectory.timeline.regionLabel")}
          className="max-h-[40%] shrink-0 overflow-y-auto overscroll-contain border-b border-border bg-surface-softer px-3 pt-2 pb-2"
          data-testid="trajectory-timeline-region"
        >
          <TrajectoryTimeline
            activeKind={chartKind}
            items={searchedItems}
            /* 「清除区间筛选」用重挂载复位窗口：避免在 effect 里 setState。 */
            key={timelineResetToken}
            onJumpToItem={(itemId) => {
              virtual.scrollToKey(itemId);
              setSelectedItemId(itemId);
            }}
            onToggleKind={handleToggleChartKind}
            onViewportChange={setChartViewport}
          />
        </section>
      ) : null}

      {chartKind || windowFiltered ? (
        <div
          className="flex flex-wrap items-center gap-1.5 border-b border-border bg-surface-softer px-3 py-1 app-text-10 text-muted-foreground"
          data-testid="trajectory-timeline-filter"
        >
          <span data-testid="trajectory-timeline-filter-summary">
            {t("panels.shell.trajectory.timelineFilter.summary", {
              visible: items.length,
              total: snapshot.items.length,
            })}
          </span>
          {chartKind ? (
            <button
              className="cursor-pointer rounded-[3px] border border-border px-1.5 py-0.5 transition hover:text-foreground"
              data-testid="trajectory-timeline-filter-kind"
              onClick={() => setChartKind(null)}
              title={t("panels.shell.trajectory.timelineFilter.clearKind")}
              type="button"
            >
              {t("panels.shell.trajectory.timelineFilter.kind", {
                kind: t(trajectoryItemKindKey(chartKind)),
              })}{" "}
              ✕
            </button>
          ) : null}
          {windowFiltered ? (
            <button
              className="cursor-pointer rounded-[3px] border border-border px-1.5 py-0.5 transition hover:text-foreground"
              data-testid="trajectory-timeline-filter-window"
              onClick={() => setTimelineResetToken((token) => token + 1)}
              title={t("panels.shell.trajectory.timelineFilter.clearWindow")}
              type="button"
            >
              {t("panels.shell.trajectory.timelineFilter.clearWindow")}
            </button>
          ) : null}
        </div>
      ) : null}

      <div className="flex min-h-0 flex-1" data-testid="trajectory-body-region">
        <div
          className="relative min-h-0 min-w-0 flex-1 overflow-hidden"
          data-testid="trajectory-list-region"
        >
          {items.length === 0 ? (
            <div className="flex h-full flex-col">
              {/* 窗口内还没有可渲染行、但更早还有内容时（极端：最新一页全是
                  生命周期事件），入口仍要可见——否则用户会以为会话是空的。 */}
              <TrajectoryLoadEarlierRow
                visible={showEarlierEntry}
                loading={loadingEarlier}
                onLoad={onLoadEarlier}
              />
              <div className="flex flex-1 items-center justify-center px-4 text-center app-text-12 text-muted-foreground">
                {snapshot.items.length === 0
                  ? t("panels.shell.trajectory.empty")
                  : windowFiltered
                    ? t("panels.shell.trajectory.timelineFilter.noWindowMatches")
                    : t("panels.shell.trajectory.noMatches")}
              </div>
            </div>
          ) : (
            <div
              data-trajectory-list="true"
              data-row-count={items.length}
              ref={containerRef}
              className="h-full overflow-y-auto"
              onScroll={handleListScroll}
            >
              <TrajectoryLoadEarlierRow
                visible={showEarlierEntry}
                loading={loadingEarlier}
                onLoad={onLoadEarlier}
              />
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
