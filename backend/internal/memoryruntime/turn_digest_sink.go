package memoryruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

var _ chat.TurnDigestSink = (*Runtime)(nil)

// RecordTurnDigest implements chat.TurnDigestSink. It persists only bounded,
// redacted turn text and never invokes an LLM.
func (r *Runtime) RecordTurnDigest(ctx context.Context, digest chat.TurnDigest) error {
	if r == nil || r.Store == nil || !r.enabled() || r.Config.GenerationDisabled || !r.Config.TurnDigestEnabled {
		return nil
	}
	sessionID := strings.TrimSpace(digest.SessionID)
	identity, err := r.digestSessionIdentity(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("memoryruntime: resolve digest session identity: %w", err)
	}
	if sessionID == "" || identity.Session.ID != sessionID {
		return fmt.Errorf("memoryruntime: digest session identity mismatch")
	}
	if r.Config.ProjectKey != "" && r.Config.ProjectKey != r.Store.ProjectKey() {
		return fmt.Errorf("memoryruntime: digest project identity mismatch")
	}
	// Producer isolation is stricter than historical extraction: an unknown
	// workspace must not cause raw turn text to be copied into this project.
	if strings.TrimSpace(r.Config.Workspace) == "" || strings.TrimSpace(identity.Session.WorkspacePath) == "" {
		return fmt.Errorf("memoryruntime: digest workspace identity is unavailable")
	}
	if ok, reason := r.eligible(ctx, identity.Session, TriggerSessionEnd); !ok {
		if reason == "session_mode_error" {
			return fmt.Errorf("memoryruntime: digest session mode is unavailable")
		}
		return nil
	}
	// A standalone producer still honors the persisted session opt-out even
	// when no external SessionModeReader was configured.
	if r.Modes == nil {
		mode, err := r.Store.GetSessionMode(ctx, sessionID)
		if err != nil {
			return err
		}
		if mode != memorystore.SessionModeEnabled {
			return nil
		}
	}
	if identity.Session.IsSubagent && identity.ParentSessionID == "" && identity.AgentType == "" {
		return fmt.Errorf("memoryruntime: digest subagent provenance is unavailable")
	}
	userText := strings.TrimSpace(digest.Prompt)
	assistantText := strings.TrimSpace(digest.AssistantOutput)
	if r.Config.RedactionEnabled {
		userText, _ = memoryredact.Redact(userText)
		assistantText, _ = memoryredact.Redact(assistantText)
	}
	userText, userTruncated := truncateTurnDigestText(userText, r.Config.TurnDigestMaxUserChars)
	assistantText, assistantTruncated := truncateTurnDigestText(assistantText, r.Config.TurnDigestMaxAssistantChars)
	if !r.Config.TurnDigestStoreText {
		userText = ""
		assistantText = ""
	}
	outcome := strings.TrimSpace(digest.Outcome)
	if outcome == "" {
		outcome = "success"
	}
	usageJSON := "{}"
	if digest.Usage != nil {
		if raw, err := json.Marshal(digest.Usage); err == nil {
			usageJSON = string(raw)
		}
	}
	metadataJSON := "{}"
	if raw, err := json.Marshal(map[string]interface{}{
		"assistant_stream_id": strings.TrimSpace(digest.AssistantStreamID),
		"store_text":          r.Config.TurnDigestStoreText,
		"user_truncated":      userTruncated,
		"assistant_truncated": assistantTruncated,
	}); err == nil {
		metadataJSON = string(raw)
	}
	now := time.Now().UTC()
	_, err = r.Store.RecordTurnDigest(ctx, memorystore.TurnDigest{
		Scope:              memorystore.ScopeProject,
		ProjectKey:         r.Store.ProjectKey(),
		SessionID:          sessionID,
		TurnID:             strings.TrimSpace(digest.TurnID),
		TraceID:            strings.TrimSpace(digest.TraceID),
		ParentSessionID:    identity.ParentSessionID,
		AgentType:          identity.AgentType,
		UserMessageID:      strings.TrimSpace(digest.UserMessageID),
		AssistantMessageID: strings.TrimSpace(digest.AssistantMessageID),
		MessageSeqEnd:      digest.MessageSeqEnd,
		UserText:           userText,
		AssistantText:      assistantText,
		TextTruncated:      userTruncated || assistantTruncated,
		Outcome:            outcome,
		Steps:              digest.Steps,
		UsageJSON:          usageJSON,
		MetadataJSON:       metadataJSON,
		Status:             memorystore.TurnDigestPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	return err
}
