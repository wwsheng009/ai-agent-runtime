import type { Dispatch, SetStateAction } from "react";

import type { Thread } from "@/data/mock";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import type { TrajectoryStorePool } from "@/lib/trajectory/store-pool";
import type { RuntimeDeltaCoordinator } from "@/lib/workspace-thread-state";

export type WorkspaceAgentChatTurnOptions = {
  deltaCoordinator?: RuntimeDeltaCoordinator;
  onSessionTouched?: () => void;
  pendingPermissionMode?: string;
  pendingPermissionModeConfirm?: boolean;
  pendingProfileRef?: string;
  selectedThread: Thread | undefined;
  setSelectedArtifactId: (artifactId: string | null) => void;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /** 页面级池化 store（Batch 1）；缺省由本 hook 自持单实例（旧行为）。 */
  trajectoryStore?: TrajectoryStore;
  /** 页面级池：回合按**发起会话**的键取 store（后台/草稿回合不写进选中会话）。 */
  trajectoryStorePool?: TrajectoryStorePool;
  userId?: string;
  workspacePath?: string;
};
