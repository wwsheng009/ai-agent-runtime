// P0-6：workspace 命名空间按 feature/面板继续拆分后在此组合。
// 新增面板文案写入各自子模块（`panels/<area>.ts`），避免多批次并行编辑同一文件；
// `base.ts` 为原 `workspace.ts` 的机械迁移，仅搬迁不改语义。
import { zhWorkspaceBase } from "./base";
import { zhWorkspacePanelsArtifacts } from "./panels-artifacts";
import { zhWorkspacePanelsInteractions } from "./panels-interactions";
import { zhWorkspacePanelsMessages } from "./panels-messages";
import { zhWorkspacePanelsShell } from "./panels-shell";
import { zhWorkspacePanelsTeamsDispatch } from "./panels-teams-dispatch";
import { zhWorkspacePanelsTeamsPanels } from "./panels-teams-panels";

export const zhWorkspace = {
  ...zhWorkspaceBase,
  panels: {
    artifacts: zhWorkspacePanelsArtifacts,
    interactions: zhWorkspacePanelsInteractions,
    messages: zhWorkspacePanelsMessages,
    shell: zhWorkspacePanelsShell,
    teamsDispatch: zhWorkspacePanelsTeamsDispatch,
    teamsPanels: zhWorkspacePanelsTeamsPanels,
  },
} as const;
