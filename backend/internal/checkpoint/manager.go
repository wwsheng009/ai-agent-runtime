package checkpoint

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Manager auto-captures checkpoints around mutating tools.
type Manager struct {
	Store             *artifact.Store
	Events            runtimeevents.Publisher
	MaxFileBytes      int64
	MaxDirectoryFiles int
	MaxDirectoryBytes int64
}

// NewManager creates a checkpoint manager.
func NewManager(store *artifact.Store, events runtimeevents.Publisher) *Manager {
	return &Manager{
		Store:             store,
		Events:            events,
		MaxFileBytes:      1 * 1024 * 1024, // 1MB
		MaxDirectoryFiles: 128,
		MaxDirectoryBytes: 4 * 1024 * 1024, // 4MB
	}
}

// BeforeMutation captures pre-mutation file states.
func (m *Manager) BeforeMutation(ctx context.Context, sessionID, toolName, toolCallID string, args map[string]interface{}) (*PendingCheckpoint, error) {
	if m == nil || m.Store == nil {
		return nil, nil
	}
	paths := extractPaths(toolName, args)
	roots := directoryRootsForFallback(toolName, args)
	if len(paths) == 0 && len(roots) == 0 {
		return nil, nil
	}
	pending := &PendingCheckpoint{
		SessionID:  sessionID,
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Paths:      paths,
		Snapshots:  make(map[string]*FileSnapshot, len(paths)),
		StartedAt:  time.Now().UTC(),
	}
	for _, path := range paths {
		pending.Snapshots[path] = m.snapshotBefore(path)
	}
	for _, root := range roots {
		rootPaths, rootSnapshots, err := m.snapshotDirectoryRoot(root)
		if err != nil {
			recordDirectorySnapshotError(pending, "before", root, err)
			continue
		}
		pending.DirectoryRoots = append(pending.DirectoryRoots, root)
		for _, path := range rootPaths {
			if _, exists := pending.Snapshots[path]; exists {
				continue
			}
			pending.Paths = append(pending.Paths, path)
			pending.Snapshots[path] = rootSnapshots[path]
		}
	}
	if len(pending.Paths) == 0 && len(pending.DirectoryRoots) == 0 && len(pending.DirectorySnapshotErrors) == 0 {
		return nil, nil
	}
	return pending, nil
}

