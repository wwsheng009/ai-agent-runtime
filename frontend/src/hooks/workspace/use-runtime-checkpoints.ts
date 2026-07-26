import { useEffect, useMemo, useState } from "react";

import {
  buildCheckpointConversationSummary,
  buildCheckpointFileCode,
  formatCheckpointProvenance,
  formatCheckpointProvenanceSummary,
  pickInitialCheckpointFilePath,
} from "@/components/workspace/artifact-panel-shared";
import {
  getSessionCheckpointFiles,
  listSessionBacktrackAudit,
  listSessionCheckpoints,
  previewSessionCheckpoint,
  restoreSessionCheckpoint,
  type RuntimeSessionBacktrackTombstone,
  type RuntimeSessionCheckpointFile,
  type RuntimeSessionCheckpointPreviewMode,
  type RuntimeSessionCheckpointPreviewResult,
  type RuntimeSessionCheckpointSummary,
} from "@/lib/runtime-api";

type UseRuntimeCheckpointsOptions = {
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  sessionId?: string;
};

type ShouldReloadRuntimeCheckpointsOptions = {
  lastHandledEventKey?: string;
  lastRuntimeEventKey?: string;
  lastRuntimeEventType?: string;
  loadedCheckpointSessionId: string;
  sessionId?: string;
};

type ShouldReloadBacktrackAuditOptions = {
  lastHandledEventKey?: string;
  lastRuntimeEventKey?: string;
  lastRuntimeEventType?: string;
  loadedAuditSessionId: string;
  sessionId?: string;
};

type ResolveCheckpointDetailStateOptions = {
  checkpointFiles: RuntimeSessionCheckpointFile[];
  checkpointPreview?: RuntimeSessionCheckpointPreviewResult;
  selectedCheckpoint?: RuntimeSessionCheckpointSummary | null;
  selectedCheckpointFilePath: string | null;
};

export function buildRuntimeEventReloadKey(
  lastRuntimeEventType?: string,
  runtimeEventCount?: number,
) {
  if (!lastRuntimeEventType) {
    return "";
  }

  return `${lastRuntimeEventType}:${runtimeEventCount ?? 0}`;
}

export function shouldReloadRuntimeCheckpoints({
  lastHandledEventKey,
  lastRuntimeEventKey,
  lastRuntimeEventType,
  loadedCheckpointSessionId,
  sessionId,
}: ShouldReloadRuntimeCheckpointsOptions) {
  if (!sessionId) {
    return false;
  }

  if (loadedCheckpointSessionId !== sessionId) {
    return true;
  }

  if (
    lastRuntimeEventType !== "checkpoint_created" &&
    lastRuntimeEventType !== "backtrack_finished" &&
    lastRuntimeEventType !== "rewind_finished"
  ) {
    return false;
  }

  if (!lastRuntimeEventKey) {
    return false;
  }

  return lastRuntimeEventKey !== lastHandledEventKey;
}

export function shouldReloadBacktrackAudit({
  lastHandledEventKey,
  lastRuntimeEventKey,
  lastRuntimeEventType,
  loadedAuditSessionId,
  sessionId,
}: ShouldReloadBacktrackAuditOptions) {
  if (!sessionId) {
    return false;
  }

  if (loadedAuditSessionId !== sessionId) {
    return true;
  }

  if (
    lastRuntimeEventType !== "backtrack_finished" &&
    lastRuntimeEventType !== "rewind_finished"
  ) {
    return false;
  }

  if (!lastRuntimeEventKey) {
    return false;
  }

  return lastRuntimeEventKey !== lastHandledEventKey;
}

