package chat

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// 会话相关错误
var (
	ErrInvalidSession     = errors.New("invalid session")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionExpired     = errors.New("session expired")
	ErrInvalidMessageType = errors.New("invalid message type")
	ErrInvalidTags        = errors.New("invalid tags")
)

// SessionStorage 会话存储接口
type SessionStorage interface {
	// Save 保存会话
	Save(ctx context.Context, session *Session) error

	// Load 加载会话
	Load(ctx context.Context, sessionID string) (*Session, error)

	// Delete 删除会话
	Delete(ctx context.Context, sessionID string) error

	// List 列出用户的所有会话
	List(ctx context.Context, userID string) ([]*Session, error)

	// ListWithState 列出用户指定状态的会话
	ListWithState(ctx context.Context, userID string, state SessionState) ([]*Session, error)

	// ListByTags 列出包含指定标签的会话
	ListByTags(ctx context.Context, userID string, tags []string) ([]*Session, error)

	// Update 更新会话
	Update(ctx context.Context, session *Session) error

	// AddMessage 添加消息到会话
	AddMessage(ctx context.Context, sessionID string, message interface{}) error

	// GetMessages 获取会话消息
	GetMessages(ctx context.Context, sessionID string) ([]interface{}, error)

	// Close 关闭会话
	Close(ctx context.Context, sessionID string) error

	// Cleanup 清理过期会话
	Cleanup(ctx context.Context, after time.Time) (int, error)

	// GetStatistics 获取存储统计信息
	GetStatistics(ctx context.Context, userID string) (*SessionStatistics, error)
}

// SessionStorageAllLister 可选接口：支持列出所有会话
type SessionStorageAllLister interface {
	ListAll(ctx context.Context, limit, offset int) ([]*Session, error)
}

// SessionStorageHistoryAppender 可选接口：原子追加消息并按上限截断
type SessionStorageHistoryAppender interface {
	AddMessageWithLimit(ctx context.Context, sessionID string, message types.Message, maxHistory int) error
}

// SessionStatistics 会话统计信息
type SessionStatistics struct {
	Total         int            `json:"total" yaml:"total"`
	Active        int            `json:"active" yaml:"active"`
	Idle          int            `json:"idle" yaml:"idle"`
	Closed        int            `json:"closed" yaml:"closed"`
	Archived      int            `json:"archived" yaml:"archived"`
	TotalMessages int            `json:"totalMessages" yaml:"totalMessages"`
	Tags          map[string]int `json:"tags" yaml:"tags"`
}

// InMemoryStorage 内存会话存储（用于开发和测试）
type InMemoryStorage struct {
	sessions    map[string]*Session
	mu          sync.RWMutex
	sessionTags map[string]map[string]bool // tag -> sessionIDs
}

// NewInMemoryStorage 创建内存存储
func NewInMemoryStorage() *InMemoryStorage {
	return &InMemoryStorage{
		sessions:    make(map[string]*Session),
		sessionTags: make(map[string]map[string]bool),
	}
}

// Save 保存会话
func (s *InMemoryStorage) Save(ctx context.Context, session *Session) error {
	if session == nil {
		return ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if session.ID == "" {
		session.ID = generateSessionID()
	}

	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}

	session.UpdatedAt = time.Now()

	stored := session.Clone()
	if stored == nil {
		return ErrInvalidSession
	}
	s.sessions[stored.ID] = stored

	// 更新标签索引
	s.updateTagIndex(stored)

	return nil
}

// Load 加载会话
func (s *InMemoryStorage) Load(ctx context.Context, sessionID string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, exists := s.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}

	return cloneSession(session), nil
}

// Delete 删除会话
func (s *InMemoryStorage) Delete(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sessionID]
	if !exists {
		return ErrSessionNotFound
	}

	// 移除标签索引
	s.removeFromTagIndex(session)

	delete(s.sessions, sessionID)

	return nil
}

// List 列出用户的所有会话
func (s *InMemoryStorage) List(ctx context.Context, userID string) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*Session

	for _, session := range s.sessions {
		if session.UserID == userID {
			results = append(results, cloneSession(session))
		}
	}

	// 按更新时间排序（最新的在前）
	s.sortSessionsByUpdated(results)

	return results, nil
}