// AfterMutation finalizes the checkpoint after mutation.
func (m *Manager) AfterMutation(ctx context.Context, pending *PendingCheckpoint, toolMeta map[string]interface{}, toolErr string) (string, error) {
	if m == nil || m.Store == nil || pending == nil {
		return "", nil
	}
	if strings.TrimSpace(toolErr) != "" {
		return "", nil
	}
	patch := ""
	if toolMeta != nil {
		if value, ok := toolMeta["patch"].(string); ok {
			patch = value
		}
		if patch == "" {
			if value, ok := toolMeta["diff"].(string); ok {
				patch = value
			}
		}
	}
	if toolMeta != nil {
		extraPaths := extractPathsFromToolMeta(toolMeta)
		if len(extraPaths) > 0 {
			for _, path := range extraPaths {
				if _, exists := pending.Snapshots[path]; exists {
					continue
				}
				pending.Paths = append(pending.Paths, path)
				pending.Snapshots[path] = &FileSnapshot{
					Path:    path,
					Skipped: true,
					Error:   "missing pre-mutation snapshot",
				}
			}
		}
	}
	if len(pending.DirectoryRoots) > 0 {
		for _, root := range pending.DirectoryRoots {
			rootPaths, _, err := m.snapshotDirectoryRoot(root)
			if err != nil {
				recordDirectorySnapshotError(pending, "after", root, err)
				continue
			}
			for _, path := range rootPaths {
				if _, exists := pending.Snapshots[path]; exists {
					continue
				}
				pending.Paths = append(pending.Paths, path)
				pending.Snapshots[path] = &FileSnapshot{
					Path:         path,
					BeforeExists: false,
				}
			}
		}
	}

	files := make([]FileSnapshot, 0, len(pending.Snapshots))
	fileRecords := make([]artifact.CheckpointFile, 0, len(pending.Snapshots))
	for _, path := range pending.Paths {
		snapshot := pending.Snapshots[path]
		if snapshot == nil {
			snapshot = &FileSnapshot{Path: path}
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			snapshot.Error = err.Error()
			snapshot.Skipped = true
			files = append(files, *snapshot)
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				snapshot.AfterExists = false
				snapshot.Op = opFor(snapshot.BeforeExists, snapshot.AfterExists)
				files = append(files, *snapshot)
				continue
			}
			snapshot.Error = err.Error()
			snapshot.Skipped = true
			files = append(files, *snapshot)
			continue
		}
		if info.Size() > m.MaxFileBytes && m.MaxFileBytes > 0 {
			snapshot.AfterExists = true
			snapshot.Skipped = true
			snapshot.Error = fmt.Sprintf("file too large (%d bytes)", info.Size())
			snapshot.Op = opFor(snapshot.BeforeExists, snapshot.AfterExists)
			files = append(files, *snapshot)
			continue
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			snapshot.AfterExists = true
			snapshot.Error = err.Error()
			snapshot.Skipped = true
			files = append(files, *snapshot)
			continue
		}
		snapshot.AfterExists = true
		snapshot.After = string(content)
		snapshot.AfterHash = hashBytes(content)
		snapshot.Op = opFor(snapshot.BeforeExists, snapshot.AfterExists)
		if snapshot.Diff == "" && patch != "" {
			snapshot.Diff = patch
		}
		files = append(files, *snapshot)
		if snapshot.Skipped {
			continue
		}
		record := artifact.CheckpointFile{
			Path:       snapshot.Path,
			Op:         snapshot.Op,
			BeforeHash: snapshot.BeforeHash,
			AfterHash:  snapshot.AfterHash,
			DiffText:   snapshot.Diff,
		}
		if snapshot.BeforeExists {
			if blobID, hash, err := m.Store.SaveBlob(ctx, []byte(snapshot.Before)); err == nil {
				record.BeforeBlobID = blobID
				if record.BeforeHash == "" {
					record.BeforeHash = hash
				}
			}
		}
		if snapshot.AfterExists {
			if blobID, hash, err := m.Store.SaveBlob(ctx, []byte(snapshot.After)); err == nil {
				record.AfterBlobID = blobID
				if record.AfterHash == "" {
					record.AfterHash = hash
				}
			}
		}
		fileRecords = append(fileRecords, record)
	}

	checkpoint := artifact.Checkpoint{
		SessionID:    pending.SessionID,
		Reason:       fmt.Sprintf("tool:%s", strings.ToLower(pending.ToolName)),
		HistoryHash:  "",
		MessageCount: pending.MessageCount,
		Metadata: map[string]interface{}{
			"tool_name":     pending.ToolName,
			"tool_call_id":  pending.ToolCallID,
			"files":         files,
			"message_count": pending.MessageCount,
		},
		CreatedAt: time.Now().UTC(),
	}
	traceID := strings.TrimSpace(stringValue(toolMeta["trace_id"]))
	if traceID != "" {
		checkpoint.Metadata["trace_id"] = traceID
	}
	if sourceRefs := checkpointSourceRefs(toolMeta); len(sourceRefs) > 0 {
		checkpoint.Metadata["source_refs"] = sourceRefs
		if profileRefs := extractProfileSourceRefs(sourceRefs); len(profileRefs) > 0 {
			checkpoint.Metadata["profile_source_refs"] = profileRefs
			checkpoint.Metadata["profile_resource_kinds"] = profileSourceKindCounts(sourceRefs)
		}
	}
	if len(pending.DirectoryRoots) > 0 {
		checkpoint.Metadata["directory_roots"] = append([]string(nil), pending.DirectoryRoots...)
	}
	if len(pending.DirectorySnapshotErrors) > 0 {
		checkpoint.Metadata["directory_snapshot_errors"] = append([]string(nil), pending.DirectorySnapshotErrors...)
	}
	if len(pending.Conversation) > 0 || pending.MessageCount == 0 {
		if blobID, historyHash, err := saveConversationSnapshot(ctx, m.Store, pending.Conversation); err == nil {
			checkpoint.HistoryHash = historyHash
			checkpoint.Metadata["conversation_blob_id"] = blobID
			checkpoint.Metadata["conversation_message_count"] = len(pending.Conversation)
		} else {
			checkpoint.Metadata["conversation_snapshot_error"] = err.Error()
		}
	}
	checkpointID, err := m.Store.SaveCheckpoint(ctx, checkpoint)
	if err != nil {
		return "", err
	}
	if len(fileRecords) > 0 {
		for i := range fileRecords {
			fileRecords[i].CheckpointID = checkpointID
		}
		_ = m.Store.SaveCheckpointFiles(ctx, checkpointID, fileRecords)
	}
	if m.Events != nil {
		provenance := SummarizeCheckpointProvenance(&checkpoint)
		payload := map[string]interface{}{
			"checkpoint_id":                  checkpointID,
			"tool_name":                      pending.ToolName,
			"tool_call_id":                   pending.ToolCallID,
			"file_count":                     len(files),
			"directory_snapshot_error_count": len(pending.DirectorySnapshotErrors),
		}
		if traceID != "" {
			payload["trace_id"] = traceID
		}
		if len(provenance.SourceRefs) > 0 {
			payload["source_refs"] = append([]string(nil), provenance.SourceRefs...)
		}
		if len(provenance.ProfileResourceRefs) > 0 {
			payload["profile_source_refs"] = append([]string(nil), provenance.ProfileResourceRefs...)
		}
		if len(provenance.ProfileResourceKinds) > 0 {
			payload["profile_resource_kinds"] = provenance.ProfileResourceKinds
			payload["provenance"] = provenance
		}
		m.Events.Publish(runtimeevents.Event{
			Type:      "checkpoint_created",
			TraceID:   traceID,
			SessionID: pending.SessionID,
			ToolName:  pending.ToolName,
			Payload:   payload,
		})
	}
	return checkpointID, nil
}

