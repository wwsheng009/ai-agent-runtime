// 共享虚拟行列表（diff 行视图与文本视图共用）：固定行高 + 可视窗口渲染 + 占位撑高。
//
// 契约纪律：
//   * **固定行高**是硬前提（不做动态测量）：调用方给出的 rowHeight 必须与真实行高一致，
//     否则滚动位置与可视窗口会漂移；
//   * 只渲染可视窗口 ± overscan 的行，DOM 行数 ≈ 视口高度 / 行高，与总行数无关（2 万行同量级成本）；
//   * 行高/总行数用外层占位 div 撑开（`height = rows.length × rowHeight`），滚动条语义与普通列表一致；
//   * 视口高度未知（首帧 / jsdom 无布局）时按 fallbackRows 渲染，避免测试与首屏出现 0 行空白。
//
// 降级判据：
//   * 未实现动态测量与「滚动锚定」：行内容换行/字号变化导致的真实行高变化由调用方负责（diff 行固定单行）。

import {
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type UIEvent,
} from "react";

import { cn } from "@/lib/utils";

export type VirtualLineListProps<Row> = {
  rows: readonly Row[];
  /** 固定行高（px），必须与真实渲染行高一致。 */
  rowHeight: number;
  rowKey: (row: Row, index: number) => string;
  renderRow: (row: Row, index: number) => ReactNode;
  /** 视口外额外渲染的行数（减少快速滚动白屏）。 */
  overscan?: number;
  /** 视口高度未知时的兜底渲染行数（默认 40）。 */
  fallbackRows?: number;
  className?: string;
  /** 滚动容器 role（diff 传 "rowgroup"；文本视图可传 "list"）。 */
  containerRole?: string;
  containerAriaLabel?: string;
  /** 变化时把滚动位置复位到顶部（切换文件 / 切换视图模式）。 */
  resetKey?: string | number | null;
  testId?: string;
};

export function VirtualLineList<Row>({
  rows,
  rowHeight,
  rowKey,
  renderRow,
  overscan = 8,
  fallbackRows = 40,
  className,
  containerRole,
  containerAriaLabel,
  resetKey = null,
  testId,
}: VirtualLineListProps<Row>) {
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const [viewportHeight, setViewportHeight] = useState(0);
  // 滚动位置与「数据源代号」绑定：resetKey 一变就在**渲染期**归零（effect 里同步 setState 会被 lint 拦下）。
  const [scrollState, setScrollState] = useState<{ key: string | number | null; top: number }>({
    key: resetKey,
    top: 0,
  });
  const scrollTop = Object.is(scrollState.key, resetKey) ? scrollState.top : 0;
  const safeRowHeight = rowHeight > 0 ? rowHeight : 1;

  // 视口高度：ResizeObserver 为主；环境不支持时退化为 0（走 fallbackRows）。
  useEffect(() => {
    const element = viewportRef.current;
    if (!element || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry) {
        setViewportHeight(entry.contentRect.height);
      }
    });
    observer.observe(element);
    setViewportHeight(element.clientHeight);
    return () => observer.disconnect();
  }, []);

  // 切换数据源（文件 / 视图模式）：滚动位置归零，避免停在上一个文件的中间。
  // 状态归零在渲染期完成（见上），这里只把真实 DOM 的滚动位置同步回去。
  useEffect(() => {
    const element = viewportRef.current;
    if (element) {
      element.scrollTop = 0;
    }
  }, [resetKey]);

  const handleScroll = (event: UIEvent<HTMLDivElement>) => {
    setScrollState({ key: resetKey, top: event.currentTarget.scrollTop });
  };

  const effectiveHeight =
    viewportHeight > 0 ? viewportHeight : Math.max(1, fallbackRows) * safeRowHeight;
  const visibleCount = Math.ceil(effectiveHeight / safeRowHeight) + overscan * 2;
  const maxStart = Math.max(0, rows.length - 1);
  const start = Math.min(
    maxStart,
    Math.max(0, Math.floor(scrollTop / safeRowHeight) - overscan),
  );
  const end = Math.min(rows.length, start + visibleCount);
  const visibleRows: ReactNode[] = [];
  for (let index = start; index < end; index += 1) {
    const row = rows[index];
    visibleRows.push(
      <div
        key={rowKey(row, index)}
        data-virtual-row-index={index}
        style={{ height: safeRowHeight }}
      >
        {renderRow(row, index)}
      </div>,
    );
  }

  return (
    <div
      ref={viewportRef}
      aria-label={containerAriaLabel}
      className={cn("app-virtual-scroll overflow-auto", className)}
      data-testid={testId}
      onScroll={handleScroll}
      role={containerRole}
      tabIndex={-1}
    >
      <div
        className="relative w-full"
        data-virtual-total={rows.length}
        style={{ height: rows.length * safeRowHeight }}
      >
        <div
          className="absolute inset-x-0 top-0"
          style={{ transform: `translateY(${start * safeRowHeight}px)` }}
        >
          {visibleRows}
        </div>
      </div>
    </div>
  );
}
