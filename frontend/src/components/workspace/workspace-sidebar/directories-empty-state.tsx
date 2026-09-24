// P0-2 拆分：目录会话树的空态（原 directories-section.tsx L257-L283）。
// 拆出原因：段主体超 500 非空行门禁；空态与段逻辑无耦合，只依赖查询态与两个回调。

import { FolderPlusIcon } from "lucide-react";
import { type TFunction } from "i18next";

import { Button } from "@/components/ui/button";

type DirectoriesEmptyStateProps = {
  deferredQuery: string;
  hiddenArchivedCount: number;
  hasRegisteredDirectories: boolean;
  /** 「添加目录」主操作（父段负责清错误 + 打开弹层）。 */
  onAdd: () => void;
  t: TFunction<"workspace">;
};

export function DirectoriesEmptyState({
  deferredQuery,
  hiddenArchivedCount,
  hasRegisteredDirectories,
  onAdd,
  t,
}: DirectoriesEmptyStateProps) {
  return (
    <div className="rounded-card border border-dashed border-border px-3 py-3 text-sm leading-6 text-muted-foreground">
      <p>
        {deferredQuery
          ? t("sidebar.emptySessions.search")
          : hiddenArchivedCount > 0
            ? t("sidebar.emptySessions.allArchived")
            : hasRegisteredDirectories
              ? t("sidebar.emptySessions.default")
              : t("sidebar.directories.empty")}
      </p>
      {deferredQuery || hiddenArchivedCount > 0 ? null : (
        <Button variant="secondary" size="sm" className="mt-2" onClick={onAdd}>
          <FolderPlusIcon size={13} />
          {t("sidebar.directories.add")}
        </Button>
      )}
    </div>
  );
}
