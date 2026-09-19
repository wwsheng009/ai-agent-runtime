package providerops

import (
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

// ---------------------------------------------------------------------------
// model card 目录加载：login / fetch-models / runtime server 共用同一目录
// 解析顺序（内置目录 → 配置内置路径 → 用户 ~/.aicli/model_cards.yaml → 请求
// 级附加目录），以及同一 strict / disable 语义。
// ---------------------------------------------------------------------------

// ModelCardWarning 是卡片目录加载警告的契约投影（runtime server 直接序列化）。
type ModelCardWarning struct {
	ProviderTemplate string `json:"provider_template,omitempty"`
	Message          string `json:"message,omitempty"`
}

// CatalogOptions 是目录加载的请求级开关：CLI login 请求可禁用目录、追加
// 临时目录或强制 strict；runtime server 走默认（全部由配置文件决定）。
type CatalogOptions struct {
	Disable     bool
	CatalogPath string
	Strict      bool
}

// LoadModelCardCatalog 按默认选项加载卡片目录（契约入口）。
func LoadModelCardCatalog(cfg *config.Config) (*modelcard.Catalog, []ModelCardWarning, error) {
	return LoadModelCardCatalogWithOptions(cfg, CatalogOptions{})
}

// LoadModelCardCatalogWithOptions 按请求级选项加载卡片目录，返回契约警告投影。
func LoadModelCardCatalogWithOptions(cfg *config.Config, opts CatalogOptions) (*modelcard.Catalog, []ModelCardWarning, error) {
	catalog, warnings, err := LoadModelCardCatalogRaw(cfg, opts)
	return catalog, ModelCardWarnings(warnings), err
}

// LoadModelCardCatalogRaw 返回 modelcard 原生警告（Source/Code/Message 全量
// 信息）。CLI 侧依赖 Code 区分警告类别（如 parse_failed），保留该入口避免
// 在契约 DTO 里丢弃信息；LoadModelCardCatalog/WithOptions 是它的契约投影。
func LoadModelCardCatalogRaw(cfg *config.Config, opts CatalogOptions) (*modelcard.Catalog, []modelcard.Warning, error) {
	if opts.Disable {
		return nil, nil, nil
	}
	modelCardsConfig := (*config.AICLIModelCardsConfig)(nil)
	if cfg != nil && cfg.AICLI != nil {
		modelCardsConfig = cfg.AICLI.ModelCards
	}
	if modelCardsConfig != nil && modelCardsConfig.Enabled != nil && !*modelCardsConfig.Enabled && strings.TrimSpace(opts.CatalogPath) == "" {
		return nil, nil, nil
	}

	strict := opts.Strict
	if modelCardsConfig != nil && modelCardsConfig.Strict {
		strict = true
	}
	sources := []modelcard.Source{modelcard.BuiltinSource()}
	if modelCardsConfig != nil && strings.TrimSpace(modelCardsConfig.BuiltinPath) != "" {
		sources = append(sources, readProviderLoginModelCardFile(modelCardsConfig.BuiltinPath))
	}
	userPath := "~/.aicli/model_cards.yaml"
	if modelCardsConfig != nil && strings.TrimSpace(modelCardsConfig.UserPath) != "" {
		userPath = modelCardsConfig.UserPath
	}
	if source, ok := readExistingProviderLoginModelCardFile(userPath); ok {
		sources = append(sources, source)
	}
	if strings.TrimSpace(opts.CatalogPath) != "" {
		sources = append(sources, readProviderLoginModelCardFile(opts.CatalogPath))
	}

	return modelcard.LoadSources(sources, strict)
}

// loadModelCardCatalog 是包内既有点名的内部入口（classify / metadata 匹配使用）。
func loadModelCardCatalog(opts CatalogOptions, cfg *config.Config) (*modelcard.Catalog, []ModelCardWarning, error) {
	return LoadModelCardCatalogWithOptions(cfg, opts)
}

func readProviderLoginModelCardFile(path string) modelcard.Source {
	resolved := resolveProviderLoginModelCardPath(path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return modelcard.Source{Name: resolved, Err: err}
	}
	return modelcard.Source{Name: resolved, Data: data}
}

func readExistingProviderLoginModelCardFile(path string) (modelcard.Source, bool) {
	resolved := resolveProviderLoginModelCardPath(path)
	if info, err := os.Stat(resolved); err != nil || info.IsDir() {
		return modelcard.Source{}, false
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return modelcard.Source{Name: resolved, Err: err}, true
	}
	return modelcard.Source{Name: resolved, Data: data}, true
}

// ModelCardWarnings 把 modelcard 原生警告投影为契约 DTO：Source（来源文件或
// builtin）放进 ProviderTemplate 字段，Code 信息并入 Message 前缀之外按契约
// 仅保留 message；需要 Code 的调用方应使用 LoadModelCardCatalogRaw。
func ModelCardWarnings(input []modelcard.Warning) []ModelCardWarning {
	if len(input) == 0 {
		return nil
	}
	output := make([]ModelCardWarning, 0, len(input))
	for _, warning := range input {
		output = append(output, ModelCardWarning{
			ProviderTemplate: strings.TrimSpace(warning.Source),
			Message:          strings.TrimSpace(warning.Message),
		})
	}
	return output
}
