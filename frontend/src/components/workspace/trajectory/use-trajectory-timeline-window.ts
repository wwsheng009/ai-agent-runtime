/**
 * 轨迹时间线窗口交互（P2-8：缩放/平移/框选）。
 *
 * 从 `trajectory-timeline.tsx` 抽出，让视图文件专注于渲染（P0-2 行数约束）：
 * - 窗口状态绑定 `axisKey`：item 集合或轴类型变化时自动回到全览（不残留旧区间）；
 * - 滚轮：默认以指针为锚点缩放；Shift+滚轮 / 横向滚轮平移。
 *   监听器必须原生非 passive（React 合成 wheel 挂在根节点上，无法 preventDefault）；
 * - 拖拽：框选区间并缩放到该区间；位移小于 `MIN_DRAG_RATIO` 视为点击，不改窗口；
 *   指针捕获**只在拖拽真正开始后**才设置——若在 pointerdown 就捕获，浏览器会把
 *   随后的 click 事件重定向到泳道容器，色块自身的 onClick 收不到（真实浏览器里
 *   表现为「点色块没反应」，jsdom 用例看不出来）；
 * - 键盘：←/→ 平移、+/- 缩放、Home/0 重置（泳道容器聚焦时生效）。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  buildTrajectoryTimelineAxis,
  clampTrajectoryWindow,
  fullTrajectoryWindow,
  isFullTrajectoryWindow,
  panTrajectoryWindow,
  trailingTrajectoryWindow,
  trajectoryWindowSpanRatio,
  zoomTrajectoryWindow,
  zoomTrajectoryWindowAtCenter,
  type TrajectoryTimelineAxis,
  type TrajectoryTimelineWindow,
} from "@/lib/trajectory/timeline-window";
import type { TrajectoryItem } from "@/lib/trajectory/types";

/** 拖拽框选的最小宽度（占泳道比例），更小视为点击。 */
const MIN_DRAG_RATIO = 0.03;
/** 滚轮缩放灵敏度（指数曲线，触控板与鼠标滚轮共用）。 */
export const WHEEL_ZOOM_SENSITIVITY = 0.0022;
/** 键盘/按钮平移步长（窗口跨度的比例）。 */
export const PAN_STEP_RATIO = 0.2;

/** 拖拽中的框选区间（泳道宽度比例，0..1）。 */
export type TrajectoryTimelineSelection = {
  startRatio: number;
  endRatio: number;
};

type WindowState = {
  axisKey: string;
  window: TrajectoryTimelineWindow;
};

export type TrajectoryTimelineWindowController = {
  /** 泳道容器引用：指针坐标 → 比例、滚轮监听、指针捕获都基于它。 */
  laneRef: React.RefObject<HTMLDivElement | null>;
  axis: TrajectoryTimelineAxis;
  window: TrajectoryTimelineWindow;
  selection: TrajectoryTimelineSelection | null;
  /** 是否处于缩放态（控制「平移/重置」按钮可用性）。 */
  zoomed: boolean;
  /** 当前窗口占全量跨度的百分比（展示缩放级别）。 */
  zoomPercent: number;
  handlePointerDown: (event: React.PointerEvent<HTMLDivElement>) => void;
  handlePointerMove: (event: React.PointerEvent<HTMLDivElement>) => void;
  handlePointerUp: (event: React.PointerEvent<HTMLDivElement>) => void;
  handlePointerCancel: () => void;
  handleZoomIn: () => void;
  handleZoomOut: () => void;
  handleReset: () => void;
  handlePanLeft: () => void;
  handlePanRight: () => void;
  /** 时间间隔预设：锚定轴终点回溯 `span` 毫秒（仅时间轴有意义）。 */
  applyTrailingSpan: (span: number) => void;
  handleKeyDown: (event: React.KeyboardEvent<HTMLDivElement>) => void;
};

/**
 * 时间轴窗口控制器。
 *
 * 注意：外部「清除区间筛选」通过父级 `key` 重挂载组件来复位（见 `trajectory-view.tsx`），
 * 而不是在 effect 里 setState —— 后者会触发 `react-hooks/set-state-in-effect`。
 */
