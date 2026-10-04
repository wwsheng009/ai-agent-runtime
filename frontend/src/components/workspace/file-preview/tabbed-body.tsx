// P2-1A 扩展：文件预览弹层的「原始 / Markdown 预览」页签与其渲染体。
//
// 只有「Markdown 文本文件」才出现页签（判定见 isMarkdownPath，与文件浏览器同一份扩展名表）；
// 其余分支（loading / error / tooLarge / empty / binary / 非 Markdown 文本）不改变既有形制。
//
// 渲染体与聊天共用 MessageMarkdown（同一套标题/列表/表格/代码块样式与链接、图片安全策略），
// 不做本地改写、不额外注入内容；解析成本随正文线性增长，超上限时如实提示并保留「原始」页签。

import { EyeIcon, FileCode2Icon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { TabSwitcher, type TabSwitcherItem } from "@/components/ui/tab-switcher";
import {
  FILE_PREVIEW_BODY_PANEL_ID,
  filePreviewTabId,
  type FilePreviewBodyTab,
} from "@/lib/file-preview/tabs";

/**
 * 弹层内 Markdown 渲染上限（字符数）。
 *
 * 读文件端点只按字节收口（FILE_PREVIEW_MAX_BYTES = 1MB），ReactMarkdown 仍在主线程解析并
 * 构建组件树；1MB 级 Markdown 会长时间阻塞弹层。这里取一个保守上限（未做基准实测），
 * 超过就如实提示并让用户切回「原始」，而不是让弹层卡住。
 */
export const MARKDOWN_PREVIEW_MAX_CHARS = 200_000;

type FilePreviewBodyTabsProps = {
  activeTab: FilePreviewBodyTab;
  onSelectTab: (tab: FilePreviewBodyTab) => void;
};

export function FilePreviewBodyTabs({
  activeTab,
  onSelectTab,
}: FilePreviewBodyTabsProps) {
  const { t } = useTranslation("workspace");
  const items: TabSwitcherItem<FilePreviewBodyTab>[] = [
    {
      id: "raw",
      label: t("panels.filePreview.tabs.raw"),
      icon: <FileCode2Icon aria-hidden size={14} />,
      tabId: filePreviewTabId("raw"),
      panelId: FILE_PREVIEW_BODY_PANEL_ID,
      testId: "file-preview-tab-raw",
    },
    {
      id: "markdown",
      label: t("panels.filePreview.tabs.preview"),
      icon: <EyeIcon aria-hidden size={14} />,
      tabId: filePreviewTabId("markdown"),
      panelId: FILE_PREVIEW_BODY_PANEL_ID,
      testId: "file-preview-tab-markdown",
    },
  ];

  return (
    <div className="border-b border-border px-3.5 py-2 sm:px-4">
      <TabSwitcher
        ariaLabel={t("panels.filePreview.tabs.ariaLabel")}
        items={items}
        onChange={onSelectTab}
        value={activeTab}
        variant="plain"
      />
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
