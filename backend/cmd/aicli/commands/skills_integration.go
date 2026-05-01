package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	skillFunctionPrefix   = "skill__"
	maxSkillFunctionName  = 64
	maxSkillHistoryWindow = 12
	defaultSkillExposureK = 5
	skillExposureAuto     = "auto"
	skillExposurePrefer   = "prefer"
	skillExposureOnly     = "only"
)

type skillExecutor interface {
	Execute(ctx context.Context, skill *runtimeskill.Skill, req *runtimetypes.Request) (*runtimeskill.ExecuteResult, error)
}

type skillsRuntimeBinding struct {
	manager              *runtimebootstrap.Manager
	count                int
	exposureTopK         int
	exposureMode         string
	exposureRouter       *runtimeskill.Router
	catalog              *aicliFunctionCatalog
	skillFunctions       map[string]*SkillFunction
	skillFunctionsByPath map[string]*SkillFunction
	skillNameCounts      map[string]int
}

type skillExposureCandidate struct {
	FunctionName string  `json:"function_name"`
	SkillName    string  `json:"skill_name"`
	Score        float64 `json:"score"`
	MatchedBy    string  `json:"matched_by,omitempty"`
	Details      string  `json:"details,omitempty"`
}

type skillExposureDetails struct {
	Mode             string                   `json:"mode,omitempty"`
	TopK             int                      `json:"top_k"`
	RoutingPrompt    string                   `json:"routing_prompt,omitempty"`
	ExplicitMentions []string                 `json:"explicit_mentions,omitempty"`
	PreviouslyCalled []string                 `json:"previously_called,omitempty"`
	Candidates       []skillExposureCandidate `json:"candidates,omitempty"`
	ExposedFunctions []string                 `json:"exposed_functions,omitempty"`
}

type aicliFunctionExposureReport struct {
	Prompt             string                    `json:"prompt,omitempty"`
	CatalogStats       aicliFunctionCatalogStats `json:"catalog_stats"`
	Mode               string                    `json:"mode,omitempty"`
	IncludeBuiltin     bool                      `json:"include_builtin"`
	BuiltinFunctions   []string                  `json:"builtin_functions,omitempty"`
	SkillFunctions     []string                  `json:"skill_functions,omitempty"`
	FinalFunctionNames []string                  `json:"final_function_names,omitempty"`
	TopK               int                       `json:"top_k"`
	RoutingPrompt      string                    `json:"routing_prompt,omitempty"`
	ExplicitMentions   []string                  `json:"explicit_mentions,omitempty"`
	PreviouslyCalled   []string                  `json:"previously_called,omitempty"`
	Candidates         []skillExposureCandidate  `json:"candidates,omitempty"`
	RoutedSkills       []string                  `json:"routed_skills,omitempty"`
}

func (b *skillsRuntimeBinding) Count() int {
	if b == nil {
		return 0
	}
	return b.count
}

func (b *skillsRuntimeBinding) Close() error {
	if b == nil || b.manager == nil {
		return nil
	}
	return b.manager.Stop()
}

func (b *skillsRuntimeBinding) ResolveExposedSkillFunctions(session *ChatSession, prompt string) map[string]struct{} {
	exposed, _ := b.AnalyzeSkillExposure(session, prompt)
	return exposed
}

func (b *skillsRuntimeBinding) AnalyzeSkillExposure(session *ChatSession, prompt string) (map[string]struct{}, *skillExposureDetails) {
	if b == nil || len(b.skillFunctions) == 0 {
		return nil, nil
	}

	exposed := make(map[string]struct{})
	details := &skillExposureDetails{
		Mode: normalizeSkillExposureMode(b.exposureMode),
		TopK: resolveSkillExposureTopK(b.exposureTopK),
	}
	addFunction := func(name string) {
		if _, ok := b.skillFunctions[name]; ok {
			exposed[name] = struct{}{}
		}
	}

	explicitMentions := b.findExplicitSkillMentions(prompt)
	details.ExplicitMentions = append(details.ExplicitMentions, explicitMentions...)
	for _, name := range explicitMentions {
		addFunction(name)
	}

	routingPrompt := strings.TrimSpace(prompt)
	if routingPrompt == "" && session != nil {
		routingPrompt = deriveRoutingPrompt(cloneRuntimeMessages(session.Messages))
	}
	details.RoutingPrompt = routingPrompt
	if routingPrompt != "" && b.exposureRouter != nil {
		for _, result := range b.exposureRouter.Route(context.Background(), routingPrompt) {
			if result == nil || result.Skill == nil {
				continue
			}
			functionName := b.functionNameForSkill(result.Skill)
			details.Candidates = append(details.Candidates, skillExposureCandidate{
				FunctionName: functionName,
				SkillName:    result.Skill.Name,
				Score:        result.Score,
				MatchedBy:    result.MatchedBy,
				Details:      result.Details,
			})
			addFunction(functionName)
		}
	}

	if session != nil {
		previouslyCalled := extractPreviouslyCalledSkillFunctions(cloneRuntimeMessages(session.Messages))
		for name := range previouslyCalled {
			details.PreviouslyCalled = append(details.PreviouslyCalled, name)
			addFunction(name)
		}
		sort.Strings(details.PreviouslyCalled)
	}

	for name := range exposed {
		details.ExposedFunctions = append(details.ExposedFunctions, name)
	}
	sort.Strings(details.ExposedFunctions)

	if details.Mode == "" {
		details.Mode = skillExposureAuto
	}

	return exposed, details
}

