import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { type TransformerModifierDraftInput } from "./runtime-transformer-domain-form-utils";
import {
  type RuntimeTransformerModifierSummary,
  type TransformerModifierScope,
} from "./runtime-transformer-domain-utils";
import { createTransformerModifierDraft } from "./runtime-transformer-domain-editor/draft-utils";
import { RuntimeTransformerModifierDialog } from "./runtime-transformer-domain-editor/modifier-dialog";
import { RuntimeTransformerModifierTable } from "./runtime-transformer-domain-editor/modifier-table";
import { RuntimeTransformerOverviewSection } from "./runtime-transformer-domain-editor/overview-section";
import { type RuntimeTransformerDomainEditorProps } from "./runtime-transformer-domain-editor/types";

export function RuntimeTransformerDomainEditor({
  config,
  onChangeConfig,
  onDeleteModifier,
  onMoveModifier,
  onSaveModifier,
  requestModifiers,
  responseModifiers,
}: RuntimeTransformerDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [dialogScope, setDialogScope] =
    useState<TransformerModifierScope>("request");
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [draft, setDraft] = useState<TransformerModifierDraftInput>(() =>
    createTransformerModifierDraft(null),
  );

  const totalModifierCount = requestModifiers.length + responseModifiers.length;
  const enabledModifierCount = useMemo(
    () =>
      [...requestModifiers, ...responseModifiers].filter((item) => item.enabled)
        .length,
    [requestModifiers, responseModifiers],
  );

  function openCreateDialog(scope: TransformerModifierScope) {
    setDialogError(null);
    setDialogScope(scope);
    setEditingIndex(null);
    setDraft(createTransformerModifierDraft(null));
    setDialogOpen(true);
  }

  function openEditDialog(item: RuntimeTransformerModifierSummary) {
    setDialogError(null);
    setDialogScope(item.scope);
    setEditingIndex(item.index);
    setDraft(createTransformerModifierDraft(item));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveModifier(dialogScope, draft, editingIndex);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <RuntimeTransformerOverviewSection
        config={config}
        enabledModifierCount={enabledModifierCount}
        onChangeConfig={onChangeConfig}
        t={t}
        totalModifierCount={totalModifierCount}
      />

      <RuntimeTransformerModifierTable
        description={t("editor.transformer.requestTable.description")}
        items={requestModifiers}
        onDeleteModifier={onDeleteModifier}
        onMoveModifier={onMoveModifier}
        openCreateDialog={openCreateDialog}
        openEditDialog={openEditDialog}
        scope="request"
        t={t}
        title={t("editor.transformer.requestTable.title")}
      />

      <RuntimeTransformerModifierTable
        description={t("editor.transformer.responseTable.description")}
        items={responseModifiers}
        onDeleteModifier={onDeleteModifier}
        onMoveModifier={onMoveModifier}
        openCreateDialog={openCreateDialog}
        openEditDialog={openEditDialog}
        scope="response"
        t={t}
        title={t("editor.transformer.responseTable.title")}
      />

      <RuntimeTransformerModifierDialog
        dialogError={dialogError}
        dialogOpen={dialogOpen}
        dialogScope={dialogScope}
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