// ListWithState 列出用户指定状态的会话
func (s *InMemoryStorage) ListWithState(ctx context.Context, userID string, state SessionState) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*Session

	for _, session := range s.sessions {
		if session.UserID == userID && session.State == state {
			results = append(results, cloneSession(session))
		}
	}

	s.sortSessionsByUpdated(results)

	return results, nil
}

// ListByTags 列出包含指定标签的会话
func (s *InMemoryStorage) ListByTags(ctx context.Context, userID string, tags []string) ([]*Session, error) {
	if len(tags) == 0 {
		return nil, ErrInvalidTags
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// 找到包含所有指定标签的会话ID
	candidateIDs := s.getSessionIDsByTags(tags)

	var results []*Session

	for _, sessionID := range candidateIDs {
		if session, ok := s.sessions[sessionID]; ok && session.UserID == userID {
			results = append(results, cloneSession(session))
		}
	}

	s.sortSessionsByUpdated(results)

	return results, nil
}

// Update 更新会话
func (s *InMemoryStorage) Update(ctx context.Context, session *Session) error {
	if session == nil || session.ID == "" {
		return ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[session.ID]; !exists {
		return ErrSessionNotFound
	}

	session.UpdatedAt = time.Now()
	stored := session.Clone()
	if stored == nil {
		return ErrInvalidSession
	}
	s.sessions[stored.ID] = stored

	s.updateTagIndex(stored)

	return nil
}

// AddMessage 添加消息到会话
func (s *InMemoryStorage) AddMessage(ctx context.Context, sessionID string, message interface{}) error {
	msg, ok := message.(types.Message)
	if !ok {
		return ErrInvalidMessageType
	}
	return s.AddMessageWithLimit(ctx, sessionID, msg, 0)
}

// AddMessageWithLimit 原子追加消息，并在需要时截断历史
func (s *InMemoryStorage) AddMessageWithLimit(ctx context.Context, sessionID string, message types.Message, maxHistory int) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sessionID]
	if !exists {
		return ErrSessionNotFound
	}

	session.AddMessage(message)
	if maxHistory > 0 && len(session.History) > maxHistory {
		keepFrom := len(session.History) - maxHistory
		session.History = append([]types.Message(nil), session.History[keepFrom:]...)
	}

	s.sessions[sessionID] = session

	return nil
}

// GetMessages 获取会话消息
func (s *InMemoryStorage) GetMessages(ctx context.Context, sessionID string) ([]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, exists := s.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}

	messages := make([]interface{}, len(session.History))
	for i, msg := range session.History {
		messages[i] = msg
	}

	return messages, nil
}

// Close 关闭会话
func (s *InMemoryStorage) Close(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sessionID]
	if !exists {
		return ErrSessionNotFound
	}

	session.MarkClosed()
	s.sessions[sessionID] = session

	return nil
}

// Cleanup 清理过期会话
func (s *InMemoryStorage) Cleanup(ctx context.Context, after time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed int
	var idsToRemove []string
	now := time.Now()

	for id, session := range s.sessions {
		if session.ExpiresAt != nil {
			if session.ExpiresAt.Before(now) {
				idsToRemove = append(idsToRemove, id)
			}
			continue
		}
		if session.UpdatedAt.Before(after) {
			idsToRemove = append(idsToRemove, id)
		}
	}

	for _, id := range idsToRemove {
		session := s.sessions[id]
		s.removeFromTagIndex(session)
		delete(s.sessions, id)
		removed++
	}

	return removed, nil
}

