/**
 * 增量搜索索引（P2-7）。
 *
 * - terms 倒排：term → 命中的 item id 集合（搜索为 AND 语义）；
 * - 版本签名：items 集合变化判定（id + revision 组合），索引重建节流；
 * - 组件接入：流式期间每帧签名变化 → 节流 3s 重建；节流窗口内搜索
 *   由调用方回退到线性过滤（保证结果正确，索引仅为性能优化）。
 */
import { useEffect, useMemo, useRef, useState } from "react";

import type { TrajectoryItem } from "@/lib/trajectory/types";

import { trajectoryItemText } from "./trajectory-view-shared";

const TERM_PATTERN = /[^\p{L}\p{N}_-]+/u;

/** 搜索词切分：小写化 + 非字母/数字/下划线/连字符分隔。 */
export function tokenizeTrajectoryText(text: string): string[] {
  return text
    .toLowerCase()
    .split(TERM_PATTERN)
    .filter((token) => token.length > 0);
}

export interface TrajectorySearchIndex {
  /** items 集合版本签名（重建判定）。 */
  signature: string;
  /** term → 命中的 item id 集合。 */
  terms: Map<string, Set<string>>;
}

/** items 版本签名：长度 + 每项 id:updatedAt（updatedAt 随每次 upsert 单调递增，等价 revision 语义）。 */
export function trajectorySearchSignature(items: TrajectoryItem[]): string {
  return `${items.length}:${items
    .map((item) => `${item.id}:${item.updatedAt}`)
    .join(",")}`;
}

export function buildTrajectorySearchIndex(
  items: TrajectoryItem[],
): TrajectorySearchIndex {
  const terms = new Map<string, Set<string>>();
  for (const item of items) {
    for (const term of tokenizeTrajectoryText(trajectoryItemText(item))) {
      let ids = terms.get(term);
      if (!ids) {
        ids = new Set();
        terms.set(term, ids);
      }
      ids.add(item.id);
    }
  }
  return { signature: trajectorySearchSignature(items), terms };
}

/**
 * AND 语义搜索。返回命中 item id 集合；
 * 空查询返回 null（调用方直接返回全量过滤结果）。
 */
export function searchTrajectoryIndex(
  index: TrajectorySearchIndex,
  query: string,
): Set<string> | null {
  const terms = tokenizeTrajectoryText(query);
  if (terms.length === 0) {
    return null;
  }
  const firstIds = index.terms.get(terms[0]);
  if (!firstIds) {
    return new Set();
  }
  const hits = new Set<string>();
  for (const id of firstIds) {
    if (terms.every((term) => index.terms.get(term)?.has(id))) {
      hits.add(id);
    }
  }
  return hits;
}

/**
 * 增量索引 hook：items 变化后延迟 rebuildDelayMs 重建（流式期间每帧
 * 变化会重置定时器，变化停止后重建；索引滞后期间调用方用线性回退）。
 */
export function useTrajectorySearchIndex(
  items: TrajectoryItem[],
  rebuildDelayMs = 3000,
): TrajectorySearchIndex {
  const [index, setIndex] = useState<TrajectorySearchIndex>(() =>
    buildTrajectorySearchIndex(items),
  );
  const indexRef = useRef(index);
  const currentSignature = useMemo(
    () => trajectorySearchSignature(items),
    [items],
  );

  useEffect(() => {
    if (indexRef.current.signature === currentSignature) {
      return;
    }
    const timer = window.setTimeout(() => {
      const rebuilt = buildTrajectorySearchIndex(items);
      indexRef.current = rebuilt;
      setIndex(rebuilt);
    }, rebuildDelayMs);
    return () => window.clearTimeout(timer);
  }, [currentSignature, items, rebuildDelayMs]);

  return index;
}
