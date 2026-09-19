package providerops

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

// ---------------------------------------------------------------------------
// login 协议语义：登录协议（含 codex-apikey/codex-oauth/openai_image 细分）
// 与运行时协议（openai/anthropic/gemini/codex/openai_image）的归一化，是
// 分类 / 探测 / 元数据匹配共用的入口语义。与 commands 侧实现保持行为等价。
// ---------------------------------------------------------------------------

const (
	// LoginProtocolAuto 表示“自动探测协议”。
	LoginProtocolAuto = "auto"
	// LoginProtocolOpenAIImage 是图像生成的独立协议分支。
	LoginProtocolOpenAIImage = "openai_image"

	authModeAPIKey = "api_key"
	authModeOAuth  = "oauth"
)

// IsAutoLoginProtocol 判断协议是否为 auto（自动探测）。
func IsAutoLoginProtocol(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), LoginProtocolAuto)
}

// NormalizeLoginProtocol 把登录协议归一化到规范拼写：
// auto / openai_image（接受 openai-image 别名）/ codex（按鉴权模式展开为
// codex-oauth 或 codex-apikey）/ 其他原样小写返回。
func NormalizeLoginProtocol(protocol, mode string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	mode = NormalizeProviderAuthMode(mode)
	if protocol == LoginProtocolAuto {
		return LoginProtocolAuto
	}
	if protocol == "openai-image" {
		return LoginProtocolOpenAIImage
	}
	if protocol == "codex" {
		if mode == authModeOAuth {
			return "codex-oauth"
		}
		return "codex-apikey"
	}
	if protocol == "codex-api-key" || protocol == "codex-apikey" {
		return "codex-apikey"
	}
	return protocol
}

// NormalizeProviderAuthMode 归一化鉴权模式：空/别名统一为 api_key 或 oauth。
func NormalizeProviderAuthMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "apikey", "api-key", "key":
		return authModeAPIKey
	case "oauth", "chatgpt", "device-code", "device_code":
		return authModeOAuth
	default:
		return mode
	}
}

// RuntimeProtocolForLoginProtocol 把登录协议映射到运行时协议：
// codex-apikey/codex-oauth → codex，auto → openai（默认兜底），其余原样。
func RuntimeProtocolForLoginProtocol(protocol string) string {
	switch NormalizeLoginProtocol(protocol, "") {
	case "codex-apikey", "codex-oauth":
		return "codex"
	case LoginProtocolAuto:
		return "openai"
	default:
		return NormalizeLoginProtocol(protocol, "")
	}
}

// LoginProtocolFromProvider 从已保存 provider 的协议 + 鉴权模式推导登录协议。
func LoginProtocolFromProvider(provider config.Provider, mode string) string {
	protocol := provider.GetProtocol()
	if protocol == "codex" {
		if NormalizeProviderAuthMode(mode) == authModeOAuth || strings.EqualFold(provider.AuthMode, authModeOAuth) {
			return "codex-oauth"
		}
		return "codex-apikey"
	}
	return protocol
}

// NormalizeModelsForProtocol 对 /models 返回的模型清单按登录协议做 ID 归一
// 化（协议特定拼写修正）、回填 display_name 并去重。
func NormalizeModelsForProtocol(models []ModelInfo, loginProtocol string) []ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		model.ID = normalizeProviderModelID(model.ID, loginProtocol)
		if strings.TrimSpace(model.DisplayName) == "" {
			model.DisplayName = model.ID
		}
		if strings.TrimSpace(model.ID) != "" {
			out = append(out, model)
		}
	}
	return dedupeProviderModels(out)
}

// HeaderTemplateProjectID 从当前工作目录推导稳定的 project 标识（sha256 前
// 8 字节 hex），镜像 opencode 的 x-opencode-project：网关可按项目路由/缓存，
// 又不暴露原始文件系统路径。探测与聊天路径共用同一实现。
func HeaderTemplateProjectID() string {
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:8])
}

// ResolveProviderLoginModelCardPath 展开 model cards 路径中的 ~ 与相对路径。
func ResolveProviderLoginModelCardPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, strings.TrimLeft(path[2:], "/\\"))
		}
	}
	return filepath.Clean(path)
}

// resolveProviderLoginModelCardPath 是包内既有点名的兼容包装。
func resolveProviderLoginModelCardPath(path string) string {
	return ResolveProviderLoginModelCardPath(path)
}

