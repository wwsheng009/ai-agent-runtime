package chat

import (
	"crypto/rand"
	"fmt"
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
	sessionTitleSourceCompact = "compact"

	// Compact lineage is stored on Metadata.Context so it survives persistence
	// without a schema migration. Compact currently rewrites history in place
	// (same session ID); these keys still record parent linkage + generation so
	// multi-round compact titles chain to the original root title.
	ContextCompactRootTitle       = "compact_root_title"
	ContextCompactParentSessionID = "compact_parent_session_id"
	ContextCompactRootSessionID   = "compact_root_session_id"
	ContextCompactGeneration      = "compact_generation"
	ContextCompactSourceSessionID = "compact_source_session_id"
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
	ID                    string          `json:"id" yaml:"id"`
	UserID                string          `json:"userId" yaml:"userId"`
	State                 SessionState    `json:"state" yaml:"state"`
	History               []types.Message `json:"history" yaml:"history"`
	HeadOffset            int             `json:"headOffset,omitempty" yaml:"headOffset,omitempty"`
	CanonicalMessageCount int             `json:"canonicalMessageCount,omitempty" yaml:"canonicalMessageCount,omitempty"`
	Metadata              SessionMetadata `json:"metadata" yaml:"metadata"`
	CreatedAt             time.Time       `json:"createdAt" yaml:"createdAt"`
	UpdatedAt             time.Time       `json:"updatedAt" yaml:"updatedAt"`
	ExpiresAt             *time.Time      `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`

	// HistoryLoaded distinguishes a metadata-only listing result from an
	// intentionally empty prompt projection. It is never serialized.
	HistoryLoaded bool `json:"-" yaml:"-"`
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
		ID:            generateSessionID(),
		UserID:        userID,
		State:         StateActive,
		History:       make([]types.Message, 0),
		HeadOffset:    0,
		HistoryLoaded: true,
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
	prevTurnID := ""
	if len(s.History) > 0 {
		prevTurnID = types.TurnID(s.History[len(s.History)-1])
	}
	_ = types.EnsureMessageIdentity(&msg, prevTurnID)

	prevLen := len(s.History)
	if s.CanonicalMessageCount < prevLen {
		s.CanonicalMessageCount = prevLen
	}
	s.History = append(s.History, msg)
	s.HistoryLoaded = true
	s.CanonicalMessageCount++
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
	s.CanonicalMessageCount = 0
	s.HistoryLoaded = true
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
	_ = types.EnsureHistoryMessageIdentities(cloned)

	s.History = cloned
	s.HistoryLoaded = true
	if len(cloned) > s.CanonicalMessageCount {
		s.CanonicalMessageCount = len(cloned)
	}
	if s.HeadOffset > 0 {
		if s.HeadOffset > len(s.History) {
			s.HeadOffset = len(s.History)
		}
	}
	s.UpdatedAt = time.Now()
	s.refreshDerivedMetadata()
}

