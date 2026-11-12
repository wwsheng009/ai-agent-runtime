package providerops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

// Automatic login discovery is deliberately bounded: it can incur real usage.
// Explicit model cards and image models never require these completion probes.
const (
	loginProbeMaxModels = 20
	loginProbeBudget    = 30 * time.Second
	loginProbeTimeout   = 15 * time.Second
)

type LoginProbeRequest struct {
	Config        *config.Config
	ProviderName  string
	LoginProtocol string // protocol used to fetch /models, not "auto"
	Provider      config.Provider
	Models        []ModelInfo
	Catalog       *modelcard.Catalog
	DefaultModel  string
	Timeout       time.Duration
}

type LoginProbeResult struct {
	Catalog  *modelcard.Catalog
	Models   []ModelInfo
	Probes   []ModelProbeMatrix
	Warnings []string
}

// ProbeUnclassifiedLoginModels is the API-key auto-login discovery step. It
// supplements protocol recommendations in memory, without writing model cards
// (including during dry-run). Capability-only cards do not prove a protocol.
// Callers must not invoke this for explicit protocols or OAuth credentials.
func ProbeUnclassifiedLoginModels(ctx context.Context, req LoginProbeRequest) (LoginProbeResult, error) {
	result := LoginProbeResult{Catalog: req.Catalog, Models: req.Models}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	catalog := req.Catalog
	if catalog == nil {
		// --no-model-cards disables capability enrichment, not protocol discovery.
		// Only retain templates; do not silently re-enable builtin model cards.
		builtin, _, err := modelcard.LoadSources([]modelcard.Source{modelcard.BuiltinSource()}, true)
		if err != nil {
			return result, err
		}
		catalog = &modelcard.Catalog{Version: 1, ProviderTemplates: builtin.ProviderTemplates}
	}
	currentProtocol := RuntimeProtocolForLoginProtocol(req.LoginProtocol)
	template, _ := ResolveProviderTemplate(catalog, currentProtocol, req.Provider)
	matchContext := modelcard.Context{
		ProviderName: req.ProviderName, LoginProtocol: req.LoginProtocol,
		RuntimeProtocol: currentProtocol, BaseURL: req.Provider.BaseURL, ProviderTemplate: template.ID,
	}
	targets := loginProbeTargets(catalog, matchContext, req.Models, req.DefaultModel)
	if len(targets) == 0 {
		return result, nil
	}
	if len(targets) > loginProbeMaxModels {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"自动协议探测最多处理 %d 个无协议映射的模型；其余 %d 个仍按原规则分类，未验证协议。",
			loginProbeMaxModels, len(targets)-loginProbeMaxModels))
		targets = targets[:loginProbeMaxModels]
	}
	budget := loginProbeBudget
	if req.Timeout > 0 && req.Timeout < budget {
		budget = req.Timeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	provider := req.Provider
	if strings.TrimSpace(provider.Protocol) == "" || IsAutoLoginProtocol(provider.Protocol) {
		provider.Protocol = currentProtocol
	}
	result.Probes = ProbeMatrix(ProbeMatrixRequest{
		Context: probeCtx, Config: req.Config, Provider: provider, Models: targets,
		Protocols: loginProbeProtocols(currentProtocol), Timeout: min(loginProbeTimeout, budget),
	})
	// An explicit caller cancellation must stop login before credentials/config
	// are saved. Our own bounded discovery timeout is merely inconclusive.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Catalog, result.Models, result.Warnings = applyLoginProbeResults(
		catalog, req.Models, result.Probes, currentProtocol, result.Warnings)
	return result, nil
}

func loginProbeTargets(catalog *modelcard.Catalog, ctx modelcard.Context, models []ModelInfo, preferred string) []string {
	targets := make([]string, 0)
	seen := make(map[string]bool)
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		key := strings.ToLower(id)
		if id == "" || seen[key] || providerLoginModelIDContainsImageKeyword(id) {
			continue
		}
		seen[key] = true
		if providerLoginRecommendationsHaveNonFallback(catalog.RecommendedProviderTemplates(ctx, id)) {
			continue
		}
		targets = append(targets, id)
	}
	// An explicitly requested default must not fall outside the discovery cap.
	for i, id := range targets {
		if strings.EqualFold(id, strings.TrimSpace(preferred)) {
			copy(targets[1:i+1], targets[:i])
			targets[0] = id
			break
		}
	}
	return targets
}

func loginProbeProtocols(current string) []string {
	protocols := []string{"openai", "codex", "anthropic"}
	if current == "gemini" {
		protocols = append(protocols, "gemini")
	}
	return protocols
}

func applyLoginProbeResults(catalog *modelcard.Catalog, models []ModelInfo, probes []ModelProbeMatrix, fallbackProtocol string, warnings []string) (*modelcard.Catalog, []ModelInfo, []string) {
	// Never mutate the shared/loaded catalog or persist these request-scoped
	// observations. The normal login persistence saves the resulting providers.
	enriched := *catalog
	enriched.Cards = append([]modelcard.Card(nil), catalog.Cards...)
	rejected := make(map[string]bool)
	for _, result := range probes {
		supported, fallbackRejected := false, false
		for _, probe := range result.Probes {
			if probe.Protocol == fallbackProtocol && probe.Verdict == string(probeVerdictUnsupported) {
				fallbackRejected = true
			}
			if probe.Verdict != string(probeVerdictSupported) {
				continue
			}
			template, ok := catalog.ProviderTemplateForProtocol(probe.Protocol)
			if !ok {
				continue
			}
			supported = true
			enriched.Cards = append(enriched.Cards, modelcard.Card{
				ID:       "login-probe." + result.Model + "." + probe.Protocol,
				Priority: providerProbeCardPriority, ProviderTemplate: template.ID,
				Match: modelcard.MatchSpec{
					// ModelIDs intentionally allow fuzzy namespace aliases.
					// A probe is evidence for this exact gateway ID only.
					ModelPatterns: []string{"^" + regexp.QuoteMeta(result.Model) + "$"},
				},
			})
		}
		if supported {
			continue
		}
		if fallbackRejected {
			rejected[strings.ToLower(strings.TrimSpace(result.Model))] = true
			warnings = append(warnings, fmt.Sprintf(
				"模型 %q 明确拒绝 %s 协议，且其他协议未获成功；未自动导入，请显式确认协议。",
				result.Model, fallbackProtocol))
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"模型 %q 的协议探测未确认支持（可能为鉴权、限流、超时或响应不兼容）；暂沿用 %s 分类，这不是协议验证通过。",
				result.Model, fallbackProtocol))
		}
	}
	retained := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		if !rejected[strings.ToLower(strings.TrimSpace(model.ID))] {
			retained = append(retained, model)
		}
	}
	return &enriched, retained, warnings
}