func (b *skillsRuntimeBinding) findExplicitSkillMentions(prompt string) []string {
	if b == nil || len(b.skillFunctions) == 0 {
		return nil
	}

	normalizedPrompt := strings.ToLower(strings.TrimSpace(prompt))
	if normalizedPrompt == "" {
		return nil
	}

	matches := make([]string, 0)
	for functionName, fn := range b.skillFunctions {
		if strings.Contains(normalizedPrompt, strings.ToLower(functionName)) {
			matches = append(matches, functionName)
			continue
		}
		skillName := ""
		if fn != nil {
			if fn.summary != nil {
				skillName = strings.TrimSpace(fn.summary.Name)
			} else if fn.skill != nil {
				skillName = strings.TrimSpace(fn.skill.Name)
			}
		}
		if skillName != "" && strings.Contains(normalizedPrompt, strings.ToLower(skillName)) {
			matches = append(matches, functionName)
		}
	}
	sort.Strings(matches)
	return uniqueStrings(matches)
}

func (b *skillsRuntimeBinding) functionNameForSkill(skill *runtimeskill.Skill) string {
	if b == nil || skill == nil {
		return ""
	}

	if sourcePath := skillSourcePath(skill); sourcePath != "" {
		if fn := b.skillFunctionByPath(sourcePath); fn != nil {
			return fn.Name()
		}
	}

	skillName := strings.TrimSpace(skill.Name)
	if skillName == "" {
		return ""
	}
	if fn := b.skillFunctionByName(skillName); fn != nil {
		return fn.Name()
	}
	return buildSkillFunctionName(skillName)
}

func (b *skillsRuntimeBinding) skillFunctionByPath(path string) *SkillFunction {
	if b == nil {
		return nil
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return nil
	}
	if fn, ok := b.skillFunctionsByPath[path]; ok {
		return fn
	}
	return nil
}

func (b *skillsRuntimeBinding) skillFunctionByName(skillName string) *SkillFunction {
	if b == nil {
		return nil
	}
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return nil
	}

	var candidate *SkillFunction
	for _, fn := range b.skillFunctions {
		if fn == nil {
			continue
		}
		currentName := ""
		if fn.summary != nil {
			currentName = strings.TrimSpace(fn.summary.Name)
		} else if fn.skill != nil {
			currentName = strings.TrimSpace(fn.skill.Name)
		}
		if !strings.EqualFold(currentName, skillName) {
			continue
		}
		if candidate != nil {
			return nil
		}
		candidate = fn
	}
	return candidate
}

func skillSourcePath(skill *runtimeskill.Skill) string {
	if skill == nil || skill.Source == nil {
		return ""
	}
	path := filepath.Clean(strings.TrimSpace(skill.Source.Path))
	if path == "" || path == "." {
		return ""
	}
	return path
}

type SkillFunction struct {
	summary          *runtimeskill.SkillSummary
	functionName     string
	sourcePath       string
	skill            *runtimeskill.Skill
	skillResolver    func() (*runtimeskill.Skill, error)
	executor         skillExecutor
	historyProvider  func() []runtimetypes.Message
	contextProvider  func() map[string]interface{}
	metadataProvider func() runtimetypes.Metadata
	schema           map[string]interface{}
}

func (f *SkillFunction) Name() string {
	return f.functionName
}

func (f *SkillFunction) Description() string {
	skillName := ""
	description := ""
	category := ""
	var capabilities []string
	var tags []string
	var tools []string

	if f.summary != nil {
		skillName = strings.TrimSpace(f.summary.Name)
		description = strings.TrimSpace(f.summary.Description)
		category = strings.TrimSpace(f.summary.Category)
		capabilities = append([]string(nil), f.summary.Capabilities...)
		tags = append([]string(nil), f.summary.Tags...)
		tools = append([]string(nil), f.summary.Tools...)
	} else if f.skill != nil {
		skillName = strings.TrimSpace(f.skill.Name)
		description = strings.TrimSpace(f.skill.Description)
		category = strings.TrimSpace(f.skill.Category)
		capabilities = append([]string(nil), f.skill.Capabilities...)
		tags = append([]string(nil), f.skill.Tags...)
		tools = append([]string(nil), f.skill.Tools...)
	}

	if skillName == "" {
		return "Execute an AI skill"
	}

	parts := []string{
		fmt.Sprintf("Execute skill %q.", skillName),
	}
	if description != "" {
		parts = append(parts, description)
	}
	if category != "" {
		parts = append(parts, "Category: "+category+".")
	}
	if len(capabilities) > 0 {
		parts = append(parts, "Capabilities: "+strings.Join(capabilities, ", ")+".")
	}
	if len(tags) > 0 {
		parts = append(parts, "Tags: "+strings.Join(tags, ", ")+".")
	}
	if len(tools) > 0 {
		parts = append(parts, "Backed by tools: "+strings.Join(tools, ", ")+".")
	}

	return strings.Join(parts, " ")
}

func (f *SkillFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Natural-language instruction to execute with this skill.",
			},
			"context": map[string]interface{}{
				"type":        "object",
				"description": "Optional structured context for the skill execution.",
			},
			"options": map[string]interface{}{
				"type":        "object",
				"description": "Optional execution options passed to the skill runtime.",
			},
		},
		"required": []string{"prompt"},
	}
}

