import type { DeepStringShape } from "../../shape";
import type { zhWorkspace } from "../../zh-CN/workspace";
import { enWorkspaceBase } from "./base";
import { enWorkspacePanelsAgents } from "./panels-agents";
import { enWorkspacePanelsArtifacts } from "./panels-artifacts";
import { enWorkspaceFilePreview } from "./panels-file-preview";
import { enWorkspacePanelsInteractions } from "./panels-interactions";
import { enWorkspacePanelsJobs } from "./panels-jobs";
import { enWorkspacePanelsMessages } from "./panels-messages";
import { enWorkspaceSessionSearch } from "./panels-session-search";
import { enWorkspacePanelsShell } from "./panels-shell";
import { enWorkspacePanelsTeamsDispatch } from "./panels-teams-dispatch";
import { enWorkspacePanelsTeamsPanels } from "./panels-teams-panels";

export const enWorkspace = {
  ...enWorkspaceBase,
  panels: {
    agents: enWorkspacePanelsAgents,
    artifacts: enWorkspacePanelsArtifacts,
    filePreview: enWorkspaceFilePreview,
    interactions: enWorkspacePanelsInteractions,
    jobs: enWorkspacePanelsJobs,
    messages: enWorkspacePanelsMessages,
    sessionSearch: enWorkspaceSessionSearch,
    shell: enWorkspacePanelsShell,
    teamsDispatch: enWorkspacePanelsTeamsDispatch,
    teamsPanels: enWorkspacePanelsTeamsPanels,
  },
} satisfies DeepStringShape<typeof zhWorkspace>;