export function resolveCheckpointDetailState({
  checkpointFiles,
  checkpointPreview,
  selectedCheckpoint,
  selectedCheckpointFilePath,
}: ResolveCheckpointDetailStateOptions) {
  const checkpointPreviewFiles = checkpointPreview?.preview_files ?? [];
  const resolvedSelectedCheckpointFilePath =
    selectedCheckpointFilePath ??
    pickInitialCheckpointFilePath(checkpointFiles, checkpointPreviewFiles);
  const selectedCheckpointFile =
    checkpointFiles.find((file) => file.path === resolvedSelectedCheckpointFilePath) ??
    checkpointFiles[0];
  const selectedCheckpointPreviewFile =
    checkpointPreviewFiles.find(
      (file) => file.path === resolvedSelectedCheckpointFilePath,
    ) ??
    checkpointPreviewFiles[0];

  return {
    checkpointFileCode: buildCheckpointFileCode(
      selectedCheckpointFile,
      selectedCheckpointPreviewFile,
    ),
    checkpointProvenance: formatCheckpointProvenance(
      checkpointPreview?.provenance ?? selectedCheckpoint?.provenance,
    ),
    checkpointPreviewFiles,
    selectedCheckpointFilePath: resolvedSelectedCheckpointFilePath,
    selectedCheckpointFile,
    selectedCheckpointPreviewFile,
  };
}

