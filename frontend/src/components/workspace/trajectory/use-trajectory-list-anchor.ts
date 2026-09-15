/**
 * 轨迹列表的「尾部优先」锚定 + 触顶续页（P0-2 拆文件，行为与内联实现一致）。
 *
 * 1) 视口锚定：更早的行插在列表**前面**，若不补偿 scrollTop，用户点「加载更早」/
 *    滚到顶端续页时视口会跳回顶部（内容整体下移了「前插高度」）。scrollHeight
 *    增量即前插内容高度，直接加回 scrollTop 就能让原本可见的行停在原地；跟随
 *    直播（贴底）时不补偿，由贴底逻辑接管。
 * 2) 触顶续页：滚动到顶端时触发 `onLoadEarlier`（与按钮同一条幂等入口，
 *    在途 / 无更早页时由 hook 内直接返回）。
 */
import { useLayoutEffect, useRef, type RefObject } from "react";

/**
 * 触顶判定阈值（px）：留出余量，避免「滚到最顶」差一两像素不触发；轨迹列表实测可达
 * 8.6 万 px，12px 窗口在快速上滑时会被整段跨过，与对话面统一放宽到 64px。
 */
const LOAD_EARLIER_TOP_THRESHOLD = 64;

export function useTrajectoryListAnchor({
  containerRef,
  firstItemKey,
  itemCount,
  followLive,
  syncScroll,
  hasEarlier,
  loadingEarlier,
  onLoadEarlier,
}: {
  /** 滚动容器（虚拟列表的 overflow 容器）。 */
  containerRef: RefObject<HTMLDivElement | null>;
  /** 列表首行 key：变化即「前插了更早内容」，需要补偿视口。 */
  firstItemKey: string | null;
  /** 行数：进入依赖，追加/删除行时刷新高度基线（否则补偿量会多算追加高度）。 */
  itemCount: number;
  /** 是否跟随直播（贴底）：贴底时不做锚定补偿。 */
  followLive: boolean;
  /** 虚拟滚动自身的 scroll 同步（先于触顶续页执行）。 */
  syncScroll: () => void;
  /** 窗口之前还有更早的事件可加载。 */
  hasEarlier: boolean;
  /** 「加载更早」在途：在途时不重复触发。 */
  loadingEarlier: boolean;
  onLoadEarlier?: () => void;
}): () => void {
  const anchorRef = useRef<{ firstKey: string | null; scrollHeight: number }>({
    firstKey: null,
    scrollHeight: 0,
  });

  useLayoutEffect(() => {
    const el = containerRef.current;
    const anchor = anchorRef.current;
    if (
      el &&
      anchor.firstKey !== null &&
      firstItemKey !== null &&
      anchor.firstKey !== firstItemKey &&
      !followLive
    ) {
      const delta = el.scrollHeight - anchor.scrollHeight;
      if (delta > 0) {
        el.scrollTop += delta;
      }
    }
    anchorRef.current = {
      firstKey: firstItemKey,
      scrollHeight: el?.scrollHeight ?? 0,
    };
  }, [containerRef, firstItemKey, followLive, itemCount]);

  return () => {
    syncScroll();
    const el = containerRef.current;
    if (!el || !hasEarlier || loadingEarlier || !onLoadEarlier) {
      return;
    }
    if (el.scrollTop <= LOAD_EARLIER_TOP_THRESHOLD) {
      onLoadEarlier();
    }
  };
}
