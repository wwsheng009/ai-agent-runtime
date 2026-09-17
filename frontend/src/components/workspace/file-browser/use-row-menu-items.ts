// 右键菜单项构造（P4-3/P4-4）：把「命中多选 → 批量 / 否则单行」的组装从面组件里抽出来，
// 面组件只持有 `rowMenu` 状态与回调（hook 文件不导出组件，`react-refresh` 纪律同其它 hook）。
//
// 纪律（见 `row-menu.tsx`）：禁用项**必须**给出原因——目录没有 zip 端点、作用域根缺失时复制绝对路径
// 只会猜路径，两种情况都显式降级为「禁用 + 原因」，不静默禁用、不假装可用。
import { useTranslation } from "react-i18next";

import type { FileRowMenuItem } from "@/components/workspace/file-browser/row-menu";
import type { FsEntry } from "@/types/runtime/fs-browser";

export type UseRowMenuItemsParams = {
  /** 右键命中的行；未打开菜单时为 null（返回空数组）。 */
  rowMenuEntry: FsEntry | null;
  /** 当前选中集合（判断右键行是否属于多选）。 */
  selectedSet: ReadonlySet<string>;
  /** 选中集合对应的真实 entry（批量动作的对象）。 */
  selectedEntries: readonly FsEntry[];
  /** 作用域绝对根：缺失时「复制路径」禁用并给原因（不猜路径）。 */
  activeRootPath: string;
  onDownload: (entries: readonly FsEntry[]) => void;
  onCopyPaths: (entries: readonly FsEntry[]) => void;
};

/** 受控菜单项：空数组 = 未打开菜单（`FileRowMenu` 据此不渲染）。 */
export function useRowMenuItems({
  rowMenuEntry,
  selectedSet,
  selectedEntries,
  activeRootPath,
  onDownload,
  onCopyPaths,
}: UseRowMenuItemsParams): FileRowMenuItem[] {
  const { t } = useTranslation("workspace");
  // 命中多选时按「所选」批量，否则只作用于当前行。
  const menuEntries =
    rowMenuEntry && selectedSet.has(rowMenuEntry.path) && selectedEntries.length > 1
      ? selectedEntries
      : rowMenuEntry
        ? [rowMenuEntry]
        : [];
  const menuFiles = menuEntries.filter((entry) => entry.type === "file");
  if (menuEntries.length === 0) {
    return [];
  }
  return [
    {
      id: "download",
      label:
        menuFiles.length > 1
          ? t("panels.fileBrowser.menu.downloadSelected", { count: menuFiles.length })
          : t("panels.fileBrowser.menu.download"),
      onSelect: () => onDownload(menuFiles),
      disabled: menuFiles.length === 0,
      hint:
        menuFiles.length === 0
          ? t("panels.fileBrowser.menu.downloadUnavailable")
          : t("panels.fileBrowser.menu.downloadHint"),
    },
    {
      id: "copy-path",
      label:
        menuEntries.length > 1
          ? t("panels.fileBrowser.menu.copySelected", { count: menuEntries.length })
          : t("panels.fileBrowser.menu.copyPath"),
      onSelect: () => onCopyPaths(menuEntries),
      disabled: activeRootPath.length === 0,
      hint: activeRootPath
        ? t("panels.fileBrowser.menu.copyPathHint")
        : t("panels.fileBrowser.menu.copyPathUnavailable"),
    },
  ];
}
