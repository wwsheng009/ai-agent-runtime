import type {
  RuntimeCheckpointProvenanceSummary,
  RuntimeSessionBacktrackTombstone,
  RuntimeSessionCheckpointConversationMessage,
  RuntimeSessionCheckpointFile,
  RuntimeSessionCheckpointPreviewFile,
  RuntimeSessionCheckpointSummary,
} from "@/types/runtime";

export function formatCheckpointTitle(checkpoint: RuntimeSessionCheckpointSummary) {
  const reason = checkpoint.reason?.trim();
  if (reason) {
    return reason;
  }
  return `Checkpoint ${checkpoint.id.slice(0, 12)}`;
}

export function formatCheckpointMeta(checkpoint: RuntimeSessionCheckpointSummary) {
  const parts = [`${checkpoint.message_count} messages`];

  if (checkpoint.conversation_exact) {
    parts.push("exact conversation");
  }

  if (checkpoint.task_id?.trim()) {
    parts.push(`task ${checkpoint.task_id.trim().slice(0, 12)}`);
  }

  return parts.join(" · ");
}

export function formatCheckpointReason(
  checkpoint: RuntimeSessionCheckpointSummary,
) {
  const reason = checkpoint.reason?.trim();
  if (reason) {
    return reason;
  }
  return "Runtime snapshot captured for later inspection.";
}

export function formatCheckpointProvenance(
  provenance: RuntimeCheckpointProvenanceSummary | undefined,
) {
  if (!provenance) {
    return [];
  }

  const labels = provenance.profile_resource_labels?.filter(Boolean) ?? [];
  if (labels.length > 0) {
    return labels.slice(0, 3);
  }

  const count = provenance.profile_resource_count ?? 0;
  if (count > 0) {
    return [`${count} profile resources`];
  }

  return [];
}

export function formatCheckpointProvenanceSummary(
  provenance: RuntimeCheckpointProvenanceSummary | undefined,
) {
  if (!provenance) {
    return [];
  }

  const parts: string[] = [];
  const sourceRefs = provenance.source_refs?.filter(Boolean) ?? [];
  if (sourceRefs.length > 0) {
    parts.push(`${sourceRefs.length} source refs`);
  }
  if ((provenance.profile_memory_count ?? 0) > 0) {
    parts.push(`${provenance.profile_memory_count} memories`);
  }
  if ((provenance.profile_notes_count ?? 0) > 0) {
    parts.push(`${provenance.profile_notes_count} notes`);
  }
  if ((provenance.profile_resource_count ?? 0) > 0) {
    parts.push(`${provenance.profile_resource_count} profile resources`);
  }

  return parts;
}

export function pickInitialCheckpointFilePath(
  files: RuntimeSessionCheckpointFile[],
  previewFiles: RuntimeSessionCheckpointPreviewFile[],
) {
  return previewFiles[0]?.path ?? files[0]?.path ?? null;
}

export function resolveCheckpointFileEntries(
  previewFiles: RuntimeSessionCheckpointPreviewFile[],
  files: RuntimeSessionCheckpointFile[],
) {
  return previewFiles.length > 0 ? previewFiles : files;
}

export function isCheckpointDetailLoading(
  selectedCheckpoint: RuntimeSessionCheckpointSummary | null | undefined,
  checkpointDetailsLoadingId: string,
) {
  return (
    selectedCheckpoint !== null &&
    selectedCheckpoint !== undefined &&
    checkpointDetailsLoadingId === selectedCheckpoint.id
  );
}

export function formatCheckpointFileChangeLabel(
  file:
    | RuntimeSessionCheckpointFile
    | RuntimeSessionCheckpointPreviewFile
    | undefined,
) {
  if (!file) {
    return "file";
  }

  const change =
    "change" in file && typeof file.change === "string"
      ? file.change
      : "op" in file && typeof file.op === "string"
        ? file.op
        : "file";

  return change.replace(/[_-]+/g, " ").trim() || "file";
}

export function buildCheckpointFileCode(
  file: RuntimeSessionCheckpointFile | undefined,
  previewFile: RuntimeSessionCheckpointPreviewFile | undefined,
) {
  if (previewFile?.diff_text?.trim()) {
    return {
      code: previewFile.diff_text,
      language: "diff",
      title: previewFile.path,
    };
  }

  if (file?.diff_text?.trim()) {
    return {
      code: file.diff_text,
      language: "diff",
      title: file.path,
    };
  }

  if (!file) {
    return {
      code: "No checkpoint file details available.",
      language: "text",
      title: "Checkpoint preview",
    };
  }

  return {
    code: JSON.stringify(file, null, 2),
    language: "json",
    title: file.path,
  };
}

export function buildCheckpointConversationSummary(
  messages: RuntimeSessionCheckpointConversationMessage[] | undefined,
) {
  if (!messages || messages.length === 0) {
    return [];
  }

  return messages.slice(0, 4).map((message, index) => {
    const role = message.role?.trim() || `message ${index + 1}`;
    const content = message.content?.trim() || "[empty message]";
    return {
      content,
      role,
    };
  });
}

export function formatBacktrackAuditTitle(entry: RuntimeSessionBacktrackTombstone) {
  const preview = entry.anchor_preview?.trim();
  if (preview) {
    return preview.length > 72 ? `${preview.slice(0, 69)}…` : preview;
  }

  const messageId = entry.message_id?.trim();
  if (messageId) {
    return `Backtrack ${messageId.slice(0, 12)}`;
  }

  return `User turn ${entry.user_turn_index}`;
}

export function formatBacktrackAuditMeta(entry: RuntimeSessionBacktrackTombstone) {
  const parts = [
    `turn ${entry.user_turn_index}`,
    `msg ${entry.message_index}`,
    `−${entry.removed_message_count} msgs`,
  ];

  if (entry.removed_user_turns > 0) {
    parts.push(`−${entry.removed_user_turns} turns`);
  }

  const mode = entry.mode?.trim();
  if (mode) {
    parts.push(mode);
  }

  if (entry.edited) {
    parts.push("edited");
  }

  return parts.join(" · ");
}

export function formatBacktrackAuditSummary(entry: RuntimeSessionBacktrackTombstone) {
  const reason = entry.reason?.trim();
  if (reason) {
    return reason;
  }

  return `Truncated to ${entry.truncated_to_message_count} messages; removed ${entry.removed_user_turns} user turn(s).`;
}

export function formatBacktrackAuditIdentity(entry: RuntimeSessionBacktrackTombstone) {
  const messageId = entry.message_id?.trim();
  if (messageId) {
    return messageId;
  }
  return entry.id;
}

export function pickInitialBacktrackAuditId(
  entries: RuntimeSessionBacktrackTombstone[],
  preferredId?: string | null,
) {
  if (preferredId && entries.some((entry) => entry.id === preferredId)) {
    return preferredId;
  }
  return entries[0]?.id ?? null;
}
