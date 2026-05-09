package team

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// AgentControlMailboxRegistry adapts team mailbox storage to the shared
// AgentControl mailbox registry. SQLite stores read the unified
// agent_control_mailbox_records table directly; non-SQLite stores fall back to
// projecting native team mailbox rows.
type AgentControlMailboxRegistry struct {
	Store Store
}

var _ agentcontrol.MailboxRegistryReader = AgentControlMailboxRegistry{}
var _ agentcontrol.MailboxRegistrySequencer = AgentControlMailboxRegistry{}

type agentControlMailboxRecordReaderStore interface {
	ListAgentControlMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error)
}

type agentControlMailboxRecordSequencerStore interface {
	LastAgentControlMailboxRecordSeq(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error)
}

// NewAgentControlMailboxRegistry creates a mailbox registry projection over a
// team store.
func NewAgentControlMailboxRegistry(store Store) AgentControlMailboxRegistry {
	return AgentControlMailboxRegistry{Store: store}
}

// ListAgentControlMailboxRecords returns team-scoped AgentControl mailbox
// records.
func (r AgentControlMailboxRegistry) ListAgentControlMailboxRecords(ctx context.Context, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	if r.Store == nil {
		return nil, nil
	}
	filter = normalizeTeamMailboxRecordFilter(filter)
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeTeam {
		return nil, nil
	}
	if filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, nil
	}
	if reader, ok := r.Store.(agentControlMailboxRecordReaderStore); ok {
		return reader.ListAgentControlMailboxRecords(ctx, filter)
	}
	return agentControlMailboxRecordsFromTeamStore(ctx, r.Store, filter)
}

// LastAgentControlMailboxRecordSeq returns the team-scoped mailbox registry
// high-water mark.
func (r AgentControlMailboxRegistry) LastAgentControlMailboxRecordSeq(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (int64, error) {
	if r.Store == nil {
		return 0, nil
	}
	filter = normalizeTeamMailboxRecordFilter(filter)
	if filter.Scope != "" && filter.Scope != agentcontrol.MailboxScopeTeam {
		return 0, nil
	}
	if filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return 0, nil
	}
	if sequencer, ok := r.Store.(agentControlMailboxRecordSequencerStore); ok {
		return sequencer.LastAgentControlMailboxRecordSeq(ctx, filter)
	}
	records, err := agentControlMailboxRecordsFromTeamStore(ctx, r.Store, agentcontrol.MailboxRecordFilter{
		Workflow: filter.Workflow,
		Scope:    filter.Scope,
		TeamID:   filter.TeamID,
	})
	if err != nil {
		return 0, err
	}
	var seq int64
	for _, record := range records {
		if record.Seq > seq {
			seq = record.Seq
		}
	}
	return seq, nil
}

func normalizeTeamMailboxRecordFilter(filter agentcontrol.MailboxRecordFilter) agentcontrol.MailboxRecordFilter {
	filter = filter.Normalize()
	if filter.Scope == "" {
		filter.Scope = agentcontrol.MailboxScopeTeam
	}
	if filter.Workflow == "" {
		filter.Workflow = agentcontrol.WorkflowSpawnTeam
	}
	return filter
}

func agentControlMailboxRecordsFromTeamStore(ctx context.Context, store Store, filter agentcontrol.MailboxRecordFilter) ([]agentcontrol.MailboxRecord, error) {
	teamIDs, err := mailboxRegistryTeamIDs(ctx, store, filter.TeamID)
	if err != nil {
		return nil, err
	}
	useCombinedCursor := strings.TrimSpace(filter.TeamID) == ""
	records := make([]agentcontrol.MailboxRecord, 0)
	for _, teamID := range teamIDs {
		afterSeq := filter.AfterSeq
		if useCombinedCursor {
			afterSeq = 0
		}
		messages, err := store.ListMail(ctx, MailFilter{
			TeamID:   teamID,
			AfterSeq: afterSeq,
		})
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			record := AgentControlMailboxRecord(message)
			if useCombinedCursor {
				sourceSeq := record.Seq
				record.SourceSeq = sourceSeq
				record.Seq = agentControlTeamProjectionSeq(teamID, sourceSeq, record.CreatedAt)
			}
			if record.Seq <= filter.AfterSeq {
				continue
			}
			records = append(records, record)
		}
	}
	sortAgentControlMailboxRecords(records)
	if filter.Limit > 0 && len(records) > filter.Limit {
		records = records[:filter.Limit]
	}
	return records, nil
}

func mailboxRegistryTeamIDs(ctx context.Context, store Store, teamID string) ([]string, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID != "" {
		return []string{teamID}, nil
	}
	ids, err := store.ListTeamIDs(ctx)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// AgentControlMailboxRecord projects one native team mailbox message into the
// shared AgentControl mailbox read model.
func AgentControlMailboxRecord(message MailMessage) agentcontrol.MailboxRecord {
	taskID := ""
	if message.TaskID != nil {
		taskID = strings.TrimSpace(*message.TaskID)
	}
	metadata := map[string]interface{}{}
	for key, value := range message.Metadata {
		metadata[key] = value
	}
	seq := message.ControlSeq
	if seq <= 0 {
		seq = message.Seq
	}
	return agentcontrol.MailboxRecord{
		Seq:       seq,
		GlobalSeq: message.GlobalSeq,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		Scope:     agentcontrol.MailboxScopeTeam,
		TeamID:    strings.TrimSpace(message.TeamID),
		TeamSeq:   message.Seq,
		MessageID: strings.TrimSpace(message.ID),
		FromAgent: strings.TrimSpace(message.FromAgent),
		ToAgent:   strings.TrimSpace(message.ToAgent),
		TaskID:    taskID,
		Kind:      strings.TrimSpace(message.Kind),
		Body:      strings.TrimSpace(message.Body),
		Metadata:  metadata,
		CreatedAt: message.CreatedAt,
		AckedAt:   cloneMailboxAckedAt(message.AckedAt),
	}.Normalize()
}

func cloneMailboxAckedAt(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sortAgentControlMailboxRecords(records []agentcontrol.MailboxRecord) {
	if len(records) < 2 {
		return
	}
	for i := 1; i < len(records); i++ {
		for j := i; j > 0 && teamMailboxRecordLess(records[j], records[j-1]); j-- {
			records[j], records[j-1] = records[j-1], records[j]
		}
	}
}

func teamMailboxRecordLess(left, right agentcontrol.MailboxRecord) bool {
	if left.Seq != right.Seq {
		return left.Seq < right.Seq
	}
	if left.TeamSeq != right.TeamSeq {
		return left.TeamSeq < right.TeamSeq
	}
	return left.MessageID < right.MessageID
}
