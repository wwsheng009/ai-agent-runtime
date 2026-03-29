package types

import "time"

// Request 统一请求
type Request struct {
	Prompt          string                 `json:"prompt" yaml:"prompt"`
	Context         map[string]interface{} `json:"context" yaml:"context"`
	History         []Message              `json:"history" yaml:"history"`
	Metadata        Metadata               `json:"metadata" yaml:"metadata"`
	Options         map[string]interface{} `json:"options" yaml:"options"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`
	Thinking        *ThinkingConfig        `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Duration        Duration               `json:"duration" yaml:"duration"`
}

// Result 统一结果
type Result struct {
	Success      bool           `json:"success" yaml:"success"`
	Output       string         `json:"output" yaml:"output"`
	Skill        string         `json:"skill,omitempty" yaml:"skill,omitempty"`
	Observations []Observation  `json:"observations,omitempty" yaml:"observations,omitempty"`
	Usage        *TokenUsage    `json:"usage,omitempty" yaml:"usage,omitempty"`
	Duration     Duration       `json:"duration" yaml:"duration"`
	Error        string         `json:"error,omitempty" yaml:"error,omitempty"`
}

// Clone 克隆请求
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}

	clone := &Request{
		Prompt:          r.Prompt,
		Metadata:        r.Metadata.Clone(),
		ReasoningEffort: r.ReasoningEffort,
		Thinking:        CloneThinkingConfig(r.Thinking),
		Duration:        Duration{},
	}

	// 克隆 Context
	if len(r.Context) > 0 {
		clone.Context = make(map[string]interface{}, len(r.Context))
		for k, v := range r.Context {
			clone.Context[k] = v
		}
	}

	// 克隆 History
	if len(r.History) > 0 {
		clone.History = make([]Message, len(r.History))
		for i, msg := range r.History {
			clone.History[i] = *msg.Clone()
		}
	}

	// 克隆 Options
	if len(r.Options) > 0 {
		clone.Options = make(map[string]interface{}, len(r.Options))
		for k, v := range r.Options {
			clone.Options[k] = v
		}
	}

	return clone
}

// GetContextValue 获取上下文值
func (r *Request) GetContextValue(key string, defaultValue interface{}) interface{} {
	if r.Context == nil {
		return defaultValue
	}
	val, exists := r.Context[key]
	if !exists {
		return defaultValue
	}
	return val
}

// AddToHistory 添加消息到历史
func (r *Request) AddToHistory(msg Message) {
	r.History = append(r.History, msg)
}

// GetRecentHistory 获取最近的 n 条历史消息
func (r *Request) GetRecentHistory(n int) []Message {
	if len(r.History) <= n {
		return r.History
	}
	return r.History[len(r.History)-n:]
}

// NewRequest 创建新请求
func NewRequest(prompt string) *Request {
	return &Request{
		Prompt:   prompt,
		Context:  make(map[string]interface{}),
		History:  make([]Message, 0),
		Metadata: NewMetadata(),
		Options:  make(map[string]interface{}),
		Duration: Duration{
			Start: time.Now(),
		},
	}
}

// MarkCompleted 标记请求完成
func (r *Request) MarkCompleted() {
	r.Duration.StopTimer()
}

// MarkFailed 标记请求失败并返回错误结果
func (r *Request) MarkFailed(errStr string) *Result {
	r.MarkCompleted()
	return &Result{
		Success:  false,
		Output:   "",
		Error:    errStr,
		Duration: r.Duration,
	}
}

// MarkSuccess 标记请求成功并返回结果
func (r *Request) MarkSuccess(output string) *Result {
	r.MarkCompleted()
	return &Result{
		Success:  true,
		Output:   output,
		Duration: r.Duration,
	}
}

// NewResult 创建结果
func NewResult(success bool, output string) *Result {
	return &Result{
		Success:  success,
		Output:   output,
		Duration: Duration{
			Start: time.Now(),
			End:   time.Now(),
		},
	}
}

// WithSkill 设置技能名称
func (r *Result) WithSkill(skillName string) *Result {
	r.Skill = skillName
	return r
}

// WithUsage 设置 Token 使用量
func (r *Result) WithUsage(usage *TokenUsage) *Result {
	r.Usage = usage
	return r
}

// WithObservations 添加观察记录
func (r *Result) WithObservations(obs []Observation) *Result {
	r.Observations = obs
	return r
}

// WithError 设置错误信息
func (r *Result) WithError(err string) *Result {
	r.Error = err
	return r
}

// AddObservation 添加单个观察记录
func (r *Result) AddObservation(obs Observation) {
	r.Observations = append(r.Observations, obs)
}
