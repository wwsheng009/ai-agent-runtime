package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// FileStorage 基于 JSON 文件的会话存储。
type FileStorage struct {
	dir string
	mu  sync.RWMutex
}

// NewFileStorage 创建文件存储。
func NewFileStorage(dir string) (*FileStorage, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("storage directory cannot be empty")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create session storage dir: %w", err)
	}
	return &FileStorage{dir: dir}, nil
}

// Dir 返回存储目录。
func (s *FileStorage) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Save 保存会话。
func (s *FileStorage) Save(ctx context.Context, session *Session) error {
	if session == nil {
		return ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stored := session.Clone()
	if stored == nil {
		return ErrInvalidSession
	}
	if stored.ID == "" {
		stored.ID = generateSessionID()
	}
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = time.Now()
	}
	stored.UpdatedAt = time.Now()

	return s.writeSessionLocked(stored)
}

// Load 加载会话。
func (s *FileStorage) Load(ctx context.Context, sessionID string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	session, err := s.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// Delete 删除会话。
func (s *FileStorage) Delete(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.resolveSessionPathLocked(sessionID)
	if err != nil {
		return ErrSessionNotFound
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete session %s: %w", sessionID, err)
	}
	return nil
}

// List 列出用户会话。
func (s *FileStorage) List(ctx context.Context, userID string) ([]*Session, error) {
	return s.listFiltered(ctx, func(session *Session) bool {
		return session != nil && session.UserID == userID
	})
}

// ListMetadataPage lists sessions without retaining their full histories.
func (s *FileStorage) ListMetadataPage(ctx context.Context, userID string, limit, offset int) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	paths, err := s.listSessionFilePathsLocked(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]*Session, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, scanErr := s.readSessionMetadataFileLocked(path)
		if scanErr != nil {
			return nil, scanErr
		}
		if session.UserID == userID {
			sessions = append(sessions, session)
		}
	}
	sortSessionsByUpdated(sessions)
	if offset >= len(sessions) {
		return []*Session{}, nil
	}
	if offset > 0 {
		sessions = sessions[offset:]
	}
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

// ListPreviews avoids materializing canonical histories for session pickers.
func (s *FileStorage) ListPreviews(ctx context.Context, userID string, limit, offset int) ([]*SessionPreview, error) {
	sessions, err := s.ListMetadataPage(ctx, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	previews := make([]*SessionPreview, 0, len(sessions))
	for _, session := range sessions {
		if session != nil {
			previews = append(previews, session.BuildPreview())
		}
	}
	return previews, nil
}

// ListWithState 列出指定状态会话。
func (s *FileStorage) ListWithState(ctx context.Context, userID string, state SessionState) ([]*Session, error) {
	return s.listFiltered(ctx, func(session *Session) bool {
		return session != nil && session.UserID == userID && session.State == state
	})
}

// ListByTags 列出包含全部标签的会话。
func (s *FileStorage) ListByTags(ctx context.Context, userID string, tags []string) ([]*Session, error) {
	if len(tags) == 0 {
		return nil, ErrInvalidTags
	}
	return s.listFiltered(ctx, func(session *Session) bool {
		if session == nil || session.UserID != userID {
			return false
		}
		for _, tag := range tags {
			if !session.HasTag(tag) {
				return false
			}
		}
		return true
	})
}

// Update 更新会话。
func (s *FileStorage) Update(ctx context.Context, session *Session) error {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.readSessionLocked(session.ID); err != nil {
		return err
	}

	stored := session.Clone()
	if stored == nil {
		return ErrInvalidSession
	}
	stored.UpdatedAt = time.Now()
	return s.writeSessionLocked(stored)
}

// AddMessage 添加消息。
func (s *FileStorage) AddMessage(ctx context.Context, sessionID string, message interface{}) error {
	msg, ok := message.(types.Message)
	if !ok {
		return ErrInvalidMessageType
	}
	return s.AddMessageWithLimit(ctx, sessionID, msg, 0)
}

// AddMessageWithLimit 原子追加消息并按上限截断。
func (s *FileStorage) AddMessageWithLimit(ctx context.Context, sessionID string, message types.Message, maxHistory int) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.readSessionLocked(sessionID)
	if err != nil {
		return err
	}

	session.AddMessage(message)
	if maxHistory > 0 && len(session.History) > maxHistory {
		session.History = append([]types.Message(nil), session.History[len(session.History)-maxHistory:]...)
		session.Metadata.TotalTurns = len(session.History)
	}

	return s.writeSessionLocked(session)
}

// GetMessages 获取会话消息。
func (s *FileStorage) GetMessages(ctx context.Context, sessionID string) ([]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	session, err := s.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}

	messages := make([]interface{}, len(session.History))
	for i, msg := range session.History {
		messages[i] = msg
	}
	return messages, nil
}

