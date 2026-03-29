package types

import "time"

// Observation 执行观察记录
type Observation struct {
	Step       string                 `json:"step" yaml:"step"`
	Tool       string                 `json:"tool" yaml:"tool"`
	Input      interface{}            `json:"input" yaml:"input"`
	Output     interface{}            `json:"output" yaml:"output"`
	Success    bool                   `json:"success" yaml:"success"`
	Error      string                 `json:"error,omitempty" yaml:"error,omitempty"`
	Metrics    map[string]interface{} `json:"metrics,omitempty" yaml:"metrics,omitempty"`
	Timestamp  time.Time              `json:"timestamp" yaml:"timestamp"`
	Duration   Duration               `json:"duration" yaml:"duration"`
}

// NewObservation 创建新的观察记录
func NewObservation(step, tool string) *Observation {
	now := time.Now()
	return &Observation{
		Step:      step,
		Tool:      tool,
		Success:   false,
		Timestamp: now,
		Duration: Duration{
			Start: now,
		},
		Metrics: make(map[string]interface{}),
	}
}

// WithInput 设置输入
func (o *Observation) WithInput(input interface{}) *Observation {
	o.Input = input
	return o
}

// WithOutput 设置输出
func (o *Observation) WithOutput(output interface{}) *Observation {
	o.Output = output
	return o
}

// MarkSuccess 标记成功
func (o *Observation) MarkSuccess() *Observation {
	o.Success = true
	o.Duration.StopTimer()
	return o
}

// MarkFailure 标记失败
func (o *Observation) MarkFailure(err string) *Observation {
	o.Success = false
	o.Error = err
	o.Duration.StopTimer()
	return o
}

// WithMetric 添加指标
func (o *Observation) WithMetric(key string, value interface{}) *Observation {
	if o.Metrics == nil {
		o.Metrics = make(map[string]interface{})
	}
	o.Metrics[key] = value
	return o
}

// GetMetric 获取指标
func (o *Observation) GetMetric(key string) (interface{}, bool) {
	if o.Metrics == nil {
		return nil, false
	}
	val, exists := o.Metrics[key]
	return val, exists
}

// Clone 克隆观察记录
func (o *Observation) Clone() *Observation {
	if o == nil {
		return nil
	}

	clone := &Observation{
		Step:      o.Step,
		Tool:      o.Tool,
		Input:     o.Input,
		Output:    o.Output,
		Success:   o.Success,
		Error:     o.Error,
		Timestamp: o.Timestamp,
		Duration: Duration{
			Start: o.Duration.Start,
			End:   o.Duration.End,
		},
	}

	// 克隆 Metrics
	if o.Metrics != nil {
		clone.Metrics = make(map[string]interface{}, len(o.Metrics))
		for k, v := range o.Metrics {
			clone.Metrics[k] = v
		}
	}

	return clone
}

// Snapshot 创建当前状态的快照
func (o *Observation) Snapshot() *Observation {
	clone := o.Clone()
	clone.Duration.End = time.Now()
	return clone
}
