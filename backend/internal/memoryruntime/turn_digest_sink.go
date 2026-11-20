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
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

var _ chat.TurnDigestSink = (*Runtime)(nil)

// RecordTurnDigest implements chat.TurnDigestSink. It persists only bounded,
// redacted turn text and never invokes an LLM.
func (r *Runtime) RecordTurnDigest(ctx context.Context, digest chat.TurnDigest) error {
	if r == nil || r.Store == nil {
		return nil
	}
	if !r.enabled() {
		return nil
	}
	if receiptID := strings.TrimSpace(digest.CaptureReceiptID); receiptID != "" {
		if !r.Config.TurnDigestEnabled {
			r.recordDigestSkipped("turn_digest_disabled")
			return nil
		}
		// This is only the bounded receipt -> digest/job handoff. The extractor
		// is exclusively run by the asynchronous worker/maintenance consumer.
		if err := r.MaterializeTurnCaptureReceipt(ctx, receiptID); err != nil {
			r.recordDigestFailed(err)
			return err
		}
		r.recordDigestWritten()
		return nil
	}
	if r.Config.GenerationDisabled {
		r.recordDigestSkipped("generation_disabled")
		return nil
	}
	if !r.Config.TurnDigestEnabled {
		r.recordDigestSkipped("turn_digest_disabled")
		return nil
	}
	sessionID := strings.TrimSpace(digest.SessionID)
	identity, err := r.digestSessionIdentity(ctx, sessionID)
	if err != nil {
		r.recordDigestFailed(err)
		return fmt.Errorf("memoryruntime: resolve digest session identity: %w", err)
	}
	if sessionID == "" || identity.Session.ID != sessionID {
		err := fmt.Errorf("memoryruntime: digest session identity mismatch")
		r.recordDigestFailed(err)
		return err
	}
	if r.Config.ProjectKey != "" && r.Config.ProjectKey != r.Store.ProjectKey() {
		err := fmt.Errorf("memoryruntime: digest project identity mismatch")
		r.recordDigestFailed(err)
		return err
	}
	// Producer isolation is stricter than historical extraction: an unknown
	// workspace must not cause raw turn text to be copied into this project.
	if strings.TrimSpace(r.Config.Workspace) == "" || strings.TrimSpace(identity.Session.WorkspacePath) == "" {
		err := fmt.Errorf("memoryruntime: digest workspace identity is unavailable")
		r.recordDigestFailed(err)
		return err
	}
	if ok, reason := r.eligible(ctx, identity.Session, TriggerSessionEnd); !ok {
		if reason == "session_mode_error" {
			err := fmt.Errorf("memoryruntime: digest session mode is unavailable")
			r.recordDigestFailed(err)
			return err
		}
		r.recordDigestSkipped(reason)
		if strings.HasPrefix(reason, "session_mode_") {
			logpkg.Infof("memory turn digest skipped: session=%s reason=%s", sessionID, reason)
		} else {
			logpkg.Warnf("memory turn digest skipped: session=%s reason=%s", sessionID, reason)
		}
		return nil
	}
	// A standalone producer still honors the persisted session opt-out even
	// when no external SessionModeReader was configured.
	if r.Modes == nil {
		mode, err := r.Store.GetSessionMode(ctx, sessionID)
		if err != nil {
			r.recordDigestFailed(err)
			return err
		}
		if mode != memorystore.SessionModeEnabled {
			r.recordDigestSkipped("session_mode_" + string(mode))
			logpkg.Infof("memory turn digest skipped: session=%s reason=session_mode_%s", sessionID, mode)
			return nil
		}
	}
	if identity.Session.IsSubagent && identity.ParentSessionID == "" && identity.AgentType == "" {
		err := fmt.Errorf("memoryruntime: digest subagent provenance is unavailable")
		r.recordDigestFailed(err)
		return err
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
		SessionGeneration:  identity.Session.SessionGeneration,
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
	if err != nil {
		r.recordDigestFailed(err)
	} else {
		r.recordDigestWritten()
	}
	return err
}
