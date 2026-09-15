/**
 * 对话面触顶续页：滚动到消息流顶端附近时自动触发「加载更早」（与顶部按钮同一条
 * 幂等入口，在途 / 无更早页时由入口自身直接返回）。
 *
 * 与轨迹列表同阈值、同语义（见 trajectory/use-trajectory-list-anchor.ts）；区别是
 * 对话面没有虚拟滚动，直接监听同一个 overflow 容器即可，不必插进滚动同步链。
 * 内容不足一屏时不会产生 scroll 事件——入口由顶部按钮兜底，不会出现「翻不动」死角。
 */
import { useEffect, type RefObject } from "react";

/**
 * 触顶判定阈值（px）：留出余量，避免「滚到最顶」差一两像素不触发；实测一页 100 条
 * 约 1.2 万 px，12px 的窗口在滚轮速度稍快时会被整段跨过（用户以为「没有自动加载」），
 * 放宽到 64px 后正常上滑到底即可续页。
 */
const LOAD_EARLIER_TOP_THRESHOLD = 64;

export function useMessageListEarlierLoader({
  containerRef,
  hasMore,
  loading,
  onLoadEarlier,
}: {
  /** 消息流的 overflow 容器（与 useConversationScroll 同一个）。 */
  containerRef: RefObject<HTMLDivElement | null>;
  /** 还有更早历史：没有更早页时不监听滚动。 */
  hasMore: boolean;
  /** 在途：期间摘掉监听（入口自身也幂等，这里只是省掉无谓调用）。 */
  loading: boolean;
  onLoadEarlier?: () => void;
}): void {
  // 依赖是「真实状态」而非每帧新对象：宿主传入的入口对象与状态稳定，
  // 因此监听只在 hasMore / loading 真正翻转时重建，不会每次 render 解绑重挂。
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
}