// Restore applies a checkpoint restore plan for code, conversation, or both.
func (m *Manager) Restore(ctx context.Context, req RestoreRequest) (*RestoreResult, error) {
	if m == nil || m.Store == nil {
		return nil, fmt.Errorf("checkpoint store is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.CheckpointID) == "" {
		return nil, fmt.Errorf("checkpoint_id is required")
	}
	mode := normalizeMode(req.Mode)

	checkpoint, err := m.Store.GetCheckpoint(ctx, req.CheckpointID)
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("checkpoint not found: %s", req.CheckpointID)
	}
	if strings.TrimSpace(req.SessionID) != "" && strings.TrimSpace(checkpoint.SessionID) != strings.TrimSpace(req.SessionID) {
		return nil, fmt.Errorf("checkpoint does not belong to session")
	}

	result := &RestoreResult{
		CheckpointID: req.CheckpointID,
		Mode:         string(mode),
	}
	result.Provenance = SummarizeCheckpointProvenance(checkpoint)
	if req.PreviewOnly {
		if summary := checkpointProvenancePreview(result.Provenance); summary != "" {
			result.Preview = append(result.Preview, summary)
		}
	}

	if mode == RestoreCode || mode == RestoreBoth {
		laterCheckpoints, listErr := checkpointsAfterTarget(ctx, m.Store, checkpoint)
		if listErr != nil {
			return nil, listErr
		}
		for _, later := range laterCheckpoints {
			snapshots := loadCheckpointSnapshots(ctx, m.Store, later)
			for _, snapshot := range snapshots {
				if snapshot.Path == "" {
					continue
				}
				if snapshot.Skipped {
					result.Errors = append(result.Errors, fmt.Sprintf("%s skipped: %s", snapshot.Path, snapshot.Error))
					continue
				}
				action := "noop"
				if snapshot.BeforeExists {
					action = "restore"
				} else {
					action = "delete"
				}
				if req.PreviewOnly {
					result.Preview = append(result.Preview, fmt.Sprintf("%s: %s", action, snapshot.Path))
					result.PreviewFiles = append(result.PreviewFiles, PreviewFile{
						Path:     snapshot.Path,
						Change:   previewChange(snapshot, action),
						DiffText: snapshot.Diff,
					})
					continue
				}
				if snapshot.BeforeExists {
					if err := writeFile(snapshot.Path, snapshot.Before); err != nil {
						result.Errors = append(result.Errors, err.Error())
						continue
					}
					result.AppliedPaths = append(result.AppliedPaths, snapshot.Path)
					continue
				}
				if err := os.Remove(snapshot.Path); err != nil && !os.IsNotExist(err) {
					result.Errors = append(result.Errors, err.Error())
					continue
				}
				result.AppliedPaths = append(result.AppliedPaths, snapshot.Path)
			}
		}
	}

	if mode == RestoreConversation || mode == RestoreBoth {
		targetCount, countErr := resolveConversationTargetCount(checkpoint)
		if countErr != nil {
			return nil, countErr
		}
		result.ConversationChanged = true
		result.ConversationHead = targetCount
		conversationMessages, exact, loadErr := m.resolveConversationMessages(ctx, checkpoint, req.PreviewOnly)
		if loadErr != nil {
			result.Errors = append(result.Errors, loadErr.Error())
		}
		result.ConversationExact = exact
		if req.PreviewOnly {
			preview := fmt.Sprintf("conversation: restore %d message(s)", targetCount)
			if !result.ConversationExact {
				preview = fmt.Sprintf("conversation: rewind visible history to %d message(s)", targetCount)
			}
			result.Preview = append(result.Preview, preview)
		} else if exact {
			result.ConversationMessages = conversationMessages
		}
	}
	return result, nil
}

