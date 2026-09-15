// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getRuntimeConfigDocument, getRuntimeServiceStatus, previewRuntimeConfigDocument, restartRuntimeService, saveRuntimeConfigDocument } from "@/lib/runtime-api";
import { notifyRuntimeModelCatalogChanged } from "@/lib/runtime-model-catalog-sync";
import { buildSaveStatusMessage, sleep } from "./format";
import { type EditorMode } from "./types";
import { type ConfigEditorState } from "./use-config-state";


export function createConfigEditorActions(state: ConfigEditorState) {
  const {
    draftErrorCount,
    draftParsed,
    draftRaw,
    hasDraftErrors,
    hasDomainChanges,
    hasSourceChanges,
    hasUnsavedChanges,
    mode,
    setDocument,
    setDraftParsed,
    setDraftRaw,
    setError,
    setIsLoading,
    setIsModeSwitching,
    setIsPreviewLoading,
    setIsRestarting,
    setIsSaving,
    setMode,
    setPreviewDocument,
    setServiceError,
    setServiceStatus,
    setStatusMessage,
    t,
  } = state;

  async function switchMode(nextMode: EditorMode) {
    if (nextMode === mode) {
      return;
    }

    const needsSync =
      (mode === "source" && nextMode !== "source" && hasSourceChanges) ||
      (mode !== "source" && nextMode === "source" && hasDomainChanges);

    if (!needsSync) {
      setMode(nextMode);
      return;
    }

    setIsModeSwitching(true);
    setError(null);
    try {
      const syncedDocument = await previewRuntimeConfigDocument(
        mode === "source"
          ? { changed_by: "workspace_frontend", mode: "raw", raw: draftRaw }
          : {
              changed_by: "workspace_frontend",
              mode: "structured",
              parsed: draftParsed,
            },
      );
      setDraftParsed(syncedDocument.parsed);
      setDraftRaw(syncedDocument.raw);
      setPreviewDocument(syncedDocument);
      setMode(nextMode);
      setStatusMessage(
        nextMode === "source"
          ? t("editor.messages.syncedToSource")
          : t("editor.messages.syncedToStructured"),
      );
    } catch (switchError) {
      setError(
        switchError instanceof Error
          ? switchError.message
          : t("editor.messages.switchSyncFailed"),
      );
    } finally {
      setIsModeSwitching(false);
    }
  }

  async function reloadDocument() {
    if (
      hasUnsavedChanges &&
      !window.confirm(t("editor.messages.reloadConfirm"))
    ) {
      return;
    }
    setIsLoading(true);
    setError(null);
    try {
      const nextDocument = await getRuntimeConfigDocument();
      setDocument(nextDocument);
      setDraftParsed(nextDocument.parsed);
      setDraftRaw(nextDocument.raw);
      setPreviewDocument(null);
      setStatusMessage(t("editor.messages.reloadedFromDisk"));
    } catch (loadError) {
      setError(
        loadError instanceof Error
          ? loadError.message
          : t("editor.messages.reloadFailed"),
      );
    } finally {
      setIsLoading(false);
    }
  }

  async function saveDocument(options?: { suppressStatusMessage?: boolean }) {
    if (hasDraftErrors) {
      setError(t("editor.draftValidation.blocked", { count: draftErrorCount }));
      return null;
    }

    setIsSaving(true);
    setError(null);
    try {
      const nextDocument = await saveRuntimeConfigDocument(
        mode === "source"
          ? { changed_by: "workspace_frontend", mode: "raw", raw: draftRaw }
          : {
              changed_by: "workspace_frontend",
              mode: "structured",
              parsed: draftParsed,
            },
      );
      setDocument(nextDocument);
      setDraftParsed(nextDocument.parsed);
      setDraftRaw(nextDocument.raw);
      setPreviewDocument(null);
      notifyRuntimeModelCatalogChanged();
      if (!options?.suppressStatusMessage) {
        setStatusMessage(buildSaveStatusMessage(t, nextDocument));
      }
      return nextDocument;
    } catch (saveError) {
      setError(
        saveError instanceof Error ? saveError.message : t("editor.messages.saveFailed"),
      );
      return null;
    } finally {
      setIsSaving(false);
    }
  }

  async function generatePreview() {
    if (hasDraftErrors) {
      setError(t("editor.draftValidation.blocked", { count: draftErrorCount }));
      return;
    }

    setIsPreviewLoading(true);
    setError(null);
    try {
      const nextPreview = await previewRuntimeConfigDocument(
        mode === "source"
          ? { changed_by: "workspace_frontend", mode: "raw", raw: draftRaw }
          : {
              changed_by: "workspace_frontend",
              mode: "structured",
              parsed: draftParsed,
            },
      );
      setPreviewDocument(nextPreview);
    } catch (previewError) {
      setError(
        previewError instanceof Error
          ? previewError.message
          : t("editor.messages.previewFailed"),
      );
    } finally {
      setIsPreviewLoading(false);
    }
  }

  async function restartService(options?: {
    skipConfirm?: boolean;
    refreshDocument?: boolean;
  }) {
    if (
      !options?.skipConfirm &&
      !window.confirm(t("editor.messages.restartConfirm"))
    ) {
      return false;
    }
    setIsRestarting(true);
    setServiceError(null);
    try {
      await restartRuntimeService();
      for (let index = 0; index < 10; index += 1) {
        await sleep(1200);
        try {
          const nextStatus = await getRuntimeServiceStatus();
          setServiceStatus(nextStatus);
          if (nextStatus.running) {
            if (options?.refreshDocument ?? !hasUnsavedChanges) {
              try {
                const nextDocument = await getRuntimeConfigDocument();
                setDocument(nextDocument);
                setDraftParsed(nextDocument.parsed);
                setDraftRaw(nextDocument.raw);
                setPreviewDocument(null);
              } catch {
                // ignore document refresh failures after restart
              }
            }
            notifyRuntimeModelCatalogChanged();
            setStatusMessage(t("editor.messages.runtimeServerReconnected"));
            return true;
          }
        } catch {
          // ignore poll errors while service restarts
        }
      }
      setStatusMessage(t("editor.messages.restartRequested"));
      return true;
    } catch (restartError) {
      setServiceError(
        restartError instanceof Error
          ? restartError.message
          : t("editor.messages.restartFailed"),
      );
      return false;
    } finally {
      setIsRestarting(false);
    }
  }

  async function saveAndRestartDocument() {
    if (hasDraftErrors) {
      setError(t("editor.draftValidation.blocked", { count: draftErrorCount }));
      return;
    }

    if (
      !window.confirm(t("editor.messages.saveAndRestartConfirm"))
    ) {
      return;
    }

    const nextDocument = await saveDocument({ suppressStatusMessage: true });
    if (!nextDocument) {
      return;
    }

    if (!nextDocument.restart_required) {
      setStatusMessage(buildSaveStatusMessage(t, nextDocument));
      return;
    }

    const restarted = await restartService({
      skipConfirm: true,
      refreshDocument: true,
    });
    if (restarted) {
      setStatusMessage(t("editor.messages.saveAndRestartDone"));
    }
  }

  return {
    switchMode,
    reloadDocument,
    saveDocument,
    generatePreview,
    restartService,
    saveAndRestartDocument,
  };
}

export type ConfigEditorActions = ReturnType<typeof createConfigEditorActions>;
