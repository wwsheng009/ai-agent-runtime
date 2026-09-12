// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Dispatch, type SetStateAction, Suspense } from "react";
import { type TFunction } from "i18next";

import { MessageBacktrackDialog } from "@/components/workspace/message-backtrack-dialog";
import { type SettingsSectionId } from "@/components/workspace/settings";

import {
  ArtifactDetailDialog,
  ArtifactDialogFallback,
  SettingsDialog,
  SettingsDialogFallback,
} from "./lazy-surfaces";
import { type WorkspaceShellProps } from "./types";

type WorkspaceOverlaysSectionProps = Pick<
  WorkspaceShellProps,
  | "backtrackDialog"
  | "modelOptions"
  | "onBacktrackEditPromptChange"
  | "onBacktrackModeChange"
  | "onBacktrackPrefillChange"
  | "onCloseBacktrackDialog"
  | "onConfirmBacktrack"
  | "onModelChange"
  | "onProviderChange"
  | "onResetRuntimeClientIdentity"
  | "providerOptions"
  | "runtimeClient"
  | "runtimeModelsError"
  | "runtimeModelsLoading"
  | "runtimeSessionsSummary"
  | "runtimeTeams"
  | "selectedArtifact"
  | "selectedModel"
  | "selectedProvider"
> & {
  artifactDialogOpen: boolean;
  setArtifactDialogOpen: Dispatch<SetStateAction<boolean>>;
  setSettingsDialogOpen: Dispatch<SetStateAction<boolean>>;
  settingsDialogOpen: boolean;
  settingsSection: SettingsSectionId;
  t: TFunction<"workspace">;
};

export function WorkspaceOverlaysSection({
  backtrackDialog,
  modelOptions,
  onBacktrackEditPromptChange,
  onBacktrackModeChange,
  onBacktrackPrefillChange,
  onCloseBacktrackDialog,
  onConfirmBacktrack,
  onModelChange,
  onProviderChange,
  onResetRuntimeClientIdentity,
  providerOptions,
  runtimeClient,
  runtimeModelsError,
  runtimeModelsLoading,
  runtimeSessionsSummary,
  runtimeTeams,
  selectedArtifact,
  selectedModel,
  selectedProvider,
  artifactDialogOpen,
  setArtifactDialogOpen,
  setSettingsDialogOpen,
  settingsDialogOpen,
  settingsSection,
  t,
}: WorkspaceOverlaysSectionProps) {
  return (
    <>
      {artifactDialogOpen && selectedArtifact ? (
        <Suspense fallback={<ArtifactDialogFallback message={t("shell.loadingArtifactDetails")} />}>
          <ArtifactDetailDialog
            artifact={selectedArtifact}
            onClose={() => setArtifactDialogOpen(false)}
            open={artifactDialogOpen}
          />
        </Suspense>
      ) : null}
      {settingsDialogOpen ? (
        <Suspense fallback={<SettingsDialogFallback message={t("shell.loadingSettingsPanel")} />}>
          <SettingsDialog
            defaultSection={settingsSection}
            modelOptions={modelOptions}
            onClose={() => setSettingsDialogOpen(false)}
            onModelChange={onModelChange}
            onProviderChange={onProviderChange}
            open={settingsDialogOpen}
            providerOptions={providerOptions}
            runtimeModelsError={runtimeModelsError}
            runtimeModelsLoading={runtimeModelsLoading}
            runtimeSessionsSummary={runtimeSessionsSummary}
            runtimeClient={runtimeClient}
            runtimeTeams={runtimeTeams}
            onResetRuntimeClientIdentity={onResetRuntimeClientIdentity}
            selectedModel={selectedModel}
            selectedProvider={selectedProvider}
          />
        </Suspense>
      ) : null}
      {backtrackDialog?.open &&
      onCloseBacktrackDialog &&
      onConfirmBacktrack &&
      onBacktrackEditPromptChange &&
      onBacktrackModeChange &&
      onBacktrackPrefillChange ? (
        <MessageBacktrackDialog
          onApply={onConfirmBacktrack}
          onClose={onCloseBacktrackDialog}
          onEditPromptChange={onBacktrackEditPromptChange}
          onModeChange={onBacktrackModeChange}
          onPrefillChange={onBacktrackPrefillChange}
          state={backtrackDialog}
        />
      ) : null}
    </>
  );
}
