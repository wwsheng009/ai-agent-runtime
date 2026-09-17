// 文件动作（P4-3/P4-4）：批量下载与「复制绝对路径」及其一次性提示。
// 从 `file-browser-surface.tsx` 抽出：面组件只做组装（同时守住 `verify-max-lines` 的 500 非空行门禁）。
//
// 归一化纪律：
//   * 下载只对 `type === "file"` 的行发请求（目录没有 zip 端点，符号链接不猜可下载性）；
//   * 复制绝对路径必须有作用域根：根缺失时不猜盘符，直接如实报失败；
//   * 剪贴板不可用（非安全上下文 / 权限被拒）→ 失败提示，不假装成功；提示 1.6s 后自动消失。
import { useCallback, useEffect, useState } from "react";

import { toAbsoluteDisplayPath } from "@/lib/file-browser/path-utils";

import type { useDownloadManager } from "@/components/workspace/file-browser/use-download-manager";
import type { FsEntry } from "@/types/runtime/fs-browser";

/** 复制路径的一次性提示（空串 = 不显示）。 */
export type BrowserCopyNotice = "" | "done" | "failed";

export type UseBrowserFileActionsParams = {
  /** 作用域绝对根：缺失时复制路径直接失败（不猜路径）。 */
  activeRootPath: string;
  /** 传输托盘开关（传 `useState` 的 setter：稳定引用，动作不必每次渲染重建）。 */
  setTrayOpen: (open: boolean) => void;
  downloadManager: ReturnType<typeof useDownloadManager>;
};

/** 菜单与工具栏共用的文件动作；返回的动作都是稳定引用（`useCallback`）。 */
export function useBrowserFileActions({ activeRootPath, setTrayOpen, downloadManager }: UseBrowserFileActionsParams) {
  const [copyNotice, setCopyNotice] = useState<BrowserCopyNotice>("");

  /** 多选下载：过滤出文件行后逐个入队，并打开托盘（空集合不入队、不弹托盘）。 */
  const startDownloads = useCallback(
    (entries: readonly FsEntry[]) => {
      const files = entries.filter((entry) => entry.type === "file");
      if (files.length === 0) {
        return;
      }
      for (const entry of files) {
        downloadManager.start(entry);
      }
      setTrayOpen(true);
    },
    [downloadManager, setTrayOpen],
  );

  /** 复制绝对路径：根缺失 / 全部拼不出绝对路径 / 剪贴板不可用都如实报失败。 */
  const copyAbsolutePaths = useCallback(
    async (entries: readonly FsEntry[]) => {
      const paths = entries
        .map((entry) => toAbsoluteDisplayPath(activeRootPath, entry.path))
        .filter((path) => path.length > 0);
      if (paths.length === 0) {
        setCopyNotice("failed");
        return;
      }
      try {
        if (!navigator.clipboard?.writeText) {
          throw new Error("clipboard unavailable");
        }
        await navigator.clipboard.writeText(paths.join("\n"));
        setCopyNotice("done");
      } catch {
        setCopyNotice("failed");
      }
    },
    [activeRootPath],
  );

  // 一次性提示：到期自动清空，避免上一轮结果被当成新一轮反馈。
  useEffect(() => {
    if (!copyNotice) {
      return;
    }
    const timer = window.setTimeout(() => setCopyNotice(""), 1600);
    return () => window.clearTimeout(timer);
  }, [copyNotice]);

  return { copyAbsolutePaths, copyNotice, startDownloads };
}
