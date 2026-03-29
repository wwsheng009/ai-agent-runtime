package skill

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type hydratedSkillFileStamp struct {
	Path    string
	Exists  bool
	ModTime int64
	Size    int64
}

type hydratedSkillCacheEntry struct {
	manifest hydratedSkillFileStamp
	prompt   hydratedSkillFileStamp
	skill    *Skill
}

var hydratedSkillCache = struct {
	mu    sync.RWMutex
	items map[string]*hydratedSkillCacheEntry
}{
	items: make(map[string]*hydratedSkillCacheEntry),
}

// HydrateSkill loads a full skill definition when the given skill is only a discovery stub.
func HydrateSkill(item *Skill) (*Skill, error) {
	return HydrateSkillWithRegistry(item, nil)
}

// HydrateSkillWithRegistry loads a full skill definition and prefers registry-level loaded cache when available.
func HydrateSkillWithRegistry(item *Skill, registry *Registry) (*Skill, error) {
	if item == nil || item.Source == nil || !item.Source.DiscoveryOnly || strings.TrimSpace(item.Source.Path) == "" {
		return item, nil
	}

	manifestPath := filepath.Clean(strings.TrimSpace(item.Source.Path))
	manifestStamp, err := statHydratedSkillFile(manifestPath)
	if err != nil {
		return nil, err
	}

	promptPath := strings.TrimSpace(item.Source.PromptPath)
	if promptPath == "" {
		promptPath = discoverPromptPath(filepath.Dir(manifestPath))
	}
	promptStamp, err := statHydratedSkillFile(promptPath)
	if err != nil {
		return nil, err
	}

	if registry != nil {
		if cached := registry.getLoadedSkill(item.Name, manifestStamp, promptStamp); cached != nil {
			return mergeHydratedSkillMetadata(cloneSkill(cached), item), nil
		}
	} else {
		if cached := getHydratedSkillCacheEntry(manifestPath, manifestStamp, promptStamp); cached != nil {
			return mergeHydratedSkillMetadata(cloneSkill(cached), item), nil
		}
	}

	parser := NewManifestParser()
	parser.SetCompanionPromptLoadMode(CompanionPromptLoadEager)
	loaded, err := parser.ParseFile(manifestPath)
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		return item, nil
	}

	loaded = mergeHydratedSkillMetadata(loaded, item)
	if registry != nil {
		registry.putLoadedSkill(item.Name, manifestStamp, promptStamp, loaded)
	} else {
		putHydratedSkillCacheEntry(manifestPath, manifestStamp, promptStamp, loaded)
	}
	return cloneSkill(loaded), nil
}

// InvalidateHydratedSkill removes a single skill from the hydration cache.
func InvalidateHydratedSkill(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	hydratedSkillCache.mu.Lock()
	delete(hydratedSkillCache.items, filepath.Clean(path))
	hydratedSkillCache.mu.Unlock()
}

// InvalidateAllHydratedSkills clears the hydration cache.
func InvalidateAllHydratedSkills() {
	hydratedSkillCache.mu.Lock()
	hydratedSkillCache.items = make(map[string]*hydratedSkillCacheEntry)
	hydratedSkillCache.mu.Unlock()
}

func statHydratedSkillFile(path string) (hydratedSkillFileStamp, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return hydratedSkillFileStamp{}, nil
	}
	cleaned := filepath.Clean(path)
	info, err := os.Stat(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return hydratedSkillFileStamp{Path: cleaned, Exists: false}, nil
		}
		return hydratedSkillFileStamp{}, err
	}
	return hydratedSkillFileStamp{
		Path:    cleaned,
		Exists:  true,
		ModTime: info.ModTime().UnixNano(),
		Size:    info.Size(),
	}, nil
}

func getHydratedSkillCacheEntry(key string, manifest hydratedSkillFileStamp, prompt hydratedSkillFileStamp) *Skill {
	hydratedSkillCache.mu.RLock()
	defer hydratedSkillCache.mu.RUnlock()

	entry, ok := hydratedSkillCache.items[key]
	if !ok || entry == nil || entry.skill == nil {
		return nil
	}
	if !sameHydratedSkillStamp(entry.manifest, manifest) || !sameHydratedSkillStamp(entry.prompt, prompt) {
		return nil
	}
	return entry.skill
}

func putHydratedSkillCacheEntry(key string, manifest hydratedSkillFileStamp, prompt hydratedSkillFileStamp, item *Skill) {
	if item == nil {
		return
	}
	hydratedSkillCache.mu.Lock()
	hydratedSkillCache.items[key] = &hydratedSkillCacheEntry{
		manifest: manifest,
		prompt:   prompt,
		skill:    cloneSkill(item),
	}
	hydratedSkillCache.mu.Unlock()
}

func sameHydratedSkillStamp(left, right hydratedSkillFileStamp) bool {
	return left.Path == right.Path &&
		left.Exists == right.Exists &&
		left.ModTime == right.ModTime &&
		left.Size == right.Size
}

func mergeHydratedSkillMetadata(loaded *Skill, source *Skill) *Skill {
	if loaded == nil {
		return nil
	}
	if loaded.Source == nil {
		loaded.Source = &SkillSource{}
	}
	if source != nil && source.Source != nil {
		loaded.Source.Path = firstNonEmptyString(loaded.Source.Path, source.Source.Path)
		loaded.Source.Dir = firstNonEmptyString(loaded.Source.Dir, source.Source.Dir)
		loaded.Source.Layer = firstNonEmptyString(source.Source.Layer, loaded.Source.Layer)
		loaded.Source.PromptPath = firstNonEmptyString(loaded.Source.PromptPath, source.Source.PromptPath)
	}
	loaded.Source.DiscoveryOnly = false
	if source != nil && loaded.Handler == nil {
		loaded.Handler = source.Handler
	}
	return loaded
}

func cloneSkill(item *Skill) *Skill {
	if item == nil {
		return nil
	}
	cloned := *item
	cloned.Capabilities = append([]string(nil), item.Capabilities...)
	cloned.Tags = append([]string(nil), item.Tags...)
	cloned.Triggers = cloneTriggers(item.Triggers)
	cloned.Tools = append([]string(nil), item.Tools...)
	cloned.Permissions = append([]string(nil), item.Permissions...)
	cloned.Context = ContextConfig{
		Files:       append([]string(nil), item.Context.Files...),
		Environment: append([]string(nil), item.Context.Environment...),
		Symbols:     append([]string(nil), item.Context.Symbols...),
	}
	if item.Workflow != nil {
		steps := make([]WorkflowStep, 0, len(item.Workflow.Steps))
		for _, step := range item.Workflow.Steps {
			steps = append(steps, WorkflowStep{
				ID:        step.ID,
				Name:      step.Name,
				Tool:      step.Tool,
				Args:      cloneMapInterface(step.Args),
				DependsOn: append([]string(nil), step.DependsOn...),
				Condition: step.Condition,
			})
		}
		cloned.Workflow = &Workflow{Steps: steps}
	}
	if item.Source != nil {
		sourceCopy := *item.Source
		cloned.Source = &sourceCopy
	}
	return &cloned
}

func cloneMapInterface(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = cloneInterfaceValue(value)
	}
	return output
}

func cloneInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMapInterface(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneInterfaceValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
