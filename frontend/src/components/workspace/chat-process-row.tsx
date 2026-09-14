// 批次 B1（§8.4/§8.5）：过程行共享控件 —— context / Think / tool / system-prompt 四处复用。
//
// 形制：24px 单行 = 前导位 + 图标 + 标题 + 分隔点 + 摘要 + 右侧展开态。
// 职责边界：只做结构与交互接线；字号 / 行高一律由 `.app-chat-process-row`
// （次级字号轴 + 24px + delta 行高盒）派生，控件内不写死字号。
//
// 两种宿主形态（DOM 结构一致，仅整行可点性不同）：
// - 纯文本摘要：整行是一个 button（整行可点，键盘可达）；
// - 含交互元素摘要（路径 / URL 链接）：整行是 div + 右侧独立展开按钮，
//   避免 button 嵌套交互元素；此时**前导图标同样可点**（指针用户不必瞄准右侧
//   chevron），键盘仍只保留右侧一个 Tab 停靠点。

import { ChevronRightIcon } from "lucide-react";
import { type ReactNode } from "react";

import { cn } from "@/lib/utils";

export type ChatProcessRowKind =
  | "context"
  | "reasoning"
  | "system-prompt"
  | "tool";

/** 过程行 → flow 锚点 kind（§8.4：推理归入 assistant-step，工具行归 tool-call）。 */
const FLOW_KIND: Record<ChatProcessRowKind, string> = {
  context: "context",
  reasoning: "assistant-step",
  "system-prompt": "system-prompt",
  tool: "tool-call",
};

type ChatProcessRowProps = {
  /** 展开面板内容（仅展开态渲染）。 */
  children?: ReactNode;
  className?: string;
  /** 展开态。 */
  expanded?: boolean;
  /** 是否可展开；false 时不渲染展开按钮，行内容不可点。 */
  expandable?: boolean;
  /** 行锚点：flow 跳转 / e2e 定位用（§8.4 规则 4）。 */
  flowKey?: string;
  anchorKey?: string;
  icon: ReactNode;
  /** 摘要内含链接等交互元素时为 true（决定整行是否为 button）。 */
  interactiveSummary?: boolean;
  onToggle?: () => void;
  panelId?: string;
  rowKind: ChatProcessRowKind;
  /** 屏幕阅读器状态播报（工具行沿用既有 role="status" 契约）。 */
  statusLabel?: string;
  summary?: ReactNode;
  title: ReactNode;
  titleClassName?: string;
  /** 失败/告警语调：只改标题色，不整行变红底（§8.5）。 */
  tone?: "default" | "danger";
  /** 独立展开按钮的无障碍名称（i18n 文案由消费方提供）。 */
  toggleLabel?: string;
  trailing?: ReactNode;
};

export function ChatProcessRow({
  children,
  className,
  expanded = false,
  expandable = false,
  flowKey,
  anchorKey,
  icon,
  interactiveSummary = false,
  onToggle,
  panelId,
  rowKind,
  statusLabel,
  summary,
  title,
  titleClassName,
  tone = "default",
  toggleLabel,
  trailing,
}: ChatProcessRowProps) {
  const chevron = (
    <ChevronRightIcon
      aria-hidden="true"
      className={cn(
        "size-4 shrink-0 text-muted-foreground transition-transform duration-200",
        expanded ? "rotate-90" : "rotate-0",
      )}
      data-chat-row-chevron="true"
    />
  );

  const leadingIcon = (
    <span
      aria-hidden="true"
      className="flex size-4 shrink-0 items-center justify-center"
      data-chat-row-leading="icon"
    >
      {icon}
    </span>
  );

  // 含交互元素摘要时整行不是 button：前导图标升级为独立展开入口（与右侧 chevron 同义）。
  // `tabIndex={-1}` 只把它移出 Tab 顺序，指针 / 语音控制仍可直接命中，避免每行两个键盘停靠点。
  const leading =
    interactiveSummary && expandable ? (
      <button
        aria-controls={panelId}
        aria-expanded={expanded}
        aria-label={toggleLabel}
        className="-ml-0.5 shrink-0 cursor-pointer rounded-chip p-0.5 text-muted-foreground transition hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        data-chat-row-toggle="icon"
        onClick={onToggle}
        tabIndex={-1}
        type="button"
      >
        {leadingIcon}
      </button>
    ) : (
      leadingIcon
    );

  const content = (
    <>
      {statusLabel ? (
        <span className="sr-only" role="status">
          {statusLabel}
        </span>
      ) : null}
      {leading}
      <span
        className={cn(
          "shrink-0 truncate app-text-13",
          tone === "danger" ? "text-accent-orange" : "text-foreground",
          titleClassName,
        )}
        data-chat-row-title="true"
      >
        {title}
      </span>
      {summary ? (
        <>
          <span aria-hidden="true" className="chat-row-sep" />
          <span
            className="min-w-0 flex-1 truncate text-muted-foreground"
            data-chat-row-summary="true"
          >
            {summary}
          </span>
        </>
      ) : (
        <span className="min-w-0 flex-1" />
      )}
      {trailing}
      {expandable && !interactiveSummary ? chevron : null}
    </>
  );

  const rowClass = cn(
    "flex w-full min-w-0 items-center gap-1.5 rounded-md px-1 app-chat-process-row transition-colors",
    expandable ? "cursor-pointer hover:bg-surface-soft" : null,
    className,
  );

  return (
    <div
      className="min-w-0"
      data-chat-anchor-key={anchorKey}
      data-chat-flow-key={flowKey}
      data-chat-flow-kind={FLOW_KIND[rowKind]}
      data-chat-row={rowKind}
      data-chat-row-state={expanded ? "open" : "closed"}
    >
      {interactiveSummary ? (
        <div className={rowClass}>
          <div className="flex min-w-0 flex-1 items-center gap-1.5">
            {content}
          </div>
          {expandable ? (
            <button
              aria-controls={panelId}
              aria-expanded={expanded}
              aria-label={toggleLabel}
              className="-mr-0.5 shrink-0 cursor-pointer rounded-chip p-0.5 text-muted-foreground transition hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
              data-chat-row-toggle="chevron"
              onClick={onToggle}
              type="button"
            >
              {chevron}
            </button>
          ) : null}
        </div>
      ) : (
        <button
          aria-controls={expandable ? panelId : undefined}
          aria-expanded={expandable ? expanded : undefined}
          className={cn(rowClass, "text-left")}
          onClick={expandable ? onToggle : undefined}
          type="button"
        >
          {content}
        </button>
      )}
      {expandable && expanded ? (
        <div data-chat-row-panel={rowKind} id={panelId}>
          {children}
        </div>
      ) : null}
    </div>
  );
}
