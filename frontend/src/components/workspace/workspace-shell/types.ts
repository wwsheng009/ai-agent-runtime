// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type Thread } from "@/data/mock";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import type { SessionBacktrackDialogState } from "@/hooks/workspace/use-session-backtrack";
import { type RuntimeClientIdentity } from "@/lib/runtime-client";
import {
  type RuntimeSessionBacktrackMode,
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
  type RuntimeWorkspaceDirectory,
} from "@/lib/runtime-api";
import { type ChatStreamPhase } from "@/types/runtime";

export type WorkspaceShellProps = {
  threads: Thread[];
  runtimeTeams: RuntimeTeamRecord[];
  runtimeTeamsError: string | null;
  runtimeTeamsLoading: boolean;
  runtimeTeamsRefreshing?: boolean;
  runtimeTeamSummaries: RuntimeTeamSummaryEntry[];
  runtimeSessionsError: string | null;
  runtimeSessions: RuntimeSessionRecord[];
  runtimeSessionsLoading: boolean;
  runtimeSessionsRefreshing?: boolean;
  runtimeSessionsSummary: RuntimeSessionsSummary;
  runtimeSessionDefaultUserId?: string;
  runtimeSessionUsers: RuntimeSessionUserSummary[];
  runtimeSessionUsersError: string | null;
  runtimeSessionUsersLoading: boolean;
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
  onAddWorkspaceDirectory: (path: string, name?: string) => Promise<unknown>;
  onRenameWorkspaceDirectory: (id: string, name: string) => Promise<void>;
  onRemoveWorkspaceDirectory: (id: string) => Promise<void>;
  onCreateSessionInDirectory: (request: {
    path: string;
    directoryId?: string;
    label: string;
  }) => Promise<void>;
  onRenameRuntimeSession: (sessionId: string, title: string) => Promise<void>;
  runtimeClient: RuntimeClientIdentity;
  selectedRuntimeSessionUserId: string;
  selectedThread: Thread;
  selectedArtifact: Artifact | null;
  selectedArtifactId: string | null;
  draft: string;
  isResponding: boolean;
  modelOptions: string[];
  phase?: ChatStreamPhase | null;
  reasoningEffortDefault: string;
  reasoningEffortError: string | null;
  reasoningEffortOptions: string[];
  trajectoryStore?: import("@/hooks/workspace/use-trajectory-snapshot").TrajectoryStore | null;
  onDraftChange: (value: string) => void;
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  onReasoningEffortChange: (value: string) => void;
  onSelectArtifact: (artifactId: string) => void;
  onSelectThread: (threadId: string) => void;
  onRefreshRuntimeTeams?: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  onResetRuntimeClientIdentity: () => void;
  onStopResponding: () => void;
  onSubmit: () => void;
  onBacktrackToMessage?: (
    messageId: string,
    mode?: "conversation" | "both",
    options?: { editPrompt?: string },
  ) => void;
  backtrackDialog?: SessionBacktrackDialogState;
  backtrackError?: string | null;
  backtrackNotice?: string | null;
  backtrackPendingMessageId?: string | null;
  backtrackNavigationActive?: boolean;
  backtrackSelectedMessageId?: string | null;
  canBacktrack?: boolean;
  onCloseBacktrackDialog?: () => void;
  onConfirmBacktrack?: () => void;
  onBacktrackEditPromptChange?: (value: string) => void;
  onBacktrackModeChange?: (mode: RuntimeSessionBacktrackMode) => void;
  onBacktrackPrefillChange?: (prefill: boolean) => void;
  onSelectBacktrackNavigationMessage?: (messageId: string) => void;
  providerOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  selectedModel: string;
  selectedProvider: string;
  selectedReasoningEffort: string;
};
