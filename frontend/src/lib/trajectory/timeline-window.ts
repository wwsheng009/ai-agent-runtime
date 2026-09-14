/**
 * 轨迹时间线条状图的纯计算层（P2-2 优化）：轴构建 / 窗口缩放 / 分桶聚合 / 刻度。
 *
 * 设计要点：
 * - **轴**：item 带墙钟时间（`_event.timestamp` → `TrajectoryItem.at`）时用时间轴；
 *   缺失（如会话历史兜底投影）或时间跨度为零时退化为序号轴——两者都是数值域，
 *   缩放/平移/分桶共用同一套实现。
 * - **窗口**：`[start, end]` 为轴坐标区间，缩放/平移都在此域内做 clamp，
 *   保证不越界、不过度放大（`MIN_SPAN_RATIO`）。
 * - **分桶**：窗口内 item 按等宽桶聚合，避免了「651 条事件全屏堆叠」的不可读状态；
 *   渲染层在 item 稀疏时仍可逐条渲染（fidelity 优先）。
 * - **两种编码**：桶带 `kind`（主导**消息类型**：用户/助手/工具…）与 `status`
 *   （主导**运行状态**：失败/运行中/完成…），第一个时间轴用 kind 着色，
 *   状态另设泳道；两者都由本模块算出，组件只负责映射成颜色。
 * - **联动**：`trajectoryItemsInWindow` 把「图表窗口」翻译成明细列表集合，
 *   缩放/过滤后列表与图表看到的是同一批 item。
 *
 * 本模块不依赖 React/DOM，便于单测；组件只负责把结果映射成 DOM 与交互。
 */
import type {
  TrajectoryItem,
  TrajectoryItemKind,
  TrajectoryItemStatus,
} from "./types";

export type TrajectoryTimelineAxisKind = "time" | "ordinal";

export interface TrajectoryTimelineAxis {
  kind: TrajectoryTimelineAxisKind;
  /** 轴起点（time: epoch ms；ordinal: 下标）。 */
  min: number;
  /** 轴终点。 */
  max: number;
  /** 每个 item 的轴坐标（与 items 同序等长）。 */
  positions: number[];
  /** 至少两个 item 带有效时间且跨度 > 0 时为 true。 */
  hasTime: boolean;
}

export interface TrajectoryTimelineWindow {
  start: number;
  end: number;
}

export interface TrajectoryTimelineBucket {
  key: string;
  /** 窗口内归一化左边界（0..1）。 */
  left: number;
  /** 归一化宽度（0..1）。 */
  width: number;
  count: number;
  /** 主导**消息类型**（第一个时间轴的着色依据：用户/助手/工具…）。 */
  kind: TrajectoryItemKind;
  /** 主导状态（优先级 failed > running > canceled > pending > completed）。 */
  status: TrajectoryItemStatus;
  statusCounts: Partial<Record<TrajectoryItemStatus, number>>;
  kindCounts: Partial<Record<TrajectoryItemKind, number>>;
  /** 桶内轴坐标范围（tooltip / 刻度复用）。 */
  axisStart: number;
  axisEnd: number;
  /** 跳转锚点 = 桶内第一条 item。 */
  firstItemId: string;
  /** 桶内 item（轴顺序）。 */
  items: TrajectoryItem[];
}

export interface TrajectoryTimelineTick {
  value: number;
  /** 窗口内归一化位置（0..1）。 */
  ratio: number;
  label: string;
}

/** 最大放大倍数（窗口跨度最小占全轴的 1/200）。 */
export const MIN_SPAN_RATIO = 0.005;

/** 状态配色优先级：越靠前越"需要被看见"。 */
export const TRAJECTORY_STATUS_PRIORITY: readonly TrajectoryItemStatus[] = [
  "failed",
  "running",
  "canceled",
  "pending",
  "completed",
];

/**
 * 消息类型展示顺序（第一个时间轴的图例/主导类型判定都用它）。
 *
 * 前四位是用户最常关心的对话结构（用户消息 / 助手消息 / 工具 / 推理），
 * 其余编排类（规划/路由/观察/子 Agent/结果）靠后，系统事件垫底。
 */
export const TRAJECTORY_KIND_PRIORITY: readonly TrajectoryItemKind[] = [
  "user",
  "assistant",
  "tool",
  "reasoning",
  "planning",
  "orchestration",
  "route",
  "observation",
  "subagent",
  "result",
  "system",
];

const ALL_STATUSES: readonly TrajectoryItemStatus[] = [
  "pending",
  "running",
  "completed",
  "failed",
  "canceled",
];

const ALL_KINDS: readonly TrajectoryItemKind[] = TRAJECTORY_KIND_PRIORITY;

