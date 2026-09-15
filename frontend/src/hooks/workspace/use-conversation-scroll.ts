/**
 * P1-3：会话滚动所有权（唯一滚动宿主）。
 *
 * 三条互斥契约：
 * - **贴底跟随**：视觉上处于底部时，内容增长（流式 chunk / 工具卡 / 异步
 *   渲染回流）由 `ResizeObserver` 持续把底线钉住；不再依赖「一次性 rAF」。
 * - **阅读保顶**：用户向上离开底部后，内容在视口上方插入（历史前插 / 折叠
 *   展开 / remount）时按**语义锚点**（消息 id + 视口偏移）重算 scrollTop，
 *   阅读位置不跳。
 * - **跨挂载记忆**：锚点按 `memoryKey`（线程 id）记忆，会话切换回来时先恢复
 *   阅读位置；锚点缺失时按贴底处理。
 *
 * active Turn 由 reading-line（视口 1/3 处）几何命中，渲染层据此标注
 * `data-active-turn`；锚点行与 active 行同源，保证「读到哪」与「锚在哪」一致。
 *
 * 宿主需配合 `overflow-anchor: none`（由 MessageList 写入）：原生 scroll
 * anchoring 与语义锚点是两套所有权，交给浏览器就无法按消息 id 恢复。
 */
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";

import {
  isAwayFromBottom,
  maxScrollTop,
  pickConversationAnchor,
  pickReadingLineIndex,
  READING_LINE_RATIO,
  recallConversationAnchor,
  rememberConversationAnchor,
  resolveAnchorScrollTop,
  type ConversationScrollAnchor,
  type ReadingLineRow,
  type ScrollMetrics,
} from "@/lib/conversation-scroll";

export type UseConversationScrollOptions = {
  /** 滚动宿主（overflow-y-auto 容器）。 */
  containerRef: RefObject<HTMLElement | null>;
  /** 语义行容器：其内 `[data-message-id]` 按 DOM 顺序即消息顺序。 */
  contentRef: RefObject<HTMLElement | null>;
  /** 内容签名（消息数组引用）：每次提交后重新对账。 */
  revision?: unknown;
  /** 让出滚动所有权（回溯导航期间由 scrollIntoView 接管）。 */
  suspended?: boolean;
  /** 跨挂载记忆键（线程 id）；缺省时锚点只在本次挂载内有效。 */
  memoryKey?: string | null;
};

export type ConversationScrollState = {
  /** reading-line 命中的消息 id（无内容时为 null）。 */
  activeMessageId: string | null;
  /** 当前是否钉在底部（供渲染层表达状态）。 */
  stickToBottom: boolean;
};

/**
 * active 标记（reading-line 命中行）的刷新间隔。
 *
 * 它是**视觉标记**而不是滚动所有权：贴底跟随期间内容每 chunk 都在长，整表几何
 * 却只需要维持一个「读到哪一行」的高亮。节流到 150ms（≈6.7 次/秒）后，大会话
 * 的流式渲染不再把 O(n) 的 rect 扫掠压在每一帧上。
 */
const MARKER_SYNC_THROTTLE_MS = 150;

