import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { useHarnessControlPlane } from "@/hooks/workspace/use-harness-control-plane";

import { HarnessGrantsSection } from "./harness-settings-page/grants-section";
import { HarnessMemorySection } from "./harness-settings-page/memory-section";
import { HarnessPermissionsSection } from "./harness-settings-page/permissions-section";
import { HarnessPluginsSection } from "./harness-settings-page/plugins-section";
import { splitTags } from "./harness-settings-page/tags";
import { type HarnessSettingsPageProps } from "./harness-settings-page/types";
import { HarnessWorkspaceSection } from "./harness-settings-page/workspace-section";

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
      <HarnessWorkspaceSection
        actionPending={actionPending}
        displayError={displayError}
        loading={loading}
        reload={reload}
        setLocalActionError={setLocalActionError}
        statusLabel={statusLabel}
        t={t}
        workspacePath={workspacePath}
      />

      <HarnessPermissionsSection
        allowTools={allowTools}
        denyTools={denyTools}
        permissions={permissions}
        rules={rules}
        t={t}
        workspacePath={workspacePath}
      />

      <HarnessGrantsSection
        actionPending={actionPending}
        grantPattern={grantPattern}
        grantTool={grantTool}
        grants={grants}
        grantsStorePath={grantsStorePath}
        handleRememberGrant={handleRememberGrant}
        revokeGrant={revokeGrant}
        setGrantPattern={setGrantPattern}
        setGrantTool={setGrantTool}
        setLocalActionError={setLocalActionError}
        t={t}
        workspacePath={workspacePath}
      />

      <HarnessMemorySection
        actionPending={actionPending}
        handleAppendMemory={handleAppendMemory}
        loading={loading}
        memoryNotes={memoryNotes}
        memoryQuery={memoryQuery}
        memoryTags={memoryTags}
        memoryText={memoryText}
        searchMemory={searchMemory}
        setMemoryQuery={setMemoryQuery}
        setMemoryTags={setMemoryTags}
        setMemoryText={setMemoryText}
        t={t}
        workspacePath={workspacePath}
      />

      <HarnessPluginsSection
        actionPending={actionPending}
        plugins={plugins}
        setLocalActionError={setLocalActionError}
        t={t}
        updatePlugin={updatePlugin}
      />
    </div>
  );
}