/** 空轴（items 为空时的占位，保证后续数学可用）。 */
function emptyAxis(): TrajectoryTimelineAxis {
  return { kind: "ordinal", min: 0, max: 1, positions: [], hasTime: false };
}

/**
 * 构建时间线轴：优先墙钟时间，缺失时退化序号轴。
 *
 * 时间轴上个别 item 缺时间（例如混合来源：live 帧有时间、历史兜底行没有）时，
 * 用相邻已知时间按序线性插值，避免这些 item 全部堆到轴起点。
 */
export function buildTrajectoryTimelineAxis(
  items: TrajectoryItem[],
): TrajectoryTimelineAxis {
  if (items.length === 0) {
    return emptyAxis();
  }

  const times = items.map((item) =>
    typeof item.at === "number" && Number.isFinite(item.at) ? item.at : undefined,
  );
  const known = times.filter((value): value is number => value !== undefined);
  const knownMin = known.length > 0 ? Math.min(...known) : 0;
  const knownMax = known.length > 0 ? Math.max(...known) : 0;
  const hasTime = known.length >= 2 && knownMax > knownMin;

  if (!hasTime) {
    const positions = items.map((_, index) => index);
    return {
      kind: "ordinal",
      min: 0,
      max: Math.max(1, items.length - 1),
      positions,
      hasTime: false,
    };
  }

  const positions = fillMissingTimes(times);
  const min = positions.reduce((acc, value) => Math.min(acc, value), positions[0]);
  const max = positions.reduce((acc, value) => Math.max(acc, value), positions[0]);
  return {
    kind: "time",
    min,
    max: max > min ? max : min + 1,
    positions,
    hasTime: true,
  };
}

/** 缺失时间填充：头部/尾部沿用最近已知时间，中间按相邻已知时间线性插值。 */
function fillMissingTimes(times: Array<number | undefined>): number[] {
  const positions = [...times];
  const firstKnownIndex = positions.findIndex((value) => value !== undefined);
  if (firstKnownIndex < 0) {
    return positions.map((_, index) => index);
  }
  const lastKnownIndex =
    positions.length -
    1 -
    [...positions].reverse().findIndex((value) => value !== undefined);

  for (let index = 0; index < firstKnownIndex; index += 1) {
    positions[index] = positions[firstKnownIndex];
  }
  for (let index = positions.length - 1; index > lastKnownIndex; index -= 1) {
    positions[index] = positions[lastKnownIndex];
  }

  let previousIndex = firstKnownIndex;
  for (let index = firstKnownIndex + 1; index <= lastKnownIndex; index += 1) {
    if (positions[index] === undefined) {
      continue;
    }
    if (index - previousIndex > 1) {
      const from = positions[previousIndex] as number;
      const to = positions[index] as number;
      const steps = index - previousIndex;
      for (let offset = 1; offset < steps; offset += 1) {
        positions[previousIndex + offset] = from + ((to - from) * offset) / steps;
      }
    }
    previousIndex = index;
  }
  return positions as number[];
}

/** 全轴窗口。 */
export function fullTrajectoryWindow(
  axis: TrajectoryTimelineAxis,
): TrajectoryTimelineWindow {
  return { start: axis.min, end: axis.max };
}

/**
 * 尾部窗口：以轴终点为锚点回溯 `span`（time: 毫秒；ordinal: 下标差）。
 *
 * 「最近 1m / 5m / 15m / 1h」这类**时间间隔预设**用它取值：会话最后一段
 * 始终留在视野内。`span` 非法或超过全轴跨度时退化为全览（clamp 负责）。
 */
export function trailingTrajectoryWindow(
  axis: TrajectoryTimelineAxis,
  span: number,
): TrajectoryTimelineWindow {
  if (!Number.isFinite(span) || span <= 0) {
    return fullTrajectoryWindow(axis);
  }
  return clampTrajectoryWindow(axis, { start: axis.max - span, end: axis.max });
}

/** 窗口跨度占全轴比例（0..1）。 */
export function trajectoryWindowSpanRatio(
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
): number {
  const full = Math.max(1e-9, axis.max - axis.min);
  return clamp((window.end - window.start) / full, 0, 1);
}

/** 是否处于全览状态（无缩放）。 */
export function isFullTrajectoryWindow(
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
): boolean {
  return (
    window.start <= axis.min + 1e-9 && window.end >= axis.max - 1e-9
  );
}

/** 平移窗口（`deltaRatio` 为窗口跨度的比例，正数向右）。 */
export function panTrajectoryWindow(
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
  deltaRatio: number,
): TrajectoryTimelineWindow {
  const span = window.end - window.start;
  return clampTrajectoryWindow(axis, {
    start: window.start + span * deltaRatio,
    end: window.end + span * deltaRatio,
  });
}

