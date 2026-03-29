package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// Memory 记忆系统
type Memory struct {
	mu           sync.RWMutex
	observations []types.Observation
	maxSize      int
	sessions     map[string]*Session
}

// NewMemory 创建记忆
func NewMemory(maxSize int) *Memory {
	if maxSize <= 0 {
		maxSize = 100
	}
	return &Memory{
		observations: make([]types.Observation, 0),
		maxSize:      maxSize,
		sessions:     make(map[string]*Session),
	}
}

// Add 添加观察
func (m *Memory) Add(obs types.Observation) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if obs.Timestamp.IsZero() {
		obs.Timestamp = time.Now()
	}

	m.observations = append(m.observations, obs)

	// 超过最大容量，删除最旧的
	if len(m.observations) > m.maxSize {
		m.observations = m.observations[1:]
	}
}

// AddBatch 批量添加观察
func (m *Memory) AddBatch(observations []types.Observation) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for _, obs := range observations {
		if obs.Timestamp.IsZero() {
			obs.Timestamp = now
		}
	}

	m.observations = append(m.observations, observations...)

	// 截断到最大容量
	if len(m.observations) > m.maxSize {
		keep := len(m.observations) - m.maxSize
		m.observations = m.observations[keep:]
	}
}

// Recent 获取最近的观察
func (m *Memory) Recent(n int) []types.Observation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if n <= 0 {
		return []types.Observation{}
	}

	if n > len(m.observations) {
		n = len(m.observations)
	}

	start := len(m.observations) - n
	result := make([]types.Observation, n)
	copy(result, m.observations[start:])
	return result
}

// GetAll 获取所有观察
func (m *Memory) GetAll() []types.Observation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]types.Observation, len(m.observations))
	copy(result, m.observations)
	return result
}

// Clear 清空记忆
func (m *Memory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.observations = make([]types.Observation, 0)
}

// Count 获取观察数量
func (m *Memory) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.observations)
}

// Search 搜索记忆（简单实现）
func (m *Memory) Search(query string, limit int) []types.Observation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if query == "" {
		return m.Recent(limit)
	}

	queryLower := strings.ToLower(query)
	var results []types.Observation

	for _, obs := range m.observations {
		// 搜索步骤 ID
		if strings.Contains(strings.ToLower(obs.Step), queryLower) {
			results = append(results, obs)
			continue
		}

		// 搜索工具名
		if strings.Contains(strings.ToLower(obs.Tool), queryLower) {
			results = append(results, obs)
			continue
		}

		// 搜索错误信息
		if obs.Error != "" && strings.Contains(strings.ToLower(obs.Error), queryLower) {
			results = append(results, obs)
			continue
		}

		// 搜索输出
		if output, ok := obs.Output.(string); ok {
			if strings.Contains(strings.ToLower(output), queryLower) {
				results = append(results, obs)
				continue
			}
		}
	}

	// 限制结果数量
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results
}

// FilterByTool 根据工具过滤观察
func (m *Memory) FilterByTool(toolName string) []types.Observation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []types.Observation
	for _, obs := range m.observations {
		if obs.Tool == toolName {
			results = append(results, obs)
		}
	}
	return results
}

// FilterBySuccess 根据成功状态过滤
func (m *Memory) FilterBySuccess(success bool) []types.Observation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []types.Observation
	for _, obs := range m.observations {
		if obs.Success == success {
			results = append(results, obs)
		}
	}
	return results
}

// FilterByTime 根据时间范围过滤
func (m *Memory) FilterByTime(start, end time.Time) []types.Observation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []types.Observation
	for _, obs := range m.observations {
		if (obs.Timestamp.After(start) || obs.Timestamp.Equal(start)) &&
			(obs.Timestamp.Before(end) || obs.Timestamp.Equal(end)) {
			results = append(results, obs)
		}
	}
	return results
}

// GetStats 获取记忆统计
func (m *Memory) GetStats() *MemoryStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := &MemoryStats{
		TotalObservations:  len(m.observations),
		MaxSize:            m.maxSize,
		TotalSessions:      len(m.sessions),
		ObservationsByTool: make(map[string]int),
	}

	for _, obs := range m.observations {
		stats.ObservationsByTool[obs.Tool]++
		if obs.Success {
			stats.SuccessfulObservations++
		} else {
			stats.FailedObservations++
		}
	}

	if stats.MaxSize > 0 {
		stats.Usage = float64(stats.TotalObservations) / float64(stats.MaxSize)
		if stats.Usage > 1 {
			stats.Usage = 1
		}
	}

	return stats
}