// Close 关闭会话。
func (s *FileStorage) Close(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.readSessionLocked(sessionID)
	if err != nil {
		return err
	}
	session.MarkClosed()
	return s.writeSessionLocked(session)
}

// Cleanup 清理过期会话。
func (s *FileStorage) Cleanup(ctx context.Context, after time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readAllSessionsLocked(ctx)
	if err != nil {
		return 0, err
	}

	now := time.Now()
	removed := 0
	for _, session := range sessions {
		if session == nil {
			continue
		}

		expired := false
		if session.ExpiresAt != nil {
			expired = session.ExpiresAt.Before(now)
		} else {
			expired = session.UpdatedAt.Before(after)
		}
		if !expired {
			continue
		}

		path, resolveErr := s.resolveSessionPathLocked(session.ID)
		if resolveErr != nil {
			if resolveErr == ErrSessionNotFound {
				continue
			}
			return removed, resolveErr
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("cleanup session %s: %w", session.ID, err)
		}
		removed++
	}
	return removed, nil
}

// GetStatistics 获取统计信息。
func (s *FileStorage) GetStatistics(ctx context.Context, userID string) (*SessionStatistics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sessions, err := s.List(ctx, userID)
	if err != nil {
		return nil, err
	}

	stats := &SessionStatistics{Tags: make(map[string]int)}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		switch session.State {
		case StateActive:
			stats.Active++
		case StateIdle:
			stats.Idle++
		case StateClosed:
			stats.Closed++
		case StateArchived:
			stats.Archived++
		}
		stats.Total++
		stats.TotalMessages += len(session.History)
		for _, tag := range session.Metadata.Tags {
			stats.Tags[tag]++
		}
	}
	return stats, nil
}

// ListAll 列出所有会话。
func (s *FileStorage) ListAll(ctx context.Context, limit, offset int) ([]*Session, error) {
	sessions, err := s.listFiltered(ctx, func(session *Session) bool {
		return session != nil
	})
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if offset >= len(sessions) {
			return []*Session{}, nil
		}
		sessions = sessions[offset:]
	}
	if limit > 0 && limit < len(sessions) {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func (s *FileStorage) listFiltered(ctx context.Context, keep func(*Session) bool) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	sessions, err := s.readAllSessionsLocked(ctx)
	if err != nil {
		return nil, err
	}

	filtered := make([]*Session, 0, len(sessions))
	for _, session := range sessions {
		if keep == nil || keep(session) {
			filtered = append(filtered, session)
		}
	}
	sortSessionsByUpdated(filtered)
	return filtered, nil
}

func (s *FileStorage) readAllSessionsLocked(ctx context.Context) ([]*Session, error) {
	paths, err := s.listSessionFilePathsLocked(ctx)
	if err != nil {
		return nil, err
	}

	sessions := make([]*Session, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, err := s.readSessionFileLocked(path)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (s *FileStorage) readSessionLocked(sessionID string) (*Session, error) {
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return nil, ErrInvalidSession
	}
	path, err := s.resolveSessionPathLocked(sessionID)
	if err != nil {
		return nil, err
	}
	return s.readSessionFileLocked(path)
}

func (s *FileStorage) readSessionFileLocked(path string) (*Session, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read session file %s: %w", path, err)
	}
	defer file.Close()

	var session Session
	if err := json.NewDecoder(file).Decode(&session); err != nil {
		return nil, fmt.Errorf("decode session file %s: %w", path, err)
	}
	return &session, nil
}

type fileSessionPreviewText struct {
	firstUser      string
	firstAssistant string
	firstOther     string
	lastContent    string
	messageCount   int
}

func (p *fileSessionPreviewText) observe(message types.Message) {
	p.messageCount++
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return
	}
	p.lastContent = summarizeSessionText(content, sessionSummaryLimit)
	switch strings.ToLower(strings.TrimSpace(message.Role)) {
	case "user":
		if p.firstUser == "" {
			p.firstUser = summarizeSessionText(content, sessionTitleLimit)
		}
	case "assistant":
		if p.firstAssistant == "" {
			p.firstAssistant = summarizeSessionText(content, sessionTitleLimit)
		}
	case "system", "developer", "tool":
	default:
		if p.firstOther == "" {
			p.firstOther = summarizeSessionText(content, sessionTitleLimit)
		}
	}
}

func (p *fileSessionPreviewText) apply(metadata *SessionMetadata) {
	if metadata == nil {
		return
	}
	derivedTitle := p.firstUser
	if derivedTitle == "" {
		derivedTitle = p.firstAssistant
	}
	if derivedTitle == "" {
		derivedTitle = p.firstOther
	}
	title := strings.TrimSpace(metadata.Title)
	// Manual and compact-inherited titles are sticky; do not re-derive from
	// compaction summary / first user message on load.
	if metadata.TitleSource != sessionTitleSourceManual &&
		metadata.TitleSource != sessionTitleSourceCompact &&
		(title == "" || metadata.TitleSource == sessionTitleSourceDerived || shouldRepairLegacyDerivedTitle(title)) {
		metadata.Title = derivedTitle
		if derivedTitle != "" {
			metadata.TitleSource = sessionTitleSourceDerived
		}
	}
	if strings.TrimSpace(metadata.Summary) == "" {
		metadata.Summary = p.lastContent
	}
}