/**
 * 缩放窗口：`factor > 1` 放大，`anchorRatio`（0..1）为窗口内锚点
 * （鼠标位置在该比例处保持不动）。
 */
export function zoomTrajectoryWindow(
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
  factor: number,
  anchorRatio: number,
): TrajectoryTimelineWindow {
  if (!Number.isFinite(factor) || factor <= 0) {
    return clampTrajectoryWindow(axis, window);
  }
  const full = Math.max(1e-9, axis.max - axis.min);
  const span = Math.max(1e-9, window.end - window.start);
  const minSpan = full * MIN_SPAN_RATIO;
  const nextSpan = clamp(span / factor, Math.min(minSpan, full), full);
  const anchor = window.start + span * clamp(anchorRatio, 0, 1);
  const start = anchor - (anchor - window.start) * (nextSpan / span);
  return clampTrajectoryWindow(axis, { start, end: start + nextSpan });
}

/** 以窗口中心缩放（按钮/键盘用）。 */
export function zoomTrajectoryWindowAtCenter(
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
  factor: number,
): TrajectoryTimelineWindow {
  return zoomTrajectoryWindow(axis, window, factor, 0.5);
}

/** 夹紧窗口：限制跨度上下界并在轴范围内平移对齐（不缩小超出部分之外的内容）。 */
export function clampTrajectoryWindow(
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
): TrajectoryTimelineWindow {
  const full = Math.max(1e-9, axis.max - axis.min);
  const minSpan = Math.min(full * MIN_SPAN_RATIO, full);
  const span = clamp(window.end - window.start, minSpan, full);
  let start = window.start;
  if (!Number.isFinite(start)) {
    start = axis.min;
  }
  let end = start + span;
  if (start < axis.min) {
    start = axis.min;
    end = start + span;
  }
  if (end > axis.max) {
    end = axis.max;
    start = end - span;
  }
  return { start, end };
}

/** 轴坐标 → 窗口内归一化位置（0..1，可越界）。 */
export function trajectoryAxisRatio(
  window: TrajectoryTimelineWindow,
  value: number,
): number {
  const span = Math.max(1e-9, window.end - window.start);
  return (value - window.start) / span;
}

/**
 * 落在窗口内的 item（**图表 → 明细列表同步过滤**的唯一入口）。
 *
 * 调用方必须保证 `items` 与构建 `axis` 时的数组同源同序（长度一致），
 * 否则下标对不上；全览窗口天然返回全部 item。
 */
export function trajectoryItemsInWindow(
  items: TrajectoryItem[],
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
): TrajectoryItem[] {
  const result: TrajectoryItem[] = [];
  for (let index = 0; index < items.length; index += 1) {
    const item = items[index];
    const position = axis.positions[index];
    if (item === undefined || position === undefined) {
      continue;
    }
    if (position < window.start || position > window.end) {
      continue;
    }
    result.push(item);
  }
  return result;
}

/**
 * 窗口内分桶聚合：把 `items` 按等宽桶聚合，返回可见桶（桶内为空则跳过）。
 *
 * - `positions` 必须与 `items` 等长（`buildTrajectoryTimelineAxis().positions`）；
 * - 桶宽度恒为 `1 / maxBuckets`（空桶不渲染 → 时间轴上的空档一眼可见）。
 */
export function bucketTrajectoryItems(
  items: TrajectoryItem[],
  positions: number[],
  window: TrajectoryTimelineWindow,
  maxBuckets: number,
): TrajectoryTimelineBucket[] {
  const buckets = Math.max(1, Math.floor(maxBuckets));
  const span = Math.max(1e-9, window.end - window.start);
  const bucketsByIndex = new Map<number, TrajectoryTimelineBucket>();

  for (let index = 0; index < items.length; index += 1) {
    const item = items[index];
    const position = positions[index];
    if (item === undefined || position === undefined) {
      continue;
    }
    if (position < window.start || position > window.end) {
      continue;
    }
    const ratio = (position - window.start) / span;
    const bucketIndex = Math.min(buckets - 1, Math.max(0, Math.floor(ratio * buckets)));
    let bucket = bucketsByIndex.get(bucketIndex);
    if (!bucket) {
      bucket = {
        key: `bucket-${bucketIndex}`,
        left: bucketIndex / buckets,
        width: 1 / buckets,
        count: 0,
        kind: "assistant",
        status: "completed",
        statusCounts: {},
        kindCounts: {},
        axisStart: window.start + (bucketIndex / buckets) * span,
        axisEnd: window.start + ((bucketIndex + 1) / buckets) * span,
        firstItemId: item.id,
        items: [],
      };
      bucketsByIndex.set(bucketIndex, bucket);
    }
    bucket.items.push(item);
    bucket.count += 1;
    bucket.statusCounts[item.status] = (bucket.statusCounts[item.status] ?? 0) + 1;
    bucket.kindCounts[item.kind] = (bucket.kindCounts[item.kind] ?? 0) + 1;
  }

  const result = [...bucketsByIndex.values()].sort((left, right) => left.left - right.left);
  for (const bucket of result) {
    bucket.kind = dominantTrajectoryKind(bucket.kindCounts);
    bucket.status = dominantTrajectoryStatus(bucket.statusCounts);
  }
  return result;
}

