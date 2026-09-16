// 右侧栏宽度拖拽手柄（WAI-ARIA Window Splitter 模式），P0-2。
//
// 设计规则（docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md §4.2.1）：
// - 命中区：容器**内部**左边缘 6px（`w-1.5`），`::before` 再向栏内扩到 12px；默认不可见，
//   hover / focus-visible / 拖拽中显示 2px 高亮线（不改变容器 border-l 的视觉厚度）。
// - 指针流程：`pointerdown` 先 `setPointerCapture`（先例 use-trajectory-timeline-window.ts:332-341），
//   `pointermove` **rAF 节流一帧一次**并**直接写 DOM**（`--right-rail-width`），拖拽过程零 setState；
//   `pointerup` / `pointercancel` → 释放捕获 → 清理 body 样式与内容层 pointer-events → **一次性**提交。
// - 键盘：←/→ = ±16px（Shift = ±64px，方向即手柄移动方向：← 变宽 / → 变窄），Home/End = min/max，
//   Enter / 双击 = 回 auto；所有入口都只提交一次（走 onCommit / onReset）；clamp 后与按键
//   方向相反时按边界处理（内容型面 auto 288px < 手动下界 320px，不能「按 → 反而变宽」）。
// - 清理：卸载时恢复 body 样式并取消未执行的 rAF，避免页面卡在 `userSelect: none`。
//
// 回归红线：本组件不参与右栏开合语义（topbar 开关不变），也不改变关闭右栏时的两列网格。