func (f *SkillFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	if f.executor == nil {
		return "", fmt.Errorf("skill executor is not configured")
	}
	skillItem := f.skill
	if f.skillResolver != nil {
		resolved, err := f.skillResolver()
		if err != nil {
			return "", err
		}
		if resolved != nil {
			skillItem = resolved
		}
	}
	if skillItem == nil {
		return "", fmt.Errorf("skill is not configured")
	}

	prompt := resolveSkillPrompt(args)
	if prompt == "" {
		return "", fmt.Errorf("prompt 参数不能为空")
	}

	req := runtimetypes.NewRequest(prompt)
	req.Context = mergeSkillContextMaps(f.context(), extractMapArg(args, "context"))
	req.Options = extractMapArg(args, "options")
	req.History = trimRuntimeHistory(f.history())
	req.Metadata = f.metadata()

	result, err := f.executor.Execute(ctx, skillItem, req)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}

	if result.Success && result.Error == "" && strings.TrimSpace(result.Output) != "" {
		return result.Output, nil
	}

	if !result.Success || result.Error != "" || len(result.Observations) > 0 || result.Usage != nil {
		payload := map[string]interface{}{
			"skill":   result.SkillName,
			"success": result.Success,
			"output":  result.Output,
		}
		if result.Error != "" {
			payload["error"] = result.Error
		}
		if len(result.Observations) > 0 {
			payload["observations"] = result.Observations
		}
		if result.Usage != nil {
			payload["usage"] = result.Usage
		}
		data, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return result.Output, nil
		}
		return string(data), nil
	}

	return result.Output, nil
}

func (f *SkillFunction) Schema() map[string]interface{} {
	if f == nil {
		return nil
	}
	if len(f.schema) > 0 {
		return cloneFunctionSchema(f.schema)
	}
	return map[string]interface{}{
		"name":        f.Name(),
		"description": f.Description(),
		"parameters":  f.Parameters(),
	}
}

func (f *SkillFunction) history() []runtimetypes.Message {
	if f.historyProvider == nil {
		return nil
	}
	return f.historyProvider()
}

func (f *SkillFunction) context() map[string]interface{} {
	if f.contextProvider == nil {
		return nil
	}
	return cloneSkillContextMap(f.contextProvider())
}

func (f *SkillFunction) metadata() runtimetypes.Metadata {
	if f.metadataProvider == nil {
		return runtimetypes.NewMetadata()
	}
	metadata := f.metadataProvider()
	if metadata == nil {
		return runtimetypes.NewMetadata()
	}
	return metadata.Clone()
}

