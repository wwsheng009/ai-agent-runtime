// 停靠列（批次 F2 + §4.6）：composer 上沿的常驻模式标识与待交互卡片。
//
// 抽出来的原因有两个：main-section.tsx 已顶到 500 非空行上限，且「模式标识 + 审批/提问/
// 计划评审条」是同一宽度轴上的一个整体——放在一起才能保证两者不会各说各话
// （模式标识只读，裁决仍由下方的 pending bar 承担）。

import { PendingInteractionBar } from "@/components/workspace/pending-interaction-bar";
import { SessionModeBanner } from "@/components/workspace/session-mode-banner";
import type { ParkedTurnView } from "@/lib/parked-turn";
import type { PendingInteraction } from "@/lib/pending-interaction";
import type {
  RuntimeSessionPlanMode,
  RuntimeSessionPlanModeExitDecision,
} from "@/lib/runtime-api";

type SessionInteractionDockProps = {
  interaction: PendingInteraction | null;
  onAnswerQuestion?: (questionId: string, answer: string) => void;
  onPlanDecision?: (decision: Exclude<RuntimeSessionPlanModeExitDecision, "">) => void;
  onPlanNotesChange?: (value: string) => void;
  onResolveApproval?: (requestId: string, allow: boolean) => void;
  /** §6.8 托管挂起：挂起快照 + 任务投影（见 SessionModeBanner 的落点说明）。 */
  parkedTurn?: ParkedTurnView | null;
  plan: RuntimeSessionPlanMode | null;
  planActionPending?: boolean;
  planNotesDraft?: string;
  planStatusLabel?: string;
  sessionId?: string;
};

export function SessionInteractionDock({
  interaction,
  onAnswerQuestion,
  onPlanDecision,
  onPlanNotesChange,
  onResolveApproval,
  parkedTurn,
  plan,
  planActionPending,
  planNotesDraft,
  planStatusLabel,
  sessionId,
}: SessionInteractionDockProps) {
  return (
    <div className="pointer-events-auto mx-auto w-full max-w-[var(--app-chat-content-width-dock)]">
      <SessionModeBanner
        parkedTurn={parkedTurn ?? null}
        plan={plan}
        planStatusLabel={planStatusLabel}
        sessionId={sessionId}
      />
      <PendingInteractionBar
        interaction={interaction}
        onAnswerQuestion={(questionId, answer) => onAnswerQuestion?.(questionId, answer)}
        onPlanDecision={onPlanDecision}
        onPlanNotesChange={onPlanNotesChange}
        onResolveApproval={(requestId, allow) => onResolveApproval?.(requestId, allow)}
        planActionPending={planActionPending}
        planNotesDraft={planNotesDraft}
      />
    </div>
  );
}
