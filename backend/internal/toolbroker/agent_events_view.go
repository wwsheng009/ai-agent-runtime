package toolbroker

import (
	"context"
	"fmt"
	"strings"
)

// Bound both store round trips and raw rows, independently of the caller's
// visible limit. Only the first read may wait under the host's wait policy.
const (
	agentEventsViewScanMaxPages     = 16
	agentEventsViewScanMaxRawEvents = 1024
	agentEventsDefaultReadLimit     = 20 // Matches both ReadEvents hosts.
)

// readAgentEventsWithView uses the hosts' existing scoped, non-destructive raw
// reads. The original request must stay intact: the broker memo describes the
// outer window, not the last internal page.
func (b *Broker) readAgentEventsWithView(ctx context.Context, request ReadAgentEventsArgs) (*AgentEventsResult, error) {
	if NormalizeAgentEventsView(request.View) != AgentEventsViewToolProgress {
		return b.AgentSessions.ReadEvents(ctx, request)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = agentEventsDefaultReadLimit
	}
	pageArgs := request
	pageArgs.View = AgentEventsViewAll // Project once, after merging raw pages.
	var result *AgentEventsResult
	visible := 0
	rawCount := 0
	for pages := 0; pages < agentEventsViewScanMaxPages && visible < limit && rawCount < agentEventsViewScanMaxRawEvents; pages++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Never fetch more rows than remaining visible slots. Every fetched row
		// can then be consumed; no visible suffix is trimmed behind LatestSeq.
		pageArgs.Limit = min(limit-visible, agentEventsViewScanMaxRawEvents-rawCount)
		page, err := b.AgentSessions.ReadEvents(ctx, pageArgs)
		if err != nil {
			return nil, err
		}
		if page == nil {
			if result == nil {
				return nil, nil
			}
			return nil, fmt.Errorf("read_agent_events: raw reader returned a nil continuation page")
		}
		if len(page.Events) > pageArgs.Limit || page.Filtered != 0 || NormalizeAgentEventsView(page.View) == AgentEventsViewToolProgress {
			return nil, fmt.Errorf("read_agent_events: projection scan requires unprojected raw pages within limit=%d", pageArgs.Limit)
		}
		if page.HasMore && page.LatestSeq <= pageArgs.AfterSeq {
			// Fail without publishing a cursor or updating the read memo. The
			// caller can retry its original window; no events were acknowledged.
			return nil, fmt.Errorf("read_agent_events: raw page has_more=true without advancing after_seq=%d", pageArgs.AfterSeq)
		}
		if result == nil {
			result = &AgentEventsResult{SessionID: page.SessionID, LatestSeq: request.AfterSeq}
		} else if result.SessionID != page.SessionID {
			return nil, fmt.Errorf("read_agent_events: raw reader changed session during projection scan")
		}
		result.Events = append(result.Events, page.Events...)
		rawCount += len(page.Events)
		result.LatestSeq = max(result.LatestSeq, page.LatestSeq)
		result.HasMore = page.HasMore
		result.UnreadCount = 0
		if page.HasMore {
			result.UnreadCount = CapAgentEventsUnread(page.UnreadCount)
		}
		result.TimedOut = page.TimedOut
		hasApproval := false
		for _, event := range page.Events {
			if keepsAgentEventForToolProgress(event) {
				visible++
			}
			hasApproval = hasApproval || agentEventHasApproval(event)
		}
		if !page.HasMore || page.TimedOut || hasApproval {
			break
		}
		pageArgs.AfterSeq = result.LatestSeq
		pageArgs.WaitMs = 0
	}
	return ApplyAgentEventsView(result, request.View), nil
}

func agentEventHasApproval(event AgentEventItem) bool {
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	if strings.Contains(eventType, "approval") || strings.Contains(eventType, "waiting_approval") {
		return true
	}
	if status, ok := event.Payload["status"].(string); ok && strings.EqualFold(strings.TrimSpace(status), "waiting_approval") {
		return true
	}
	pending, _ := event.Payload["pending_approval"].(bool)
	return pending
}

func keepsAgentEventForToolProgress(event AgentEventItem) bool {
	return KeepsAgentEventForView(AgentEventsViewToolProgress, event.Type) || agentEventHasApproval(event)
}

// refreshAgentEventsViewGuidance replaces advice written for the raw Count. The
// cursor and unread count still describe raw events, not just the visible ones.
func refreshAgentEventsViewGuidance(result *AgentEventsResult) *AgentEventsResult {
	result.NextAction = ""
	FinalizeAgentEventsResult(result)
	cursor := fmt.Sprintf("keep view=tool_progress and use after_seq=%d (raw high-water cursor, not the last visible event); do not rewind after_seq", result.LatestSeq)
	if strings.HasPrefix(result.NextAction, "resolve_pending_approval:") {
		result.NextAction += "; after resolving the approval, " + cursor
		return result
	}
	if !result.HasMore {
		result.NextAction += fmt.Sprintf("; filtered=%d; for future reads, %s", result.Filtered, cursor)
		return result
	}
	detail := "more raw event(s) remain (not necessarily visible in this view)"
	switch {
	case result.UnreadCount >= AgentEventsUnreadProbeLimit:
		detail = fmt.Sprintf("at least %d more raw event(s) remain", result.UnreadCount)
	case result.UnreadCount > 0:
		detail = fmt.Sprintf("%d more raw event(s) remain", result.UnreadCount)
	}
	if result.Count == 0 {
		result.NextAction = fmt.Sprintf("continue_tool_progress_scan: 0 tool_progress event(s) returned by the bounded raw scan; filtered=%d; %s. Continue read_agent_events: %s", result.Filtered, detail, cursor)
	} else {
		result.NextAction = fmt.Sprintf("consume_events_then_advance: %d tool_progress event(s) returned; filtered=%d; %s. Consume these first, then continue read_agent_events: %s", result.Count, result.Filtered, detail, cursor)
	}
	return result
}
