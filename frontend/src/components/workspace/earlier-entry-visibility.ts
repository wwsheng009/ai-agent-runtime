/**
 * 「加载更早」入口的贴顶可见性（对话面消息流 / 轨迹列表共用同一条判定）。
 *
 * 入口是滚动口顶部的 sticky 行，但 sticky 只保证「在贴顶区内浮着」，不保证「该不该
 * 出现」：常驻形态下用户贴在底部读最新内容时，它会一直压在正文/列表上方挡字。这里把
 * 「入口是否渲染」收敛成一个判定——**只在视口贴近顶部时露面**，滚回贴顶区立刻又能找到
 * （手动兜底入口不丢）。
 *
 * 与入口的触顶自动续页（64px，见 message-list 与 trajectory 的 paging hook）是两件事：
 * 续页阈值不变，可见性另用 120 / 240px 的滞回带，互不干扰。
 *
 * 兜底不变式（沿用旧行为）：内容不足一屏时不会产生 scroll 事件——此时 `scrollTop` 恒为
 * 0，判定为贴顶，入口可见，不会出现「翻不动」死角。
 */
import { useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";

/**
 * 入口**出现**的上界（px）：滚到距顶部 120px 以内（约一条消息 / 十几行轨迹）就露面。
 * 与 `SCROLL_FOLLOW_THRESHOLD`（贴底 120px）对称：底部贴底区不收入口，顶部贴顶区才收。
 */
export const EARLIER_ENTRY_SHOW_TOP_THRESHOLD = 120;

/**
 * 入口**消失**的下界（px）：越过它才收起，与出现阈值之间留出一条滞回带。
 *
 * 为什么必须滞回：入口是**流内 sticky 行**，它的出现 / 收起会改变内容高度（消息面约
 * 32px，轨迹列表约 38px）。滚动所有权层为了「视口里的内容不跳」会把这段高度补偿回
 * `scrollTop`（对话面是 useConversationScroll 的「阅读保顶」，轨迹面是
 * use-trajectory-list-anchor 的前插补偿），补偿量恰好等于入口高度——单阈值下就变成
 * 自激：越过阈值收起 → 保顶把 scrollTop 拉回阈值内 → 又要显示 → 再补偿……按钮持续闪烁。
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
 * 入口可见性 hook：返回值即「入口是否渲染」（`enabled && 贴顶`）。
 *
 * 对话面与轨迹面共用；两边的差别只有「入口是否可能存在」与「容器何时挂载」，都由参数传入。
 */
export function useEarlierEntryVisibility({
  containerRef,
  enabled,
  containerKey,
}: {
  /** 入口所在的 overflow 容器（与滚动所有权层同一个）。 */
  containerRef: RefObject<HTMLDivElement | null>;
  /** 入口是否可能渲染（还有更早内容 / 在途）；false 时直接收起，也不挂监听。 */
  enabled: boolean;
  /**
   * 容器挂载键：容器可能按内容条件挂载（轨迹面板空列表时整块不渲染），变化即重新对账。
   * 不传时也可以工作，但容器后挂上（0 行 → 有行）就不会补挂监听，判定会停在旧结论上。
   */
  containerKey?: unknown;
}): boolean {
  // 乐观初值 true：入口是「翻不动」死角之外的唯一兜底，几何还量不到时（ref 未挂上）
  // 宁可照旧显示。首次对账在 useLayoutEffect 里按真实几何收敛，发生在绘制之前，
  // 所以贴底挂载也不会先画出入口再撤掉（那正是要消掉的「闪一下」）。
  const [atTop, setAtTop] = useState(true);
  const atTopRef = useRef(true);

  // 可见性：只在「可能有入口」时监听；结论没翻转就不 setState，普通滚动不带来渲染。
  useLayoutEffect(() => {
    const el = containerRef.current;
    // 对账函数：容器不在时判为贴顶——没有滚动可言，入口保持兜底可见；也负责把上一个
    // 容器留下的旧结论收回（空列表 + 还有更早内容时，手动入口不能跟着消失）。
    const sync = () => {
      const node = containerRef.current;
      const next = node
        ? resolveEarlierEntryAtTop(node.scrollTop, atTopRef.current)
        : true;
      if (next === atTopRef.current) {
        return;
      }
      atTopRef.current = next;
      setAtTop(next);
    };
    if (!el) {
      sync();
      return;
    }
    if (!enabled) {
      return;
    }
    // 挂载 / 入口重新可用时先对一次账（绘制前完成，避免贴底挂载第一帧闪出入口）；
    // 之后只由滚动事件推进——内容高度变化（前插 / 流式）都会经滚动所有权层补偿
    // scrollTop，同样以滚动事件的形式回到这里。
    sync();
    el.addEventListener("scroll", sync, { passive: true });
    return () => el.removeEventListener("scroll", sync);
  }, [containerRef, enabled, containerKey]);

  return enabled && atTop;
}
