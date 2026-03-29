package team

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MailboxService provides higher-level helpers around team mailbox messages.
type MailboxService struct {
	Store Store
}

// MailboxDispatcher pushes a persisted mailbox message to active sessions.
type MailboxDispatcher interface {
	DispatchTeamMailboxMessage(ctx context.Context, message MailMessage) error
}

// MailboxDigest describes a digest build for unread mailbox items.
type MailboxDigest struct {
	Digest       string
	MessageIDs   []string
	MessageCount int
	MarkedRead   bool
}

// NewMailboxService creates a mailbox helper bound to a store.
func NewMailboxService(store Store) *MailboxService {
	return &MailboxService{Store: store}
}

// Send inserts a mailbox message and returns its ID.
func (m *MailboxService) Send(ctx context.Context, message MailMessage) (string, error) {
	if m == nil || m.Store == nil {
		return "", fmt.Errorf("mailbox store is not configured")
	}
	message.TeamID = strings.TrimSpace(message.TeamID)
	if message.TeamID == "" {
		return "", fmt.Errorf("team id is required")
	}
	if strings.TrimSpace(message.ToAgent) == "" {
		message.ToAgent = "*"
	}
	if strings.TrimSpace(message.Kind) == "" {
		message.Kind = "info"
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	return m.Store.InsertMail(ctx, message)
}

// Broadcast sends a message to all teammates in the team.
func (m *MailboxService) Broadcast(ctx context.Context, teamID, from, body string) (string, error) {
	return m.Send(ctx, MailMessage{
		TeamID:    strings.TrimSpace(teamID),
		FromAgent: strings.TrimSpace(from),
		ToAgent:   "*",
		Kind:      "info",
		Body:      strings.TrimSpace(body),
	})
}

// ListUnread returns unread messages for a teammate.
func (m *MailboxService) ListUnread(ctx context.Context, teamID, agentID string, limit int) ([]MailMessage, error) {
	if m == nil || m.Store == nil {
		return nil, fmt.Errorf("mailbox store is not configured")
	}
	return m.Store.ListMail(ctx, MailFilter{
		TeamID:           strings.TrimSpace(teamID),
		ToAgent:          strings.TrimSpace(agentID),
		UnreadOnly:       true,
		IncludeBroadcast: true,
		Limit:            limit,
	})
}

// Ack marks messages as read.
func (m *MailboxService) Ack(ctx context.Context, teamID string, messageIDs []string) error {
	return m.AckByAgent(ctx, teamID, "", messageIDs)
}

// AckByAgent marks messages as read for a specific teammate.
func (m *MailboxService) AckByAgent(ctx context.Context, teamID, agentID string, messageIDs []string) error {
	if m == nil || m.Store == nil {
		return fmt.Errorf("mailbox store is not configured")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return fmt.Errorf("team id is required")
	}
	if len(messageIDs) == 0 {
		return nil
	}
	ackedAt := time.Now().UTC()
	for _, id := range messageIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		receipt := MailReceipt{
			TeamID:    teamID,
			MessageID: strings.TrimSpace(id),
			AgentID:   strings.TrimSpace(agentID),
			AckedAt:   ackedAt,
		}
		if receipt.AgentID == "" {
			if err := m.Store.AckMail(ctx, teamID, receipt.MessageID, ackedAt); err != nil {
				return err
			}
			continue
		}
		if err := m.Store.RecordMailReceipt(ctx, receipt); err != nil {
			return err
		}
	}
	return nil
}

// ReadDigest returns a digest for unread messages and can optionally mark them as read.
func (m *MailboxService) ReadDigest(ctx context.Context, teamID, agentID string, maxItems int, markRead bool) (*MailboxDigest, error) {
	if m == nil || m.Store == nil {
		return nil, fmt.Errorf("mailbox store is not configured")
	}
	if maxItems <= 0 {
		maxItems = 4
	}
	messages, err := m.ListUnread(ctx, teamID, agentID, maxItems)
	if err != nil {
		return nil, err
	}
	digest := &MailboxDigest{
		Digest:       buildDigest(messages),
		MessageIDs:   make([]string, 0, len(messages)),
		MessageCount: len(messages),
	}
	for _, msg := range messages {
		if strings.TrimSpace(msg.ID) == "" {
			continue
		}
		digest.MessageIDs = append(digest.MessageIDs, strings.TrimSpace(msg.ID))
	}
	if markRead && len(digest.MessageIDs) > 0 {
		if err := m.AckByAgent(ctx, teamID, agentID, digest.MessageIDs); err != nil {
			return nil, err
		}
		digest.MarkedRead = true
	}
	return digest, nil
}

// BuildDigest composes a short digest of unread messages.
func (m *MailboxService) BuildDigest(ctx context.Context, teamID, agentID string, maxItems int) (string, error) {
	digest, err := m.ReadDigest(ctx, teamID, agentID, maxItems, false)
	if err != nil {
		return "", err
	}
	if digest == nil {
		return "", nil
	}
	return digest.Digest, nil
}

func buildDigest(messages []MailMessage) string {
	if len(messages) == 0 {
		return ""
	}
	lines := []string{"Team digest:"}
	for _, msg := range messages {
		lines = append(lines, formatDigestLine(msg))
	}
	return strings.Join(lines, "\n")
}

func formatDigestLine(message MailMessage) string {
	kind := strings.TrimSpace(message.Kind)
	if kind == "" {
		kind = "info"
	}
	from := strings.TrimSpace(message.FromAgent)
	to := strings.TrimSpace(message.ToAgent)
	header := kind
	if from != "" || to != "" {
		header = fmt.Sprintf("%s %s->%s", kind, firstNonEmptyString(from, "?"), firstNonEmptyString(to, "*"))
	}
	body := truncateLine(message.Body, 160)
	return fmt.Sprintf("- %s: %s", header, body)
}