// SummarizeCheckpointProvenance returns checkpoint-linked provenance suitable for API responses.
func SummarizeCheckpointProvenance(checkpoint *artifact.Checkpoint) ProvenanceSummary {
	if checkpoint == nil {
		return ProvenanceSummary{}
	}
	sourceRefs := make([]string, 0)
	for _, entry := range checkpoint.Ledger {
		sourceRefs = append(sourceRefs, entry.SourceRefs...)
	}
	if len(checkpoint.Metadata) > 0 {
		sourceRefs = append(sourceRefs, stringSliceValue(checkpoint.Metadata["source_refs"])...)
	}
	sourceRefs = dedupeAndSortStrings(sourceRefs)
	if len(sourceRefs) == 0 {
		return ProvenanceSummary{}
	}
	memoryCount, notesCount, labels := profileResourceDisplay(sourceRefs)
	return ProvenanceSummary{
		SourceRefs:            sourceRefs,
		ProfileResourceRefs:   extractProfileSourceRefs(sourceRefs),
		ProfileResourceKinds:  profileSourceKindCounts(sourceRefs),
		ProfileResourceCount:  len(extractProfileSourceRefs(sourceRefs)),
		ProfileMemoryCount:    memoryCount,
		ProfileNotesCount:     notesCount,
		ProfileResourceLabels: labels,
	}
}

func checkpointSourceRefs(toolMeta map[string]interface{}) []string {
	if len(toolMeta) == 0 {
		return nil
	}
	sourceRefs := stringSliceValue(toolMeta["source_refs"])
	sourceRefs = append(sourceRefs, stringSliceValue(toolMeta["profile_source_refs"])...)
	if len(sourceRefs) == 0 {
		return nil
	}
	return dedupeAndSortStrings(sourceRefs)
}

func checkpointProvenancePreview(provenance ProvenanceSummary) string {
	if len(provenance.ProfileResourceRefs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(provenance.ProfileResourceKinds))
	if count := provenance.ProfileResourceKinds["memory"]; count > 0 {
		parts = append(parts, fmt.Sprintf("memory=%d", count))
	}
	if count := provenance.ProfileResourceKinds["notes"]; count > 0 {
		parts = append(parts, fmt.Sprintf("notes=%d", count))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("provenance: %d profile resource link(s)", len(provenance.ProfileResourceRefs))
	}
	summary := "provenance: profile resources " + strings.Join(parts, ", ")
	if len(provenance.ProfileResourceLabels) > 0 {
		summary += " [" + strings.Join(limitStrings(provenance.ProfileResourceLabels, 4), ", ") + "]"
	}
	return summary
}

func stringSliceValue(raw interface{}) []string {
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func dedupeAndSortStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func extractProfileSourceRefs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, "profile-resource:memory:") || strings.HasPrefix(value, "profile-resource:notes:") {
			filtered = append(filtered, value)
		}
	}
	return dedupeAndSortStrings(filtered)
}

