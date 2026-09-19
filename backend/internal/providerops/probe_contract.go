package providerops

import (
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ---------------------------------------------------------------------------
// 模型探测契约入口：runtime server `POST /api/runtime/providers/probe-models`
// 的冻结 API。实现复用 probe.go 的探测矩阵（与 CLI/web client 同源）。
// ---------------------------------------------------------------------------

// ProbeRequest 是一次跨协议探测请求。
type ProbeRequest struct {
	Config         *config.Config
	ProviderName   string
	Provider       config.Provider
	LoginProtocol  string
	Models         []ModelInfo
	Protocols      []string
	Timeout        time.Duration
	MaxConcurrency int
}

// ProbeResult 是单个模型 × 协议探测的扁平结论（契约词表：
// ok=实测支持；unsupported=网关明确拒绝；error=无法定性（鉴权/限流/区域/
// 网络等传输或环境错误）；invalid 预留给请求构造失败，当前内部分类不会产生）。
type ProbeResult struct {
	ModelID    string `json:"model_id"`
	Protocol   string `json:"protocol"`
	Verdict    string `json:"verdict"`
	Message    string `json:"message,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// ProbeModels 并发探测 models × protocols 矩阵，返回扁平结论列表。protocols
// 为空时回退到 provider 自身协议（LoginProtocol）。
func ProbeModels(req ProbeRequest) []ProbeResult {
	models := make([]string, 0, len(req.Models))
	for _, model := range req.Models {
		if id := strings.TrimSpace(model.ID); id != "" {
			models = append(models, id)
		}
	}
	protocols := make([]string, 0, len(req.Protocols))
	for _, protocol := range req.Protocols {
		if trimmed := strings.TrimSpace(protocol); trimmed != "" {
			protocols = append(protocols, trimmed)
		}
	}
	if len(protocols) == 0 {
		if fallback := RuntimeProtocolForLoginProtocol(req.LoginProtocol); fallback != "" {
			protocols = []string{fallback}
		}
	}
	if len(models) == 0 || len(protocols) == 0 {
		return nil
	}
	matrix := ProbeMatrix(ProbeMatrixRequest{
		Config:        req.Config,
		Provider:      req.Provider,
		Models:        models,
		Protocols:     protocols,
		Timeout:       req.Timeout,
		MaxConcurrent: req.MaxConcurrency,
	})
	out := make([]ProbeResult, 0, len(matrix)*len(protocols))
	for _, entry := range matrix {
		for _, probe := range entry.Probes {
			out = append(out, ProbeResult{
				ModelID:    entry.Model,
				Protocol:   probe.Protocol,
				Verdict:    ProviderProbeContractVerdict(probe.Verdict),
				Message:    probe.Detail,
				StatusCode: probe.HTTPStatus,
				DurationMS: probe.DurationMS,
			})
		}
	}
	return out
}

// ProviderProbeContractVerdict 把内部三值判定（supported/unsupported/unknown）
// 映射为契约词表（ok/unsupported/error）。
func ProviderProbeContractVerdict(verdict string) string {
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case string(probeVerdictSupported):
		return "ok"
	case string(probeVerdictUnsupported):
		return "unsupported"
	default:
		return "error"
	}
}
