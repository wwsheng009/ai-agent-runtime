// 字段旁的帮助提示：可悬停、可聚焦、可点击锁定，读屏通过 aria-describedby 关联。
//
// 刻意不把触发按钮放进 <label>：label 会把内部文本计入控件的可访问名称，
// 图标按钮的 aria-label 会污染输入框名称。因此宿主把它渲染在 label 之外。
//
// 提示正文不做绝对定位浮层：弹层会盖住下方的下拉/输入控件，长文案还会压住
// Cron 预设区。这里拆成「触发按钮」+「就地展开的块」两段，由宿主放在字段行
// 下方，展开时把下面的内容推开，不会遮挡任何可交互控件。

import { CircleHelpIcon } from "lucide-react";

import { cn } from "@/lib/utils";

export type HelpTipTriggerProps = {
  /** 无障碍名称，例如「时区 说明」。 */
  label: string;
  open: boolean;
  tipId: string;
  onOpenChange: (open: boolean) => void;
  className?: string;
};

/** 仅渲染问号触发按钮；展开状态由宿主持有。 */
export function HelpTipTrigger({
  className,
  label,
  onOpenChange,
  open,
  tipId,
}: HelpTipTriggerProps) {
  return (
    <button
      aria-describedby={open ? tipId : undefined}
      aria-expanded={open}
      aria-label={label}
      className={cn(
        "grid size-4 shrink-0 place-items-center rounded-full text-muted-foreground/70",
        "transition-colors hover:text-foreground",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
      data-testid="schedule-field-help"
      // 点击设置为「打开」而非取反：鼠标点击会先触发 focus（已打开），
      // 若此处再取反就会立刻关掉，表现为点不开。关闭交给 blur/离开/Esc。
      onClick={() => onOpenChange(true)}
      onBlur={() => onOpenChange(false)}
      onFocus={() => onOpenChange(true)}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onOpenChange(false);
        }
      }}
      onMouseEnter={() => onOpenChange(true)}
      onMouseLeave={() => onOpenChange(false)}
      type="button"
    >
      <CircleHelpIcon aria-hidden="true" size={13} />
    </button>
  );
}

/** 就地展开的说明块：参与文档流，展开时推开下方内容。 */
export function HelpTipBody({
  className,
  text,
  tipId,
}: {
  className?: string;
  text: string;
  tipId: string;
}) {
  return (
    <p
      className={cn(
        "rounded-field border border-border bg-surface-solid px-2.5 py-2",
        "text-[11px] font-normal leading-4 text-muted-foreground",
        className,
      )}
      id={tipId}
      role="tooltip"
    >
      {text}
    </p>
  );
}
