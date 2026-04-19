import {
  useCallback,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";

import { CheckIcon, ChevronDownIcon } from "lucide-react";

import { cn } from "@/lib/utils";

export type SelectOption = {
  value: string;
  label: string;
  disabled?: boolean;
};

type SelectProps = {
  ariaLabel: string;
  options: readonly SelectOption[];
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  align?: "start" | "end";
  side?: "top" | "bottom";
  className?: string;
  triggerClassName?: string;
  menuClassName?: string;
  optionClassName?: string;
};

type SelectMenuPosition = Pick<
  CSSProperties,
  "bottom" | "left" | "minWidth" | "top" | "right"
> & {
  maxHeight: number;
};

const SELECT_MENU_GAP = 8;
const SELECT_VIEWPORT_PADDING = 8;

function resolveMenuPosition(
  triggerRect: DOMRect,
  align: NonNullable<SelectProps["align"]>,
  side: NonNullable<SelectProps["side"]>,
): SelectMenuPosition {
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  const minWidth = Math.min(
    triggerRect.width,
    viewportWidth - SELECT_VIEWPORT_PADDING * 2,
  );
  const availableHeight =
    side === "top"
      ? triggerRect.top - SELECT_MENU_GAP - SELECT_VIEWPORT_PADDING
      : viewportHeight -
        triggerRect.bottom -
        SELECT_MENU_GAP -
        SELECT_VIEWPORT_PADDING;

  return {
    bottom:
      side === "top"
        ? viewportHeight - triggerRect.top + SELECT_MENU_GAP
        : "auto",
    left:
      align === "start"
        ? Math.max(triggerRect.left, SELECT_VIEWPORT_PADDING)
        : "auto",
    maxHeight: Math.max(96, Math.min(240, availableHeight)),
    minWidth,
    right:
      align === "end"
        ? Math.max(
            viewportWidth - triggerRect.right,
            SELECT_VIEWPORT_PADDING,
          )
        : "auto",
    top:
      side === "bottom"
        ? Math.max(triggerRect.bottom + SELECT_MENU_GAP, SELECT_VIEWPORT_PADDING)
        : "auto",
  };
}

function findFirstEnabledOptionIndex(options: readonly SelectOption[]) {
  return options.findIndex((option) => !option.disabled);
}

function findLastEnabledOptionIndex(options: readonly SelectOption[]) {
  for (let index = options.length - 1; index >= 0; index -= 1) {
    if (!options[index]?.disabled) {
      return index;
    }
  }

  return -1;
}

function findNextEnabledOptionIndex(
  options: readonly SelectOption[],
  currentIndex: number,
  direction: 1 | -1,
) {
  if (options.length === 0) {
    return -1;
  }

  if (currentIndex < 0) {
    return direction === 1
      ? findFirstEnabledOptionIndex(options)
      : findLastEnabledOptionIndex(options);
  }

  let nextIndex = currentIndex;
  for (let checked = 0; checked < options.length; checked += 1) {
    nextIndex = (nextIndex + direction + options.length) % options.length;
    if (!options[nextIndex]?.disabled) {
      return nextIndex;
    }
  }

  return -1;
}

export function Select({
  ariaLabel,
  options,
  value,
  onChange,
  placeholder = "Select",
  disabled = false,
  align = "start",
  side = "bottom",
  className,
  triggerClassName,
  menuClassName,
  optionClassName,
}: SelectProps) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const listboxRef = useRef<HTMLDivElement | null>(null);
  const listboxId = useId();
  const [open, setOpen] = useState(false);
  const [menuPosition, setMenuPosition] = useState<SelectMenuPosition | null>(null);
  const selectedIndex = options.findIndex((option) => option.value === value);
  const selectedOption = selectedIndex >= 0 ? options[selectedIndex] : undefined;
  const triggerLabel = selectedOption?.label ?? (value || placeholder);
  const [activeIndex, setActiveIndex] = useState(() =>
    selectedIndex >= 0 ? selectedIndex : findFirstEnabledOptionIndex(options),
  );
  const isDisabled = disabled || options.length === 0;
  const resolvedActiveIndex =
    activeIndex >= 0 && activeIndex < options.length && !options[activeIndex]?.disabled
      ? activeIndex
      : selectedIndex >= 0
        ? selectedIndex
        : findFirstEnabledOptionIndex(options);

  function focusTrigger() {
    requestAnimationFrame(() => {
      triggerRef.current?.focus();
    });
  }

  const updateMenuPosition = useCallback(() => {
    if (typeof window === "undefined") {
      return;
    }

    const triggerRect = triggerRef.current?.getBoundingClientRect();
    if (!triggerRect) {
      return;
    }

    setMenuPosition(resolveMenuPosition(triggerRect, align, side));
  }, [align, side]);

  function closeMenu() {
    setOpen(false);
  }

  function openMenu(initialIndex?: number) {
    if (isDisabled) {
      return;
    }

    updateMenuPosition();
    const fallbackIndex = findFirstEnabledOptionIndex(options);
    setActiveIndex(initialIndex ?? (selectedIndex >= 0 ? selectedIndex : fallbackIndex));
    setOpen(true);
  }

  function commitSelection(index: number) {
    const option = options[index];
    if (!option || option.disabled) {
      return;
    }

    if (option.value !== value) {
      onChange(option.value);
    }
    closeMenu();
    focusTrigger();
  }

  function handleTriggerKeyDown(event: ReactKeyboardEvent<HTMLButtonElement>) {
    if (isDisabled) {
      return;
    }

    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        openMenu(selectedIndex >= 0 ? selectedIndex : findFirstEnabledOptionIndex(options));
        break;
      case "ArrowUp":
        event.preventDefault();
        openMenu(selectedIndex >= 0 ? selectedIndex : findLastEnabledOptionIndex(options));
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        if (open) {
          closeMenu();
          return;
        }
        openMenu(selectedIndex >= 0 ? selectedIndex : findFirstEnabledOptionIndex(options));
        break;
      default:
        break;
    }
  }

  function handleListboxKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        setActiveIndex((currentIndex) =>
          findNextEnabledOptionIndex(options, currentIndex, 1),
        );
        break;
      case "ArrowUp":
        event.preventDefault();
        setActiveIndex((currentIndex) =>
          findNextEnabledOptionIndex(options, currentIndex, -1),
        );
        break;
      case "Home":
        event.preventDefault();
        setActiveIndex(findFirstEnabledOptionIndex(options));
        break;
      case "End":
        event.preventDefault();
        setActiveIndex(findLastEnabledOptionIndex(options));
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        if (resolvedActiveIndex >= 0) {
          commitSelection(resolvedActiveIndex);
        }
        break;
      case "Escape":
        event.preventDefault();
        closeMenu();
        focusTrigger();
        break;
      case "Tab":
        closeMenu();
        break;
      default:
        break;
    }
  }

  useEffect(() => {
    if (!open) {
      return;
    }

    listboxRef.current?.focus();
  }, [open]);

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
        !listboxRef.current?.contains(target)
      ) {
        closeMenu();
      }
    };

    const handleWindowKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        closeMenu();
        focusTrigger();
      }
    };

    window.addEventListener("pointerdown", handlePointerDown);
    window.addEventListener("keydown", handleWindowKeyDown);

    return () => {
      window.removeEventListener("pointerdown", handlePointerDown);
      window.removeEventListener("keydown", handleWindowKeyDown);
    };
  }, [open]);

  useEffect(() => {
    if (!open || typeof window === "undefined") {
      return;
    }

    const handleViewportChange = () => {
      updateMenuPosition();
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
  }, [open, updateMenuPosition]);

  useEffect(() => {
    if (!open || resolvedActiveIndex < 0) {
      return;
    }

    const optionElement = document.getElementById(
      `${listboxId}-option-${resolvedActiveIndex}`,
    );
    optionElement?.scrollIntoView({ block: "nearest" });
  }, [listboxId, open, resolvedActiveIndex]);

  const menu = open ? (
    <div
      id={listboxId}
      ref={listboxRef}
      role="listbox"
      tabIndex={-1}
      aria-label={ariaLabel}
      aria-activedescendant={
        resolvedActiveIndex >= 0
          ? `${listboxId}-option-${resolvedActiveIndex}`
          : undefined
      }
      onKeyDown={handleListboxKeyDown}
      style={
        menuPosition
          ? {
              bottom: menuPosition.bottom,
              left: menuPosition.left,
              minWidth: menuPosition.minWidth,
              maxHeight: menuPosition.maxHeight,
              position: "fixed",
              right: menuPosition.right,
              top: menuPosition.top,
            }
          : undefined
      }
      className={cn(
        "z-[160] w-max max-w-[min(22rem,calc(100vw-1rem))] overflow-hidden rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-overlay)] p-1.5 shadow-[0_10px_24px_rgba(0,0,0,0.24)] outline-none",
        menuClassName,
      )}
    >
      <div className="flex max-h-[inherit] flex-col overflow-y-auto">
        {options.map((option, index) => {
          const selected = option.value === value;
          const active = index === resolvedActiveIndex;

          return (
            <div
              key={option.value}
              id={`${listboxId}-option-${index}`}
              role="option"
              aria-selected={selected}
              aria-disabled={option.disabled || undefined}
              onMouseEnter={() => {
                if (!option.disabled) {
                  setActiveIndex(index);
                }
              }}
              onMouseDown={(event) => {
                event.preventDefault();
              }}
              onClick={() => commitSelection(index)}
              className={cn(
                "flex cursor-pointer items-center gap-2 rounded-[0.65rem] px-2.5 py-2 leading-5 text-[var(--muted-foreground)] transition",
                option.disabled
                  ? "cursor-not-allowed opacity-45"
                  : active
                    ? "bg-[var(--surface-soft)] text-[var(--foreground)]"
                    : "hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]",
                optionClassName,
              )}
            >
              <span className="flex size-4 shrink-0 items-center justify-center">
                {selected ? <CheckIcon size={13} /> : null}
              </span>
              <span className="truncate">{option.label}</span>
            </div>
          );
        })}
      </div>
    </div>
  ) : null;

  return (
    <>
      <div ref={rootRef} className={cn("relative inline-flex", className)}>
        <button
          ref={triggerRef}
          type="button"
          aria-label={ariaLabel}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-controls={listboxId}
          disabled={isDisabled}
          onClick={(event) => {
            event.stopPropagation();
            if (open) {
              closeMenu();
              return;
            }
            openMenu(selectedIndex >= 0 ? selectedIndex : findFirstEnabledOptionIndex(options));
          }}
          onKeyDown={(event) => {
            event.stopPropagation();
            handleTriggerKeyDown(event);
          }}
          className={cn(
            "inline-flex w-full items-center justify-between gap-2 rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2 text-left text-[var(--foreground)] outline-none transition hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)] focus-visible:ring-2 focus-visible:ring-[var(--ring)] disabled:cursor-not-allowed disabled:opacity-60",
            triggerClassName,
          )}
        >
          <span className="truncate">{triggerLabel}</span>
          <ChevronDownIcon
            size={14}
            className={cn(
              "shrink-0 text-[var(--muted-foreground)] transition-transform duration-150",
              open ? "rotate-180" : "rotate-0",
            )}
          />
        </button>
      </div>
      {menu && typeof document !== "undefined" && document.body
        ? createPortal(menu, document.body)
        : menu}
    </>
  );
}
