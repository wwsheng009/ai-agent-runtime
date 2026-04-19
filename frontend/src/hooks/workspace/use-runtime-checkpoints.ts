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
  listSessionCheckpoints,
  previewSessionCheckpoint,
  type RuntimeSessionCheckpointFile,
  type RuntimeSessionCheckpointPreviewResult,
  type RuntimeSessionCheckpointSummary,
} from "@/lib/runtime-api";

type UseRuntimeCheckpointsOptions = {
  lastRuntimeEventType?: string;
  sessionId?: string;
};

type ShouldReloadRuntimeCheckpointsOptions = {
  checkpointsCount: number;
  lastRuntimeEventType?: string;
  loadedCheckpointSessionId: string;
  sessionId?: string;
};

type ResolveCheckpointDetailStateOptions = {
  checkpointFiles: RuntimeSessionCheckpointFile[];
  checkpointPreview?: RuntimeSessionCheckpointPreviewResult;
  selectedCheckpoint?: RuntimeSessionCheckpointSummary | null;
  selectedCheckpointFilePath: string | null;
};

export function shouldReloadRuntimeCheckpoints({
  checkpointsCount,
  lastRuntimeEventType,
  loadedCheckpointSessionId,
  sessionId,
}: ShouldReloadRuntimeCheckpointsOptions) {
  if (!sessionId) {
    return false;
  }

  return (
    loadedCheckpointSessionId !== sessionId ||
    checkpointsCount === 0 ||
    lastRuntimeEventType === "checkpoint_created"
  );
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
  sessionId,
}: UseRuntimeCheckpointsOptions) {
  const [checkpoints, setCheckpoints] = useState<RuntimeSessionCheckpointSummary[]>([]);
  const [checkpointsError, setCheckpointsError] = useState<string | null>(null);
  const [checkpointsLoading, setCheckpointsLoading] = useState(false);
  const [loadedCheckpointSessionId, setLoadedCheckpointSessionId] = useState("");
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

  useEffect(() => {
    if (
      !shouldReloadRuntimeCheckpoints({
        checkpointsCount: checkpoints.length,
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
    checkpoints.length,
    lastRuntimeEventType,
    loadedCheckpointSessionId,
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

  return {
    checkpointConversationSummary,
    checkpointDetailsError,
    checkpointDetailsLoadingId,
    checkpointFileCode,
    checkpointFiles,
    checkpointPreview,
    checkpointPreviewFiles,
    checkpointProvenance,
    checkpointProvenanceSummary,
    checkpoints,
    checkpointsError,
    checkpointsLoading,
    onSelectCheckpoint: handleSelectCheckpoint,
    onSelectCheckpointFile: handleSelectCheckpointFile,
    selectedCheckpoint,
    selectedCheckpointFile,
    selectedCheckpointFilePath: resolvedSelectedCheckpointFilePath,
    selectedCheckpointId,
    selectedCheckpointPreviewFile,
  };
}
