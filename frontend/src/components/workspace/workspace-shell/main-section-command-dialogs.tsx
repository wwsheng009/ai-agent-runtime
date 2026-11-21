// WorkspaceMainSection 底部的 `/` 命令弹窗（`/model` / `/skill` / `/profile`）。
//
// 为什么单独成文件：main-section.tsx 已逼近 P0-2 的 500 非空行上限；
// 这三张弹窗共用 composer 命令面与运行时 profile 目录，按机械拆分口径整块搬迁。

import { ComposerModelDialog } from "@/components/workspace/composer-model-dialog";
import { ComposerProfileDialog } from "@/components/workspace/composer-profile-dialog";
import { ComposerSkillDialog } from "@/components/workspace/composer-skill-dialog";
import { type ComposerCommandSurface } from "@/hooks/workspace/composer/use-composer-command-surface";
import { type RuntimeProfileCatalogState } from "@/hooks/workspace/composer/use-runtime-profile-catalog";

import { type WorkspaceMainSectionProps } from "./main-section-props";

type WorkspaceMainSectionCommandDialogsProps = {
  commandSurface: ComposerCommandSurface;
  onModelChange: (value: string) => void;
  profileCatalog: RuntimeProfileCatalogState;
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  runtimeSkills: WorkspaceMainSectionProps["runtimeSkills"];
  runtimeSkillsError: string | null;
  runtimeSkillsLoading: boolean;
  selectedModel: string;
  selectedProvider: string;
};

export function WorkspaceMainSectionCommandDialogs({
  commandSurface,
  onModelChange,
  profileCatalog,
  runtimeModelsError,
  runtimeModelsLoading,
  runtimeSkills,
  runtimeSkillsError,
  runtimeSkillsLoading,
  selectedModel,
  selectedProvider,
}: WorkspaceMainSectionCommandDialogsProps) {
  return (
    <>
      <ComposerModelDialog
        error={runtimeModelsError}
        groups={commandSurface.modelGroups}
        loading={runtimeModelsLoading}
        onClose={commandSurface.closeModelDialog}
        onSelect={(model) => {
          onModelChange(model);
          commandSurface.closeModelDialog();
        }}
        open={commandSurface.modelDialogOpen}
        selectedModel={selectedModel}
        selectedProvider={selectedProvider}
      />
      <ComposerSkillDialog
        error={runtimeSkillsError}
        loading={runtimeSkillsLoading}
        onClose={commandSurface.closeSkillDialog}
        onSelect={commandSurface.selectSkill}
        open={commandSurface.skillDialogOpen}
        skills={runtimeSkills?.skills.map((skill) => skill.name) ?? []}
      />
      <ComposerProfileDialog
        candidates={commandSurface.profileCandidates}
        error={profileCatalog.error}
        loading={profileCatalog.loading}
        onClose={commandSurface.closeProfileDialog}
        onRetry={profileCatalog.refresh}
        onSelect={commandSurface.applyProfile}
        open={commandSurface.profileDialogOpen}
      />
    </>
  );
}