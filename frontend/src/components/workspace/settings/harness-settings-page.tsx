import {
  BrainIcon,
  FolderLockIcon,
  PlugIcon,
  RefreshCwIcon,
  ShieldCheckIcon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useHarnessControlPlane } from "@/hooks/workspace/use-harness-control-plane";
import type { RuntimeClientIdentity } from "@/lib/runtime-client";

import { editorControlClassName } from "./editor-control-class";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsEmptyState } from "./settings-empty-state";
import { SettingsInfoCard } from "./settings-info-card";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsSection } from "./settings-section";

type HarnessSettingsPageProps = {
  runtimeClient: RuntimeClientIdentity;
};

function splitTags(value: string) {
  return value
    .split(/[,，\s]+/)
    .map((tag) => tag.trim())
    .filter(Boolean);
}

export function HarnessSettingsPage({
  runtimeClient,
}: HarnessSettingsPageProps) {
  const { t } = useTranslation("settings");
  const workspacePath = runtimeClient.workspacePath?.trim() || "";
  const {
    actionPending,
    appendMemory,
    error,
    grants,
    grantsStorePath,
    loading,
    memoryNotes,
    memoryQuery,
    permissions,
    plugins,
    reload,
    rememberGrant,
    revokeGrant,
    searchMemory,
    setMemoryQuery,
    updatePlugin,
  } = useHarnessControlPlane({ workspacePath });

  const [grantTool, setGrantTool] = useState("write");
  const [grantPattern, setGrantPattern] = useState("");
  const [memoryText, setMemoryText] = useState("");
  const [memoryTags, setMemoryTags] = useState("");
  const [localActionError, setLocalActionError] = useState<string | null>(null);

  const displayError = localActionError || error;
  const rules = permissions?.rules ?? [];
  const denyTools = permissions?.deny_tools ?? [];
  const allowTools = permissions?.allow_tools ?? [];

  const statusLabel = useMemo(() => {
    if (!workspacePath) {
      return t("harness.workspaceMissing");
    }
    if (loading) {
      return t("harness.loading");
    }
    return t("harness.ready");
  }, [loading, t, workspacePath]);

  async function handleRememberGrant() {
    setLocalActionError(null);
    try {
      await rememberGrant({
        tool: grantTool.trim(),
        pattern: grantPattern.trim() || undefined,
        scope: "project",
      });
      setGrantPattern("");
    } catch (actionError) {
      setLocalActionError(
        actionError instanceof Error
          ? actionError.message
          : t("harness.grantRememberFailed"),
      );
    }
  }

  async function handleAppendMemory() {
    setLocalActionError(null);
    try {
      await appendMemory({
        text: memoryText.trim(),
        tags: splitTags(memoryTags),
      });
      setMemoryText("");
      setMemoryTags("");
    } catch (actionError) {
      setLocalActionError(
        actionError instanceof Error
          ? actionError.message
          : t("harness.memoryAppendFailed"),
      );
    }
  }

  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("harness.title")}
        description={t("harness.description")}
      >
        <SettingsPanelCard
          title={t("harness.workspaceTitle")}
          icon={
            <FolderLockIcon
              size={16}
              className="text-[var(--accent-primary)]"
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
            <div className="rounded-[0.75rem] border border-[var(--danger-border,var(--border))] bg-[var(--surface-solid)] px-3 py-2 text-sm leading-6 text-[var(--danger,var(--foreground))]">
              {displayError}
            </div>
          ) : null}
        </SettingsPanelCard>
      </SettingsSection>

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
            <p className="mt-2 text-xs leading-5 text-[var(--muted-foreground)]">
              {t("harness.grantHint")}
            </p>
          </SettingsPanelCard>

          {grants.length > 0 ? (
            <div className="space-y-2">
              {grants.map((grant) => (
                <div
                  key={`${grant.tool}:${grant.pattern || ""}:${grant.scope || ""}`}
                  className="flex items-start justify-between gap-3 rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-semibold text-[var(--foreground)]">
                        {grant.tool}
                      </span>
                      {grant.scope ? <Badge>{grant.scope}</Badge> : null}
                    </div>
                    <p className="mt-1 app-inline-mono break-all text-sm text-[var(--muted-foreground)]">
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

      <SettingsSection
        title={t("harness.memoryTitle")}
        description={t("harness.memoryDescription")}
      >
        <div className="space-y-3">
          <SettingsPanelCard
            title={t("harness.memorySearch")}
            icon={
              <BrainIcon size={16} className="text-[var(--accent-primary)]" />
            }
          >
            <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
              <input
                className={editorControlClassName}
                value={memoryQuery}
                onChange={(event) => setMemoryQuery(event.target.value)}
                placeholder={t("harness.memorySearchPlaceholder")}
                aria-label={t("harness.memorySearchPlaceholder")}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    void searchMemory(memoryQuery);
                  }
                }}
              />
              <Button
                variant="secondary"
                size="sm"
                disabled={!workspacePath || loading || actionPending}
                onClick={() => {
                  void searchMemory(memoryQuery);
                }}
              >
                {t("harness.search")}
              </Button>
            </div>
          </SettingsPanelCard>

          <SettingsPanelCard title={t("harness.memoryAppend")}>
            <textarea
              className={`${editorControlClassName} min-h-24`}
              value={memoryText}
              onChange={(event) => setMemoryText(event.target.value)}
              placeholder={t("harness.memoryTextPlaceholder")}
              aria-label={t("harness.memoryTextPlaceholder")}
            />
            <div className="mt-3 grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
              <input
                className={editorControlClassName}
                value={memoryTags}
                onChange={(event) => setMemoryTags(event.target.value)}
                placeholder={t("harness.memoryTagsPlaceholder")}
                aria-label={t("harness.memoryTagsPlaceholder")}
              />
              <Button
                size="sm"
                disabled={
                  !workspacePath ||
                  actionPending ||
                  !memoryText.trim()
                }
                onClick={() => {
                  void handleAppendMemory();
                }}
              >
                {t("harness.append")}
              </Button>
            </div>
          </SettingsPanelCard>

          {memoryNotes.length > 0 ? (
            <div className="space-y-2">
              {memoryNotes.map((note) => (
                <div
                  key={note.id}
                  className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5"
                >
                  <p className="text-sm leading-6 text-[var(--foreground)]">
                    {note.text}
                  </p>
                  <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-[var(--muted-foreground)]">
                    {note.source ? <Badge>{note.source}</Badge> : null}
                    {note.created_at ? <span>{note.created_at}</span> : null}
                    {(note.tags ?? []).map((tag) => (
                      <Badge key={`${note.id}-${tag}`}>#{tag}</Badge>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <SettingsEmptyState variant="dashed">
              {memoryQuery.trim()
                ? t("harness.noMemoryHits")
                : t("harness.noMemoryNotes")}
            </SettingsEmptyState>
          )}
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("harness.pluginsTitle")}
        description={t("harness.pluginsDescription")}
      >
        {plugins.length > 0 ? (
          <div className="space-y-2">
            {plugins.map((plugin) => (
              <SettingsPanelCard
                key={plugin.id}
                title={plugin.name || plugin.id}
                icon={
                  <PlugIcon
                    size={16}
                    className="text-[var(--accent-secondary)]"
                  />
                }
                description={
                  plugin.description ||
                  t("harness.pluginNoDescription")
                }
                headerAside={
                  <div className="flex flex-wrap gap-2">
                    <Badge>{plugin.trust || "untrusted"}</Badge>
                    <Badge>
                      {plugin.active
                        ? t("harness.pluginActive")
                        : t("harness.pluginInactive")}
                    </Badge>
                  </div>
                }
              >
                <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--muted-foreground)]">
                  <span className="app-inline-mono">{plugin.id}</span>
                  {plugin.version ? <span>v{plugin.version}</span> : null}
                  {plugin.root ? (
                    <span className="app-inline-mono break-all">
                      {plugin.root}
                    </span>
                  ) : null}
                </div>
                {(plugin.warnings?.length ?? 0) > 0 ? (
                  <div className="mt-2 space-y-1 text-sm text-[var(--muted-foreground)]">
                    {plugin.warnings?.map((warning) => (
                      <div key={`${plugin.id}-${warning}`}>{warning}</div>
                    ))}
                  </div>
                ) : null}
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={actionPending}
                    onClick={() => {
                      setLocalActionError(null);
                      void updatePlugin(
                        plugin.id,
                        plugin.trust === "trusted" ? "untrust" : "trust",
                      ).catch((actionError) => {
                        setLocalActionError(
                          actionError instanceof Error
                            ? actionError.message
                            : t("harness.pluginUpdateFailed"),
                        );
                      });
                    }}
                  >
                    {plugin.trust === "trusted"
                      ? t("harness.untrust")
                      : t("harness.trust")}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={actionPending}
                    onClick={() => {
                      setLocalActionError(null);
                      void updatePlugin(
                        plugin.id,
                        plugin.enabled ? "disable" : "enable",
                      ).catch((actionError) => {
                        setLocalActionError(
                          actionError instanceof Error
                            ? actionError.message
                            : t("harness.pluginUpdateFailed"),
                        );
                      });
                    }}
                  >
                    {plugin.enabled
                      ? t("harness.disable")
                      : t("harness.enable")}
                  </Button>
                </div>
              </SettingsPanelCard>
            ))}
          </div>
        ) : (
          <SettingsEmptyState variant="dashed">
            {t("harness.noPlugins")}
          </SettingsEmptyState>
        )}
      </SettingsSection>
    </div>
  );
}