// EnsureMessageIdentities backfills stable message_id / turn_id on loaded history.
// Returns true when metadata was written (caller should persist when durable).
func (s *Session) EnsureMessageIdentities() bool {
	if s == nil || len(s.History) == 0 {
		return false
	}
	return types.EnsureHistoryMessageIdentities(s.History)
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

// CompactRootTitleCandidate returns the title that multi-round compact should
// inherit. Call this before ReplaceHistory so compaction summary text does not
// become the new root title.
func (s *Session) CompactRootTitleCandidate() string {
	if s == nil {
		return ""
	}
	if root := strings.TrimSpace(contextStringValue(s.Metadata.Context, ContextCompactRootTitle)); root != "" {
		return root
	}
	title := strings.TrimSpace(s.Metadata.Title)
	if title == "" {
		title = strings.TrimSpace(s.effectiveTitle())
	}
	return stripCompactTitleMarker(title)
}

// ApplyCompactTitleLineage records compact parent/root linkage + generation for
// diagnostics. It intentionally does NOT rewrite the display title: compaction
// is a model-context operation and must never change what the user sees as the
// session title. Legacy titles that already embed the old " · compact #N"
// marker are repaired to their root title. parentSessionID is the session that
// was compacted to produce the current history (usually the same ID when
// compact rewrites in place). rootTitleHint should be captured before history
// rewrite (see CompactRootTitleCandidate); when empty, existing lineage context
// or the current title is used.
func (s *Session) ApplyCompactTitleLineage(parentSessionID, rootTitleHint string) {
	if s == nil {
		return
	}

	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		parentSessionID = strings.TrimSpace(s.ID)
	}

	rootTitle := strings.TrimSpace(contextStringValue(s.Metadata.Context, ContextCompactRootTitle))
	rootSessionID := strings.TrimSpace(contextStringValue(s.Metadata.Context, ContextCompactRootSessionID))
	generation := contextIntValue(s.Metadata.Context, ContextCompactGeneration)

	if rootTitle == "" {
		rootTitle = strings.TrimSpace(rootTitleHint)
	}
	if rootTitle == "" {
		rootTitle = strings.TrimSpace(s.Metadata.Title)
		if rootTitle == "" {
			rootTitle = strings.TrimSpace(s.effectiveTitle())
		}
	}
	rootTitle = stripCompactTitleMarker(rootTitle)
	if rootTitle == "" {
		rootTitle = "(untitled)"
	}
	if rootSessionID == "" {
		rootSessionID = parentSessionID
		if rootSessionID == "" {
			rootSessionID = strings.TrimSpace(s.ID)
		}
	}
	if generation < 0 {
		generation = 0
	}
	generation++

	// Compaction never rewrites the user-visible title. Repair legacy titles
	// that still embed the old " · compact #N" marker (manual titles are left
	// untouched so a user-typed marker survives).
	if s.Metadata.TitleSource != sessionTitleSourceManual {
		if cleaned, changed := repairCompactTitleMarker(strings.TrimSpace(s.Metadata.Title)); changed {
			s.Metadata.Title = cleaned
			if s.Metadata.TitleSource == sessionTitleSourceCompact {
				s.Metadata.TitleSource = sessionTitleSourceDerived
			}
		}
	}
	s.SetContext(ContextCompactRootTitle, rootTitle)
	s.SetContext(ContextCompactRootSessionID, rootSessionID)
	s.SetContext(ContextCompactParentSessionID, parentSessionID)
	s.SetContext(ContextCompactSourceSessionID, parentSessionID)
	s.SetContext(ContextCompactGeneration, generation)
	s.UpdatedAt = time.Now()
}

// repairCompactTitleMarker strips a legacy " · compact #N" marker from a title
// and reports whether the title changed. Compaction no longer edits titles, so
// any marker found in persisted data is legacy and can be safely removed.
func repairCompactTitleMarker(title string) (string, bool) {
	cleaned := stripCompactTitleMarker(strings.TrimSpace(title))
	if cleaned == title {
		return title, false
	}
	if cleaned == "" {
		cleaned = "(untitled)"
	}
	return cleaned, true
}

func formatCompactChildTitle(rootTitle string, generation int) string {
	rootTitle = strings.TrimSpace(rootTitle)
	if rootTitle == "" {
		rootTitle = "(untitled)"
	}
	if generation < 1 {
		generation = 1
	}
	return rootTitle + " · compact #" + itoaCompactGeneration(generation)
}

func stripCompactTitleMarker(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	const marker = " · compact #"
	if idx := strings.LastIndex(title, marker); idx >= 0 {
		// Only strip a trailing marker with a generation suffix.
		suffix := title[idx+len(marker):]
		if isCompactGenerationSuffix(suffix) {
			return strings.TrimSpace(title[:idx])
		}
	}
	return title
}

