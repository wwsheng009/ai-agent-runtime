/**
 * 对话面「加载更早」入口（同一处 overflow 容器，两件互不干扰的事）：
 *
 * 1) 触顶续页：滚动到消息流顶端附近时自动触发「加载更早」（与顶部按钮同一条幂等
 *    入口，在途 / 无更早页时由入口自身直接返回）。
 * 2) 入口可见性：入口只在**视口贴近顶部**时露面，而不是常驻在滚动口顶部。
 *    常驻形态下，用户贴在底部读最新消息时，入口会一直浮在正文上方挡字（本轮诉求）；
 *    贴顶判定用**滞回**（出现 / 消失两个阈值），理由见阈值注释。
 *
 * 与轨迹列表同阈值、同语义（见 trajectory/use-trajectory-list-anchor.ts）；区别是
 * 对话面没有虚拟滚动，直接监听同一个 overflow 容器即可，不必插进滚动同步链。
 *
 * 兜底不变式（沿用旧行为）：内容不足一屏时不会产生 scroll 事件——此时 `scrollTop`
 * 恒为 0，判定为贴顶，入口可见，不会出现「翻不动」死角。
 */
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";

/**
 * 触顶判定阈值（px）：留出余量，避免「滚到最顶」差一两像素不触发；实测一页 100 条
 * 约 1.2 万 px，12px 的窗口在滚轮速度稍快时会被整段跨过（用户以为「没有自动加载」），
 * 放宽到 64px 后正常上滑到底即可续页。
 */
const LOAD_EARLIER_TOP_THRESHOLD = 64;

/**
 * 入口**出现**的上界（px）：滚到距顶部 120px 以内（约一条消息）就露面。与
 * `SCROLL_FOLLOW_THRESHOLD`（贴底 120px）对称：底部贴底区不收入口，顶部贴顶区才收。
 */
export const EARLIER_ENTRY_SHOW_TOP_THRESHOLD = 120;

/**
 * 入口**消失**的下界（px）：越过它才收起，与出现阈值之间留出一条滞回带。
 *
 * 为什么必须滞回：入口是**流内 sticky 行**，它的出现 / 收起会改变内容高度（约 32px）。
 * 滚动所有权层（useConversationScroll 的「阅读保顶」）为了「视口里的内容不跳」会把
 * 这段高度补偿回 `scrollTop`，补偿量恰好等于入口高度——单阈值下就变成自激：
 * 越过阈值收起 → 保顶把 scrollTop 拉回阈值内 → 又要显示 → 再补偿……按钮持续闪烁。
 * 滞回带宽（120px）远大于入口高度，两个方向都收敛；顺带也消掉了阈值附近的抖动。
 */
export const EARLIER_ENTRY_HIDE_TOP_THRESHOLD = 240;

/**
 * 贴顶判定的滞回：`wasAtTop` 是上一次结论，只在越过**对应**阈值时才翻转
 * （隐藏时要越过消失下界才显示，显示时要越过出现上界才隐藏）。
 */
export function resolveEarlierEntryAtTop(scrollTop: number, wasAtTop: boolean): boolean {
  return wasAtTop
    ? scrollTop <= EARLIER_ENTRY_HIDE_TOP_THRESHOLD
    : scrollTop <= EARLIER_ENTRY_SHOW_TOP_THRESHOLD;
}

/**
 * 入口 hook：返回值即「入口是否渲染」。
 *
 * 宿主只需透传状态；按钮点击、触顶自动续页共用同一条幂等入口
 * （`HistoryEarlierLoader.onLoadEarlier`）。
 */
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
  // 乐观初值 true：入口是「翻不动」死角之外的唯一兜底，几何还量不到时（ref 未挂上）
  // 宁可照旧显示。首次对账在 useLayoutEffect 里按真实几何收敛，发生在绘制之前，
  // 所以贴底挂载也不会先画出入口再撤掉（那正是要消掉的「闪一下」）。
  const [atTop, setAtTop] = useState(true);
  const atTopRef = useRef(true);
  const entryPossible = hasMore || loading;

  // 可见性：只在「可能有入口」时监听；结论没翻转就不 setState，普通滚动不带来渲染。
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el || !entryPossible) {
      return;
    }
    const sync = () => {
      const next = resolveEarlierEntryAtTop(el.scrollTop, atTopRef.current);
      if (next === atTopRef.current) {
        return;
      }
      atTopRef.current = next;
      setAtTop(next);
    };
    // 挂载 / 入口重新可用时先对一次账（绘制前完成，避免贴底挂载第一帧闪出入口）；
    // 之后只由滚动事件推进——内容高度变化（前插 / 流式）都会经滚动所有权层补偿
    // scrollTop，同样以滚动事件的形式回到这里。
    sync();
    el.addEventListener("scroll", sync, { passive: true });
    return () => el.removeEventListener("scroll", sync);
  }, [containerRef, entryPossible]);

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

  return entryPossible && atTop;
}
