import { useMemo } from "react";

/** 「加载更早」入口的数据源：轨迹回放结果的窗口状态 + 加载回调（结构子集，便于脱离回放实现测试）。 */
export type TrajectoryEarlierSource = {
  window: { hasMore: boolean };
  loadingEarlier: boolean;
  loadEarlier: () => void;
};

/**
 * 轨迹视图的「加载更早」入口（尾部优先窗口）。
 *
 * 对象引用必须保持稳定：`window` 状态不变时下游不应收到新 prop，否则渲染链上
 * 每次 render 都会认为入口发生变化。从 workspace-page 抽出，页面只保留编排职责。
 */
export function useTrajectoryEarlierEntry(source: TrajectoryEarlierSource) {
  const { hasMore } = source.window;
  const { loadEarlier, loadingEarlier } = source;
  return useMemo(
    () => ({ hasEarlier: hasMore, loading: loadingEarlier, onLoad: loadEarlier }),
    [hasMore, loadEarlier, loadingEarlier],
  );
}
