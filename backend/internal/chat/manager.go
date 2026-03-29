package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// SessionManagerConfig Session 管理器配置
type SessionManagerConfig struct {
	TTL             time.Duration `yaml:"ttl" json:"ttl"`                         // 会话生存时间
	MaxHistory      int           `yaml:"maxHistory" json:"maxHistory"`           // 最大历史记录数
	CleanupInterval time.Duration `yaml:"cleanupInterval" json:"cleanupInterval"` // 清理间隔
	AutoArchive     bool          `yaml:"autoArchive" json:"autoArchive"`         // 自动归档空闲会话
	IdleTimeout     time.Duration `yaml:"idleTimeout" json:"idleTimeout"`         // 空闲超时
}

// SessionManager 会话管理器
type SessionManager struct {
	storage SessionStorage
	config  *SessionManagerConfig

	// 清理定时器
	cleanupTicker *time.Ticker
	cleanupStop   chan struct{}

	// 归档定时器
	archiveTicker *time.Ticker
	archiveStop   chan struct{}
	stopOnce      sync.Once
}

// DefaultSessionManagerConfig 默认配置
func DefaultSessionManagerConfig() *SessionManagerConfig {
	return &SessionManagerConfig{
		TTL:             7 * 24 * time.Hour, // 7天
		MaxHistory:      100,                // 最多保留100条历史
		CleanupInterval: 1 * time.Hour,      // 每小时清理一次
		AutoArchive:     true,               // 自动归档空闲会话
		IdleTimeout:     24 * time.Hour,     // 24小时空闲后归档
	}
}

// NewSessionManager 创建会话管理器
func NewSessionManager(storage SessionStorage, config *SessionManagerConfig) *SessionManager {
	if config == nil {
		config = DefaultSessionManagerConfig()
	}

	m := &SessionManager{
		storage:     storage,
		config:      config,
		cleanupStop: make(chan struct{}),
		archiveStop: make(chan struct{}),
	}

	// 启动清理定时器
	m.startCleanup()
	m.startArchive()

	return m
}

// Create 创建新会话
func (m *SessionManager) Create(ctx context.Context, userID string) (*Session, error) {
	if userID == "" {
		return nil, fmt.Errorf("userID cannot be empty")
	}

	session := NewSession(userID)

	if err := m.storage.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return session, nil
}

// Get 获取会话
func (m *SessionManager) Get(ctx context.Context, sessionID string) (*Session, error) {
	session, err := m.storage.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	// 检查是否过期
	if session.IsExpired() {
		_ = m.storage.Delete(ctx, sessionID)
		return nil, ErrSessionExpired
	}

	return session, nil
}

// CreateSession 创建新会话（Create的别名）
func (m *SessionManager) CreateSession(ctx context.Context, userID string) (*Session, error) {
	return m.Create(ctx, userID)
}

// GetSession 获取会话（Get的别名）
func (m *SessionManager) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	return m.Get(ctx, sessionID)
}

// GetOrCreate 获取或创建会话（如果不存在则自动创建）
func (m *SessionManager) GetOrCreate(ctx context.Context, userID, sessionID string) (*Session, error) {
	if sessionID == "" {
		return m.Create(ctx, userID)
	}

	session, err := m.Get(ctx, sessionID)
	if err == nil {
		return session, nil
	}

	if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrSessionExpired) || strings.Contains(err.Error(), "expired") {
		return m.Create(ctx, userID)
	}

	return nil, err
}

// AddMessage 添加消息到会话
func (m *SessionManager) AddMessage(ctx context.Context, sessionID string, message interface{}) error {
	msg, err := coerceMessage(message)
	if err != nil {
		return err
	}

	if appender, ok := m.storage.(SessionStorageHistoryAppender); ok {
		return appender.AddMessageWithLimit(ctx, sessionID, msg, m.config.MaxHistory)
	}

	if err := m.storage.AddMessage(ctx, sessionID, msg); err != nil {
		return err
	}

	if m.config.MaxHistory <= 0 {
		return nil
	}

	session, err := m.storage.Load(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session: %w", err)
	}
	if len(session.History) > m.config.MaxHistory {
		keepFrom := len(session.History) - m.config.MaxHistory
		session.History = session.GetMessages()[keepFrom:]
		return m.storage.Update(ctx, session)
	}

	return nil
}

func coerceMessage(message interface{}) (types.Message, error) {
	switch msg := message.(type) {
	case types.Message:
		return msg, nil
	case *types.Message:
		if msg == nil {
			return types.Message{}, fmt.Errorf("message cannot be nil")
		}
		return *msg, nil
	default:
		return types.Message{}, fmt.Errorf("invalid message type: %T", message)
	}
}

// Update 更新会话
func (m *SessionManager) Update(ctx context.Context, session *Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
	}

	if session.ID == "" {
		return fmt.Errorf("session ID cannot be empty")
	}

	return m.storage.Update(ctx, session)
}

