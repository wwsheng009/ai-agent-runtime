package chat

import (
	"context"
	"fmt"
	"time"
)

var (
	_ SessionStorageTurnCaptureCommitter                              = (*lazySessionStorage)(nil)
	_ SessionStorageTurnCaptureReceiptStore                           = (*lazySessionStorage)(nil)
	_ SessionStorageTurnCaptureReceiptGenerationStore                 = (*lazySessionStorage)(nil)
	_ SessionStorageMaterializedTurnCaptureReceiptGenerationPageStore = (*lazySessionStorage)(nil)
)

func (s *lazySessionStorage) turnCaptureCommitter(ctx context.Context) (SessionStorageTurnCaptureCommitter, error) {
	store, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	committer, ok := store.(SessionStorageTurnCaptureCommitter)
	if !ok {
		return nil, fmt.Errorf("chat: session storage does not support atomic turn capture receipts")
	}
	return committer, nil
}

func (s *lazySessionStorage) turnCaptureReceiptStore(ctx context.Context) (SessionStorageTurnCaptureReceiptStore, error) {
	store, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	receiptStore, ok := store.(SessionStorageTurnCaptureReceiptStore)
	if !ok {
		return nil, fmt.Errorf("chat: session storage does not support turn capture receipts")
	}
	return receiptStore, nil
}

func (s *lazySessionStorage) turnCaptureReceiptGenerationStore(ctx context.Context) (SessionStorageTurnCaptureReceiptGenerationStore, error) {
	store, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	receiptStore, ok := store.(SessionStorageTurnCaptureReceiptGenerationStore)
	if !ok {
		return nil, fmt.Errorf("chat: session storage does not support generation-scoped turn capture receipts")
	}
	return receiptStore, nil
}

func (s *lazySessionStorage) materializedTurnCaptureReceiptGenerationPageStore(ctx context.Context) (SessionStorageMaterializedTurnCaptureReceiptGenerationPageStore, error) {
	store, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	receiptStore, ok := store.(SessionStorageMaterializedTurnCaptureReceiptGenerationPageStore)
	if !ok {
		return nil, fmt.Errorf("chat: session storage does not support generation-scoped materialized turn capture receipt pages")
	}
	return receiptStore, nil
}

func (s *lazySessionStorage) SaveWithTurnCapture(ctx context.Context, session *Session, receipt TurnCaptureReceipt) (TurnCaptureReceipt, error) {
	committer, err := s.turnCaptureCommitter(ctx)
	if err != nil {
		return TurnCaptureReceipt{}, err
	}
	return committer.SaveWithTurnCapture(ctx, session, receipt)
}

func (s *lazySessionStorage) UpdateWithTurnCapture(ctx context.Context, session *Session, receipt TurnCaptureReceipt) (TurnCaptureReceipt, error) {
	committer, err := s.turnCaptureCommitter(ctx)
	if err != nil {
		return TurnCaptureReceipt{}, err
	}
	return committer.UpdateWithTurnCapture(ctx, session, receipt)
}

func (s *lazySessionStorage) ListTurnCaptureReceipts(ctx context.Context, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListTurnCaptureReceipts(ctx, limit)
}

func (s *lazySessionStorage) ListTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListTurnCaptureReceiptsForSession(ctx, sessionID, limit)
}

func (s *lazySessionStorage) ListMaterializedTurnCaptureReceipts(ctx context.Context, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceipts(ctx, limit)
}

func (s *lazySessionStorage) ListMaterializedTurnCaptureReceiptsPage(ctx context.Context, after *TurnCaptureReceipt, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceiptsPage(ctx, after, limit)
}

func (s *lazySessionStorage) ListMaterializedTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceiptsForSession(ctx, sessionID, limit)
}

func (s *lazySessionStorage) ListMaterializedTurnCaptureReceiptsForSessionPage(ctx context.Context, sessionID string, after *TurnCaptureReceipt, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceiptsForSessionPage(ctx, sessionID, after, limit)
}

func (s *lazySessionStorage) HasTurnCaptureReceiptsForSession(ctx context.Context, sessionID string) (bool, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return false, err
	}
	return store.HasTurnCaptureReceiptsForSession(ctx, sessionID)
}

func (s *lazySessionStorage) GetTurnCaptureReceipt(ctx context.Context, id string) (TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return TurnCaptureReceipt{}, err
	}
	return store.GetTurnCaptureReceipt(ctx, id)
}

func (s *lazySessionStorage) ValidateTurnCaptureReceipt(ctx context.Context, receipt TurnCaptureReceipt) error {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return err
	}
	return store.ValidateTurnCaptureReceipt(ctx, receipt)
}

func (s *lazySessionStorage) MarkTurnCaptureReceiptMaterialized(ctx context.Context, id string) error {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return err
	}
	return store.MarkTurnCaptureReceiptMaterialized(ctx, id)
}

func (s *lazySessionStorage) DeferTurnCaptureReceipt(ctx context.Context, id, reason string, retryAt time.Time) error {
	store, err := s.turnCaptureReceiptStore(ctx)
	if err != nil {
		return err
	}
	return store.DeferTurnCaptureReceipt(ctx, id, reason, retryAt)
}

func (s *lazySessionStorage) ListTurnCaptureReceiptsForSessionGeneration(ctx context.Context, sessionID string, generation int64, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.turnCaptureReceiptGenerationStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListTurnCaptureReceiptsForSessionGeneration(ctx, sessionID, generation, limit)
}

func (s *lazySessionStorage) HasTurnCaptureReceiptsForSessionGeneration(ctx context.Context, sessionID string, generation int64) (bool, error) {
	store, err := s.turnCaptureReceiptGenerationStore(ctx)
	if err != nil {
		return false, err
	}
	return store.HasTurnCaptureReceiptsForSessionGeneration(ctx, sessionID, generation)
}

func (s *lazySessionStorage) ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(ctx context.Context, sessionID string, generation int64, after *TurnCaptureReceipt, limit int) ([]TurnCaptureReceipt, error) {
	store, err := s.materializedTurnCaptureReceiptGenerationPageStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(ctx, sessionID, generation, after, limit)
}
