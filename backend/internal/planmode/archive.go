// Archive helpers: persist plan-mode artifacts (index + review-round snapshots)
// outside the workspace so a plan survives cancel/quit and can be browsed later.
//
// Archiving is deliberately best-effort: callers must never let a store failure
// block or roll back a plan-mode transition. The helpers return the error so the
// host can surface it, but the state machine stays authoritative.
package planmode

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// PlansDirEnv overrides the plan artifact store root (tests / portable hosts).
const PlansDirEnv = "AICLI_PLANS_DIR"

var (
	planStoreMu    sync.Mutex
	planStoreCache = map[string]*planstore.Store{}
)

// DefaultPlanStore returns the process-wide plan artifact store. The root comes
// from AICLI_PLANS_DIR when set, otherwise $HOME/.aicli/plans. Stores are cached
// per resolved root so a host (or test) that changes the env var gets its own
// instance instead of the previously cached one.
func DefaultPlanStore() *planstore.Store {
	root := strings.TrimSpace(os.Getenv(PlansDirEnv))
	if root == "" {
		root = planstore.DefaultRoot()
	}
	planStoreMu.Lock()
	defer planStoreMu.Unlock()
	if store, ok := planStoreCache[root]; ok {
		return store
	}
	store := planstore.NewStore(root)
	planStoreCache[root] = store
	return store
}

// ArchiveOptions describes one archive operation.
type ArchiveOptions struct {
	// Store overrides the default plan artifact store (tests / hosts).
	Store *planstore.Store
	// SessionID is recorded as the record's most recent owner.
	SessionID string
	// Workspace is the project root used to resolve a relative PlanPath and to
	// derive the record id.
	Workspace string
	// PlanPath is the plan artifact path recorded in plan-mode state.
	PlanPath string
	// Decision is enter|approve|request_changes|quit (free-form is preserved).
	Decision string
	// Source is user|model.
	Source string
	// Notes are the review notes recorded with the decision.
	Notes string
	// Content overrides the plan body; when nil the file on disk is read.
	Content []byte
	// Title is an optional human-readable record title.
	Title string
	// Status overrides the derived status; empty derives from Decision.
	Status planstore.Status
}

// ArchivePlan upserts the record for a plan artifact and, when the plan body is
// available, appends a review-round snapshot.
func ArchivePlan(ctx context.Context, opts ArchiveOptions) (planstore.Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	store := opts.Store
	if store == nil {
		store = DefaultPlanStore()
	}
	if store == nil {
		return planstore.Record{}, planstore.ErrNotFound
	}
	planPath := strings.TrimSpace(opts.PlanPath)
	if planPath == "" {
		planPath = DefaultPlanPath
	}
	record, err := store.Record(planstore.RecordOptions{
		ID:          planstore.IDFor(opts.Workspace, planPath),
		SessionID:   strings.TrimSpace(opts.SessionID),
		ProjectPath: strings.TrimSpace(opts.Workspace),
		PlanPath:    planPath,
		Title:       strings.TrimSpace(opts.Title),
	})
	if err != nil {
		return planstore.Record{}, err
	}

	// enter/start only register the artifact: the plan file may not exist yet,
	// and a stale workspace file must not be recorded as a review round.
	content := opts.Content
	if content == nil && archiveSnapshotsContent(opts.Decision) {
		content = readPlanArtifact(opts.Workspace, planPath)
	}
	if len(content) == 0 {
		// The plan file may not exist yet (enter before the model writes it):
		// keep the record and apply the derived status without a snapshot.
		if status := archiveStatus(opts); status != "" {
			return store.SetStatus(record.ID, status)
		}
		return record, nil
	}
	snapshot, err := store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: strings.TrimSpace(opts.Decision),
		Notes:    opts.Notes,
		Source:   strings.TrimSpace(opts.Source),
		Content:  content,
		Status:   archiveStatus(opts),
	})
	if err != nil {
		return planstore.Record{}, err
	}
	return pruneArchivedPlan(store, snapshot), nil
}

// archiveRetentionEnv bounds the number of snapshot rounds kept per plan.
// Unset/0 keeps every round (the historical behavior); a positive value prunes
// the oldest rounds after each archive.
const archiveRetentionEnv = "AICLI_PLANS_MAX_VERSIONS"

// PlanRetentionLimit reads the configured retention limit (0 = unlimited).
func PlanRetentionLimit() int {
	raw := strings.TrimSpace(os.Getenv(archiveRetentionEnv))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// pruneArchivedPlan applies the retention limit best-effort: a prune failure
// never fails the archive (the snapshot itself is already durable).
func pruneArchivedPlan(store *planstore.Store, record planstore.Record) planstore.Record {
	limit := PlanRetentionLimit()
	if store == nil || limit <= 0 {
		return record
	}
	result, err := store.PruneVersions(planstore.PruneOptions{ID: record.ID, Keep: limit})
	if err != nil {
		return record
	}
	return result.Record
}

// archiveSnapshotsContent reports whether a decision represents a review round
// (and therefore should snapshot the plan body).
func archiveSnapshotsContent(decision string) bool {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "", "enter", "start", "on":
		return false
	default:
		return true
	}
}

// archiveStatus derives the artifact status from the decision unless the caller
// pinned one. enter/request_changes keep the current (pending) status.
func archiveStatus(opts ArchiveOptions) planstore.Status {
	if opts.Status != "" {
		return opts.Status
	}
	switch strings.ToLower(strings.TrimSpace(opts.Decision)) {
	case string(ExitApprove), "approved":
		return planstore.StatusApproved
	case string(ExitQuit), "cancel", "canceled", "cancelled":
		return planstore.StatusNotImplemented
	default:
		return ""
	}
}

// readPlanArtifact reads the plan body from the workspace. A missing or
// unreadable file is reported as nil: archiving must not fail the transition.
func readPlanArtifact(workspace, planPath string) []byte {
	path := resolveArchivePath(workspace, planPath)
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

// ReadPlanArtifact exposes the workspace plan body for hosts that need to
// decide whether a plan revision is ready for review (e.g. the run-end
// backstop). Missing or unreadable files yield nil content.
func ReadPlanArtifact(workspace, planPath string) []byte {
	return readPlanArtifact(workspace, planPath)
}

// resolveArchivePath resolves a plan path against the workspace root. Relative
// paths that escape the workspace are rejected to keep archiving from reading
// arbitrary files through a hand-edited session state.
func resolveArchivePath(workspace, planPath string) string {
	planPath = strings.TrimSpace(planPath)
	if planPath == "" {
		return ""
	}
	if filepath.IsAbs(planPath) {
		return filepath.Clean(planPath)
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return filepath.Clean(planPath)
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return ""
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.Clean(planPath)))
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return target
}
