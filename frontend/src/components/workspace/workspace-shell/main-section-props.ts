// WorkspaceMainSection 的 props 类型抽离文件。
//
// 为什么单独成文件：main-section.tsx 已逼近 P0-2 的 500 非空行上限，
// 纯类型（编译期零运行时语义）搬出来最不容易引入行为回归，
// 也不需要像抽组件那样重新梳理 render 依赖；与 workspace-shell.tsx
// 的机械拆分同一思路，仅搬迁不改语义。

import { type CSSProperties, type Dispatch, type RefObject, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import { type SettingsSectionId } from "@/components/workspace/settings";
import { type NewThreadSuggestion } from "@/components/workspace/workspace-shell/new-thread-placeholder";
import {
  type WorkspaceShellProps,
  type WorkspaceViewMode,
} from "@/components/workspace/workspace-shell/types";
import { type WorkspaceDensity } from "@/core/settings";

export type WorkspaceMainSectionProps = Pick<
  WorkspaceShellProps,
  | "backtrackError"
  | "backtrackNavigationActive"
  | "backtrackNotice"
  | "backtrackPendingMessageId"
  | "backtrackSelectedMessageId"
  | "branchError"
  | "branchPendingMessageId"
  | "canBacktrack"
  | "composerAttachments"
  | "connectionStatus"
  | "draft"
  | "earlierLoader"
  | "isResponding"
  | "modelOptions"
  | "onAnswerPendingQuestion"
  | "onBacktrackToMessage"
  | "onBranchFromMessage"
  | "onDraftChange"
  | "onModelChange"
  | "onProviderChange"
  | "onReasoningEffortChange"
  | "onRenameRuntimeSession"
  | "onResolvePendingApproval"
  | "onRefreshSession"
  | "onRetryConnection"
  | "onPlanDecision"
  | "onPlanNotesChange"
  | "onSelectBacktrackNavigationMessage"
  | "onStopResponding"
  | "onSubmit"
  | "pendingInteraction"
  | "phase"
  | "planActionPending"
  | "planNotesDraft"
  | "providerOptions"
  | "reasoningEffortDefault"
  | "reasoningEffortError"
  | "reasoningEffortOptions"
  | "runtimeModels"
  | "runtimeModelsError"
  | "runtimeModelsLoading"
  | "runtimeSkills"
  | "runtimeSkillsError"
  | "runtimeSkillsLoading"
  | "selectedModel"
  | "selectedProvider"
  | "selectedReasoningEffort"
  | "selectedThread"
  | "sessionRefreshing"
  | "streamStalled"
  | "trajectoryStore"
  | "trajectoryEarlier"
> & {
  composerOverlayHeight: number;
  composerOverlayRef: RefObject<HTMLDivElement | null>;
  density: WorkspaceDensity;
  handleOpenArtifact: (artifactId: string) => void;
  isCompact: boolean;
  isNewThread: boolean;
  liveTeamCount: number;
  messageListStyle: CSSProperties | undefined;
  newThreadSuggestions: readonly NewThreadSuggestion[];
  openSettings: (section?: SettingsSectionId) => void;
  onToggleRightRail: () => void;
  rightRailOpen: boolean;
  setMobileSidebarOpen: Dispatch<SetStateAction<boolean>>;
  setViewMode: Dispatch<SetStateAction<WorkspaceViewMode>>;
  t: TFunction<"workspace">;
  threadStatusLabel: string;
  threadSubtitle: string;
  transportLabel: string;
  viewMode: WorkspaceViewMode;
};