export function useTrajectoryTimelineWindow(
  items: TrajectoryItem[],
): TrajectoryTimelineWindowController {
  const laneRef = useRef<HTMLDivElement | null>(null);
  const latestRef = useRef<{
    axis: TrajectoryTimelineAxis;
    window: TrajectoryTimelineWindow;
  } | null>(null);
  const dragRef = useRef<{
    pointerId: number;
    startRatio: number;
    moved: boolean;
  } | null>(null);
  const [selection, setSelection] = useState<TrajectoryTimelineSelection | null>(
    null,
  );
  const [windowState, setWindowState] = useState<WindowState | null>(null);

  const axis = useMemo(() => buildTrajectoryTimelineAxis(items), [items]);
  const axisKey = useMemo(
    () =>
      [
        axis.kind,
        axis.min,
        axis.max,
        items.length,
        items[0]?.id ?? "",
        items[items.length - 1]?.id ?? "",
      ].join("|"),
    [axis, items],
  );
  const full = useMemo(() => fullTrajectoryWindow(axis), [axis]);
  const timelineWindow = useMemo(
    () =>
      windowState && windowState.axisKey === axisKey
        ? clampTrajectoryWindow(axis, windowState.window)
        : full,
    [axis, axisKey, full, windowState],
  );

  const setWindow = useCallback(
    (next: TrajectoryTimelineWindow) => {
      setWindowState({ axisKey, window: clampTrajectoryWindow(axis, next) });
    },
    [axis, axisKey],
  );

  useEffect(() => {
    latestRef.current = { axis, window: timelineWindow };
  }, [axis, timelineWindow]);

  const ratioFromClientX = useCallback((clientX: number): number => {
    const element = laneRef.current;
    if (!element) {
      return 0;
    }
    const rect = element.getBoundingClientRect();
    if (!rect.width) {
      return 0;
    }
    return clampRatio((clientX - rect.left) / rect.width);
  }, []);

  // 滚轮缩放/平移：原生非 passive 监听（React 合成 wheel 在根节点是 passive，无法 preventDefault）。
  useEffect(() => {
    const element = laneRef.current;
    if (!element) {
      return;
    }
    const onWheel = (event: WheelEvent) => {
      const latest = latestRef.current;
      if (!latest) {
        return;
      }
      const panning =
        event.shiftKey || Math.abs(event.deltaX) > Math.abs(event.deltaY);
      if (panning) {
        const delta = event.shiftKey ? event.deltaY : event.deltaX;
        if (!delta) {
          return;
        }
        event.preventDefault();
        setWindow(
          panTrajectoryWindow(
            latest.axis,
            latest.window,
            delta > 0 ? PAN_STEP_RATIO : -PAN_STEP_RATIO,
          ),
        );
        return;
      }
      if (!event.deltaY) {
        return;
      }
      event.preventDefault();
      setWindow(
        zoomTrajectoryWindow(
          latest.axis,
          latest.window,
          Math.exp(-event.deltaY * WHEEL_ZOOM_SENSITIVITY),
          ratioFromClientX(event.clientX),
        ),
      );
    };
    element.addEventListener("wheel", onWheel, { passive: false });
    return () => element.removeEventListener("wheel", onWheel);
  }, [ratioFromClientX, setWindow]);

  const handlePointerDown = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      if (event.button !== 0) {
        return;
      }
      const ratio = ratioFromClientX(event.clientX);
      dragRef.current = {
        pointerId: event.pointerId,
        startRatio: ratio,
        moved: false,
      };
      setSelection({ startRatio: ratio, endRatio: ratio });
    },
    [ratioFromClientX],
  );

  const handlePointerMove = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      const drag = dragRef.current;
      if (!drag || drag.pointerId !== event.pointerId) {
        return;
      }
      const ratio = ratioFromClientX(event.clientX);
      if (!drag.moved && Math.abs(ratio - drag.startRatio) >= MIN_DRAG_RATIO) {
        drag.moved = true;
        // 超过阈值才算拖拽：此刻才捕获指针，之前保持 click 的原始目标（色块按钮）。
        capturePointer(event.currentTarget, event.pointerId);
      }
      setSelection({ startRatio: drag.startRatio, endRatio: ratio });
    },
    [ratioFromClientX],
  );

  const handlePointerUp = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      const drag = dragRef.current;
      const current = selection;
      dragRef.current = null;
      setSelection(null);
      releasePointer(event.currentTarget, event.pointerId);
      if (!drag || !current || !drag.moved) {
        return;
      }
      const from = Math.min(current.startRatio, current.endRatio);
      const to = Math.max(current.startRatio, current.endRatio);
      if (to - from < MIN_DRAG_RATIO) {
        return;
      }
      const span = timelineWindow.end - timelineWindow.start;
      setWindow({
        start: timelineWindow.start + span * from,
        end: timelineWindow.start + span * to,
      });
    },
    [selection, setWindow, timelineWindow],
  );

  const handlePointerCancel = useCallback(() => {
    dragRef.current = null;
    setSelection(null);
  }, []);

  const zoomed = !isFullTrajectoryWindow(axis, timelineWindow);
  const zoomPercent = Math.round(
    trajectoryWindowSpanRatio(axis, timelineWindow) * 100,
  );
  const handleZoomIn = useCallback(() => {
    setWindow(zoomTrajectoryWindowAtCenter(axis, timelineWindow, 1.6));
  }, [axis, setWindow, timelineWindow]);
  const handleZoomOut = useCallback(() => {
    setWindow(zoomTrajectoryWindowAtCenter(axis, timelineWindow, 1 / 1.6));
  }, [axis, setWindow, timelineWindow]);
  const handleReset = useCallback(() => {
    setWindowState(null);
  }, []);
  const handlePanLeft = useCallback(() => {
    setWindow(panTrajectoryWindow(axis, timelineWindow, -PAN_STEP_RATIO));
  }, [axis, setWindow, timelineWindow]);
  const handlePanRight = useCallback(() => {
    setWindow(panTrajectoryWindow(axis, timelineWindow, PAN_STEP_RATIO));
  }, [axis, setWindow, timelineWindow]);
  const applyTrailingSpan = useCallback(
    (span: number) => {
      setWindow(trailingTrajectoryWindow(axis, span));
    },
    [axis, setWindow],
  );
  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>) => {
      if (event.key === "ArrowLeft") {
        event.preventDefault();
        handlePanLeft();
        return;
      }
      if (event.key === "ArrowRight") {
        event.preventDefault();
        handlePanRight();
        return;
      }
      if (event.key === "+" || event.key === "=") {
        event.preventDefault();
        handleZoomIn();
        return;
      }
      if (event.key === "-") {
        event.preventDefault();
        handleZoomOut();
        return;
      }
      if (event.key === "Home" || event.key === "0") {
        event.preventDefault();
        handleReset();
      }
    },
    [handlePanLeft, handlePanRight, handleReset, handleZoomIn, handleZoomOut],
  );

  return {
    laneRef,
    axis,
    window: timelineWindow,
    selection,
    zoomed,
    zoomPercent,
    handlePointerDown,
    handlePointerMove,
    handlePointerUp,
    handlePointerCancel,
    handleZoomIn,
    handleZoomOut,
    handleReset,
    handlePanLeft,
    handlePanRight,
    applyTrailingSpan,
    handleKeyDown,
  };
}

function clampRatio(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(Math.max(value, 0), 1);
}

function capturePointer(element: Element, pointerId: number) {
  const target = element as Element & {
    setPointerCapture?: (id: number) => void;
  };
  try {
    target.setPointerCapture?.(pointerId);
  } catch {
    // jsdom / 旧浏览器没有指针捕获：拖拽仍可用（事件冒泡到泳道）。
  }
}

function releasePointer(element: Element, pointerId: number) {
  const target = element as Element & {
    releasePointerCapture?: (id: number) => void;
  };
  try {
    target.releasePointerCapture?.(pointerId);
  } catch {
    // 同上：忽略不支持指针捕获的环境。
  }
}
