// 右侧栏页签栏：面的渲染顺序、标题、图标、tone 全部来自 panel-registry，
// 新增面不需要改本文件（tone → 样式已收敛为映射表）。
//
// P0-3：9 个页签在 320px 宽下折成 4 行 → 改为「3 个一等公民 + 更多」。
// - 条目 / 文件 / Git 变更常驻；
// - 产物类 / 会话类 / 运维类收进更多菜单，单行横向滚动，禁止 flex-wrap；
// - 当前激活面若在更多里，触发按钮直接显示该面的图标与名称，避免"打开了却找不到"。

import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { MoreHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  WORKSPACE_PANEL_PRIMARY_SURFACE_IDS,
  WORKSPACE_PANEL_SECONDARY_GROUPS,
  WORKSPACE_PANEL_SURFACES,
  type WorkspacePanelSurfaceId,
  type WorkspacePanelSurfaceSpec,
  type WorkspacePanelSurfaceTabIds,
  type WorkspacePanelSurfaceTone,
} from "@/components/workspace/panel-registry";
import { cn } from "@/lib/utils";

const TAB_BASE =
  "inline-flex shrink-0 items-center gap-2 rounded-control border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--workspace-sidebar-bg)]";
const TAB_IDLE =
  "border-white/10 bg-white/4 text-muted-foreground hover:border-white/16 hover:bg-white/8";
const TAB_DISABLED =
  "inline-flex shrink-0 items-center gap-2 rounded-control border border-white/8 bg-white/4 px-2.5 py-1 text-base text-muted-foreground opacity-60";

/** tone 只影响激活配色；没有专属 token 的面复用 `artifact`。 */
const TONE_ACTIVE_CLASS: Record<WorkspacePanelSurfaceTone, string> = {
  artifact: "border-accent-gold/30 bg-accent-gold/8 text-accent-gold",
  plan: "border-[#9db7ff]/30 bg-[#9db7ff]/10 text-[#9db7ff]",
  checkpoint: "border-accent-teal/30 bg-accent-teal/10 text-accent-teal",
  file: "border-accent-cyan/30 bg-accent-cyan/10 text-accent-cyan",
  git: "border-accent-violet/30 bg-accent-violet/10 text-accent-violet",
};

const SURFACE_BY_ID = new Map<WorkspacePanelSurfaceId, WorkspacePanelSurfaceSpec>(
  WORKSPACE_PANEL_SURFACES.map((spec) => [spec.id, spec]),
);
const PRIMARY_IDS = new Set<WorkspacePanelSurfaceId>(
  WORKSPACE_PANEL_PRIMARY_SURFACE_IDS,
);

function surfaceTabClass(
  spec: WorkspacePanelSurfaceSpec,
  active: boolean,
  disabled: boolean,
) {
  if (disabled) {
    return TAB_DISABLED;
  }
  return cn(TAB_BASE, TAB_IDLE, active && TONE_ACTIVE_CLASS[spec.tone]);
}