// Close 关闭会话
func (m *SessionManager) Close(ctx context.Context, sessionID string) error {
	return m.storage.Close(ctx, sessionID)
}

// Delete 删除会话
func (m *SessionManager) Delete(ctx context.Context, sessionID string) error {
	return m.storage.Delete(ctx, sessionID)
}

// List 列出用户的所有会话
func (m *SessionManager) List(ctx context.Context, userID string) ([]*Session, error) {
	return m.storage.List(ctx, userID)
}

// ListActive 列出用户的活跃会话
func (m *SessionManager) ListActive(ctx context.Context, userID string) ([]*Session, error) {
	return m.storage.ListWithState(ctx, userID, StateActive)
}

// ListByTags 列出包含指定标签的会话
func (m *SessionManager) ListByTags(ctx context.Context, userID string, tags []string) ([]*Session, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("tags cannot be empty")
	}

	return m.storage.ListByTags(ctx, userID, tags)
}

// GetStatistics 获取用户会话统计
func (m *SessionManager) GetStatistics(ctx context.Context, userID string) (*SessionStatistics, error) {
	return m.storage.GetStatistics(ctx, userID)
}

// GetHistory 获取会话历史
func (m *SessionManager) GetHistory(ctx context.Context, sessionID string, limit int) ([]types.Message, error) {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	return session.GetRecentMessages(limit), nil
}

// ClearHistory 清空会话历史
func (m *SessionManager) ClearHistory(ctx context.Context, sessionID string) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	session.ClearHistory()
	session.UpdatedAt = time.Now()

	return m.storage.Update(ctx, session)
}

// Archive 归档会话
func (m *SessionManager) Archive(ctx context.Context, sessionID string) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	session.UpdateState(StateArchived)
	return m.storage.Update(ctx, session)
}

// Activate 激活会话
func (m *SessionManager) Activate(ctx context.Context, sessionID string) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	session.UpdateState(StateActive)
	return m.storage.Update(ctx, session)
}

// SetTTL 设置会话 TTL
func (m *SessionManager) SetTTL(session *Session, ttl time.Duration) {
	if session == nil {
		return
	}

	session.SetTTL(ttl)
}

// AddTag 为会话添加标签
func (m *SessionManager) AddTag(ctx context.Context, sessionID, tag string) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	session.AddTag(tag)
	return m.storage.Update(ctx, session)
}

// RemoveTag 从会话移除标签
func (m *SessionManager) RemoveTag(ctx context.Context, sessionID, tag string) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	session.RemoveTag(tag)
	return m.storage.Update(ctx, session)
}

// AddTags 为会话添加多个标签
func (m *SessionManager) AddTags(ctx context.Context, sessionID string, tags ...string) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	for _, tag := range tags {
		session.AddTag(tag)
	}
	return m.storage.Update(ctx, session)
}

// UpdateContext 更新会话上下文（SetContext的别名）
func (m *SessionManager) UpdateContext(ctx context.Context, sessionID, key string, value interface{}) error {
	return m.SetContext(ctx, sessionID, key, value)
}

// SetTitle 设置会话标题
func (m *SessionManager) SetTitle(ctx context.Context, sessionID, title string) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	session.UpdateTitle(title)
	return m.storage.Update(ctx, session)
}

// SetContext 设置会话上下文
func (m *SessionManager) SetContext(ctx context.Context, sessionID, key string, value interface{}) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	session.SetContext(key, value)
	return m.storage.Update(ctx, session)
}

// startCleanup 启动清理定时器
func (m *SessionManager) startCleanup() {
	if m.config.CleanupInterval <= 0 {
		return
	}

	m.cleanupTicker = time.NewTicker(m.config.CleanupInterval)

	go func() {
		for {
			select {
			case <-m.cleanupTicker.C:
				m.Cleanup()
			case <-m.cleanupStop:
				m.cleanupTicker.Stop()
				return
			}
		}
	}()
}

// startArchive 启动归档定时器
func (m *SessionManager) startArchive() {
	if !m.config.AutoArchive || m.config.CleanupInterval <= 0 {
		return
	}

	m.archiveTicker = time.NewTicker(m.config.CleanupInterval)

	go func() {
		for {
			select {
			case <-m.archiveTicker.C:
				m.ArchiveIdleSessions()
			case <-m.archiveStop:
				m.archiveTicker.Stop()
				return
			}
		}
	}()
}

// Cleanup 清理过期会话
func (m *SessionManager) Cleanup() int {
	ctx := context.Background()
	after := time.Now().Add(-m.config.TTL)

	removed, err := m.storage.Cleanup(ctx, after)
	if err != nil {
		// log error
		return 0
	}

	return removed
}

// CleanupExpired 清理过期会话（Cleanup的别名，带context和返回值）
func (m *SessionManager) CleanupExpired(ctx context.Context) (int, error) {
	after := time.Now().Add(-m.config.TTL)

	removed, err := m.storage.Cleanup(ctx, after)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup expired sessions: %w", err)
	}

	return removed, nil
}