func initSkillFunctions(cfg *config.Config, session *ChatSession, toolManager *runtimetools.Manager, cliSkillDirs []string, cliSkillsTopK int, cliSkillsMode string) (*skillsRuntimeBinding, error) {
	catalog := ensureFunctionCatalog(session)
	if cfg == nil || session == nil || catalog == nil || catalog.Registry() == nil {
		return nil, nil
	}
	if cfg.SkillsRuntime == nil || !cfg.SkillsRuntime.Enabled {
		return nil, nil
	}

	resolvedSkillDirs := resolveChatSkillDirs(cfg, session, cliSkillDirs)
	if len(resolvedSkillDirs) == 0 {
		return nil, nil
	}

	runtimeManager := runtimecfg.NewRuntimeManager(resolveChatRuntimeConfigPath(cfg, session))
	if err := runtimeManager.Load(); err != nil {
		return nil, fmt.Errorf("加载 skills runtime 配置失败: %w", err)
	}

	runtimeConfig := runtimeManager.Get()
	runtimeConfig.HotReload.Enabled = false
	if session.Model != "" {
		runtimeConfig.Agent.DefaultModel = session.Model
	}

	var mcpRuntime runtimeskill.MCPManager
	if toolManager != nil {
		mcpRuntime = runtimetools.NewAgentAdapter(toolManager)
	} else if MCPManagerInstance != nil {
		mcpRuntime = runtimeskill.NewMCPAdapter(MCPManagerInstance)
	}

	manager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config:          runtimeConfig,
		SkillDir:        resolvedSkillDirs[0],
		SkillDirs:       resolvedSkillDirs,
		DiscoverOnly:    true,
		MCPManager:      mcpRuntime,
		ProviderConfigs: buildSkillsProviderConfigs(cfg),
	})
	if err != nil {
		return nil, fmt.Errorf("初始化 skills runtime 失败: %w", err)
	}

	if err := bindSessionModelAlias(manager, session); err != nil {
		_ = manager.Stop()
		return nil, err
	}

	summaries := manager.Registry().ListSummaries()
	if len(summaries) == 0 {
		_ = manager.Stop()
		return nil, nil
	}

	sort.Slice(summaries, func(i, j int) bool {
		leftPath := ""
		rightPath := ""
		if summaries[i].Source != nil {
			leftPath = strings.TrimSpace(summaries[i].Source.Path)
		}
		if summaries[j].Source != nil {
			rightPath = strings.TrimSpace(summaries[j].Source.Path)
		}
		if summaries[i].Name == summaries[j].Name {
			return leftPath < rightPath
		}
		return summaries[i].Name < summaries[j].Name
	})

	executor := runtimeskill.NewExecutor(manager.Registry(), mcpRuntime, manager.LLMRuntime())
	skillNameCounts := make(map[string]int, len(summaries))
	for _, summaryItem := range summaries {
		if summaryItem == nil {
			continue
		}
		skillNameCounts[strings.ToLower(strings.TrimSpace(summaryItem.Name))]++
	}
	skillFunctions := make(map[string]*SkillFunction, len(summaries))
	skillFunctionsByPath := make(map[string]*SkillFunction, len(summaries))
	for _, summaryItem := range summaries {
		if summaryItem == nil {
			continue
		}
		summaryRef := summaryItem
		skillRef := summaryRef.ToSkillStub()
		skillName := summaryRef.Name
		sourcePath := ""
		if summaryRef.Source != nil {
			sourcePath = strings.TrimSpace(summaryRef.Source.Path)
		}
		var resolver func() (*runtimeskill.Skill, error)
		if source := summaryRef.Source; source != nil && sourcePath != "" {
			sourceDir := strings.TrimSpace(source.Dir)
			sourceLayer := strings.TrimSpace(source.Layer)
			promptPath := strings.TrimSpace(source.PromptPath)
			loader := manager.Loader()
			resolver = func() (*runtimeskill.Skill, error) {
				if loader == nil {
					return skillRef, nil
				}
				loaded, err := loader.LoadFileFull(sourcePath)
				if err != nil {
					return nil, err
				}
				if loaded != nil {
					loaded.SetSource(sourcePath, sourceDir, sourceLayer)
					if promptPath != "" {
						loaded.SetPromptSource(promptPath)
					}
				}
				return loaded, nil
			}
		}
		functionName := buildSkillFunctionNameForSummary(summaryRef, skillNameCounts)
		fn := &SkillFunction{
			summary:         summaryRef,
			functionName:    functionName,
			sourcePath:      sourcePath,
			skill:           skillRef,
			skillResolver:   resolver,
			executor:        executor,
			historyProvider: func() []runtimetypes.Message { return runtimeHistorySnapshot(session) },
			contextProvider: func() map[string]interface{} { return cloneSkillContextMap(session.ProfileContext) },
			metadataProvider: func() runtimetypes.Metadata {
				return buildSkillMetadata(session, skillName, functionName, sourcePath)
			},
		}
		fn.schema = map[string]interface{}{
			"name":        fn.Name(),
			"description": fn.Description(),
			"parameters":  fn.Parameters(),
		}
		catalog.RegisterSkillFunction(fn)
		skillFunctions[fn.functionName] = fn
		if fn.sourcePath != "" {
			skillFunctionsByPath[filepath.Clean(fn.sourcePath)] = fn
		}
	}

	exposureTopK := resolveConfiguredSkillExposureTopK(cfg.SkillsRuntime, cliSkillsTopK)
	var exposureRouter *runtimeskill.Router
	if manager != nil {
		exposureRouter = runtimeskill.NewRouter(manager.Registry())
		exposureRouter.SetMaxResults(resolveSkillExposureTopK(exposureTopK))
		if embeddingRouter := manager.EmbeddingRouter(); embeddingRouter != nil {
			exposureRouter.SetEmbeddingRouter(embeddingRouter)
		}
	}

	binding := &skillsRuntimeBinding{
		manager:              manager,
		count:                len(summaries),
		exposureTopK:         exposureTopK,
		exposureMode:         resolveConfiguredSkillExposureMode(cfg.SkillsRuntime, cliSkillsMode),
		exposureRouter:       exposureRouter,
		catalog:              catalog,
		skillFunctions:       skillFunctions,
		skillFunctionsByPath: skillFunctionsByPath,
		skillNameCounts:      skillNameCounts,
	}
	catalog.SetSkillsBinding(binding)
	session.SkillsBinding = binding
	return binding, nil
}

func resolveChatRuntimeConfigPath(cfg *config.Config, session *ChatSession) string {
	if session != nil && strings.TrimSpace(session.RuntimeConfigPath) != "" {
		return strings.TrimSpace(session.RuntimeConfigPath)
	}
	return resolveGlobalRuntimeConfigPath(cfg)
}

func resolveConfiguredSkillDirs(cfg *config.SkillsRuntimeConfig, cliSkillDirs []string) []string {
	if cfg == nil {
		return nil
	}

	seen := make(map[string]struct{})
	resolved := make([]string, 0, 1+len(cfg.SkillDirs)+len(cfg.ExtraSkillDirs)+len(cliSkillDirs)+6)

	addDir := func(dir string) {
		resolvedDir := resolveExistingPathValue(dir, true)
		if resolvedDir == "" {
			return
		}
		if _, exists := seen[resolvedDir]; exists {
			return
		}
		seen[resolvedDir] = struct{}{}
		resolved = append(resolved, resolvedDir)
	}

	addDir(cfg.SkillDir)
	for _, dir := range cfg.SkillDirs {
		addDir(dir)
	}
	for _, dir := range cfg.ExtraSkillDirs {
		addDir(dir)
	}
	for _, dir := range cliSkillDirs {
		addDir(dir)
	}
	if configFile := strings.TrimSpace(cfg.ConfigFile); configFile != "" {
		if resolvedConfigFile := resolveExistingPathValue(configFile, false); resolvedConfigFile != "" {
			for _, dir := range runtimeskill.DiscoverCodexCompatibleSkillDirs(filepath.Dir(resolvedConfigFile), resolvedConfigFile) {
				addDir(dir)
			}
		}
	}

	return resolved
}