// GetStatistics 获取存储统计信息
func (s *InMemoryStorage) GetStatistics(ctx context.Context, userID string) (*SessionStatistics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &SessionStatistics{
		Tags: make(map[string]int),
	}

	for _, session := range s.sessions {
		if session.UserID == userID {
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
	}

	return stats, nil
}

// updateTagIndex 更新标签索引
func (s *InMemoryStorage) updateTagIndex(session *Session) {
	// 先移除旧标签的索引
	for tag := range s.sessionTags {
		if ids, ok := s.sessionTags[tag]; ok {
			delete(ids, session.ID)
			if len(ids) == 0 {
				delete(s.sessionTags, tag)
			}
		}
	}

	// 添加新标签的索引
	for _, tag := range session.Metadata.Tags {
		if s.sessionTags[tag] == nil {
			s.sessionTags[tag] = make(map[string]bool)
		}
		s.sessionTags[tag][session.ID] = true
	}
}

// removeFromTagIndex 从标签索引中移除会话
func (s *InMemoryStorage) removeFromTagIndex(session *Session) {
	for tag, ids := range s.sessionTags {
		delete(ids, session.ID)
		if len(ids) == 0 {
			delete(s.sessionTags, tag)
		}
	}
}

// getSessionIDsByTags 查找包含所有指定标签的会话ID
func (s *InMemoryStorage) getSessionIDsByTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}

	var candidateIDs []string

	// 获取第一个标签的所有会话ID
	firstTag := tags[0]
	if ids, ok := s.sessionTags[firstTag]; ok {
		for id := range ids {
			candidateIDs = append(candidateIDs, id)
		}
	}

	// 过滤掉不包含其他标签的会话
	for _, tag := range tags[1:] {
		var filtered []string

		if existingIDs, ok := s.sessionTags[tag]; ok {
			for _, id := range candidateIDs {
				if existingIDs[id] {
					filtered = append(filtered, id)
				}
			}
		}

		candidateIDs = filtered
		if len(candidateIDs) == 0 {
			break
		}
	}

	return candidateIDs
}

// sortSessionsByUpdated 按更新时间排序
func (s *InMemoryStorage) sortSessionsByUpdated(sessions []*Session) {
	if len(sessions) <= 1 {
		return
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
}

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}
	return session.Clone()
}

// Get 加载会话（别名，为了向后兼容）
func (s *InMemoryStorage) Get(ctx context.Context, sessionID string) (*Session, error) {
	return s.Load(ctx, sessionID)
}

// Exists 检查会话是否存在
func (s *InMemoryStorage) Exists(ctx context.Context, sessionID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.sessions[sessionID]
	return exists, nil
}

// ListByUser 列出用户的所有会话（别名，支持分页）
func (s *InMemoryStorage) ListByUser(ctx context.Context, userID string, limit, offset int) ([]*Session, error) {
	sessions, err := s.List(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 应用分页
	if offset >= len(sessions) {
		return []*Session{}, nil
	}

	end := offset + limit
	if end > len(sessions) {
		end = len(sessions)
	}

	return sessions[offset:end], nil
}

// List 列出所有会话（扩展版本，支持分页）
func (s *InMemoryStorage) ListAll(ctx context.Context, limit, offset int) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var allSessions []*Session
	for _, session := range s.sessions {
		allSessions = append(allSessions, cloneSession(session))
	}

	s.sortSessionsByUpdated(allSessions)

	// 应用分页
	if offset >= len(allSessions) {
		return []*Session{}, nil
	}

	end := offset + limit
	if end > len(allSessions) {
		end = len(allSessions)
	}

	return allSessions[offset:end], nil
}

// SearchMetadata 按metadata的context搜索
func (s *InMemoryStorage) SearchContext(ctx context.Context, key string, value interface{}) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*Session

	for _, session := range s.sessions {
		if session.Metadata.Context != nil {
			if val, ok := session.Metadata.Context[key]; ok && val == value {
				results = append(results, cloneSession(session))
			}
		}
	}

	s.sortSessionsByUpdated(results)

	return results, nil
}

// SearchTags 按标签搜索会话
func (s *InMemoryStorage) SearchTags(ctx context.Context, tag string) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids, ok := s.sessionTags[tag]
	if !ok {
		return []*Session{}, nil
	}

	var results []*Session
	for id := range ids {
		if session, exists := s.sessions[id]; exists {
			results = append(results, cloneSession(session))
		}
	}

	s.sortSessionsByUpdated(results)

	return results, nil
}
