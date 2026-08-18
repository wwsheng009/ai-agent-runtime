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

import { fetchSessionRuntimeEvents } from "@/api/runtime/sessions";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { useTrajectorySnapshot } from "@/hooks/workspace/use-trajectory-snapshot";
import {
  buildTrajectoryExportFilename,
  downloadTrajectoryJsonl,
  eventsToTrajectoryJsonl,
} from "@/lib/trajectory/export";
import { nextRecoveryAfter, TRAJECTORY_RECOVERY_PAGE_SIZE } from "@/lib/trajectory/recovery";
import type { TrajectoryItem } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { TrajectoryDetailPanel } from "./trajectory-detail-panel";
import { TrajectoryTimeline } from "./trajectory-timeline";
import {
  TRAJECTORY_VIEW_FILTERS,
  trajectoryItemKindLabel,
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
  reasoning: "text-[#8fd0c6]",
  tool: "text-[#f0c77b]",
  planning: "text-[#a78bfa]",
  orchestration: "text-[#a78bfa]",
  route: "text-[#a78bfa]",
  observation: "text-[#a78bfa]",
  subagent: "text-[#a78bfa]",
  result: "text-[#a78bfa]",
  system: "text-[var(--muted-foreground)]",
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
  const Icon = KIND_ICONS[item.kind];
  return (
    <button
      data-trajectory-row="true"
      ref={measure}
      aria-selected={selected}
      className={cn(
        "flex h-full w-full items-center gap-2.5 border-l-2 px-3 text-left transition hover:bg-[var(--surface-soft)]",
        selected
          ? "border-[#8fd0c6] bg-[var(--surface-soft)]"
          : "border-transparent",
      )}
      onClick={() => onSelect(item.id)}
      type="button"
    >
      <Icon size={13} className={cn("shrink-0", KIND_TEXT_COLORS[item.kind])} />
      <span className="shrink-0 font-mono app-text-10 text-[var(--muted-foreground)]">
        #{item.seq}
      </span>
      <span className="w-20 shrink-0 app-text-10 uppercase tracking-[0.1em] text-[var(--muted-foreground)]">
        {trajectoryItemKindLabel(item.kind)}
      </span>
      <span
        className={cn(
          "min-w-0 flex-1 truncate app-text-12 text-[var(--foreground)]",
          item.status === "running" && "text-[#8fd0c6]",
        )}
      >
        {trajectoryItemSummary(item)}
      </span>
      {item.status === "running" ? (
        <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-[#8fd0c6]" />
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
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<TrajectoryViewFilter>("all");
  const [timelineOpen, setTimelineOpen] = useState(true);
  const [selectedItemId, setSelectedItemId] = useState<string | null>(null);
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
      const events: Awaited<
        ReturnType<typeof fetchSessionRuntimeEvents>
      >["events"] = [];
      let after = 0;
      for (;;) {
        const page = await fetchSessionRuntimeEvents(sessionId, {
          after,
          limit: TRAJECTORY_RECOVERY_PAGE_SIZE,
        });
        events.push(...page.events);
        if (
          page.events.length === 0 ||
          page.events.length < TRAJECTORY_RECOVERY_PAGE_SIZE
        ) {
          break;
        }
        after = nextRecoveryAfter(page.events, after);
      }
      const jsonl = eventsToTrajectoryJsonl(events, { redact: redactExport });
      downloadTrajectoryJsonl(
        jsonl,
        buildTrajectoryExportFilename(sessionId, undefined, redactExport),
      );
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className={cn("flex h-full min-h-0 flex-col", className)}>
      <div className="flex flex-wrap items-center gap-2 border-b border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2">
        <div className="relative min-w-0 flex-1 basis-48">
          <SearchIcon
            size={13}
            className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--muted-foreground)]"
          />
          <input
            aria-label="Search trajectory"
            className="w-full rounded-md border border-[var(--border)] bg-[var(--surface-solid)] py-1.5 pl-7 pr-2.5 app-text-12 text-[var(--foreground)] outline-none transition placeholder:text-[var(--muted-foreground)] focus:border-[#8fd0c6]/45"
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search trajectory…"
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
                  ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                  : "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] hover:text-[var(--foreground)]",
              )}
              onClick={() => setFilter(option.id)}
              type="button"
            >
              {option.label}
            </button>
          ))}
        </div>
        <button
          aria-label="Toggle timeline"
          aria-pressed={timelineOpen}
          className={cn(
            "rounded-md border p-1.5 transition",
            timelineOpen
              ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 text-[#8fd0c6]"
              : "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] hover:text-[var(--foreground)]",
          )}
          onClick={() => setTimelineOpen((current) => !current)}
          title={timelineOpen ? "Hide timeline" : "Show timeline"}
          type="button"
        >
          <TimerIcon size={13} />
        </button>
        <button
          aria-label="Toggle export redaction"
          aria-pressed={redactExport}
          className={cn(
            "rounded-md border p-1.5 transition",
            redactExport
              ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 text-[#8fd0c6]"
              : "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] hover:text-[var(--foreground)]",
          )}
          onClick={() => setRedactExport((current) => !current)}
          title={
            redactExport
              ? "Redaction on: tool args/outputs are masked in the export"
              : "Redaction off: export includes raw tool args/outputs"
          }
          type="button"
        >
          <ShieldCheckIcon size={13} />
        </button>
        <button
          aria-label="Export trajectory"
          className={cn(
            "rounded-md border p-1.5 transition",
            sessionId && !exporting
              ? "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] hover:text-[var(--foreground)]"
              : "cursor-not-allowed border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]",
          )}
          disabled={!sessionId || exporting}
          onClick={handleExport}
          title={
            sessionId
              ? exporting
                ? "Exporting…"
                : "Export trajectory as JSONL"
              : "No session to export"
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
            <div className="flex h-full items-center justify-center px-4 text-center app-text-12 text-[var(--muted-foreground)]">
              {snapshot.items.length === 0
                ? "No trajectory events yet — start a conversation to see the agent run trail."
                : "No rows match the current filter."}
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
        />
      </div>

      {isLive ? (
        <div className="flex items-center gap-1.5 border-t border-[var(--border)] bg-[var(--surface-softer)] px-3 py-1 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          <span className="size-1.5 animate-pulse rounded-full bg-[#8fd0c6]" />
          Streaming
        </div>
      ) : null}
    </div>
  );
}
