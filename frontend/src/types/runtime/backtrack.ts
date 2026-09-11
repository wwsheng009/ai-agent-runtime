export type RuntimeSessionUserTurn = {
  index: number;
  message_index: number;
  preview: string;
  end_message_index: number;
  message_id?: string;
  turn_id?: string;
  has_later_mutation?: boolean;
  checkpoint_ids?: string[];
  base_checkpoint_id?: string;
};

export type RuntimeSessionTurnsResponse = {
  session_id: string;
  turns: RuntimeSessionUserTurn[];
  count: number;
};

export type RuntimeSessionBacktrackMode = "conversation" | "both" | "code";

export type RuntimeSessionBacktrackRequest = {
  user_turn_index?: number;
  message_index?: number;
  message_id?: string;
  mode?: RuntimeSessionBacktrackMode;
  edit_prompt?: string;
  auto_submit?: boolean;
  include_anchor?: boolean;
  preview_only?: boolean;
};

export type RuntimeSessionBacktrackCodeRestore = {
  checkpoint_id?: string;
  mode?: string;
  applied_paths?: string[];
  errors?: string[];
  preview?: string[];
};

export type RuntimeSessionBacktrackTombstone = {
  id: string;
  created_at: string;
  session_id?: string;
  mode?: string;
  reason?: string;
  user_turn_index: number;
  message_index: number;
  message_id?: string;
  anchor_preview?: string;
  truncated_to_message_count: number;
  removed_message_count: number;
  removed_user_turns: number;
  prior_message_count?: number;
  removed_message_ids?: string[];
  removed_turn_ids?: string[];
  edited?: boolean;
  include_anchor?: boolean;
  base_checkpoint_id?: string;
  later_checkpoint_ids?: string[];
};

export type RuntimeSessionBacktrackResult = {
  session_id: string;
  mode: string;
  user_turn_index: number;
  message_index: number;
  message_id?: string;
  truncated_to_message_count: number;
  removed_message_count: number;
  removed_user_turns: number;
  anchor_preview?: string;
  edited_prompt?: string;
  composer_prompt?: string;
  include_anchor?: boolean;
  auto_submitted?: boolean;
  preview_only?: boolean;
  base_checkpoint_id?: string;
  later_checkpoint_ids?: string[];
  tombstone?: RuntimeSessionBacktrackTombstone;
  code_restore?: RuntimeSessionBacktrackCodeRestore;
  warnings?: string[];
  events_emitted?: string[];
};

export type RuntimeSessionBacktrackResponse = {
  ok: boolean;
  result?: RuntimeSessionBacktrackResult;
  error?: string;
  submit_result?: Record<string, unknown>;
};

export type RuntimeSessionBacktrackAuditResponse = {
  session_id: string;
  entries: RuntimeSessionBacktrackTombstone[];
  count: number;
};