func bindSessionModelAlias(manager *runtimebootstrap.Manager, session *ChatSession) error {
	if manager == nil || manager.LLMRuntime() == nil || session == nil {
		return nil
	}
	if session.ProviderName == "" || session.Model == "" {
		return nil
	}

	knownProviders := manager.LLMRuntime().ListProviders()
	for _, providerName := range knownProviders {
		if providerName == session.ProviderName {
			return manager.LLMRuntime().RegisterProviderAlias(session.Model, session.ProviderName)
		}
	}
	return nil
}

func buildSkillsProviderConfigs(cfg *config.Config) map[string]*runtimellm.ProviderConfig {
	providerConfigs := make(map[string]*runtimellm.ProviderConfig)
	if cfg == nil {
		return providerConfigs
	}

	retryTuning := runtimellm.RetryTuningFromAgentConfig(cfg)
	retryRules := runtimellm.RetryRulesFromAgentConfig(cfg)

	for name, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}

		providerType := provider.GetType()
		if providerType == "" {
			continue
		}

		timeout := provider.Timeout
		if timeout <= 0 {
			timeout = cfg.Providers.Timeout
		}

		maxRetries := runtimellm.ProviderMaxRetriesFromAgentConfig(cfg)

		providerConfigs[name] = &runtimellm.ProviderConfig{
			Type:               providerType,
			APIKey:             provider.GetAPIKey(),
			BaseURL:            provider.BaseURL,
			APIPath:            provider.APIPath,
			Timeout:            timeout,
			MaxRetries:         maxRetries,
			RetryTuning:        retryTuning,
			RetryRules:         retryRules,
			DefaultModel:       provider.DefaultModel,
			SupportedModels:    append([]string(nil), provider.SupportedModels...),
			ModelMappings:      cloneStringMap(provider.ModelMappings),
			ModelCapabilities:  cloneProviderModelCapabilities(provider.ModelCapabilities),
			HeaderMappings:     cloneStringMap(provider.HeaderMappings),
			HeaderMappingRules: cloneHeaderMappingRules(provider.HeaderMappingRules),
			Proxy:              config.EffectiveProxyConfig(&cfg.Providers.Proxy, provider.Proxy),
			RequestsPerMinute:  provider.RequestsPerMinute,
		}
	}

	return providerConfigs
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}

	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneProviderModelCapabilities(input map[string]config.ModelCapabilitySpec) map[string]config.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]config.ModelCapabilitySpec, len(input))
	for key, value := range input {
		cloned := value
		if len(value.InputModalities) > 0 {
			cloned.InputModalities = append([]string(nil), value.InputModalities...)
		}
		output[key] = cloned
	}
	return output
}

func cloneHeaderMappingRules(input []config.HeaderMappingRule) []runtimellm.HeaderMappingRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]runtimellm.HeaderMappingRule, len(input))
	for i, r := range input {
		output[i] = runtimellm.HeaderMappingRule{
			Name:         r.Name,
			Enabled:      r.Enabled,
			Header:       r.Header,
			TargetHeader: r.TargetHeader,
			MatchType:    r.MatchType,
			Match:        r.Match,
			Value:        r.Value,
		}
	}
	return output
}

func buildSkillFunctionName(skillName string) string {
	sanitized := strings.ToLower(strings.TrimSpace(skillName))
	if sanitized == "" {
		return skillFunctionPrefix + "unnamed"
	}

	var builder strings.Builder
	lastUnderscore := false
	for _, r := range sanitized {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if valid {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}

	name := strings.Trim(builder.String(), "_-")
	if name == "" {
		name = "unnamed"
	}

	fullName := skillFunctionPrefix + name
	if len(fullName) <= maxSkillFunctionName {
		return fullName
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(skillName))
	suffix := fmt.Sprintf("_%08x", hash.Sum32())
	baseLimit := maxSkillFunctionName - len(skillFunctionPrefix) - len(suffix)
	if baseLimit < 1 {
		baseLimit = 1
	}

	return skillFunctionPrefix + name[:baseLimit] + suffix
}

func buildSkillFunctionNameForSummary(summary *runtimeskill.SkillSummary, nameCounts map[string]int) string {
	if summary == nil {
		return buildSkillFunctionName("")
	}

	skillName := strings.TrimSpace(summary.Name)
	if skillName == "" {
		return buildSkillFunctionName("")
	}
	countKey := strings.ToLower(skillName)
	if nameCounts == nil || nameCounts[countKey] <= 1 {
		return buildSkillFunctionName(skillName)
	}

	sourcePath := ""
	if summary.Source != nil {
		sourcePath = strings.TrimSpace(summary.Source.Path)
	}
	if sourcePath == "" {
		return buildSkillFunctionName(skillName)
	}

	return buildSkillFunctionNameFromIdentity(skillName, sourcePath)
}

func buildSkillFunctionNameFromIdentity(skillName, identity string) string {
	baseName := buildSkillFunctionName(skillName)
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return baseName
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(filepath.Clean(identity)))
	suffix := fmt.Sprintf("_%08x", hash.Sum32())
	baseLimit := maxSkillFunctionName - len(suffix)
	if baseLimit < 1 {
		baseLimit = 1
	}

	prefix := baseName
	if len(prefix) > baseLimit {
		prefix = prefix[:baseLimit]
	}
	return prefix + suffix
}