function handlePrimaryTabKeyDown(
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
  tabListLabel?: string;
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
  tabListLabel,
  onSelectSurface,
  tabIds,
  titleId,
  trailingBadge,
}: ArtifactPanelSurfaceTabsProps) {
  const { t } = useTranslation("workspace");
  const primaryRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const moreMenuRef = useRef<HTMLDivElement | null>(null);
  const moreTriggerRef = useRef<HTMLButtonElement | null>(null);
  const [moreOpen, setMoreOpen] = useState(false);
  const activeSpec = SURFACE_BY_ID.get(activeSurface) ?? WORKSPACE_PANEL_SURFACES[0];
  const activeSecondarySpec = PRIMARY_IDS.has(activeSurface) ? null : activeSpec;
  const ActiveSecondaryIcon = activeSecondarySpec?.icon;
  const primarySpecs = WORKSPACE_PANEL_PRIMARY_SURFACE_IDS.map((id) =>
    SURFACE_BY_ID.get(id),
  ).filter((spec): spec is WorkspacePanelSurfaceSpec => Boolean(spec));
  const enabledPrimaryIndices = primarySpecs.flatMap((spec, index) =>
    disabledReasons?.[spec.id] ? [] : [index],
  );
  const rovingPrimaryIndex =
    primarySpecs.findIndex(
      (spec) => spec.id === activeSurface && !disabledReasons?.[spec.id],
    ) >= 0
      ? primarySpecs.findIndex(
          (spec) => spec.id === activeSurface && !disabledReasons?.[spec.id],
        )
      : (enabledPrimaryIndices[0] ?? 0);

  useEffect(() => {
    if (!moreOpen) {
      return;
    }

    const menu = moreMenuRef.current;
    menu
      ?.querySelector<HTMLButtonElement>(
        '[role="menuitem"]:not([disabled])',
      )
      ?.focus();

    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      if (
        !moreMenuRef.current?.contains(target) &&
        !moreTriggerRef.current?.contains(target)
      ) {
        setMoreOpen(false);
      }
    };
    const handleWindowKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setMoreOpen(false);
        moreTriggerRef.current?.focus();
      }
    };

    document.addEventListener("pointerdown", handlePointerDown);
    window.addEventListener("keydown", handleWindowKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      window.removeEventListener("keydown", handleWindowKeyDown);
    };
  }, [moreOpen]);

  function closeMoreMenu(focusTrigger: boolean) {
    setMoreOpen(false);
    if (focusTrigger) {
      requestAnimationFrame(() => {
        moreTriggerRef.current?.focus();
      });
    }
  }

  function handleMoreMenuKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      closeMoreMenu(true);
      return;
    }
    if (
      event.key !== "ArrowDown" &&
      event.key !== "ArrowUp" &&
      event.key !== "Home" &&
      event.key !== "End"
    ) {
      return;
    }

    const items = Array.from(
      moreMenuRef.current?.querySelectorAll<HTMLButtonElement>(
        '[role="menuitem"]:not([disabled])',
      ) ?? [],
    );
    if (items.length === 0) {
      return;
    }
    event.preventDefault();
    const currentIndex = items.findIndex(
      (item) => item === document.activeElement,
    );
    let nextIndex = 0;
    if (event.key === "ArrowDown") {
      nextIndex =
        currentIndex < 0 ? 0 : (currentIndex + 1) % items.length;
    } else if (event.key === "ArrowUp") {
      nextIndex =
        currentIndex < 0
          ? items.length - 1
          : (currentIndex - 1 + items.length) % items.length;
    } else if (event.key === "End") {
      nextIndex = items.length - 1;
    }
    items[nextIndex]?.focus();
  }

  return (
    <div className="border-b border-white/8 px-3 py-2.5">
      <div className="flex min-w-0 items-center justify-between gap-3">
        <div className="sr-only" id={titleId}>
          {t(activeSpec.labelKey as never) as string}
        </div>
        <div
          aria-label={tabListLabel ?? t("panels.artifacts.tabs.tabListLabel")}
          aria-orientation="horizontal"
          className="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto [scrollbar-width:none]"
          role="tablist"
        >
          {primarySpecs.map((spec, index) => {
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
                  primaryRefs.current[index] = node;
                }}
                role="tab"
                tabIndex={index === rovingPrimaryIndex ? 0 : -1}
                title={disabledReason}
                type="button"
                onClick={() => {
                  if (!disabledReason) {
                    onSelectSurface(spec.id);
                  }
                }}
                onKeyDown={(event) =>
                  handlePrimaryTabKeyDown(event, {
                    currentIndex: index,
                    surfaceIndices: enabledPrimaryIndices,
                    onSelectIndex: (targetIndex) => {
                      const target = primarySpecs[targetIndex];
                      if (target) {
                        onSelectSurface(target.id);
                      }
                    },
                    refs: primaryRefs.current,
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

        <div className="flex shrink-0 items-center gap-2">
          <div className="relative">
            <button
              ref={moreTriggerRef}
              type="button"
              aria-expanded={moreOpen}
              aria-haspopup="menu"
              aria-label={t("panels.artifacts.tabs.more")}
              title={
                activeSecondarySpec
                  ? (t(activeSecondarySpec.labelKey as never) as string)
                  : (t("panels.artifacts.tabs.more") as string)
              }
              data-testid="artifact-panel-more-trigger"
              onClick={() => setMoreOpen((current) => !current)}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown") {
                  event.preventDefault();
                  setMoreOpen(true);
                } else if (event.key === "Escape" && moreOpen) {
                  event.preventDefault();
                  closeMoreMenu(true);
                }
              }}
              className={cn(
                TAB_BASE,
                activeSecondarySpec
                  ? TONE_ACTIVE_CLASS[activeSecondarySpec.tone]
                  : TAB_IDLE,
              )}
            >
              {ActiveSecondaryIcon ? (
                <ActiveSecondaryIcon size={14} />
              ) : (
                <MoreHorizontalIcon size={14} />
              )}
              <span>
                {activeSecondarySpec
                  ? (t(activeSecondarySpec.labelKey as never) as string)
                  : (t("panels.artifacts.tabs.more") as string)}
              </span>
            </button>
            {moreOpen ? (
              <div
                ref={moreMenuRef}
                role="menu"
                aria-label={t("panels.artifacts.tabs.more")}
                onKeyDown={handleMoreMenuKeyDown}
                className="absolute right-0 top-full z-30 mt-1 w-max min-w-[13rem] rounded-card-lg border border-border bg-surface-overlay p-1.5 shadow-[0_10px_24px_rgba(0,0,0,0.24)]"
              >
                {WORKSPACE_PANEL_SECONDARY_GROUPS.map((group) => (
                  <div key={group.id} role="presentation">
                    <div className="px-2 pb-1 pt-1.5 app-text-10 font-medium text-muted-foreground">
                      {t(group.labelKey as never) as string}
                    </div>
                    {group.surfaceIds.map((surfaceId) => {
                      const spec = SURFACE_BY_ID.get(surfaceId);
                      if (!spec) {
                        return null;
                      }
                      const disabledReason = disabledReasons?.[surfaceId];
                      const active = activeSurface === surfaceId;
                      const Icon = spec.icon;
                      return (
                        <button
                          key={surfaceId}
                          role="menuitem"
                          type="button"
                          aria-current={active ? "true" : undefined}
                          disabled={Boolean(disabledReason)}
                          title={disabledReason}
                          data-testid={`artifact-panel-more-item-${surfaceId}`}
                          onClick={() => {
                            if (disabledReason) {
                              return;
                            }
                            onSelectSurface(surfaceId);
                            closeMoreMenu(true);
                          }}
                          className={cn(
                            "flex w-full items-center gap-2 rounded-control px-2 py-1.5 text-left app-text-12 transition",
                            disabledReason
                              ? "cursor-not-allowed opacity-50"
                              : active
                                ? "bg-surface-soft text-foreground"
                                : "text-muted-foreground hover:bg-surface-soft hover:text-foreground",
                          )}
                        >
                          <Icon size={13} />
                          <span className="min-w-0 flex-1 truncate">
                            {t(spec.labelKey as never) as string}
                          </span>
                          {badges?.[surfaceId]}
                          {active ? (
                            <span
                              aria-hidden="true"
                              className="size-1.5 shrink-0 rounded-full bg-current"
                            />
                          ) : null}
                        </button>
                      );
                    })}
                  </div>
                ))}
              </div>
            ) : null}
          </div>
          {trailingBadge ? (
            <div className="flex items-center gap-2">{trailingBadge}</div>
          ) : null}
        </div>
      </div>
    </div>
  );
}