package commands

import (
	"context"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// sqliteCellBodyStore 是 ui.CellBodyStore 的 sqlite 适配器（任务 T1 / Spike-1）。
//
// internal/chat.SessionCellStore 只认不透明 payload；scene 的编解码与校验
// 全部留在 ui 层（设计文档 §14.2），依赖方向保持 ui ← commands → chat。
type sqliteCellBodyStore struct {
	store *runtimechat.SessionCellStore
}

func openSQLiteCellBodyStore(path string) (*sqliteCellBodyStore, error) {
	store, err := runtimechat.OpenSessionCellStore(path, 5*time.Second, 2048)
	if err != nil {
		return nil, err
	}
	return &sqliteCellBodyStore{store: store}, nil
}

func (s *sqliteCellBodyStore) Put(ctx context.Context, sessionID string, body ui.CellBodyPayload) error {
	return s.store.PutCell(ctx, runtimechat.SessionCellRow{
		SessionID: sessionID,
		CellID:    int64(body.Meta.CellID),
		Revision:  body.Meta.Revision,
		SourceFP:  body.Meta.SourceFP,
		RowsAtGen: body.Meta.RowsAtGen,
		Gen:       body.Meta.Gen,
		Encoding:  body.Meta.Encoding,
		Payload:   body.Data,
		CreatedAt: time.Now(),
	})
}

func (s *sqliteCellBodyStore) Get(ctx context.Context, sessionID string, cellID scene.CellID, revision uint64) (ui.CellBodyPayload, bool, error) {
	row, ok, err := s.store.GetCell(ctx, sessionID, int64(cellID), revision)
	if err != nil || !ok {
		return ui.CellBodyPayload{}, false, err
	}
	return ui.CellBodyPayload{
		Meta: ui.CellBodyMeta{
			CellID:    scene.CellID(row.CellID),
			Revision:  row.Revision,
			SourceFP:  row.SourceFP,
			RowsAtGen: row.RowsAtGen,
			Gen:       row.Gen,
			Encoding:  row.Encoding,
		},
		Data: row.Payload,
	}, true, nil
}

func (s *sqliteCellBodyStore) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *sqliteCellBodyStore) Stats() runtimechat.SessionCellStoreStats {
	if s == nil || s.store == nil {
		return runtimechat.SessionCellStoreStats{}
	}
	return s.store.Stats()
}