export function useRuntimeCheckpoints({
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
}: UseRuntimeCheckpointsOptions) {
  const [checkpoints, setCheckpoints] = useState<RuntimeSessionCheckpointSummary[]>([]);
  const [checkpointsError, setCheckpointsError] = useState<string | null>(null);
  const [checkpointsLoading, setCheckpointsLoading] = useState(false);
  const [loadedCheckpointSessionId, setLoadedCheckpointSessionId] = useState("");
  const [lastHandledCheckpointEventKey, setLastHandledCheckpointEventKey] = useState("");
  const [selectedCheckpointId, setSelectedCheckpointId] = useState<string | null>(null);
  const [selectedCheckpointFilePath, setSelectedCheckpointFilePath] = useState<string | null>(
    null,
  );
  const [checkpointDetailsError, setCheckpointDetailsError] = useState<string | null>(
    null,
  );
  const [checkpointDetailsLoadingId, setCheckpointDetailsLoadingId] = useState("");
  const [checkpointFilesById, setCheckpointFilesById] = useState<
    Record<string, RuntimeSessionCheckpointFile[]>
  >({});
  const [checkpointPreviewById, setCheckpointPreviewById] = useState<
    Record<string, RuntimeSessionCheckpointPreviewResult>
  >({});
  const [checkpointRestorePendingId, setCheckpointRestorePendingId] = useState("");
  const [checkpointRestoreError, setCheckpointRestoreError] = useState<string | null>(null);
  const [checkpointRestoreNotice, setCheckpointRestoreNotice] = useState<string | null>(null);
  const [backtrackAuditEntries, setBacktrackAuditEntries] = useState<
    RuntimeSessionBacktrackTombstone[]
  >([]);
  const [backtrackAuditError, setBacktrackAuditError] = useState<string | null>(null);
  const [backtrackAuditLoading, setBacktrackAuditLoading] = useState(false);
  const [loadedAuditSessionId, setLoadedAuditSessionId] = useState("");
  const [lastHandledAuditEventKey, setLastHandledAuditEventKey] = useState("");
  const lastRuntimeEventKey = buildRuntimeEventReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );

  useEffect(() => {
    if (!sessionId) {
      setCheckpoints([]);
      setCheckpointsError(null);
      setCheckpointsLoading(false);
      setLoadedCheckpointSessionId("");
      setLastHandledCheckpointEventKey("");
      setSelectedCheckpointId(null);
      setSelectedCheckpointFilePath(null);
      setCheckpointDetailsError(null);
      setCheckpointDetailsLoadingId("");
      setCheckpointRestoreError(null);
      setCheckpointRestoreNotice(null);
      return;
    }

    if (
      !shouldReloadRuntimeCheckpoints({
        lastHandledEventKey: lastHandledCheckpointEventKey,
        lastRuntimeEventKey,
        lastRuntimeEventType,
        loadedCheckpointSessionId,
        sessionId,
      })
    ) {
      return;
    }

    let cancelled = false;

    void (async () => {
      setCheckpointsLoading(true);
      setCheckpointsError(null);

      try {
        const response = await listSessionCheckpoints(sessionId!, { limit: 8 });
        if (cancelled) {
          return;
        }

        setCheckpoints(response.checkpoints);
        setLoadedCheckpointSessionId(sessionId!);
        setSelectedCheckpointId((current) =>
          response.checkpoints.some((checkpoint) => checkpoint.id === current)
            ? current
            : response.checkpoints[0]?.id ?? null,
        );
        setLastHandledCheckpointEventKey(lastRuntimeEventKey);
      } catch (error) {
        if (cancelled) {
          return;
        }

        setCheckpoints([]);
        setCheckpointsError(
          error instanceof Error ? error.message : "failed to load restore points",
        );
      } finally {
        if (!cancelled) {
          setCheckpointsLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    lastHandledCheckpointEventKey,
    lastRuntimeEventKey,
    lastRuntimeEventType,
    loadedCheckpointSessionId,
    sessionId,
  ]);

  useEffect(() => {
    if (!sessionId) {
      setBacktrackAuditEntries([]);
      setBacktrackAuditError(null);
      setBacktrackAuditLoading(false);
      setLoadedAuditSessionId("");
      setLastHandledAuditEventKey("");
      return;
    }

    if (
      !shouldReloadBacktrackAudit({
        lastHandledEventKey: lastHandledAuditEventKey,
        lastRuntimeEventKey,
        lastRuntimeEventType,
        loadedAuditSessionId,
        sessionId,
      })
    ) {
      return;
    }

    let cancelled = false;

    void (async () => {
      setBacktrackAuditLoading(true);
      setBacktrackAuditError(null);

      try {
        const response = await listSessionBacktrackAudit(sessionId);
        if (cancelled) {
          return;
        }

        setBacktrackAuditEntries(response.entries ?? []);
        setLoadedAuditSessionId(sessionId);
        setLastHandledAuditEventKey(lastRuntimeEventKey);
      } catch (error) {
        if (cancelled) {
          return;
        }

        setBacktrackAuditEntries([]);
        setBacktrackAuditError(
          error instanceof Error ? error.message : "failed to load backtrack audit",
        );
      } finally {
        if (!cancelled) {
          setBacktrackAuditLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    lastHandledAuditEventKey,
    lastRuntimeEventKey,
    lastRuntimeEventType,
    loadedAuditSessionId,
    sessionId,
  ]);

  useEffect(() => {
    if (!sessionId || !selectedCheckpointId) {
      return;
    }

    let cancelled = false;

    void (async () => {
      setCheckpointDetailsLoadingId(selectedCheckpointId);
      setCheckpointDetailsError(null);

      try {
        const [previewResponse, filesResponse] = await Promise.all([
          previewSessionCheckpoint(sessionId, selectedCheckpointId, "both"),
          getSessionCheckpointFiles(sessionId, selectedCheckpointId),
        ]);
        if (cancelled) {
          return;
        }

        const preview = previewResponse.result;
        const files = filesResponse.files;

        setCheckpointPreviewById((current) => ({
          ...current,
          [selectedCheckpointId]: preview,
        }));
        setCheckpointFilesById((current) => ({
          ...current,
          [selectedCheckpointId]: files,
        }));
        setSelectedCheckpointFilePath((current) =>
          current &&
          (preview.preview_files ?? []).some((file) => file.path === current)
            ? current
            : current && files.some((file) => file.path === current)
              ? current
              : pickInitialCheckpointFilePath(files, preview.preview_files ?? []),
        );
      } catch (error) {
        if (cancelled) {
          return;
        }

        setCheckpointDetailsError(
          error instanceof Error
            ? error.message
            : "failed to load checkpoint preview details",
        );
      } finally {
        if (!cancelled) {
          setCheckpointDetailsLoadingId("");
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [selectedCheckpointId, sessionId]);

  const selectedCheckpoint = sessionId
    ? checkpoints.find((checkpoint) => checkpoint.id === selectedCheckpointId) ?? null
    : null;
  const checkpointPreview = useMemo(
    () => (selectedCheckpointId ? checkpointPreviewById[selectedCheckpointId] : undefined),
    [checkpointPreviewById, selectedCheckpointId],
  );
  const checkpointFiles = useMemo(
    () => (selectedCheckpointId ? checkpointFilesById[selectedCheckpointId] ?? [] : []),
    [checkpointFilesById, selectedCheckpointId],
  );
  const checkpointProvenanceSummary = formatCheckpointProvenanceSummary(
    checkpointPreview?.provenance ?? selectedCheckpoint?.provenance,
  );
  const checkpointConversationSummary = buildCheckpointConversationSummary(
    checkpointPreview?.conversation_messages,
  );
  const {
    checkpointFileCode,
    checkpointProvenance,
    checkpointPreviewFiles,
    selectedCheckpointFile,
    selectedCheckpointFilePath: resolvedSelectedCheckpointFilePath,
    selectedCheckpointPreviewFile,
  } = useMemo(
    () =>
      resolveCheckpointDetailState({
        checkpointFiles,
        checkpointPreview,
        selectedCheckpoint,
        selectedCheckpointFilePath,
      }),
    [checkpointFiles, checkpointPreview, selectedCheckpoint, selectedCheckpointFilePath],
  );

  function handleSelectCheckpoint(checkpointId: string) {
    setSelectedCheckpointId(checkpointId);
    setSelectedCheckpointFilePath(null);
  }

  function handleSelectCheckpointFile(filePath: string) {
    setSelectedCheckpointFilePath(filePath);
  }

  async function handleRestoreCheckpoint(
    mode: RuntimeSessionCheckpointPreviewMode = "both",
  ) {
    if (!sessionId || !selectedCheckpointId) {
      return;
    }

    const confirmed =
      typeof window === "undefined"
        ? true
        : window.confirm(
            `Restore checkpoint ${selectedCheckpointId} with mode="${mode}"?\nThis may rewrite conversation and/or workspace files.`,
          );
    if (!confirmed) {
      return;
    }

    setCheckpointRestorePendingId(selectedCheckpointId);
    setCheckpointRestoreError(null);
    setCheckpointRestoreNotice(null);
    try {
      const response = await restoreSessionCheckpoint(sessionId, selectedCheckpointId, mode);
      if (!response.ok) {
        throw new Error(response.error || "Failed to restore checkpoint");
      }
      const applied = response.result?.applied_paths?.length ?? 0;
      const conversationChanged = response.result?.conversation_changed ? "yes" : "no";
      setCheckpointRestoreNotice(
        `Restored ${selectedCheckpointId} (mode=${mode}, files=${applied}, conversation_changed=${conversationChanged}).`,
      );
      // Force list reload on next event/effect cycle.
      setLoadedCheckpointSessionId("");
      setCheckpoints([]);
    } catch (error) {
      setCheckpointRestoreError(
        error instanceof Error ? error.message : "Failed to restore checkpoint",
      );
    } finally {
      setCheckpointRestorePendingId("");
    }
  }

  return {
    backtrackAuditEntries,
    backtrackAuditError,
    backtrackAuditLoading,
    checkpointConversationSummary,
    checkpointDetailsError,
    checkpointDetailsLoadingId,
    checkpointFileCode,
    checkpointFiles,
    checkpointPreview,
    checkpointPreviewFiles,
    checkpointProvenance,
    checkpointProvenanceSummary,
    checkpointRestoreError,
    checkpointRestoreNotice,
    checkpointRestorePendingId,
    checkpoints,
    checkpointsError,
    checkpointsLoading,
    onRestoreCheckpoint: handleRestoreCheckpoint,
    onSelectCheckpoint: handleSelectCheckpoint,
    onSelectCheckpointFile: handleSelectCheckpointFile,
    selectedCheckpoint,
    selectedCheckpointFile,
    selectedCheckpointFilePath: resolvedSelectedCheckpointFilePath,
    selectedCheckpointId,
    selectedCheckpointPreviewFile,
  };
}