func resolveSkillPrompt(args map[string]interface{}) string {
	for _, key := range []string{"prompt", "request", "input", "task"} {
		if value, ok := args[key].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func extractMapArg(args map[string]interface{}, key string) map[string]interface{} {
	if args == nil {
		return map[string]interface{}{}
	}
	if value, ok := args[key].(map[string]interface{}); ok && value != nil {
		return cloneSkillContextMap(value)
	}
	return map[string]interface{}{}
}

func mergeSkillContextMaps(base, overlay map[string]interface{}) map[string]interface{} {
	if len(base) == 0 && len(overlay) == 0 {
		return map[string]interface{}{}
	}

	merged := make(map[string]interface{}, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = cloneSkillContextValue(value)
	}
	for key, value := range overlay {
		merged[key] = cloneSkillContextValue(value)
	}
	ensureProfileContextPack(merged)
	return merged
}

func cloneSkillContextMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneSkillContextValue(value)
	}
	return cloned
}

func cloneSkillContextValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneSkillContextMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneSkillContextValue(item)
		}
		return cloned
	case []string:
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	default:
		return typed
	}
}

func ensureProfileContextPack(context map[string]interface{}) {
	if len(context) == 0 {
		return
	}

	profilePack := map[string]interface{}{}
	if memoryPath, ok := context["profile_memory_path"].(string); ok && strings.TrimSpace(memoryPath) != "" {
		profilePack["memory_path"] = strings.TrimSpace(memoryPath)
	}
	if notesPath, ok := context["profile_notes_path"].(string); ok && strings.TrimSpace(notesPath) != "" {
		profilePack["notes_path"] = strings.TrimSpace(notesPath)
	}
	if resources, ok := context["profile_resources"].(map[string]interface{}); ok && len(resources) > 0 {
		profilePack["resources"] = cloneSkillContextMap(resources)
	}
	if len(profilePack) == 0 {
		return
	}

	pack, _ := context["context_pack"].(map[string]interface{})
	if pack == nil {
		pack = make(map[string]interface{})
	} else {
		pack = cloneSkillContextMap(pack)
	}
	existingProfile, _ := pack["profile"].(map[string]interface{})
	mergedProfile := cloneSkillContextMap(existingProfile)
	if mergedProfile == nil {
		mergedProfile = make(map[string]interface{})
	}
	for key, value := range profilePack {
		if _, exists := mergedProfile[key]; exists {
			continue
		}
		mergedProfile[key] = cloneSkillContextValue(value)
	}
	pack["profile"] = mergedProfile
	context["context_pack"] = pack
}

func runtimeHistorySnapshot(session *ChatSession) []runtimetypes.Message {
	if session == nil {
		return nil
	}
	if history := cloneRuntimeMessages(session.Messages); len(history) > 0 {
		return history
	}
	return nil
}

func trimRuntimeHistory(history []runtimetypes.Message) []runtimetypes.Message {
	if len(history) <= maxSkillHistoryWindow {
		return history
	}
	return append([]runtimetypes.Message(nil), history[len(history)-maxSkillHistoryWindow:]...)
}

func buildSkillMetadata(session *ChatSession, skillName, functionName, sourcePath string) runtimetypes.Metadata {
	metadata := runtimetypes.NewMetadata()
	metadata.Set("source", "aicli_chat_skill_function")
	metadata.Set("skill_name", skillName)
	// Local CLI skill execution is treated as a trusted host surface.
	metadata.Set("permissions", []string{"*"})

	if session == nil {
		return metadata
	}
	if session.ProviderName != "" {
		metadata.Set("provider", session.ProviderName)
	}
	if session.Model != "" {
		metadata.Set("model", session.Model)
	}
	if session.Stream {
		metadata.Set("chat_stream", true)
	}
	if strings.TrimSpace(functionName) != "" {
		metadata.Set("skill_function_name", functionName)
	}
	if strings.TrimSpace(sourcePath) != "" {
		metadata.Set("skill_path", filepath.Clean(strings.TrimSpace(sourcePath)))
	}

	return metadata
}

func resolveConfiguredSkillExposureTopK(cfg *config.SkillsRuntimeConfig, cliTopK int) int {
	if cliTopK > 0 {
		return cliTopK
	}
	if cfg == nil || cfg.AICLISkillExposureTopK <= 0 {
		return defaultSkillExposureK
	}
	return cfg.AICLISkillExposureTopK
}

func resolveConfiguredSkillExposureMode(cfg *config.SkillsRuntimeConfig, cliMode string) string {
	if mode := normalizeSkillExposureMode(cliMode); mode != "" {
		return mode
	}
	if cfg == nil {
		return skillExposureAuto
	}
	if mode := normalizeSkillExposureMode(cfg.AICLISkillExposureMode); mode != "" {
		return mode
	}
	return skillExposureAuto
}

func normalizeSkillExposureMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return ""
	case skillExposureAuto:
		return skillExposureAuto
	case skillExposurePrefer:
		return skillExposurePrefer
	case skillExposureOnly:
		return skillExposureOnly
	default:
		return skillExposureAuto
	}
}

