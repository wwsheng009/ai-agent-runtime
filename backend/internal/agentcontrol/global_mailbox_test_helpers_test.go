package agentcontrol

import (
	"context"
	"fmt"
	"strings"
)

func (s *SQLiteGlobalMailboxRegistryStore) getGlobalMailboxRecordByPrimaryKey(ctx context.Context, primaryKey string) (MailboxRecord, error) {
	primaryKey = strings.TrimSpace(primaryKey)
	if primaryKey == "" {
		return MailboxRecord{}, fmt.Errorf("global mailbox primary key is required")
	}
	db, err := s.dbHandle()
	if err != nil {
		return MailboxRecord{}, err
	}
	return getGlobalMailboxRecordByPrimaryKeySQL(ctx, db, "agent_control_global_mailbox_records", primaryKey)
}
