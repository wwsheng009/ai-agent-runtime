package types

import "time"

// Duration 持续时间
type Duration struct {
	Start time.Time `json:"start" yaml:"start"`
	End   time.Time `json:"end" yaml:"end"`
}

// GetDuration 获取持续时间
func (d *Duration) GetDuration() time.Duration {
	if d.Start.IsZero() || d.End.IsZero() {
		return 0
	}
	return d.End.Sub(d.Start)
}

// IsZero 检查是否为零值
func (d *Duration) IsZero() bool {
	if d == nil {
		return true
	}
	return d.Start.IsZero() && d.End.IsZero()
}

// StartTimer 开始计时
func (d *Duration) StartTimer() {
	d.Start = time.Now()
}

// StopTimer 停止计时
func (d *Duration) StopTimer() {
	d.End = time.Now()
}

// NewDuration 创建新的持续时间
func NewDuration() *Duration {
	return &Duration{
		Start: time.Now(),
	}
}

// Metadata 元数据
type Metadata map[string]interface{}

// NewMetadata 创建新的元数据
func NewMetadata() Metadata {
	return make(Metadata)
}

// Get 获取元数据值
func (m Metadata) Get(key string) (interface{}, bool) {
	val, exists := m[key]
	return val, exists
}

// Set 设置元数据值
func (m Metadata) Set(key string, value interface{}) {
	m[key] = value
}

// GetString 获取字符串值
func (m Metadata) GetString(key string, defaultValue string) string {
	if val, exists := m[key]; exists {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return defaultValue
}

// GetInt 获取整数值
func (m Metadata) GetInt(key string, defaultValue int) int {
	if val, exists := m[key]; exists {
		if num, ok := val.(int); ok {
			return num
		}
	}
	return defaultValue
}

// GetBool 获取布尔值
func (m Metadata) GetBool(key string, defaultValue bool) bool {
	if val, exists := m[key]; exists {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return defaultValue
}

// Merge 合并另一个元数据
func (m Metadata) Merge(other Metadata) Metadata {
	result := NewMetadata()
	for k, v := range m {
		result[k] = v
	}
	for k, v := range other {
		result[k] = v
	}
	return result
}

// Clone 克隆元数据
func (m Metadata) Clone() Metadata {
	if m == nil {
		return NewMetadata()
	}
	result := make(Metadata, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// With 创建带新键值对的副本
func (m Metadata) With(key string, value interface{}) Metadata {
	result := m.Clone()
	result[key] = value
	return result
}
