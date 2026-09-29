// P3：后台任务行动作按钮 —— 按 `jobAvailableActions` 状态矩阵渲染；
// 同一 job 有在途动作时统一禁用，并把该动作的按钮文案切到「进行中」。

import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  jobActionLabelKey,
  jobActionPendingLabelKey,
  jobAvailableActions,
  type JobAction,
  type JobActionPending,
} from "@/components/workspace/jobs-panel-shared";
import type { RuntimeJobStatus } from "@/types/runtime";

export type JobActionsProps = {
  jobId: string;
  status: RuntimeJobStatus;
  pendingAction: JobActionPending;
  onAction: (jobId: string, action: JobAction) => void;
};

export function JobActions({
  jobId,
  onAction,
  pendingAction,
  status,
}: JobActionsProps) {
  const { t } = useTranslation("workspace");
  const actions = jobAvailableActions(status);
  if (actions.length === 0) {
    return null;
  }
  const inFlight =
    pendingAction && pendingAction.jobId === jobId ? pendingAction.action : null;

  return (
    <div className="flex shrink-0 items-center gap-0.5">
      {actions.map((action) => (
        <Button
          disabled={inFlight !== null}
          key={action}
          onClick={() => onAction(jobId, action)}
          size="sm"
          variant="ghost"
        >
          {inFlight === action
            ? t(jobActionPendingLabelKey(action))
            : t(jobActionLabelKey(action))}
        </Button>
      ))}
    </div>
  );
}
