// 工作台右侧栏：单一可折叠面板，内含「条目 / 计划 / 还原 / 会话用量 / 文件 / Git 变更」页签。
//
// P0-2 新增：可变宽度（拖拽手柄 + 键盘 + 设置页入口）与 `<xl` 覆盖层降级。
// 设计规则（docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md §4.2）：
// - 宽度来自 `railWidth` 控制器（workspace-shell 用 `use-right-rail-width` 计算，唯一出口
//   `lib/layout/rail-width.ts`），本组件只把它写成 CSS 变量 + 挂手柄，不做任何宽度算术。
// - 激活面上抛：`ArtifactPanel` 的 `onActiveSurfaceChange` 回调把当前面报给 shell，shell 据此
//   按 `widthClass` 重算 auto 宽度；未接线时按内容型面兜底（288px），UI 照常工作不白屏。
// - `<xl`（或主区放不下最小右栏）且当前面是宽内容面（files/git）→ 覆盖层抽屉 + 遮罩 + Esc 关闭；
//   其它面在 `<xl` 仍沿用既有「不渲染右栏列」策略。
//
// 回归红线：① 关闭右栏不渲染（不占位）；② 新建会话不渲染；③ 面板抛错时容器仍在；
// ④ auto + 内容型面 = 288px（由 rail-width 保证，本组件不覆盖）。

