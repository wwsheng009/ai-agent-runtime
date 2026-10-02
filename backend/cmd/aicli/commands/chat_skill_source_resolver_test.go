package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// countingSkillFileLoader 记录 LoadFileFull 调用次数，并模拟 loader 每次返回
// 独立对象（与真实 Loader.ParseFile 行为一致）。
type countingSkillFileLoader struct {
	calls int
	skill *runtimeskill.Skill
	err   error
}

func (l *countingSkillFileLoader) LoadFileFull(string) (*runtimeskill.Skill, error) {
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	if l.skill == nil {
		return nil, nil
	}
	clone := *l.skill
	return &clone, nil
}

func TestSkillSourceResolverCachesUntilFileVersionChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o644))

	loader := &countingSkillFileLoader{skill: &runtimeskill.Skill{Name: "cached-skill", SystemPrompt: "first body"}}
	resolver := newSkillSourceResolver(loader, nil, path, dir, "user", "")

	first, err := resolver.resolve()
	require.NoError(t, err)
	require.Equal(t, "cached-skill", first.Name)

	second, err := resolver.resolve()
	require.NoError(t, err)
	require.Equal(t, "first body", second.SystemPrompt)
	require.Equal(t, 1, loader.calls, "文件版本未变化时不应重复读盘解析")

	// 调用方改写返回值（含 Source）不得污染缓存。
	second.Source.Layer = "mutated"
	third, err := resolver.resolve()
	require.NoError(t, err)
	require.Equal(t, "user", third.Source.Layer)
	require.Equal(t, 1, loader.calls)

	// 文件版本变化（mtime 前移）后必须重新加载。
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
	loader.skill = &runtimeskill.Skill{Name: "cached-skill", SystemPrompt: "second body"}
	fourth, err := resolver.resolve()
	require.NoError(t, err)
	require.Equal(t, 2, loader.calls, "文件版本变化后应重新加载")
	require.Equal(t, "second body", fourth.SystemPrompt)
}

func TestSkillSourceResolverFallsBackToStubWithoutLoader(t *testing.T) {
	stub := &runtimeskill.Skill{Name: "stub"}
	resolver := newSkillSourceResolver(nil, stub, "missing.md", "", "", "")

	got, err := resolver.resolve()
	require.NoError(t, err)
	require.Same(t, stub, got)
}

func TestSkillSourceResolverPropagatesLoadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))

	resolver := newSkillSourceResolver(&countingSkillFileLoader{err: errors.New("boom")}, nil, path, "", "", "")
	_, err := resolver.resolve()
	require.Error(t, err)
}

func TestSkillSourceResolverAppliesSourceAndPromptPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	promptPath := filepath.Join(dir, "prompt.md")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))

	resolver := newSkillSourceResolver(
		&countingSkillFileLoader{skill: &runtimeskill.Skill{Name: "resolved"}},
		nil,
		path,
		dir,
		"user",
		promptPath,
	)
	got, err := resolver.resolve()
	require.NoError(t, err)
	require.NotNil(t, got.Source)
	require.Equal(t, path, got.Source.Path)
	require.Equal(t, dir, got.Source.Dir)
	require.Equal(t, "user", got.Source.Layer)
	require.Equal(t, promptPath, got.Source.PromptPath)
	require.False(t, got.Source.DiscoveryOnly)
}