func (s *FileStorage) readSessionMetadataFileLocked(path string) (*Session, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read session file %s: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("decode session metadata file %s: invalid object", path)
	}
	session := &Session{}
	previewText := fileSessionPreviewText{}
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, fmt.Errorf("decode session metadata file %s: %w", path, tokenErr)
		}
		name, _ := nameToken.(string)
		switch name {
		case "id":
			err = decoder.Decode(&session.ID)
		case "userId":
			err = decoder.Decode(&session.UserID)
		case "state":
			err = decoder.Decode(&session.State)
		case "history":
			err = decodeFileSessionHistoryMetadata(decoder, &previewText)
		case "headOffset":
			err = decoder.Decode(&session.HeadOffset)
		case "canonicalMessageCount":
			err = decoder.Decode(&session.CanonicalMessageCount)
		case "metadata":
			err = decoder.Decode(&session.Metadata)
		case "createdAt":
			err = decoder.Decode(&session.CreatedAt)
		case "updatedAt":
			err = decoder.Decode(&session.UpdatedAt)
		case "expiresAt":
			err = decoder.Decode(&session.ExpiresAt)
		default:
			var ignored json.RawMessage
			err = decoder.Decode(&ignored)
		}
		if err != nil {
			return nil, fmt.Errorf("decode session metadata file %s field %s: %w", path, name, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("decode session metadata file %s: %w", path, err)
	}
	if session.CanonicalMessageCount < previewText.messageCount {
		session.CanonicalMessageCount = previewText.messageCount
	}
	session.Metadata.TotalTurns = session.CanonicalMessageCount
	previewText.apply(&session.Metadata)
	session.HistoryLoaded = false
	return session, nil
}

func decodeFileSessionHistoryMetadata(decoder *json.Decoder, preview *fileSessionPreviewText) error {
	if decoder == nil || preview == nil {
		return ErrInvalidSession
	}
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if opening == nil {
		return nil
	}
	if opening != json.Delim('[') {
		return fmt.Errorf("history is not an array")
	}
	for decoder.More() {
		var message types.Message
		if err := decoder.Decode(&message); err != nil {
			return err
		}
		preview.observe(message)
	}
	_, err = decoder.Token()
	return err
}

func (s *FileStorage) writeSessionLocked(session *Session) error {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return ErrInvalidSession
	}
	path, err := s.sessionWritePathLocked(session)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create session dir for %s: %w", session.ID, err)
	}
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("write temp session %s: %w", session.ID, err)
	}
	writer := bufio.NewWriterSize(file, 64*1024)
	encodeErr := encodeSessionJSON(writer, session)
	if encodeErr == nil {
		encodeErr = writer.Flush()
	}
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("encode session %s: %w", session.ID, encodeErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp session %s: %w", session.ID, closeErr)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("replace session %s: %w", session.ID, err)
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("rename session %s: %w", session.ID, retryErr)
		}
	}
	return nil
}

func encodeSessionJSON(writer *bufio.Writer, session *Session) error {
	if writer == nil || session == nil {
		return ErrInvalidSession
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if _, err := writer.WriteString("{\n"); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value interface{}
	}{
		{name: "id", value: session.ID},
		{name: "userId", value: session.UserID},
		{name: "state", value: session.State},
	}
	for _, field := range fields {
		if err := encodeSessionJSONField(writer, encoder, field.name, field.value, true); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString("  \"history\": [\n"); err != nil {
		return err
	}
	for index := range session.History {
		if index > 0 {
			if _, err := writer.WriteString(",\n"); err != nil {
				return err
			}
		}
		if err := encoder.Encode(session.History[index]); err != nil {
			return err
		}
	}
	if _, err := writer.WriteString("  ],\n"); err != nil {
		return err
	}
	remaining := []struct {
		name  string
		value interface{}
	}{
		{name: "headOffset", value: session.HeadOffset},
		{name: "canonicalMessageCount", value: session.CanonicalMessageCount},
		{name: "metadata", value: session.Metadata},
		{name: "createdAt", value: session.CreatedAt},
		{name: "updatedAt", value: session.UpdatedAt},
		{name: "expiresAt", value: session.ExpiresAt},
	}
	for index, field := range remaining {
		if err := encodeSessionJSONField(writer, encoder, field.name, field.value, index < len(remaining)-1); err != nil {
			return err
		}
	}
	_, err := writer.WriteString("}\n")
	return err
}

func encodeSessionJSONField(writer *bufio.Writer, encoder *json.Encoder, name string, value interface{}, comma bool) error {
	if _, err := fmt.Fprintf(writer, "  %q: ", name); err != nil {
		return err
	}
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if comma {
		_, err := writer.WriteString(",")
		return err
	}
	return nil
}

// sessionPath returns the preferred dated path for a session ID.
// Existing files may still live at a legacy flat path; use resolveSessionPathLocked for reads.
func (s *FileStorage) sessionPath(sessionID string) string {
	return s.preferredSessionPath(sessionID, time.Time{})
}

func (s *FileStorage) preferredSessionPath(sessionID string, createdAt time.Time) string {
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return ""
	}
	partitionAt := createdAt
	if partitionAt.IsZero() {
		if parsed, ok := aiclipaths.ParseTimestampedSessionIDTime(sessionID); ok {
			partitionAt = parsed
		} else {
			partitionAt = time.Now()
		}
	}
	return aiclipaths.JoinDatePartition(s.dir, partitionAt, sessionID+".json")
}

