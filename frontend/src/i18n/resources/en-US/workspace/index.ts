import type { DeepStringShape } from "../../shape";
import type { zhWorkspace } from "../../zh-CN/workspace";
import { enWorkspaceBase } from "./base";
import { enWorkspacePanelsArtifacts } from "./panels-artifacts";
import { enWorkspacePanelsInteractions } from "./panels-interactions";
import { enWorkspacePanelsMessages } from "./panels-messages";
import { enWorkspacePanelsShell } from "./panels-shell";
import { enWorkspacePanelsTeamsDispatch } from "./panels-teams-dispatch";
import { enWorkspacePanelsTeamsPanels } from "./panels-teams-panels";

export const enWorkspace = {
  ...enWorkspaceBase,
  panels: {
    artifacts: enWorkspacePanelsArtifacts,
    interactions: enWorkspacePanelsInteractions,
    messages: enWorkspacePanelsMessages,
    shell: enWorkspacePanelsShell,
    teamsDispatch: enWorkspacePanelsTeamsDispatch,
    teamsPanels: enWorkspacePanelsTeamsPanels,
  },
} satisfies DeepStringShape<typeof zhWorkspace>;
