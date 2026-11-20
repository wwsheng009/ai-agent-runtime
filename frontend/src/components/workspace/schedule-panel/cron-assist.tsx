// Cron 表达式助手：锚定在表达式字段上的弹出面板。
//
// 预览 / 常用预设 / 可视化生成原先是一整块内联内容，会把「运行规则」区撑得很高，
// 而且它们只在需要时才用，常驻展示反而挤走了时区等主字段。改为浮层后表单保持紧凑，
// 面板按需展开；面板挂在 document.body 上并使用共享的视口夹取定位，避免被弹窗裁切。

import { SparklesIcon } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { createPortal } from "react-dom";

import { fieldControlVariants } from "@/components/ui/field-variants";
import {
  POPOVER_GAP,
  POPOVER_VIEWPORT_PADDING,
  resolvePopoverPosition,
  type PopoverPosition,
} from "@/components/ui/popover-position";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";

import {
  buildCron,
  CRON_PRESETS,
  DEFAULT_CRON_BUILDER,
  describeCron,
  invertCron,
  type CronBuilderFrequency,
  type CronBuilderState,
} from "./cron";

const PANEL_MAX_HEIGHT = 420;

const CRON_WEEKDAY_KEYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"] as const;

const CRON_PRESET_LABEL_KEYS = [
  "every5",
  "every15",
  "hourly",
  "daily9",
  "weekdays9",
  "monthly1",
] as const;