func (s *FileStorage) legacySessionPath(sessionID string) string {
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return ""
	}
	return filepath.Join(s.dir, sessionID+".json")
}

func (s *FileStorage) sessionWritePathLocked(session *Session) (string, error) {
	if session == nil {
		return "", ErrInvalidSession
	}
	sessionID := sanitizeSessionID(session.ID)
	if sessionID == "" {
		return "", ErrInvalidSession
	}
	if path, err := s.resolveSessionPathLocked(sessionID); err == nil {
		return path, nil
	} else if err != ErrSessionNotFound {
		return "", err
	}
	return s.preferredSessionPath(sessionID, session.CreatedAt), nil
}

func (s *FileStorage) resolveSessionPathLocked(sessionID string) (string, error) {
	sessionID = sanitizeSessionID(sessionID)
	if sessionID == "" {
		return "", ErrInvalidSession
	}

	candidates := make([]string, 0, 3)
	if preferred := s.preferredSessionPath(sessionID, time.Time{}); preferred != "" {
		candidates = append(candidates, preferred)
	}
	if legacy := s.legacySessionPath(sessionID); legacy != "" {
		candidates = append(candidates, legacy)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat session %s: %w", sessionID, err)
		}
	}

	// Fallback: walk dated tree for older/malformed IDs that do not encode timestamps.
	paths, err := s.listSessionFilePathsLocked(context.Background())
	if err != nil {
		return "", err
	}
	wantName := sessionID + ".json"
	for _, path := range paths {
		if filepath.Base(path) == wantName {
			return path, nil
		}
	}
	return "", ErrSessionNotFound
}

func (s *FileStorage) listSessionFilePathsLocked(ctx context.Context) ([]string, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, fmt.Errorf("storage directory cannot be empty")
	}

	paths := make([]string, 0, 16)
	seen := make(map[string]struct{})
	add := func(path string) {
		path = filepath.Clean(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read session dir: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		full := filepath.Join(s.dir, name)
		if !entry.IsDir() {
			// Legacy flat layout: <dir>/<sessionID>.json
			if filepath.Ext(name) == ".json" {
				add(full)
			}
			continue
		}
		if !isYearDirName(name) {
			continue
		}
		monthEntries, err := os.ReadDir(full)
		if err != nil {
			return nil, fmt.Errorf("read session year dir %s: %w", full, err)
		}
		for _, monthEntry := range monthEntries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !monthEntry.IsDir() || !isMonthOrDayDirName(monthEntry.Name()) {
				continue
			}
			monthDir := filepath.Join(full, monthEntry.Name())
			dayEntries, err := os.ReadDir(monthDir)
			if err != nil {
				return nil, fmt.Errorf("read session month dir %s: %w", monthDir, err)
			}
			for _, dayEntry := range dayEntries {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if !dayEntry.IsDir() || !isMonthOrDayDirName(dayEntry.Name()) {
					continue
				}
				dayDir := filepath.Join(monthDir, dayEntry.Name())
				fileEntries, err := os.ReadDir(dayDir)
				if err != nil {
					return nil, fmt.Errorf("read session day dir %s: %w", dayDir, err)
				}
				for _, fileEntry := range fileEntries {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if fileEntry.IsDir() || filepath.Ext(fileEntry.Name()) != ".json" {
						continue
					}
					add(filepath.Join(dayDir, fileEntry.Name()))
				}
			}
		}
	}
	return paths, nil
}

func isYearDirName(name string) bool {
	if len(name) != 4 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

func isMonthOrDayDirName(name string) bool {
	if len(name) != 2 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

func sanitizeSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return filepath.Base(sessionID)
}

func sortSessionsByUpdated(sessions []*Session) {
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
}
