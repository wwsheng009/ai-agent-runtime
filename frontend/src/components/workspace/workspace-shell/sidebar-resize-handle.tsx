import {
  useCallback,
  useEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from "react";

import {
  clampWorkspaceSidebarWidth,
  WORKSPACE_SIDEBAR_WIDTH_MIN_PX,
} from "@/lib/layout/sidebar-width";

const KEYBOARD_STEP_PX = 16;
const KEYBOARD_STEP_COARSE_PX = 64;

type DragState = {
  pointerId: number;
  startX: number;
  startWidth: number;
  viewportWidth: number;
  reservedRightRailWidth: number;
  moving: boolean;
};

type SidebarResizeHandleProps = {
  containerRef: RefObject<HTMLDivElement | null>;
  label: string;
  maxWidthPx: number;
  onCommit: (widthPx: number) => void;
  onReset: () => void;
  reservedRightRailWidth: number;
  value: number;
  viewportWidth: number;
};

function setPointerCapture(element: HTMLElement, pointerId: number) {
  try {
    element.setPointerCapture(pointerId);
  } catch {
    // jsdom / older browsers may not implement pointer capture; keep mouse fallback.
  }
}

function releasePointerCapture(element: HTMLElement, pointerId: number) {
  try {
    element.releasePointerCapture(pointerId);
  } catch {
    // Ignore unsupported or already-released pointer capture.
  }
}

export function SidebarResizeHandle({
  containerRef,
  label,
  maxWidthPx,
  onCommit,
  onReset,
  reservedRightRailWidth,
  value,
  viewportWidth,
}: SidebarResizeHandleProps) {
  const handleRef = useRef<HTMLDivElement | null>(null);
  const dragRef = useRef<DragState | null>(null);
  const bodyStyleRef = useRef<{ userSelect: string; cursor: string } | null>(
    null,
  );

  const writeWidth = useCallback(
    (widthPx: number) => {
      containerRef.current?.style.setProperty(
        "--workspace-sidebar-width",
        `${widthPx}px`,
      );
      handleRef.current?.setAttribute("aria-valuenow", String(widthPx));
    },
    [containerRef],
  );

  const restoreBodyStyle = useCallback(() => {
    const previous = bodyStyleRef.current;
    bodyStyleRef.current = null;
    if (!previous || typeof document === "undefined") {
      return;
    }
    document.body.style.userSelect = previous.userSelect;
    document.body.style.cursor = previous.cursor;
  }, []);

  useEffect(() => restoreBodyStyle, [restoreBodyStyle]);

  function handlePointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    if (event.pointerType === "mouse" && event.button !== 0) {
      return;
    }
    event.preventDefault();

    const measured = Math.round(
      containerRef.current
        ?.querySelector("aside[aria-label]")
        ?.getBoundingClientRect().width ?? value,
    );
    const startWidth = measured > 0 ? measured : value;
    dragRef.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startWidth,
      viewportWidth,
      reservedRightRailWidth,
      moving: false,
    };
    setPointerCapture(event.currentTarget, event.pointerId);
    event.currentTarget.setAttribute("data-dragging", "true");
    if (typeof document !== "undefined") {
      bodyStyleRef.current = {
        userSelect: document.body.style.userSelect,
        cursor: document.body.style.cursor,
      };
      document.body.style.userSelect = "none";
      document.body.style.cursor = "col-resize";
    }
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
    if (drag.moving) {
      writeWidth(
        clampWorkspaceSidebarWidth(
          drag.startWidth + event.clientX - drag.startX,
          drag.viewportWidth,
          drag.reservedRightRailWidth,
        ),
      );
    }
  }

  function handlePointerEnd(event: ReactPointerEvent<HTMLDivElement>) {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== event.pointerId) {
      return;
    }
    dragRef.current = null;
    releasePointerCapture(event.currentTarget, event.pointerId);
    event.currentTarget.removeAttribute("data-dragging");
    restoreBodyStyle();
    if (!drag.moving) {
      return;
    }

    const next = clampWorkspaceSidebarWidth(
      drag.startWidth + event.clientX - drag.startX,
      drag.viewportWidth,
      drag.reservedRightRailWidth,
    );
    writeWidth(next);
    onCommit(next);
  }

  function commitStep(delta: number) {
    const next = clampWorkspaceSidebarWidth(
      value + delta,
      viewportWidth,
      reservedRightRailWidth,
    );
    if (next !== value) {
      writeWidth(next);
      onCommit(next);
    }
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key === "Enter") {
      event.preventDefault();
      onReset();
      return;
    }

    const step = event.shiftKey ? KEYBOARD_STEP_COARSE_PX : KEYBOARD_STEP_PX;
    if (event.key === "ArrowLeft") {
      event.preventDefault();
      commitStep(-step);
    } else if (event.key === "ArrowRight") {
      event.preventDefault();
      commitStep(step);
    } else if (event.key === "Home") {
      event.preventDefault();
      commitStep(-maxWidthPx);
    } else if (event.key === "End") {
      event.preventDefault();
      commitStep(maxWidthPx);
    }
  }

  return (
    <div
      className="absolute inset-y-0 z-20 hidden w-3 xl:block"
      data-testid="left-sidebar-resize-control"
      style={{ left: "var(--workspace-sidebar-width)" }}
    >
      <div
        ref={handleRef}
        aria-label={label}
        aria-orientation="vertical"
        aria-valuemax={maxWidthPx}
        aria-valuemin={WORKSPACE_SIDEBAR_WIDTH_MIN_PX}
        aria-valuenow={Math.round(value)}
        className="group absolute inset-y-0 left-0 w-3 cursor-col-resize touch-none select-none before:absolute before:inset-y-0 before:left-0 before:w-3 before:content-['']"
        data-testid="left-sidebar-resize-handle"
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
    </div>
  );
}
