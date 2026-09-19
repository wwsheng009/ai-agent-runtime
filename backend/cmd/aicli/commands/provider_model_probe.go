package commands

import (
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
)

// ---------------------------------------------------------------------------
// 模型协议探测（commands 薄转发层）。
//
// 实现本体已迁移至 internal/providerops（probe.go + probe_contract.go），
// 与 runtime server 的 /api/runtime/providers/probe-models 共用同一份逻辑；
// 本文件只保留既有调用点（web_config_handlers / provider model-probe 命令）
// 与单元测试使用的类型别名和转发函数，不再持有任何探测实现。
//
// 判定采用错误分类学：
//   - HTTP 200 + 可识别的补全结构            → supported（强正向证据）
//   - 网关明确拒绝该模型/格式（401/404/400 +  → unsupported（强负向证据）
//     "not supported for format" 等措辞）
//   - 鉴权 / 配额 / 区域 / 限流 / 参数错误 /  → unknown（不能下负向结论，
//     网络错误 / 无法识别的 200 响应             避免把区域限制误判成不支持）
// ---------------------------------------------------------------------------

type providerModelProbeVerdict = providerops.ProbeVerdict

const (
	probeVerdictSupported   = providerops.ProbeVerdictSupported
	probeVerdictUnsupported = providerops.ProbeVerdictUnsupported
	probeVerdictUnknown     = providerops.ProbeVerdictUnknown
)

type providerModelSingleProbe = providerops.ModelSingleProbe

type providerModelProbeResult = providerops.ModelProbeMatrix

type providerModelProbeRequest = providerops.ProbeMatrixRequest

func classifyProviderModelProbeMessage(message string) providerModelProbeVerdict {
	return providerops.ClassifyProviderModelProbeMessage(message)
}

func classifyProviderModelProbeResponse(statusCode int, body []byte, protocol string) (providerModelProbeVerdict, string) {
	return providerops.ClassifyProviderModelProbeResponse(statusCode, body, protocol)
}

func probeResponseLooksSuccessful(protocol string, payload map[string]interface{}) bool {
	return providerops.ProbeResponseLooksSuccessful(protocol, payload)
}

func providerProbeSessionID() string { return providerops.ProviderProbeSessionID() }

func providerProbeBaseURLHost(baseURL string) string {
	return providerops.ProviderProbeBaseURLHost(baseURL)
}

func providerProbeCardID(providerName, host, modelID, protocol string) string {
	return providerops.ProviderProbeCardID(providerName, host, modelID, protocol)
}

func runProviderModelProbes(req providerModelProbeRequest) []providerModelProbeResult {
	return providerops.ProbeMatrix(req)
}

func appendProviderModelProbeCards(
	cfg *config.Config,
	providerName string,
	provider config.Provider,
	results []providerModelProbeResult,
) (written []string, cardsPath string, err error) {
	return providerops.AppendProviderModelProbeCards(cfg, providerName, provider, results)
}