// headerTemplateProjectID 是包内既有点名的兼容包装。
func headerTemplateProjectID() string {
	return HeaderTemplateProjectID()
}

// SameProviderLoginTemplateID 按模板 ID（大小写不敏感）比较两个模板。
func SameProviderLoginTemplateID(left, right modelcard.ProviderTemplate) bool {
	return strings.EqualFold(strings.TrimSpace(left.ID), strings.TrimSpace(right.ID))
}

// sameProviderLoginTemplateID 是包内既有点名的兼容包装。
func sameProviderLoginTemplateID(left, right modelcard.ProviderTemplate) bool {
	return SameProviderLoginTemplateID(left, right)
}

// LoginModelGroup 是 login/fetch-models 共用的中间分组结构：与对外 JSON 的
// ModelGroup 不同，ProviderTemplate 保留完整模板（含 APIPath 等字段），
// Models 保留完整 ModelInfo。
type LoginModelGroup struct {
	Key              string
	LoginProtocol    string
	RuntimeProtocol  string
	ProviderTemplate modelcard.ProviderTemplate
	HasTemplate      bool
	Models           []ModelInfo
}

// ModelCardAppliedInfo / ModelCardSkippedInfo 是能力匹配过程中“哪些模型
// 命中了哪些卡 / 哪些模型没有卡可依”的报告 DTO，login 与 fetch-models
// （runtime 编辑器）共用。
type ModelCardAppliedInfo struct {
	Model  string   `json:"model"`
	CardID string   `json:"card_id"`
	Fields []string `json:"fields,omitempty"`
}

type ModelCardSkippedInfo struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

// ---------------------------------------------------------------------------
// 分组与主组选择：与 aicli login 自动导入同源的 model card 推荐分组链路。
// ---------------------------------------------------------------------------

// GroupProviderLoginModelsByProviderTemplate 按 model card 推荐的 provider
// template 把模型清单分组。与 provider 自身协议一致的 fallback 组保证
// “无卡片数据时模型仍跟随当前协议”。
func GroupProviderLoginModelsByProviderTemplate(
	providerName, loginProtocol, requestedLoginProtocol string,
	provider config.Provider,
	models []ModelInfo,
	catalog *modelcard.Catalog,
	authMode string,
) []LoginModelGroup {
	if len(models) == 0 {
		return nil
	}
	runtimeProtocol := RuntimeProtocolForLoginProtocol(loginProtocol)
	currentTemplate, hasCurrentTemplate := ResolveProviderTemplate(catalog, runtimeProtocol, provider)
	ctx := modelcard.Context{
		ProviderName:     providerName,
		LoginProtocol:    loginProtocol,
		RuntimeProtocol:  runtimeProtocol,
		BaseURL:          provider.BaseURL,
		ProviderTemplate: strings.TrimSpace(currentTemplate.ID),
	}
	groups := make([]LoginModelGroup, 0)
	indexByKey := make(map[string]int)
	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		recommendations := catalog.RecommendedProviderTemplates(ctx, modelID)
		if imageTemplate, ok := providerLoginAutoImageModelTemplate(catalog, requestedLoginProtocol, modelID, recommendations); ok {
			recommendations = []modelcard.RecommendedProviderTemplateMatch{{
				Template: imageTemplate,
			}}
		}
		if len(recommendations) == 0 && hasCurrentTemplate {
			recommendations = []modelcard.RecommendedProviderTemplateMatch{{
				Template: currentTemplate,
			}}
		}
		if len(recommendations) == 0 {
			recommendations = []modelcard.RecommendedProviderTemplateMatch{{}}
		}
		for _, recommendation := range recommendations {
			template := recommendation.Template
			hasTemplate := strings.TrimSpace(template.ID) != "" || strings.TrimSpace(template.Protocol) != ""
			groupLoginProtocol := LoginProtocolForProviderTemplate(template, hasTemplate, loginProtocol, authMode)
			groupRuntimeProtocol := RuntimeProtocolForLoginProtocol(groupLoginProtocol)
			groupKey := providerLoginModelGroupKey(groupRuntimeProtocol, template, hasTemplate)
			groupModel := model
			groupModel.ID = normalizeProviderModelID(groupModel.ID, groupLoginProtocol)
			if strings.TrimSpace(groupModel.ID) == "" {
				continue
			}
			index, ok := indexByKey[groupKey]
			if !ok {
				index = len(groups)
				indexByKey[groupKey] = index
				groups = append(groups, LoginModelGroup{
					Key:              groupKey,
					LoginProtocol:    groupLoginProtocol,
					RuntimeProtocol:  groupRuntimeProtocol,
					ProviderTemplate: template,
					HasTemplate:      hasTemplate,
				})
			}
			groups[index].Models = append(groups[index].Models, groupModel)
		}
	}
	for i := range groups {
		groups[i].Models = dedupeProviderModels(groups[i].Models)
	}
	return groups
}

