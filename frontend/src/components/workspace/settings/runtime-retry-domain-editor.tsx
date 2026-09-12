// 由 components/workspace/settings/runtime-retry-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { type RuntimeRetryRuleSummary } from "./runtime-retry-domain-utils";
import { createRetryRuleDraft } from "./runtime-retry-domain-editor/draft-utils";
import { RuntimeRetryOverviewSection } from "./runtime-retry-domain-editor/overview-section";
import { RuntimeRetryRuleDialog } from "./runtime-retry-domain-editor/rule-dialog";
import { RuntimeRetryRulesTable } from "./runtime-retry-domain-editor/rules-table";
import {
  type RetryRuleDraftInput,
  type RuntimeRetryDomainEditorProps,
} from "./runtime-retry-domain-editor/types";

export type { RetryRuleDraftInput } from "./runtime-retry-domain-editor/types";

export function RuntimeRetryDomainEditor({
  config,
  onChangeConfig,
  onDeleteRule,
  onMoveRule,
  onSaveRule,
  rules,
}: RuntimeRetryDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [draft, setDraft] = useState<RetryRuleDraftInput>(() =>
    createRetryRuleDraft(null),
  );

  const enabledRuleCount = useMemo(
    () => rules.filter((rule) => rule.enabled).length,
    [rules],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingIndex(null);
    setDraft(createRetryRuleDraft(null));
    setDialogOpen(true);
  }

  function openEditDialog(rule: RuntimeRetryRuleSummary) {
    setDialogError(null);
    setEditingIndex(rule.index);
    setDraft(createRetryRuleDraft(rule));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveRule(draft, editingIndex);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }
  return (
    <div className="space-y-3">
      <RuntimeRetryOverviewSection
        config={config}
        enabledRuleCount={enabledRuleCount}
        onChangeConfig={onChangeConfig}
        rules={rules}
        t={t}
      />

      <RuntimeRetryRulesTable
        config={config}
        onDeleteRule={onDeleteRule}
        onMoveRule={onMoveRule}
        openCreateDialog={openCreateDialog}
        openEditDialog={openEditDialog}
        rules={rules}
        t={t}
      />

      <RuntimeRetryRuleDialog
        dialogError={dialogError}
        dialogOpen={dialogOpen}
        draft={draft}
        editingIndex={editingIndex}
        handleSave={handleSave}
        setDialogOpen={setDialogOpen}
        setDraft={setDraft}
        t={t}
      />
    </div>
  );
}
