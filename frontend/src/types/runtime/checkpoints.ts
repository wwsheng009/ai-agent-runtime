export type RuntimeCheckpointProvenanceSummary = {
  source_refs?: string[];
  profile_resource_refs?: string[];
  profile_resource_kinds?: Record<string, number>;
  profile_resource_count?: number;
  profile_memory_count?: number;
  profile_notes_count?: number;
  profile_resource_labels?: string[];
};

export type RuntimeSessionCheckpointSummary = {
  id: string;
  session_id: string;
  task_id?: string;
  reason?: string;
  history_hash?: string;
  message_count: number;
  conversation_exact?: boolean;
  conversation_message_count?: number;
  created_at: string;
  metadata?: Record<string, unknown>;
  provenance?: RuntimeCheckpointProvenanceSummary;
};

export type RuntimeSessionCheckpointsResponse = {
  checkpoints: RuntimeSessionCheckpointSummary[];
  count: number;
};

export type RuntimeSessionCheckpointPreviewMode = "both" | "code" | "conversation";

export type RuntimeSessionCheckpointPreviewFile = {
  path: string;
  change: string;
  diff_text?: string;
};

export type RuntimeSessionCheckpointConversationMessage = {
  role?: string;
  content?: string;
};

export type RuntimeSessionCheckpointPreviewResult = {
  checkpoint_id: string;
  mode: string;
  applied_paths?: string[];
  errors?: string[];
  preview?: string[];
  preview_files?: RuntimeSessionCheckpointPreviewFile[];
  conversation_changed?: boolean;
  conversation_head?: number;
  conversation_exact?: boolean;
  conversation_messages?: RuntimeSessionCheckpointConversationMessage[];
  provenance?: RuntimeCheckpointProvenanceSummary;
};

export type RuntimeSessionCheckpointPreviewResponse = {
  result: RuntimeSessionCheckpointPreviewResult;
};

export type RuntimeSessionCheckpointFile = {
  id: string;
  checkpoint_id: string;
  path: string;
  op: string;
  before_blob_id?: string;
  after_blob_id?: string;
  before_hash?: string;
  after_hash?: string;
  diff_text?: string;
};

export type RuntimeSessionCheckpointFilesResponse = {
  files: RuntimeSessionCheckpointFile[];
  count: number;
};

export type RuntimeSessionCheckpointRestoreResponse = {
  ok: boolean;
  result?: RuntimeSessionCheckpointPreviewResult;
  error?: string;
};
