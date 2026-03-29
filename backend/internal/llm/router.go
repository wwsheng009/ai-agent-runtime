package llm

import (
	"sort"
)

// ModelRouter LLM 模型路由器
type ModelRouter struct {
	rules []*RoutingRule
}

// NewModelRouter 创建模型路由器
func NewModelRouter() *ModelRouter {
	return &ModelRouter{
		rules: make([]*RoutingRule, 0),
	}
}

// AddRule 添加路由规则
func (r *ModelRouter) AddRule(rule *RoutingRule) {
	if rule == nil {
		return
	}

	r.rules = append(r.rules, rule)

	// 按优先级排序（降序）
	r.sortRules()
}

// AddRules 批量添加路由规则
func (r *ModelRouter) AddRules(rules []*RoutingRule) {
	for _, rule := range rules {
		if rule != nil {
			r.rules = append(r.rules, rule)
		}
	}

	r.sortRules()
}

// RemoveRule 移除指定模型的路由规则
func (r *ModelRouter) RemoveRule(model string) {
	var filtered []*RoutingRule
	for _, rule := range r.rules {
		if rule.Model != model {
			filtered = append(filtered, rule)
		}
	}
	r.rules = filtered
}

// Route 根据请求条件路由到合适的模型
func (r *ModelRouter) Route(req *LLMRequest) string {
	if req == nil || len(r.rules) == 0 {
		return ""
	}

	// 按优先级顺序检查规则
	for _, rule := range r.rules {
		if rule.Condition != nil && rule.Condition.Match(req) {
			return rule.Model
		}
	}

	return ""
}

// RouteByCondition 根据自定义条件路由
func (r *ModelRouter) RouteByCondition(condition func(*LLMRequest) bool) string {
	if len(r.rules) == 0 || condition == nil {
		return ""
	}

	for _, rule := range r.rules {
		if condition(&LLMRequest{Model: rule.Model}) {
			return rule.Model
		}
	}

	return ""
}

// GetRules 获取所有路由规则
func (r *ModelRouter) GetRules() []*RoutingRule {
	rules := make([]*RoutingRule, len(r.rules))
	copy(rules, r.rules)
	return rules
}

// GetRuleForModel 获取指定模型的路由规则
func (r *ModelRouter) GetRuleForModel(model string) *RoutingRule {
	for _, rule := range r.rules {
		if rule.Model == model {
			return rule
		}
	}
	return nil
}

// ClearRules 清空所有路由规则
func (r *ModelRouter) ClearRules() {
	r.rules = make([]*RoutingRule, 0)
}

// Count 返回路由规则数量
func (r *ModelRouter) Count() int {
	return len(r.rules)
}

// sortRules 按优先级排序规则
func (r *ModelRouter) sortRules() {
	sort.SliceStable(r.rules, func(i, j int) bool {
		return r.rules[i].Priority > r.rules[j].Priority
	})
}

// TokenBudgetCondition 基于 Token 预算的路由条件
type TokenBudgetCondition struct {
	MaxTokens int
}

func (c *TokenBudgetCondition) Match(req *LLMRequest) bool {
	if req == nil || req.MaxTokens <= 0 {
		return true
	}

	return req.MaxTokens <= c.MaxTokens
}

// TaskTypeCondition 基于任务类型的路由条件
type TaskTypeCondition struct {
	TaskType string
}

func (c *TaskTypeCondition) Match(req *LLMRequest) bool {
	if req == nil || req.Metadata == nil {
		return false
	}

	taskType, ok := req.Metadata["task_type"].(string)
	if !ok || taskType == "" {
		return false
	}

	return taskType == c.TaskType
}

// ModelCondition 基于模型名称的路由条件
type ModelCondition struct {
	ModelName string
}

func (c *ModelCondition) Match(req *LLMRequest) bool {
	if req == nil || req.Model == "" {
		return false
	}

	return req.Model == c.ModelName
}

// ToolRequirementCondition 基于工具需求的条件
type ToolRequirementCondition struct {
	RequiresTools bool
}

func (c *ToolRequirementCondition) Match(req *LLMRequest) bool {
	if req == nil {
		return false
	}

	hasTools := len(req.Tools) > 0

	if c.RequiresTools {
		return hasTools
	}

	return !hasTools
}

// StreamingRequirementCondition 基于流式需求的条件
type StreamingRequirementCondition struct {
	RequiresStreaming bool
}

func (c *StreamingRequirementCondition) Match(req *LLMRequest) bool {
	if req == nil {
		return false
	}

	if c.RequiresStreaming {
		return req.Stream
	}

	return !req.Stream
}

// CompositeCondition 组合条件（AND 逻辑）
type CompositeCondition struct {
	Conditions []RoutingCondition
}

func (c *CompositeCondition) Match(req *LLMRequest) bool {
	if len(c.Conditions) == 0 {
		return true
	}

	for _, condition := range c.Conditions {
		if !condition.Match(req) {
			return false
		}
	}

	return true
}

// OrCondition 或条件（OR 逻辑）
type OrCondition struct {
	Conditions []RoutingCondition
}

func (c *OrCondition) Match(req *LLMRequest) bool {
	if len(c.Conditions) == 0 {
		return true
	}

	for _, condition := range c.Conditions {
		if condition.Match(req) {
			return true
		}
	}

	return false
}
