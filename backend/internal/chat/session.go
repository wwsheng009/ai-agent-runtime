package chat

import (
	"crypto/rand"
	"math/big"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// SessionState 会话状态
type SessionState string

const (
	StateActive   SessionState = "active"
	StateIdle     SessionState = "idle"
	StateClosed   SessionState = "closed"
	StateArchived SessionState = "archived"
)

const (
	sessionTitleLimit         = 48
	sessionSummaryLimit       = 120
	sessionTitleSourceDerived = "derived"
	sessionTitleSourceManual  = "manual"
)

// SessionMetadata 会话元数据
type SessionMetadata struct {
	Tags        []string               `json:"tags" yaml:"tags"`
	Title       string                 `json:"title" yaml:"title"`
	TitleSource string                 `json:"titleSource,omitempty" yaml:"titleSource,omitempty"`
	Summary     string                 `json:"summary" yaml:"summary"`
	TotalTurns  int                    `json:"totalTurns" yaml:"totalTurns"`
	LastAgent   string                 `json:"lastAgent" yaml:"lastAgent"`
	LastSkill   string                 `json:"lastSkill" yaml:"lastSkill"`
	LastModel   string                 `json:"lastModel" yaml:"lastModel"`
	CreatedBy   string                 `json:"createdBy" yaml:"createdBy"`
	Context     map[string]interface{} `json:"context" yaml:"context"`
}

// Session 用户会话
type Session struct {
	ID         string          `json:"id" yaml:"id"`
	UserID     string          `json:"userId" yaml:"userId"`
	State      SessionState    `json:"state" yaml:"state"`
	History    []types.Message `json:"history" yaml:"history"`
	HeadOffset int             `json:"headOffset,omitempty" yaml:"headOffset,omitempty"`
	Metadata   SessionMetadata `json:"metadata" yaml:"metadata"`
	CreatedAt  time.Time       `json:"createdAt" yaml:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt" yaml:"updatedAt"`
	ExpiresAt  *time.Time      `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
}

// SessionPreview 会话预览信息
type SessionPreview struct {
	ID           string       `json:"id" yaml:"id"`
	UserID       string       `json:"userId" yaml:"userId"`
	State        SessionState `json:"state" yaml:"state"`
	Title        string       `json:"title,omitempty" yaml:"title,omitempty"`
	Summary      string       `json:"summary,omitempty" yaml:"summary,omitempty"`
	MessageCount int          `json:"messageCount" yaml:"messageCount"`
	CreatedAt    time.Time    `json:"createdAt" yaml:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt" yaml:"updatedAt"`
	Tags         []string     `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// NewSession 创建新会话
func NewSession(userID string) *Session {
	now := time.Now()

	return &Session{
		ID:         generateSessionID(),
		UserID:     userID,
		State:      StateActive,
		History:    make([]types.Message, 0),
		HeadOffset: 0,
		Metadata: SessionMetadata{
			Tags:       []string{},
			TotalTurns: 0,
			Context:    make(map[string]interface{}),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// AddMessage 添加消息到会话
func (s *Session) AddMessage(msg types.Message) {
	prevLen := len(s.History)
	s.History = append(s.History, msg)
	if s.HeadOffset > 0 {
		if s.HeadOffset < prevLen {
			s.HeadOffset++
		} else {
			s.HeadOffset = len(s.History)
		}
	}
	s.UpdatedAt = time.Now()

	// 更新元数据
	s.updateMetadata(msg)
	s.refreshDerivedMetadata()
}

// GetRecentMessages 获取最近的 n 条消息
func (s *Session) GetRecentMessages(n int) []types.Message {
	if n <= 0 {
		return []types.Message{}
	}
	history := s.visibleHistory()
	if len(history) <= n {
		return append([]types.Message(nil), history...)
	}
	return append([]types.Message(nil), history[len(history)-n:]...)
}

// GetMessages 获取所有消息
func (s *Session) GetMessages() []types.Message {
	history := s.visibleHistory()
	return append([]types.Message(nil), history...)
}

// ClearHistory 清空历史消息
func (s *Session) ClearHistory() {
	s.History = make([]types.Message, 0)
	s.HeadOffset = 0
	s.UpdatedAt = time.Now()
	s.Metadata.TotalTurns = 0
	s.Metadata.Summary = ""
}

// ReplaceHistory 替换会话历史
func (s *Session) ReplaceHistory(messages []types.Message) {
	if len(messages) == 0 {
		s.ClearHistory()
		return
	}

	cloned := make([]types.Message, len(messages))
	for i, msg := range messages {
		cloned[i] = *msg.Clone()
	}

	s.History = cloned
	if s.HeadOffset > 0 {
		if s.HeadOffset > len(s.History) {
			s.HeadOffset = len(s.History)
		}
	}
	s.UpdatedAt = time.Now()
	s.refreshDerivedMetadata()
}

// AddTag 添加标签
func (s *Session) AddTag(tag string) {
	for _, t := range s.Metadata.Tags {
		if t == tag {
			return
		}
	}
	s.Metadata.Tags = append(s.Metadata.Tags, tag)
	s.UpdatedAt = time.Now()
}

// AddTags 添加多个标签
func (s *Session) AddTags(tags ...string) {
	for _, tag := range tags {
		s.AddTag(tag)
	}
}

// HasTag 检查是否有指定标签
func (s *Session) HasTag(tag string) bool {
	for _, t := range s.Metadata.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// RemoveTag 移除标签
func (s *Session) RemoveTag(tag string) {
	var tags []string
	for _, t := range s.Metadata.Tags {
		if t != tag {
			tags = append(tags, t)
		}
	}
	s.Metadata.Tags = tags
	s.UpdatedAt = time.Now()
}

// SetTTL 设置会话生存时间
func (s *Session) SetTTL(ttl time.Duration) {
	expiresAt := time.Now().Add(ttl)
	s.ExpiresAt = &expiresAt
	s.UpdatedAt = time.Now()
}

// SetContext 设置上下文
func (s *Session) SetContext(key string, value interface{}) {
	if s.Metadata.Context == nil {
		s.Metadata.Context = make(map[string]interface{})
	}
	s.Metadata.Context[key] = value
	s.UpdatedAt = time.Now()
}

// GetContext 获取上下文
func (s *Session) GetContext(key string) (interface{}, bool) {
	if s.Metadata.Context == nil {
		return nil, false
	}
	value, exists := s.Metadata.Context[key]
	return value, exists
}

// UpdateState 更新会话状态
func (s *Session) UpdateState(state SessionState) {
	s.State = state
	s.UpdatedAt = time.Now()

	// 如果关闭或归档，设置过期时间
	if state == StateClosed || state == StateArchived {
		expiresAt := time.Now().Add(30 * 24 * time.Hour) // 30天后过期
		s.ExpiresAt = &expiresAt
	}
}

// IsActive 检查会话是否活跃
func (s *Session) IsActive() bool {
	if s.State != StateActive {
		return false
	}

	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		return false
	}

	return true
}

// UpdateTitle 更新会话标题
func (s *Session) UpdateTitle(title string) {
	s.Metadata.Title = strings.TrimSpace(title)
	s.Metadata.TitleSource = sessionTitleSourceManual
	s.UpdatedAt = time.Now()
}

// LastMessage 返回最后一条消息
func (s *Session) LastMessage() *types.Message {
	history := s.visibleHistory()
	if len(history) == 0 {
		return nil
	}
	return history[len(history)-1].Clone()
}

// SessionID 返回会话 ID，供 runtime 通过接口解耦访问。
func (s *Session) SessionID() string {
	if s == nil {
		return ""
	}
	return s.ID
}

// MessageCount 返回消息数量
func (s *Session) MessageCount() int {
	return len(s.visibleHistory())
}

// BuildPreview 构建会话预览
func (s *Session) BuildPreview() *SessionPreview {
	if s == nil {
		return nil
	}

	title := s.effectiveTitle()

	summary := strings.TrimSpace(s.Metadata.Summary)
	if summary == "" {
		summary = summarizeSessionText(s.lastContent(), sessionSummaryLimit)
	}

	preview := &SessionPreview{
		ID:           s.ID,
		UserID:       s.UserID,
		State:        s.State,
		Title:        title,
		Summary:      summary,
		MessageCount: len(s.History),
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
	if len(s.Metadata.Tags) > 0 {
		preview.Tags = append([]string(nil), s.Metadata.Tags...)
	}
	return preview
}

// Clone 克隆会话（不包含敏感信息）
func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}

	var expiresAt *time.Time
	if s.ExpiresAt != nil {
		copyTime := *s.ExpiresAt
		expiresAt = &copyTime
	}

	clone := &Session{
		ID:         s.ID,
		UserID:     s.UserID,
		State:      s.State,
		History:    make([]types.Message, len(s.History)),
		HeadOffset: s.HeadOffset,
		Metadata:   s.Metadata,
		CreatedAt:  s.CreatedAt,
		UpdatedAt:  s.UpdatedAt,
		ExpiresAt:  expiresAt,
	}

	// 克隆历史
	for i, msg := range s.History {
		clone.History[i] = *msg.Clone()
	}

	// 克隆标签
	tags := make([]string, len(s.Metadata.Tags))
	copy(tags, s.Metadata.Tags)
	clone.Metadata.Tags = tags

	// 克隆上下文
	if s.Metadata.Context != nil {
		context := make(map[string]interface{})
		for k, v := range s.Metadata.Context {
			context[k] = v
		}
		clone.Metadata.Context = context
	}

	clone.refreshDerivedMetadata()
	return clone
}

// updateMetadata 根据消息更新元数据
func (s *Session) updateMetadata(msg types.Message) {
	if msg.Role == "assistant" {
		s.Metadata.LastAgent = "default"
	}
}

func (s *Session) refreshDerivedMetadata() {
	s.Metadata.TotalTurns = len(s.visibleHistory())
	s.refreshDerivedTitle()
	s.Metadata.Summary = summarizeSessionText(s.lastContent(), sessionSummaryLimit)
}

func (s *Session) refreshDerivedTitle() {
	derivedTitle := s.derivedTitle()
	currentTitle := strings.TrimSpace(s.Metadata.Title)
	titleSource := strings.TrimSpace(s.Metadata.TitleSource)
	if titleSource == sessionTitleSourceManual {
		return
	}
	if strings.TrimSpace(derivedTitle) == "" {
		if currentTitle != "" && shouldRepairLegacyDerivedTitle(currentTitle) {
			s.Metadata.Title = ""
			s.Metadata.TitleSource = ""
		}
		return
	}

	if currentTitle != "" && titleSource != sessionTitleSourceDerived && !shouldRepairLegacyDerivedTitle(currentTitle) {
		return
	}

	s.Metadata.Title = derivedTitle
	s.Metadata.TitleSource = sessionTitleSourceDerived
}

func (s *Session) effectiveTitle() string {
	currentTitle := strings.TrimSpace(s.Metadata.Title)
	derivedTitle := s.derivedTitle()
	titleSource := strings.TrimSpace(s.Metadata.TitleSource)
	if currentTitle == "" {
		if titleSource == sessionTitleSourceManual {
			return ""
		}
		return derivedTitle
	}

	if titleSource == sessionTitleSourceManual {
		return currentTitle
	}
	if titleSource == sessionTitleSourceDerived || shouldRepairLegacyDerivedTitle(currentTitle) {
		if strings.TrimSpace(derivedTitle) != "" {
			return derivedTitle
		}
		if shouldRepairLegacyDerivedTitle(currentTitle) && titleSource != sessionTitleSourceManual {
			return ""
		}
	}

	return currentTitle
}

func (s *Session) derivedTitle() string {
	return summarizeSessionText(s.titleSourceContent(), sessionTitleLimit)
}

func (s *Session) titleSourceContent() string {
	if s == nil {
		return ""
	}

	history := s.visibleHistory()
	for _, role := range []string{"user", "assistant"} {
		for _, msg := range history {
			if !strings.EqualFold(strings.TrimSpace(msg.Role), role) {
				continue
			}
			if content := strings.TrimSpace(msg.Content); content != "" {
				return content
			}
		}
	}

	for _, msg := range history {
		if isInstructionMessageRole(msg.Role) || strings.EqualFold(strings.TrimSpace(msg.Role), "tool") {
			continue
		}
		if content := strings.TrimSpace(msg.Content); content != "" {
			return content
		}
	}

	return ""
}

func isInstructionMessageRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return true
	default:
		return false
	}
}

func shouldRepairLegacyDerivedTitle(title string) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}

	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(title)), " "))
	switch {
	case strings.HasPrefix(normalized, "shell guidance:"):
		return true
	case strings.HasPrefix(normalized, "file editing guidance:"):
		return true
	case strings.HasPrefix(normalized, "parallel tool guidance:"):
		return true
	case strings.HasPrefix(normalized, "detected operating system:"):
		return true
	default:
		return false
	}
}

func (s *Session) lastContent() string {
	history := s.visibleHistory()
	for i := len(history) - 1; i >= 0; i-- {
		if content := strings.TrimSpace(history[i].Content); content != "" {
			return content
		}
	}
	return ""
}

func (s *Session) visibleHistory() []types.Message {
	if s == nil {
		return nil
	}
	if s.HeadOffset <= 0 || s.HeadOffset >= len(s.History) {
		return s.History
	}
	return s.History[:s.HeadOffset]
}

// SetHeadOffset sets the visible history length.
func (s *Session) SetHeadOffset(offset int) {
	if s == nil {
		return
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(s.History) {
		offset = len(s.History)
	}
	s.HeadOffset = offset
	s.UpdatedAt = time.Now()
	s.refreshDerivedMetadata()
}

func summarizeSessionText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || limit <= 0 {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

// IsExpired 检查会话是否已过期
func (s *Session) IsExpired() bool {
	if s.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*s.ExpiresAt)
}

// MarkIdle 标记为空闲状态
func (s *Session) MarkIdle() {
	s.UpdateState(StateIdle)
}

// MarkActive 标记为活跃状态
func (s *Session) MarkActive() {
	s.UpdateState(StateActive)
}

// MarkClosed 关闭会话
func (s *Session) MarkClosed() {
	s.UpdateState(StateClosed)
}

// GetTokenCount 获取会话的 Token 估计数（简化版）
func (s *Session) GetTokenCount() int {
	count := 0
	for _, msg := range s.History {
		count += len(msg.Content) / 4 // 粗略估计：4字符约1个token
	}
	return count
}

// generateSessionID 生成会话 ID
func generateSessionID() string {
	return "session_" + time.Now().Format("20060102150405") + "_" +
		randomString(8)
}

// randomString 生成随机字符串
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[n.Int64()]
	}
	return string(b)
}
