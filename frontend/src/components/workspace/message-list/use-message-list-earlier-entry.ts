/**
 * 对话面「加载更早」入口（同一处 overflow 容器，两件互不干扰的事）：
 *
 * 1) 触顶续页：滚动到消息流顶端附近时自动触发「加载更早」（与顶部按钮同一条幂等
 *    入口，在途 / 无更早页时由入口自身直接返回）。
 * 2) 入口可见性：入口只在**视口贴近顶部**时露面，而不是常驻在滚动口顶部——常驻形态下
 *    用户贴在底部读最新消息时入口一直压着正文（本轮诉求）。判定（含阈值与滞回理由）在
 *    `../earlier-entry-visibility`，与轨迹列表共用同一条实现。
 *
 * 兜底不变式（沿用旧行为）：内容不足一屏时不会产生 scroll 事件——此时 `scrollTop`
 * 恒为 0，判定为贴顶，入口可见，不会出现「翻不动」死角。
 */
import { useEffect } from "react";
import type { RefObject } from "react";

import { useEarlierEntryVisibility } from "../earlier-entry-visibility";

/**
 * 触顶判定阈值（px）：留出余量，避免「滚到最顶」差一两像素不触发；实测一页 100 条
 * 约 1.2 万 px，12px 的窗口在滚轮速度稍快时会被整段跨过（用户以为「没有自动加载」），
 * 放宽到 64px 后正常上滑到底即可续页。
 */
const LOAD_EARLIER_TOP_THRESHOLD = 64;

/** 入口 hook：返回值即「入口是否渲染」。宿主只需透传状态；按钮点击、触顶自动续页共用
 * 同一条幂等入口（`HistoryEarlierLoader.onLoadEarlier`）。 */
export function useMessageListEarlierEntry({
  containerRef,
  hasMore,
  loading,
  onLoadEarlier,
}: {
  /** 消息流的 overflow 容器（与 useConversationScroll 同一个）。 */
  containerRef: RefObject<HTMLDivElement | null>;
  /** 还有更早历史：没有更早页时不渲染入口。 */
  hasMore: boolean;
  /** 在途：入口切到「正在加载」文案并禁用。 */
  loading: boolean;
  /** 加载更早一页（幂等；按钮与触顶自动加载共用）。 */
  onLoadEarlier?: () => void;
}): boolean {
  const showEntry = useEarlierEntryVisibility({
    containerRef,
    enabled: hasMore || loading,
  });

  // 触顶续页（不变量与旧实现一致：在途 / 无更早页 / 无回调时不监听）。
  useEffect(() => {
    const el = containerRef.current;
    if (!el || !hasMore || loading || !onLoadEarlier) {
      return;
    }
    const handleScroll = () => {
      if (el.scrollTop <= LOAD_EARLIER_TOP_THRESHOLD) {
        onLoadEarlier();
      }
    };
    el.addEventListener("scroll", handleScroll, { passive: true });
    return () => el.removeEventListener("scroll", handleScroll);
  }, [containerRef, hasMore, loading, onLoadEarlier]);

  return showEntry;
}
