package chat

// Runtime event types emitted by the session actor.
const (
	EventSessionStart        = "session_start"
	EventSessionEnd          = "session_end"
	EventSessionInterrupted  = "session_interrupted"
	EventAssistantDelta      = "assistant_delta"
	EventAssistantMessage    = "assistant_message"
	EventLLMRequestStarted   = "llm_request_started"
	EventLLMRequestFinished  = "llm_request_finished"
	EventToolStarted         = "tool_started"
	EventToolFinished        = "tool_finished"
	EventToolReceiptRecorded = "tool_receipt_recorded"
	EventToolReceiptReplayed = "tool_receipt_replayed"
	EventApprovalRequested   = "approval_requested"
	EventApprovalResolved    = "approval_resolved"
	EventQuestionAsked       = "question_asked"
	EventQuestionAnswered    = "question_answered"
	EventCheckpointCreated   = "checkpoint_created"
	EventRewindStarted       = "rewind_started"
	EventRewindFinished      = "rewind_finished"
	EventJobStarted          = "job_started"
	EventJobOutput           = "job_output"
	EventJobCancelled        = "job_cancelled"
	EventJobFinished         = "job_finished"
	EventMailboxReceived     = "mailbox_received"
)