// MemoryStats 记忆统计
type MemoryStats struct {
	TotalObservations      int            `json:"totalObservations"`
	MaxSize                int            `json:"maxSize"`
	SuccessfulObservations int            `json:"successfulObservations"`
	FailedObservations     int            `json:"failedObservations"`
	ObservationsByTool     map[string]int `json:"observationsByTool"`
	TotalSessions          int            `json:"totalSessions"`
	Usage                  float64        `json:"usage"` // 0-1, 表示内存使用率
}

// CreateSession 创建会话
func (m *Memory) CreateSession(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := &Session{
		ID:        id,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Memory:    make([]string, 0),
	}

	m.sessions[id] = session
	return session
}

// GetSession 获取会话
func (m *Memory) GetSession(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[id]
	return session, ok
}

// DeleteSession 删除会话
func (m *Memory) DeleteSession(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.sessions, id)
}

// Session 记忆会话
type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Context   string    `json:"context"`
	Memory    []string  `json:"memory"`
}

// AddMessage 添加消息到会话
func (s *Session) AddMessage(msg string) {
	s.Memory = append(s.Memory, msg)
	s.UpdatedAt = time.Now()
}

// GetRecentMessages 获取最近的消息
func (s *Session) GetRecentMessages(n int) []string {
	if n <= 0 {
		return []string{}
	}

	if n > len(s.Memory) {
		n = len(s.Memory)
	}

	start := len(s.Memory) - n
	result := make([]string, n)
	copy(result, s.Memory[start:])
	return result
}

// FormatObservations 格式化观察为字符串
func (m *Memory) FormatObservations(obs []types.Observation) string {
	if len(obs) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("Recent Observations:\n")

	for _, o := range obs {
		// 将输出转换为字符串
		var outputStr string
		if o.Output != nil {
			switch v := o.Output.(type) {
			case string:
				outputStr = v
			case []byte:
				outputStr = string(v)
			case error:
				outputStr = v.Error()
			default:
				outputStr = fmt.Sprintf("%v", v)
			}
		}

		builder.WriteString(fmt.Sprintf("- [%s] %s: %s\n", o.Step, o.Tool, outputStr))
		if !o.Success && o.Error != "" {
			builder.WriteString(fmt.Sprintf("  Error: %s\n", o.Error))
		}
	}

	return builder.String()
}

// ToContext 转换为上下文字符串
func (m *Memory) ToContext(n int) string {
	recent := m.Recent(n)
	return m.FormatObservations(recent)
}

// SetMaxSize 设置最大容量
func (m *Memory) SetMaxSize(max int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.maxSize = max

	// 如果当前超过新限制，截断
	if len(m.observations) > max {
		keep := len(m.observations) - max
		m.observations = m.observations[keep:]
	}
}

// GetMaxSize 获取最大容量
func (m *Memory) GetMaxSize() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxSize
}

// Compact 压缩记忆
func (m *Memory) Compact(targetSize int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.observations) <= targetSize {
		return
	}

	// 保留最近的观察
	keep := len(m.observations) - targetSize
	m.observations = m.observations[keep:]
}

// Export 导出记忆为 JSON
func (m *Memory) Export() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data := struct {
		Observations []types.Observation `json:"observations"`
		Sessions     map[string]*Session `json:"sessions"`
	}{
		Observations: m.observations,
		Sessions:     m.sessions,
	}

	return json.MarshalIndent(data, "", "  ")
}

// Import 从导入记忆
func (m *Memory) Import(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var imported struct {
		Observations []types.Observation `json:"observations"`
		Sessions     map[string]*Session `json:"sessions"`
	}

	if err := json.Unmarshal(data, &imported); err != nil {
		return err
	}

	m.observations = imported.Observations
	m.sessions = imported.Sessions

	return nil
}

// RemoveOld 移除旧的观察（早于指定时间）
func (m *Memory) RemoveOld(before time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	newObs := make([]types.Observation, 0)

	for _, obs := range m.observations {
		if obs.Timestamp.Before(before) {
			removed++
		} else {
			newObs = append(newObs, obs)
		}
	}

	m.observations = newObs
	return removed
}

// GetObservationsByTimeRange 按时间范围获取观察
func (m *Memory) GetObservationsByTimeRange(start, end time.Time) []types.Observation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []types.Observation
	for _, obs := range m.observations {
		if (obs.Timestamp.After(start) || obs.Timestamp.Equal(start)) &&
			(obs.Timestamp.Before(end) || obs.Timestamp.Equal(end)) {
			results = append(results, obs)
		}
	}

	return results
}