// SessionSearchOptions 会话搜索选项
type SessionSearchOptions struct {
	UserID string       `json:"userId,omitempty" yaml:"userId,omitempty"`
	Tags   []string     `json:"tags,omitempty" yaml:"tags,omitempty"`
	State  SessionState `json:"state,omitempty" yaml:"state,omitempty"`
	Limit  int          `json:"limit,omitempty" yaml:"limit,omitempty"`
	Offset int          `json:"offset,omitempty" yaml:"offset,omitempty"`
}

// SearchSessions 搜索会话
func (m *SessionManager) SearchSessions(ctx context.Context, opts *SessionSearchOptions) ([]*Session, error) {
	if opts == nil {
		opts = &SessionSearchOptions{}
	}

	var sessions []*Session
	var err error

	// 按用户ID筛选
	if opts.UserID != "" {
		sessions, err = m.storage.List(ctx, opts.UserID)
		if err != nil {
			return nil, fmt.Errorf("failed to list sessions: %w", err)
		}
	} else {
		listAller, ok := m.storage.(SessionStorageAllLister)
		if !ok {
			return nil, fmt.Errorf("storage does not support listing all sessions")
		}
		sessions, err = listAller.ListAll(ctx, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to list sessions: %w", err)
		}
	}

	// 按状态筛选
	if opts.State != "" {
		var filtered []*Session
		for _, session := range sessions {
			if session.State == opts.State {
				filtered = append(filtered, session)
			}
		}
		sessions = filtered
	}

	// 按标签筛选（AND逻辑）
	if len(opts.Tags) > 0 {
		var filtered []*Session
		for _, session := range sessions {
			hasAllTags := true
			for _, tag := range opts.Tags {
				if !session.HasTag(tag) {
					hasAllTags = false
					break
				}
			}
			if hasAllTags {
				filtered = append(filtered, session)
			}
		}
		sessions = filtered
	}

	// 应用分页
	if opts.Offset > 0 || opts.Limit > 0 {
		if opts.Offset >= len(sessions) {
			return []*Session{}, nil
		}
		end := opts.Offset + opts.Limit
		if end > len(sessions) || opts.Limit == 0 {
			end = len(sessions)
		}
		sessions = sessions[opts.Offset:end]
	}

	return sessions, nil
}

// GetLatest 获取用户最近更新的会话
func (m *SessionManager) GetLatest(ctx context.Context, userID string) (*Session, error) {
	sessions, err := m.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, ErrSessionNotFound
	}
	return sessions[0], nil
}

// ListPreviews 列出会话预览信息
func (m *SessionManager) ListPreviews(ctx context.Context, userID string, limit, offset int) ([]*SessionPreview, error) {
	sessions, err := m.List(ctx, userID)
	if err != nil {
		return nil, err
	}

	if offset > 0 {
		if offset >= len(sessions) {
			return []*SessionPreview{}, nil
		}
		sessions = sessions[offset:]
	}
	if limit > 0 && limit < len(sessions) {
		sessions = sessions[:limit]
	}

	previews := make([]*SessionPreview, 0, len(sessions))
	for _, session := range sessions {
		if session == nil {
			continue
		}
		previews = append(previews, session.BuildPreview())
	}
	return previews, nil
}

// ArchiveSession 归档会话（Archive的别名）
func (m *SessionManager) ArchiveSession(ctx context.Context, sessionID string) error {
	return m.Archive(ctx, sessionID)
}

// ArchiveIdleSessions 归档空闲会话
func (m *SessionManager) ArchiveIdleSessions() {
	ctx := context.Background()
	idleThreshold := time.Now().Add(-m.config.IdleTimeout)

	listAller, ok := m.storage.(SessionStorageAllLister)
	if !ok {
		return
	}
	sessions, err := listAller.ListAll(ctx, 0, 0)
	if err != nil {
		return
	}

	for _, session := range sessions {
		if session.State == StateActive && session.UpdatedAt.Before(idleThreshold) {
			session.UpdateState(StateIdle)
			m.storage.Update(ctx, session)
		}
	}
}

// Stop 停止管理器（停止定时器）
func (m *SessionManager) Stop() {
	m.stopOnce.Do(func() {
		if m.cleanupTicker != nil {
			close(m.cleanupStop)
		}

		if m.archiveTicker != nil {
			close(m.archiveStop)
		}
	})
}

// GetConfig 获取配置
func (m *SessionManager) GetConfig() *SessionManagerConfig {
	return m.config
}

// SetConfig 设置配置
func (m *SessionManager) SetConfig(config *SessionManagerConfig) {
	m.config = config
}

// GetStorage 获取存储实现
func (m *SessionManager) GetStorage() SessionStorage {
	return m.storage
}