// groupProviderLoginModelsByProviderTemplate 是包内既有点名的兼容包装。
func groupProviderLoginModelsByProviderTemplate(
	providerName, loginProtocol, requestedLoginProtocol string,
	provider config.Provider,
	models []ModelInfo,
	catalog *modelcard.Catalog,
	authMode string,
) []LoginModelGroup {
	return GroupProviderLoginModelsByProviderTemplate(providerName, loginProtocol, requestedLoginProtocol, provider, models, catalog, authMode)
}

// EnsureExplicitProviderLoginModelGroup 当用户显式指定了非 auto、非 openai
// 的协议时，保证存在该协议的显式分组（丢弃仅靠 fallback 的猜测分组），
// 避免显式选择 anthropic 却仍按 openai 分组。
func EnsureExplicitProviderLoginModelGroup(
	requestedLoginProtocol string,
	provider config.Provider,
	models []ModelInfo,
	groups []LoginModelGroup,
	catalog *modelcard.Catalog,
	authMode string,
) []LoginModelGroup {
	if IsAutoLoginProtocol(requestedLoginProtocol) || len(models) == 0 {
		return groups
	}
	runtimeProtocol := RuntimeProtocolForLoginProtocol(requestedLoginProtocol)
	if strings.EqualFold(runtimeProtocol, "openai") {
		return groups
	}
	template, hasTemplate := ResolveProviderTemplate(catalog, runtimeProtocol, provider)
	if providerLoginModelGroupsContainExplicitProtocol(groups, runtimeProtocol, template, hasTemplate) {
		return groups
	}
	loginProtocol := LoginProtocolForProviderTemplate(template, hasTemplate, requestedLoginProtocol, authMode)
	groupRuntimeProtocol := RuntimeProtocolForLoginProtocol(loginProtocol)
	group := LoginModelGroup{
		Key:              providerLoginModelGroupKey(groupRuntimeProtocol, template, hasTemplate),
		LoginProtocol:    loginProtocol,
		RuntimeProtocol:  groupRuntimeProtocol,
		ProviderTemplate: template,
		HasTemplate:      hasTemplate,
	}
	for _, model := range models {
		groupModel := model
		groupModel.ID = normalizeProviderModelID(groupModel.ID, loginProtocol)
		if strings.TrimSpace(groupModel.DisplayName) == "" {
			groupModel.DisplayName = groupModel.ID
		}
		if strings.TrimSpace(groupModel.ID) != "" {
			group.Models = append(group.Models, groupModel)
		}
	}
	group.Models = dedupeProviderModels(group.Models)
	if len(group.Models) == 0 {
		return groups
	}
	return []LoginModelGroup{group}
}

// ensureExplicitProviderLoginModelGroup 是包内既有点名的兼容包装。
func ensureExplicitProviderLoginModelGroup(
	requestedLoginProtocol string,
	provider config.Provider,
	models []ModelInfo,
	groups []LoginModelGroup,
	catalog *modelcard.Catalog,
	authMode string,
) []LoginModelGroup {
	return EnsureExplicitProviderLoginModelGroup(requestedLoginProtocol, provider, models, groups, catalog, authMode)
}

func providerLoginModelGroupsContainExplicitProtocol(groups []LoginModelGroup, runtimeProtocol string, template modelcard.ProviderTemplate, hasTemplate bool) bool {
	for _, group := range groups {
		if hasTemplate && group.HasTemplate && sameProviderLoginTemplateID(group.ProviderTemplate, template) {
			return true
		}
		if strings.EqualFold(group.RuntimeProtocol, runtimeProtocol) {
			return true
		}
	}
	return false
}

func providerLoginAutoImageModelTemplate(catalog *modelcard.Catalog, requestedLoginProtocol, modelID string, recommendations []modelcard.RecommendedProviderTemplateMatch) (modelcard.ProviderTemplate, bool) {
	if !IsAutoLoginProtocol(requestedLoginProtocol) || !providerLoginModelIDContainsImageKeyword(modelID) {
		return modelcard.ProviderTemplate{}, false
	}
	if providerLoginRecommendationsHaveNonFallback(recommendations) {
		return modelcard.ProviderTemplate{}, false
	}
	return catalog.ProviderTemplateForProtocol(LoginProtocolOpenAIImage)
}

