package commands

import (
	"fmt"
	"os"
	"strings"
	"sync"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// skillFileLoader 抽象单个技能文件的加载入口，便于测试注入计数实现；
// *runtimeskill.Loader 天然满足该接口。
type skillFileLoader interface {
	LoadFileFull(filePath string) (*runtimeskill.Skill, error)
}

// skillSourceResolver 缓存技能源文件的解析结果，并以文件版本键（mtime + size）
// 判定失效。
//
// 背景：`/skill` 回合解析（SkillFunction.resolvedTurnSkill）与 skill 函数执行
// （SkillFunction.Execute）都会调用 resolver；此前每次调用都会
// Loader.LoadFileFull 重新读盘 + 解析，一个回合最多重复两次。这里在保持
// "编辑技能文件即时生效" 语义的前提下复用解析结果：
//   - mtime/size 未变：返回缓存定义（浅拷贝，隔离调用方对字段的改写）；
//   - mtime/size 变化：重新加载并覆盖缓存；
//   - stat 失败或 loader 缺失：退化为直读 / 返回原始 stub，与旧行为一致。
type skillSourceResolver struct {
	loader      skillFileLoader
	skillRef    *runtimeskill.Skill
	sourcePath  string
	sourceDir   string
	sourceLayer string
	promptPath  string

	mu         sync.Mutex
	cached     *runtimeskill.Skill
	cachedMod  int64
	cachedSize int64
}

func newSkillSourceResolver(loader skillFileLoader, skillRef *runtimeskill.Skill, sourcePath, sourceDir, sourceLayer, promptPath string) *skillSourceResolver {
	return &skillSourceResolver{
		loader:      loader,
		skillRef:    skillRef,
		sourcePath:  strings.TrimSpace(sourcePath),
		sourceDir:   strings.TrimSpace(sourceDir),
		sourceLayer: strings.TrimSpace(sourceLayer),
		promptPath:  strings.TrimSpace(promptPath),
	}
}

// resolve 返回技能定义；缓存命中返回浅拷贝，调用方改写单字段不会污染缓存。
func (r *skillSourceResolver) resolve() (*runtimeskill.Skill, error) {
	if r == nil {
		return nil, fmt.Errorf("skill source resolver is nil")
	}
	if r.loader == nil {
		return r.skillRef, nil
	}
	mod, size, statErr := skillSourceStamp(r.sourcePath)

	r.mu.Lock()
	defer r.mu.Unlock()

	if statErr == nil && r.cached != nil && mod == r.cachedMod && size == r.cachedSize {
		return cloneSkillShallow(r.cached), nil
	}

	loaded, err := r.loader.LoadFileFull(r.sourcePath)
	if err != nil {
		return nil, err
	}
	if loaded != nil {
		loaded.SetSource(r.sourcePath, r.sourceDir, r.sourceLayer)
		if r.promptPath != "" {
			loaded.SetPromptSource(r.promptPath)
		}
		if loaded.Handler == nil && r.skillRef != nil && r.skillRef.Handler != nil {
			loaded.Handler = r.skillRef.Handler
		}
		if len(loaded.Tools) == 0 && r.skillRef != nil && len(r.skillRef.Tools) > 0 {
			loaded.Tools = append([]string(nil), r.skillRef.Tools...)
		}
	}
	if statErr == nil {
		r.cached = loaded
		r.cachedMod = mod
		r.cachedSize = size
	}
	return cloneSkillShallow(loaded), nil
}

// skillSourceStamp 返回文件版本键；路径为空或文件不可 stat 时返回 error。
func skillSourceStamp(path string) (int64, int64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, 0, fmt.Errorf("skill source path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	return info.ModTime().UnixNano(), info.Size(), nil
}

// cloneSkillShallow 复制 Skill 值本身及其 Source，解析后的字段整体替换语义
// （Handler/Tools/Source/Prompt 等）下足以隔离调用方改写。
func cloneSkillShallow(src *runtimeskill.Skill) *runtimeskill.Skill {
	if src == nil {
		return nil
	}
	clone := *src
	if src.Source != nil {
		sourceCopy := *src.Source
		clone.Source = &sourceCopy
	}
	return &clone
}
