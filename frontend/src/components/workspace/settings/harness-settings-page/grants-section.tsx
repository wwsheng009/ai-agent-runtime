// 由 components/workspace/settings/harness-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { Trash2Icon } from "lucide-react";
import { type Dispatch, type SetStateAction } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type RuntimeHarnessGrant } from "@/lib/runtime-api";

import { editorControlClassName } from "../editor-control-class";
import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsInfoCard } from "../settings-info-card";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsSection } from "../settings-section";

export function HarnessGrantsSection({
  actionPending,
  grantPattern,
  grantTool,
  grants,
  grantsStorePath,
  handleRememberGrant,
  revokeGrant,
  setGrantPattern,
  setGrantTool,
  setLocalActionError,
  t,
  workspacePath,
}: {
  actionPending: boolean;
  grantPattern: string;
  grantTool: string;
  grants: RuntimeHarnessGrant[];
  grantsStorePath: string;
  handleRememberGrant: () => Promise<void>;
  revokeGrant: (input: { tool: string; pattern?: string }) => Promise<void>;
  setGrantPattern: Dispatch<SetStateAction<string>>;
  setGrantTool: Dispatch<SetStateAction<string>>;
  setLocalActionError: Dispatch<SetStateAction<string | null>>;
  t: TFunction<"settings">;
  workspacePath: string;
}) {
  return (
      <SettingsSection
        title={t("harness.grantsTitle")}
        description={t("harness.grantsDescription")}
      >
        <div className="space-y-3">
          <SettingsInfoCard
            tone="softer"
            title={t("harness.grantsStore")}
            description={
              grantsStorePath ||
              (workspacePath
                ? `${workspacePath}/.aicli/grants.json`
                : t("harness.notAvailable"))
            }
            descriptionClassName="app-inline-mono break-all"
          />

          <SettingsPanelCard title={t("harness.rememberGrant")}>
            <div className="grid gap-3 md:grid-cols-[1fr_1fr_auto]">
              <input
                className={editorControlClassName}
                value={grantTool}
                onChange={(event) => setGrantTool(event.target.value)}
                placeholder={t("harness.grantToolPlaceholder")}
                aria-label={t("harness.grantToolPlaceholder")}
              />
              <input
                className={editorControlClassName}
                value={grantPattern}
                onChange={(event) => setGrantPattern(event.target.value)}
                placeholder={t("harness.grantPatternPlaceholder")}
                aria-label={t("harness.grantPatternPlaceholder")}
              />
              <Button
                size="sm"
                disabled={
                  !workspacePath ||
                  actionPending ||
                  !grantTool.trim()
                }
                onClick={() => {
                  void handleRememberGrant();
                }}
              >
                {t("harness.remember")}
              </Button>
            </div>
            <p className="mt-2 text-xs leading-5 text-muted-foreground">
              {t("harness.grantHint")}
            </p>
          </SettingsPanelCard>

          {grants.length > 0 ? (
            <div className="space-y-2">
              {grants.map((grant) => (
                <div
                  key={`${grant.tool}:${grant.pattern || ""}:${grant.scope || ""}`}
                  className="flex items-start justify-between gap-3 rounded-card-lg border border-border bg-surface-softer px-3 py-2.5"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-semibold text-foreground">
                        {grant.tool}
                      </span>
                      {grant.scope ? <Badge>{grant.scope}</Badge> : null}
                    </div>
                    <p className="mt-1 app-inline-mono break-all text-sm text-muted-foreground">
                      {grant.pattern || t("harness.toolWideGrant")}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="gap-2"
                    disabled={actionPending}
                    onClick={() => {
                      setLocalActionError(null);
                      void revokeGrant({
                        tool: grant.tool,
                        pattern: grant.pattern,
                      }).catch((actionError) => {
                        setLocalActionError(
                          actionError instanceof Error
                            ? actionError.message
                            : t("harness.grantRevokeFailed"),
                        );
                      });
                    }}
                  >
                    <Trash2Icon size={14} />
                    {t("harness.revoke")}
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <SettingsEmptyState variant="dashed">
              {t("harness.noGrants")}
            </SettingsEmptyState>
          )}
        </div>
      </SettingsSection>
  );
}