func resolveSkillExposureTopK(topK int) int {
	if topK > 0 {
		return topK
	}
	return defaultSkillExposureK
}

func deriveRoutingPrompt(messages []runtimetypes.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if strings.TrimSpace(msg.Role) != "user" {
			continue
		}
		if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func extractPreviouslyCalledSkillFunctions(messages []runtimetypes.Message) map[string]struct{} {
	result := make(map[string]struct{})
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			name := strings.TrimSpace(call.Name)
			if strings.HasPrefix(name, skillFunctionPrefix) {
				result[name] = struct{}{}
			}
		}
	}
	return result
}

func analyzeRequestFunctionSchemas(session *ChatSession, prompt string) ([]map[string]interface{}, *skillExposureDetails) {
	catalog := ensureFunctionCatalog(session)
	if session == nil || catalog == nil || catalog.Registry() == nil {
		return nil, nil
	}

	selection, exposureDetails := catalog.SelectRequestFunctions(session, prompt)
	if selection == nil {
		return nil, exposureDetails
	}
	return cloneFunctionSchemas(selection.Schemas), exposureDetails
}

func buildRequestFunctionSchemas(session *ChatSession, prompt string) []map[string]interface{} {
	schemas, _ := analyzeRequestFunctionSchemas(session, prompt)
	return schemas
}

func buildFunctionExposureReport(catalog *aicliFunctionCatalog, prompt string, selection *aicliFunctionSelection, details *skillExposureDetails) *aicliFunctionExposureReport {
	if catalog == nil && selection == nil && details == nil {
		return nil
	}

	report := &aicliFunctionExposureReport{
		Prompt: strings.TrimSpace(prompt),
	}
	if catalog != nil {
		report.CatalogStats = catalog.Stats()
	}
	if selection != nil {
		report.Mode = selection.Mode
		report.IncludeBuiltin = selection.IncludeBuiltin
		report.BuiltinFunctions = append([]string(nil), selection.BuiltinFunctions...)
		report.SkillFunctions = append([]string(nil), selection.SkillFunctions...)
		report.FinalFunctionNames = append([]string(nil), selection.FinalFunctionNames...)
	}
	if details != nil {
		if report.Mode == "" {
			report.Mode = details.Mode
		}
		report.TopK = details.TopK
		report.RoutingPrompt = details.RoutingPrompt
		report.ExplicitMentions = append([]string(nil), details.ExplicitMentions...)
		report.PreviouslyCalled = append([]string(nil), details.PreviouslyCalled...)
		report.Candidates = append([]skillExposureCandidate(nil), details.Candidates...)
		report.RoutedSkills = append([]string(nil), details.ExposedFunctions...)
		if report.Prompt == "" {
			report.Prompt = details.RoutingPrompt
		}
	}
	return report
}

func buildFunctionExposureReportForPrompt(session *ChatSession, prompt string) *aicliFunctionExposureReport {
	catalog := ensureFunctionCatalog(session)
	if session == nil || catalog == nil || catalog.Registry() == nil {
		return nil
	}
	selection, details := catalog.SelectRequestFunctions(session, prompt)
	return buildFunctionExposureReport(catalog, prompt, selection, details)
}

func buildToolLoopRequestMetadataFromExposureReport(report *aicliFunctionExposureReport) map[string]interface{} {
	if report == nil {
		return nil
	}
	return runtimeskill.BuildSkillExposureMetadata(buildSkillExposureProjectionFromReport(report))
}

func buildSkillExposureProjectionFromReport(report *aicliFunctionExposureReport) runtimeskill.ExposureProjection {
	projection := runtimeskill.ExposureProjection{}
	if report == nil {
		return projection
	}

	projection.Mode = report.Mode
	projection.IncludeBuiltin = report.IncludeBuiltin
	projection.BuiltinFunctions = append([]string(nil), report.BuiltinFunctions...)
	projection.SkillFunctions = append([]string(nil), report.SkillFunctions...)
	projection.FinalFunctionNames = append([]string(nil), report.FinalFunctionNames...)
	projection.TopK = report.TopK
	projection.RoutedSkills = append([]string(nil), report.RoutedSkills...)
	projection.ExplicitMentions = append([]string(nil), report.ExplicitMentions...)
	projection.PreviouslyCalled = append([]string(nil), report.PreviouslyCalled...)
	projection.CatalogTotalFunctions = report.CatalogStats.TotalFunctions
	projection.CatalogBuiltinTools = report.CatalogStats.BuiltinTools
	projection.CatalogSkillFunctions = report.CatalogStats.SkillFunctions
	projection.BuiltinFunctionCount = len(report.BuiltinFunctions)
	projection.SkillFunctionCount = len(report.SkillFunctions)
	projection.FinalFunctionCount = len(report.FinalFunctionNames)
	projection.RoutedSkillCount = len(report.RoutedSkills)
	projection.CandidateCount = len(report.Candidates)
	if len(report.Candidates) > 0 {
		projection.Candidates = make([]runtimeskill.ExposureCandidate, 0, len(report.Candidates))
		for _, candidate := range report.Candidates {
			projection.Candidates = append(projection.Candidates, runtimeskill.ExposureCandidate{
				FunctionName: candidate.FunctionName,
				SkillName:    candidate.SkillName,
				Score:        candidate.Score,
				MatchedBy:    candidate.MatchedBy,
				Details:      candidate.Details,
			})
		}
	}

	return projection
}

