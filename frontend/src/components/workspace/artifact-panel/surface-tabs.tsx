// 右侧栏页签栏：面的渲染顺序、标题、图标、tone 全部来自 panel-registry，
// 新增面不需要改本文件（tone → 样式已收敛为映射表）。

import {
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import {
  WORKSPACE_PANEL_SURFACES,
  type WorkspacePanelSurfaceId,
  type WorkspacePanelSurfaceTabIds,
  type WorkspacePanelSurfaceTone,
} from "@/components/workspace/panel-registry";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

const TAB_BASE =
  "inline-flex items-center gap-2 rounded-control border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--workspace-sidebar-bg)]";
const TAB_IDLE =
  "border-white/10 bg-white/4 text-muted-foreground hover:border-white/16 hover:bg-white/8";
const TAB_DISABLED =
  "inline-flex items-center gap-2 rounded-control border border-white/8 bg-white/4 px-2.5 py-1 text-base text-muted-foreground opacity-60";

/** tone 只影响激活配色；没有专属 token 的面复用 `artifact`。 */
const TONE_ACTIVE_CLASS: Record<WorkspacePanelSurfaceTone, string> = {
  artifact: "border-accent-gold/30 bg-accent-gold/8 text-accent-gold",
  plan: "border-[#9db7ff]/30 bg-[#9db7ff]/10 text-[#9db7ff]",
  checkpoint: "border-accent-teal/30 bg-accent-teal/10 text-accent-teal",
  file: "border-accent-cyan/30 bg-accent-cyan/10 text-accent-cyan",
  git: "border-accent-violet/30 bg-accent-violet/10 text-accent-violet",
};

function surfaceTabClass(
  spec: (typeof WORKSPACE_PANEL_SURFACES)[number],
  active: boolean,
  disabled: boolean,
) {
  if (disabled) {
    return TAB_DISABLED;
  }
  return cn(TAB_BASE, TAB_IDLE, active && TONE_ACTIVE_CLASS[spec.tone]);
}

function handleHorizontalTabKeyDown(
  event: ReactKeyboardEvent<HTMLButtonElement>,
  options: {
    currentIndex: number;
    surfaceIndices: number[];
    onSelectIndex: (index: number) => void;
    refs: Array<HTMLButtonElement | null>;
  },
) {
  const { surfaceIndices } = options;
  if (surfaceIndices.length === 0) {
    return;
  }

  const currentEnabledIndex = surfaceIndices.indexOf(options.currentIndex);
  let nextIndex = -1;

  if (event.key === "Home") {
    nextIndex = surfaceIndices[0] ?? -1;
  } else if (event.key === "End") {
    nextIndex = surfaceIndices[surfaceIndices.length - 1] ?? -1;
  } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
    const target =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex + 1) % surfaceIndices.length
        : 0;
    nextIndex = surfaceIndices[target] ?? -1;
  } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
    const target =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex - 1 + surfaceIndices.length) % surfaceIndices.length
        : surfaceIndices.length - 1;
    nextIndex = surfaceIndices[target] ?? -1;
  }

  if (nextIndex < 0) {
    return;
  }

  event.preventDefault();
  options.onSelectIndex(nextIndex);
  options.refs[nextIndex]?.focus();
}

type ArtifactPanelSurfaceTabsProps = {
  activeSurface: WorkspacePanelSurfaceId;
  /** 页签内的可选徽标（计数 / 状态胶囊），由 PanelHost 提供数据。 */
  badges?: Partial<Record<WorkspacePanelSurfaceId, ReactNode>>;
  /** 已翻译的禁用原因；命中即禁用该页签并给出可解释提示。 */
  disabledReasons?: Partial<Record<WorkspacePanelSurfaceId, string>>;
  onSelectSurface: (surface: WorkspacePanelSurfaceId) => void;
  tabIds: Record<WorkspacePanelSurfaceId, WorkspacePanelSurfaceTabIds>;
  titleId: string;
  /** 右上角面板级徽标（沿用既有「条目数」入口）。 */
  trailingBadge?: ReactNode;
};

export function ArtifactPanelSurfaceTabs({
  activeSurface,
  badges,
  disabledReasons,
  onSelectSurface,
  tabIds,
  titleId,
  trailingBadge,
}: ArtifactPanelSurfaceTabsProps) {
  const { t } = useTranslation("workspace");
  const surfaceTabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const enabledSurfaceIndices = WORKSPACE_PANEL_SURFACES.flatMap((spec, index) =>
    disabledReasons?.[spec.id] ? [] : [index],
  );

  return (
    <div className="border-b border-white/8 px-3 py-2.5">
      <div className="flex items-center justify-between gap-3">
        <div className="sr-only" id={titleId}>
          {t("panels.artifacts.tabs.panelTitle")}
        </div>
        <div
          aria-label={t("panels.artifacts.tabs.tabListLabel")}
          aria-orientation="horizontal"
          className="flex flex-wrap gap-1.5"
          role="tablist"
        >
          {WORKSPACE_PANEL_SURFACES.map((spec, index) => {
            const disabledReason = disabledReasons?.[spec.id];
            const active = activeSurface === spec.id;
            const Icon = spec.icon;
            return (
              <button
                key={spec.id}
                aria-controls={tabIds[spec.id].panelId}
                aria-selected={active}
                className={surfaceTabClass(spec, active, Boolean(disabledReason))}
                data-testid={`artifact-panel-tab-${spec.id}`}
                disabled={Boolean(disabledReason)}
                id={tabIds[spec.id].tabId}
                ref={(node) => {
                  surfaceTabRefs.current[index] = node;
                }}
                role="tab"
                tabIndex={active ? 0 : -1}
                title={disabledReason}
                type="button"
                onClick={() => {
                  if (!disabledReason) {
                    onSelectSurface(spec.id);
                  }
                }}
                onKeyDown={(event) =>
                  handleHorizontalTabKeyDown(event, {
                    currentIndex: index,
                    surfaceIndices: enabledSurfaceIndices,
                    onSelectIndex: (targetIndex) => {
                      const target = WORKSPACE_PANEL_SURFACES[targetIndex];
                      if (target) {
                        onSelectSurface(target.id);
                      }
                    },
                    refs: surfaceTabRefs.current,
                  })
                }
              >
                <Icon size={14} />
                {t(spec.labelKey as never) as string}
                {badges?.[spec.id]}
              </button>
            );
          })}
        </div>
        <div className="flex items-center gap-2">{trailingBadge}</div>
      </div>
    </div>
  );
}

export { Badge as SurfaceTabsBadge };