/** 属性选择器的值转义（消息 id 只可能含引号/反斜杠这类字符）。 */
function escapeAttributeValue(value: string): string {
  return value.replace(/["\\]/g, "\\$&");
}

export function useConversationScroll({
  containerRef,
  contentRef,
  revision,
  suspended = false,
  memoryKey = null,
}: UseConversationScrollOptions): ConversationScrollState {
  const [activeMessageId, setActiveMessageId] = useState<string | null>(null);
  const [stickToBottom, setStickToBottom] = useState(true);

  /** 贴底意图：true = 内容增长时把底线钉住。只有用户主动离开底部才翻 false。 */
  const stickRef = useRef(true);
  /** 当前锚点（reading-line 命中行 + 视口偏移）。 */
  const anchorRef = useRef<ConversationScrollAnchor | null>(null);
  /** 已对账过的记忆键：键变化（含 remount 首次提交）时从记忆里取锚点。 */
  const memoryKeyRef = useRef<string | null>(null);
  const suspendedRef = useRef(suspended);
  const wasSuspendedRef = useRef(false);
  const frameRef = useRef(0);
  /**
   * 帧内几何缓存：同一帧里布局效应 / ResizeObserver / rAF 回调都会做对账，缓存让
   * 整表 `getBoundingClientRect` 扫掠一帧只发生一次（流式期间每 chunk 一次提交，
   * 原实现是 3 次 O(n) 扫掠 + 3 次强制回流，大会话下就是「页面卡住」的主因）。
   */
  const rowsCacheRef = useRef<ReadingLineRow[] | null>(null);
  const rowsCacheFrameRef = useRef(0);
  /** active 标记（reading-line 命中行）的刷新节流时间戳。 */
  const markerSyncAtRef = useRef(0);

  useEffect(() => {
    suspendedRef.current = suspended;
  }, [suspended]);

  useEffect(
    () => () => {
      if (rowsCacheFrameRef.current) {
        cancelAnimationFrame(rowsCacheFrameRef.current);
        rowsCacheFrameRef.current = 0;
      }
      rowsCacheRef.current = null;
    },
    [],
  );

  const readMetrics = useCallback((): ScrollMetrics | null => {
    const container = containerRef.current;
    if (!container) {
      return null;
    }
    return {
      clientHeight: container.clientHeight,
      scrollHeight: container.scrollHeight,
      scrollTop: container.scrollTop,
    };
  }, [containerRef]);

  const readRows = useCallback((): ReadingLineRow[] => {
    const cached = rowsCacheRef.current;
    if (cached) {
      return cached;
    }
    const container = containerRef.current;
    const content = contentRef.current;
    if (!container || !content) {
      return [];
    }
    const containerTop = container.getBoundingClientRect().top;
    const rows: ReadingLineRow[] = [];
    content
      .querySelectorAll<HTMLElement>("[data-message-id]")
      .forEach((node) => {
        const id = node.dataset.messageId;
        if (!id) {
          return;
        }
        const rect = node.getBoundingClientRect();
        rows.push({
          id,
          top: rect.top - containerTop,
          bottom: rect.bottom - containerTop,
        });
      });
    rowsCacheRef.current = rows;
    if (!rowsCacheFrameRef.current) {
      rowsCacheFrameRef.current = requestAnimationFrame(() => {
        rowsCacheFrameRef.current = 0;
        rowsCacheRef.current = null;
      });
    }
    return rows;
  }, [containerRef, contentRef]);

  /**
   * 单行几何：锚点恢复只关心锚点行自身，不必扫掠整表（阅读历史 + 流式增长时
   * 原来每个 chunk 都要重新量 N 行，这里退化成 1 行）。
   */
  const readRowById = useCallback(
    (messageId: string): ReadingLineRow | null => {
      const container = containerRef.current;
      const content = contentRef.current;
      if (!container || !content) {
        return null;
      }
      const node = content.querySelector<HTMLElement>(
        `[data-message-id="${escapeAttributeValue(messageId)}"]`,
      );
      if (!node) {
        return null;
      }
      const containerTop = container.getBoundingClientRect().top;
      const rect = node.getBoundingClientRect();
      return {
        id: messageId,
        top: rect.top - containerTop,
        bottom: rect.bottom - containerTop,
      };
    },
    [containerRef, contentRef],
  );

  const pinToBottom = useCallback(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    container.scrollTop = container.scrollHeight;
  }, [containerRef]);

  /** 把锚点行拉回原视口偏移；锚点行不在 DOM（被移除/折叠）时放弃调整。 */
  const restoreAnchor = useCallback(
    (anchor: ConversationScrollAnchor | null) => {
      const container = containerRef.current;
      const metrics = readMetrics();
      if (!container || !anchor || !metrics) {
        return;
      }
      const row = readRowById(anchor.messageId);
      if (!row) {
        return;
      }
      const next = resolveAnchorScrollTop({
        anchorOffsetTop: anchor.offsetTop,
        currentOffsetTop: row.top,
        max: maxScrollTop(metrics),
        scrollTop: metrics.scrollTop,
      });
      if (Math.abs(next - metrics.scrollTop) >= 1) {
        container.scrollTop = next;
      }
    },
    [containerRef, readMetrics, readRowById],
  );

  /** DOM 几何快照：贴底判定、锚点行、reading-line 命中。 */
  const readSnapshot = useCallback(() => {
    const metrics = readMetrics();
    if (!metrics) {
      return null;
    }
    const rows = readRows();
    const activeIndex = pickReadingLineIndex(
      rows,
      metrics.clientHeight * READING_LINE_RATIO,
    );
    return {
      activeId: activeIndex >= 0 ? rows[activeIndex].id : null,
      anchor: pickConversationAnchor(rows, metrics.clientHeight),
      away: isAwayFromBottom(metrics),
    };
  }, [readMetrics, readRows]);

  /** 锚点只写 ref + 记忆：可在布局效应里同步调用（不触发渲染）。 */
  const refreshAnchor = useCallback(
    (anchor: ConversationScrollAnchor | null) => {
      if (!anchor) {
        return;
      }
      anchorRef.current = anchor;
      if (memoryKeyRef.current) {
        rememberConversationAnchor(memoryKeyRef.current, anchor);
      }
    },
    [],
  );

  /**
   * 状态对账（setState）：只允许在滚动事件 / ResizeObserver / rAF 回调里调用
   * ——避免在 effect 主体内同步 setState 引发级联渲染。
   */
  const applySnapshot = useCallback(
    (snapshot: {
      activeId: string | null;
      anchor: ConversationScrollAnchor | null;
      away: boolean;
    }) => {
      const nextStick = !snapshot.away;
      if (nextStick !== stickRef.current) {
        stickRef.current = nextStick;
        setStickToBottom(nextStick);
      }
      setActiveMessageId((current) =>
        current === snapshot.activeId ? current : snapshot.activeId,
      );
    },
    [],
  );

  /** 提交后 / 滚动后的统一对账：ref 同步落地，state 走调用方上下文。 */
  const sync = useCallback(() => {
    const snapshot = readSnapshot();
    if (!snapshot) {
      return;
    }
    refreshAnchor(snapshot.anchor);
    applySnapshot(snapshot);
  }, [applySnapshot, readSnapshot, refreshAnchor]);

  /**
   * active 标记的节流对账：内容增长（流式 chunk / 工具卡回流 / 折叠展开）时
   * 只按 `MARKER_SYNC_THROTTLE_MS` 刷新一次，不再每帧扫掠整表几何。
   * `force` 用于记忆键切换 / 让出恢复这类必须立即对齐的时刻。
   */
  const scheduleMarkerSync = useCallback(
    (force = false) => {
      const now = Date.now();
      if (!force && now - markerSyncAtRef.current < MARKER_SYNC_THROTTLE_MS) {
        return;
      }
      markerSyncAtRef.current = now;
      if (frameRef.current) {
        return;
      }
      frameRef.current = requestAnimationFrame(() => {
        frameRef.current = 0;
        sync();
      });
    },
    [sync],
  );

  // 滚动事件 rAF 合流：读取最新 DOM 几何，避免每个滚动事件都触发一次布局查询。
  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    const handleScroll = () => {
      if (frameRef.current) {
        return;
      }
      frameRef.current = requestAnimationFrame(() => {
        frameRef.current = 0;
        sync();
      });
    };
    container.addEventListener("scroll", handleScroll, { passive: true });
    return () => {
      container.removeEventListener("scroll", handleScroll);
      if (frameRef.current) {
        cancelAnimationFrame(frameRef.current);
        frameRef.current = 0;
      }
    };
  }, [containerRef, sync]);

  // 内容/宿主几何变化：贴底时钉底线，否则按锚点保顶。ResizeObserver 回调在
  // 绘制前投递，调整不会闪一帧。
  useEffect(() => {
    const container = containerRef.current;
    const content = contentRef.current;
    if (
      !container ||
      !content ||
      typeof ResizeObserver === "undefined" ||
      suspended
    ) {
      return;
    }
    const observer = new ResizeObserver(() => {
      if (suspendedRef.current) {
        return;
      }
      if (stickRef.current) {
        pinToBottom();
      } else {
        restoreAnchor(anchorRef.current);
      }
      scheduleMarkerSync();
    });
    observer.observe(content);
    observer.observe(container);
    return () => {
      observer.disconnect();
    };
  }, [
    containerRef,
    contentRef,
    pinToBottom,
    restoreAnchor,
    scheduleMarkerSync,
    suspended,
  ]);

  // 每次提交后的布局对账（绘制前）：记忆键切换 → 恢复阅读位置；否则贴底跟随 /
  // 保顶二选一。
  useLayoutEffect(() => {
    const key = memoryKey ?? null;
    const keyChanged = key !== memoryKeyRef.current;
    memoryKeyRef.current = key;
    // 让出所有权（回溯导航）期间滚动位置由 scrollIntoView 决定；恢复时先从
    // 几何重新推导贴底意图，避免拿让出前的陈旧意图把视图拽走。
    const resumed = wasSuspendedRef.current;
    wasSuspendedRef.current = suspended;
    if (suspended) {
      return;
    }
    if (keyChanged) {
      anchorRef.current = key ? recallConversationAnchor(key) : null;
    }
    if (resumed) {
      const snapshot = readSnapshot();
      if (snapshot) {
        stickRef.current = !snapshot.away;
      }
    }
    if (keyChanged && anchorRef.current) {
      // 首次进入该线程：按记忆恢复阅读位置（锚点贴近底部时等价于贴底）。
      restoreAnchor(anchorRef.current);
      scheduleMarkerSync(true);
    } else if (stickRef.current) {
      pinToBottom();
      // 贴底跟随期间只需要钉住底线；active 标记是纯视觉状态，节流刷新即可，
      // 这样流式提交不再每帧触发整表几何扫掠。
      scheduleMarkerSync(keyChanged || resumed);
    } else {
      restoreAnchor(anchorRef.current);
      scheduleMarkerSync(false);
    }
  }, [
    memoryKey,
    pinToBottom,
    readSnapshot,
    restoreAnchor,
    revision,
    scheduleMarkerSync,
    suspended,
  ]);

  return { activeMessageId, stickToBottom };
}