import { type TFunction } from "i18next";
import {
  Suspense,
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";

import { PanelErrorBoundary } from "@/components/errors/boundaries";
import {
  type WorkspacePanelSurfaceId,
  type WorkspacePanelThreadRelation,
} from "@/components/workspace/panel-registry";
import {
  getThreadRelationKind,
  getThreadTransportKind,
} from "@/components/workspace/workspace-shell-shared";
import {
  resolveRailSurfaceWidthClass,
  type RightRailWidthController,
} from "@/hooks/workspace/use-right-rail-width";

import { ArtifactPanel, ArtifactPanelFallback } from "./lazy-surfaces";
import { RailResizeHandle } from "./rail-resize-handle";
import { type WorkspaceShellProps } from "./types";

type WorkspaceRightRailSectionProps = Pick<
  WorkspaceShellProps,
  | "isResponding"
  | "selectedArtifactId"
  | "selectedThread"
> & {
  handleOpenArtifact: (artifactId: string) => void;
  isNewThread: boolean;
  /** 合并面板的开合：由顶栏开关控制；关闭时整列不占位。 */
  rightRailOpen: boolean;
  t: TFunction<"workspace">;
  /** 宽度控制器（缺省值仅用于独立挂载/测试：auto + 内容型面 288px）。 */
  railWidth?: RightRailWidthController;
  /** 当前激活面上抛给 shell（驱动 auto 宽度按面重算）。 */
  onActiveSurfaceChange?: (surface: WorkspacePanelSurfaceId) => void;
  /** 覆盖层抽屉的关闭入口（Esc / 遮罩点击）；缺省时不注册 Esc。 */
  onCloseRightRail?: () => void;
};

const noop = () => {};

/** 独立挂载兜底：auto 模式 + 内容型面（288px），与改造前的固定列宽一致。 */
const FALLBACK_RAIL_WIDTH: RightRailWidthController = {
  mode: "auto",
  widthPx: 288,
  minWidthPx: 320,
  maxWidthPx: 832,
  viewportWidth: 1280,
  widthClass: "content",
  overlay: false,
  commitWidth: noop,
  resetToAuto: noop,
};

export function WorkspaceRightRailSection({
  handleOpenArtifact,
  isNewThread,
  onActiveSurfaceChange,
  onCloseRightRail,
  railWidth,
  rightRailOpen,
  selectedArtifactId,
  selectedThread,
  t,
}: WorkspaceRightRailSectionProps) {
  const widthController = railWidth ?? FALLBACK_RAIL_WIDTH;
  const [activeSurface, setActiveSurface] =
    useState<WorkspacePanelSurfaceId | null>(null);
  const railRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);

  const visible = rightRailOpen && !isNewThread;
  // 文件 / Git 等宽内容面在窄视口改用覆盖层抽屉承载（内容型面维持既有「不渲染」策略）。
  const overlayVisible =
    visible &&
    widthController.overlay &&
    resolveRailSurfaceWidthClass(activeSurface) === "wide";

  const handleActiveSurfaceChange = useCallback(
    (surface: WorkspacePanelSurfaceId) => {
      setActiveSurface(surface);
      onActiveSurfaceChange?.(surface);
    },
    [onActiveSurfaceChange],
  );

  useEffect(() => {
    if (!overlayVisible || !onCloseRightRail) {
      return;
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onCloseRightRail();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [overlayVisible, onCloseRightRail]);

  if (!visible) {
    return null;
  }

  const sessionId = selectedThread.sessionId?.trim() ?? "";
  // 关联快照在这里按纯判据派生一次后向下传值：自包含面（会话详情）不再各判一次，
  // 与侧栏行状态、顶栏状态图标共用同一口径。
  const threadRelation: WorkspacePanelThreadRelation = {
    kind: getThreadRelationKind(selectedThread),
    transport: getThreadTransportKind(selectedThread),
  };
  const railStyle = {
    "--right-rail-width": `${widthController.widthPx}px`,
  } as CSSProperties;

  const railBody = (
    <>
      <RailResizeHandle
        contentRef={contentRef}
        label={t("panels.shell.rightRail.resizeHandle")}
        maxWidthPx={widthController.maxWidthPx}
        minWidthPx={widthController.minWidthPx}
        onCommit={widthController.commitWidth}
        onReset={widthController.resetToAuto}
        railRef={railRef}
        value={widthController.widthPx}
        viewportWidth={widthController.viewportWidth}
      />
      <div
        className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden"
        ref={contentRef}
      >
        <PanelErrorBoundary key={`right-rail-${sessionId || "no-session"}`}>
          <Suspense
            fallback={
              <ArtifactPanelFallback message={t("shell.loadingArtifactPanel")} />
            }
          >
            <ArtifactPanel
              artifacts={selectedThread.artifacts}
              lastRuntimeEventType={selectedThread.lastRuntimeEventType}
              onActiveSurfaceChange={handleActiveSurfaceChange}
              onOpenArtifact={handleOpenArtifact}
              runtimeEventCount={selectedThread.runtimeEventCount}
              selectedArtifactId={selectedArtifactId}
              sessionId={selectedThread.sessionId}
              threadRelation={threadRelation}
            />
          </Suspense>
        </PanelErrorBoundary>
      </div>
    </>
  );

  if (overlayVisible) {
    return (
      <>
        <div
          aria-hidden="true"
          className="fixed inset-0 z-[95] bg-dialog-backdrop xl:hidden"
          data-testid="right-rail-overlay-backdrop"
          onClick={onCloseRightRail}
          role="presentation"
        />
        <div
          aria-label={t("panels.shell.rightRail.overlayLabel")}
          aria-modal="true"
          className="fixed inset-y-0 right-0 z-[96] flex w-[var(--right-rail-width,18rem)] max-w-[92vw] min-h-0 flex-col overflow-hidden border-l border-white/10 shadow-[0_18px_48px_rgba(0,0,0,0.35)] [background:var(--workspace-sidebar-bg)] xl:hidden"
          ref={railRef}
          role="dialog"
          style={railStyle}
        >
          {railBody}
        </div>
      </>
    );
  }

  return (
    <div
      className="relative hidden min-h-0 min-w-0 flex-col overflow-hidden border-l border-white/8 [background:var(--workspace-sidebar-bg)] xl:flex"
      ref={railRef}
      style={railStyle}
    >
      {railBody}
    </div>
  );
}
