import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  buildProviderGroupCreateConfigSnippet,
  type RuntimeProviderGroupSummary,
} from "./runtime-config-domain-utils";
import {
  type ProviderGroupDraftInput,
  validateProviderGroupDraft,
} from "./runtime-provider-groups-domain-form-utils";
import { type RuntimeProviderSummary } from "./runtime-provider-config-utils";

import { createProviderGroupDraftInput } from "./runtime-provider-groups-domain-editor/draft-utils";
import { ProviderGroupDialog } from "./runtime-provider-groups-domain-editor/provider-group-dialog";
import { ProviderGroupsTable } from "./runtime-provider-groups-domain-editor/provider-groups-table";

type RuntimeProviderGroupsDomainEditorProps = {
  groups: RuntimeProviderGroupSummary[];
  onDeleteGroup: (name: string) => void;
  onSaveGroup: (
    draft: ProviderGroupDraftInput,
    previousName: string | null,
  ) => string | null;
  providers: RuntimeProviderSummary[];
};

export function RuntimeProviderGroupsDomainEditor({
  groups,
  onDeleteGroup,
  onSaveGroup,
  providers,
}: RuntimeProviderGroupsDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingGroupName, setEditingGroupName] = useState<string | null>(null);
  const [copiedGroupName, setCopiedGroupName] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProviderGroupDraftInput>(() =>
    createProviderGroupDraftInput(null),
  );

  const providerLookup = useMemo(
    () => new Map(providers.map((provider) => [provider.name, provider])),
    [providers],
  );
  const providerNames = useMemo(
    () => providers.map((provider) => provider.name),
    [providers],
  );
  const draftValidationIssues = useMemo(
    () => validateProviderGroupDraft(draft),
    [draft],
  );
  const referencedProviderCount = useMemo(
    () =>
      new Set(
        groups.flatMap((group) =>
          group.providers.map((provider) => provider.name).filter(Boolean),
        ),
      ).size,
    [groups],
  );
  const missingReferencedProviderCount = useMemo(
    () =>
      new Set(
        groups.flatMap((group) =>
          group.providers
            .map((provider) => provider.name)
            .filter((name) => name && !providerLookup.has(name)),
        ),
      ).size,
    [groups, providerLookup],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingGroupName(null);
    setDraft(createProviderGroupDraftInput(null));
    setDialogOpen(true);
  }

  function openEditDialog(group: RuntimeProviderGroupSummary) {
    setDialogError(null);
    setEditingGroupName(group.name);
    setDraft(createProviderGroupDraftInput(group));
    setDialogOpen(true);
  }

  function handleSave() {
    if (draftValidationIssues.length > 0) {
      setDialogError(draftValidationIssues[0].message);
      return;
    }
    const error = onSaveGroup(draft, editingGroupName);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  async function handleCopyGroup(group: RuntimeProviderGroupSummary) {
    try {
      await navigator.clipboard.writeText(buildProviderGroupCreateConfigSnippet(group));
      setCopiedGroupName(group.name);
      window.setTimeout(() => {
        setCopiedGroupName((currentName) =>
          currentName === group.name ? null : currentName,
        );
      }, 1500);
    } catch {
      setCopiedGroupName(null);
    }
  }

  return (
    <>
      <ProviderGroupsTable
        copiedGroupName={copiedGroupName}
        groups={groups}
        missingReferencedProviderCount={missingReferencedProviderCount}
        onCopyGroup={(group) => void handleCopyGroup(group)}
        onCreateGroup={openCreateDialog}
        onDeleteGroup={onDeleteGroup}
        onEditGroup={openEditDialog}
        providerLookup={providerLookup}
        providers={providers}
        referencedProviderCount={referencedProviderCount}
        t={t}
      />

      <ProviderGroupDialog
        dialogError={dialogError}
        draft={draft}
        draftValidationIssues={draftValidationIssues}
        editingGroupName={editingGroupName}
        onClose={() => setDialogOpen(false)}
        onConfirm={handleSave}
        open={dialogOpen}
        providerLookup={providerLookup}
        providerNames={providerNames}
        providers={providers}
        setDraft={setDraft}
        t={t}
      />
    </>
  );
}
