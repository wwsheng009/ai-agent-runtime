/**
 * 轨迹虚拟滚动：行 = Item，行高缓存，窗口化渲染（对齐 P2-3 / DeepSeek-Reasonix transcriptRows 思路）。
 *
 * - 纯函数 `computeVirtualWindow`：给定视口与行高缓存 → 可见行区间（O(n)，行数少时足够；
 *   1000+ 行场景由窗口化保证 DOM 常驻行数 O(overscan)，不随行数增长）；
 * - hook `useVirtualRows`：scroll/resize 状态 + 行高缓存 + 定位能力（scrollToKey/scrollToBottom）。
 */

import { useCallback, useEffect, useRef, useState, type RefObject } from "react";

export interface VirtualRowEntry<T> {
  item: T;
  index: number;
  offset: number;
  height: number;
}

export interface VirtualWindow {
  start: number;
  end: number;
  totalHeight: number;
}

/** 计算行累计高度（第 i 行的 offset = heights 前缀和；未知高度用估算值）。 */
export function computeRowOffsets(
  count: number,
  heights: ReadonlyMap<number, number>,
  estimateHeight: number,
): { offsets: number[]; totalHeight: number } {
  const offsets = new Array<number>(count);
  let total = 0;
  for (let i = 0; i < count; i++) {
    offsets[i] = total;
    total += heights.get(i) ?? estimateHeight;
  }
  return { offsets, totalHeight: total };
}

/**
 * 窗口化：返回 [start, end) 可见区间（含 overscan）。
 * 二分查找起始行（offset 单调不减），再线性推进结束行。
 */
export function computeVirtualWindow(
  scrollTop: number,
  viewportHeight: number,
  count: number,
  heights: ReadonlyMap<number, number>,
  estimateHeight: number,
  overscan = 6,
): VirtualWindow {
  if (count === 0) {
    return { start: 0, end: 0, totalHeight: 0 };
  }
  const { offsets, totalHeight } = computeRowOffsets(count, heights, estimateHeight);

  // 二分：最后一个 offset <= scrollTop 的行
  let lo = 0;
  let hi = count - 1;
  let start = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (offsets[mid] <= scrollTop) {
      start = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }

  const bottom = scrollTop + viewportHeight;
  let end = start;
  while (end < count && offsets[end] < bottom) {
    end++;
  }

  return {
    start: Math.max(0, start - overscan),
    end: Math.min(count, end + overscan),
    totalHeight,
  };
}

export function useVirtualRows<T>(options: {
  items: T[];
  getKey: (item: T) => string;
  estimateHeight: number;
  overscan?: number;
  containerRef: RefObject<HTMLDivElement | null>;
}) {
  const { items, getKey, estimateHeight, overscan = 6, containerRef } = options;
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(0);
  /** index -> 已测量高度（index 布局随 items 头尾变化，key 方案更稳但 O(n)；先 index，行身份由 key 保证）。 */
  const [heights, setHeights] = useState<ReadonlyMap<number, number>>(new Map());
  const [followLive, setFollowLive] = useState(true);
  const heightsRef = useRef<Map<number, number>>(new Map());
  const followLiveRef = useRef(true);

  const updateHeights = useCallback(
    (index: number, height: number) => {
      if (!Number.isFinite(height) || height <= 0) {
        return;
      }
      const map = heightsRef.current;
      const previous = map.get(index);
      if (previous !== undefined && Math.abs(previous - height) < 1) {
        return;
      }
      map.set(index, height);
      setHeights(new Map(map));
    },
    [],
  );

  // 视口高度测量（ResizeObserver）：列表容器可能在 items 非空时才挂载，
  // 依赖 items.length 使 observer 在容器出现/消失时重建。
  useEffect(() => {
    const el = containerRef.current;
    if (!el || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(() => {
      setViewportHeight(el.clientHeight);
    });
    observer.observe(el);
    setViewportHeight(el.clientHeight);
    return () => observer.disconnect();
  }, [containerRef, items.length]);

  const handleScroll = useCallback(() => {
    const el = containerRef.current;
    if (!el) {
      return;
    }
    setScrollTop(el.scrollTop);
    const nearBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < estimateHeight * 2;
    if (nearBottom !== followLiveRef.current) {
      followLiveRef.current = nearBottom;
      setFollowLive(nearBottom);
    }
  }, [containerRef, estimateHeight]);

  const window = computeVirtualWindow(
    scrollTop,
    viewportHeight,
    items.length,
    heights,
    estimateHeight,
    overscan,
  );

  // 窗口化行（带 offset）
  let rows: VirtualRowEntry<T>[] = [];
  if (items.length > 0 && viewportHeight > 0) {
    const { offsets } = computeRowOffsets(items.length, heights, estimateHeight);
    rows = [];
    for (let i = window.start; i < window.end; i++) {
      rows.push({
        item: items[i],
        index: i,
        offset: offsets[i],
        height: heights.get(i) ?? estimateHeight,
      });
    }
  }

  const scrollToKey = useCallback(
    (key: string) => {
      const el = containerRef.current;
      const index = items.findIndex((item) => getKey(item) === key);
      if (!el || index < 0) {
        return;
      }
      const { offsets } = computeRowOffsets(items.length, heightsRef.current, estimateHeight);
      el.scrollTop = Math.max(0, offsets[index] - 24);
      setScrollTop(el.scrollTop);
      followLiveRef.current = false;
      setFollowLive(false);
    },
    [containerRef, items, getKey, estimateHeight],
  );

  const scrollToBottom = useCallback(() => {
    const el = containerRef.current;
    if (!el) {
      return;
    }
    el.scrollTop = el.scrollHeight;
    setScrollTop(el.scrollTop);
    followLiveRef.current = true;
    setFollowLive(true);
  }, [containerRef]);

  // 流式跟随：live 且 items 增长时贴底（主视图在 items 变化后调用 scrollToBottom）
  const lastCount = useRef(items.length);
  useEffect(() => {
    const grew = items.length > lastCount.current;
    lastCount.current = items.length;
    if (grew && followLiveRef.current) {
      const el = containerRef.current;
      if (el) {
        el.scrollTop = el.scrollHeight;
        setScrollTop(el.scrollTop);
      }
    }
  }, [items.length, containerRef]);

  return {
    rows,
    totalHeight: window.totalHeight,
    startIndex: window.start,
    endIndex: window.end,
    followLive,
    updateHeights,
    scrollToKey,
    scrollToBottom,
    handleScroll,
  };
}
