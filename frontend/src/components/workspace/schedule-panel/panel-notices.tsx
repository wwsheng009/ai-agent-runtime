// Schedule 面板的状态提示：操作结果、删除等待撤销与目录错误。

import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import type { RuntimeScheduleTask } from "@/types/runtime";

export type SchedulePanelNoticesProps = {
  errorMessage: string | null;
  notice: string | null;
  onUndoDelete: () => void;
  pendingDelete: RuntimeScheduleTask | null;
  pendingDeleteCommitting: boolean;
};

export function SchedulePanelNotices({
  errorMessage,
  notice,
  onUndoDelete,
  pendingDelete,
  pendingDeleteCommitting,
}: SchedulePanelNoticesProps) {
  const { t } = useTranslation("workspace");

  return (
    <>
      {notice && (
        <p
          aria-live="polite"
          className="rounded border border-border px-2 py-1 text-xs text-muted-foreground"
          role="status"
        >
          {notice}
        </p>
      )}
      {pendingDelete ? (
        <p
          aria-live="polite"
          className="flex items-center justify-between gap-2 rounded border border-border px-2 py-1 text-xs text-muted-foreground"
          role="status"
        >
          <span>
            {t(
              pendingDeleteCommitting
                ? "panels.schedule.deleting"
                : "panels.schedule.deleteUndoPending",
              pendingDeleteCommitting ? undefined : { seconds: "5" },
            )}
          </span>
          {!pendingDeleteCommitting ? (
            <Button onClick={onUndoDelete} size="sm" variant="ghost">
              {t("panels.schedule.undoDelete")}
            </Button>
          ) : null}
        </p>
      ) : null}
      {errorMessage && (
        <p
          className="rounded border border-destructive/40 px-2 py-1 text-xs text-destructive"
          role="alert"
        >
          {errorMessage}
        </p>
      )}
    </>
  );
}