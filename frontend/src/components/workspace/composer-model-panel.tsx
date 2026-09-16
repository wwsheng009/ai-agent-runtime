// composer 常驻模型座位：provider / model / reasoning effort 收进同一个弹出面板。
//
// 动机：三个并排 Select 在窄窗口下把工具条挤成两行，且「换模型 + 调推理强度」要走两次弹层。
//
// 交互（二级菜单）：
// - 一级：供应商 / 模型 / 推理等级 三行摘要，行内是当前生效值，点行进二级候选列表；
// - 二级：一次只渲染当前这一项的候选（不再让三段列表同屏滚动），左上角返回，Esc 回一级；
// - 换供应商：宿主会立即重解析模型（保留仍被支持的，否则回落）并钳制推理档位；面板额外把
//   「模型 + 推理等级」标成待重新选择并自动下钻到模型列表，选完模型再自动进入推理列表，
//   用户不显式确认就不会把这组选择当作已定 —— 避免换供应商后把旧组合默默带过去；
// - 同一供应商原地重选不触发失效。
//
// 边界（与旧三个 Select 的语义保持一致，不新增猜测）：
// - 只渲染宿主投影的候选：不拉取 / 不缓存 / 不推断默认选中；
// - 某段候选为空 → 整段隐藏；三段都为空 → 连触发器都不渲染（没有目录就不假装能选）；
// - 不校验 / 不降级任何取值：有效值由宿主解析（`resolveRuntimeModelSelection`、
//   `resolveEffectiveReasoningEffort`），面板只如实显示并把「重选」这一步显式化；
// - 禁用态与原因由宿主传入（目录加载中 / 响应中），原因挂在 title，避免「点了没反应」；
// - 点选即回调宿主；关闭按钮 / 外部点击 / Esc（一级）关闭并归还焦点。
import {
  useCallback,
  type KeyboardEvent as ReactKeyboardEvent,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";

import { ChevronDownIcon, SlidersHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { ComposerModelPanelSurface } from "@/components/workspace/composer-model-panel-surface";
import {
  resolvePopoverPosition,
  type PopoverPosition,
} from "@/components/ui/popover-position";
import {
  advanceAfterModelSelect,
  advanceAfterProviderSelect,
  advanceAfterReasoningSelect,
  buildComposerModelSections,
  nextOptionIndex,
  PANEL_MAX_HEIGHT,
  PANEL_MIN_HEIGHT,
  PANEL_MIN_WIDTH,
  type ComposerModelPanelPending,
  type ComposerModelPanelSection,
  type ComposerModelPanelSectionId,
  type ComposerModelPanelView,
} from "@/lib/composer/model-panel-model";
import { cn } from "@/lib/utils";

export type ComposerModelPanelProps = {
  disabled?: boolean;
  /** 禁用原因：作为触发器 title，让「为什么点不动」可见。 */
  disabledReason?: string | null;
  modelOptions: readonly string[];
  onModelChange: (model: string) => void;
  onProviderChange: (provider: string) => void;
  onReasoningEffortChange: (effort: string) => void;
  providerOptions: readonly string[];
  reasoningEffortDefault: string;
  reasoningEffortOptions: readonly string[];
  selectedModel: string;
  selectedProvider: string;
  selectedReasoningEffort: string;
};

export function ComposerModelPanel({
  disabled = false,
  disabledReason = null,
  modelOptions,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  providerOptions,
  reasoningEffortDefault,
  reasoningEffortOptions,
  selectedModel,
  selectedProvider,
  selectedReasoningEffort,
}: ComposerModelPanelProps) {
  const { t } = useTranslation("workspace");
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<PopoverPosition | null>(null);
  const [view, setView] = useState<ComposerModelPanelView>({ level: "root" });
  const [pending, setPending] = useState<ComposerModelPanelPending>({
    model: false,
    reasoning: false,
  });
  const panelId = useId();
  const panelRef = useRef<HTMLDivElement | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);

  const unselectedLabel = t("composer.modelPanel.unselected");
  const reselectHint = t("composer.modelPanel.reselectHint");
  const defaultEffortLabel = reasoningEffortDefault
    ? t("composer.reasoningDefaultWithValue", { effort: reasoningEffortDefault })
    : t("composer.reasoningDefault");
  const reasoningLabel = selectedReasoningEffort
    ? selectedReasoningEffort
    : defaultEffortLabel;

  function goBackToRoot() {
    setView({ level: "root" });
  }

  function hasSection(id: ComposerModelPanelSectionId) {
    return sections.some((section) => section.id === id);
  }

  // 供应商一变，「模型 × 推理等级」这组选择就可能失效：宿主会重解析模型（仍被支持的才保留，
  // 否则回落）并钳制推理档位。面板不猜宿主结果，只把重选这一步显式化并顺序引导。
  function handleProviderSelect(provider: string) {
    onProviderChange(provider);
    const advance = advanceAfterProviderSelect({
      hasModel: hasSection("model"),
      hasReasoning: hasSection("reasoning"),
      providerChanged: provider !== selectedProvider,
    });
    if (advance.pending) {
      setPending(advance.pending);
    }
    setView(advance.view);
  }

  function handleModelSelect(model: string) {
    onModelChange(model);
    // 普通换模型（返回 null）：留在候选列表里继续比较。
    const advance = advanceAfterModelSelect({
      hasReasoning: hasSection("reasoning"),
      pendingModel: pending.model,
    });
    if (!advance) {
      return;
    }
    setPending(advance.pending);
    setView(advance.view);
  }

  function handleReasoningSelect(effort: string) {
    onReasoningEffortChange(effort);
    // 普通调档（返回 null）：留在候选列表里继续比较。
    const advance = advanceAfterReasoningSelect(pending.reasoning);
    if (!advance) {
      return;
    }
    setPending(advance.pending);
    setView(advance.view);
  }

  // 段顺序固定为 provider → model → reasoning：与旧工具条从左到右的阅读顺序一致。
  const sections: ComposerModelPanelSection[] = buildComposerModelSections({
    defaultEffortLabel,
    labels: {
      model: t("composer.model"),
      provider: t("composer.provider"),
      reasoning: t("composer.reasoning"),
    },
    modelOptions,
    onModelSelect: handleModelSelect,
    onProviderSelect: handleProviderSelect,
    onReasoningSelect: handleReasoningSelect,
    providerOptions,
    reasoningEffortOptions,
    reasoningValue: reasoningLabel,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort,
    unselectedLabel,
  });

  // 候选段消失时（例如新模型不声明推理档位）对应的「待确认」就没有对象了，不该继续提醒。
  const modelPending = pending.model && hasSection("model");
  const reasoningPending = pending.reasoning && hasSection("reasoning");
  const hasPending = modelPending || reasoningPending;

  // 摘要只列「有候选可选」的项：单 provider 不给选择，就不占位置。
  const summarySegments = [
    providerOptions.length > 1 ? selectedProvider : "",
    modelOptions.length > 0 ? selectedModel : "",
    reasoningEffortOptions.length > 0 ? reasoningLabel : "",
  ].filter((segment) => segment.length > 0);
  const triggerLabel =
    summarySegments.length > 0
      ? summarySegments.join(" · ")
      : t("composer.modelPanel.trigger");

  // 二级菜单：候选在切换后消失时退回一级，而不是渲染一个空列表。
  const activeSection =
    view.level === "section"
      ? sections.find((section) => section.id === view.section)
      : undefined;

  const updatePosition = useCallback(() => {
    if (typeof window === "undefined") {
      return;
    }

    const triggerRect = triggerRef.current?.getBoundingClientRect();
    if (!triggerRect) {
      return;
    }

    // composer 在页面底部：固定向上展开，避免弹层被视口下沿裁掉。
    setPosition(
      resolvePopoverPosition(triggerRect, {
        align: "start",
        maxHeight: PANEL_MAX_HEIGHT,
        minHeight: PANEL_MIN_HEIGHT,
        minWidth: PANEL_MIN_WIDTH,
        side: "top",
      }),
    );
  }, []);

  function focusTrigger() {
    requestAnimationFrame(() => {
      triggerRef.current?.focus();
    });
  }

  function closePanel(restoreFocus = false) {
    setOpen(false);
    if (restoreFocus) {
      focusTrigger();
    }
  }

  function openPanel() {
    if (disabled) {
      return;
    }

    // 每次打开都从一级开始：二级是「这一次要改什么」的上下文，不该跨次保留。
    setView({ level: "root" });
    updatePosition();
    setOpen(true);
  }

  // 响应开始 / 目录重新加载：禁用即收起，避免停留在无法生效的面板上。
  // 走 React 官方「prop 变化时调整 state」写法（渲染期比对上一轮的 disabled），不在 effect 里同步 setState，
  // 也不用派生 `open && !disabled`（那会在禁用结束后把面板弹回来）。
  const [lastDisabled, setLastDisabled] = useState(disabled);
  if (lastDisabled !== disabled) {
    setLastDisabled(disabled);
    if (disabled && open) {
      setOpen(false);
    }
  }

  // 一级 ↔ 二级切换后把焦点放进当前层的条目（二级优先当前选中项），
  // 键盘用户不必先 Tab 穿过表头。
  useEffect(() => {
    if (!open) {
      return;
    }

    if (view.level === "section") {
      const options = Array.from(
        panelRef.current?.querySelectorAll<HTMLButtonElement>(
          `[data-composer-model-panel-option="${view.section}"]`,
        ) ?? [],
      );
      const selected = options.find(
        (node) => node.getAttribute("aria-selected") === "true",
      );
      (selected ?? options[0] ?? panelRef.current)?.focus();
      return;
    }

    panelRef.current?.focus();
  }, [open, view]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }

      if (
        !rootRef.current?.contains(target) &&
        !panelRef.current?.contains(target)
      ) {
        closePanel();
      }
    };

    const handleWindowKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }

      // 二级菜单里的 Esc 只退一层：先回一级，再按一次才关面板。
      if (view.level === "section") {
        setView({ level: "root" });
        return;
      }

      closePanel(true);
    };

    window.addEventListener("pointerdown", handlePointerDown);
    window.addEventListener("keydown", handleWindowKeyDown);

    return () => {
      window.removeEventListener("pointerdown", handlePointerDown);
      window.removeEventListener("keydown", handleWindowKeyDown);
    };
  }, [open, view]);

  useEffect(() => {
    if (!open || typeof window === "undefined") {
      return;
    }

    const handleViewportChange = () => {
      updatePosition();
    };

    window.addEventListener("resize", handleViewportChange);
    window.addEventListener("scroll", handleViewportChange, true);

    const triggerNode = triggerRef.current;
    const resizeObserver =
      typeof ResizeObserver !== "undefined" && triggerNode
        ? new ResizeObserver(() => {
            handleViewportChange();
          })
        : null;

    if (resizeObserver && triggerNode) {
      resizeObserver.observe(triggerNode);
    }

    return () => {
      window.removeEventListener("resize", handleViewportChange);
      window.removeEventListener("scroll", handleViewportChange, true);
      resizeObserver?.disconnect();
    };
  }, [open, updatePosition]);

  // 面板内上下键在「当前这一层」的条目间移动焦点（条目本身是 button，Enter/Space 走原生语义）。
  function handlePanelKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key === "ArrowLeft" && activeSection) {
      event.preventDefault();
      goBackToRoot();
      return;
    }

    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") {
      return;
    }

    const selector = activeSection
      ? `[data-composer-model-panel-option="${activeSection.id}"]`
      : "[data-composer-model-panel-row]";
    const nodes = panelRef.current?.querySelectorAll<HTMLButtonElement>(selector);
    if (!nodes || nodes.length === 0) {
      return;
    }

    event.preventDefault();
    const options = Array.from(nodes);
    const currentIndex = options.findIndex(
      (node) => node === document.activeElement,
    );
    const step = event.key === "ArrowDown" ? 1 : -1;
    const nextIndex = nextOptionIndex(currentIndex, options.length, step);
    options[nextIndex]?.focus();
  }

  if (sections.length === 0) {
    return null;
  }

  const panel =
    open && position ? (
      <ComposerModelPanelSurface
        activeSection={activeSection}
        modelPending={modelPending}
        onBackToRoot={goBackToRoot}
        onClose={() => {
          closePanel(true);
        }}
        onKeyDown={handlePanelKeyDown}
        onSelectSection={(section) => {
          setView({ level: "section", section });
        }}
        panelId={panelId}
        panelRef={panelRef}
        position={position}
        reasoningPending={reasoningPending}
        reselectHint={reselectHint}
        sections={sections}
      />
    ) : null;

  return (
    <>
      <div ref={rootRef} className="inline-flex min-w-0">
        <button
          ref={triggerRef}
          type="button"
          aria-label={t("composer.modelPanel.trigger")}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-controls={open ? panelId : undefined}
          data-composer-model-panel-trigger
          disabled={disabled}
          title={
            disabled && disabledReason
              ? disabledReason
              : hasPending
                ? `${triggerLabel} · ${reselectHint}`
                : triggerLabel
          }
          onClick={(event) => {
            event.stopPropagation();
            if (open) {
              closePanel();
              return;
            }
            openPanel();
          }}
          onKeyDown={(event) => {
            if (event.key !== "ArrowDown" && event.key !== "ArrowUp") {
              return;
            }
            event.preventDefault();
            event.stopPropagation();
            openPanel();
          }}
          className={cn(
            "inline-flex min-w-0 max-w-[22rem] items-center gap-1.5 rounded-[0.6rem] border border-border",
            "bg-surface-soft px-2 py-1 text-left text-base leading-none text-muted-foreground outline-none transition",
            "hover:border-border-strong hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
            "disabled:cursor-not-allowed disabled:opacity-60",
          )}
        >
          <SlidersHorizontalIcon size={13} aria-hidden="true" className="shrink-0" />
          <span className="truncate" data-composer-model-panel-summary>
            {triggerLabel}
          </span>
          {/* 面板关着也要能看出「换了供应商、还欠一次确认」。 */}
          {hasPending ? (
            <span
              data-composer-model-panel-pending
              aria-hidden="true"
              className="size-1.5 shrink-0 rounded-full bg-amber-300/70"
            />
          ) : null}
          <ChevronDownIcon
            size={12}
            aria-hidden="true"
            className={cn(
              "shrink-0 transition-transform duration-150",
              open ? "rotate-180" : "rotate-0",
            )}
          />
        </button>
      </div>
      {panel && typeof document !== "undefined" && document.body
        ? createPortal(panel, document.body)
        : panel}
    </>
  );
}
