package agentcontrol

import (
	"context"
	"strings"
	"time"
)

// SameAgentBinding reports whether two records describe the same durable
// identity binding. Records are considered the same binding when they share an
// agent id, or when they point at the same root session + agent path pair.
func SameAgentBinding(left, right AgentRecord) bool {
	left = left.Normalize()
	right = right.Normalize()
	if left.AgentID != "" && right.AgentID != "" && strings.EqualFold(left.AgentID, right.AgentID) {
		return true
	}
	return left.RootSessionID != "" &&
		right.RootSessionID != "" &&
		left.AgentPath != "" &&
		right.AgentPath != "" &&
		strings.EqualFold(left.RootSessionID, right.RootSessionID) &&
		strings.EqualFold(left.AgentPath, right.AgentPath)
}

// CloseStaleAgentSessionBindings closes durable identity rows that still route
// the same session id under a different binding (different agent id or
// different root/path). Session ids can be reused after a local or remote
// restart, so a rebind that skips this step leaves two active rows pointing at
// one container and inflates active-thread counts.
//
// It is shared by the API runtime and the CLI local runtime so both hosts
// resolve stale bindings identically.
func CloseStaleAgentSessionBindings(ctx context.Context, store AgentRegistryStore, record AgentRecord) error {
	if store == nil {
		return nil
	}
	record = record.Normalize()
	if record.SessionID == "" {
		return nil
	}
	records, err := store.ListAgentControlAgents(ctx, AgentFilter{
		SessionID: record.SessionID,
	})
	if err != nil {
		return err
	}
	closedAt := time.Now().UTC()
	for _, existing := range records {
		existing = existing.Normalize()
		if SameAgentBinding(existing, record) {
			continue
		}
		if existing.RootSessionID == "" || existing.AgentPath == "" {
			continue
		}
		if _, err := store.CloseAgentControlAgentSubtree(ctx, existing.RootSessionID, existing.AgentPath, closedAt); err != nil {
			return err
		}
	}
	return nil
}
