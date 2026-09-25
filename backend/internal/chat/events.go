package chat

// Runtime event types emitted by the session actor.
const (
	EventSessionStart       = "session_start"
	EventSessionEnd         = "session_end"
	EventSessionInterrupted = "session_interrupted"
	EventAssistantDelta     = "assistant_delta"
	EventAssistantReasoning = "assistant_reasoning"
	// EventAssistantReasoningDelta is the canonical runtime stream event name.
	// Keep EventAssistantReasoning above as a compatibility alias for clients
	// which still publish the historical underscore spelling.
	EventAssistantReasoningDelta = "assistant.reasoning"
	EventAssistantImageProgress  = "assistant.image_progress"
	EventAssistantMessage        = "assistant_message"
	EventLLMRequestStarted       = "llm_request_started"
	EventLLMRequestFinished      = "llm_request_finished"
	EventToolStarted             = "tool_started"
	EventToolFinished            = "tool_finished"
	EventToolReceiptRecorded     = "tool_receipt_recorded"
	EventToolReceiptReplayed     = "tool_receipt_replayed"
	EventApprovalRequested       = "approval_requested"
	EventApprovalResolved        = "approval_resolved"
	EventQuestionAsked           = "question_asked"
	EventQuestionAnswered        = "question_answered"
	EventCheckpointCreated       = "checkpoint_created"
	// EventPlanModeChanged fires whenever durable plan-mode state changes
	// (enter, exit, request_changes, model exit request). Clients use it to
	// reload the plan surface instead of polling.
	EventPlanModeChanged = "plan_mode_changed"
	// EventPlanArchiveFailed reports a best-effort plan artifact archive failure
	// (index/snapshot). The plan-mode transition itself already succeeded.
	EventPlanArchiveFailed = "plan_archive_failed"
	// EventPlanReviewAvailable is the run-end backstop: a run finished while plan
	// mode was active, the plan file holds a revision the user has not reviewed
	// yet, and the model never asked for a verdict. Hosts use it to surface the
	// review entry point instead of leaving the plan silently unreviewed.
	EventPlanReviewAvailable = "plan_review_available"
	// EventPlanReviewRequested fires when the plan_review tool opens a plan in
	// the review surface. It is read-only: clients reload/focus the plan view,
	// the review itself is still decided by the user.
	EventPlanReviewRequested     = "plan_review_requested"
	EventSessionCompactStarted   = "session_compact_started"
	EventSessionCompactCompleted = "session_compact_completed"
	EventSessionCompactSkipped   = "session_compact_skipped"
	EventSessionCompactFailed    = "session_compact_failed"
	EventContextReconciled       = "context_reconciled"
	EventRewindStarted           = "rewind_started"
	EventRewindFinished          = "rewind_finished"
	EventBacktrackStarted        = "backtrack_started"
	EventBacktrackFinished       = "backtrack_finished"
	EventJobStarted              = "job_started"
	EventJobOutput               = "job_output"
	EventJobCancelled            = "job_cancelled"
	EventJobFinished             = "job_finished"
	EventMailboxReceived         = "mailbox_received"
)
