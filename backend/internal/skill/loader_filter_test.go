package skill

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeFilterTestSkill(t *testing.T, root string, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	parser := NewManifestParser()
	if err := parser.SaveFile(&Skill{
		Name:        name,
		Description: name + " test skill",
		Triggers: []Trigger{{
			Type:   "keyword",
			Values: []string{name},
			Weight: 1,
		}},
	}, filepath.Join(dir, "skill.yaml")); err != nil {
		t.Fatalf("save skill %s: %v", name, err)
	}
}

func registeredSkillNames(t *testing.T, registry *Registry) []string {
	t.Helper()
	names := make([]string, 0)
	for _, item := range registry.List() {
		if item != nil {
			names = append(names, item.Name)
		}
	}
	sort.Strings(names)
	return names
}

func assertRegistered(t *testing.T, registry *Registry, want ...string) {
	t.Helper()
	got := registeredSkillNames(t, registry)
	if len(got) != len(want) {
		t.Fatalf("registered skills = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered skills = %v, want %v", got, want)
		}
	}
}

// 无假开关断言：profile 技能选择必须在加载权威点生效。
func TestLoader_SetNameFilter_SkipsFilteredSkillsOnLoad(t *testing.T) {
	root := t.TempDir()
	writeFilterTestSkill(t, root, "docs")
	writeFilterTestSkill(t, root, "danger")

	loader := NewLoader(nil)
	loader.SetNameFilter(func(name string) bool { return name != "danger" })
	registry := NewRegistry(nil)
	if err := loader.LoadAllWithRegistry([]string{root}, registry); err != nil {
		t.Fatalf("LoadAllWithRegistry: %v", err)
	}
	assertRegistered(t, registry, "docs")
}

// 发现模式（chat 启动路径使用 DiscoverOnly）同样必须生效。
func TestLoader_SetNameFilter_AppliesToDiscoveryStubs(t *testing.T) {
	root := t.TempDir()
	writeFilterTestSkill(t, root, "docs")
	writeFilterTestSkill(t, root, "danger")

	loader := NewLoader(nil)
	loader.SetNameFilter(func(name string) bool { return name != "danger" })
	registry := NewRegistry(nil)
	if err := loader.DiscoverAllWithRegistry([]string{root}, registry); err != nil {
		t.Fatalf("DiscoverAllWithRegistry: %v", err)
	}
	assertRegistered(t, registry, "docs")
}

// nil 过滤器必须保持 profile 之前的全量行为（NFR-1 零变化）。
func TestLoader_SetNameFilter_NilKeepsEverySkill(t *testing.T) {
	root := t.TempDir()
	writeFilterTestSkill(t, root, "docs")
	writeFilterTestSkill(t, root, "danger")

	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	if err := loader.DiscoverAllWithRegistry([]string{root}, registry); err != nil {
		t.Fatalf("DiscoverAllWithRegistry: %v", err)
	}
	assertRegistered(t, registry, "danger", "docs")

	loader.SetNameFilter(nil)
	cleared := NewRegistry(nil)
	if err := loader.DiscoverAllWithRegistry([]string{root}, cleared); err != nil {
		t.Fatalf("DiscoverAllWithRegistry (nil filter): %v", err)
	}
	assertRegistered(t, cleared, "danger", "docs")
}
