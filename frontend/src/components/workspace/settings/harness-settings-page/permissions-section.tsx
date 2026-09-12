// 由 components/workspace/settings/harness-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { ShieldCheckIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { type RuntimeHarnessPermissionsResponse } from "@/lib/runtime-api";

import { SettingsBadgeList } from "../settings-badge-list";
import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsInfoCard } from "../settings-info-card";
import { SettingsSection } from "../settings-section";

export function HarnessPermissionsSection({
  allowTools,
  denyTools,
  permissions,
  rules,
  t,
  workspacePath,
}: {
  allowTools: string[];
  denyTools: string[];
  permissions: RuntimeHarnessPermissionsResponse | null;
  rules: NonNullable<RuntimeHarnessPermissionsResponse["rules"]>;
  t: TFunction<"settings">;
  workspacePath: string;
}) {
  return (
      <SettingsSection
        title={t("harness.permissionsTitle")}
        description={t("harness.permissionsDescription")}
      >
        <div className="space-y-3">
          <SettingsInfoCard
            tone="softer"
            title={t("harness.permissionsFile")}
            icon={
              <ShieldCheckIcon
                size={16}
                className="text-[var(--accent-secondary)]"
              />
            }
          >
            <p className="app-inline-mono break-all text-sm text-[var(--muted-foreground)]">
              {permissions?.source_path ||
                (workspacePath
                  ? `${workspacePath}/.aicli/permissions.yaml`
                  : t("harness.notAvailable"))}
            </p>
            <p className="mt-2 text-sm text-[var(--muted-foreground)]">
              {permissions?.exists
                ? t("harness.permissionsExists", {
                    version: String(permissions.version ?? 1),
                  })
                : t("harness.permissionsMissing")}
            </p>
          </SettingsInfoCard>

          <div className="grid gap-3 lg:grid-cols-2">
            <SettingsInfoCard title={t("harness.denyTools")} tone="softer">
              {denyTools.length > 0 ? (
                <SettingsBadgeList>
                  {denyTools.map((tool) => (
                    <Badge key={`deny-${tool}`}>{tool}</Badge>
                  ))}
                </SettingsBadgeList>
              ) : (
                <SettingsEmptyState variant="dashed">
                  {t("harness.noDenyTools")}
                </SettingsEmptyState>
              )}
            </SettingsInfoCard>
            <SettingsInfoCard title={t("harness.allowTools")} tone="softer">
              {allowTools.length > 0 ? (
                <SettingsBadgeList>
                  {allowTools.map((tool) => (
                    <Badge key={`allow-${tool}`}>{tool}</Badge>
                  ))}
                </SettingsBadgeList>
              ) : (
                <SettingsEmptyState variant="dashed">
                  {t("harness.noAllowTools")}
                </SettingsEmptyState>
              )}
            </SettingsInfoCard>
          </div>

          <SettingsInfoCard title={t("harness.rulesTitle")} tone="softer">
            {rules.length > 0 ? (
              <div className="space-y-2">
                {rules.map((rule, index) => (
                  <div
                    key={`${rule.name || "rule"}-${index}`}
                    className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5"
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <div className="text-sm font-semibold text-[var(--foreground)]">
                        {rule.name || t("harness.unnamedRule", { index: String(index + 1) })}
                      </div>
                      <Badge>{rule.decision}</Badge>
                    </div>
                    {rule.reason ? (
                      <p className="mt-1 text-sm text-[var(--muted-foreground)]">
                        {rule.reason}
                      </p>
                    ) : null}
                    {(rule.tools?.length || rule.capabilities?.length) ? (
                      <SettingsBadgeList className="mt-2" compact>
                        {(rule.tools ?? []).map((tool) => (
                          <Badge key={`${rule.name}-tool-${tool}`}>{tool}</Badge>
                        ))}
                        {(rule.capabilities ?? []).map((capability) => (
                          <Badge key={`${rule.name}-cap-${capability}`}>
                            cap:{capability}
                          </Badge>
                        ))}
                      </SettingsBadgeList>
                    ) : null}
                  </div>
                ))}
              </div>
            ) : (
              <SettingsEmptyState variant="dashed">
                {t("harness.noRules")}
              </SettingsEmptyState>
            )}
          </SettingsInfoCard>
        </div>
      </SettingsSection>
  );
}
