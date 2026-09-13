// 由 components/workspace/settings/harness-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { FolderLockIcon, RefreshCwIcon } from "lucide-react";
import { type Dispatch, type SetStateAction } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsSection } from "../settings-section";

export function HarnessWorkspaceSection({
  actionPending,
  displayError,
  loading,
  reload,
  setLocalActionError,
  statusLabel,
  t,
  workspacePath,
}: {
  actionPending: boolean;
  displayError: string | null;
  loading: boolean;
  reload: () => Promise<void>;
  setLocalActionError: Dispatch<SetStateAction<string | null>>;
  statusLabel: string;
  t: TFunction<"settings">;
  workspacePath: string;
}) {
  return (
      <SettingsSection
        title={t("harness.title")}
        description={t("harness.description")}
      >
        <SettingsPanelCard
          title={t("harness.workspaceTitle")}
          icon={
            <FolderLockIcon
              size={16}
              className="text-accent-primary"
            />
          }
          description={workspacePath || t("harness.workspaceMissing")}
          descriptionClassName="app-inline-mono break-all"
          headerAside={
            <div className="flex flex-wrap items-center gap-2">
              <Badge>{statusLabel}</Badge>
              <Button
                variant="secondary"
                size="sm"
                className="gap-2"
                disabled={!workspacePath || loading || actionPending}
                onClick={() => {
                  setLocalActionError(null);
                  void reload();
                }}
              >
                <RefreshCwIcon size={14} />
                {t("harness.refresh")}
              </Button>
            </div>
          }
        >
          {displayError ? (
            <div className="rounded-[0.75rem] border border-[var(--danger-border,var(--border))] bg-surface-solid px-3 py-2 text-sm leading-6 text-[var(--danger,var(--foreground))]">
              {displayError}
            </div>
          ) : null}
        </SettingsPanelCard>
      </SettingsSection>
  );
}