/** 主导消息类型：按展示优先级取第一个非零类型；空集合按 assistant 兜底。 */
export function dominantTrajectoryKind(
  counts: Partial<Record<TrajectoryItemKind, number>>,
): TrajectoryItemKind {
  for (const kind of TRAJECTORY_KIND_PRIORITY) {
    if ((counts[kind] ?? 0) > 0) {
      return kind;
    }
  }
  return "assistant";
}

/** 统计各消息类型 item 数（第一个时间轴的图例与类型筛选提示复用）。 */
export function trajectoryKindCounts(
  items: TrajectoryItem[],
): Record<TrajectoryItemKind, number> {
  const counts = {} as Record<TrajectoryItemKind, number>;
  for (const kind of ALL_KINDS) {
    counts[kind] = 0;
  }
  for (const item of items) {
    counts[item.kind] = (counts[item.kind] ?? 0) + 1;
  }
  return counts;
}

/** 主导状态：按优先级取第一个非零状态；空集合按 completed 兜底。 */
export function dominantTrajectoryStatus(
  counts: Partial<Record<TrajectoryItemStatus, number>>,
): TrajectoryItemStatus {
  for (const status of TRAJECTORY_STATUS_PRIORITY) {
    if ((counts[status] ?? 0) > 0) {
      return status;
    }
  }
  return "completed";
}

/** 统计各状态 item 数（图例与筛选提示复用）。 */
export function trajectoryStatusCounts(
  items: TrajectoryItem[],
): Record<TrajectoryItemStatus, number> {
  const counts = {} as Record<TrajectoryItemStatus, number>;
  for (const status of ALL_STATUSES) {
    counts[status] = 0;
  }
  for (const item of items) {
    counts[item.status] = (counts[item.status] ?? 0) + 1;
  }
  return counts;
}

/** 轴刻度：等分窗口 `count` 段，返回 `count + 1` 个刻度。 */
export function trajectoryTimelineTicks(
  axis: TrajectoryTimelineAxis,
  window: TrajectoryTimelineWindow,
  items: TrajectoryItem[],
  count = 4,
): TrajectoryTimelineTick[] {
  const segments = Math.max(1, Math.floor(count));
  const span = window.end - window.start;
  const ticks: TrajectoryTimelineTick[] = [];
  for (let index = 0; index <= segments; index += 1) {
    const ratio = index / segments;
    const value = window.start + span * ratio;
    ticks.push({ value, ratio, label: formatTrajectoryAxisTick(axis, value, items) });
  }
  return ticks;
}

/** 刻度文案：时间轴 → 时钟（跨天带日期）；序号轴 → `#seq`。 */
export function formatTrajectoryAxisTick(
  axis: TrajectoryTimelineAxis,
  value: number,
  items: TrajectoryItem[],
): string {
  if (axis.kind === "ordinal") {
    const index = Math.min(
      items.length - 1,
      Math.max(0, Math.round(value)),
    );
    const seq = items[index]?.seq;
    return seq === undefined ? "" : `#${seq}`;
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const withDate = axis.max - axis.min > 24 * 60 * 60 * 1000;
  const pad = (input: number) => String(input).padStart(2, "0");
  const clock = `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
  if (!withDate) {
    return clock;
  }
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${clock}`;
}

/** 窗口跨度（毫秒或下标差，供 UI 展示缩放级别）。 */
export function trajectoryWindowSpan(
  window: TrajectoryTimelineWindow,
): number {
  return Math.max(0, window.end - window.start);
}

/** 人类可读的跨度文案（时间轴用；序号轴返回空串）。 */
export function formatTrajectoryDuration(spanMs: number): string {
  if (!Number.isFinite(spanMs) || spanMs <= 0) {
    return "0s";
  }
  const totalSeconds = Math.round(spanMs / 1000);
  if (totalSeconds < 60) {
    return `${totalSeconds}s`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) {
    return seconds > 0 ? `${minutes}m${seconds}s` : `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  const restMinutes = minutes % 60;
  return restMinutes > 0 ? `${hours}h${restMinutes}m` : `${hours}h`;
}

function clamp(value: number, min: number, max: number): number {
  if (!Number.isFinite(value)) {
    return min;
  }
  return Math.min(Math.max(value, min), max);
}
