// Reopening an archived plan: restore one stored snapshot back into the
// workspace so a session (or a later one) can review and continue it.
//
// The archive is the source of truth for "what the plan said at round N"; the
// workspace file is what plan mode edits. Reopen bridges the two without ever
// silently destroying workspace content: a file that differs from the snapshot
// is only overwritten when the caller explicitly forces it.
package planmode

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// ErrReopenConflict reports that the workspace plan file differs from the
// archived snapshot and the caller did not opt into overwriting it. Use
// errors.Is to detect it and offer the force path to the user.
var ErrReopenConflict = errors.New("planmode: plan file differs from the archived snapshot")

// ReopenOptions describes restoring an archived snapshot into the workspace.
type ReopenOptions struct {
	// Store overrides the default plan artifact store (tests / hosts).
	Store *planstore.Store
	// Workspace is the project root used to resolve the record's PlanPath.
	Workspace string
	// RecordID is the archived record id (for example "ai-agent-runtime/plan").
	RecordID string
	// Version selects a snapshot; 0 means the latest recorded version.
	Version int
	// Force overwrites a workspace file whose content differs from the snapshot.
	Force bool
}

// ReopenResult reports what one reopen did.
type ReopenResult struct {
	Record planstore.Record
	// PlanPath is the workspace-resolved absolute path the snapshot targets.
	PlanPath string
	// DisplayPath is the recorded plan path as stored in the archive.
	DisplayPath string
	// Version is the snapshot version that was restored.
	Version int
	// Bytes is the restored snapshot size.
	Bytes int
	// Restored is true when the snapshot content was written to disk.
	Restored bool
	// Unchanged is true when the file already matched the snapshot.
	Unchanged bool
	// Created is true when the plan file did not exist before the reopen.
	Created bool
}

// ReopenPlan restores an archived snapshot into the workspace plan file.
//
// It never fails on missing content that the archive can supply: a missing plan
// file is created (with parents), an identical file is left untouched, and a
// diverged file is refused unless ReopenOptions.Force is set.
func ReopenPlan(opts ReopenOptions) (ReopenResult, error) {
	id := strings.TrimSpace(opts.RecordID)
	if id == "" {
		return ReopenResult{}, errors.New("planmode: reopen requires an archived plan id")
	}
	store := opts.Store
	if store == nil {
		store = DefaultPlanStore()
	}
	record, ok, err := store.Get(id)
	if err != nil {
		return ReopenResult{}, err
	}
	if !ok {
		return ReopenResult{}, fmt.Errorf("%w: %s", planstore.ErrNotFound, id)
	}
	displayPath := strings.TrimSpace(record.PlanPath)
	if displayPath == "" {
		return ReopenResult{}, fmt.Errorf("planmode: archived plan %s has no recorded plan path", record.ID)
	}
	if record.Version <= 0 {
		return ReopenResult{}, fmt.Errorf("%w: archived plan %s has no snapshot yet", planstore.ErrNotFound, record.ID)
	}
	version := opts.Version
	if version <= 0 {
		version = record.Version
	}
	content, err := store.ReadVersion(record.ID, version)
	if err != nil {
		return ReopenResult{}, err
	}
	target := resolveArchivePath(opts.Workspace, displayPath)
	if target == "" {
		return ReopenResult{}, fmt.Errorf("planmode: cannot resolve plan path %q", displayPath)
	}

	result := ReopenResult{
		Record:      record,
		PlanPath:    target,
		DisplayPath: displayPath,
		Version:     version,
		Bytes:       len(content),
	}

	existing, readErr := os.ReadFile(target)
	switch {
	case readErr == nil && bytes.Equal(existing, content):
		result.Unchanged = true
		return result, nil
	case readErr == nil && !opts.Force:
		return result, fmt.Errorf("%w: %s（用 --force 覆盖，或先查看归档正文）", ErrReopenConflict, displayPath)
	case readErr != nil && !errors.Is(readErr, os.ErrNotExist):
		return ReopenResult{}, fmt.Errorf("planmode: read plan file %s: %w", target, readErr)
	}
	if readErr != nil {
		result.Created = true
	}
	if dir := filepath.Dir(target); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ReopenResult{}, fmt.Errorf("planmode: create plan directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return ReopenResult{}, fmt.Errorf("planmode: write plan file %s: %w", target, err)
	}
	result.Restored = true
	return result, nil
}

// MarkReopened records where the active plan body came from so hosts can show
// the lineage (status/panels) instead of presenting restored content as fresh
// work.
func MarkReopened(state State, recordID string, version int) State {
	state.ReopenedFrom = strings.TrimSpace(recordID)
	if version > 0 {
		state.ReopenedVersion = version
	} else {
		state.ReopenedVersion = 0
	}
	return state
}

// ReopenProvenance renders the reopen lineage ("<id> vN"), or "" when the plan
// was not restored from the archive.
func ReopenProvenance(state State) string {
	from := strings.TrimSpace(state.ReopenedFrom)
	if from == "" {
		return ""
	}
	if state.ReopenedVersion > 0 {
		return fmt.Sprintf("%s v%d", from, state.ReopenedVersion)
	}
	return from
}