func profileSourceKindCounts(values []string) map[string]int {
	if len(values) == 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, value := range values {
		switch {
		case strings.HasPrefix(value, "profile-resource:memory:"):
			counts["memory"]++
		case strings.HasPrefix(value, "profile-resource:notes:"):
			counts["notes"]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func profileResourceDisplay(values []string) (int, int, []string) {
	profileRefs := extractProfileSourceRefs(values)
	if len(profileRefs) == 0 {
		return 0, 0, nil
	}
	labels := make([]string, 0, len(profileRefs))
	memoryCount := 0
	notesCount := 0
	for _, value := range profileRefs {
		switch {
		case strings.HasPrefix(value, "profile-resource:memory:"):
			memoryCount++
			labels = append(labels, "memory:"+shortProfileResourceName(strings.TrimPrefix(value, "profile-resource:memory:")))
		case strings.HasPrefix(value, "profile-resource:notes:"):
			notesCount++
			labels = append(labels, "notes:"+shortProfileResourceName(strings.TrimPrefix(value, "profile-resource:notes:")))
		}
	}
	return memoryCount, notesCount, dedupeAndSortStrings(labels)
}

func shortProfileResourceName(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(value, "/"); index >= 0 && index+1 < len(value) {
		return value[index+1:]
	}
	return value
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func checkpointsAfterTarget(ctx context.Context, store *artifact.Store, target *artifact.Checkpoint) ([]artifact.Checkpoint, error) {
	if store == nil {
		return nil, fmt.Errorf("checkpoint store is not configured")
	}
	if target == nil {
		return nil, fmt.Errorf("target checkpoint is nil")
	}
	checkpoints, err := store.ListCheckpoints(ctx, target.SessionID, 0, 0)
	if err != nil {
		return nil, err
	}
	if len(checkpoints) == 0 {
		return nil, nil
	}
	targetID := strings.TrimSpace(target.ID)
	for index, checkpoint := range checkpoints {
		if strings.TrimSpace(checkpoint.ID) != targetID {
			continue
		}
		return checkpoints[:index], nil
	}
	return nil, fmt.Errorf("checkpoint not found in session timeline: %s", target.ID)
}

func loadCheckpointSnapshots(ctx context.Context, store *artifact.Store, checkpoint artifact.Checkpoint) []FileSnapshot {
	snapshots := decodeSnapshots(checkpoint.Metadata)
	if len(snapshots) > 0 {
		return snapshots
	}
	files, err := store.GetCheckpointFiles(ctx, checkpoint.ID)
	if err != nil || len(files) == 0 {
		return nil
	}
	return snapshotsFromFiles(ctx, store, files)
}

func normalizeMode(mode RestoreMode) RestoreMode {
	switch RestoreMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case RestoreConversation:
		return RestoreConversation
	case RestoreBoth:
		return RestoreBoth
	default:
		return RestoreCode
	}
}

func decodeSnapshots(metadata map[string]interface{}) []FileSnapshot {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata["files"]
	if !ok {
		return nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		if typed, ok := raw.([]FileSnapshot); ok {
			return append([]FileSnapshot(nil), typed...)
		}
		return nil
	}
	out := make([]FileSnapshot, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		snapshot := FileSnapshot{
			Path:         stringValue(entry["path"]),
			Op:           stringValue(entry["op"]),
			Before:       stringValue(entry["before"]),
			After:        stringValue(entry["after"]),
			BeforeExists: boolValue(entry["before_exists"]),
			AfterExists:  boolValue(entry["after_exists"]),
			BeforeHash:   stringValue(entry["before_hash"]),
			AfterHash:    stringValue(entry["after_hash"]),
			Diff:         stringValue(entry["diff"]),
			Skipped:      boolValue(entry["skipped"]),
			Error:        stringValue(entry["error"]),
		}
		out = append(out, snapshot)
	}
	return out
}

func snapshotsFromFiles(ctx context.Context, store *artifact.Store, files []artifact.CheckpointFile) []FileSnapshot {
	if len(files) == 0 {
		return nil
	}
	out := make([]FileSnapshot, 0, len(files))
	for _, file := range files {
		snapshot := FileSnapshot{
			Path:       file.Path,
			Op:         file.Op,
			BeforeHash: file.BeforeHash,
			AfterHash:  file.AfterHash,
			Diff:       file.DiffText,
		}
		beforeExists := strings.ToLower(strings.TrimSpace(file.Op)) != "create"
		afterExists := strings.ToLower(strings.TrimSpace(file.Op)) != "delete"
		if file.BeforeBlobID != "" {
			if data, err := store.LoadBlob(ctx, file.BeforeBlobID); err == nil && data != nil {
				snapshot.Before = string(data)
				snapshot.BeforeExists = true
			} else {
				snapshot.Error = "failed to load before blob"
				snapshot.Skipped = true
			}
		} else {
			snapshot.BeforeExists = beforeExists
		}
		if file.AfterBlobID != "" {
			if data, err := store.LoadBlob(ctx, file.AfterBlobID); err == nil && data != nil {
				snapshot.After = string(data)
				snapshot.AfterExists = true
			} else {
				snapshot.Error = "failed to load after blob"
				snapshot.Skipped = true
			}
		} else {
			snapshot.AfterExists = afterExists
		}
		out = append(out, snapshot)
	}
	return out
}

func previewChange(snapshot FileSnapshot, fallback string) string {
	if strings.TrimSpace(snapshot.Op) != "" {
		return strings.TrimSpace(snapshot.Op)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if snapshot.BeforeExists {
		return "restore"
	}
	return "delete"
}

func stringValue(raw interface{}) string {
	if text, ok := raw.(string); ok {
		return text
	}
	return ""
}

func boolValue(raw interface{}) bool {
	value, _ := raw.(bool)
	return value
}

func intValue(raw interface{}) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func resolveConversationTargetCount(checkpoint *artifact.Checkpoint) (int, error) {
	if checkpoint == nil {
		return 0, fmt.Errorf("checkpoint is nil")
	}
	if len(checkpoint.Metadata) > 0 {
		if raw, ok := checkpoint.Metadata["conversation_message_count"]; ok {
			if count, ok := intValue(raw); ok && count >= 0 {
				return count, nil
			}
		}
	}
	if checkpoint.MessageCount > 0 {
		return checkpoint.MessageCount, nil
	}
	if len(checkpoint.Metadata) > 0 {
		if raw, ok := checkpoint.Metadata["message_count"]; ok {
			if count, ok := intValue(raw); ok && count >= 0 {
				return count, nil
			}
		}
	}
	return 0, fmt.Errorf("checkpoint does not contain message count")
}

func saveConversationSnapshot(ctx context.Context, store *artifact.Store, messages []runtimetypes.Message) (string, string, error) {
	if store == nil {
		return "", "", fmt.Errorf("checkpoint store is not configured")
	}
	payload, err := json.Marshal(cloneMessages(messages))
	if err != nil {
		return "", "", fmt.Errorf("marshal conversation snapshot: %w", err)
	}
	blobID, historyHash, err := store.SaveBlob(ctx, payload)
	if err != nil {
		return "", "", fmt.Errorf("save conversation snapshot blob: %w", err)
	}
	return blobID, historyHash, nil
}

func loadConversationSnapshot(ctx context.Context, store *artifact.Store, checkpoint *artifact.Checkpoint) ([]runtimetypes.Message, bool, error) {
	if store == nil || checkpoint == nil || !hasConversationSnapshot(checkpoint) {
		return nil, false, nil
	}
	rawBlobID := checkpoint.Metadata["conversation_blob_id"]
	blobID, _ := rawBlobID.(string)
	blobID = strings.TrimSpace(blobID)
	if blobID == "" {
		return nil, false, nil
	}
	data, err := store.LoadBlob(ctx, blobID)
	if err != nil {
		return nil, false, fmt.Errorf("load conversation snapshot blob: %w", err)
	}
	var messages []runtimetypes.Message
	if len(data) == 0 {
		return []runtimetypes.Message{}, true, nil
	}
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, false, fmt.Errorf("decode conversation snapshot: %w", err)
	}
	return cloneMessages(messages), true, nil
}

func (m *Manager) resolveConversationMessages(ctx context.Context, checkpoint *artifact.Checkpoint, previewOnly bool) ([]runtimetypes.Message, bool, error) {
	messages, exact, err := loadConversationSnapshot(ctx, m.Store, checkpoint)
	if err != nil || exact {
		return messages, exact, err
	}
	return m.backfillConversationSnapshot(ctx, checkpoint, previewOnly)
}

func (m *Manager) backfillConversationSnapshot(ctx context.Context, target *artifact.Checkpoint, previewOnly bool) ([]runtimetypes.Message, bool, error) {
	if m == nil || m.Store == nil || target == nil {
		return nil, false, nil
	}
	targetCount, err := resolveConversationTargetCount(target)
	if err != nil {
		return nil, false, err
	}
	laterCheckpoints, err := checkpointsAfterTarget(ctx, m.Store, target)
	if err != nil {
		return nil, false, err
	}
	for index := len(laterCheckpoints) - 1; index >= 0; index-- {
		candidate := laterCheckpoints[index]
		if !hasConversationSnapshot(&candidate) {
			continue
		}
		messages, exact, loadErr := loadConversationSnapshot(ctx, m.Store, &candidate)
		if loadErr != nil || !exact {
			continue
		}
		if len(messages) < targetCount {
			continue
		}
		prefix := cloneMessages(messages[:targetCount])
		if !canBackfillConversationSnapshot(target, len(messages), prefix) {
			continue
		}
		if !previewOnly {
			if err := m.persistConversationBackfill(ctx, target, prefix); err != nil {
				return prefix, true, err
			}
		}
		return prefix, true, nil
	}
	return nil, false, nil
}

func canBackfillConversationSnapshot(target *artifact.Checkpoint, candidateCount int, messages []runtimetypes.Message) bool {
	if target == nil {
		return false
	}
	targetCount := len(messages)
	if targetCount == 0 {
		return true
	}
	historyHash := strings.TrimSpace(target.HistoryHash)
	if historyHash != "" {
		return matchesConversationHistoryHash(historyHash, messages)
	}
	return candidateCount == targetCount
}

func (m *Manager) persistConversationBackfill(ctx context.Context, checkpoint *artifact.Checkpoint, messages []runtimetypes.Message) error {
	if m == nil || m.Store == nil || checkpoint == nil || hasConversationSnapshot(checkpoint) {
		return nil
	}
	blobID, snapshotHash, err := saveConversationSnapshot(ctx, m.Store, messages)
	if err != nil {
		return err
	}
	if checkpoint.Metadata == nil {
		checkpoint.Metadata = map[string]interface{}{}
	}
	checkpoint.Metadata["conversation_blob_id"] = blobID
	checkpoint.Metadata["conversation_message_count"] = len(messages)
	if strings.TrimSpace(checkpoint.HistoryHash) == "" {
		checkpoint.HistoryHash = snapshotHash
	}
	if checkpoint.MessageCount == 0 {
		checkpoint.MessageCount = len(messages)
	}
	return m.Store.UpdateCheckpoint(ctx, *checkpoint)
}

func matchesConversationHistoryHash(historyHash string, messages []runtimetypes.Message) bool {
	historyHash = strings.ToLower(strings.TrimSpace(historyHash))
	if historyHash == "" {
		return false
	}
	if snapshotHash, err := conversationSnapshotHash(messages); err == nil && snapshotHash == historyHash {
		return true
	}
	return legacyConversationHash(messages) == historyHash
}

func conversationSnapshotHash(messages []runtimetypes.Message) (string, error) {
	payload, err := json.Marshal(cloneMessages(messages))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func legacyConversationHash(messages []runtimetypes.Message) string {
	parts := make([]string, 0, len(messages)*2)
	for _, message := range messages {
		parts = append(parts, message.Role)
		parts = append(parts, strings.TrimSpace(message.Content))
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func hasConversationSnapshot(checkpoint *artifact.Checkpoint) bool {
	if checkpoint == nil || len(checkpoint.Metadata) == 0 {
		return false
	}
	rawBlobID, ok := checkpoint.Metadata["conversation_blob_id"]
	if !ok {
		return false
	}
	blobID, _ := rawBlobID.(string)
	return strings.TrimSpace(blobID) != ""
}

func cloneMessages(messages []runtimetypes.Message) []runtimetypes.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]runtimetypes.Message, len(messages))
	for i := range messages {
		cloned[i] = *messages[i].Clone()
	}
	return cloned
}

func opFor(beforeExists, afterExists bool) string {
	switch {
	case beforeExists && afterExists:
		return "update"
	case beforeExists && !afterExists:
		return "delete"
	case !beforeExists && afterExists:
		return "create"
	default:
		return "noop"
	}
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeFile(path string, content string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

func (m *Manager) snapshotBefore(path string) *FileSnapshot {
	snapshot := &FileSnapshot{Path: path}
	abs, err := filepath.Abs(path)
	if err != nil {
		snapshot.Error = err.Error()
		snapshot.Skipped = true
		return snapshot
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			snapshot.BeforeExists = false
			return snapshot
		}
		snapshot.Error = err.Error()
		snapshot.Skipped = true
		return snapshot
	}
	if info.IsDir() {
		snapshot.BeforeExists = false
		snapshot.Skipped = true
		snapshot.Error = "path is a directory"
		return snapshot
	}
	if info.Size() > m.MaxFileBytes && m.MaxFileBytes > 0 {
		snapshot.BeforeExists = true
		snapshot.Skipped = true
		snapshot.Error = fmt.Sprintf("file too large (%d bytes)", info.Size())
		return snapshot
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		snapshot.BeforeExists = true
		snapshot.Error = err.Error()
		snapshot.Skipped = true
		return snapshot
	}
	snapshot.BeforeExists = true
	snapshot.Before = string(content)
	snapshot.BeforeHash = hashBytes(content)
	return snapshot
}

func (m *Manager) snapshotDirectoryRoot(root string) ([]string, map[string]*FileSnapshot, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil, errors.New("root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, errors.New("root is not a directory")
	}

	paths := make([]string, 0)
	snapshots := make(map[string]*FileSnapshot)
	totalBytes := int64(0)
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if shouldSkipDirectorySnapshot(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if m.MaxDirectoryFiles > 0 && len(paths) >= m.MaxDirectoryFiles {
			return errors.New("directory snapshot file limit exceeded")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() <= m.MaxFileBytes || m.MaxFileBytes <= 0 {
			totalBytes += info.Size()
			if m.MaxDirectoryBytes > 0 && totalBytes > m.MaxDirectoryBytes {
				return errors.New("directory snapshot byte limit exceeded")
			}
		}
		normalized := filepath.Clean(path)
		paths = append(paths, normalized)
		snapshots[normalized] = m.snapshotBefore(normalized)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	return paths, snapshots, nil
}

func shouldSkipDirectorySnapshot(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", "node_modules", ".next", ".turbo", ".cache":
		return true
	default:
		return false
	}
}

func recordDirectorySnapshotError(pending *PendingCheckpoint, phase, root string, err error) {
	if pending == nil || err == nil {
		return
	}
	message := fmt.Sprintf("%s snapshot for %s: %s", strings.TrimSpace(phase), strings.TrimSpace(root), err.Error())
	for _, existing := range pending.DirectorySnapshotErrors {
		if existing == message {
			return
		}
	}
	pending.DirectorySnapshotErrors = append(pending.DirectorySnapshotErrors, message)
}

func extractPaths(toolName string, args map[string]interface{}) []string {
	if len(args) == 0 {
		return nil
	}
	return extractPathsFromArgs(args)
}

func extractPathsFromArgs(args map[string]interface{}) []string {
	if len(args) == 0 {
		return nil
	}
	paths := make([]string, 0, 2)
	singleKeys := []string{"path", "file", "file_path", "source", "destination", "target"}
	for _, key := range singleKeys {
		if value, ok := args[key]; ok {
			paths = append(paths, extractPathsFromValue(value)...)
		}
	}
	listKeys := []string{"paths", "files", "file_paths", "targets", "sources", "destinations", "mutated_paths", "mutated_files", "changed_paths", "changed_files"}
	for _, key := range listKeys {
		if value, ok := args[key]; ok {
			paths = append(paths, extractPathsFromValue(value)...)
		}
	}
	if value, ok := args["files"]; ok {
		switch items := value.(type) {
		case []interface{}:
			for _, item := range items {
				if entry, ok := item.(map[string]interface{}); ok {
					paths = append(paths, extractPathsFromValue(entry["path"])...)
					paths = append(paths, extractPathsFromValue(entry["file_path"])...)
				}
			}
		case []map[string]interface{}:
			for _, entry := range items {
				paths = append(paths, extractPathsFromValue(entry["path"])...)
				paths = append(paths, extractPathsFromValue(entry["file_path"])...)
			}
		}
	}
	if raw, ok := args["patch"]; ok {
		if patch, ok := raw.(string); ok {
			paths = append(paths, extractPathsFromPatch(patch)...)
		}
	}
	if raw, ok := args["diff"]; ok {
		if patch, ok := raw.(string); ok {
			paths = append(paths, extractPathsFromPatch(patch)...)
		}
	}
	return dedupeStrings(paths)
}

func directoryRootsForFallback(toolName string, args map[string]interface{}) []string {
	if !isShellLikeTool(toolName) || len(args) == 0 {
		return nil
	}
	roots := make([]string, 0, 2)
	for _, key := range []string{"workspace_path", "cwd", "workdir", "working_dir"} {
		roots = append(roots, extractPathsFromValue(args[key])...)
	}
	return dedupeStrings(roots)
}

func extractPathsFromToolMeta(toolMeta map[string]interface{}) []string {
	if len(toolMeta) == 0 {
		return nil
	}
	paths := extractPathsFromArgs(toolMeta)
	if raw, ok := toolMeta["tool_metadata"].(map[string]interface{}); ok {
		paths = append(paths, extractPathsFromArgs(raw)...)
	}
	if raw, ok := toolMeta["metadata"].(map[string]interface{}); ok {
		paths = append(paths, extractPathsFromArgs(raw)...)
	}
	return dedupeStrings(paths)
}

func isShellLikeTool(toolName string) bool {
	lower := strings.ToLower(strings.TrimSpace(toolName))
	if lower == "" {
		return false
	}
	for _, marker := range []string{"shell", "bash", "exec"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func extractPathsFromValue(value interface{}) []string {
	out := make([]string, 0)
	switch item := value.(type) {
	case string:
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	case []string:
		for _, text := range item {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []interface{}:
		for _, raw := range item {
			out = append(out, extractPathsFromValue(raw)...)
		}
	}
	return out
}

func extractPathsFromPatch(patch string) []string {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return nil
	}
	lines := strings.Split(patch, "\n")
	paths := make([]string, 0, 2)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				path := strings.TrimPrefix(parts[3], "b/")
				path = strings.TrimPrefix(path, "a/")
				if path != "" && path != "/dev/null" {
					paths = append(paths, path)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			path = strings.TrimPrefix(path, "a/")
			if path != "" && path != "/dev/null" {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