func providerLoginModelIDContainsImageKeyword(modelID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(modelID)), "image")
}

func providerLoginRecommendationsHaveNonFallback(recommendations []modelcard.RecommendedProviderTemplateMatch) bool {
	for _, recommendation := range recommendations {
		if len(recommendation.Applied) == 0 {
			// Template-only recommendation without applied cards is treated as non-fallback.
			if strings.TrimSpace(recommendation.Template.ID) != "" || strings.TrimSpace(recommendation.Template.Protocol) != "" {
				return true
			}
			continue
		}
		for _, item := range recommendation.Applied {
			if !item.Fallback {
				return true
			}
		}
	}
	return false
}

// LoginProtocolForProviderTemplate 决定分组使用的登录协议：模板自带协议时
// 展开 codex 的 oauth/apikey 细分；否则回退到当前协议（auto 归 openai）。
func LoginProtocolForProviderTemplate(template modelcard.ProviderTemplate, hasTemplate bool, fallbackLoginProtocol, authMode string) string {
	if hasTemplate {
		protocol := strings.ToLower(strings.TrimSpace(template.Protocol))
		if protocol == "codex" {
			if NormalizeProviderAuthMode(authMode) == authModeOAuth && strings.EqualFold(fallbackLoginProtocol, "codex-oauth") {
				return "codex-oauth"
			}
			return "codex-apikey"
		}
		if protocol != "" {
			return protocol
		}
	}
	if IsAutoLoginProtocol(fallbackLoginProtocol) {
		return "openai"
	}
	return NormalizeLoginProtocol(fallbackLoginProtocol, authMode)
}

func providerLoginModelGroupKey(runtimeProtocol string, template modelcard.ProviderTemplate, hasTemplate bool) string {
	if hasTemplate && strings.TrimSpace(template.ID) != "" {
		return strings.ToLower(strings.TrimSpace(template.ID))
	}
	runtimeProtocol = strings.ToLower(strings.TrimSpace(runtimeProtocol))
	if runtimeProtocol == "" {
		return "openai"
	}
	return runtimeProtocol
}

// SelectPrimaryProviderLoginModelGroup 选出与请求协议一致的主分组：显式
// 协议优先按 template ID 精确匹配，其次按运行时协议；auto 时选模型数最多、
// 协议优先级最高的组。
func SelectPrimaryProviderLoginModelGroup(groups []LoginModelGroup, requestedLoginProtocol string, provider config.Provider, catalog *modelcard.Catalog) int {
	if len(groups) == 0 {
		return -1
	}
	if !IsAutoLoginProtocol(requestedLoginProtocol) {
		runtimeProtocol := RuntimeProtocolForLoginProtocol(requestedLoginProtocol)
		if template, ok := ResolveProviderTemplate(catalog, runtimeProtocol, provider); ok {
			for i, group := range groups {
				if group.HasTemplate && sameProviderLoginTemplateID(group.ProviderTemplate, template) {
					return i
				}
			}
		}
		for i, group := range groups {
			if strings.EqualFold(group.RuntimeProtocol, runtimeProtocol) {
				return i
			}
		}
	}
	best := 0
	for i := 1; i < len(groups); i++ {
		if len(groups[i].Models) > len(groups[best].Models) {
			best = i
			continue
		}
		if len(groups[i].Models) == len(groups[best].Models) && providerLoginProtocolRank(groups[i].RuntimeProtocol) < providerLoginProtocolRank(groups[best].RuntimeProtocol) {
			best = i
		}
	}
	return best
}

// selectPrimaryProviderLoginModelGroup 是包内既有点名的兼容包装。
func selectPrimaryProviderLoginModelGroup(groups []LoginModelGroup, requestedLoginProtocol string, provider config.Provider, catalog *modelcard.Catalog) int {
	return SelectPrimaryProviderLoginModelGroup(groups, requestedLoginProtocol, provider, catalog)
}

func providerLoginProtocolRank(protocol string) int {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return 0
	case LoginProtocolOpenAIImage:
		return 1
	case "codex":
		return 2
	case "anthropic":
		return 3
	case "gemini":
		return 4
	default:
		return 10
	}
}