function formatCronClock(hour: number, minute: number) {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

export type CronAssistProps = {
  /** 选中预设或点击「应用」时回填表达式输入框。 */
  onPick: (expression: string) => void;
  /** 当前表达式，用于预览与反向同步构建器。 */
  value: string;
};

/** 触发器 + 弹出面板；面板内的一切操作都只回写同一个表达式。 */
export function CronAssist({ onPick, value }: CronAssistProps) {
  const { t } = useTranslation("workspace");
  const panelId = useId();
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<PopoverPosition | null>(null);
  const [builder, setBuilder] = useState<CronBuilderState>(DEFAULT_CRON_BUILDER);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  // 表达式能被反向解析时让构建器跟随它，避免面板内两处显示不一致。
  useEffect(() => {
    const inverted = invertCron(value);
    if (inverted) {
      setBuilder(inverted);
    }
  }, [value]);

  const preview = useMemo(() => {
    const trimmed = value.trim();
    if (trimmed === "") {
      return t("panels.schedule.cron.empty");
    }
    const described = describeCron(value);
    if (!described) {
      return t("panels.schedule.cron.unrecognized");
    }
    const clock = (hour: number, minute: number) => formatCronClock(hour, minute);
    const weekdayName = (weekday: number) =>
      t(`panels.schedule.cron.weekday.${CRON_WEEKDAY_KEYS[weekday] ?? "mon"}`);

    switch (described.kind) {
      case "everyMinutes":
        return t("panels.schedule.cron.desc.everyMinutes", {
          minutes: String(described.minutes),
        });
      case "everyHours":
        return t("panels.schedule.cron.desc.everyHours", {
          hours: String(described.hours),
        });
      case "hourly":
        return t("panels.schedule.cron.desc.hourly", {
          minute: String(described.minute),
        });
      case "daily":
        return t("panels.schedule.cron.desc.daily", {
          time: clock(described.hour, described.minute),
        });
      case "weekdays":
        return t("panels.schedule.cron.desc.weekdays", {
          time: clock(described.hour, described.minute),
        });
      case "weekly":
        return t("panels.schedule.cron.desc.weekly", {
          weekday: weekdayName(described.weekday),
          time: clock(described.hour, described.minute),
        });
      case "monthly":
        return t("panels.schedule.cron.desc.monthly", {
          day: String(described.day),
          time: clock(described.hour, described.minute),
        });
      default:
        return t("panels.schedule.cron.unrecognized");
    }
  }, [t, value]);

  // 面板与触发器对齐（右缘贴合），高度按视口余量收，避免超出屏幕。
  useEffect(() => {
    if (!open) {
      return;
    }
    const update = () => {
      const node = triggerRef.current;
      if (!node) {
        return;
      }
      const rect = node.getBoundingClientRect();
      // 触发按钮常落在弹窗底部（表单需要滚动才看到表达式），此时下方余量不足，
      // 朝下展开会被视口截断并压住页脚按钮。先量上下余量，空间大的一侧展开。
      const below = window.innerHeight - rect.bottom - POPOVER_GAP - POPOVER_VIEWPORT_PADDING;
      const above = rect.top - POPOVER_GAP - POPOVER_VIEWPORT_PADDING;
      const side = below >= PANEL_MAX_HEIGHT || below >= above ? "bottom" : "top";
      setPosition(
        resolvePopoverPosition(rect, {
          align: "end",
          maxHeight: PANEL_MAX_HEIGHT,
          minHeight: 200,
          minWidth: 340,
          side,
        }),
      );
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open]);

  // 打开后把焦点移进面板：触发器仍在「新建任务」弹窗里，若焦点留在触发器上，
  // Esc 会先被弹窗接管并把整个对话框关掉；焦点进面板后 Esc 才由面板自己消费。
  useEffect(() => {
    if (open && position) {
      panelRef.current?.focus();
    }
  }, [open, position]);

  // 点外部 / Esc 关闭；面板内的下拉菜单是独立 portal，需排除它，否则一点选项就关面板。
  useEffect(() => {
    if (!open) {
      return;
    }
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (!target) {
        return;
      }
      if (panelRef.current?.contains(target) || triggerRef.current?.contains(target)) {
        return;
      }
      if (target instanceof Element && target.closest('[role="listbox"]')) {
        return;
      }
      setOpen(false);
    };
    document.addEventListener("pointerdown", handlePointerDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
    };
  }, [open]);

  const commit = (expression: string) => {
    onPick(expression);
    setOpen(false);
  };

  const numberField = (
    label: string,
    fieldValue: number,
    min: number,
    max: number,
    onChange: (next: number) => void,
    testId: string,
  ) => (
    <label className="grid gap-1">
      <span className="app-text-11 text-muted-foreground">{label}</span>
      <input
        aria-label={label}
        className={cn(fieldControlVariants({ size: "sm" }), "w-full")}
        data-testid={testId}
        max={max}
        min={min}
        onChange={(event) => onChange(Number(event.target.value))}
        type="number"
        value={fieldValue}
      />
    </label>
  );

  const panel =
    open && position ? (
      <div
        aria-label={t("panels.schedule.cron.panelTitle")}
        className={cn(
          // z 低于 Select 的候选面（160），保证下拉菜单浮在面板之上。
          "z-[150] overflow-y-auto rounded-card-lg border border-border bg-surface-overlay p-3",
          "shadow-[0_12px_28px_rgba(0,0,0,0.28)]",
        )}
        id={panelId}
        onKeyDown={(event) => {
          // 面板在弹窗内部，弹窗自己也监听 Esc 关闭；这里就地吞掉，
          // 否则一次 Esc 会连整个「新建任务」对话框一起关掉。
          if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            setOpen(false);
            triggerRef.current?.focus();
          }
        }}
        ref={panelRef}
        role="dialog"
        data-testid="schedule-cron-panel"
        tabIndex={-1}
        style={{
          position: "fixed",
          // 固定 22rem：不设宽度时 fixed 元素按内容收缩-包裹，预览长句会把面板撑到
          // 640px 以上；这个宽度与表单字段的视觉尺度一致，预设按钮也能自然换行。
          width: "min(22rem, calc(100vw - 1rem))",
          ...position,
        }}
      >
        <div className="grid gap-3">
          <div className="grid gap-1">
            <span className="app-text-11 font-medium text-foreground">
              {t("panels.schedule.cron.previewLabel")}
            </span>
            <p
              className="rounded-field bg-surface-soft px-2.5 py-1.5 app-text-11 leading-4 text-muted-foreground"
              data-testid="schedule-cron-preview"
            >
              {preview}
            </p>
          </div>

          <div className="grid gap-1">
            <span className="app-text-11 text-muted-foreground">
              {t("panels.schedule.cron.presetsLabel")}
            </span>
            <div className="flex flex-wrap gap-1.5">
              {CRON_PRESETS.map((preset, index) => (
                <button
                  className={cn(
                    "rounded-full border border-border px-2 py-0.5 app-text-11 text-muted-foreground",
                    "transition-colors hover:border-accent-secondary hover:text-foreground",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    preset === value.trim() &&
                      "border-accent-secondary text-foreground",
                  )}
                  data-testid={`schedule-cron-preset-${preset.replace(/[^0-9a-z]/gi, "-")}`}
                  key={preset}
                  onClick={() => commit(preset)}
                  type="button"
                >
                  {t(`panels.schedule.cron.preset.${CRON_PRESET_LABEL_KEYS[index]}`)}
                </button>
              ))}
            </div>
          </div>

          <div className="grid gap-2 border-t border-border/70 pt-2.5">
            <span className="app-text-11 text-muted-foreground">
              {t("panels.schedule.cron.builderLabel")}
            </span>
            <Select
              ariaLabel={t("panels.schedule.cron.frequencyLabel")}
              className="w-full"
              onChange={(next) =>
                setBuilder((current) => ({
                  ...current,
                  frequency: next as CronBuilderFrequency,
                }))
              }
              options={[
                {
                  value: "everyMinutes",
                  label: t("panels.schedule.cron.frequency.everyMinutes"),
                },
                {
                  value: "hourly",
                  label: t("panels.schedule.cron.frequency.hourly"),
                },
                { value: "daily", label: t("panels.schedule.cron.frequency.daily") },
                { value: "weekly", label: t("panels.schedule.cron.frequency.weekly") },
              ]}
              triggerClassName={cn(fieldControlVariants({ size: "sm" }), "w-full")}
              triggerTestId="schedule-cron-frequency"
              value={builder.frequency}
            />

            <div className="grid grid-cols-2 gap-2">
              {builder.frequency === "everyMinutes"
                ? numberField(
                    t("panels.schedule.cron.intervalLabel"),
                    builder.interval,
                    1,
                    59,
                    (next) =>
                      setBuilder((current) => ({ ...current, interval: next })),
                    "schedule-cron-interval",
                  )
                : null}
              {builder.frequency === "hourly"
                ? numberField(
                    t("panels.schedule.cron.minuteLabel"),
                    builder.minute,
                    0,
                    59,
                    (next) =>
                      setBuilder((current) => ({ ...current, minute: next })),
                    "schedule-cron-minute",
                  )
                : null}
              {builder.frequency === "daily" || builder.frequency === "weekly"
                ? numberField(
                    t("panels.schedule.cron.hourLabel"),
                    builder.hour,
                    0,
                    23,
                    (next) => setBuilder((current) => ({ ...current, hour: next })),
                    "schedule-cron-hour",
                  )
                : null}
              {builder.frequency === "daily" || builder.frequency === "weekly"
                ? numberField(
                    t("panels.schedule.cron.minuteLabel"),
                    builder.minute,
                    0,
                    59,
                    (next) =>
                      setBuilder((current) => ({ ...current, minute: next })),
                    "schedule-cron-minute",
                  )
                : null}
              {builder.frequency === "weekly" ? (
                <Select
                  ariaLabel={t("panels.schedule.cron.weekdayLabel")}
                  className="col-span-2 w-full"
                  onChange={(next) =>
                    setBuilder((current) => ({
                      ...current,
                      weekday: Number(next),
                    }))
                  }
                  options={CRON_WEEKDAY_KEYS.map((key, index) => ({
                    value: String(index),
                    label: t(`panels.schedule.cron.weekday.${key}`),
                  }))}
                  triggerClassName={cn(
                    fieldControlVariants({ size: "sm" }),
                    "w-full",
                  )}
                  triggerTestId="schedule-cron-weekday"
                  value={String(builder.weekday)}
                />
              ) : null}
            </div>

            <button
              className={cn(
                "w-full rounded-field border border-border px-2.5 py-1.5 app-text-11",
                "text-muted-foreground transition-colors hover:border-accent-secondary hover:text-foreground",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              )}
              data-testid="schedule-cron-apply"
              onClick={() => commit(buildCron(builder))}
              type="button"
            >
              {t("panels.schedule.cron.apply")}
            </button>
          </div>
        </div>
      </div>
    ) : null;

  return (
    <>
      <button
        aria-controls={open ? panelId : undefined}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t("panels.schedule.cron.panelTrigger")}
        className={cn(
          "inline-flex items-center gap-1 rounded-control px-1.5 py-0.5 app-text-11 text-muted-foreground",
          "transition-colors hover:bg-surface-soft hover:text-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          open && "bg-surface-soft text-foreground",
        )}
        data-testid="schedule-cron-assist-trigger"
        onClick={() => setOpen((current) => !current)}
        ref={triggerRef}
        type="button"
      >
        <SparklesIcon aria-hidden="true" size={12} />
        {t("panels.schedule.cron.panelTrigger")}
      </button>
      {panel && typeof document !== "undefined" && document.body
        ? createPortal(panel, document.body)
        : panel}
    </>
  );
}
