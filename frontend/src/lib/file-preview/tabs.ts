// P2-1A 扩展：文件预览弹层「原始 / Markdown 预览」页签的类型与 DOM id 口径。
//
// 为什么不放在渲染组件旁边（components/workspace/file-preview/tabbed-body.tsx）：
// 组件模块只能导出组件与常量，多导出一个普通函数就会让 Fast Refresh 退化为整文件刷新
// （react-refresh/only-export-components 直接报错），故把 id 口径收在这里供两侧共用。

export type FilePreviewBodyTab = "raw" | "markdown";

/** 页签面板（内容区）id：页签按钮通过 `aria-controls` 指向它。 */
export const FILE_PREVIEW_BODY_PANEL_ID = "file-preview-body-panel";

const TAB_IDS: Record<FilePreviewBodyTab, string> = {
  raw: "file-preview-tab-raw",
  markdown: "file-preview-tab-markdown",
};

/** 页签按钮 id：面板的 `aria-labelledby` 指向它（与 `data-testid` 同名，便于对照定位）。 */
export function filePreviewTabId(tab: FilePreviewBodyTab): string {
  return TAB_IDS[tab];
}