import {
  useCallback,
  useEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from "react";

import { clampRailWidth } from "@/lib/layout/rail-width";

/** 键盘步进：普通 16px，按住 Shift 64px。 */
const KEYBOARD_STEP_PX = 16;
const KEYBOARD_STEP_COARSE_PX = 64;

type RailResizeHandleProps = {
  /** 右栏容器：CSS 变量写在它身上（workspace-shell 的网格列宽读同一个变量）。 */
  railRef: RefObject<HTMLElement | null>;
  /** 内容层：拖拽中临时 `pointer-events: none`，阻断原生 drag 与文本选区。 */
  contentRef?: RefObject<HTMLElement | null>;
  viewportWidth: number;
  /** 当前宽度（px，与容器 CSS 变量同口径）。 */
  value: number;
  minWidthPx: number;
  maxWidthPx: number;
  /** 提交一次（写 settings：mode=manual）。 */
  onCommit: (px: number) => void;
  /** 回到自适应（Enter / 双击）。 */
  onReset: () => void;
  /** aria-label（i18n 由调用方提供）。 */
  label: string;
};

type DragState = {
  pointerId: number | undefined;
  startX: number;
  startWidth: number;
  viewportWidth: number;
  /** 是否发生过位移：轻点（无位移）不写设置，避免误把 auto 切成 manual。 */
  moving: boolean;
};

function capturePointer(element: Element, pointerId: number | undefined) {
  const target = element as Element & {
    setPointerCapture?: (id: number | undefined) => void;
  };
  try {
    target.setPointerCapture?.(pointerId);
  } catch {
    // jsdom / 旧浏览器不支持指针捕获：拖拽仍可用（事件落在手柄自身上）。
  }
}

function releasePointer(element: Element, pointerId: number | undefined) {
  const target = element as Element & {
    releasePointerCapture?: (id: number | undefined) => void;
  };
  try {
    target.releasePointerCapture?.(pointerId);
  } catch {
    // 同上：忽略不支持指针捕获的环境。
  }
}

export function RailResizeHandle({
  railRef,
  contentRef,
  viewportWidth,
  value,
  minWidthPx,
  maxWidthPx,
  onCommit,
  onReset,
  label,
}: RailResizeHandleProps) {
  const handleRef = useRef<HTMLDivElement | null>(null);
  const dragRef = useRef<DragState | null>(null);
  const frameRef = useRef<number | null>(null);
  const pendingXRef = useRef<number | null>(null);
  const bodyStyleRef = useRef<{ userSelect: string; cursor: string } | null>(
    null,
  );

  /** 拖拽期唯一的写入口：直接改 CSS 变量，不触发 React 渲染。 */
  const writeWidth = useCallback(
    (px: number) => {
      railRef.current?.style.setProperty("--right-rail-width", `${px}px`);
    },
    [railRef],
  );

  const endBodyLock = useCallback(() => {
    const saved = bodyStyleRef.current;
    bodyStyleRef.current = null;
    if (!saved || typeof document === "undefined") {
      return;
    }

    document.body.style.userSelect = saved.userSelect;
    document.body.style.cursor = saved.cursor;

    const content = contentRef?.current;
    if (content) {
      content.style.setProperty("pointer-events", "");
    }
  }, [contentRef]);

  const beginBodyLock = useCallback(() => {
    if (typeof document === "undefined") {
      return;
    }

    const body = document.body;
    bodyStyleRef.current = {
      userSelect: body.style.userSelect,
      cursor: body.style.cursor,
    };
    body.style.userSelect = "none";
    body.style.cursor = "col-resize";

    const content = contentRef?.current;
    if (content) {
      content.style.setProperty("pointer-events", "none");
    }
  }, [contentRef]);

  /** 计算一帧的目标宽度并写入 DOM（rAF 回调与收尾冲帧共用）。 */
  const applyPendingFrame = useCallback(() => {
    frameRef.current = null;
    const drag = dragRef.current;
    const clientX = pendingXRef.current;
    if (!drag || clientX === null || !drag.moving) {
      return;
    }

    writeWidth(
      clampRailWidth(
        drag.startWidth - (clientX - drag.startX),
        drag.viewportWidth,
      ),
    );
  }, [writeWidth]);

  const scheduleFrame = useCallback(
    (clientX: number) => {
      pendingXRef.current = clientX;
      if (frameRef.current !== null) {
        return;
      }

      if (typeof requestAnimationFrame !== "function") {
        applyPendingFrame();
        return;
      }

      frameRef.current = requestAnimationFrame(applyPendingFrame);
    },
    [applyPendingFrame],
  );

  function cancelPendingFrame() {
    if (frameRef.current !== null && typeof cancelAnimationFrame === "function") {
      cancelAnimationFrame(frameRef.current);
    }
    frameRef.current = null;
  }

  useEffect(
    () => () => {
      cancelPendingFrame();
      endBodyLock();
    },
    [endBodyLock],
  );

  function handlePointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    const rail = railRef.current;
    if (!rail || (event.pointerType === "mouse" && event.button !== 0)) {
      return;
    }

    event.preventDefault();

    // 以容器**实际**宽度为起点（覆盖层模式下宽度会被 min(…, 92vw) 收窄）。
    const measured = Math.round(rail.getBoundingClientRect().width);
    const startWidth = Number.isFinite(measured) && measured > 0 ? measured : value;

    dragRef.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startWidth,
      viewportWidth,
      moving: false,
    };
    pendingXRef.current = event.clientX;
    capturePointer(event.currentTarget, event.pointerId);
    handleRef.current?.setAttribute("data-dragging", "true");
    beginBodyLock();
  }

  function handlePointerMove(event: ReactPointerEvent<HTMLDivElement>) {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== event.pointerId) {
      return;
    }

    event.preventDefault();
    if (event.clientX !== drag.startX) {
      drag.moving = true;
    }
    scheduleFrame(event.clientX);
  }

  function handlePointerEnd(event: ReactPointerEvent<HTMLDivElement>) {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== event.pointerId) {
      return;
    }

    // 冲掉未执行的一帧：提交值与最后一帧 DOM 保持同口径。
    const clientX = pendingXRef.current ?? event.clientX;
    const next = clampRailWidth(
      drag.startWidth - (clientX - drag.startX),
      drag.viewportWidth,
    );
    cancelPendingFrame();
    dragRef.current = null;
    pendingXRef.current = null;
    releasePointer(event.currentTarget, event.pointerId);
    handleRef.current?.removeAttribute("data-dragging");
    endBodyLock();

    if (!drag.moving) {
      return;
    }

    writeWidth(next);
    onCommit(next);
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key === "Enter") {
      event.preventDefault();
      onReset();
      return;
    }

    const step = event.shiftKey ? KEYBOARD_STEP_COARSE_PX : KEYBOARD_STEP_PX;
    let next: number | null = null;

    if (event.key === "ArrowLeft") {
      // 手柄向左移动 = 右栏变宽（与拖拽方向一致）。
      next = clampRailWidth(value + step, viewportWidth);
    } else if (event.key === "ArrowRight") {
      next = clampRailWidth(value - step, viewportWidth);
    } else if (event.key === "Home") {
      next = minWidthPx;
    } else if (event.key === "End") {
      next = maxWidthPx;
    }

    if (next === null) {
      return;
    }

    event.preventDefault();
    if (next === value) {
      // 已在边界：不写设置，避免「按一下方向键就退出 auto」。
      return;
    }

    // 方向保护：auto 兜底值（内容型面 288px）低于手动下界 320px，clamp 可能把「变窄」抬回
    // 320px；这类与意图相反的结果一律视为边界，不提交、不退出 auto。
    if (
      (event.key === "ArrowRight" && next > value) ||
      (event.key === "ArrowLeft" && next < value)
    ) {
      return;
    }

    writeWidth(next);
    onCommit(next);
  }

  return (
    <div
      ref={handleRef}
      aria-label={label}
      aria-orientation="vertical"
      aria-valuemax={maxWidthPx}
      aria-valuemin={minWidthPx}
      aria-valuenow={Math.round(value)}
      className="group absolute inset-y-0 left-0 z-10 w-1.5 cursor-col-resize touch-none select-none before:absolute before:inset-y-0 before:left-0 before:w-3 before:content-['']"
      data-testid="right-rail-resize-handle"
      onDoubleClick={onReset}
      onKeyDown={handleKeyDown}
      onPointerCancel={handlePointerEnd}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={handlePointerEnd}
      role="separator"
      tabIndex={0}
    >
      <span
        aria-hidden="true"
        className="pointer-events-none absolute inset-y-0 left-0 w-0.5 rounded-full bg-accent-cyan/0 opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-hover:bg-accent-cyan/40 group-focus-visible:opacity-100 group-focus-visible:bg-accent-cyan/70 group-data-[dragging=true]:opacity-100 group-data-[dragging=true]:bg-accent-cyan/70"
      />
    </div>
  );
}
