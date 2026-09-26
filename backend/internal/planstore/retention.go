package planstore

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// PruneOptions describes a retention pass over one record's snapshots.
type PruneOptions struct {
	// ID selects the record.
	ID string
	// Keep is the number of newest rounds to retain. Values <= 0 disable
	// pruning. When the record has rounds, at least the newest round is always
	// kept so ReadLatest keeps working.
	Keep int
}

// PruneResult reports what a retention pass removed.
type PruneResult struct {
	Record        Record
	RemovedRounds int
	RemovedFiles  int
	KeptRounds    int
}

// PruneVersions drops the oldest snapshot rounds of one record. The record's
// monotonic Version counter is preserved (a pruned version number is simply
// unreadable afterwards), and the index is rewritten only after the files were
// removed, so a crash never leaves the index pointing at a missing snapshot.
func (s *Store) PruneVersions(opts PruneOptions) (PruneResult, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return PruneResult{}, fmt.Errorf("%w: empty id", ErrNotFound)
	}
	if opts.Keep <= 0 {
		rec, ok, err := s.Get(id)
		if err != nil {
			return PruneResult{}, err
		}
		if !ok {
			return PruneResult{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return PruneResult{Record: rec, KeptRounds: len(rec.Rounds)}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return PruneResult{}, err
	}
	i, ok := indexFind(idx, id)
	if !ok {
		return PruneResult{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	rec := idx.Records[i]
	if len(rec.Rounds) <= opts.Keep {
		return PruneResult{Record: cloneRecord(rec), KeptRounds: len(rec.Rounds)}, nil
	}

	drop := rec.Rounds[:len(rec.Rounds)-opts.Keep]
	// Newest round always survives; the file for it stays on disk.
	removedFiles := 0
	for _, round := range drop {
		rel := strings.TrimSpace(round.Snapshot)
		if rel == "" {
			continue
		}
		abs, err := s.resolveSnapshotPath(rel)
		if err != nil {
			return PruneResult{}, err
		}
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return PruneResult{}, fmt.Errorf("planstore: remove snapshot %s: %w", rel, err)
		}
		removedFiles++
	}

	rec.Rounds = append([]Round(nil), rec.Rounds[len(drop):]...)
	rec.UpdatedAt = nowStamp()
	idx.Records[i] = rec
	if err := s.saveIndex(idx); err != nil {
		return PruneResult{}, err
	}
	return PruneResult{
		Record:        cloneRecord(rec),
		RemovedRounds: len(drop),
		RemovedFiles:  removedFiles,
		KeptRounds:    len(rec.Rounds),
	}, nil
}

// Delete removes a record together with its snapshot files and index entry.
// A missing record is not an error (idempotent delete).
func (s *Store) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrNotFound)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	i, ok := indexFind(idx, id)
	if !ok {
		return nil
	}
	rec := idx.Records[i]
	for _, round := range rec.Rounds {
		rel := strings.TrimSpace(round.Snapshot)
		if rel == "" {
			continue
		}
		abs, err := s.resolveSnapshotPath(rel)
		if err != nil {
			return err
		}
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("planstore: remove snapshot %s: %w", rel, err)
		}
	}
	// 行级评论随记录一起删除（§4.4）：版本可被 retention 裁剪，但评论不单独裁剪——
	// 它们自带当时的正文摘录，即使锚定的修订已不在库里也仍然可读、可交付。
	if rel := commentsRelPath(rec.ID); rel != "" {
		if abs, err := s.resolveSnapshotPath(rel); err == nil {
			if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("planstore: remove comments %s: %w", rel, err)
			}
		}
	}
	idx.Records = append(idx.Records[:i], idx.Records[i+1:]...)
	return s.saveIndex(idx)
}
