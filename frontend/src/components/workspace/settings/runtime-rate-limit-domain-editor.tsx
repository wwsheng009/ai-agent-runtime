import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  type RuntimeRateLimitApiKeyLimitSummary,
  type RuntimeRateLimitPathLimitSummary,
} from "./runtime-rate-limit-domain-utils";
import {
  type RateLimitApiKeyDraftInput,
  type RateLimitPathDraftInput,
} from "./runtime-rate-limit-domain-form-utils";
import { RuntimeRateLimitApiKeyLimitDialog } from "./runtime-rate-limit-domain-editor/api-key-limit-dialog";
import { RuntimeRateLimitApiKeyLimitsTable } from "./runtime-rate-limit-domain-editor/api-key-limits-table";
import {
  createApiKeyDraftInput,
  createPathDraftInput,
} from "./runtime-rate-limit-domain-editor/format";
import { RuntimeRateLimitOverviewSection } from "./runtime-rate-limit-domain-editor/overview-section";
import { RuntimeRateLimitPathLimitDialog } from "./runtime-rate-limit-domain-editor/path-limit-dialog";
import { RuntimeRateLimitPathLimitsTable } from "./runtime-rate-limit-domain-editor/path-limits-table";
import { type RuntimeRateLimitDomainEditorProps } from "./runtime-rate-limit-domain-editor/types";

export function RuntimeRateLimitDomainEditor({
  apiKeyLimits,
  onChangeConfig,
  onDeleteApiKeyLimit,
  onDeletePathLimit,
  onSaveApiKeyLimit,
  onSavePathLimit,
  pathLimits,
  rateLimitConfig,
}: RuntimeRateLimitDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [apiDialogOpen, setApiDialogOpen] = useState(false);
  const [apiDialogError, setApiDialogError] = useState<string | null>(null);
  const [editingApiKeyIndex, setEditingApiKeyIndex] = useState<number | null>(null);
  const [apiDraft, setApiDraft] = useState<RateLimitApiKeyDraftInput>(() =>
    createApiKeyDraftInput(null),
  );

  const [pathDialogOpen, setPathDialogOpen] = useState(false);
  const [pathDialogError, setPathDialogError] = useState<string | null>(null);
  const [editingPath, setEditingPath] = useState<string | null>(null);
  const [pathDraft, setPathDraft] = useState<RateLimitPathDraftInput>(() =>
    createPathDraftInput(null),
  );

  const totalPathBurst = useMemo(
    () =>
      pathLimits.reduce((sum, item) => sum + (Number(item.burst) || 0), 0),
    [pathLimits],
  );

  function openCreateApiDialog() {
    setApiDialogError(null);
    setEditingApiKeyIndex(null);
    setApiDraft(createApiKeyDraftInput(null));
    setApiDialogOpen(true);
  }

  function openEditApiDialog(limit: RuntimeRateLimitApiKeyLimitSummary) {
    setApiDialogError(null);
    setEditingApiKeyIndex(limit.index);
    setApiDraft(createApiKeyDraftInput(limit));
    setApiDialogOpen(true);
  }

  function openCreatePathDialog() {
    setPathDialogError(null);
    setEditingPath(null);
    setPathDraft(createPathDraftInput(null));
    setPathDialogOpen(true);
  }

  function openEditPathDialog(limit: RuntimeRateLimitPathLimitSummary) {
    setPathDialogError(null);
    setEditingPath(limit.path);
    setPathDraft(createPathDraftInput(limit));
    setPathDialogOpen(true);
  }

  function handleSaveApiLimit() {
    const error = onSaveApiKeyLimit(apiDraft, editingApiKeyIndex);
    if (error) {
      setApiDialogError(error);
      return;
    }
    setApiDialogOpen(false);
  }

  function handleSavePathLimit() {
    const error = onSavePathLimit(pathDraft, editingPath);
    if (error) {
      setPathDialogError(error);
      return;
    }
    setPathDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <RuntimeRateLimitOverviewSection
        apiKeyLimits={apiKeyLimits}
        onChangeConfig={onChangeConfig}
        pathLimits={pathLimits}
        rateLimitConfig={rateLimitConfig}
        t={t}
        totalPathBurst={totalPathBurst}
      />

      <RuntimeRateLimitApiKeyLimitsTable
        apiKeyLimits={apiKeyLimits}
        onDeleteApiKeyLimit={onDeleteApiKeyLimit}
        openCreateApiDialog={openCreateApiDialog}
        openEditApiDialog={openEditApiDialog}
        t={t}
      />

      <RuntimeRateLimitPathLimitsTable
        onDeletePathLimit={onDeletePathLimit}
        openCreatePathDialog={openCreatePathDialog}
        openEditPathDialog={openEditPathDialog}
        pathLimits={pathLimits}
        t={t}
        totalPathBurst={totalPathBurst}
      />

      <RuntimeRateLimitApiKeyLimitDialog
        apiDialogError={apiDialogError}
        apiDialogOpen={apiDialogOpen}
        apiDraft={apiDraft}
        editingApiKeyIndex={editingApiKeyIndex}
        handleSaveApiLimit={handleSaveApiLimit}
        setApiDialogOpen={setApiDialogOpen}
        setApiDraft={setApiDraft}
        t={t}
      />

      <RuntimeRateLimitPathLimitDialog
        editingPath={editingPath}
        handleSavePathLimit={handleSavePathLimit}
        pathDialogError={pathDialogError}
        pathDialogOpen={pathDialogOpen}
        pathDraft={pathDraft}
        setPathDialogOpen={setPathDialogOpen}
        setPathDraft={setPathDraft}
        t={t}
      />
    </div>
  );
}

