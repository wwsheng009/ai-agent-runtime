package llm

// ResourceManager 抽象接口，由宿主（如 ai-gateway）实现并注入。
// 解耦 llm 包对 gateway/loadbalancer 具体实现的直接依赖。
type ResourceManager interface {
	// SelectResource 根据重试信息选择一个 provider 资源
	SelectResource(retryInfo RetryInfo) (*SelectedResource, error)
	// RecordResult 记录本次调用结果（用于健康追踪和负载均衡反馈）
	RecordResult(selected *SelectedResource, success bool, err error, statusCode int, latencyMs int64)
}

// SelectedResource 选中的 provider 资源
type SelectedResource struct {
	GroupName string            // 组名称
	Provider  *ProviderResource // Provider 信息
	Key       *KeyResource      // Key 资源
	KeyValue  string            // API Key 值
	KeyID     string            // Key ID（用于追踪）
	Model     string            // 请求模型
}

// KeyResource API Key 资源信息
type KeyResource struct {
	ID          string            // Key ID（脱敏后）
	OriginalKey string            // 原始 Key
	Weight      int               // 权重
	Provider    *ProviderResource // 所属 Provider
}

// ProviderResource Provider 资源信息
type ProviderResource struct {
	GroupName string
	Name      string
	Type      string
	BaseURL   string
	Weight    int
	Enabled   bool
	Config    interface{} // 原始配置（用于模型映射等）
}

// RetryInfo 重试上下文信息
type RetryInfo struct {
	TargetGroup      string              // 目标组名称
	Attempt          int                 // 当前尝试次数
	MaxAttempts      int                 // 最大尝试次数
	RequestedModel   string              // 请求模型
	TriedGroups      []string            // 已尝试的组
	TriedProviders   []string            // 已尝试的供应商
	TriedAPIKeys     map[string][]string // provider -> keyIDs
	UseEnhancedRetry bool                // 是否使用增强重试策略
}
