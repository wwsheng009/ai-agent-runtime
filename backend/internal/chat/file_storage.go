package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	return session.Clone(), nil
}

// Delete 删除会话。
func (s *FileStorage) Delete(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.sessionPath(sessionID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
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

		if err := os.Remove(s.sessionPath(session.ID)); err != nil && !os.IsNotExist(err) {
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
			filtered = append(filtered, session.Clone())
		}
	}
	sortSessionsByUpdated(filtered)
	return filtered, nil
}

func (s *FileStorage) readAllSessionsLocked(ctx context.Context) ([]*Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read session dir: %w", err)
	}

	sessions := make([]*Session, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		session, err := s.readSessionFileLocked(filepath.Join(s.dir, entry.Name()))
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
	return s.readSessionFileLocked(s.sessionPath(sessionID))
}

func (s *FileStorage) readSessionFileLocked(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read session file %s: %w", path, err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("decode session file %s: %w", path, err)
	}
	return &session, nil
}

func (s *FileStorage) writeSessionLocked(session *Session) error {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return ErrInvalidSession
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session %s: %w", session.ID, err)
	}

	path := s.sessionPath(session.ID)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp session %s: %w", session.ID, err)
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

func (s *FileStorage) sessionPath(sessionID string) string {
	return filepath.Join(s.dir, sanitizeSessionID(sessionID)+".json")
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