func isCompactGenerationSuffix(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func itoaCompactGeneration(n int) string {
	if n <= 0 {
		return "0"
	}
	// Avoid importing strconv solely for this small helper on the hot title path.
	const digits = "0123456789"
	if n < 10 {
		return digits[n : n+1]
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

func contextStringValue(ctx map[string]interface{}, key string) string {
	if ctx == nil {
		return ""
	}
	raw, ok := ctx[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func contextIntValue(ctx map[string]interface{}, key string) int {
	if ctx == nil {
		return 0
	}
	raw, ok := ctx[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case string:
		n := 0
		for _, r := range strings.TrimSpace(v) {
			if r < '0' || r > '9' {
				return 0
			}
			n = n*10 + int(r-'0')
		}
		return n
	default:
		return 0
	}
}

type sessionTitleStateSnapshot struct {
	Title       string
	TitleSource string
	RootTitle   string
	RootID      string
	ParentID    string
	SourceID    string
	Generation  interface{}
	HasRoot     bool
	HasRootID   bool
	HasParent   bool
	HasSource   bool
	HasGen      bool
}

func snapshotSessionTitleState(session *Session) sessionTitleStateSnapshot {
	if session == nil {
		return sessionTitleStateSnapshot{}
	}
	snap := sessionTitleStateSnapshot{
		Title:       session.Metadata.Title,
		TitleSource: session.Metadata.TitleSource,
	}
	if session.Metadata.Context == nil {
		return snap
	}
	if v, ok := session.Metadata.Context[ContextCompactRootTitle]; ok {
		snap.HasRoot = true
		snap.RootTitle = contextStringValue(session.Metadata.Context, ContextCompactRootTitle)
		_ = v
	}
	if _, ok := session.Metadata.Context[ContextCompactRootSessionID]; ok {
		snap.HasRootID = true
		snap.RootID = contextStringValue(session.Metadata.Context, ContextCompactRootSessionID)
	}
	if _, ok := session.Metadata.Context[ContextCompactParentSessionID]; ok {
		snap.HasParent = true
		snap.ParentID = contextStringValue(session.Metadata.Context, ContextCompactParentSessionID)
	}
	if _, ok := session.Metadata.Context[ContextCompactSourceSessionID]; ok {
		snap.HasSource = true
		snap.SourceID = contextStringValue(session.Metadata.Context, ContextCompactSourceSessionID)
	}
	if v, ok := session.Metadata.Context[ContextCompactGeneration]; ok {
		snap.HasGen = true
		snap.Generation = v
	}
	return snap
}

func restoreSessionTitleState(session *Session, snap sessionTitleStateSnapshot) {
	if session == nil {
		return
	}
	session.Metadata.Title = snap.Title
	session.Metadata.TitleSource = snap.TitleSource
	if session.Metadata.Context == nil {
		if !snap.HasRoot && !snap.HasRootID && !snap.HasParent && !snap.HasSource && !snap.HasGen {
			return
		}
		session.Metadata.Context = make(map[string]interface{})
	}
	restoreContextValue(session.Metadata.Context, ContextCompactRootTitle, snap.RootTitle, snap.HasRoot)
	restoreContextValue(session.Metadata.Context, ContextCompactRootSessionID, snap.RootID, snap.HasRootID)
	restoreContextValue(session.Metadata.Context, ContextCompactParentSessionID, snap.ParentID, snap.HasParent)
	restoreContextValue(session.Metadata.Context, ContextCompactSourceSessionID, snap.SourceID, snap.HasSource)
	if snap.HasGen {
		session.Metadata.Context[ContextCompactGeneration] = snap.Generation
	} else {
		delete(session.Metadata.Context, ContextCompactGeneration)
	}
}

func restoreContextValue(ctx map[string]interface{}, key, value string, present bool) {
	if ctx == nil {
		return
	}
	if present {
		ctx[key] = value
		return
	}
	delete(ctx, key)
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
	if s == nil {
		return 0
	}
	if s.CanonicalMessageCount > len(s.visibleHistory()) {
		return s.CanonicalMessageCount
	}
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
		MessageCount: s.MessageCount(),
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
	clone := s.CloneWithoutHistory()
	clone.History = make([]types.Message, len(s.History))
	clone.HistoryLoaded = s.HistoryLoaded

	for i, msg := range s.History {
		clone.History[i] = *msg.Clone()
	}

	clone.refreshDerivedMetadata()
	return clone
}

// CloneWithoutHistory clones session metadata without retaining or copying the
// prompt projection. HistoryLoaded is false until the caller supplies history.
func (s *Session) CloneWithoutHistory() *Session {
	if s == nil {
		return nil
	}

	var expiresAt *time.Time
	if s.ExpiresAt != nil {
		copyTime := *s.ExpiresAt
		expiresAt = &copyTime
	}

	clone := &Session{
		ID:                    s.ID,
		UserID:                s.UserID,
		State:                 s.State,
		HeadOffset:            s.HeadOffset,
		CanonicalMessageCount: s.CanonicalMessageCount,
		Metadata:              s.Metadata,
		CreatedAt:             s.CreatedAt,
		UpdatedAt:             s.UpdatedAt,
		ExpiresAt:             expiresAt,
		HistoryLoaded:         false,
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

	return clone
}

// updateMetadata 根据消息更新元数据
func (s *Session) updateMetadata(msg types.Message) {
	if msg.Role == "assistant" {
		s.Metadata.LastAgent = "default"
	}
}

func (s *Session) refreshDerivedMetadata() {
	messageCount := len(s.visibleHistory())
	if s.CanonicalMessageCount > messageCount {
		messageCount = s.CanonicalMessageCount
	}
	s.Metadata.TotalTurns = messageCount
	s.refreshDerivedTitle()
	s.Metadata.Summary = summarizeSessionText(s.lastContent(), sessionSummaryLimit)
}

func (s *Session) refreshDerivedTitle() {
	currentTitle := strings.TrimSpace(s.Metadata.Title)
	titleSource := strings.TrimSpace(s.Metadata.TitleSource)

	// Manual titles are always sticky across history rewrites.
	if titleSource == sessionTitleSourceManual {
		return
	}

	// Repair legacy " · compact #N" markers: compaction no longer edits titles.
	// This also migrates titles persisted by the old in-place lineage scheme.
	if cleaned, changed := repairCompactTitleMarker(currentTitle); changed {
		s.Metadata.Title = cleaned
		currentTitle = cleaned
		if titleSource == sessionTitleSourceCompact {
			titleSource = sessionTitleSourceDerived
			s.Metadata.TitleSource = titleSource
		}
	}
	if titleSource == sessionTitleSourceCompact {
		return
	}

	// A non-empty derived title is sticky: compaction and other history
	// rewrites must not re-derive the title from surviving messages. Legacy
	// titles that are actually compaction summaries are still repaired below.
	if currentTitle != "" && titleSource == sessionTitleSourceDerived && !shouldRepairLegacyDerivedTitle(currentTitle) {
		return
	}

	derivedTitle := s.derivedTitle()
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

	// Manual titles are always sticky.
	if titleSource == sessionTitleSourceManual {
		return currentTitle
	}

	// Legacy compact titles embed " · compact #N"; strip the marker so lists
	// show the stable root title (compaction no longer edits titles). Manual
	// titles are excluded above so a user-typed marker survives.
	if cleaned, changed := repairCompactTitleMarker(currentTitle); changed {
		currentTitle = cleaned
		if titleSource == sessionTitleSourceCompact {
			titleSource = sessionTitleSourceDerived
		}
	}
	if titleSource == sessionTitleSourceCompact {
		return currentTitle
	}

	if currentTitle == "" {
		return derivedTitle
	}

	if titleSource == sessionTitleSourceDerived {
		// Sticky derived titles: keep the stored title unless it is a legacy
		// compaction summary that should be repaired to a real message.
		if shouldRepairLegacyDerivedTitle(currentTitle) {
			if strings.TrimSpace(derivedTitle) != "" {
				return derivedTitle
			}
			return ""
		}
		return currentTitle
	}

	if shouldRepairLegacyDerivedTitle(currentTitle) {
		if strings.TrimSpace(derivedTitle) != "" {
			return derivedTitle
		}
		if strings.TrimSpace(currentTitle) != "" {
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
			if isContextManagementArtifact(msg) {
				continue
			}
			if content := strings.TrimSpace(msg.Content); content != "" && !shouldIgnoreDerivedTitleContent(content) {
				return content
			}
		}
	}

	for _, msg := range history {
		if isInstructionMessageRole(msg.Role) || strings.EqualFold(strings.TrimSpace(msg.Role), "tool") {
			continue
		}
		if isContextManagementArtifact(msg) {
			continue
		}
		if content := strings.TrimSpace(msg.Content); content != "" && !shouldIgnoreDerivedTitleContent(content) {
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
	return shouldIgnoreDerivedTitleContent(title)
}

// isContextManagementArtifact reports whether a message is a synthetic
// context-management artifact (compaction summaries, recall/ledger/workspace
// snapshots, corrections, todo state, ...) rather than a real conversational
// message. Such messages must never drive derived titles or previews.
func isContextManagementArtifact(msg types.Message) bool {
	if stage := strings.TrimSpace(msg.Metadata.GetString("context_stage", "")); stage != "" {
		return true
	}
	content := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(msg.Content)), " "))
	return strings.HasPrefix(content, "compacted context from earlier turns:")
}

// needsLegacyTitleRepair reports whether a persisted title requires the lazy
// legacy-title repair on load: compact markers ("title · compact #N"), titles
// derived from compaction summaries, or instruction-pollution titles that
// survived older writes. Manual titles are always left untouched.
func needsLegacyTitleRepair(source, title string) bool {
	if strings.TrimSpace(source) == sessionTitleSourceManual {
		return false
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	if strings.TrimSpace(source) == sessionTitleSourceCompact {
		return true
	}
	if cleaned := stripCompactTitleMarker(title); cleaned != title {
		return true
	}
	return shouldRepairLegacyDerivedTitle(title)
}

func shouldIgnoreDerivedTitleContent(content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(content)), " "))
	switch {
	case strings.HasPrefix(normalized, "shell guidance:"):
		return true
	case strings.HasPrefix(normalized, "file editing guidance:"):
		return true
	case strings.HasPrefix(normalized, "parallel tool guidance:"):
		return true
	case strings.HasPrefix(normalized, "detected operating system:"):
		return true
	case strings.HasPrefix(normalized, "running shell commands="):
		return true
	case strings.HasPrefix(normalized, "\u2022 running shell commands="):
		return true
	case strings.HasPrefix(normalized, "exit code:") && strings.Contains(normalized, " shell:"):
		return true
	case strings.HasPrefix(normalized, "runtime tool result contract:"):
		return true
	case strings.HasPrefix(normalized, "compacted context from earlier turns:"):
		return true
	case strings.HasPrefix(normalized, "compacted context from earlier turns (continued):"):
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