func formatSkillExposureDebug(report *aicliFunctionExposureReport) string {
	if report == nil {
		return "[skills-debug] no function exposure details"
	}

	lines := make([]string, 0, 16)
	lines = append(lines, fmt.Sprintf("[skills-debug] catalog total=%d builtin=%d skills=%d",
		report.CatalogStats.TotalFunctions, report.CatalogStats.BuiltinTools, report.CatalogStats.SkillFunctions))
	if report.Mode != "" || report.IncludeBuiltin || len(report.FinalFunctionNames) > 0 {
		lines = append(lines, fmt.Sprintf("[skills-debug] request mode=%s include_builtin=%t total_exposed=%d",
			report.Mode, report.IncludeBuiltin, len(report.FinalFunctionNames)))
		if len(report.BuiltinFunctions) == 0 {
			lines = append(lines, "[skills-debug] builtin_exposed=<none>")
		} else {
			lines = append(lines, fmt.Sprintf("[skills-debug] builtin_exposed=%s", strings.Join(report.BuiltinFunctions, ", ")))
		}
		if len(report.SkillFunctions) == 0 {
			lines = append(lines, "[skills-debug] skill_exposed=<none>")
		} else {
			lines = append(lines, fmt.Sprintf("[skills-debug] skill_exposed=%s", strings.Join(report.SkillFunctions, ", ")))
		}
		if len(report.FinalFunctionNames) == 0 {
			lines = append(lines, "[skills-debug] final_functions=<none>")
		} else {
			lines = append(lines, fmt.Sprintf("[skills-debug] final_functions=%s", strings.Join(report.FinalFunctionNames, ", ")))
		}
	}
	if report.TopK <= 0 && report.RoutingPrompt == "" && len(report.ExplicitMentions) == 0 && len(report.PreviouslyCalled) == 0 && len(report.Candidates) == 0 && len(report.RoutedSkills) == 0 {
		return strings.Join(lines, "\n")
	}
	lines = append(lines, fmt.Sprintf("[skills-debug] route mode=%s top_k=%d", report.Mode, report.TopK))
	if report.RoutingPrompt != "" {
		lines = append(lines, fmt.Sprintf("[skills-debug] routing_prompt=%q", report.RoutingPrompt))
	}
	if len(report.ExplicitMentions) > 0 {
		lines = append(lines, fmt.Sprintf("[skills-debug] explicit=%s", strings.Join(report.ExplicitMentions, ", ")))
	}
	if len(report.PreviouslyCalled) > 0 {
		lines = append(lines, fmt.Sprintf("[skills-debug] previous=%s", strings.Join(report.PreviouslyCalled, ", ")))
	}
	if len(report.Candidates) == 0 {
		lines = append(lines, "[skills-debug] candidates=<none>")
	} else {
		for _, candidate := range report.Candidates {
			line := fmt.Sprintf("[skills-debug] candidate=%s skill=%s score=%.3f matched_by=%s",
				candidate.FunctionName, candidate.SkillName, candidate.Score, candidate.MatchedBy)
			if candidate.Details != "" {
				line += fmt.Sprintf(" details=%q", candidate.Details)
			}
			lines = append(lines, line)
		}
	}
	if len(report.RoutedSkills) == 0 {
		lines = append(lines, "[skills-debug] routed_skills=<none>")
	} else {
		lines = append(lines, fmt.Sprintf("[skills-debug] routed_skills=%s", strings.Join(report.RoutedSkills, ", ")))
	}
	return strings.Join(lines, "\n")
}

func (b *skillsRuntimeBinding) orderedSkillFunctionNames() []string {
	if b == nil {
		return nil
	}
	if b.catalog != nil {
		if names := b.catalog.SkillFunctionNames(); len(names) > 0 {
			return names
		}
	}
	names := make([]string, 0, len(b.skillFunctions))
	for name := range b.skillFunctions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (b *skillsRuntimeBinding) schemaForSkillFunction(name string) map[string]interface{} {
	if b == nil || name == "" {
		return nil
	}
	if b.catalog != nil {
		if schema := b.catalog.SkillSchema(name); len(schema) > 0 {
			return schema
		}
	}
	fn, ok := b.skillFunctions[name]
	if !ok || fn == nil {
		return nil
	}
	return fn.Schema()
}

func refreshBuiltinFunctionSchemas(session *ChatSession) {
	catalog := ensureFunctionCatalog(session)
	if session == nil || catalog == nil {
		return
	}
	session.BuiltinSchemas = catalog.BuiltinSchemas()
}

func ensureBuiltinFunctionSchemas(session *ChatSession) []map[string]interface{} {
	catalog := ensureFunctionCatalog(session)
	if session == nil || catalog == nil || catalog.Registry() == nil {
		return nil
	}
	if schemas := catalog.BuiltinSchemas(); len(schemas) > 0 {
		return schemas
	}
	refreshBuiltinFunctionSchemas(session)
	return catalog.BuiltinSchemas()
}

func cloneFunctionSchemas(input []map[string]interface{}) []map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	output := make([]map[string]interface{}, 0, len(input))
	for _, item := range input {
		output = append(output, cloneFunctionSchema(item))
	}
	return output
}

func cloneFunctionSchema(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
