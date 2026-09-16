// P2-1A 扩展：文件预览弹层的「原始 / Markdown 预览」页签与其渲染体。
//
// 只有「Markdown 文本文件」才出现页签（判定见 isMarkdownPath，与文件浏览器同一份扩展名表）；
// 其余分支（loading / error / tooLarge / empty / binary / 非 Markdown 文本）不改变既有形制。
//
// 渲染体与聊天共用 MessageMarkdown（同一套标题/列表/表格/代码块样式与链接、图片安全策略），
// 不做本地改写、不额外注入内容；解析成本随正文线性增长，超上限时如实提示并保留「原始」页签。

import { EyeIcon, FileCode2Icon } from "lucide-react";
import { type KeyboardEvent, useRef } from "react";
import { useTranslation } from "react-i18next";

import { MessageMarkdown } from "@/components/workspace/message-markdown";
import {
  FILE_PREVIEW_BODY_PANEL_ID,
  filePreviewTabId,
  type FilePreviewBodyTab,
} from "@/lib/file-preview/tabs";
import { cn } from "@/lib/utils";

/**
 * 弹层内 Markdown 渲染上限（字符数）。
 *
 * 读文件端点只按字节收口（FILE_PREVIEW_MAX_BYTES = 1MB），ReactMarkdown 仍在主线程解析并
 * 构建组件树；1MB 级 Markdown 会长时间阻塞弹层。这里取一个保守上限（未做基准实测），
 * 超过就如实提示并让用户切回「原始」，而不是让弹层卡住。
 */
export const MARKDOWN_PREVIEW_MAX_CHARS = 200_000;

const TAB_ORDER: FilePreviewBodyTab[] = ["raw", "markdown"];

type FilePreviewBodyTabsProps = {
  activeTab: FilePreviewBodyTab;
  onSelectTab: (tab: FilePreviewBodyTab) => void;
};

export function FilePreviewBodyTabs({
  activeTab,
  onSelectTab,
}: FilePreviewBodyTabsProps) {
  const { t } = useTranslation("workspace");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  // roving tabindex：焦点跟随选中项，方向键/Home/End 在页签间切换（与产物详情弹层同口径）。
  const selectAt = (index: number) => {
    const next = TAB_ORDER[index];
    if (!next) {
      return;
    }
    onSelectTab(next);
    tabRefs.current[index]?.focus();
  };

  const handleKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    let nextIndex = -1;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      nextIndex = (index + 1) % TAB_ORDER.length;
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      nextIndex = (index - 1 + TAB_ORDER.length) % TAB_ORDER.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = TAB_ORDER.length - 1;
    }
    if (nextIndex < 0) {
      return;
    }
    event.preventDefault();
    selectAt(nextIndex);
  };

  const tabClass = (active: boolean) =>
    cn(
      "inline-flex items-center gap-2 rounded-control border px-2.5 py-1 app-text-12 transition focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
      active
        ? "border-accent-gold/30 bg-accent-gold/8 text-accent-gold"
        : "border-border bg-surface-softer text-muted-foreground hover:bg-surface-solid hover:text-foreground",
    );

  const tabProps = (tab: FilePreviewBodyTab, index: number) => ({
    "aria-controls": FILE_PREVIEW_BODY_PANEL_ID,
    "aria-selected": activeTab === tab,
    className: tabClass(activeTab === tab),
    id: filePreviewTabId(tab),
    onClick: () => onSelectTab(tab),
    onKeyDown: (event: KeyboardEvent<HTMLButtonElement>) =>
      handleKeyDown(event, index),
    ref: (node: HTMLButtonElement | null) => {
      tabRefs.current[index] = node;
    },
    role: "tab" as const,
    tabIndex: activeTab === tab ? 0 : -1,
    type: "button" as const,
  });

  return (
    <div
      aria-label={t("panels.filePreview.tabs.ariaLabel")}
      aria-orientation="horizontal"
      className="flex flex-wrap items-center gap-2 border-b border-border px-3.5 py-2 sm:px-4"
      role="tablist"
    >
      <button data-testid="file-preview-tab-raw" {...tabProps("raw", 0)}>
        <FileCode2Icon aria-hidden size={14} />
        {t("panels.filePreview.tabs.raw")}
      </button>
      <button data-testid="file-preview-tab-markdown" {...tabProps("markdown", 1)}>
        <EyeIcon aria-hidden size={14} />
        {t("panels.filePreview.tabs.preview")}
      </button>
    </div>
  );
}

export function FilePreviewMarkdownBody({ text }: { text: string }) {
  const { t } = useTranslation("workspace");

  if (text.length > MARKDOWN_PREVIEW_MAX_CHARS) {
    return (
      <div
        className="rounded-card border border-accent-gold/24 bg-accent-gold/10 px-2.5 py-2 text-xs leading-5 text-foreground"
        data-testid="file-preview-markdown-too-large"
      >
        {t("panels.filePreview.body.markdownTooLarge", {
          size: String(text.length),
          limit: String(MARKDOWN_PREVIEW_MAX_CHARS),
        })}
      </div>
    );
  }

  return (
    <div data-testid="file-preview-markdown">
      <MessageMarkdown content={text} streaming={false} />
    </div>
  );
}
