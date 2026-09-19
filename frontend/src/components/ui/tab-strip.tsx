// 通用页签条（通用 tab 组件）：把「页签列表 + 激活态 + 可选关闭」从具体业务里抽出来，
// 调用方只给数据与回调，不关心渲染形制——文件管理器用它承载「文件浏览器 + 已打开文件」。
//
// 归一化纪律：
//   * 只做受控渲染：激活态、列表顺序与关闭语义全部由调用方持有，本组件不引入内部状态；
//   * 关闭按钮只对 `closable` 项渲染，非可关项（如根页签）不出现关闭入口；
//   * 页签多到放不下时整条横向滚动（`overflow-x-auto`），不压缩标签。
//
// 无障碍：role=tablist / role=tab + aria-selected；关闭按钮自带 aria-label（由调用方给文案）。

import { XIcon } from "lucide-react";

import { cn } from "@/lib/utils";

import { closeTabTestId, tabTestId } from "./tab-strip-shared";

export type WorkspaceTabStripItem = {
  /** 页签身份（激活态 / 关闭目标）。 */
  id: string;
  /** 页签标题文本。 */
  label: string;
  /** 可关闭页签渲染关闭按钮；非可关项不渲染。 */
  closable?: boolean;
  /** 悬浮完整标题（如长路径），缺省回退到 label。 */
  title?: string;
};

export type WorkspaceTabStripProps = {
  items: WorkspaceTabStripItem[];
  /** 当前激活页签 id；null = 无激活项（依旧渲染条，调用方自行处理空态）。 */
  activeId: string | null;
  onSelect: (id: string) => void;
  /** 关闭回调；只有 closable 项会触发。缺省时关闭按钮不渲染。 */
  onClose?: (id: string) => void;
  /** tablist 的无障碍标签。 */
  ariaLabel: string;
  /** 关闭按钮的无障碍标签（i18n 由调用方取词，这里只给条目以便拼文案）。 */
  closeLabel?: (item: WorkspaceTabStripItem) => string;
  /** 页签 data-testid 前缀；缺省回退 `workspace-tab`。 */
  testIdBase?: string;
  className?: string;
};

export function WorkspaceTabStrip({
  activeId,
  ariaLabel,
  className,
  closeLabel,
  items,
  onClose,
  onSelect,
  testIdBase = "workspace-tab",
}: WorkspaceTabStripProps) {
  return (
    <div
      aria-label={ariaLabel}
      className={cn(
        "flex shrink-0 items-center gap-1 overflow-x-auto border-b border-border/60 px-1.5 pt-1.5 app-text-12",
        className,
      )}
      data-testid={`${testIdBase}-strip`}
      role="tablist"
    >
      {items.map((item) => {
        const active = item.id === activeId;
        return (
          <div
            className={cn(
              "group flex shrink-0 items-center rounded-t-md border border-b-0",
              active
                ? "border-border bg-surface-softer text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
            key={item.id}
          >
            <button
              aria-selected={active}
              className="max-w-[14rem] truncate px-2.5 py-1.5"
              data-testid={tabTestId(testIdBase, item.id)}
              onClick={() => onSelect(item.id)}
              role="tab"
              title={item.title ?? item.label}
              type="button"
            >
              {item.label}
            </button>
            {item.closable && onClose ? (
              <button
                aria-label={closeLabel ? closeLabel(item) : `Close ${item.label}`}
                className="mr-1 inline-flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-white/10 hover:text-foreground"
                data-testid={closeTabTestId(testIdBase, item.id)}
                onClick={() => onClose(item.id)}
                title={closeLabel ? closeLabel(item) : undefined}
                type="button"
              >
                <XIcon aria-hidden className="size-3" />
              </button>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}